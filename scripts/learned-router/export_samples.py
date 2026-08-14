#!/usr/bin/env python3
"""
export_samples.py — Extract training samples from opencode SQLite.

Usage:
    python scripts/learned-router/export_samples.py \
        --db <backup.db> \
        --output data/learned-router/samples.jsonl \
        --workers 8

Reads session + message + part tables, extracts user prompts with
metadata, rebuilds the FULL conversation context (bounded by
compaction epochs), and writes one JSONL line per sample.

Performance:
  - read-only SQLite connections, one per worker
  - sessions sharded across a multiprocessing Pool (no shared state)
  - per-message part summaries parsed once per worker
  - compaction boundaries locate each sample's epoch

Context reconstruction:
  - opencode compaction parts carry a tail_start_id pointing at the
    first message of a new epoch; a sample's context is every message
    since the most recent compaction before it.
  - Tool calls are replayed as OpenAI-style assistant tool_calls +
    role=tool result messages so candidates see the real scene.
  - The historical actual reply is captured as actual_reply.
"""
import argparse
import json
import os
import re
import sqlite3
import statistics
import sys
from collections import defaultdict
from multiprocessing import Pool

DEFAULT_OUTPUT_DIR = "data/learned-router"

CODE_KEYWORDS = re.compile(
    r"\b(function|class|def|import|from|const|let|var|return|if|else|for|while|"
    r"interface|struct|enum|type|func|package|module|require|export|async|await|"
    r"try|catch|throw|new|this|self)\b",
    re.IGNORECASE,
)
IMAGE_PATTERN = re.compile(r"!\[.*?\]\(.*?\)|<img|image_url|base64", re.IGNORECASE)


def detect_language(text: str) -> str:
    has_cn = bool(re.search(r"[\u4e00-\u9fff]", text))
    has_en = bool(re.search(r"[a-zA-Z]{3,}", text))
    if has_cn and has_en:
        return "mixed"
    if has_cn:
        return "chinese"
    return "english"


def sanitize_text(text: str) -> str:
    """Anonymize paths and emails. No truncation — full fidelity."""
    if not text:
        return ""
    text = re.sub(r"/home/[^\s]+", "/home/**", text)
    text = re.sub(r"/Users/[^\s]+", "/Users/**", text)
    text = re.sub(r"\S+@\S+\.\S+", "[email]", text)
    return text


def normalize_prompt(prompt: str) -> str:
    s = prompt.lower()
    return re.sub(r"[\s\W_]+", "", s)


def connect_ro(db_path: str) -> sqlite3.Connection:
    uri = f"file:{db_path}?mode=ro"
    conn = sqlite3.connect(uri, uri=True)
    conn.row_factory = sqlite3.Row
    return conn


# ---------------------------------------------------------------------------
# Worker: process a shard of sessions in one read-only connection
# ---------------------------------------------------------------------------

def _process_shard(args):
    db_path, session_ids, min_prompt, exclude_models, dedup_keep = args
    conn = connect_ro(db_path)
    try:
        return process_sessions(conn, session_ids, min_prompt, exclude_models, dedup_keep)
    finally:
        conn.close()


