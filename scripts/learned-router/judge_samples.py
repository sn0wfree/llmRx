#!/usr/bin/env python3
"""
judge_samples.py — Label training samples using LLM-as-judge.

Usage:
    python scripts/learned-router/judge_samples.py \
        --input data/learned-router/samples.jsonl \
        --output data/learned-router/labels.jsonl \
        --api-base http://localhost:8787/v1 \
        --api-key sk-xxx \
        --candidates MiniMax-M3 mimo-v2.5 deepseek-v4-flash \
        --cross-judge

Each sample carries a FULL conversation context (rebuilt from
opencode history, bounded by compaction epochs). Candidate models
receive the whole context — not just the last prompt — so they are
judged on a realistic agentic scene.

The historical actual reply (the model that really answered at the
time) is included as a pseudo-candidate named "actual", letting us
measure whether history picked the right model:
  consistent   best == model_used   (history's choice was optimal)
  history_wins best == "actual"     (history's reply is still best)
"""
import argparse
import json
import os
import sys
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from typing import Optional

# ---------------------------------------------------------------------------
# LLM Client (OpenAI-compatible)
# ---------------------------------------------------------------------------

# Per-model max output tokens (official/实测). Set per model so
# reasoning-heavy models (e.g. mimo-v2.5) don't exhaust the budget on
# chain-of-thought before emitting visible content.
MODEL_MAX_OUTPUT = {
    "MiniMax-M3": 131072,        # 1M context, ≥262K max output (实测)
    "mimo-v2.5": 131072,         # 1M context, ≤131K max output (262K rejected)
    "mimo-v2.5-pro": 131072,
    "deepseek-v4-flash": 131072, # 1M context, 384K max output (官方)
    "deepseek-v4-pro": 131072,
    "deepseek-chat": 8192,
    "deepseek-reasoner": 8192,
}

DEFAULT_MAX_OUTPUT = 32768  # fallback for unknown models

# Extra per-model request params. deepseek-v4-flash defaults to
# thinking mode with HIGH effort, which can take minutes on long
# agentic prompts; lower the effort to keep the scene intact while
# keeping response times usable for judging.
MODEL_EXTRA_PARAMS = {
    "deepseek-v4-flash": {"reasoning_effort": "low"},
    "deepseek-v4-pro": {"reasoning_effort": "low"},
}


def model_max_output(model: str) -> int:
    return MODEL_MAX_OUTPUT.get(model, DEFAULT_MAX_OUTPUT)


def call_llm(api_base: str, api_key: str, model: str, messages: list,
             max_tokens: int = None, temperature: float = 0.3,
             timeout: int = 300) -> Optional[str]:
    """Call an OpenAI-compatible chat completions endpoint.
    max_tokens defaults to the model's documented max output.
    """
    import urllib.request

    if max_tokens is None:
        max_tokens = model_max_output(model)

    body = {
        "model": model,
        "messages": messages,
        "max_tokens": max_tokens,
        "temperature": temperature,
    }
    body.update(MODEL_EXTRA_PARAMS.get(model, {}))

    url = f"{api_base.rstrip('/')}/chat/completions"
    payload = json.dumps(body).encode("utf-8")

    req = urllib.request.Request(
        url,
        data=payload,
        headers={
            "Content-Type": "application/json",
            "Authorization": f"Bearer {api_key}",
        },
        method="POST",
    )

    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            data = json.loads(resp.read().decode("utf-8"))
            if "choices" not in data:
                print(f"  [debug] {model}: no choices in response: {json.dumps(data, ensure_ascii=False)[:400]}", file=sys.stderr)
            return data["choices"][0]["message"]["content"]
    except Exception as e:
        err_body = ""
        if isinstance(e, urllib.error.HTTPError):
            err_body = e.read().decode("utf-8", "replace")[:300]
        print(f"  [warn] LLM call failed ({model}): {e} {err_body}", file=sys.stderr)
        return None


# ---------------------------------------------------------------------------
# Sample messages / candidates
# ---------------------------------------------------------------------------

def sample_messages(sample: dict) -> list:
    """The full conversation context for a sample. Falls back to the
    single prompt when no context was rebuilt."""
    ctx = sample.get("context")
    if ctx:
        return ctx
    return [{"role": "user", "content": sample.get("prompt", "")}]


def get_candidate_responses(sample: dict, api_base: str, api_key: str,
                            candidate_models: list, timeout: int = 300,
                            sample_workers: int = 3) -> dict:
    """Concurrently get a reply from every candidate model using the
    FULL conversation context."""
    messages = sample_messages(sample)
    responses = {}

    def fetch(model):
        return model, call_llm(api_base, api_key, model, messages, timeout=timeout)

    with ThreadPoolExecutor(max_workers=max(1, sample_workers)) as executor:
        futures = {executor.submit(fetch, m): m for m in candidate_models}
        for future in as_completed(futures):
            model, resp = future.result()
            if resp:
                responses[model] = resp
    return responses


