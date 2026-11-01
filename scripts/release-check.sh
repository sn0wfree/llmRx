#!/bin/sh
# scripts/release-check.sh — pre-flight checks before cutting a tag.
#
# Run this on a clean master checkout. It refuses to pass when:
#   - the working tree is dirty
#   - CHANGELOG.md still has an unreleased section (it must be renamed
#     to the version being released)
#   - the tag already exists
#   - the Go version in go.mod does not match the CI version
#
# Usage:
#   scripts/release-check.sh v0.1.0

set -eu

VERSION="${1:-}"
if [ -z "$VERSION" ]; then
    echo "usage: $0 <version>   e.g. $0 v0.1.0" >&2
    exit 2
fi

case "$VERSION" in
    v[0-9]*.[0-9]*.[0-9]*) ;;
    *) echo "FAIL: version must look like vMAJOR.MINOR.PATCH (got '$VERSION')" >&2; exit 1 ;;
esac

fail=0

# 1. Clean tree
if [ -n "$(git status --porcelain)" ]; then
    echo "FAIL: working tree is dirty — commit or stash first"
    git status --short
    fail=1
else
    echo "ok: working tree clean"
fi

# 2. Tag must not exist
if git rev-parse -q --verify "refs/tags/$VERSION" >/dev/null 2>&1; then
    echo "FAIL: tag $VERSION already exists"
    fail=1
else
    echo "ok: tag $VERSION is free"
fi

# 3. CHANGELOG must be cut for this version
if [ -f CHANGELOG.md ]; then
    if grep -q "^## \[$VERSION\]" CHANGELOG.md 2>/dev/null; then
        echo "ok: CHANGELOG.md has a [$VERSION] section"
    elif grep -q "^## \[Unreleased\]" CHANGELOG.md 2>/dev/null; then
        echo "FAIL: CHANGELOG.md still has an [Unreleased] section — rename it to [$VERSION]"
        fail=1
    else
        echo "FAIL: CHANGELOG.md has neither [Unreleased] nor [$VERSION]"
        fail=1
    fi
else
    echo "FAIL: CHANGELOG.md missing"
    fail=1
fi

# 4. go.mod version must match CI
if [ -f go.mod ]; then
    MODVER=$(awk '/^go /{print $2; exit}' go.mod)
    CIVER=$(awk '/go-version:/{gsub(/["'"'"']/,"",$2); print $2; exit}' .github/workflows/test.yml 2>/dev/null || echo "")
    if [ -n "$CIVER" ] && [ "$MODVER" != "$CIVER" ]; then
        echo "FAIL: go.mod says go $MODVER but CI uses $CIVER"
        fail=1
    else
        echo "ok: go version aligned ($MODVER)"
    fi
else
    echo "FAIL: go.mod missing"
    fail=1
fi

# 5. Tests must pass (skipped with --fast)
if [ "${2:-}" != "--fast" ]; then
    echo ">>> running tests (pass --fast as 2nd arg to skip)"
    if go test ./... > /tmp/release-check-test.log 2>&1; then
        echo "ok: go test ./..."
    else
        echo "FAIL: go test ./... failed (see /tmp/release-check-test.log)"
        tail -20 /tmp/release-check-test.log
        fail=1
    fi
fi

if [ "$fail" -ne 0 ]; then
    echo
    echo "release-check: FAILED — fix the items above before tagging"
    exit 1
fi

echo
echo "release-check: all clear. Next:"
echo "  git tag $VERSION && git push origin $VERSION"
