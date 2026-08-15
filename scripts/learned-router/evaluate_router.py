#!/usr/bin/env python3
"""
evaluate_router.py — Evaluate learned router against multiple LLMs.

Usage:
    python scripts/learned-router/evaluate_router.py \
        --model data/learned-router/router_model.json \
        --api-base http://localhost:8080/v1 \
        --api-key sk-xxx \
        --output data/learned-router/eval_report.json

Selects core cases, sends to multiple LLMs, compares router
decisions against judge-selected best models.
"""
import argparse
import json
import os
import random
import sys
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from typing import Optional

# Reuse LLM client from judge_samples
from judge_samples import call_llm, parse_judge_response


# ---------------------------------------------------------------------------
# Core cases: representative examples for evaluation
# ---------------------------------------------------------------------------

CORE_CASES = [
    # Simple factual (5)
    {"prompt": "What is the capital of France?", "category": "factual", "expected_cheap": True},
    {"prompt": "What year was Python created?", "category": "factual", "expected_cheap": True},
    {"prompt": "What does HTTP stand for?", "category": "factual", "expected_cheap": True},
    {"prompt": "What is the speed of light?", "category": "factual", "expected_cheap": True},
    {"prompt": "What is a REST API?", "category": "factual", "expected_cheap": True},

    # Code generation (5)
    {"prompt": "Write a Python function to merge two sorted arrays", "category": "code_gen", "expected_cheap": False},
    {"prompt": "Implement a binary search in Go", "category": "code_gen", "expected_cheap": False},
    {"prompt": "Write a React hook for debouncing", "category": "code_gen", "expected_cheap": False},
    {"prompt": "Create a SQL query to find duplicate emails", "category": "code_gen", "expected_cheap": False},
    {"prompt": "Write a TypeScript type for a deeply nested object", "category": "code_gen", "expected_cheap": False},

    # Code understanding (5)
    {"prompt": "Explain what this regex does: ^(?=.*[A-Z])(?=.*[0-9]).{8,}$", "category": "code_understand", "expected_cheap": False},
    {"prompt": "Why would you use a mutex instead of a channel in Go?", "category": "code_understand", "expected_cheap": False},
    {"prompt": "Explain the difference between == and === in JavaScript", "category": "code_understand", "expected_cheap": True},
    {"prompt": "What is the time complexity of quicksort?", "category": "code_understand", "expected_cheap": False},
    {"prompt": "Explain how garbage collection works in Java", "category": "code_understand", "expected_cheap": False},

    # Architecture/Design (5)
    {"prompt": "Design a URL shortener like bit.ly", "category": "architecture", "expected_cheap": False},
    {"prompt": "How should I structure a microservices architecture for an e-commerce platform?", "category": "architecture", "expected_cheap": False},
    {"prompt": "What are the trade-offs between SQL and NoSQL for a social media app?", "category": "architecture", "expected_cheap": False},
    {"prompt": "Design a real-time chat system with WebSockets", "category": "architecture", "expected_cheap": False},
    {"prompt": "How to implement rate limiting in a distributed system?", "category": "architecture", "expected_cheap": False},

    # Writing (5)
    {"prompt": "Write a professional email declining a meeting request", "category": "writing", "expected_cheap": True},
    {"prompt": "Draft a technical blog post about WebSocket security", "category": "writing", "expected_cheap": False},
    {"prompt": "Write a README for a CLI tool that converts CSV to JSON", "category": "writing", "expected_cheap": True},
    {"prompt": "Compose a project proposal for a new authentication system", "category": "writing", "expected_cheap": False},
    {"prompt": "Write documentation for a REST API endpoint", "category": "writing", "expected_cheap": True},

    # Math/Reasoning (5)
    {"prompt": "Calculate the derivative of x^3 * sin(x)", "category": "math", "expected_cheap": False},
    {"prompt": "What is the probability of getting at least one head in 3 coin flips?", "category": "math", "expected_cheap": True},
    {"prompt": "Solve: 2x + 5 = 15", "category": "math", "expected_cheap": True},
    {"prompt": "Prove that the sum of first n odd numbers equals n^2", "category": "math", "expected_cheap": False},
    {"prompt": "Explain Big O notation with examples", "category": "math", "expected_cheap": False},

    # Translation (5)
    {"prompt": "Translate to Chinese: The quick brown fox jumps over the lazy dog", "category": "translation", "expected_cheap": True},
    {"prompt": "Translate this technical documentation from English to Spanish: API endpoints support GET, POST, and DELETE methods", "category": "translation", "expected_cheap": True},
    {"prompt": "翻译以下内容：机器学习是人工智能的一个分支", "category": "translation", "expected_cheap": True},
    {"prompt": "Translate this error message to formal English: 服务器开小差了，请稍后再试", "category": "translation", "expected_cheap": True},
    {"prompt": "Localize this UI string for Japanese: Submit your application", "category": "translation", "expected_cheap": True},
]


