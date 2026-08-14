#!/usr/bin/env python3
"""
export_samples.py — Extract training samples from opencode SQLite.

Usage:
    python scripts/learned-router/export_samples.py \
        --db ~/.local/share/opencode/opencode.db \
        --output data/learned-router/samples.jsonl

Reads session + message + part tables, extracts user prompts with
metadata (model used, tokens, cost, finish reason, agent mode, etc.)
and writes one JSONL line per sample.
"""
import argparse
import json
import os
import re
import sqlite3
import sys
from pathlib import Path

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------

DEFAULT_MAX_PROMPT_LEN = 2000
DEFAULT_MIN_PROMPT_LEN = 10
DEFAULT_OUTPUT_DIR = "data/learned-router"

# Regex patterns for feature extraction
CODE_KEYWORDS = re.compile(
    r'\b(function|class|def|import|from|const|let|var|return|if|else|for|while|'
    r'interface|struct|enum|type|func|package|module|require|export|async|await|'
    r'try|catch|throw|new|this|self)\b',
    re.IGNORECASE,
)
IMAGE_PATTERN = re.compile(r'!\[.*?\]\(.*?\)|<img|image_url|base64', re.IGNORECASE)
MULTI_LANG = re.compile(r'[\u4e00-\u9fff]+.*[a-zA-Z]+|[a-zA-Z]+.*[\u4e00-\u9fff]+')


def detect_language(text: str) -> str:
    """Detect whether prompt is English, Chinese, or mixed."""
    has_cn = bool(re.search(r'[\u4e00-\u9fff]', text))
    has_en = bool(re.search(r'[a-zA-Z]{3,}', text))
    if has_cn and has_en:
        return "mixed"
    if has_cn:
        return "chinese"
    return "english"


def sanitize_prompt(text: str, max_len: int, anonymize: bool) -> str:
    """Truncate and optionally anonymize prompt text."""
    if not text:
        return ""
    text = text[:max_len]
    if anonymize:
        # Remove home directory paths
        text = re.sub(r'/home/[^/\s]+', '/home/**', text)
        text = re.sub(r'/Users/[^/\s]+', '/Users/**', text)
        # Remove email addresses
        text = re.sub(r'\S+@\S+\.\S+', '[email]', text)
    return text


def extract_sample(session_row, message_row, part_text, turn_index):
    """Build one training sample dict from raw rows."""
    session_id, title, directory, agent = session_row
    (
        msg_id, provider, model, tokens_in, tokens_out,
        tokens_reasoning, cost, finish_reason, mode, time_created, time_completed
    ) = message_row

    prompt = part_text or ""
    prompt_len = len(prompt)
    word_count = len(prompt.split())

    has_tools = False  # will be enriched later if needed
    has_code = bool(CODE_KEYWORDS.search(prompt))
    has_image = bool(IMAGE_PATTERN.search(prompt))
    language = detect_language(prompt)

    return {
        "session_id": session_id,
        "session_title": title or "",
        "project_dir": directory or "",
        "agent": agent or "",
        "message_id": msg_id,
        "prompt": sanitize_prompt(prompt, DEFAULT_MAX_PROMPT_LEN, True),
        "prompt_length": prompt_len,
        "word_count": word_count,
        "model_used": model or "",
        "provider_used": provider or "",
        "tokens_in": tokens_in or 0,
        "tokens_out": tokens_out or 0,
        "tokens_reasoning": tokens_reasoning or 0,
        "cost": cost or 0.0,
        "finish_reason": finish_reason or "",
        "mode": mode or "",
        "has_code": has_code,
        "has_image": has_image,
        "language": language,
        "conversation_turns": turn_index,
        "latency_ms": (time_completed - time_created) if time_created and time_completed else 0,
        "success": finish_reason in ("stop", "tool-calls", ""),
    }