def responses_with_actual(responses: dict, sample: dict) -> dict:
    """Merge the historical actual reply as pseudo-candidate 'actual'
    (only when a non-empty reply exists)."""
    out = dict(responses)
    actual = sample.get("actual_reply")
    if actual and actual.strip():
        out["actual"] = actual.strip()
    return out


# ---------------------------------------------------------------------------
# Judge prompts
# ---------------------------------------------------------------------------

JUDGE_PROMPT_TEMPLATE = """You are a quality evaluator for LLM assistant responses in a real coding-agent session.

You are given the full conversation context, followed by several candidate replies to the LAST user message. Decide which reply is best.

=== Conversation Context ===
{context_block}

=== User Prompt (last message) ===
{prompt}

=== Candidate Replies ===
{responses_block}

Rate each reply on these dimensions (1-5):
1. Correctness: Is the reply accurate and complete given the context?
2. Contextual fit: Does it correctly use the conversation history / tool outputs?
3. Efficiency: Is it concise without unnecessary filler?

Respond with ONLY a JSON object in this exact format:
{{"best_model": "<model_name or actual>", "scores": {{"<model_name or actual>": <total_score>, ...}}}}

Do not include any other text."""


def build_judge_prompt(sample: dict, responses: dict) -> str:
    ctx = sample_messages(sample)
    # Compress context for the judge (tool outputs can be huge).
    parts = []
    for m in ctx:
        role = m.get("role", "")
        if "tool_calls" in m:
            calls = m["tool_calls"]
            args = " | ".join(
                c["function"]["arguments"][:200] for c in calls
            )
            parts.append(f"[assistant tool_calls] {args[:400]}")
        elif role == "tool":
            parts.append(f"[tool result] {(m.get('content') or '')[:200]}")
        else:
            parts.append(f"[{role}] {(m.get('content') or '')[:300]}")
    context_block = "\n".join(parts[-60:])  # last 60 messages, keep it bounded

    rparts = []
    for i, (model, resp) in enumerate(responses.items(), 1):
        label = f"actual (historical reply)" if model == "actual" else model
        truncated = resp[:800] + ("..." if len(resp) > 800 else "")
        rparts.append(f"Model {i} ({label}):\n{truncated}")

    return JUDGE_PROMPT_TEMPLATE.format(
        context_block=context_block,
        prompt=sample.get("prompt", "")[:1000],
        responses_block="\n\n".join(rparts),
    )


def parse_judge_response(response: str) -> Optional[dict]:
    try:
        start = response.find("{")
        end = response.rfind("}") + 1
        if start >= 0 and end > start:
            return json.loads(response[start:end])
    except (json.JSONDecodeError, ValueError):
        pass
    return None


# ---------------------------------------------------------------------------
# Single-judge
# ---------------------------------------------------------------------------

def judge_sample(sample: dict, api_base: str, api_key: str,
                 candidate_models: list, judge_model: str,
                 timeout: int = 300, sample_workers: int = 3,
                 include_actual: bool = True) -> Optional[dict]:
    model_used = sample["model_used"]
    candidates = list(dict.fromkeys(candidate_models + [model_used]))
    responses = get_candidate_responses(
        sample, api_base, api_key, candidates, timeout, sample_workers,
    )
    if include_actual:
        responses = responses_with_actual(responses, sample)

    if len(responses) < 2:
        return {
            "prompt": sample["prompt"],
            "best_model": model_used,
            "model_used": model_used,
            "consistent": True,
            "history_wins": True,
            "scores": {model_used: 3.0},
            "judge_model": judge_model,
            "candidate_count": len(responses),
            "fallback": True,
        }

    judge_prompt = build_judge_prompt(sample, responses)
    judge_response = call_llm(
        api_base, api_key, judge_model,
        [{"role": "user", "content": judge_prompt}],
        temperature=0.1, timeout=timeout,
    )
    if not judge_response:
        return None
    parsed = parse_judge_response(judge_response)
    if not parsed or "best_model" not in parsed:
        return None

    best = parsed["best_model"]
    return {
        "prompt": sample["prompt"],
        "best_model": best,
        "model_used": model_used,
        "consistent": best == model_used,
        "history_wins": best == "actual",
        "scores": parsed.get("scores", {}),
        "judge_model": judge_model,
        "candidate_count": len(responses),
        "fallback": False,
    }


# ---------------------------------------------------------------------------
# Cross-judge
# ---------------------------------------------------------------------------

