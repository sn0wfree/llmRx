"""Resource-limit tests (P20 §4, L1-L4).

L1/L2: the 32 MiB body cap boundary -- exactly at the cap must flow,
one byte over must 413 and close.
L3: cache churn at capacity with TTL expiry -- stats stay coherent.
L4: a slow client that never finishes its request must not pin the
    server forever (BaseHTTPRequestHandler.timeout, matching the Go
    server's ReadHeaderTimeout).
"""

from __future__ import annotations

import http.client
import json
import socket
import threading
import time
import unittest
from unittest import mock

import llmrx_client.proxy as proxy_mod

from tests.helpers import ClientHarness, MockUpstream, chat_request, make_cfg

CANNED = [(200, {}, json.dumps(
    {"choices": [], "usage": {"prompt_tokens": 1}}).encode())]


class TestL1L2BodyCap(unittest.TestCase):
    def setUp(self):
        self._ups = MockUpstream(CANNED)
        self._ups.__enter__()
        self.h = ClientHarness(make_cfg([self._ups.url], cache_enabled=False))

    def tearDown(self):
        self.h.stop()
        self._ups.__exit__(None, None, None)

    def test_l1_exactly_at_cap_flows(self):
        """L1: a body of exactly MAX_BODY bytes must be forwarded whole."""
        payload = b"x" * proxy_mod.MAX_BODY
        self._ups.responses = [(200, {}, b'{"choices":[]}')]
        conn = http.client.HTTPConnection("127.0.0.1", self.h.port, timeout=120)
        try:
            conn.request("POST", "/v1/embeddings", body=payload,
                         headers={"Content-Type": "application/octet-stream"})
            resp = conn.getresponse()
            resp.read()
        finally:
            conn.close()
        self.assertEqual(resp.status, 200)
        self.assertEqual(self._ups.requests[0]["body"], payload)

    def test_l2_one_byte_over_rejected_413(self):
        """L2: MAX_BODY + 1 -> 413 client_body_too_large. The server
        responds and closes while the client is still writing, so the
        client may see BrokenPipe before it can read the response --
        both outcomes are the same server behaviour."""
        payload = b"x" * (proxy_mod.MAX_BODY + 1)
        conn = http.client.HTTPConnection("127.0.0.1", self.h.port, timeout=120)
        resp = None
        try:
            try:
                conn.request("POST", "/v1/embeddings", body=payload,
                             headers={"Content-Type": "application/octet-stream"})
                resp = conn.getresponse()
                data = resp.read()
            except (BrokenPipeError, ConnectionResetError):
                # The server already replied 413 and closed mid-write.
                resp = conn.getresponse()
                data = resp.read()
        finally:
            conn.close()
        self.assertIsNotNone(resp, "no response at all")
        self.assertEqual(resp.status, 413)
        self.assertEqual(json.loads(data)["error"]["code"],
                         "client_body_too_large")
        self.assertEqual(self._ups.requests, [])


class TestL3CacheChurn(unittest.TestCase):
    def test_full_capacity_churn_with_expiry(self):
        """L3: fill capacity, keep inserting with rolling TTL expiry --
        size must never exceed capacity and stats must stay coherent."""
        with MockUpstream(CANNED) as up:
            h = ClientHarness(make_cfg([up.url], cache_enabled=True,
                                       cache_ttl_sec=1, cache_max_items=50))
            try:
                for i in range(400):  # 8x capacity
                    body = chat_request(content=f"churn-{i % 60}")
                    h.chat(body)
                    st = h.proxy.cache.stats()
                    self.assertLessEqual(st["size"], 50)
                    self.assertGreaterEqual(st["hits"], 0)
                # TTL=1s: after sleeping, everything expires -> misses
                time.sleep(1.1)
                st = h.proxy.cache.stats()
                before = st["misses"]
                body = chat_request(content="churn-0")
                s, hd, _ = h.chat(body)
                self.assertNotIn("X-Llmrx-Cache", hd, "expired entry served")
                self.assertEqual(h.proxy.cache.stats()["misses"], before + 1)
            finally:
                h.stop()


class TestL4SlowClient(unittest.TestCase):
    def test_slowloris_connection_is_reclaimed(self):
        """L4: a connection that opens and sends nothing must eventually
        be reclaimed (Handler.timeout) instead of pinning a thread
        forever; other requests keep working meanwhile."""
        with MockUpstream(CANNED) as up:
            h = ClientHarness(make_cfg([up.url]))
            try:
                slow = socket.create_connection(("127.0.0.1", h.port),
                                                timeout=10)
                slow.sendall(b"POST /v1/chat/completions HTTP/1.1\r\n"
                             b"Host: x\r\n")  # incomplete request
                # A normal request must still succeed while the slow one
                # is parked.
                status, _, _ = h.request("GET", "/healthz")
                self.assertEqual(status, 200)
                # BaseHTTPRequestHandler enforces its timeout on the
                # first read: wait past it, then confirm the server
                # closed the slow socket (recv returns b"").
                slow.settimeout(125)
                self.assertEqual(slow.recv(1024), b"")
                slow.close()
            finally:
                h.stop()


if __name__ == "__main__":
    unittest.main()
