"""The data plane: local OpenAI-compatible endpoint.

Mirrors internal/client/proxy.go. Request flow for
POST /v1/chat/completions: cache lookup (non-streaming, temperature 0) →
sticky failover dial → cache fill on 200 → fallback chain (cache → 503 /
5xx passthrough). /healthz is local; everything else streams through
with all upstream headers preserved.

Deliberate divergences from the Go client (docs/P18 §5): the request
body is always forwarded (Go drops it for streaming/passthrough); the
breaker counts at most once per request; cache_enabled and max_inflight
are enforced.
"""

from __future__ import annotations

import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from .breaker import Breaker
from .cache import Entry, MemoryCache, TemperaturePositive, canonical_key
from .config import Config, ConfigError
from .upstream import ServerIndex, attempt_servers, copy_request_headers

MAX_BODY = 32 << 20  # 32 MiB, matching io.LimitReader in the Go client
MAX_LINE = 64 * 1024  # chunk-size / trailer line cap
CHAT_PATH = "/v1/chat/completions"
STATUS_TIMEOUT = 408

# Hop-by-hop framing headers must not be copied: the server de-chunks
# before we read, so forwarding Transfer-Encoding would corrupt framing.
RESPONSE_SKIP = {"connection", "transfer-encoding", "keep-alive"}


def _json_bytes(obj) -> bytes:
    return json.dumps(obj, separators=(",", ":")).encode()


ERR_METHOD = _json_bytes(
    {"error": {"message": "chat/completions requires POST"}})
ERR_UNREACHABLE = _json_bytes(
    {"error": {"code": "client_upstream_unreachable",
               "message": "llmRx server unreachable"}})
ERR_UNREACHABLE_CACHED = _json_bytes(
    {"error": {"code": "client_upstream_unreachable",
               "message": "llmRx server unreachable and no cached response"}})
ERR_OVERLOADED = _json_bytes(
    {"error": {"code": "client_overloaded",
               "message": "client max_inflight exceeded"}})
ERR_TOO_LARGE = _json_bytes(
    {"error": {"code": "client_body_too_large",
               "message": "request body exceeds 32 MiB"}})

# Methods whose body is forwarded upstream. RFC 7231 allows DELETE to
# carry a body and real APIs use it.
BODY_METHODS = ("POST", "PUT", "PATCH", "DELETE")


def fallible(status: int, transport_err: bool) -> bool:
    """Whether the failure may be softened: transport errors, 408 and any
    5xx. Business errors (401, 429, other 4xx) are authoritative and pass
    through untouched."""
    return transport_err or status == STATUS_TIMEOUT or status >= 500


def _temp_zero(req: dict) -> bool:
    temperature = req.get("temperature")
    if temperature is None:
        return True
    return isinstance(temperature, (int, float)) and not isinstance(
        temperature, bool) and temperature == 0


class Proxy:
    """Owns cache, breaker, server index and usage buffer. No dialing
    happens in the constructor."""

    def __init__(self, cfg: Config, version: str, buffer=None, control=None) -> None:
        self.cfg = cfg
        self.version = version
        self.brk = Breaker(3, 30.0)
        self.index = ServerIndex()
        self.buffer = buffer
        self.control = control
        self.started_at = time.time()
        self.cfg_path = ""
        with cfg.mu():
            self.cache = MemoryCache(cfg.cache_max_items)
        self._inflight = 0
        self._inflight_mu = threading.Lock()
        self._inflight_cv = threading.Condition(self._inflight_mu)

    def uptime(self) -> float:
        return time.time() - self.started_at

    def server_breaker(self) -> tuple[bool, int]:
        return self.brk.state()

    def close(self, drain_sec: float = 15.0) -> None:
        """Wait for in-flight requests, mirroring the Go drain."""
        deadline = time.time() + drain_sec
        with self._inflight_cv:
            while self._inflight > 0:
                remaining = deadline - time.time()
                if remaining <= 0 or not self._inflight_cv.wait(remaining):
                    break


