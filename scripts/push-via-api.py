#!/usr/bin/env python3
"""scripts/push-via-api.py — push refs to GitHub through the REST API.

Why this exists
---------------
Some corporate networks filter GitHub by SNI: `github.com` completes
neither the TLS handshake nor a long-running git transfer (symptoms:
"GnuTLS recv error (-110)", "Recv failure: Connection was reset",
pushes hanging for minutes), while `api.github.com` answers
reliably in well under a second. When `git push` cannot get through,
this script uploads the same objects through the Git Database API
instead.

Safety
-------
Every object created via the API is compared against the local SHA.
A mismatch aborts BEFORE any ref is moved, so the remote history can
never diverge from the local one. The script also refuses to
force-update a branch (fast-forward only).

Usage
-----
    export GH_TOKEN=<token with repo scope>       # or: gh auth token
    scripts/push-via-api.py                       # branch + all local tags
    scripts/push-via-api.py master v0.3.1         # explicit targets
    scripts/push-via-api.py --branch-only master

Get a token with:
    gh auth token            (GitHub CLI, already logged in)
or https://github.com/settings/tokens (scope: repo)

Note on GitHub's timestamp normalization: the create-commit API
stores the epoch shifted by the timezone offset while keeping the
original tz label (a local `1794378884 +0800` comes back as
`1794350084 +0800`). The script compensates by trying the
equivalent renderings and keeping the one whose SHA matches the
local object.
"""
import base64
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request

