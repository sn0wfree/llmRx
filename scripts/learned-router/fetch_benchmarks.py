#!/usr/bin/env python3
"""
fetch_benchmarks.py — Fetch model pricing + benchmark scores from
OpenRouter APIs.

Usage:
    python scripts/learned-router/fetch_benchmarks.py \
        --token sk-or-v1-... \
        --output data/learned-router/models_value.json

Sources:
  - GET /api/v1/models          (pricing + context, no auth needed)
  - GET /api/v1/benchmarks      (intelligence/coding/agentic index
                                 from Artificial Analysis; needs an
                                 OpenRouter API token)
  - data/learned-router/benchmarks.json (manual/curated extra scores)

Output: models_value.json — per model: pricing, context, and
benchmark indices (intelligence_index / coding_index / agentic_index).
"""
import argparse
import json
import os
import re
import urllib.request

OPENROUTER_MODELS = "https://openrouter.ai/api/v1/models"
OPENROUTER_BENCHMARKS = "https://openrouter.ai/api/v1/benchmarks"

# Strips trailing date suffixes like "-20260731" from model slugs.
_DATE_SUFFIX = re.compile(r"-\d{8}$")


def norm_slug(slug):
    """deepseek/deepseek-v4-flash-20260731 -> deepseek-v4-flash"""
    return _DATE_SUFFIX.sub("", slug.split("/")[-1])

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


def http_json(url, token=None):
    headers = {"User-Agent": "llmrx-bench/1.0", "Accept": "application/json"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    req = urllib.request.Request(url, headers=headers)
    with urllib.request.urlopen(req, timeout=40) as resp:
        return json.loads(resp.read().decode())


def fetch_openrouter():
    data = http_json(OPENROUTER_MODELS)
    return data.get("data", [])


def fetch_benchmarks(token):
    data = http_json(OPENROUTER_BENCHMARKS, token)
    out = {}
    for m in data.get("data", []):
        slug = m.get("model_permaslug", "")
        # Same slug can appear multiple times (provider/price variants);
        # keep the entry that carries actual index values.
        cur = out.get(slug)
        entry = {
            "intelligence_index": m.get("intelligence_index"),
            "coding_index": m.get("coding_index"),
            "agentic_index": m.get("agentic_index"),
            "bench_source": m.get("source"),
        }
        if cur is None:
            out[slug] = entry
            continue
        # Prefer the variant with non-null indices.
        cur_has = any(cur.get(k) is not None for k in ("intelligence_index", "coding_index", "agentic_index"))
        new_has = any(entry.get(k) is not None for k in ("intelligence_index", "coding_index", "agentic_index"))
        if new_has and not cur_has:
            out[slug] = entry
    return out


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", default="data/learned-router/models_value.json")
    parser.add_argument("--benchmarks", default="data/learned-router/benchmarks.json",
                        help="Manual benchmark scores file (may not exist)")
    parser.add_argument("--token", default=None,
                        help="OpenRouter API token for /api/v1/benchmarks "
                             "(or set OPENROUTER_API_KEY)")
    parser.add_argument("--all", action="store_true",
                        help="Fetch all models, not just the watch list")
    args = parser.parse_args()

    token = args.token or os.environ.get("OPENROUTER_API_KEY")

    models = fetch_openrouter()
    print(f"OpenRouter /models returned {len(models)} models")

    bench_api = {}
    if token:
        try:
            bench_api = fetch_benchmarks(token)
            print(f"OpenRouter /benchmarks returned {len(bench_api)} models "
                  f"(source: {next(iter(bench_api.values()), {}).get('bench_source')})")
        except Exception as e:
            print(f"WARN: benchmarks fetch failed: {e}")
    else:
        print("No token: skipping /api/v1/benchmarks (pricing only)")

    # Manual benchmark scores.
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
        entry = {
            "cost_in_per_1m": round(ci * 1e6, 4) if ci is not None else None,
            "cost_out_per_1m": round(co * 1e6, 4) if co is not None else None,
            "context": m.get("context_length"),
        }
        # Auto benchmark indices (match by normalized family name).
        tail = norm_slug(mid)
        b = None
        for k, v in bench_api.items():
            if norm_slug(k) == tail:
                b = v
                break
        if b:
            entry.update({k: v for k, v in b.items() if v is not None})
        # Manual scores override/annotate.
        bm = bench.get(mid)
        if bm is None:
            for k, v in bench.items():
                if k.split("/")[-1] == tail:
                    bm = v
                    break
        if bm:
            entry["manual"] = bm
        out[mid] = entry

    os.makedirs(os.path.dirname(args.output) or ".", exist_ok=True)
    with open(args.output, "w") as f:
        json.dump(out, f, indent=2, ensure_ascii=False)
    print(f"\nWrote {len(out)} models -> {args.output}")
    for mid in sorted(out):
        e = out[mid]
        b = {k: v for k, v in e.items() if k.startswith(("intelligence", "coding", "agentic"))}
        bs = json.dumps(b, ensure_ascii=False) if b else "-"
        print(f"  {mid:35s} in=${e['cost_in_per_1m']} out=${e['cost_out_per_1m']} "
              f"bench={bs}")


if __name__ == "__main__":
    main()