def export(db_path: str, output_path: str, config: dict):
    """Main export logic."""
    max_prompt = config.get("max_prompt_length", DEFAULT_MAX_PROMPT_LEN)
    min_prompt = config.get("min_prompt_length", DEFAULT_MIN_PROMPT_LEN)
    sample_rate = config.get("sample_rate", 1.0)
    exclude_models = set(config.get("exclude_models", []))

    print(f"Opening database: {db_path}")
    conn = sqlite3.connect(db_path)
    conn.row_factory = sqlite3.Row
    cur = conn.cursor()

    # Step 1: Get all user messages with their text parts
    user_query = """
    SELECT
        m.id as message_id,
        m.session_id,
        m.time_created,
        json_extract(p.data, '$.text') as text_content
    FROM message m
    JOIN part p ON p.message_id = m.id
    WHERE json_extract(m.data, '$.role') = 'user'
      AND json_extract(p.data, '$.type') = 'text'
    ORDER BY m.time_created ASC
    """
    print("Fetching user messages...")
    cur.execute(user_query)
    user_rows = cur.fetchall()
    print(f"Found {len(user_rows)} user messages")

    # Step 2: Get all assistant messages with metadata
    assistant_query = """
    SELECT
        m.id as message_id,
        m.session_id,
        m.time_created,
        json_extract(m.data, '$.providerID') as provider,
        json_extract(m.data, '$.modelID') as model,
        json_extract(m.data, '$.tokens.input') as tokens_in,
        json_extract(m.data, '$.tokens.output') as tokens_out,
        json_extract(m.data, '$.tokens.reasoning') as tokens_reasoning,
        json_extract(m.data, '$.cost') as cost,
        json_extract(m.data, '$.finish') as finish_reason,
        json_extract(m.data, '$.mode') as mode,
        json_extract(m.data, '$.time.completed') as time_completed
    FROM message m
    WHERE json_extract(m.data, '$.role') = 'assistant'
    ORDER BY m.time_created ASC
    """
    print("Fetching assistant messages...")
    cur.execute(assistant_query)
    assistant_rows = cur.fetchall()
    print(f"Found {len(assistant_rows)} assistant messages")

    # Step 3: Get session metadata
    session_query = "SELECT id, title, directory, agent FROM session"
    cur.execute(session_query)
    session_map = {}
    for row in cur.fetchall():
        session_map[row[0]] = (row[1], row[2], row[3])

    # Step 4: Build index of assistant messages by session
    from collections import defaultdict
    assistant_by_session = defaultdict(list)
    for row in assistant_rows:
        assistant_by_session[row["session_id"]].append(row)

    # Step 5: Match user messages to next assistant message
    matched = []
    for user_row in user_rows:
        sid = user_row["session_id"]
        user_time = user_row["time_created"]
        assistants = assistant_by_session.get(sid, [])
        # Find the first assistant message after this user message
        next_asst = None
        for asst in assistants:
            if asst["time_created"] > user_time:
                next_asst = asst
                break
        if next_asst:
            matched.append((user_row, next_asst))

    print(f"Matched {len(matched)} user-assistant pairs")
    rows = matched

    # Track turns per session
    session_turns = {}
    exported = 0
    skipped = 0

    os.makedirs(os.path.dirname(output_path) or ".", exist_ok=True)
    with open(output_path, "w", encoding="utf-8") as f:
        for user_row, asst_row in rows:
            session_id = user_row["session_id"]
            text = user_row["text_content"] or ""

            # Filter
            if len(text.strip()) < min_prompt:
                skipped += 1
                continue
            if exclude_models and asst_row["model"] in exclude_models:
                skipped += 1
                continue

            # Sample rate
            import random
            if sample_rate < 1.0 and random.random() > sample_rate:
                skipped += 1
                continue

            # Turn tracking
            turn_idx = session_turns.get(session_id, 0)
            session_turns[session_id] = turn_idx + 1

            # Session metadata
            s_meta = session_map.get(session_id, ("", "", ""))

            sample = extract_sample(
                session_row=(session_id, s_meta[0], s_meta[1], s_meta[2]),
                message_row=(
                    user_row["message_id"], asst_row["provider"], asst_row["model"],
                    asst_row["tokens_in"], asst_row["tokens_out"],
                    asst_row["tokens_reasoning"], asst_row["cost"],
                    asst_row["finish_reason"], asst_row["mode"],
                    user_row["time_created"], asst_row["time_completed"]
                ),
                part_text=text,
                turn_index=turn_idx,
            )
            f.write(json.dumps(sample, ensure_ascii=False) + "\n")
            exported += 1

    conn.close()
    print(f"Exported: {exported} samples, skipped: {skipped}")
    print(f"Output: {output_path}")

    # Summary stats
    models = {}
    with open(output_path, "r") as f:
        for line in f:
            s = json.loads(line)
            m = s["model_used"]
            models[m] = models.get(m, 0) + 1
    print("\nModel distribution:")
    for m, count in sorted(models.items(), key=lambda x: -x[1]):
        print(f"  {m}: {count} ({100*count/exported:.1f}%)")


def main():
    parser = argparse.ArgumentParser(description="Export opencode training samples")
    parser.add_argument("--db", default="~/.local/share/opencode/opencode.db",
                        help="Path to opencode SQLite database")
    parser.add_argument("--output", default=None,
                        help="Output JSONL path (default: data/learned-router/samples.jsonl)")
    parser.add_argument("--max-prompt-length", type=int, default=DEFAULT_MAX_PROMPT_LEN)
    parser.add_argument("--min-prompt-length", type=int, default=DEFAULT_MIN_PROMPT_LEN)
    parser.add_argument("--sample-rate", type=float, default=1.0)
    parser.add_argument("--no-anonymize", action="store_true")
    args = parser.parse_args()

    db_path = os.path.expanduser(args.db)
    if not os.path.exists(db_path):
        print(f"Error: database not found: {db_path}", file=sys.stderr)
        sys.exit(1)

    output = args.output or os.path.join(DEFAULT_OUTPUT_DIR, "samples.jsonl")
    config = {
        "max_prompt_length": args.max_prompt_length,
        "min_prompt_length": args.min_prompt_length,
        "sample_rate": args.sample_rate,
        "exclude_models": ["auto"],
    }
    export(db_path, output, config)


if __name__ == "__main__":
    main()