# ---------------------------------------------------------------------------
# Router model inference
# ---------------------------------------------------------------------------

def load_router_model(model_path: str) -> dict:
    """Load the trained router model JSON."""
    with open(model_path, "r") as f:
        return json.load(f)


def router_select(model: dict, prompt: str, candidates: list) -> str:
    """Use trained model to select best candidate."""
    from export_samples import detect_language, CODE_KEYWORDS
    from train_router import SimpleTfidf, extract_structural_features
    import numpy as np

    # Reconstruct features
    tfidf_data = model.get("tfidf", {})
    tfidf = SimpleTfidf.from_json(tfidf_data)
    X_tfidf = tfidf.transform([prompt])

    sample = {
        "prompt": prompt,
        "has_code": bool(CODE_KEYWORDS.search(prompt)),
        "has_image": False,
        "conversation_turns": 0,
        "mode": "",
        "tokens_in": len(prompt) // 4,
        "tokens_out": 0,
    }
    struct = extract_structural_features(sample)
    X_struct = np.array([list(struct.values())], dtype=np.float32)
    X = np.hstack([X_tfidf, X_struct])

    # Simple linear scoring
    weights = model.get("weights", {})
    bias = model.get("bias", {})
    model_names = model.get("models", [])

    scores = {}
    for m in candidates:
        if m in weights:
            w = np.array(weights[m])
            score = float(X.dot(w) + bias.get(m, 0.0))
            scores[m] = score
        else:
            scores[m] = 0.5  # unknown model

    if not scores:
        return candidates[0]
    return max(scores, key=scores.get)


# ---------------------------------------------------------------------------
# Multi-LLM evaluation
# ---------------------------------------------------------------------------

EVAL_JUDGE_PROMPT = """You are evaluating the quality of LLM responses. Given a user prompt and responses from multiple models, determine which response is best.

User Prompt:
{prompt}

{responses_block}

Rate each response on correctness, completeness, and clarity (1-5 each).

Respond with ONLY a JSON object:
{{"best_model": "<model_name>", "scores": {{"<model_name>": <total_score>, ...}}}}

Total score = sum of 3 dimensions (max 15). Be strict and discriminating."""


def evaluate_one_case(case: dict, candidates: list, router_model: dict,
                      api_base: str, api_key: str, judge_model: str,
                      timeout: int = 60) -> dict:
    """Evaluate one core case."""
    prompt = case["prompt"]

    # 1. Router decision
    router_pick = router_select(router_model, prompt, candidates)

    # 2. Get responses from all candidates
    messages = [{"role": "user", "content": prompt}]
    responses = {}

    def fetch(m):
        return m, call_llm(api_base, api_key, m, messages, timeout=timeout)

    with ThreadPoolExecutor(max_workers=len(candidates)) as ex:
        futures = {ex.submit(fetch, m): m for m in candidates}
        for f in as_completed(futures):
            m, resp = f.result()
            if resp:
                responses[m] = resp

    if len(responses) < 2:
        return {
            "prompt": prompt[:200],
            "category": case["category"],
            "router_pick": router_pick,
            "best_model": router_pick,
            "match": True,
            "scores": {},
            "error": "insufficient_responses",
        }

    # 3. Judge
    parts = []
    for i, (m, resp) in enumerate(responses.items(), 1):
        truncated = resp[:600] + ("..." if len(resp) > 600 else "")
        parts.append(f"Model {i} ({m}):\n{truncated}")

    judge_prompt = EVAL_JUDGE_PROMPT.format(
        prompt=prompt[:800],
        responses_block="\n\n".join(parts),
    )

    judge_response = call_llm(
        api_base, api_key, judge_model,
        [{"role": "user", "content": judge_prompt}],
        max_tokens=512, temperature=0.1, timeout=timeout,
    )

    parsed = parse_judge_response(judge_response) if judge_response else None
    best_model = parsed.get("best_model", router_pick) if parsed else router_pick
    scores = parsed.get("scores", {}) if parsed else {}

    return {
        "prompt": prompt[:200],
        "category": case["category"],
        "router_pick": router_pick,
        "best_model": best_model,
        "match": router_pick == best_model,
        "scores": scores,
        "responses_received": list(responses.keys()),
    }