def process_sessions(conn, session_ids, min_prompt, exclude_models, dedup_keep):
    """Process a subset of sessions; returns list of sample dicts."""
    if not session_ids:
        return []

    # -- sessions metadata ---------------------------------------------------
    cur = conn.cursor()
    placeholders = ",".join("?" * len(session_ids))
    cur.execute(f"SELECT id, title, directory, agent FROM session WHERE id IN ({placeholders})", session_ids)
    sessions = {row["id"]: (row["title"] or "", row["directory"] or "", row["agent"] or "") for row in cur.fetchall()}

    # -- messages for these sessions (pre-sorted) ------------------------------
    cur.execute(
        f"SELECT id, session_id, time_created, data FROM message "
        f"WHERE session_id IN ({placeholders}) ORDER BY time_created",
        session_ids,
    )
    msgs_by_session = defaultdict(list)
    for row in cur.fetchall():
        try:
            d = json.loads(row["data"])
        except (json.JSONDecodeError, TypeError):
            continue
        model_ref = d.get("model")
        if isinstance(model_ref, dict):
            provider = model_ref.get("providerID", "")
            model = model_ref.get("modelID", "")
        else:
            provider = d.get("providerID", "")
            model = d.get("modelID", "")
        tokens = d.get("tokens") if isinstance(d.get("tokens"), dict) else {}
        tm = d.get("time") if isinstance(d.get("time"), dict) else {}
        msgs_by_session[row["session_id"]].append({
            "id": row["id"],
            "time_created": row["time_created"],
            "role": d.get("role", ""),
            "provider": provider,
            "model": model,
            "tokens_in": tokens.get("input") or 0,
            "tokens_out": tokens.get("output") or 0,
            "tokens_reasoning": tokens.get("reasoning") or 0,
            "cost": d.get("cost") or 0.0,
            "finish": d.get("finish") or "",
            "mode": d.get("mode") or "",
            "time_completed": tm.get("completed") or 0,
        })

    # -- part summaries for these sessions -------------------------------------
    cur.execute(
        f"SELECT session_id, message_id, data FROM part "
        f"WHERE session_id IN ({placeholders})",
        session_ids,
    )
    sums = {}
    for row in cur.fetchall():
        try:
            d = json.loads(row["data"])
        except (json.JSONDecodeError, TypeError):
            continue
        ptype = d.get("type", "")
        if ptype not in ("text", "tool", "patch", "file", "compaction", "reasoning"):
            continue
        s = sums.get(row["message_id"])
        if s is None:
            s = {"text": "", "tools": [], "files": [], "reasoning": "", "compaction": False, "tail_id": ""}
            sums[row["message_id"]] = s
        if ptype == "text":
            t = d.get("text", "")
            if t:
                s["text"] = (s["text"] + "\n" if s["text"] else "") + t
        elif ptype == "reasoning":
            t = d.get("text", "")
            if t:
                s["reasoning"] = (s["reasoning"] + "\n" if s["reasoning"] else "") + t
        elif ptype == "tool":
            st = d.get("state") or {}
            inp = st.get("input")
            cmd = ""
            if isinstance(inp, dict):
                cmd = inp.get("command") or inp.get("input") or json.dumps(inp, ensure_ascii=False)
            elif inp is not None:
                cmd = str(inp)
            output = st.get("output")
            if output is None and isinstance(st.get("metadata"), dict):
                output = st["metadata"].get("output")
            s["tools"].append({
                "call_id": d.get("callID", ""),
                "tool": d.get("tool", "tool"),
                "command": cmd,
                "output": str(output) if output is not None else "",
            })
        elif ptype in ("patch", "file"):
            for f in d.get("files", []):
                if f not in s["files"]:
                    s["files"].append(f)
        elif ptype == "compaction":
            s["compaction"] = True
            s["tail_id"] = d.get("tail_start_id", "")

    samples = []
    for sid in session_ids:
        msgs = msgs_by_session.get(sid, [])
        if not msgs:
            continue
        s_meta = sessions.get(sid, ("", "", ""))
        n = len(msgs)

        # next-assistant per position (reverse scan)
        next_asst = [None] * n
        nxt = None
        for i in range(n - 1, -1, -1):
            next_asst[i] = nxt
            if msgs[i]["role"] == "assistant":
                nxt = msgs[i]

        # compaction boundaries -> (position, tail_id)
        boundaries = []
        for i, m in enumerate(msgs):
            s = sums.get(m["id"])
            if s and s["compaction"] and s["tail_id"]:
                boundaries.append((i, s["tail_id"]))
        boundaries.sort(key=lambda x: x[0])
        # tail_id -> position
        tail_pos = {}
        for i, m in enumerate(msgs):
            tail_pos[m["id"]] = i

        # within-session dedup
        seen = defaultdict(int)
        turn = 0
        for i, m in enumerate(msgs):
            if m["role"] != "user":
                continue
            text = sums.get(m["id"], {}).get("text", "")
            if not text.strip():
                continue
            if len(text.strip()) < min_prompt:
                continue
            nxt = next_asst[i]
            if nxt is None or not nxt["model"]:
                continue
            if exclude_models and nxt["model"] in exclude_models:
                continue
            norm = normalize_prompt(text)
            if seen[norm] >= dedup_keep:
                continue
            seen[norm] += 1

            # locate epoch start
            epoch_start_pos = 0
            for bi, tail_id in boundaries:
                if bi < i:
                    epoch_start_pos = tail_pos.get(tail_id, 0)
                else:
                    break

            context = build_context(msgs, sums, epoch_start_pos, i)
            actual_model, actual_reply = find_actual_reply(msgs, sums, i)
            ctx_chars = sum(
                len(m.get("content") or "")
                + sum(len(tc["function"]["arguments"]) for tc in m.get("tool_calls", []))
                for m in context
            )
            samples.append({
                "session_id": sid,
                "session_title": s_meta[0],
                "project_dir": s_meta[1],
                "agent": s_meta[2],
                "message_id": m["id"],
                "prompt": sanitize_text(text),
                "prompt_length": len(text),
                "model_used": nxt["model"],
                "provider_used": nxt["provider"],
                "tokens_in": nxt["tokens_in"],
                "tokens_out": nxt["tokens_out"],
                "tokens_reasoning": nxt["tokens_reasoning"],
                "cost": nxt["cost"],
                "finish_reason": nxt["finish"],
                "mode": nxt["mode"],
                "has_code": bool(CODE_KEYWORDS.search(text)),
                "has_image": bool(IMAGE_PATTERN.search(text)),
                "language": detect_language(text),
                "conversation_turns": turn,
                "success": nxt["finish"] in ("stop", "tool-calls", ""),
                "context": context,
                "actual_model": actual_model,
                "actual_reply": actual_reply,
                "context_len_chars": ctx_chars,
            })
            turn += 1
    return samples


