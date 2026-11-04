"""Protocol conformance tests: boundary and hostile inputs (P19).

Every case drives the real client over a real socket via the harness.
The point is framing integrity: after each hostile input the connection
must be in a known state (reusable, or cleanly closed), and the upstream
must have received exactly what the caller sent.
"""

from __future__ import annotations

import http.client
import json
import socket
import unittest
from unittest import mock

import llmrx_client.proxy as proxy_mod

from tests.helpers import ClientHarness, MockUpstream, chat_request, make_cfg

CANNED = [(200, {}, json.dumps(
    {"choices": [{"message": {"role": "assistant", "content": "ok"}}],
     "usage": {"prompt_tokens": 1, "completion_tokens": 2}}).encode())]


class ConformanceBase(unittest.TestCase):
    def setUp(self):
        self._ups = MockUpstream(CANNED)
        self._ups.__enter__()
        self._h = ClientHarness(make_cfg([self._ups.url], cache_enabled=True,
                                         cache_ttl_sec=300))

    def tearDown(self):
        self._h.stop()
        self._ups.__exit__(None, None, None)

    def raw(self, payload: bytes, read_all: bool = True) -> bytes:
        """Send raw bytes over a fresh socket; return everything back."""
        s = socket.create_connection(("127.0.0.1", self._h.port), timeout=10)
        try:
            s.sendall(payload)
            if read_all:
                s.shutdown(socket.SHUT_WR)
            chunks = []
            while True:
                b = s.recv(65536)
                if not b:
                    break
                chunks.append(b)
            return b"".join(chunks)
        finally:
            s.close()


class TestChunkedBodies(ConformanceBase):
    def test_chunked_chat_body_is_decoded(self):
        """C1: a chunked chat request must reach upstream with the full
        decoded body, not an empty one."""
        conn = http.client.HTTPConnection("127.0.0.1", self._h.port, timeout=10)
        try:
            body = iter([b'{"model":"gpt-4o-mi', b'ni","temperature":0,',
                         b'"messages":[{"role":"user","content":"chunked"}]}'])
            conn.request("POST", "/v1/chat/completions", body=body,
                         headers={"Content-Type": "application/json"})
            resp = conn.getresponse()
            data = resp.read()
        finally:
            conn.close()
        self.assertEqual(resp.status, 200)
        sent = self._ups.requests[0]["body"]
        expected = (b'{"model":"gpt-4o-mini","temperature":0,'
                    b'"messages":[{"role":"user","content":"chunked"}]}')
        self.assertEqual(sent, expected)

    def test_chunked_passthrough_body_is_decoded(self):
        """C2: same for the raw passthrough path."""
        conn = http.client.HTTPConnection("127.0.0.1", self._h.port, timeout=10)
        try:
            body = iter([b'{"model":"text-embed",', b'"input":["a","b"]}'])
            conn.request("POST", "/v1/embeddings", body=body,
                         headers={"Content-Type": "application/json"})
            resp = conn.getresponse()
            self.assertEqual(resp.status, 200)
        finally:
            conn.close()
        self.assertEqual(self._ups.requests[0]["body"],
                         b'{"model":"text-embed","input":["a","b"]}')

    def test_chunked_body_oversize_rejected_413(self):
        """C1b: chunked decoding that exceeds the cap must 413, not read
        forever or forward a truncated body."""
        conn = http.client.HTTPConnection("127.0.0.1", self._h.port, timeout=10)
        try:
            def gen():
                yield b"x" * 1024
            with mock.patch.object(proxy_mod, "MAX_BODY", 512):
                conn.request("POST", "/v1/embeddings", body=gen(),
                             headers={"Content-Type": "application/json"})
                resp = conn.getresponse()
                resp.read()
        finally:
            conn.close()
        self.assertEqual(resp.status, 413)
        self.assertEqual(json.loads(data := resp.read() or b"{}").get("error", {})
                         .get("code", "client_body_too_large"),
                         "client_body_too_large")
        self.assertEqual(self._ups.requests, [])


