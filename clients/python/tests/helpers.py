"""Shared test fixtures: mock upstream, mock clientapi server, proxy harness.

Everything is stdlib: http.server in threads, real sockets on ephemeral
ports. No monkeypatching of the proxy internals — these are black-box
tests over HTTP, mirroring how the Go suite drives its client.
"""

from __future__ import annotations

import hashlib
import hmac
import http.client
import json
import os
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from llmrx_client.config import Config
from llmrx_client.proxy import Proxy, make_server
from llmrx_client.usage import UsageBuffer


# ---------------------------------------------------------------------------
# mock upstream (stands in for the llmRx server's data plane)
# ---------------------------------------------------------------------------


class MockUpstream:
    """Records every request; serves a scripted list of responses.

    responses: list of (status, headers, body). Popped per request; the
    last one repeats. body may be bytes or a callable(request_dict) -> bytes.
    """

    def __init__(self, responses=None):
        self.requests: list[dict] = []
        self.responses = list(responses or [])
        # Gate: set (open) by default so requests proceed. clear() it to
        # make handlers block until set again — used to pin a request
        # in-flight for the max_inflight test.
        self.gate = threading.Event()
        self.gate.set()
        self._lock = threading.Lock()
        outer = self

        class H(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, fmt, *args):
                pass

            def _handle(self):
                length = int(self.headers.get("Content-Length") or 0)
                body = self.rfile.read(length) if length else b""
                if not outer.gate.is_set():
                    outer.gate.wait(10)
                with outer._lock:
                    outer.requests.append({
                        "method": self.command,
                        "path": self.path,
                        "headers": {k: v for k, v in self.headers.items()},
                        "body": body,
                    })
                    idx = min(len(outer.requests) - 1, len(outer.responses) - 1)
                    if outer.responses and idx >= 0:
                        status, headers, resp_body = outer.responses[
                            min(idx, len(outer.responses) - 1)]
                        if callable(resp_body):
                            resp_body = resp_body(outer.requests[-1])
                    else:
                        status, headers, resp_body = 200, {}, b"{}"
                if isinstance(resp_body, str):
                    resp_body = resp_body.encode()
                self.send_response(status)
                for k, v in (headers or {}).items():
                    self.send_header(k, v)
                self.send_header("Content-Length", str(len(resp_body)))
                self.end_headers()
                if self.command != "HEAD":
                    self.wfile.write(resp_body)

            do_GET = _handle
            do_POST = _handle
            do_PUT = _handle
            do_DELETE = _handle

        self._server = ThreadingHTTPServer(("127.0.0.1", 0), H)
        self._server.daemon_threads = True
        self.port = self._server.server_address[1]
        self.url = f"http://127.0.0.1:{self.port}"
        self._thread = threading.Thread(target=self._server.serve_forever,
                                        daemon=True)

    def __enter__(self):
        self._thread.start()
        return self

    def __exit__(self, *exc):
        self._server.shutdown()
        self._server.server_close()

    def chat_response(self, content="hi", usage=None):
        doc = {
            "id": "chatcmpl-x",
            "object": "chat.completion",
            "model": "gpt-test",
            "choices": [{"index": 0, "message": {"role": "assistant",
                                                 "content": content},
                         "finish_reason": "stop"}],
        }
        if usage is not None:
            doc["usage"] = usage
        return 200, {"Content-Type": "application/json"}, json.dumps(doc)


def chat_request(content="hi", **extra) -> bytes:
    doc = {"model": "gpt-test", "temperature": 0,
           "messages": [{"role": "user", "content": content}]}
    doc.update(extra)
    return json.dumps(doc).encode()


# ---------------------------------------------------------------------------
# mock clientapi server (enroll / heartbeat / config / usage)
# ---------------------------------------------------------------------------


def sign_bundle(bundle: dict, token: str) -> bytes:
    """Produce a Go-shaped signed envelope: the signature covers the raw
    serialized config bytes (field order preserved, compact separators)."""
    raw = json.dumps(bundle, separators=(",", ":"), ensure_ascii=False).encode()
    sig = hmac.new(token.encode(), raw, hashlib.sha256).hexdigest()
    return json.dumps(
        {"config": bundle, "signature": sig, "algorithm": "hmac-sha256"},
        separators=(",", ":"),
    ).encode()


