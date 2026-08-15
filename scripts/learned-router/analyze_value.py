#!/usr/bin/env python3
"""
analyze_value.py — Cost-value analysis: merge OpenRouter pricing,
public benchmark scores, and our own judge-measured quality, then
generate routing strategy suggestions.

Usage:
    python scripts/learned-router/analyze_value.py \
        --value data/learned-router/models_value.json \
        --benchmarks data/learned-router/benchmarks.json \
        --judge data/learned-router/train_ready.jsonl

Outputs:
  - Per-model: price, public quality, judge-measured quality
  - Value index = quality / cost (per 1M output tokens)
  - Strategy suggestions:
      * tier assignment (cheap workhorses vs high-end)
      * orchestrator-worker suggestion (planner vs executor)
"""
import argparse
import json
import statistics
from collections import defaultdict


def load(path, required=True):
    if not path:
        return None
    try:
        with open(path) as f:
            return json.load(f)
    except FileNotFoundError:
        if required:
            raise
        return None


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--value", default="data/learned-router/models_value.json")
    parser.add_argument("--benchmarks", default="data/learned-router/benchmarks.json")
    parser.add_argument("--judge", default="data/learned-router/train_ready.jsonl")
    args = parser.parse_args()

    value = load(args.value)
    bench = load(args.benchmarks, required=False) or {}
    judge = []
    if args.judge:
        with open(args.judge) as f:
            judge = [json.loads(l) for l in f if l.strip()]

    # Judge-measured mean quality per model (3-dim scores).
    jq = defaultdict(list)
    for d in judge:
        m = d.get("best_model")
        s = d.get("scores") or {}
        vals = [v for v in s.values() if isinstance(v, (int, float))]
        if m and vals:
            jq[m].append(sum(vals) / len(vals))

    print(f"{'model':28s} {'in/1M':>8s} {'out/1M':>8s} {'pubQ':>5s} {'judgeQ':>7s} "
          f"{'valueIdx':>8s}")
    print("-" * 72)

    rows = []
    for mid, v in sorted(value.items(), key=lambda kv: kv[1].get("cost_out_per_1m") or 1e9):
        ci = v.get("cost_in_per_1m")
        co = v.get("cost_out_per_1m")
        if co is None:
            continue
        b = bench.get(mid) or {}
        pubq = b.get("quality_rating")
        tail = mid.split("/")[-1]
        jvals = jq.get(tail, jq.get(mid, []))
        jmean = round(statistics.mean(jvals), 2) if jvals else None
        # Value index: quality / cost (use judge quality when available)
        q = jmean if jmean is not None else pubq
        vix = round(q / co, 3) if q and co else None
        rows.append((mid, ci, co, pubq, jmean, vix, b.get("note", "")))
        print(f"{mid:28s} {ci if ci else '?':>8} {co:>8} "
              f"{pubq if pubq is not None else '-':>5} "
              f"{jmean if jmean is not None else '-':>7} "
              f"{vix if vix is not None else '-':>8}")

    print("\n" + "=" * 72)
    print("STRATEGY SUGGESTIONS (cost-value driven)")
    print("=" * 72)

    # Tier split by value index + quality
    ranked = sorted([r for r in rows if r[5] is not None], key=lambda r: -r[5])
    if ranked:
        print("\n1. 性价比排序 (value index = quality / cost-per-1M-out):")
        for mid, ci, co, pubq, jmean, vix, note in ranked:
            print(f"   {vix:6.3f}  {mid:28s} judgeQ={jmean} pubQ={pubq}")

    # Workhorse vs specialist split
    cheap = [r for r in rows if (r[2] or 1e9) <= 0.5]
    premium = [r for r in rows if (r[2] or 1e9) > 0.5]
    print("\n2. 分层建议:")
    print("   日常/简单任务 (workhorses, out<=$0.5/1M):")
    for mid, ci, co, pubq, jmean, vix, note in cheap:
        print(f"     - {mid} (out=${co}/1M, judgeQ={jmean})")
    print("   高难度任务 (premium, out>$0.5/1M):")
    for mid, ci, co, pubq, jmean, vix, note in premium:
        print(f"     - {mid} (out=${co}/1M, judgeQ={jmean})")

    # Orchestrator-worker suggestion
    best_premium = max(premium, key=lambda r: r[4] or 0, default=None) if premium else None
    best_cheap = max(cheap, key=lambda r: r[5] or 0, default=None) if cheap else None
    print("\n3. Orchestrator-Worker 建议 (plan → execute 分发):")
    if best_premium:
        print(f"   Planner (强模型拆解方案): {best_premium[0]} (judgeQ={best_premium[4]})")
    if best_cheap:
        print(f"   Worker (性价比执行):      {best_cheap[0]} (valueIdx={best_cheap[5]})")
    print("   分发规则: agentic/complex tier → planner 先出方案与任务拆解,")
    print("             拆解后的子任务按 task_model_bias 分发给 workhorse 执行")

    # If judge data exists, print the empirical ranking too
    if jq:
        print("\n4. 本地 judge 实测平均质量 (41 样本候选模型):")
        for m, vals in sorted(jq.items(), key=lambda kv: -statistics.mean(kv[1])):
            print(f"   {m:20s} {statistics.mean(vals):.2f} (n={len(vals)})")


if __name__ == "__main__":
    main()