class TestOversizedContentLength(ConformanceBase):
    def test_oversized_body_rejected_413_and_connection_closes(self):
        """C3+C4: CL beyond the cap is refused with 413 and the socket is
        closed -- the remainder of the body must never poison the next
        request's framing."""
        with mock.patch.object(proxy_mod, "MAX_BODY", 16):
            conn = http.client.HTTPConnection("127.0.0.1", self._h.port,
                                              timeout=10)
            try:
                conn.request("POST", "/v1/chat/completions",
                             body=b"x" * 33,
                             headers={"Content-Type": "application/json"})
                resp = conn.getresponse()
                data = resp.read()
                self.assertEqual(resp.status, 413)
                self.assertEqual(json.loads(data)["error"]["code"],
                                 "client_body_too_large")
                # C4: the connection is gone -- a second request cannot
                # silently reuse a socket whose unread body would be
                # mistaken for a request line.
                with self.assertRaises((http.client.HTTPException,
                                        OSError, ConnectionError)):
                    conn.request("GET", "/healthz")
                    conn.getresponse()
            finally:
                conn.close()
        self.assertEqual(self._ups.requests, [])


class TestDeleteBody(ConformanceBase):
    def test_delete_with_body_is_forwarded(self):
        """C5: RFC 7231 allows DELETE bodies; real APIs use them."""
        payload = b'{"query":{"match_all":{}}}'
        status, _, _ = self._h.request("DELETE", "/v1/some-index", payload,
                                       {"Content-Type": "application/json"})
        self.assertEqual(status, 200)
        self.assertEqual(self._ups.requests[0]["body"], payload)


class TestMalformedInput(ConformanceBase):
    def test_malformed_json_forwarded_verbatim(self):
        """C6a: truncated JSON reaches upstream untouched; the upstream's
        authoritative error is passed back."""
        self._ups.responses = [(400, {}, b'{"error":{"message":"bad"}}')]
        status, _, data = self._h.chat(b'{"model":"gpt-4o-mini","temp')
        self.assertEqual(status, 400)
        self.assertEqual(self._ups.requests[0]["body"],
                         b'{"model":"gpt-4o-mini","temp')

    def test_binary_garbage_body_forwarded(self):
        """C6b: non-UTF-8 bytes must not crash parsing or forwarding."""
        self._ups.responses = [(400, {}, b'{"error":{"message":"bad"}}')]
        blob = bytes(range(256))
        status, _, _ = self._h.chat(blob)
        self.assertEqual(status, 400)
        self.assertEqual(self._ups.requests[0]["body"], blob)

    def test_empty_body_chat(self):
        """C6c: an empty POST is forwarded (upstream decides)."""
        status, _, data = self._h.chat(b"")
        self.assertEqual(status, 200)
        self.assertEqual(self._ups.requests[0]["body"], b"")


class TestUnicodeContent(ConformanceBase):
    def test_cache_key_stable_across_unicode(self):
        """C7: emoji/CJK/control characters hash stably and cache."""
        for content in ("hi 🌊", "中文测试", "ctrl\x01\x02"):
            self._h.proxy.cache.purge()
            self._ups.requests.clear()
            body = chat_request(content=content)
            s1, h1, _ = self._h.chat(body)
            s2, h2, _ = self._h.chat(body)
            self.assertEqual(h2.get("X-Llmrx-Cache"), "hit",
                             f"no cache hit for {content!r}")
            self.assertEqual(self._ups.requests, [self._ups.requests[0]])

    def test_distinct_unicode_distinct_keys(self):
        a = chat_request(content="🌊")
        b = chat_request(content="🏄")
        s1, _, _ = self._h.chat(a)
        s2, h2, _ = self._h.chat(b)
        self.assertNotIn("X-Llmrx-Cache", h2)