class Handler(BaseHTTPRequestHandler):
    """One instance per connection (ThreadingHTTPServer). The Proxy and
    the UI dispatcher live on the server object (self.server)."""

    protocol_version = "HTTP/1.1"
    server_version = "llmrx-client"
    sys_version = ""  # BaseHTTPRequestHandler would append " " + sys_version
    # Read timeout on the inbound socket. Without it a slow-loris client
    # that opens a connection and sends nothing pins a thread forever
    # (the Go server's ReadHeaderTimeout exists for the same reason).
    # 120s is generous enough for legitimate large/slow uploads.
    timeout = 120

    def version_string(self) -> str:
        # sys_version is empty; the default join would leave a trailing
        # space in the Server header.
        return self.server_version

    # -- plumbing ---------------------------------------------------------

    def log_message(self, fmt: str, *args) -> None:
        pass  # local endpoint; the usage rows are the audit trail

    @property
    def proxy(self) -> Proxy:
        return self.server.proxy  # type: ignore[attr-defined]

    def _inflight_enter(self) -> bool:
        p = self.proxy
        with p.cfg.mu():
            limit = p.cfg.max_inflight
        # _inflight_cv wraps _inflight_mu, so this single with-block is
        # the lock (acquiring the raw Lock here too would self-deadlock —
        # it is not reentrant).
        with p._inflight_cv:
            if limit and p._inflight >= limit:
                return False
            p._inflight += 1
            return True

    def _inflight_exit(self) -> None:
        p = self.proxy
        with p._inflight_cv:
            p._inflight -= 1
            p._inflight_cv.notify()

    def _read_body(self) -> bytes | None:
        """Read the request body, decoding chunked transfer encoding.

        Returns None when the body is too large: the caller must reject
        with 413 and close the connection, because a truncated read
        would leave the remainder of the body on the socket and corrupt
        the next request's framing on a keep-alive connection.
        """
        te = (self.headers.get("Transfer-Encoding") or "").lower()
        if "chunked" in te:
            return self._read_chunked()

        try:
            length = int(self.headers.get("Content-Length") or 0)
        except ValueError:
            length = 0
        if length <= 0:
            return b""
        if length > MAX_BODY:
            return None
        return self.rfile.read(length)

    def _read_chunked(self) -> bytes | None:
        """Decode a chunked body: size-line CRLF data CRLF ... 0-chunk
        trailers. Returns None on overflow (same contract as CL > MAX_BODY)."""
        chunks: list[bytes] = []
        total = 0
        while True:
            size_line = self.rfile.readline(MAX_LINE)
            if not size_line:
                return None  # truncated stream
            try:
                size = int(size_line.strip().split(b";")[0], 16)
            except ValueError:
                return None
            if size == 0:
                # Consume trailers up to the blank line.
                while True:
                    line = self.rfile.readline(MAX_LINE)
                    if line in (b"\r\n", b"\n", b""):
                        break
                break
            data = self.rfile.read(size)
            if len(data) < size:
                return None  # truncated stream
            total += len(data)
            if total > MAX_BODY:
                return None
            chunks.append(data)
            crlf = self.rfile.read(2)
            if crlf not in (b"\r\n", b"\n"):
                return None
        return b"".join(chunks)

    def _write_full(self, status: int, payload: bytes,
                    extra: dict | None = None,
                    content_type: str = "application/json") -> None:
        self._responded = True
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(payload)))
        for k, v in (extra or {}).items():
            self.send_header(k, v)
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(payload)

    # -- routing ----------------------------------------------------------

    def _route(self) -> None:
        if not self._inflight_enter():
            self._write_full(503, ERR_OVERLOADED)
            return
        try:
            path = self.path.split("?", 1)[0]
            if path == "/ui" or path.startswith("/ui/"):
                dispatch = self.server.ui_dispatch  # type: ignore[attr-defined]
                if dispatch is not None:
                    dispatch(self)
                    return
            if path == CHAT_PATH:
                self._handle_chat()
            elif path == "/healthz":
                self._handle_local_health()
            else:
                self._forward_raw()
        except (BrokenPipeError, ConnectionResetError):
            self.close_connection = True
        except Exception:
            # The Go server wraps everything in chi's Recoverer so a
            # handler bug yields a 500 instead of a bare connection
            # close; best-effort equivalent here. Once bytes have gone
            # out there is nothing sane to send, so only speak up when
            # the response has not started.
            import traceback
            traceback.print_exc()
            if not getattr(self, "_responded", False):
                try:
                    self._write_full(500, _json_bytes(
                        {"error": {"message": "client internal error"}}))
                except OSError:
                    self.close_connection = True
        finally:
            self._inflight_exit()

    do_GET = _route
    do_POST = _route
    do_PUT = _route
    do_PATCH = _route
    do_DELETE = _route
    do_HEAD = _route
    do_OPTIONS = _route

    # -- health -----------------------------------------------------------

    def _handle_local_health(self) -> None:
        p = self.proxy
        cfg = p.cfg
        open_b, fails = p.brk.state()
        out: dict = {
            "status": "ok",
            "server_open": open_b,
            "server_fails": fails,
            "pending_usage": len(p.buffer) if p.buffer is not None else 0,
            "cache": p.cache.stats(),
            "uptime_sec": int(p.uptime()),
        }
        if p.control is not None:
            out["config_version"] = p.control.applied_version()
            out["control_last_error"] = p.control.last_error()
            last_ok = p.control.last_ok()
            if last_ok:
                out["control_last_ok"] = last_ok
        body = json.dumps(out, sort_keys=True, separators=(",", ":")).encode()
        self._write_full(200, body)

    # -- chat -------------------------------------------------------------

    def _handle_chat(self) -> None:
        p = self.proxy
        cfg = p.cfg
        started = time.time()

        if self.command != "POST":
            self._write_full(405, ERR_METHOD)
            return
        try:
            body = self._read_body()
        except OSError as exc:
            self._write_full(400, _json_bytes(
                {"error": {"message": f"read body: {exc}"}}))
            return
        if body is None:
            # Oversized: framing requires closing, not truncating.
            self.close_connection = True
            self._write_full(413, ERR_TOO_LARGE)
            return

        try:
            req = json.loads(body) if body else None
            strict = isinstance(req, dict)
        except (ValueError, UnicodeDecodeError):
            req, strict = None, False

        model = (req.get("model") or "") if strict else ""
        streaming = bool(strict and req.get("stream"))
        cacheable = bool(strict and not streaming and _temp_zero(req))
        key = None
        if cacheable and cfg.cache_enabled:
            try:
                key = canonical_key(req)
            except TemperaturePositive:
                key = None

        # 1. online cache short-circuit
        if key is not None:
            entry = p.cache.get(key)
            if entry is not None:
                self._serve_cached(entry)
                self._record(started, source="cache", model=model,
                             status=200, tokens=entry.usage or {})
                return

        # 2. streaming chat goes through the raw passthrough — with the
        # already-read body (the Go client re-reads and gets nil).
        if streaming:
            self._forward_raw(record=True, model=model, body=body)
            return

        # 3. buffered forward
        resp, conn, _transport_err = self._attempt(body)
        status = resp.status if resp is not None else 0
        resp_body = b""
        if resp is not None:
            # Counted exactly once per request (Go counts buffered 5xx
            # twice: docs/P18 §5.2).
            if status < 500:
                p.brk.success()
            else:
                p.brk.failure()
            try:
                resp_body = resp.read(MAX_BODY + 1)[:MAX_BODY]
            except OSError:
                resp_body = b""
            finally:
                if conn is not None:
                    try:
                        conn.close()
                    except OSError:
                        pass

        # 4. cache fill on a clean 200 whose body is a JSON object
        if key is not None and resp is not None and status == 200:
            usage: dict | None = {}
            try:
                doc = json.loads(resp_body)
            except (ValueError, UnicodeDecodeError):
                doc = None
            if isinstance(doc, dict):
                raw_usage = doc.get("usage")
                if isinstance(raw_usage, dict):
                    usage = raw_usage
                p.cache.put(key, Entry(200, resp_body, usage=usage),
                            ttl_sec=cfg.cache_ttl_sec)

        # 5. success
        if resp is not None and not fallible(status, False):
            self._write_full(status, resp_body)
            self._record(started, source="proxy", model=model, status=status)
            return

        # 6. fallback chain — breaker already counted above
        if key is not None:
            entry = p.cache.get(key)
            if entry is not None:
                self._serve_cached(entry)
                self._record(started, source="cache", model=model,
                             status=200, tokens=entry.usage or {})
                return
        if resp is None:
            self._write_full(503, ERR_UNREACHABLE_CACHED)
            self._record(started, source="proxy", model=model, status=0)
            return
        self._write_full(status, resp_body)
        self._record(started, source="proxy", model=model, status=status)

    # -- raw passthrough (everything else, plus streaming chat) -----------

    def _forward_raw(self, record: bool = False, model: str = "",
                     body: bytes | None = None) -> None:
        p = self.proxy

        if not p.brk.allow():
            # Mirror the Go client: no usage row on the early 503.
            self._write_full(503, ERR_UNREACHABLE)
            return

        if body is None and self.command in BODY_METHODS:
            body = self._read_body()
            if body is None:
                self.close_connection = True
                self._write_full(413, ERR_TOO_LARGE)
                return

        resp, conn, _transport_err = self._attempt(body)
        if resp is None:
            p.brk.failure()
            self._write_full(503, ERR_UNREACHABLE)
            return

        status = resp.status
        if status < 500:
            p.brk.success()
        else:
            p.brk.failure()

        self._responded = True
        self.send_response(status)
        has_length = False
        for name, value in resp.getheaders():
            lname = name.lower()
            # Hop-by-hop framing must not be copied (we de-chunk before
            # reading), and date/server are emitted by send_response()
            # itself — copying the upstream's would produce duplicates.
            if lname in RESPONSE_SKIP or lname in ("date", "server"):
                continue
            if lname == "content-length":
                has_length = True
            self.send_header(name, value)
        if not has_length:
            self.close_connection = True
        self.end_headers()
        try:
            while True:
                chunk = resp.read(32 * 1024)
                if not chunk:
                    break
                self.wfile.write(chunk)
                self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError, OSError):
            self.close_connection = True
        finally:
            if conn is not None:
                try:
                    conn.close()
                except OSError:
                    pass

        if record:
            # Streaming chat records one row with the observed status,
            # even if the client disconnected mid-stream.
            self._record(time.time(), source="proxy", model=model,
                         status=status)

    # -- upstream -----------------------------------------------------------

    def _attempt(self, body: bytes | None):
        """Returns (response|None, conn|None, transport_error)."""
        p = self.proxy
        cfg = p.cfg
        servers = cfg.servers_snapshot()
        with cfg.mu():
            token, client_name = cfg.token, cfg.client_name
        headers: dict = {}
        peer = self.client_address[0] if self.client_address else ""
        copy_request_headers(headers, self.headers, client_name, token, peer)
        path, _, query = self.path.partition("?")
        resp, _err = attempt_servers(
            servers, p.index, self.command, path, query, body, headers)
        if resp is None:
            return None, None, True
        return resp, getattr(resp, "connection", None), False

    # -- response + usage helpers ------------------------------------------

    def _serve_cached(self, entry: Entry) -> None:
        self._write_full(entry.status_code, entry.body,
                         {"X-Llmrx-Cache": "hit"})

    def _record(self, started: float, source: str, model: str,
                status: int, tokens: dict | None = None) -> None:
        p = self.proxy
        if p.buffer is None:
            return
        from .usage import UsageRecord

        tokens = tokens or {}
        with p.cfg.mu():
            client_name = p.cfg.client_name
        p.buffer.add(UsageRecord(
            request_id=p.buffer.next_request_id(client_name),
            ts=int(time.time()),
            model=model,
            status=status,
            prompt_tokens=int(tokens.get("prompt_tokens") or 0),
            completion_tokens=int(tokens.get("completion_tokens") or 0),
            source=source,
            latency_ms=int((time.time() - started) * 1000),
        ))


def make_server(cfg: Config, proxy: Proxy, ui_dispatch=None) -> ThreadingHTTPServer:
    """Build the local server. ui_dispatch=None leaves /ui to be forwarded
    upstream like any other path; cli passes ui.dispatch."""
    class Server(ThreadingHTTPServer):
        daemon_threads = True
        allow_reuse_address = True

    host, _, port = cfg.listen.rpartition(":")
    try:
        port_num = int(port or 3425)
    except ValueError:
        # normalize() rejects this first; this is the defensive backstop
        # for a Config that skipped normalize (the enroll path).
        from .config import ConfigError
        raise ConfigError(
            f"listen: {cfg.listen!r} must be host:port (e.g. 127.0.0.1:3425)")
    server = Server((host or "127.0.0.1", port_num), Handler)
    server.proxy = proxy  # type: ignore[attr-defined]
    server.ui_dispatch = ui_dispatch  # type: ignore[attr-defined]
    return server


def serve(cfg: Config, proxy: Proxy) -> ThreadingHTTPServer:
    """Production wiring: the status page is always mounted."""
    from . import ui as ui_mod
    return make_server(cfg, proxy, ui_dispatch=ui_mod.dispatch)