def run_evaluation(model_path: str, api_base: str, api_key: str,
                   judge_model: str, candidate_models: list,
                   output_path: str, max_cases: int = 0):
    """Run full evaluation pipeline."""
    print(f"Loading router model: {model_path}")
    router_model = load_router_model(model_path)

    cases = CORE_CASES[:max_cases] if max_cases > 0 else CORE_CASES
    print(f"Evaluating {len(cases)} core cases")
    print(f"Candidates: {candidate_models}")
    print(f"Judge: {judge_model}")

    results = []
    matches = 0
    total = 0

    for i, case in enumerate(cases):
        print(f"\n[{i+1}/{len(cases)}] {case['category']}: {case['prompt'][:60]}...")
        result = evaluate_one_case(
            case, candidate_models, router_model,
            api_base, api_key, judge_model,
        )
        results.append(result)
        total += 1
        if result["match"]:
            matches += 1
        print(f"  Router: {result['router_pick']}, Judge: {result['best_model']}, "
              f"Match: {'✓' if result['match'] else '✗'}")

    # Summary
    accuracy = matches / total if total > 0 else 0
    print(f"\n{'='*60}")
    print(f"Accuracy: {matches}/{total} = {accuracy:.1%}")

    # Per-category accuracy
    by_cat = {}
    for r in results:
        cat = r["category"]
        if cat not in by_cat:
            by_cat[cat] = {"matches": 0, "total": 0}
        by_cat[cat]["total"] += 1
        if r["match"]:
            by_cat[cat]["matches"] += 1

    print("\nPer-category accuracy:")
    for cat, stats in sorted(by_cat.items()):
        acc = stats["matches"] / stats["total"] if stats["total"] > 0 else 0
        print(f"  {cat}: {stats['matches']}/{stats['total']} = {acc:.1%}")

    # Router model distribution
    router_picks = {}
    for r in results:
        m = r["router_pick"]
        router_picks[m] = router_picks.get(m, 0) + 1
    print("\nRouter picks:")
    for m, count in sorted(router_picks.items(), key=lambda x: -x[1]):
        print(f"  {m}: {count}")

    # Save report
    report = {
        "accuracy": accuracy,
        "total": total,
        "matches": matches,
        "per_category": by_cat,
        "router_picks": router_picks,
        "results": results,
        "config": {
            "candidates": candidate_models,
            "judge_model": judge_model,
            "n_cases": len(cases),
        },
    }

    os.makedirs(os.path.dirname(output_path) or ".", exist_ok=True)
    with open(output_path, "w", encoding="utf-8") as f:
        json.dump(report, f, indent=2, ensure_ascii=False)
    print(f"\nReport saved: {output_path}")


def main():
    parser = argparse.ArgumentParser(description="Evaluate learned router")
    parser.add_argument("--model", required=True, help="Router model JSON path")
    parser.add_argument("--api-base", default="http://localhost:8080/v1")
    parser.add_argument("--api-key", default="sk-placeholder")
    parser.add_argument("--judge", default="deepseek-v4-flash")
    parser.add_argument("--candidates", nargs="+",
                        default=["deepseek-v4-flash", "mimo-v2.5", "MiniMax-M3"])
    parser.add_argument("--output", default="data/learned-router/eval_report.json")
    parser.add_argument("--max-cases", type=int, default=0)
    args = parser.parse_args()

    run_evaluation(
        args.model, args.api_base, args.api_key,
        args.judge, args.candidates, args.output, args.max_cases,
    )


if __name__ == "__main__":
    main()