REPO = os.environ.get("GH_REPO", "sn0wfree/llmrx")
API = "https://api.github.com"
REPO_DIR = os.environ.get("REPO_DIR", os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
BRANCH = os.environ.get("GH_BRANCH", "master")


def git(*args):
    return subprocess.run(
        ["git", "-C", REPO_DIR] + list(args),
        capture_output=True, check=True).stdout


def api(path, method="GET", body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(
        API + path, data=data, method=method,
        headers={
            "Authorization": f"Bearer {TOKEN}",
            "Accept": "application/vnd.github+json",
            "X-GitHub-Api-Version": "2022-11-28",
            "User-Agent": "llmrx-release",
            "Content-Type": "application/json",
        })
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            raw = r.read()
    except urllib.error.HTTPError as e:
        raise SystemExit(
            f"FATAL: {method} {path} -> HTTP {e.code}\n{e.read().decode()[:500]}")
    return json.loads(raw) if raw else {}


def create_blob(sha):
    b64 = base64.b64encode(git("cat-file", "blob", sha)).decode()
    res = api(f"/repos/{REPO}/git/blobs", "POST",
              {"content": b64, "encoding": "base64"})
    if res["sha"] != sha:
        raise SystemExit(f"FATAL: blob sha mismatch {res['sha']} != {sha}")


def create_tree(sha):
    entries = []
    for line in git("ls-tree", sha).decode().splitlines():
        meta, path = line.split("\t", 1)
        mode, typ, sha_ = meta.split()
        entries.append({"path": path, "mode": mode, "type": typ, "sha": sha_})
    res = api(f"/repos/{REPO}/git/trees", "POST", {"tree": entries})
    if res["sha"] != sha:
        raise SystemExit(f"FATAL: tree sha mismatch {res['sha']} != {sha}")


def parse_ident(line):
    """'author Name <mail> 1700000000 +0800' → ident + ISO date candidates."""
    who, date = line.rsplit(">", 1)
    name, mail = who.split("<", 1)
    ts, tz = date.strip().split(" ", 1)
    off = int(tz[1:3]) * 3600 + int(tz[3:5]) * 60
    if tz[0] == "-":
        off = -off
    iso_tz = f"{tz[:3]}:{tz[3:]}"
    cands = []
    for delta in (0, off, -off):
        wall = time.gmtime(int(ts) + delta)
        cands.append(time.strftime("%Y-%m-%dT%H:%M:%S", wall) + iso_tz)
    return {"name": name.strip(), "email": mail}, cands


def create_commit(sha):
    raw = git("cat-file", "commit", sha).decode()
    head, _, message = raw.partition("\n\n")
    author = committer = None
    a_cands = c_cands = []
    parents = []
    for line in head.splitlines():
        if line.startswith("author "):
            author, a_cands = parse_ident(line[len("author "):])
        elif line.startswith("committer "):
            committer, c_cands = parse_ident(line[len("committer "):])
        elif line.startswith("parent "):
            parents.append(line.split()[1])
    tree = git("rev-parse", f"{sha}^{{tree}}").decode().strip()

    msgs = [message]
    stripped = message.rstrip("\n")
    if stripped != message:
        msgs += [stripped, stripped + "\n"]

    last = None
    for adate in a_cands:
        for cdate in c_cands:
            for msg in msgs:
                res = api(f"/repos/{REPO}/git/commits", "POST", {
                    "message": msg, "tree": tree, "parents": parents,
                    "author": {**author, "date": adate},
                    "committer": {**committer, "date": cdate}})
                last = res["sha"]
                if last == sha:
                    return
    raise SystemExit(f"FATAL: commit sha mismatch {last} != {sha}")


def push_branch(name):
    try:
        remote = api(f"/repos/{REPO}/git/ref/heads/{name}")["object"]["sha"]
    except SystemExit:
        remote = None            # branch does not exist on the remote yet
    head = git("rev-parse", name).decode().strip()
    print(f"branch {name}: remote={(remote or 'none')[:8]} local={head[:8]}")
    if remote == head:
        print("  already up to date")
        return
    if remote is not None and \
            git("merge-base", "--is-ancestor", remote, name).returncode != 0:
        raise SystemExit(f"FATAL: {name} is not a fast-forward of {remote}")

    commits = git("rev-list", "--reverse", f"{remote}..{head}").decode().split()

    blobs, trees = [], set()
    for line in git("rev-list", "--reverse", "--objects", f"{remote}..{head}").decode().splitlines():
        sha = line.split(" ", 1)[0]
        kind = git("cat-file", "-t", sha).decode().strip()
        if kind == "blob":
            blobs.append(sha)
        elif kind == "tree":
            trees.add(sha)

    depths = {}
    for c in commits:
        root = git("rev-parse", f"{c}^{{tree}}").decode().strip()
        trees.add(root)
        for entry in git("ls-tree", "-r", "-t", root).decode().splitlines():
            meta, path = entry.split("\t", 1)
            mode, typ, sha = meta.split()
            if typ == "tree":
                depths[sha] = max(depths.get(sha, 0), path.count("/") + 1)
            elif sha not in depths:
                depths[sha] = 0

    for i, b in enumerate(blobs, 1):
        create_blob(b)
        if i % 25 == 0:
            print(f"  blobs {i}/{len(blobs)}", flush=True)
    print(f"  blobs ok: {len(blobs)}")

    for i, t in enumerate(sorted(trees, key=lambda t: -depths.get(t, 0)), 1):
        create_tree(t)
        if i % 10 == 0:
            print(f"  trees {i}/{len(trees)}", flush=True)
    print(f"  trees ok: {len(trees)}")

    for i, c in enumerate(commits, 1):
        create_commit(c)
        print(f"  commit {i}/{len(commits)} {c[:8]}", flush=True)

    api(f"/repos/{REPO}/git/refs/heads/{name}", "PATCH",
        {"sha": head, "force": False})
    print(f"  branch {name} -> {head[:8]}")


def push_tag(tag):
    # already on the remote? then nothing to do
    # (the endpoint returns a bare list of refs)
    existing = api(f"/repos/{REPO}/git/refs/tags")
    if isinstance(existing, dict):
        existing = existing.get("refs", [])
    for r in existing:
        if r.get("ref") == f"refs/tags/{tag}":
            print(f"  tag {tag} already on remote ({r['object']['sha'][:8]})")
            return
    tag_sha = git("rev-parse", tag).decode().strip()
    kind = git("cat-file", "-t", tag_sha).decode().strip()
    if kind == "tag":
        raw = git("cat-file", "tag", tag_sha).decode()
        head, _, message = raw.partition("\n\n")
        obj = git("rev-parse", f"{tag_sha}^{{}}").decode().strip()
        tagger, cands = parse_ident(
            [l for l in head.splitlines() if l.startswith("tagger ")][0][len("tagger "):])
        msgs = [message]
        stripped = message.rstrip("\n")
        if stripped != message:
            msgs += [stripped, stripped + "\n"]
        last = None
        for date in cands:
            for msg in msgs:
                res = api(f"/repos/{REPO}/git/tags", "POST", {
                    "tag": tag, "message": msg, "object": obj,
                    "type": "commit", "tagger": {**tagger, "date": date}})
                last = res["sha"]
                if last == tag_sha:
                    break
            if last == tag_sha:
                break
        if last != tag_sha:
            raise SystemExit(f"FATAL: tag {tag} sha mismatch {last} != {tag_sha}")
        ref_sha = tag_sha
    else:
        ref_sha = tag_sha
    api(f"/repos/{REPO}/git/refs", "POST",
        {"ref": f"refs/tags/{tag}", "sha": ref_sha})
    print(f"  tag {tag} -> {ref_sha[:8]}")


def main():
    global TOKEN
    TOKEN = os.environ.get("GH_TOKEN", "").strip()
    if not TOKEN:
        raise SystemExit("GH_TOKEN is required (gh auth token)")
    args = sys.argv[1:]
    branch_only = False
    if args and args[0] == "--branch-only":
        branch_only = True
        args = args[1:]
    if not args:
        args = [BRANCH]
    branch = args[0]
    tags = args[1:]

    push_branch(branch)
    if not branch_only:
        if not tags:
            tags = git("tag", "-l").decode().split()
        for t in tags:
            push_tag(t)
    print("done")


if __name__ == "__main__":
    main()
