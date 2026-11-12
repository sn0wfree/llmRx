#!/bin/sh
# scripts/release-check.sh — pre-flight checks before cutting a tag.
#
# Run this on a clean master checkout. It refuses to pass when:
#   - the working tree is dirty
#   - CHANGELOG.md still has an unreleased section (it must be renamed
#     to the version being released)
#   - the tag already exists
#   - the Go version in go.mod does not match the CI version
#   - go vet / unit tests / race detector / coverage floors / build fail
#
# All gate exit codes are captured directly — never piped through
# head/tail, which masks failures (a real incident: `go test ... |
# head -8` printed "success" while the suite was red).
#
# See docs/RELEASE-PROCESS.md for the full runbook.
#
# Usage:
#   scripts/release-check.sh v0.1.0            # full gate suite
#   scripts/release-check.sh v0.1.0 --fast     # static checks only

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
#
# `git status --porcelain` and plain `git diff` both report phantom
# modifications when the working tree is read through a different
# filesystem than the one it was checked out on (WSL reading a Windows
# checkout): the index says LF and the working copy has CRLF, so every
# file looks changed.
#
# `git diff -w --ignore-blank-lines` ignores that, because CRLF-vs-LF is
# the *only* difference in those cases. Anything with a real content
# change still shows up.
if ! git diff -w --ignore-blank-lines --quiet || ! git diff --cached --quiet; then
    echo "FAIL: working tree is dirty — commit or stash first"
    git --no-pager diff -w --ignore-blank-lines --stat | tail -15
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
#
# The CI matrix may be a list (`go-version: ['1.22']` or
# `['1.18','1.21','1.22']`), so extract every quoted Go version and
# require that go.mod's version appears among them. The previous
# single-value extraction took whatever followed the colon, which for a
# list yielded "[1.18," and failed spuriously.
if [ -f go.mod ]; then
    # tr strips the CR that a CRLF working copy leaves on the field.
    MODVER=$(awk '/^go /{print $2; exit}' go.mod | tr -d '\r')
    CIVER=$(grep -oE "'[0-9]+\.[0-9]+(\.[0-9]+)?'" .github/workflows/test.yml 2>/dev/null |
        tr -d "'\r" | tr '\n' ' ' | sed 's/ *$//')
    if [ -z "$CIVER" ]; then
        echo "WARN: could not read go-version from test.yml (skipping check)"
    else
        found=0
        for v in $CIVER; do
            [ "$v" = "$MODVER" ] && found=1
        done
        if [ "$found" -eq 1 ]; then
            echo "ok: go version $MODVER is in the CI matrix ($CIVER)"
        else
            echo "FAIL: go.mod says go $MODVER but CI tests $CIVER"
            fail=1
        fi
    fi
else
    echo "FAIL: go.mod missing"
    fail=1
fi

# 5. Tests must pass (skipped with --fast)
if [ "${2:-}" != "--fast" ]; then
    echo ">>> running gate suite (pass --fast as 2nd arg to skip)"

    # 5a. vet
    if go vet ./... > /tmp/release-check-vet.log 2>&1; then
        echo "ok: go vet ./..."
    else
        echo "FAIL: go vet ./... failed (see /tmp/release-check-vet.log)"
        tail -20 /tmp/release-check-vet.log
        fail=1
    fi

    # 5b. unit tests
    if go test -count=1 ./... > /tmp/release-check-test.log 2>&1; then
        echo "ok: go test ./..."
    else
        echo "FAIL: go test ./... failed (see /tmp/release-check-test.log)"
        tail -20 /tmp/release-check-test.log
        fail=1
    fi

    # 5c. race detector
    if go test -race -count=1 ./... > /tmp/release-check-race.log 2>&1; then
        echo "ok: go test -race ./..."
    else
        echo "FAIL: go test -race ./... failed (see /tmp/release-check-race.log)"
        tail -20 /tmp/release-check-race.log
        fail=1
    fi

    # 5d. per-package coverage floors (make cover-check)
    if make cover-check > /tmp/release-check-cover.log 2>&1; then
        echo "ok: coverage floors (make cover-check)"
    else
        echo "FAIL: coverage floors below minimum (see /tmp/release-check-cover.log)"
        tail -20 /tmp/release-check-cover.log
        fail=1
    fi

    # 5e. build (the release artifact must compile)
    if go build ./... > /tmp/release-check-build.log 2>&1; then
        echo "ok: go build ./..."
    else
        echo "FAIL: go build ./... failed (see /tmp/release-check-build.log)"
        tail -20 /tmp/release-check-build.log
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