def build_context(msgs, sums, start_pos, end_pos):
    """OpenAI-style messages from msgs[start_pos:end_pos].

    Tool calls are replayed as assistant tool_calls + role=tool
    messages, with the model's original chain-of-thought attached as
    `reasoning_content` (required by DeepSeek thinking mode when
    tool-call history is present; the data comes from opencode's
    `reasoning` parts). Plain assistant turns also carry it when
    available — harmless for providers that ignore the field.
    """
    out = []
    for msg in msgs[start_pos:end_pos]:
        s = sums.get(msg["id"])
        if s is None:
            continue
        if msg["role"] == "user":
            if s["text"]:
                out.append({"role": "user", "content": sanitize_text(s["text"])})
        elif msg["role"] == "assistant":
            tools = s["tools"]
            text = s["text"]
            files = s["files"]
            reasoning = sanitize_text(s.get("reasoning", "")) or None
            if not tools:
                content = text
                if files:
                    content = (content + "\n" if content else "") + "[files changed: " + ", ".join(files) + "]"
                if content:
                    m = {"role": "assistant", "content": sanitize_text(content)}
                    if reasoning:
                        m["reasoning_content"] = reasoning
                    out.append(m)
            else:
                tool_calls = []
                for tp in tools:
                    call_id = tp["call_id"]
                    tool_calls.append({
                        "id": call_id or f"call_{len(tool_calls)}",
                        "type": "function",
                        "function": {
                            "name": tp["tool"],
                            # Standard OpenAI shape: arguments is a
                            # JSON-encoded object string. Some providers
                            # (MiniMax) reject bare-string arguments.
                            "arguments": json.dumps({"command": tp["command"]}, ensure_ascii=False),
                        },
                    })
                m = {
                    "role": "assistant",
                    "content": sanitize_text(text) or None,
                    "tool_calls": tool_calls,
                    # DeepSeek thinking mode REQUIRES the
                    # reasoning_content FIELD on every assistant
                    # message that made tool calls. Empty strings are
                    # dropped by gateways with omitempty tags, so use
                    # a non-empty placeholder when the history lacks
                    # the real CoT (opencode stores it in `reasoning`
                    # parts when available).
                    "reasoning_content": reasoning if reasoning else "[reasoning_content omitted in history]",
                }
                out.append(m)
                for tp in tools:
                    out.append({
                        "role": "tool",
                        "tool_call_id": tp["call_id"],
                        "content": sanitize_text(tp["output"]),
                    })
    return out


