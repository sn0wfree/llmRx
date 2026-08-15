#!/usr/bin/env python3
"""
update_benchmarks.py — Auto-generate model benchmark scores from
local judge labels (the most reliable, always-fresh benchmark we
have) and merge with OpenRouter pricing.

Usage:
    python scripts/learned-router/update_benchmarks.py \
        --labels data/learned-router/labels_review2.jsonl \
        --samples data/learned-router/review_samples.jsonl \
        --output data/learned-router/benchmarks_auto.json

Why not public leaderboards? LMArena / Artificial Analysis / HF are
JS-rendered or unreachable from this network; OpenRouter only serves
pricing via API. The local judge pipeline (docs/EVALUATION-
METHODOLOGY.md) measures our own models on our own traffic — that IS
the freshest benchmark.

Output schema (per model):
  {quality_global, quality_by_task, n_samples, cost_in_per_1m,
   cost_out_per_1m, value_index}
"""
import argparse
import json
import statistics
from collections import defaultdict


def load_jsonl(path):
    out = []
    with open(path) as f:
        for line in f:
            if line.strip():
                out.append(json.loads(line))
    return out


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--labels", default="data/learned-router/labels_review2.jsonl")
    parser.add_argument("--samples", default="data/learned-router/review_samples.jsonl")
    parser.add_argument("--value", default="data/learned-router/models_value.json")
    parser.add_argument("--output", default="data/learned-router/benchmarks_auto.json")
    args = parser.parse_args()

    labels = load_jsonl(args.labels)
    reviews = {s["prompt"]: s for s in load_jsonl(args.samples)}
    try:
        with open(args.value) as f:
            value = json.load(f)
    except FileNotFoundError:
        value = {}

    # Collect judge scores per model (mean of 3 dims), split by task.
    q_global = defaultdict(list)
    q_task = defaultdict(lambda: defaultdict(list))
    for l in labels:
        rev = reviews.get(l["prompt"])
        task = rev.get("task_category", "general") if rev else "general"
        scores = l.get("scores") or {}
        for m, dims in scores.items():
            if m in ("actual", "conflict"):
                continue
            vals = [v for v in dims.values() if isinstance(v, (int, float))]
            if not vals:
                continue
            mean = sum(vals) / len(vals)
            q_global[m].append(mean)
            q_task[m][task].append(mean)

    out = {}
    for m in sorted(set(q_global) | set(q_task)):
        entry = {}
        if q_global[m]:
            entry["quality_global"] = round(statistics.mean(q_global[m]), 3)
            entry["n_samples"] = len(q_global[m])
        if q_task[m]:
            entry["quality_by_task"] = {
                t: round(statistics.mean(v), 3)
                for t, v in sorted(q_task[m].items())
            }
        # Merge OpenRouter pricing if available (match by tail).
        for mid, v in value.items():
            if mid.split("/")[-1] == m:
                entry["cost_in_per_1m"] = v.get("cost_in_per_1m")
                entry["cost_out_per_1m"] = v.get("cost_out_per_1m")
                break
        co = entry.get("cost_out_per_1m")
        q = entry.get("quality_global")
        if co and q:
            entry["value_index"] = round(q / co, 3)
        out[m] = entry

    with open(args.output, "w") as f:
        json.dump(out, f, indent=2, ensure_ascii=False)

    print(f"Auto benchmarks -> {args.output}\n")
    print(f"{'model':16s} {'globQ':>7s} {'n':>3s} {'out/1M':>8s} {'valueIdx':>9s}  taskQ")
    print("-" * 90)
    for m in sorted(out, key=lambda k: -(out[k].get("value_index") or 0)):
        e = out[m]
        tq = e.get("quality_by_task", {})
        tqs = ", ".join(f"{t}:{v}" for t, v in sorted(tq.items(), key=lambda x: -x[1])[:4])
        print(f"{m:16s} {e.get('quality_global', '-'):>7} {e.get('n_samples', 0):>3} "
              f"{e.get('cost_out_per_1m', '-'):>8} {e.get('value_index', '-'):>9}  {tqs}")


if __name__ == "__main__":
    main()