CROSS_JUDGE_PROMPT = """You are a quality evaluator for a real coding-agent session.

=== Conversation Context ===
{context_block}

=== User Prompt (last message) ===
{prompt}

Response A ({model_a}):
{response_a}

Response B ({model_b}):
{response_b}

Which reply is better given the conversation context? Consider correctness, contextual fit, and efficiency.

Respond with ONLY a JSON object:
{{"winner": "<model_name or actual>", "reason": "<brief reason>"}}

Do not include any other text."""


def judge_cross_one(sample: dict, responses: dict, judge_model: str,
                    api_base: str, api_key: str, timeout: int = 300,
                    sleep_s: float = 0.5) -> dict:
    """One model judges all pairwise comparisons."""
    votes = {}
    votes_known = set(responses.keys())
    models = list(responses.keys())
    ctx = sample_messages(sample)
    parts = []
    for m in ctx:
        role = m.get("role", "")
        if "tool_calls" in m:
            parts.append(f"[assistant tool_calls] {' | '.join(c['function']['arguments'][:200] for c in m['tool_calls'])[:400]}")
        elif role == "tool":
            parts.append(f"[tool result] {(m.get('content') or '')[:200]}")
        else:
            parts.append(f"[{role}] {(m.get('content') or '')[:300]}")
    context_block = "\n".join(parts[-60:])

    for i in range(len(models)):
        for j in range(i + 1, len(models)):
            m_a, m_b = models[i], models[j]
            label_a = "actual (historical reply)" if m_a == "actual" else m_a
            label_b = "actual (historical reply)" if m_b == "actual" else m_b
            judge_prompt = CROSS_JUDGE_PROMPT.format(
                context_block=context_block,
                prompt=sample.get("prompt", "")[:1000],
                model_a=label_a,
                response_a=responses[m_a][:600],
                model_b=label_b,
                response_b=responses[m_b][:600],
            )
            result = call_llm(
                api_base, api_key, judge_model,
                [{"role": "user", "content": judge_prompt}],
                temperature=0.1, timeout=timeout,
            )
            if result:
                parsed = parse_judge_response(result)
                if parsed and "winner" in parsed:
                    w = parsed["winner"]
                    # Only accept winners that match a known candidate
                    # (model name or "actual"); judges sometimes emit
                    # "Response A" style labels we must discard.
                    if w in votes_known:
                        votes[w] = votes.get(w, 0) + 1
            if sleep_s > 0:
                time.sleep(sleep_s)
    return votes


def judge_sample_cross(sample: dict, api_base: str, api_key: str,
                       candidate_models: list, timeout: int = 300,
                       sample_workers: int = 3, include_actual: bool = True,
                       sleep_s: float = 0.5) -> Optional[dict]:
    model_used = sample["model_used"]
    candidates = list(dict.fromkeys(candidate_models + [model_used]))
    responses = get_candidate_responses(
        sample, api_base, api_key, candidates, timeout, sample_workers,
    )
    if include_actual:
        responses = responses_with_actual(responses, sample)

    if len(responses) < 2:
        return {
            "prompt": sample["prompt"],
            "best_model": model_used,
            "model_used": model_used,
            "consistent": True,
            "history_wins": True,
            "scores": {model_used: 3.0},
            "judge_model": "cross-judge",
            "candidate_count": len(responses),
            "fallback": True,
            "votes": {},
        }

    all_votes = {}
    for judge in responses.keys():
        if judge == "actual":
            continue  # pseudo-candidate, not a real model — can't judge
        other = {m: r for m, r in responses.items() if m != judge}
        if len(other) < 2:
            continue
        votes = judge_cross_one(
            sample, other, judge, api_base, api_key, timeout, sleep_s,
        )
        for m, c in votes.items():
            all_votes[m] = all_votes.get(m, 0) + c

    if not all_votes:
        return None

    max_votes = max(all_votes.values())
    winners = [m for m, v in all_votes.items() if v == max_votes]
    best = winners[0] if len(winners) == 1 else "tie"

    return {
        "prompt": sample["prompt"],
        "best_model": best,
        "model_used": model_used,
        "consistent": best == model_used,
        "history_wins": best == "actual",
        "scores": all_votes,
        "judge_model": "cross-judge",
        "candidate_count": len(responses),
        "fallback": False,
        "votes": all_votes,
    }


# ---------------------------------------------------------------------------
# Pipeline
# ---------------------------------------------------------------------------

