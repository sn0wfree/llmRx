#!/usr/bin/env python3
"""
classify_samples.py — Classify training samples by task type.

Usage:
    python scripts/learned-router/classify_samples.py \
        --input data/learned-router/samples.jsonl \
        --output data/learned-router/samples_classified.jsonl \
        --pick 3 \
        --review-out data/learned-router/review_samples.jsonl

Classification input = prompt + last 3 context messages + recent
tool calls (agentic prompts are context-dependent short commands,
so the prompt alone cannot determine the task type).

9 categories: code / understand / writing / math / translate /
data / deploy / visual / general.

After classifying, picks N representative samples per category
(covering different models and context lengths, deterministic via
hash ordering) and writes them to review_out.
"""
import argparse
import hashlib
import json
import re
import statistics
import sys
from collections import Counter, defaultdict

# ---------------------------------------------------------------------------
# Classifier
# ---------------------------------------------------------------------------

# (category, [regexes], weight) — weighted keyword scoring on
# (prompt + recent context). Higher weight = stronger signal.
RULES = [
    ('code', [
        r'\b(function|class|def|import|api|endpoint|script|代码|函数|接口|实现|编写|修复|refactor|bug|test|单元测试|git|commit|分支|merge|编译|报错|traceback|error)\b',
    ], 3),
    ('understand', [
        r'\b(explain|debug|why|error|分析|排查|解释|为什么|检查|梳理|逻辑|理解|review|评估|调研|调查|原因)\b',
    ], 2),
    ('writing', [
        r'\b(draft|essay|blog|document|report|文档|报告|总结|说明|写入|记录|注释|readme|changelog|备注)\b',
    ], 2),
    ('math', [
        r'\b(calculate|equation|solve|统计|计算|公式|回测|correlation|概率|因子|回归|quant|风险|波动|收益率|alpha|beta|标准差|夏普|回撤|收益|净值)\b',
    ], 2),
    ('translate', [
        r'\b(translate|翻译|localize)\b',
    ], 3),
    ('data', [
        r'\b(data|csv|excel|sql|query|数据库|表|字段|导入|导出|清洗|pandas|dataframe|json|列|行)\b',
    ], 2),
    ('deploy', [
        r'\b(deploy|docker|nginx|server|配置|部署|启动|运行|环境|install|依赖|build|重启|进程|服务|端口)\b',
    ], 2),
    ('visual', [
        r'\b(图表|图|plot|chart|dashboard|前端|页面|ui|css|html|样式|表格|展示|布局|样式)\b',
    ], 2),
]


def _recent_context_text(sample, n=3):
    """prompt + last n context messages + recent tool calls."""
    parts = [sample.get("prompt", "")]
    for m in sample.get("context", [])[-n:]:
        c = m.get("content")
        if c:
            parts.append(str(c)[:200])
        for tc in m.get("tool_calls", []):
            parts.append(tc["function"]["arguments"][:100])
    return " ".join(parts).lower()


def classify(sample) -> str:
    """Return one of the 9 task categories."""
    text = _recent_context_text(sample)
    best, best_score = "general", 0
    for cat, pats, w in RULES:
        score = sum(len(re.findall(pat, text)) for pat in pats) * w
        if score > best_score:
            best, best_score = cat, score
    return best if best_score > 0 else "general"


# ---------------------------------------------------------------------------
# Representative picking
# ---------------------------------------------------------------------------

def pick_representatives(samples, cat, n):
    """Pick up to n representatives for a category.

    Strategy: 3 buckets by context length (short/mid/long), rotate
    through them, prefer different model_used. Deterministic via
    md5(message_id) ordering.
    """
    def bucket(s):
        t = s.get("tokens_in", 0)
        if t > 300000:
            return "ultra"
        if t > 100000:
            return "long"
        if t > 30000:
            return "mid"
        return "short"

    # Order deterministically within the category
    ordered = sorted(
        samples,
        key=lambda s: hashlib.md5(s["message_id"].encode()).hexdigest(),
    )
    # Round-robin across length buckets for diversity
    buckets = defaultdict(list)
    for s in ordered:
        buckets[bucket(s)].append(s)
    bucket_names = ["ultra", "long", "mid", "short"]
    picked = []
    seen_models = set()
    # Pass 1: prefer distinct models, rotating buckets
    for name in bucket_names * 3:
        if len(picked) >= n:
            break
        b = buckets[name]
        if not b:
            continue
        for s in b:
            if s["model_used"] not in seen_models:
                picked.append(s)
                seen_models.add(s["model_used"])
                break
    # Pass 2: fill remaining from any bucket
    for name in bucket_names:
        if len(picked) >= n:
            break
        for s in buckets[name]:
            if s not in picked:
                picked.append(s)
                if len(picked) >= n:
                    break
    return picked


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="Classify samples by task type")
    parser.add_argument("--input", default="data/learned-router/samples.jsonl")
    parser.add_argument("--output", default="data/learned-router/samples_classified.jsonl")
    parser.add_argument("--pick", type=int, default=3,
                        help="Representatives per category (default 3)")
    parser.add_argument("--review-out", default="data/learned-router/review_samples.jsonl")
    args = parser.parse_args()

    samples = []
    with open(args.input, "r", encoding="utf-8") as f:
        for line in f:
            if line.strip():
                samples.append(json.loads(line))
    print(f"Loaded {len(samples)} samples")

    # Classify
    counts = Counter()
    for s in samples:
        cat = classify(s)
        s["task_category"] = cat
        counts[cat] += 1

    print("\nTask distribution:")
    for cat, n in counts.most_common():
        print(f"  {cat}: {n} ({100*n/len(samples):.1f}%)")

    # Write classified samples
    with open(args.output, "w", encoding="utf-8") as f:
        for s in samples:
            f.write(json.dumps(s, ensure_ascii=False) + "\n")
    print(f"\nClassified: {args.output}")

    # Pick representatives per category
    by_cat = defaultdict(list)
    for s in samples:
        by_cat[s["task_category"]].append(s)

    review = []
    print(f"\nRepresentatives (up to {args.pick} per category):")
    for cat in sorted(by_cat.keys()):
        reps = pick_representatives(by_cat[cat], cat, args.pick)
        models = Counter(s["model_used"] for s in reps)
        lens = [s["tokens_in"] for s in reps]
        print(f"  {cat}: picked {len(reps)}/{len(by_cat[cat])} | "
              f"models={dict(models)} | tokens={[l for l in lens]}")
        for s in reps:
            review.append(s)

    with open(args.review_out, "w", encoding="utf-8") as f:
        for s in review:
            f.write(json.dumps(s, ensure_ascii=False) + "\n")
    print(f"\nReview set: {args.review_out} ({len(review)} samples)")

    # Summary stats of the review set
    total_tokens = sum(s["tokens_in"] for s in review)
    print(f"Review set tokens_in: min={min(s['tokens_in'] for s in review)} "
          f"median={statistics.median(s['tokens_in'] for s in review):.0f} "
          f"max={max(s['tokens_in'] for s in review)}")


if __name__ == "__main__":
    main()