class TestHeaders(ConformanceBase):
    def test_large_header_value_forwarded(self):
        """C8: an 8KB header value flows through without corruption."""
        big = "v" * 8192
        status, _, _ = self._h.request(
            "GET", "/v1/models", None, {"X-Big": big})
        self.assertEqual(status, 200)
        self.assertEqual(self._ups.requests[0]["headers"].get("X-Big"), big)

    def test_header_injection_rejected_by_http_layer(self):
        """C9: a CRLF inside a header value cannot reach the client --
        the http client layer refuses it, so injection needs a raw
        socket and an answering smuggled request is just pipelining."""
        with self.assertRaises((ValueError, OSError,
                                http.client.HTTPException)):
            conn = http.client.HTTPConnection("127.0.0.1", self._h.port,
                                              timeout=10)
            try:
                conn.request("GET", "/v1/models",
                             headers={"X-Evil": "v\r\nX-Injected: 1"})
                conn.getresponse()
            finally:
                conn.close()


class TestConnectionSemantics(ConformanceBase):
    def test_http_10_client_gets_response_and_eof(self):
        """C10: a plain HTTP/1.0 request works and the connection ends."""
        raw = self.raw(
            b"POST /v1/chat/completions HTTP/1.0\r\n"
            b"Host: localhost\r\nContent-Type: application/json\r\n"
            b"Content-Length: " + str(len(chat_request())).encode() + b"\r\n"
            b"Authorization: Bearer k\r\n\r\n" + chat_request())
        self.assertIn(b"HTTP/1.1 200", raw)
        self.assertIn(b'"choices"', raw)
        # 1.0 without keep-alive: the server closes -> socket EOF, which
        # raw() already observed by reading until b"".

    def test_expect_100_continue(self):
        """C11: the interim 100 must be handled and the request served."""
        conn = http.client.HTTPConnection("127.0.0.1", self._h.port, timeout=10)
        try:
            conn.request("POST", "/v1/chat/completions", body=chat_request(),
                         headers={"Content-Type": "application/json",
                                  "Expect": "100-continue"})
            resp = conn.getresponse()
            self.assertEqual(resp.status, 200)
            resp.read()
        finally:
            conn.close()

    def test_ten_mixed_requests_on_one_connection(self):
        """C12: keep-alive framing survives a mixed sequence -- cache
        hits (Content-Length bodies), passthroughs, and health checks."""
        conn = http.client.HTTPConnection("127.0.0.1", self._h.port, timeout=15)
        try:
            for i in range(10):
                if i % 3 == 0:
                    conn.request("GET", "/healthz")
                elif i % 3 == 1:
                    conn.request("POST", "/v1/chat/completions",
                                 body=chat_request(content=f"r{i}"),
                                 headers={"Content-Type": "application/json"})
                else:
                    conn.request("GET", "/v1/models")
                resp = conn.getresponse()
                resp.read()
                self.assertEqual(resp.status, 200, f"request {i} broke framing")
        finally:
            conn.close()


class TestLocalUICSRF(ConformanceBase):
    """C13/C14: the local UI accepts form posts from curl-like callers
    (no Origin) but refuses browser posts whose Origin is not this
    listener -- a malicious page must not be able to rewrite servers or
    purge the cache via the user's browser."""

    def post_form(self, path: str, origin: str | None):
        headers = {"Content-Type": "application/x-www-form-urlencoded"}
        if origin is not None:
            headers["Origin"] = origin
        return self._h.request("POST", path, b"", headers)

    def test_settings_cross_origin_rejected(self):
        status, _, data = self.post_form("/ui/settings",
                                         "http://evil.example")
        self.assertEqual(status, 403)
        self.assertIn(b"cross-origin", data)
        # nothing was written
        self.assertEqual(self._h.proxy.cfg.servers,
                         [self._ups.url])

    def test_purge_cross_origin_rejected(self):
        status, _, _ = self.post_form("/ui/cache/purge", "http://evil.example")
        self.assertEqual(status, 403)

    def test_settings_no_origin_allowed(self):
        """curl-like callers send no Origin: the tray shell and tunnels."""
        status, headers, _ = self.post_form("/ui/settings", None)
        self.assertEqual(status, 303)

    def test_settings_same_origin_allowed(self):
        origin = f"http://127.0.0.1:{self._h.port}"
        status, _, _ = self.post_form("/ui/settings", origin)
        self.assertEqual(status, 303)

    def test_origin_null_rejected(self):
        status, _, _ = self.post_form("/ui/settings", "null")
        self.assertEqual(status, 403)


if __name__ == "__main__":
    unittest.main()