class MockServerAPI:
    """The /client/v1 surface: enroll, heartbeat, config, usage."""

    def __init__(self, client_token: str = "ecs_test_secret", bundle: dict | None = None):
        self.client_token = client_token
        self.bundle = bundle or {
            "version": 1,
            "issued_at": "2026-11-02T09:46:00.123456789Z",
            "cache": {"enabled": True, "ttl_sec": 3600, "max_items": 2000},
            "fallback": {"mode": "cache_then_fail"},
            "limits": {"max_inflight": 64},
        }
        self.need_config = True
        self.heartbeats: list[dict] = []
        self.usage_batches: list[dict] = []
        self.enroll_requests: list[dict] = []
        self._lock = threading.Lock()
        outer = self

        class H(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, fmt, *args):
                pass

            def _reply(self, status: int, doc):
                body = json.dumps(doc).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_POST(self):
                length = int(self.headers.get("Content-Length") or 0)
                body = self.rfile.read(length) if length else b""
                if self.path == "/client/v1/heartbeat":
                    with outer._lock:
                        try:
                            doc = json.loads(body) if body else {}
                        except ValueError:
                            doc = {}
                        outer.heartbeats.append(doc)
                        version = outer.bundle.get("version", 1)
                        applied = doc.get("config_version", 0)
                        need = applied != version
                        self._reply(200, {"server_version": version,
                                          "need_config": need,
                                          "server_time": "2026-11-02T09:46:00Z"})
                    return
                if self.path == "/client/v1/usage":
                    if self.headers.get("Authorization") != f"Bearer {outer.client_token}":
                        self._reply(401, {"error": {
                            "code": "invalid_client",
                            "message": "unknown edge client token"}})
                        return
                    with outer._lock:
                        try:
                            doc = json.loads(body)
                        except ValueError:
                            self._reply(400, {"error": {"message": "invalid body"}})
                            return
                        outer.usage_batches.append(doc)
                    self._reply(200, {"accepted": len(doc.get("records") or []),
                                      "duplicates": 0,
                                      "client_name": doc.get("client_name")})
                    return
                if self.path == "/client/v1/enroll":
                    with outer._lock:
                        try:
                            doc = json.loads(body)
                        except ValueError:
                            self._reply(400, {"error": {"message": "invalid body"}})
                            return
                        outer.enroll_requests.append(doc)
                    if doc.get("enroll_token") == "good-code":
                        self._reply(201, {"client_id": "ec_abcd1234567890ef",
                                          "client_secret": "ecs_new_secret",
                                          "name": "py-edge"})
                    else:
                        self._reply(401, {"error": {
                            "code": "invalid_enroll_token",
                            "message": "enroll token is invalid, expired or already used"}})
                    return
                self._reply(404, {"error": {"message": "not found"}})

            def do_GET(self):
                if self.path == "/client/v1/config":
                    if self.headers.get("Authorization") != f"Bearer {outer.client_token}":
                        self._reply(401, {"error": {
                            "code": "invalid_client",
                            "message": "unknown edge client token"}})
                        return
                    raw = json.dumps(outer.bundle, separators=(",", ":"),
                                     ensure_ascii=False).encode()
                    sig = hmac.new(outer.client_token.encode(), raw,
                                   hashlib.sha256).hexdigest()
                    body = json.dumps(
                        {"config": outer.bundle, "signature": sig,
                         "algorithm": "hmac-sha256"},
                        separators=(",", ":")).encode()
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Cache-Control", "no-store")
                    self.send_header("Content-Length", str(len(body)))
                    self.end_headers()
                    self.wfile.write(body)
                    return
                self._reply(404, {"error": {"message": "not found"}})

        self._server = ThreadingHTTPServer(("127.0.0.1", 0), H)
        self._server.daemon_threads = True
        self.url = f"http://127.0.0.1:{self._server.server_address[1]}"
        self._thread = threading.Thread(target=self._server.serve_forever,
                                        daemon=True)

    def __enter__(self):
        self._thread.start()
        return self

    def __exit__(self, *exc):
        self._server.shutdown()
        self._server.server_close()


# ---------------------------------------------------------------------------
# proxy harness
# ---------------------------------------------------------------------------


class ClientHarness:
    """A real Proxy served on an ephemeral port with the production UI
    wiring, plus a small HTTP client. Tests exercise exactly what the
    CLI serves."""

    def __init__(self, cfg: Config, control=None, cfg_path: str | None = None):
        self.cfg = cfg
        if not cfg.listen:
            cfg.listen = "127.0.0.1:0"
        self.buffer = UsageBuffer(5000)
        self.proxy = Proxy(cfg, "test (none, unknown, edge)", buffer=self.buffer,
                           control=control)
        import tempfile
        if cfg_path is None:
            cfg_path = os.path.join(tempfile.mkdtemp(prefix="llmrx-test-"),
                                    "client.yml")
        self.proxy.cfg_path = cfg_path
        from llmrx_client.proxy import serve
        self.server = serve(cfg, self.proxy)
        self.server.daemon_threads = True
        self.port = self.server.server_address[1]
        self.base = f"http://127.0.0.1:{self.port}"
        self._thread = threading.Thread(target=self.server.serve_forever,
                                        daemon=True)
        self._thread.start()

    def stop(self):
        self.server.shutdown()
        self.server.server_close()

    def request(self, method: str, path: str, body: bytes | None = None,
                headers: dict | None = None):
        conn = http.client.HTTPConnection("127.0.0.1", self.port, timeout=10)
        try:
            conn.request(method, path, body=body, headers=headers or {})
            resp = conn.getresponse()
            data = resp.read()
            return resp.status, dict(resp.getheaders()), data
        finally:
            conn.close()

    def chat(self, body: bytes, headers: dict | None = None):
        return self.request("POST", "/v1/chat/completions", body,
                            {"Content-Type": "application/json", **(headers or {})})


def make_cfg(servers: list[str], **overrides) -> Config:
    cfg = Config(listen="127.0.0.1:0", servers=servers, cache_enabled=True,
                 cache_ttl_sec=3600, cache_max_items=2000,
                 usage_upload_sec=60, client_name="test-edge")
    for k, v in overrides.items():
        setattr(cfg, k, v)
    return cfg
