#!/usr/bin/env python3
"""
train_structured.py — Train a structured-feature router classifier
and compare against the task-rule baseline.

Usage:
    python scripts/learned-router/train_structured.py \
        --train data/learned-router/train_ready.jsonl

Features: task_category one-hot + context-length bucket + tool-call
count bucket + language. Model: LogisticRegression (stable on tiny
datasets). Evaluation: leave-one-out CV vs the task_model_bias rule
baseline and the majority-class baseline.
"""
import argparse
import json
import sys
from collections import Counter

import numpy as np


def load(path):
    out = []
    with open(path) as f:
        for line in f:
            if line.strip():
                out.append(json.loads(line))
    return out


def features(d, tasks):
    f = []
    for t in tasks:
        f.append(1.0 if d["task_category"] == t else 0.0)
    # context length bucket
    t = d["tokens_in"]
    f += [1.0 if t <= 30000 else 0.0,
          1.0 if 30000 < t <= 100000 else 0.0,
          1.0 if 100000 < t <= 300000 else 0.0,
          1.0 if t > 300000 else 0.0]
    # tool-call count bucket
    n_tools = sum(1 for m in d["context"] if "tool_calls" in m)
    f += [1.0 if n_tools == 0 else 0.0,
          1.0 if 0 < n_tools <= 100 else 0.0,
          1.0 if 100 < n_tools <= 300 else 0.0,
          1.0 if n_tools > 300 else 0.0]
    # language
    p = d["prompt"]
    cn = sum(1 for c in p if "\u4e00" <= c <= "\u9fff")
    en = sum(1 for c in p if c.isascii() and c.isalpha())
    f += [1.0 if cn > en * 2 else 0.0,
          1.0 if en > cn * 2 else 0.0,
          1.0 if not (cn > en * 2 or en > cn * 2) else 0.0]
    return f


def task_rule_predict(d, bias):
    """Rule baseline: first bias model for the task, else cost order."""
    order = ["deepseek-v4-flash", "mimo-v2.5", "MiniMax-M3"]
    prefs = bias.get(d["task_category"])
    if not prefs:
        return order[0]
    for m in prefs:
        if m in order:
            return m
    return order[0]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--train", default="data/learned-router/train_ready.jsonl")
    parser.add_argument("--bias", default=None,
                        help="JSON bias file for the rule baseline (default: tuned)")
    args = parser.parse_args()

    data = load(args.train)
    if len(data) < 10:
        print("too few samples", file=sys.stderr)
        sys.exit(1)

    DEFAULT_BIAS = {
        "math": ["MiniMax-M3", "mimo-v2.5"],
        "general": ["mimo-v2.5", "MiniMax-M3"],
        "code": ["mimo-v2.5", "MiniMax-M3"],
        "understand": ["mimo-v2.5", "MiniMax-M3"],
        "visual": ["MiniMax-M3", "mimo-v2.5"],
        "translate": ["MiniMax-M3", "mimo-v2.5"],
        "data": ["mimo-v2.5", "MiniMax-M3"],
    }
    if args.bias:
        with open(args.bias) as f:
            DEFAULT_BIAS = json.load(f)

    tasks = sorted({d["task_category"] for d in data})
    labels = sorted({d["best_model"] for d in data})
    label_idx = {m: i for i, m in enumerate(labels)}
    X = np.array([features(d, tasks) for d in data], dtype=np.float32)
    y = np.array([label_idx[d["best_model"]] for d in data])

    print(f"samples={len(data)} classes={labels} features={X.shape[1]}")
    print(f"task set: {tasks}")

    # Baselines
    majority = Counter(y).most_common(1)[0][0]
    maj_acc = np.mean(y == majority)

    rule_pred = [label_idx[task_rule_predict(d, DEFAULT_BIAS)] for d in data]
    rule_acc = np.mean(np.array(rule_pred) == y)
    print(f"\nBaselines (LOOCV-style):")
    print(f"  majority-class: {maj_acc:.3f} (always {labels[majority]})")
    print(f"  task-rule bias: {rule_acc:.3f}")

    # Logistic regression LOOCV
    from sklearn.linear_model import LogisticRegression
    from sklearn.preprocessing import StandardScaler
    preds = []
    for i in range(len(data)):
        tr, te = list(range(len(data))), [i]
        tr.remove(i)
        scaler = StandardScaler().fit(X[tr])
        Xtr = scaler.transform(X[tr])
        Xte = scaler.transform(X[te])
        clf = LogisticRegression(max_iter=1000, C=1.0)
        clf.fit(Xtr, y[tr])
        preds.append(clf.predict(Xte)[0])
    preds = np.array(preds)
    lr_acc = np.mean(preds == y)
    print(f"  logistic(LOOCV): {lr_acc:.3f}")

    # Confusion detail
    print("\nPer-sample comparison (task | true -> rule / lr):")
    for d, yt, pr, pl in zip(data, y, rule_pred, preds):
        mark = "✓" if pr == yt or pl == yt else " "
        print(f"  {mark} [{d['task_category']:10s}] true={labels[yt]:14s} "
              f"rule={labels[pr]:14s} lr={labels[pl]:14s} tokens={d['tokens_in']}")

    print("\nConclusion: pick the better strategy (rule vs lr).")


if __name__ == "__main__":
    main()
