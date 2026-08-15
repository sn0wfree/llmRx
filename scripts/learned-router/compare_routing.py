#!/usr/bin/env python3
"""
compare_routing.py — Compare routing strategies against judge labels.

Usage:
    python scripts/learned-router/compare_routing.py \
        --classified data/learned-router/samples_classified.jsonl \
        --labels data/learned-router/labels_review.jsonl \
        --review data/learned-router/review_samples.jsonl

Simulates two auto-routing strategies on the review set and scores
them against the independent judge's candidate ranking:

  V1        — plain cost order (tier candidates as configured)
  TaskBias  — task_model_bias reorder (V1 order + per-task preference)

The judge's per-candidate mean score (correctness/completeness/
contextual_fit) is the ground truth ranking. 'actual' is excluded
(it is not a routable model).
"""
import argparse
import json
from collections import defaultdict

# Default task_model_bias (mirrors the 25-sample hints + config):
# math -> MiniMax-M3 won the review; general -> mimo-v2.5 won.
DEFAULT_BIAS = {
    "math": ["MiniMax-M3", "mimo-v2.5"],
    "general": ["mimo-v2.5", "MiniMax-M3"],
    "code": ["MiniMax-M3", "mimo-v2.5"],
    "data": ["mimo-v2.5", "MiniMax-M3"],
}

# Tier candidate order (cost order as configured in a combo).
V1_ORDER = ["deepseek-v4-flash", "mimo-v2.5", "MiniMax-M3"]


def load_jsons(path):
    out = []
    with open(path) as f:
        for line in f:
            if line.strip():
                out.append(json.loads(line))
    return out


def judge_ranking(label):
    """Candidate ranking from the judge's scores (mean of 3 dims),
    excluding 'actual' and 'conflict' artifacts."""
    scores = label.get("scores") or {}
    means = {}
    for m, dims in scores.items():
        if m == "actual" or m == "conflict":
            continue
        vals = [v for v in dims.values() if isinstance(v, (int, float))]
        if vals:
            means[m] = sum(vals) / len(vals)
    return [m for m, _ in sorted(means.items(), key=lambda x: -x[1])], means


def strategy_v1():
    return list(V1_ORDER)


def strategy_task_bias(cat):
    order = list(V1_ORDER)
    prefs = DEFAULT_BIAS.get(cat)
    if not prefs:
        return order
    preferred = [m for m in prefs if m in order]
    rest = [m for m in order if m not in preferred]
    return preferred + rest


def top_k_hit(pick, ranking, k):
    return pick in ranking[:k]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--classified", default="data/learned-router/samples_classified.jsonl")
    parser.add_argument("--labels", default="data/learned-router/labels_review.jsonl")
    parser.add_argument("--review", default="data/learned-router/review_samples.jsonl")
    parser.add_argument("--bias", default=None, help="JSON file overriding task_model_bias")
    args = parser.parse_args()

    global DEFAULT_BIAS
    if args.bias:
        with open(args.bias) as f:
            DEFAULT_BIAS = json.load(f)

    classified = {s["message_id"]: s for s in load_jsons(args.classified)}
    labels = load_jsons(args.labels)
    reviews = load_jsons(args.review)

    # Map labels to reviews by prompt
    review_by_prompt = {s["prompt"]: s for s in reviews}

    stats = defaultdict(lambda: {"v1_t1": 0, "bias_t1": 0, "v1_t2": 0, "bias_t2": 0, "n": 0})
    total = {"v1_t1": 0, "bias_t1": 0, "v1_t2": 0, "bias_t2": 0, "n": 0}

    for label in labels:
        prompt = label["prompt"]
        rev = review_by_prompt.get(prompt)
        if not rev:
            continue
        cat = rev.get("task_category", "general")
        ranking, means = judge_ranking(label)
        if len(ranking) < 2:
            continue  # not enough judge signal

        v1_pick = strategy_v1()[0]
        bias_pick = strategy_task_bias(cat)[0]

        s = stats[cat]
        s["n"] += 1
        total["n"] += 1
        if top_k_hit(v1_pick, ranking, 1):
            s["v1_t1"] += 1
            total["v1_t1"] += 1
        if top_k_hit(bias_pick, ranking, 1):
            s["bias_t1"] += 1
            total["bias_t1"] += 1
        if top_k_hit(v1_pick, ranking, 2):
            s["v1_t2"] += 1
            total["v1_t2"] += 1
        if top_k_hit(bias_pick, ranking, 2):
            s["bias_t2"] += 1
            total["bias_t2"] += 1

    print(f"{'task':12s} {'n':>3s} | {'V1 top1':>8s} {'bias top1':>9s} | {'V1 top2':>8s} {'bias top2':>9s}")
    print("-" * 60)
    for cat in sorted(stats):
        s = stats[cat]
        if s["n"] == 0:
            continue
        v1t1 = f"{100*s['v1_t1']/s['n']:.0f}%"
        bt1 = f"{100*s['bias_t1']/s['n']:.0f}%"
        v1t2 = f"{100*s['v1_t2']/s['n']:.0f}%"
        bt2 = f"{100*s['bias_t2']/s['n']:.0f}%"
        print(f"{cat:12s} {s['n']:3d} | {v1t1:>8s} {bt1:>9s} | {v1t2:>8s} {bt2:>9s}")

    print("-" * 60)
    t = total
    print(f"{'ALL':12s} {t['n']:3d} | {100*t['v1_t1']/t['n']:6.0f}% {100*t['bias_t1']/t['n']:7.0f}% | "
          f"{100*t['v1_t2']/t['n']:6.0f}% {100*t['bias_t2']/t['n']:7.0f}%")

    print("\nDetailed picks (prompt | V1 -> judge ranking | bias -> judge ranking):")
    for label in labels:
        prompt = label["prompt"]
        rev = review_by_prompt.get(prompt)
        if not rev:
            continue
        cat = rev.get("task_category", "general")
        ranking, means = judge_ranking(label)
        if len(ranking) < 2:
            continue
        v1_pick = strategy_v1()[0]
        bias_pick = strategy_task_bias(cat)[0]
        print(f"  [{cat:10s}] {prompt[:45]!r}")
        print(f"    V1   picks {v1_pick:16s} | judge top1={ranking[0]:16s} (means={ {k: round(v,1) for k,v in list(means.items())[:3]} })")
        print(f"    bias picks {bias_pick:16s} | {'HIT' if bias_pick==ranking[0] else 'miss'}")


if __name__ == "__main__":
    main()
