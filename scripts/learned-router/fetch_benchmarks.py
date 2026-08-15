#!/usr/bin/env python3
"""
fetch_benchmarks.py — Fetch model pricing from OpenRouter and merge
with benchmark scores.

Usage:
    python scripts/learned-router/fetch_benchmarks.py \
        --output data/learned-router/models_value.json

Sources:
  - OpenRouter /api/v1/models (pricing + context, live)
  - data/learned-router/benchmarks.json (manual/curated scores, e.g.
    LMArena Elo / SuperCLUE / SWE-bench; edit freely)

Output: models_value.json — {model_name: {cost_in, cost_out, ctx,
benchmark: {name: score}}, ...} for the models we care about.
"""
import argparse
import json
import os
import urllib.request

OPENROUTER_MODELS = "https://openrouter.ai/api/v1/models"

# Models of interest (canonical names in our gateways/channels).
WATCH = [
    "deepseek/deepseek-v4-flash",
    "deepseek/deepseek-v4-flash-0731",
    "deepseek/deepseek-v4-pro",
    "minimax/minimax-m3",
    "minimax/minimax-m2.7",
    "minimax/minimax-m2.5",
    "xiaomi/mimo-v2.5",
    "xiaomi/mimo-v2.5-pro",
    "z-ai/glm-5.2",
    "volcengine/ark-code-latest",
]


def fetch_openrouter():
    req = urllib.request.Request(OPENROUTER_MODELS, headers={
        "User-Agent": "llmrx-bench/1.0",
        "Accept": "application/json",
    })
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.loads(resp.read().decode())


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", default="data/learned-router/models_value.json")
    parser.add_argument("--benchmarks", default="data/learned-router/benchmarks.json",
                        help="Manual benchmark scores file (may not exist)")
    parser.add_argument("--all", action="store_true",
                        help="Fetch all models, not just the watch list")
    args = parser.parse_args()

    data = fetch_openrouter()
    models = data.get("data", [])
    print(f"OpenRouter returned {len(models)} models")

    # Load manual benchmarks (LMArena Elo / SuperCLUE / ...).
    bench = {}
    if os.path.exists(args.benchmarks):
        with open(args.benchmarks) as f:
            bench = json.load(f)
        print(f"Loaded manual benchmarks: {len(bench)} models")

    out = {}
    for m in models:
        mid = m.get("id", "")
        if not args.all and mid not in WATCH:
            continue
        pricing = m.get("pricing") or {}
        ci = pricing.get("prompt")
        co = pricing.get("completion")
        try:
            ci = float(ci) if ci not in (None, "") else None
        except (TypeError, ValueError):
            ci = None
        try:
            co = float(co) if co not in (None, "") else None
        except (TypeError, ValueError):
            co = None
        # OpenRouter stores per-token; convert to per-1M.
        entry = {
            "cost_in_per_1m": round(ci * 1e6, 4) if ci is not None else None,
            "cost_out_per_1m": round(co * 1e6, 4) if co is not None else None,
            "context": m.get("context_length"),
        }
        # Benchmark scores keyed by exact model id or alias.
        b = bench.get(mid)
        if b is None:
            # try aliases: strip provider prefix, match tail
            tail = mid.split("/")[-1]
            for k, v in bench.items():
                if k.split("/")[-1] == tail:
                    b = v
                    break
        if b:
            entry["benchmark"] = b
        out[mid] = entry

    os.makedirs(os.path.dirname(args.output) or ".", exist_ok=True)
    with open(args.output, "w") as f:
        json.dump(out, f, indent=2, ensure_ascii=False)
    print(f"\nWrote {len(out)} models -> {args.output}")
    for mid in sorted(out):
        e = out[mid]
        b = e.get("benchmark", {})
        top = {k: v for k, v in list(b.items())[:3]} if b else {}
        print(f"  {mid:35s} in=${e['cost_in_per_1m']} out=${e['cost_out_per_1m']} "
              f"ctx={e['context']} bench={json.dumps(top, ensure_ascii=False)[:80]}")


if __name__ == "__main__":
    main()
