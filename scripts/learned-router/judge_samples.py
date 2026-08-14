#!/usr/bin/env python3
"""
judge_samples.py — Label training samples using LLM-as-judge.

Usage:
    python scripts/learned-router/judge_samples.py \
        --input data/learned-router/samples.jsonl \
        --output data/learned-router/labels.jsonl \
        --judge deepseek-v4-flash \
        --api-base http://localhost:8080/v1 \
        --api-key sk-xxx

For each sample, sends the prompt to 2-3 candidate models, collects
responses, then asks a judge model to pick the best one. Outputs
labels.jsonl with (prompt, best_model, scores) per sample.
"""
import argparse
import json
import os
import sys
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from typing import Optional

# ---------------------------------------------------------------------------
# LLM Client (minimal, compatible with OpenAI API)
# ---------------------------------------------------------------------------

def call_llm(api_base: str, api_key: str, model: str, messages: list,
             max_tokens: int = 1024, temperature: float = 0.3,
             timeout: int = 30) -> Optional[str]:
    """Call an OpenAI-compatible chat completions endpoint."""
    import urllib.request
    import urllib.error

    url = f"{api_base.rstrip('/')}/chat/completions"
    payload = json.dumps({
        "model": model,
        "messages": messages,
        "max_tokens": max_tokens,
        "temperature": temperature,
    }).encode("utf-8")

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
            return data["choices"][0]["message"]["content"]
    except Exception as e:
        print(f"  [warn] LLM call failed ({model}): {e}", file=sys.stderr)
        return None


# ---------------------------------------------------------------------------
# Candidate selection: get responses from multiple models
# ---------------------------------------------------------------------------

def get_candidate_responses(sample: dict, api_base: str, api_key: str,
                            candidate_models: list, timeout: int = 30) -> dict:
    """Get responses from multiple candidate models for a prompt."""
    prompt = sample["prompt"]
    messages = [{"role": "user", "content": prompt}]
    responses = {}

    def fetch(model):
        resp = call_llm(api_base, api_key, model, messages, timeout=timeout)
        return model, resp

    with ThreadPoolExecutor(max_workers=len(candidate_models)) as executor:
        futures = {executor.submit(fetch, m): m for m in candidate_models}
        for future in as_completed(futures):
            model, resp = future.result()
            if resp:
                responses[model] = resp

    return responses


# ---------------------------------------------------------------------------
# Judge: pick the best response
# ---------------------------------------------------------------------------

JUDGE_PROMPT_TEMPLATE = """You are a quality evaluator for LLM responses. Given a user prompt and multiple model responses, evaluate which response is best.

User Prompt:
{prompt}

{responses_block}

Rate each response on these dimensions (1-5):
1. Correctness: Is the response accurate and complete?
2. Efficiency: Is it concise without unnecessary filler?
3. Relevance: Does it directly address the prompt?

Respond with ONLY a JSON object in this exact format:
{{"best_model": "<model_name>", "scores": {{"<model_name>": <score>, ...}}}}

Do not include any other text."""


def build_judge_prompt(prompt: str, responses: dict) -> str:
    """Build the judge evaluation prompt."""
    parts = []
    for i, (model, resp) in enumerate(responses.items(), 1):
        truncated = resp[:800] + ("..." if len(resp) > 800 else "")
        parts.append(f"Model {i} ({model}):\n{truncated}")

    responses_block = "\n\n".join(parts)
    return JUDGE_PROMPT_TEMPLATE.format(
        prompt=prompt[:1000],
        responses_block=responses_block,
    )


def parse_judge_response(response: str) -> Optional[dict]:
    """Parse judge LLM response into structured scores."""
    try:
        # Try to extract JSON from response
        start = response.find("{")
        end = response.rfind("}") + 1
        if start >= 0 and end > start:
            return json.loads(response[start:end])
    except (json.JSONDecodeError, ValueError):
        pass
    return None


# ---------------------------------------------------------------------------
# Main pipeline
# ---------------------------------------------------------------------------

