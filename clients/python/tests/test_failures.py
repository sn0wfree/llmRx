"""Failure-matrix tests (P20 §2, F1-F9).

One test per failure MODE, not just "unreachable": DNS failure, partial
SSE, garbage bodies, redirects, disk-full, breaker probes.
"""

from __future__ import annotations

import json
import socket
import threading
import unittest
from unittest import mock

from llmrx_client.usage import JSONLUsageStore, UsageBuffer, _drain_to_store

from tests.helpers import ClientHarness, MockUpstream, chat_request, make_cfg


def refusings():
    """A port that actively refuses (nothing bound)."""
    return "http://127.0.0.1:1"


class TestF1DnsFailure(unittest.TestCase):
    def test_unresolvable_host_gives_503_with_usage_zero(self):
        with MockUpstream([(200, {}, b"{}")]) as live:
            h = ClientHarness(make_cfg(["http://no-such-host-llmrx.invalid"],
                                       cache_enabled=False))
            try:
                status, _, data = h.chat(chat_request())
            finally:
                h.stop()
        self.assertEqual(status, 503)
        self.assertEqual(json.loads(data)["error"]["code"],
                         "client_upstream_unreachable")
        row = h.buffer.take_all()[-1].to_json()
        self.assertEqual(row["status"], 0)


class TestF2MultiServerFailover(unittest.TestCase):
    def test_three_servers_mixed_health(self):
        """F2: dead + refusing + alive -- traffic must land on the alive
        one regardless of order."""
        dead = MockUpstream()
        dead.__enter__()
        dead._server.shutdown()
        dead._server.server_close()
        try:
            with MockUpstream([(200, {}, b'{"choices":[]}')]) as alive:
                h = ClientHarness(make_cfg(
                    [refusings(), dead.url, alive.url], cache_enabled=False))
                try:
                    for _ in range(3):
                        status, _, _ = h.chat(chat_request())
                        self.assertEqual(status, 200)
                finally:
                    h.stop()
        finally:
            pass
        self.assertEqual(len(alive.requests), 3)


class TestF3MidStreamClose(unittest.TestCase):
    def test_upstream_close_mid_sse_forwards_partial(self):
        """F3: upstream dies mid-stream -- forward what arrived, don't
        hang, don't invent a trailing error."""
        partial = b"data: {\"delta\":\"hel"
        with MockUpstream([(200, {"Content-Type": "text/event-stream"},
                            partial)]) as up:
            h = ClientHarness(make_cfg([up.url], cache_enabled=False))
            try:
                status, headers, data = h.chat(
                    chat_request(stream=True),
                    headers={"Accept": "text/event-stream"})
            finally:
                h.stop()
        self.assertEqual(status, 200)
        self.assertEqual(data, partial)
        # usage row still recorded with the observed status
        rows = h.buffer.take_all()
        self.assertEqual(rows[-1].to_json()["status"], 200)


class TestF4F5F6F7UpstreamOddities(unittest.TestCase):
    from contextlib import contextmanager

    @contextmanager
    def _harness(self, response):
        with MockUpstream([response]) as up:
            h = ClientHarness(make_cfg([up.url], cache_enabled=False))
            try:
                yield h
            finally:
                h.stop()

    def test_f4_200_with_binary_garbage_body(self):
        blob = bytes(range(256))
        with self._harness((200, {}, blob)) as h:
            status, _, data = h.chat(chat_request())
        self.assertEqual(status, 200)
        self.assertEqual(data, blob)

    def test_f5_200_with_empty_body(self):
        with self._harness((200, {}, b"")) as h:
            status, _, data = h.chat(chat_request())
        self.assertEqual(status, 200)
        self.assertEqual(data, b"")

    def test_f6_304_passthrough_has_no_body(self):
        with self._harness((304, {"ETag": '"x"'}, b"")) as h:
            status, headers, data = h.request("GET", "/v1/models")
        self.assertEqual(status, 304)
        self.assertEqual(headers.get("ETag"), '"x"')
        self.assertEqual(data, b"")

    def test_f7_302_redirect_not_followed_by_client(self):
        """The client is a proxy: upstream redirects are the CALLER's to
        follow, so Location must pass through untouched."""
        with self._harness((302, {"Location": "http://upstream/there"},
                            b"")) as h:
            status, headers, _ = h.request("GET", "/v1/models")
        self.assertEqual(status, 302)
        self.assertEqual(headers.get("Location"), "http://upstream/there")


class TestF8DiskFull(unittest.TestCase):
    def test_append_oserror_does_not_lose_rows(self):
        with tempfile.TemporaryDirectory() as d:
            store = JSONLUsageStore(d + "/usage.jsonl")
            buf = UsageBuffer(10)
            buf.add(rec := __import__("tests.test_race", fromlist=["rec"]).rec(1))
            with mock.patch.object(store, "append",
                                   side_effect=OSError("No space left")):
                _drain_to_store(buf, store)
            self.assertEqual(len(buf), 1)
            self.assertEqual(len(buf.unpersisted()), 1)


class TestF9BreakerProbeStorm(unittest.TestCase):
    def test_concurrent_requests_during_cooldown_then_recovery(self):
        """F9: while the breaker is open (within cooldown) every request
        gets the immediate 503 without dialing; once cooled, a probe
        dials and success closes it."""
        cooldown_hit = {"n": 0}

        with MockUpstream([(500, {}, b"boom"), (500, {}, b"boom"),
                           (500, {}, b"boom")]) as up:
            h = ClientHarness(make_cfg([up.url], cache_enabled=False))
            try:
                for _ in range(3):
                    h.chat(chat_request())  # three 5xx -> breaker opens
                self.assertTrue(h.proxy.brk.state()[0])

                errors = []
                barrier = threading.Barrier(6)

                def hammer():
                    barrier.wait(timeout=10)
                    # The passthrough path is the one gated by
                    # allow(); buffered chat deliberately always dials
                    # (Go parity, P18 §5).
                    s, _, data = h.request("GET", "/v1/models")
                    if s == 503:
                        cooldown_hit["n"] += 1
                    elif s != 200:
                        errors.append(f"unexpected {s}: {data[:80]}")

                threads = [threading.Thread(target=hammer, daemon=True)
                           for _ in range(6)]
                for t in threads:
                    t.start()
                for t in threads:
                    t.join(timeout=30)
                self.assertEqual(errors, [])
                # Within the 30s cooldown all six must have been refused
                # without dialing: the mock must not have seen them.
                served = len(up.requests) - 3
                self.assertEqual(cooldown_hit["n"], 6)
                self.assertEqual(served, 0, "dialled during cooldown!")
            finally:
                h.stop()

        # After cooldown a probe goes through and success closes it.
        with MockUpstream([(500, {}, b"boom"), (500, {}, b"boom"),
                           (500, {}, b"boom"), (200, {}, b'{"choices":[]}')]) as up:
            h = ClientHarness(make_cfg([up.url], cache_enabled=False))
            try:
                for _ in range(3):
                    h.chat(chat_request())
                self.assertTrue(h.proxy.brk.state()[0])
                h.proxy.brk._cooldown = 0.0  # fast-forward time
                status, _, _ = h.request("GET", "/v1/models")
                self.assertEqual(status, 200)
                self.assertFalse(h.proxy.brk.state()[0])
            finally:
                h.stop()


import tempfile  # noqa: E402


if __name__ == "__main__":
    unittest.main()
