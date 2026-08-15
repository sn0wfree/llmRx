#!/usr/bin/env python3
"""
prepare_training.py — Build the learned-router training set from
judge labels.

Usage:
    python scripts/learned-router/prepare_training.py \
        --labels data/learned-router/labels_review2.jsonl \
        --samples data/learned-router/review_samples.jsonl \
        --output data/learned-router/train_ready.jsonl

Each sample's label is the judge's top candidate model (mean of the
3-dim scores), excluding the non-routable 'actual' pseudo-candidate.
'conflict' samples keep their score-track winner when available.
"""
import argparse
import json


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--labels", default="data/learned-router/labels_review2.jsonl")
    parser.add_argument("--samples", default="data/learned-router/review_samples.jsonl")
    parser.add_argument("--output", default="data/learned-router/train_ready.jsonl")
    parser.add_argument("--min-candidates", type=int, default=2,
                        help="Require at least N candidates with judge scores")
    args = parser.parse_args()

    labels = []
    with open(args.labels) as f:
        for line in f:
            if line.strip():
                labels.append(json.loads(line))
    samples = {}
    with open(args.samples) as f:
        for line in f:
            if line.strip():
                s = json.loads(line)
                samples[s["prompt"]] = s

    out = []
    skipped = {"no_signal": 0, "few_candidates": 0, "no_sample": 0}
    for l in labels:
        rev = samples.get(l["prompt"])
        if not rev:
            skipped["no_sample"] += 1
            continue
        scores = l.get("scores") or {}
        means = {}
        for m, dims in scores.items():
            if m in ("actual", "conflict"):
                continue
            vals = [v for v in dims.values() if isinstance(v, (int, float))]
            if vals:
                means[m] = sum(vals) / len(vals)
        if not means:
            skipped["no_signal"] += 1
            continue
        if len(means) < args.min_candidates:
            skipped["few_candidates"] += 1
            continue
        top = max(means, key=means.get)

        out.append({
            "prompt": rev["prompt"],
            "context": rev["context"],
            "best_model": top,
            "scores": means,
            "model_used": rev["model_used"],
            "task_category": rev.get("task_category", "general"),
            "tokens_in": rev["tokens_in"],
            "actual_reply": rev.get("actual_reply"),
            "conflict": l.get("best_model") == "conflict",
            "confidence": l.get("confidence"),
        })

    with open(args.output, "w") as f:
        for s in out:
            f.write(json.dumps(s, ensure_ascii=False) + "\n")

    from collections import Counter
    dist = Counter(s["best_model"] for s in out)
    print(f"Train ready: {len(out)} samples")
    print(f"Skipped: {skipped}")
    print("Label distribution:", dict(dist))
    print(f"Output: {args.output}")


if __name__ == "__main__":
    main()
