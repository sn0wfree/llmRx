"""Dual-implementation diff (P20 §3): Go client vs Python client.

The Go client is in maintenance mode; this is the guard against silent
drift between the two implementations. The same request sequence is
driven against each implementation against its own identical mock, and
the observable behaviour is compared after masking the documented
divergences (P18 §5):

  * breaker counts (Go 2 effective vs Py 3 on 5xx) -> no consecutive 5xx
  * cache_enabled / max_inflight enforcement -> identical caching setup
  * chunked decoding, 413 vs truncation -> not exercised here
  * Date/Server duplication -> header comparison ignores them
  * request_id nonce shapes -> compare field sets/values, not ids

Skipped entirely when no Go toolchain is available.
"""

from __future__ import annotations

import http.client
import json
import os
import shutil
import subprocess
import tempfile
import threading
import time
import unittest
import urllib.request

from tests.helpers import MockUpstream

REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", ".."))

CANNED = [
    (200, {}, json.dumps({
        "choices": [{"message": {"role": "assistant", "content": "ok"}}],
        "usage": {"prompt_tokens": 5, "completion_tokens": 7}}).encode()),
    (200, {}, b'{"data":[]}'),
    (401, {}, b'{"error":{"message":"bad key"}}'),
]


def _find_go() -> str | None:
    found = shutil.which("go")
    if found:
        return found
    for candidate in ("/home/ll/.local/go/bin/go", "/usr/local/go/bin/go"):
        if os.path.exists(candidate):
            return candidate
    return None


def _sequence(base: str) -> list[tuple[str, int, bytes | None]]:
    """The identical request sequence each implementation must serve."""
    body = json.dumps({"model": "gpt-4o-mini", "temperature": 0,
                       "messages": [{"role": "user",
                                     "content": "diff"}]}).encode()
    return [
        ("POST", "/v1/chat/completions", body),  # fresh -> upstream 200
        ("POST", "/v1/chat/completions", body),  # cache hit (both)
        ("GET", "/v1/models", None),             # passthrough 200
        ("GET", "/nope", b"anything"),           # upstream 401 passthrough
    ]


def _drive(base: str) -> dict:
    """Run the sequence; return status/error-shape/cache-marker/healthz."""
    port = int(base.rsplit(":", 1)[1])
    out = {"responses": [], "healthz": {}}
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=15)
    try:
        for method, path, body in _sequence(base):
            hdr = {"Content-Type": "application/json"}
            conn.request(method, path, body=body, headers=hdr)
            r = conn.getresponse()
            data = r.read()
            if path == "/healthz":
                out["healthz"] = json.loads(data)
                continue
            err_shape = None
            if r.status >= 400:
                try:
                    err_shape = sorted(json.loads(data)["error"].keys())
                except (ValueError, KeyError):
                    err_shape = ["<unparseable>"]
            out["responses"].append(
                (r.status, err_shape, r.getheader("X-Llmrx-Cache")))
    finally:
        conn.close()
    return out


def _build_go_client() -> str:
    """Build cmd/gateway-client once per run; cache by mtime."""
    go = _find_go()
    out = os.path.join(tempfile.gettempdir(), "gateway-client-diff")
    marker = os.path.join(REPO, "cmd", "gateway-client")
    if os.path.exists(out) and (not os.path.exists(marker)
                                or os.path.getmtime(out) > os.path.getmtime(marker)):
        return out
    env = dict(os.environ, CGO_ENABLED="0")
    r = subprocess.run([go, "build", "-o", out, "./cmd/gateway-client"],
                       cwd=REPO, capture_output=True, text=True,
                       timeout=300, env=env)
    if r.returncode != 0:
        raise RuntimeError(f"go build failed: {r.stderr[-400:]}")
    return out


