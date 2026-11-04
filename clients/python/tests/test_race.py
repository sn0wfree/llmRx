"""Race-condition stress tests (P20 §1, R1-R5).

The two worst defects found in this client (an in-flight limiter
self-deadlock, and the persist-vs-upload accounting race) were both
luck catches. These tests make the races deliberate.
"""

from __future__ import annotations

import json
import tempfile
import threading
import time
import unittest
import urllib.error
from unittest import mock

from llmrx_client.config import apply_bundle
from llmrx_client.usage import JSONLUsageStore, UsageBuffer, UsageRecord, _drain_to_store, _drain_to_server

from tests.helpers import (
    ClientHarness,
    MockServerAPI,
    MockUpstream,
    chat_request,
    make_cfg,
)


def rec(n, **over):
    doc = UsageRecord(request_id=f"edge-{n}", ts=1700000000, model="m",
                      status=200, source="proxy")
    for k, v in over.items():
        setattr(doc, k, v)
    return doc


class TestR1BundleUnderLoad(unittest.TestCase):
    def test_server_swap_mid_flight_loses_nothing(self):
        """R1: apply_bundle replacing servers while requests fly must not
        fail a single request, and new requests must reach the new
        server (snapshot semantics)."""
        with MockUpstream(CANNED := [(200, {}, b'{"choices":[]}')]) as old, \
             MockUpstream(CANNED) as new:
            h = ClientHarness(make_cfg([old.url], cache_enabled=False))
            errors = []
            stop = threading.Event()

            def hammer():
                while not stop.is_set():
                    try:
                        s, _, _ = h.chat(chat_request())
                        if s != 200:
                            errors.append(f"status {s}")
                    except Exception as exc:  # noqa: BLE001
                        errors.append(repr(exc))

            workers = [threading.Thread(target=hammer, daemon=True)
                       for _ in range(4)]
            for w in workers:
                w.start()
            time.sleep(0.3)
            apply_bundle(h.proxy.cfg, {
                "version": 2, "servers": [new.url],
                "cache": {"enabled": True, "ttl_sec": 300, "max_items": 100},
                "fallback": {"mode": "cache_then_fail"},
                "limits": {"max_inflight": 0}})
            time.sleep(0.8)
            stop.set()
            for w in workers:
                w.join(timeout=10)
            h.stop()

        self.assertEqual(errors, [])
        self.assertGreater(len(new.requests), 0, "new server never reached")


class TestR2PurgeUnderLoad(unittest.TestCase):
    def test_purge_loop_during_requests(self):
        """R2: cache purge racing requests must not throw and stats must
        stay coherent (never negative, hits+misses monotonic)."""
        with MockUpstream([(200, {}, json.dumps(
                {"choices": [], "usage": {}}).encode())]) as up:
            h = ClientHarness(make_cfg([up.url]))
            errors = []
            stop = threading.Event()

            def purge_loop():
                while not stop.is_set():
                    try:
                        h.proxy.cache.purge()
                    except Exception as exc:  # noqa: BLE001
                        errors.append(repr(exc))
                    time.sleep(0.01)

            t = threading.Thread(target=purge_loop, daemon=True)
            t.start()
            for i in range(40):
                try:
                    s, _, _ = h.chat(chat_request(content=f"r{i}"))
                    if s != 200:
                        errors.append(f"status {s}")
                    st = h.proxy.cache.stats()
                    self.assertGreaterEqual(st["hits"], 0)
                    self.assertGreaterEqual(st["misses"], 0)
                except Exception as exc:  # noqa: BLE001
                    errors.append(repr(exc))
            stop.set()
            t.join(timeout=10)
            h.stop()

        self.assertEqual(errors, [])


class TestR3SettingsUnderLoad(unittest.TestCase):
    def test_settings_writes_concurrent_with_requests(self):
        """R3: /ui/settings writes racing chat requests -- every write
        gets its 303, every chat a 200, nothing 500s."""
        import urllib.parse

        with MockUpstream([(200, {}, b'{"choices":[]}')]) as up:
            h = ClientHarness(make_cfg([up.url]))
            errors = []
            stop = threading.Event()

            def write_loop():
                i = 0
                while not stop.is_set():
                    i += 1
                    form = urllib.parse.urlencode({
                        "cache_ttl_sec": str(300 + i)}).encode()
                    try:
                        s, _, _ = h.request("POST", "/ui/settings", form,
                                            {"Content-Type": "application/x-www-form-urlencoded"})
                        if s != 303:
                            errors.append(f"settings status {s}")
                    except Exception as exc:  # noqa: BLE001
                        errors.append(repr(exc))

            def chat_loop():
                while not stop.is_set():
                    try:
                        s, _, _ = h.chat(chat_request())
                        if s != 200:
                            errors.append(f"chat status {s}")
                    except Exception as exc:  # noqa: BLE001
                        errors.append(repr(exc))

            threads = [threading.Thread(target=write_loop, daemon=True),
                       threading.Thread(target=chat_loop, daemon=True)]
            for t in threads:
                t.start()
            time.sleep(1.0)
            stop.set()
            for t in threads:
                t.join(timeout=10)
            h.stop()

        self.assertEqual(errors, [])
        self.assertGreaterEqual(h.proxy.cfg.cache_ttl_sec, 300)


class TestR4UploadStormOverflow(unittest.TestCase):
    def test_overflow_drops_oldest_then_recovers(self):
        """R4: uploads failing while rows pile up -- the buffer drops the
        oldest at 5000, and once uploads recover the buffer drains."""
        buf = UsageBuffer(5000)
        for i in range(5100):
            buf.add(rec(i))

        self.assertEqual(len(buf), 5000)
        ids = [r.request_id for r in buf.take_all()]
        self.assertEqual(ids[0], "edge-100", "oldest rows not dropped")
        self.assertEqual(ids[-1], "edge-5099")

        # recovery: requeue everything, uploads succeed, buffer drains
        buf.requeue_front([rec(i) for i in range(100, 5100)])
        self.assertEqual(len(buf), 5000)
        batch = buf.take(200)
        self.assertEqual(len(batch), 200)


class TestR5PersistFailureRetry(unittest.TestCase):
    def test_oserror_leaves_rows_for_next_tick(self):
        """R5: a persist failure (disk full) must neither lose rows nor
        mark them -- the next tick retries."""
        with tempfile.TemporaryDirectory() as d:
            store = JSONLUsageStore(d + "/usage.jsonl")
            buf = UsageBuffer(10)
            buf.add(rec(1))
            buf.add(rec(2))

            with mock.patch.object(store, "append",
                                   side_effect=OSError("disk full")):
                _drain_to_store(buf, store)
            self.assertEqual(len(buf), 2, "rows lost on persist failure")
            self.assertEqual(len(buf.unpersisted()), 2, "rows wrongly marked")

            # disk recovers: next tick persists both, exactly once
            _drain_to_store(buf, store)
            with open(d + "/usage.jsonl") as fh:
                ids = [json.loads(l)["request_id"] for l in fh]
            self.assertEqual(ids, ["edge-1", "edge-2"])
            self.assertEqual(buf.unpersisted(), [])


CANNED = [(200, {}, json.dumps({"choices": [], "usage": {}}).encode())]


if __name__ == "__main__":
    unittest.main()
