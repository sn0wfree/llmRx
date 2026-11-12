#!/bin/sh
# scripts/push-retry.sh — push branches/tags with connectivity probing.
#
# GitHub's HTTPS endpoint intermittently refuses connections (seen
# repeatedly on this project: "Connection was reset", then a 300s
# timeout, then fine). A plain `git push` fails and leaves you guessing
# whether it was the network or the push. This script probes with a
# cheap `ls-remote` first and only pushes when the probe succeeds,
# retrying until a round limit is reached.
#
# Usage:
#   scripts/push-retry.sh                     # push current branch
#   scripts/push-retry.sh master v0.3.0       # push a branch + tag(s)
#   scripts/push-retry.sh --all-tags master   # branch + every local tag not on remote
#
# Environment:
#   REMOTE=origin      remote name (default origin)
#   ROUNDS=20          max attempts (default 20)
#   WAIT_SEC=20        seconds between attempts (default 20)

set -u

REMOTE="${REMOTE:-origin}"
ROUNDS="${ROUNDS:-20}"
WAIT_SEC="${WAIT_SEC:-20}"

ALL_TAGS=0
if [ "${1:-}" = "--all-tags" ]; then
    ALL_TAGS=1
    shift
fi

# Default target: the current branch.
if [ $# -eq 0 ]; then
    BRANCH=$(git rev-parse --abbrev-ref HEAD 2>/dev/null) || {
        echo "FAIL: not in a git repository" >&2
        exit 2
    }
    set -- "$BRANCH"
fi

echo "push-retry: remote=$REMOTE targets=$* rounds=$ROUNDS wait=${WAIT_SEC}s"

round=1
while [ "$round" -le "$ROUNDS" ]; do
    printf '=== round %d/%d %s ===\n' "$round" "$ROUNDS" "$(date +%H:%M:%S)"

    if git ls-remote --heads "$REMOTE" >/dev/null 2>&1; then
        echo "probe ok, pushing..."

        if [ "$ALL_TAGS" -eq 1 ]; then
            if git push "$REMOTE" "$@" --follow-tags 2>&1; then
                echo "PUSH_OK"
                exit 0
            fi
            # --follow-tags only pushes annotated tags reachable from the
            # pushed commits; push each remaining local tag explicitly.
            failed=0
            for t in $(git tag -l); do
                if ! git ls-remote --tags "$REMOTE" "refs/tags/$t" | grep -q .; then
                    echo "pushing tag $t"
                    git push "$REMOTE" "refs/tags/$t" 2>&1 || failed=1
                fi
            done
            if [ "$failed" -eq 0 ]; then
                echo "PUSH_OK"
                exit 0
            fi
        else
            if git push "$REMOTE" "$@" 2>&1; then
                echo "PUSH_OK"
                exit 0
            fi
        fi
        echo "push failed this round"
    else
        echo "probe failed (remote unreachable)"
    fi

    round=$((round + 1))
    [ "$round" -le "$ROUNDS" ] && sleep "$WAIT_SEC"
done

echo "PUSH_EXHAUSTED after $ROUNDS rounds" >&2
exit 1
