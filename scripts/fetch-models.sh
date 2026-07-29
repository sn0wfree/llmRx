#!/bin/bash
#
# fetch-models.sh — download the LiteLLM model catalog and place it
# where the gateway will pick it up.
#
# Usage:
#   scripts/fetch-models.sh                    # download to data/models.json
#   scripts/fetch-models.sh output.json        # custom output path
#   OUTPUT=/tmp/m.json scripts/fetch-models.sh # via env var
#
# The file is saved as-is (LiteLLM's raw format). The gateway parses
# it on load and on each 24h refresh cycle. If the format changes
# upstream, individual entries with missing required fields are
# skipped with a warning — the gateway never crashes on bad data.
#
# Requirements: curl + a network path to GitHub.
# On systems where raw.githubusercontent.com is unreachable, set
# GITHUB_MIRROR to a mirror URL before running.

set -euo pipefail

LITELLM_REPO="${GITHUB_MIRROR:-https://raw.githubusercontent.com}/BerriAI/litellm/main"
LITELLM_FILE="model_prices_and_context_window.json"

OUTPUT="${OUTPUT:-${1:-data/models.json}}"

echo "Fetching $LITELLM_REPO/$LITELLM_FILE ..."
curl -sSL --fail --max-time 60 "$LITELLM_REPO/$LITELLM_FILE" -o "$OUTPUT.tmp"

COUNT=$(python3 -c "import json; d=json.load(open('$OUTPUT.tmp')); print(len([k for k in d if k not in ('_meta', 'metadata')]))" 2>/dev/null || echo "?")

mv "$OUTPUT.tmp" "$OUTPUT"
echo "Saved $OUTPUT (~$COUNT models)"