def run_judge(input_path: str, output_path: str, api_base: str, api_key: str,
              judge_model: str, candidate_models: list, max_samples: int = 0,
              timeout: int = 300, cross_judge: bool = False,
              sample_workers: int = 3, sleep_s: float = 0.5,
              no_actual: bool = False, resume: bool = False):
    samples = []
    with open(input_path, "r", encoding="utf-8") as f:
        for line in f:
            if line.strip():
                samples.append(json.loads(line))
    if max_samples > 0:
        samples = samples[:max_samples]

    print(f"Loaded {len(samples)} samples")
    print(f"Mode: {'cross-judge' if cross_judge else 'single-judge (' + judge_model + ')'}")
    print(f"Candidates: {candidate_models}")
    print(f"Include 'actual' pseudo-candidate: {not no_actual}")
    print(f"Sample-internal concurrency: {sample_workers}")

    # Resume support: skip samples already labeled (match by prompt hash)
    done_prompts = set()
    if resume and os.path.exists(output_path):
        with open(output_path, "r", encoding="utf-8") as f:
            for line in f:
                if line.strip():
                    try:
                        done_prompts.add(json.loads(line)["prompt"])
                    except (json.JSONDecodeError, KeyError):
                        pass
        print(f"Resume: {len(done_prompts)} already labeled, skipping")

    os.makedirs(os.path.dirname(output_path) or ".", exist_ok=True)
    out_mode = "a" if (resume and done_prompts) else "w"

    labels = []
    success = 0
    fail = 0
    ties = 0
    skipped = 0
    start_t = time.time()

    with open(output_path, out_mode, encoding="utf-8") as f:
        for i, sample in enumerate(samples):
            if sample.get("prompt") in done_prompts:
                skipped += 1
                continue
            if cross_judge:
                label = judge_sample_cross(
                    sample, api_base, api_key, candidate_models,
                    timeout, sample_workers, not no_actual, sleep_s,
                )
            else:
                label = judge_sample(
                    sample, api_base, api_key, candidate_models,
                    judge_model, timeout, sample_workers, not no_actual,
                )
            if label:
                f.write(json.dumps(label, ensure_ascii=False) + "\n")
                f.flush()
                labels.append(label)
                success += 1
                if label.get("best_model") == "tie":
                    ties += 1
            else:
                fail += 1

            if (i + 1) % 5 == 0:
                elapsed = time.time() - start_t
                print(f"  [{i+1}/{len(samples)}] success={success} fail={fail} "
                      f"ties={ties} elapsed={elapsed:.0f}s")

    print(f"\nDone: success={success}, fail={fail}, ties={ties}, skipped={skipped}")
    print(f"Output: {output_path}")

    if labels:
        models_chosen = {}
        for l in labels:
            m = l["best_model"]
            models_chosen[m] = models_chosen.get(m, 0) + 1
        print("\nBest model distribution (judge-selected):")
        for m, c in sorted(models_chosen.items(), key=lambda x: -x[1]):
            print(f"  {m}: {c} ({100*c/len(labels):.1f}%)")

        consistent = sum(1 for l in labels if l.get("consistent"))
        history_wins = sum(1 for l in labels if l.get("history_wins"))
        n = len(labels)
        print(f"\n历史一致率 (best == model_used): {consistent}/{n} = {100*consistent/n:.1f}%")
        print(f"历史胜出率 (best == actual):      {history_wins}/{n} = {100*history_wins/n:.1f}%")


def main():
    parser = argparse.ArgumentParser(description="Judge training samples with LLM")
    parser.add_argument("--input", required=True, help="Input samples.jsonl")
    parser.add_argument("--output", required=True, help="Output labels.jsonl")
    parser.add_argument("--api-base", default="http://localhost:8787/v1")
    parser.add_argument("--api-key", default="sk-placeholder")
    parser.add_argument("--judge", default="MiniMax-M3",
                        help="Judge model (ignored with --cross-judge)")
    parser.add_argument("--candidates", nargs="+",
                        default=["MiniMax-M3", "mimo-v2.5", "deepseek-v4-flash"])
    parser.add_argument("--cross-judge", action="store_true",
                        help="Each model judges the others")
    parser.add_argument("--max-samples", type=int, default=0,
                        help="Process only first N samples (0=all)")
    parser.add_argument("--timeout", type=int, default=300)
    parser.add_argument("--sample-workers", type=int, default=3,
                        help="Concurrent calls within one sample (default 3)")
    parser.add_argument("--sleep", type=float, default=0.5,
                        help="Sleep between judge calls (rate control)")
    parser.add_argument("--no-actual", action="store_true",
                        help="Exclude the historical 'actual' pseudo-candidate")
    parser.add_argument("--resume", action="store_true",
                        help="Append and skip already-labeled samples")
    args = parser.parse_args()

    run_judge(
        args.input, args.output, args.api_base, args.api_key,
        args.judge, args.candidates, args.max_samples,
        args.timeout, args.cross_judge, args.sample_workers,
        args.sleep, args.no_actual, args.resume,
    )


if __name__ == "__main__":
    main()