def find_actual_reply(msgs, sums, sample_pos):
    """(model, reply_text) of the first assistant text after sample_pos."""
    for msg in msgs[sample_pos + 1:]:
        if msg["role"] == "assistant":
            text = sums.get(msg["id"], {}).get("text", "")
            if text:
                return msg.get("model", ""), sanitize_text(text)
            continue
        if msg["role"] == "user":
            break
    return None, None


# ---------------------------------------------------------------------------
# Orchestration
# ---------------------------------------------------------------------------

def export(db_path: str, output_path: str, config: dict):
    min_prompt = config.get("min_prompt_length", 1)
    exclude_models = set(config.get("exclude_models", []))
    dedup_keep = config.get("dedup_keep", 3)
    workers = config.get("workers", max(1, min(os.cpu_count() or 4, 8)))

    print(f"Opening database (read-only): {db_path}")
    conn = connect_ro(db_path)
    cur = conn.cursor()
    cur.execute("SELECT id FROM session ORDER BY id")
    session_ids = [r["id"] for r in cur.fetchall()]
    conn.close()
    print(f"Sessions: {len(session_ids)}, workers: {workers}")

    # Shard sessions across workers
    shards = [[] for _ in range(workers)]
    for i, sid in enumerate(session_ids):
        shards[i % workers].append(sid)

    task_args = [
        (db_path, shard, min_prompt, exclude_models, dedup_keep)
        for shard in shards if shard
    ]

    all_samples = []
    if workers > 1 and len(task_args) > 1:
        with Pool(processes=len(task_args)) as pool:
            for i, samples in enumerate(pool.imap_unordered(_process_shard, task_args)):
                all_samples.extend(samples)
                print(f"  shard done ({i+1}/{len(task_args)}): {len(samples)} samples")
    else:
        for args in task_args:
            samples = _process_shard(args)
            all_samples.extend(samples)
            print(f"  shard done: {len(samples)} samples")

    print(f"Total samples: {len(all_samples)}")

    os.makedirs(os.path.dirname(output_path) or ".", exist_ok=True)
    with open(output_path, "w", encoding="utf-8") as f:
        for s in all_samples:
            f.write(json.dumps(s, ensure_ascii=False) + "\n")
    print(f"Output: {output_path}")

    models = defaultdict(int)
    ctx_lens = []
    for s in all_samples:
        models[s["model_used"]] += 1
        ctx_lens.append(s["context_len_chars"])
    if ctx_lens:
        print(f"\nContext size (chars): min={min(ctx_lens)} median={statistics.median(ctx_lens):.0f} "
              f"p90={sorted(ctx_lens)[int(len(ctx_lens)*0.9)]} max={max(ctx_lens)}")
    print("\nModel distribution:")
    for m, c in sorted(models.items(), key=lambda x: -x[1]):
        print(f"  {m}: {c} ({100*c/len(all_samples):.1f}%)")


def main():
    parser = argparse.ArgumentParser(description="Export opencode training samples with full context")
    parser.add_argument("--db", default="~/.local/share/opencode/opencode.db.bak20260807",
                        help="Path to opencode SQLite backup database")
    parser.add_argument("--output", default=None,
                        help="Output JSONL path (default: data/learned-router/samples.jsonl)")
    parser.add_argument("--min-prompt-length", type=int, default=1)
    parser.add_argument("--dedup-keep", type=int, default=3,
                        help="Keep N representatives per normalized prompt within a session")
    parser.add_argument("--workers", type=int, default=0,
                        help="Parallel workers (default: min(cpu, 8))")
    args = parser.parse_args()

    db_path = os.path.expanduser(args.db)
    if not os.path.exists(db_path):
        print(f"Error: database not found: {db_path}", file=sys.stderr)
        sys.exit(1)

    output = args.output or os.path.join(DEFAULT_OUTPUT_DIR, "samples.jsonl")
    config = {
        "min_prompt_length": args.min_prompt_length,
        "exclude_models": ["auto"],
        "dedup_keep": args.dedup_keep,
        "workers": args.workers,
    }
    export(db_path, output, config)


if __name__ == "__main__":
    main()