def judge_sample(sample: dict, api_base: str, api_key: str,
                 candidate_models: list, judge_model: str,
                 timeout: int = 30) -> Optional[dict]:
    """Process one sample: get responses, judge, return label."""
    model_used = sample["model_used"]

    # Get responses from candidates (including the model that was actually used)
    candidates = list(set(candidate_models + [model_used]))
    responses = get_candidate_responses(sample, api_base, api_key, candidates, timeout)

    if len(responses) < 2:
        # Not enough responses to compare — use the original model as fallback
        return {
            "prompt": sample["prompt"],
            "best_model": model_used,
            "model_used": model_used,
            "scores": {model_used: 3.0},
            "judge_model": judge_model,
            "candidate_count": len(responses),
            "fallback": True,
        }

    # Ask judge to pick the best
    judge_prompt = build_judge_prompt(sample["prompt"], responses)
    judge_response = call_llm(
        api_base, api_key, judge_model,
        [{"role": "user", "content": judge_prompt}],
        max_tokens=512, temperature=0.1, timeout=timeout,
    )

    if not judge_response:
        return None

    parsed = parse_judge_response(judge_response)
    if not parsed or "best_model" not in parsed:
        return None

    return {
        "prompt": sample["prompt"],
        "best_model": parsed["best_model"],
        "model_used": model_used,
        "scores": parsed.get("scores", {}),
        "judge_model": judge_model,
        "candidate_count": len(responses),
        "fallback": False,
    }


def run_judge(input_path: str, output_path: str, api_base: str, api_key: str,
              judge_model: str, candidate_models: list, max_workers: int = 4,
              max_samples: int = 0, timeout: int = 30):
    """Run the full judging pipeline."""
    # Load samples
    samples = []
    with open(input_path, "r", encoding="utf-8") as f:
        for line in f:
            if line.strip():
                samples.append(json.loads(line))

    if max_samples > 0:
        samples = samples[:max_samples]

    print(f"Loaded {len(samples)} samples")
    print(f"Judge model: {judge_model}")
    print(f"Candidate models: {candidate_models}")
    print(f"API base: {api_base}")

    # Process samples
    labels = []
    success = 0
    fail = 0

    def process(sample):
        return judge_sample(sample, api_base, api_key, candidate_models,
                           judge_model, timeout)

    os.makedirs(os.path.dirname(output_path) or ".", exist_ok=True)
    with open(output_path, "w", encoding="utf-8") as f:
        with ThreadPoolExecutor(max_workers=max_workers) as executor:
            futures = {executor.submit(process, s): i for i, s in enumerate(samples)}
            for i, future in enumerate(as_completed(futures)):
                label = future.result()
                if label:
                    f.write(json.dumps(label, ensure_ascii=False) + "\n")
                    f.flush()
                    labels.append(label)
                    success += 1
                else:
                    fail += 1

                if (i + 1) % 100 == 0:
                    print(f"  Progress: {i+1}/{len(samples)} "
                          f"(success={success}, fail={fail})")

    print(f"\nDone: {success} labeled, {fail} failed")
    print(f"Output: {output_path}")

    # Summary
    if labels:
        models_chosen = {}
        for l in labels:
            m = l["best_model"]
            models_chosen[m] = models_chosen.get(m, 0) + 1
        print("\nBest model distribution (judge-selected):")
        for m, count in sorted(models_chosen.items(), key=lambda x: -x[1]):
            print(f"  {m}: {count} ({100*count/len(labels):.1f}%)")


def main():
    parser = argparse.ArgumentParser(description="Judge training samples with LLM")
    parser.add_argument("--input", required=True, help="Input samples.jsonl")
    parser.add_argument("--output", required=True, help="Output labels.jsonl")
    parser.add_argument("--api-base", default="http://localhost:8080/v1",
                        help="OpenAI-compatible API base URL")
    parser.add_argument("--api-key", default="sk-placeholder",
                        help="API key")
    parser.add_argument("--judge", default="deepseek-v4-flash",
                        help="Judge model name")
    parser.add_argument("--candidates", nargs="+",
                        default=["deepseek-v4-flash", "mimo-v2.5", "MiniMax-M3"],
                        help="Candidate models to evaluate")
    parser.add_argument("--max-workers", type=int, default=4)
    parser.add_argument("--max-samples", type=int, default=0,
                        help="Max samples to process (0=all)")
    parser.add_argument("--timeout", type=int, default=30)
    args = parser.parse_args()

    run_judge(
        args.input, args.output, args.api_base, args.api_key,
        args.judge, args.candidates, args.max_workers,
        args.max_samples, args.timeout,
    )


if __name__ == "__main__":
    main()