@unittest.skipIf(_find_go() is None, "Go toolchain not available")
class TestGoPythonDiff(unittest.TestCase):
    def test_observable_behavior_matches(self):
        go_bin = _build_go_client()
        work = tempfile.mkdtemp(prefix="llmrx-diff-")
        results = {}

        # ---- Python ----
        from llmrx_client.config import Config
        from llmrx_client.proxy import Proxy, serve
        from llmrx_client.usage import UsageBuffer, start_recorder

        with MockUpstream(CANNED) as py_up:
            cfg = Config.from_dict({
                "listen": "127.0.0.1:19101",
                "servers": [py_up.url],
                "cache_enabled": True, "cache_ttl_sec": 3600,
                "fallback_mode": "cache_then_fail"})
            cfg.normalize()
            buf = UsageBuffer(5000)
            proxy = Proxy(cfg, "diff", buffer=buf)
            server = serve(cfg, proxy)
            server.daemon_threads = True
            stop = threading.Event()
            threading.Thread(target=server.serve_forever, daemon=True).start()
            # The recorder gives the Go-parity local-file behaviour; the
            # Go run below has the same 5s persist tick.
            store = None
            try:
                store = __import__("llmrx_client.usage",
                                   fromlist=["JSONLUsageStore"]).JSONLUsageStore(
                                       work + "/py-usage.jsonl")
                start_recorder(buf, store, stop)
                time.sleep(0.3)
                results["python"] = _drive("http://127.0.0.1:19101")
                time.sleep(5.5)  # one Go-parity persist tick
            finally:
                stop.set()
                server.shutdown()
                server.server_close()
                if store is not None:
                    store.close()
            py_rows = [r.to_json() for r in buf.take_all()]

        # ---- Go ----
        go_bin = _build_go_client()
        with MockUpstream(list(CANNED)) as go_up:
            cfg = os.path.join(work, "go-client.yml")
            with open(cfg, "w") as fh:
                fh.write(f"listen: 127.0.0.1:19102\n"
                         f"servers:\n  - {go_up.url}\n"
                         "cache_enabled: true\n"
                         "cache_ttl_sec: 3600\n"
                         "fallback_mode: cache_then_fail\n"
                         f"usage_file: {work}/go-usage.jsonl\n"
                         "usage_upload_sec: 3600\n")
            log = open(os.path.join(work, "go.log"), "w")
            err = open(os.path.join(work, "go.err"), "w")
            proc = subprocess.Popen([go_bin, "-config", cfg],
                                    stdout=log, stderr=err)
            try:
                base = "http://127.0.0.1:19102"
                up_ok = False
                for _ in range(40):
                    try:
                        with urllib.request.urlopen(base + "/healthz",
                                                    timeout=2) as r:
                            if r.status == 200:
                                up_ok = True
                                break
                    except OSError:
                        time.sleep(0.5)
                if not up_ok:
                    with open(go_bin, "rb") as bf:
                        magic = bf.read(4)
                    log_txt = open(
                        os.path.join(work, "go.log"),
                        encoding="utf-8", errors="replace").read()
                    err_txt = open(
                        os.path.join(work, "go.err"),
                        encoding="utf-8", errors="replace").read()
                    cfg_txt = open(cfg, encoding="utf-8").read()
                    self.fail(
                        f"Go client never came up; exit={proc.poll()} "
                        f"bin_size={os.path.getsize(go_bin)} "
                        f"magic={magic!r}\nARGV was: {[go_bin, '-config', cfg]}"
                        f"\ncfg content:\n{cfg_txt}\nLOG:\n{log_txt}\nERR:\n{err_txt}")
                results["go"] = _drive(base)
                time.sleep(5.5)  # one Go-parity persist tick
            finally:
                proc.terminate()
                try:
                    proc.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    proc.kill()
                log.close()
                err.close()
            go_rows = [json.loads(l)
                       for l in open(work + "/go-usage.jsonl",
                                     encoding="utf-8") if l.strip()]

        # ---- diff ----
        py, go = results["python"], results["go"]
        for i, (p_row, g_row) in enumerate(zip(py["responses"],
                                               go["responses"])):
            p_status, p_err, p_cache = p_row
            g_status, g_err, g_cache = g_row
            self.assertEqual(p_status, g_status, f"response {i}: status")
            self.assertEqual(p_err, g_err, f"response {i}: error shape")
            self.assertEqual(p_cache, g_cache, f"response {i}: cache marker")
        self.assertEqual(len(py["responses"]), len(go["responses"]))

        self.assertEqual(sorted(py["healthz"].keys()),
                         sorted(go["healthz"].keys()))

        self.assertEqual([r["source"] for r in py_rows],
                         [r["source"] for r in go_rows])
        self.assertEqual([r["status"] for r in py_rows],
                         [r["status"] for r in go_rows])
        self.assertEqual(sorted(py_rows[0].keys()),
                         sorted(go_rows[0].keys()))


if __name__ == "__main__":
    unittest.main()
