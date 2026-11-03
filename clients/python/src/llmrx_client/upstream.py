"""Upstream dialing: sticky failover across the configured server list.

Mirrors internal/client/proxy.go's attemptServers/copyHeaders/remoteIP.

One deliberate divergence (docs/P18 §5.1): the body is always forwarded.
The Go client passes nil here for streaming and passthrough requests, so
every streaming chat call and every non-chat POST reaches upstream with
an empty body; that is a bug, not a contract.
"""

from __future__ import annotations

import http.client
import threading
from urllib.parse import urlsplit

HOP_SKIP = {"host", "content-length", "connection"}


class UpstreamError(Exception):
    """All configured servers failed at the transport level."""


class ServerIndex:
    """Sticky index: remembers the last server that returned any HTTP
    response, so after an outage recovery traffic stays on the recovered
    server. Not round-robin despite the Go comment — replicated as
    observed."""

    def __init__(self) -> None:
        self._v = 0
        self._mu = threading.Lock()

    def get(self) -> int:
        with self._mu:
            return self._v

    def set(self, v: int) -> None:
        with self._mu:
            self._v = v


def remote_ip(addr: str) -> str:
    """RemoteAddr minus the port segment."""
    if not addr:
        return ""
    i = addr.rfind(":")
    return addr[:i] if i != -1 else addr


def copy_request_headers(dst: dict, headers, client_name: str,
                         token: str, peer: str) -> None:
    """Build the outbound header set from the inbound request.

    dst is a plain dict, so repeated inbound headers collapse to the last
    value — the same last-value-wins the Go client produces via Set.
    """
    for name, value in headers.items():
        if name.lower() in HOP_SKIP:
            continue
        dst[name] = value

    if "X-Forwarded-For" not in dst:
        dst["X-Forwarded-For"] = remote_ip(peer)
    if client_name:
        dst["X-Llmrx-Client"] = client_name
    if token and "Authorization" not in dst:
        # Explicit caller token always wins; we only fill the gap.
        dst["Authorization"] = f"Bearer {token}"


def _connect(base: str, timeout: float | None) -> http.client.HTTPConnection:
    parts = urlsplit(base)
    if parts.scheme == "https":
        return http.client.HTTPSConnection(parts.netloc, timeout=timeout)
    return http.client.HTTPConnection(parts.netloc, timeout=timeout)


def attempt_servers(
    servers: list[str],
    index: ServerIndex,
    method: str,
    path: str,
    query: str,
    body: bytes | None,
    headers: dict,
    timeout: float | None = None,
) -> tuple[http.client.HTTPResponse | None, str]:
    """Dial servers in failover order. Returns (response, "") on any HTTP
    response (any status — the caller decides), or (None, last_error)
    when every attempt failed at the transport level."""
    if not servers:
        return None, "no servers configured"

    start = index.get() % len(servers)
    last_err = ""
    for i in range(len(servers)):
        base = servers[(start + i) % len(servers)].rstrip("/")
        target = base + path + (f"?{query}" if query else "")
        conn = None
        try:
            conn = _connect(base, timeout)
            conn.putrequest(method, target[len(base):] or "/",
                            skip_host=False, skip_accept_encoding=True)
            for name, value in headers.items():
                conn.putheader(name, value)
            if body is not None and "Content-Length" not in {
                k.lower() for k in headers
            }:
                conn.putheader("Content-Length", str(len(body)))
            conn.endheaders(message_body=body if body is not None else None)
            resp = conn.getresponse()
            index.set((start + i) % len(servers))
            # The connection is handed to the caller for stream draining;
            # closing it here would kill SSE. Caller owns conn via resp.
            setattr(resp, "connection", conn)
            return resp, ""
        except (OSError, http.client.HTTPException) as exc:
            last_err = f"{base}{path}: {exc}"
            if conn is not None:
                try:
                    conn.close()
                except OSError:
                    pass
            continue
    return None, last_err or "all servers failed"
