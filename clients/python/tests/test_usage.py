import json
import os
import tempfile
import threading
import time
import unittest
import urllib.error
from unittest import mock

from llmrx_client.usage import (
    JSONLUsageStore,
    UsageBuffer,
    UsageRecord,
    upload_batch,
)


def rec(n=1, **over):
    doc = UsageRecord(request_id=f"edge-{n}", ts=1700000000, model="m",
                      status=200, prompt_tokens=1, completion_tokens=2,
                      source="proxy", latency_ms=5)
    for k, v in over.items():
        setattr(doc, k, v)
    return doc


class TestRecord(unittest.TestCase):
    def test_json_field_set_and_order(self):
        doc = rec().to_json()
        keys = list(doc.keys())
        self.assertEqual(keys, [
            "request_id", "ts", "model", "status", "prompt_tokens",
            "completion_tokens", "cost_usd", "source", "latency_ms"])
        self.assertEqual(doc["request_id"], "edge-1")
        self.assertEqual(doc["cost_usd"], 0)


class TestBuffer(unittest.TestCase):
    def test_drops_oldest_when_full(self):
        b = UsageBuffer(3)
        for i in range(5):
            b.add(rec(i))
        rows = b.take_all()
        self.assertEqual([r.request_id for r in rows], ["edge-2", "edge-3", "edge-4"])

    def test_take_drains_head(self):
        b = UsageBuffer(10)
        for i in range(3):
            b.add(rec(i))
        first = b.take(2)
        self.assertEqual([r.request_id for r in first], ["edge-0", "edge-1"])
        rest = b.take_all()
        self.assertEqual([r.request_id for r in rest], ["edge-2"])

    def test_requeue_front_preserves_order(self):
        b = UsageBuffer(10)
        b.add(rec(1))
        b.add(rec(2))
        batch = b.take_all()
        b.add(rec(3))  # new row arrives while batch is in flight
        b.requeue_front(batch)
        ids = [r.request_id for r in b.take_all()]
        self.assertEqual(ids, ["edge-1", "edge-2", "edge-3"])

    def test_request_ids_monotonic(self):
        b = UsageBuffer()
        first = b.next_request_id("edge")
        second = b.next_request_id("edge")
        self.assertTrue(first.startswith("edge-"))
        self.assertTrue(second.startswith("edge-"))
        self.assertNotEqual(first, second)
        # the counter must still advance
        self.assertEqual(int(second.rsplit("-", 1)[1]),
                         int(first.rsplit("-", 1)[1]) + 1)

    def test_request_ids_unique_across_restarts(self):
        """The server dedups on client_name+request_id for 24h, so ids
        must not repeat when the client restarts and its counter resets."""
        a = UsageBuffer()
        b = UsageBuffer()
        ids_a = [a.next_request_id("edge") for _ in range(3)]
        ids_b = [b.next_request_id("edge") for _ in range(3)]
        self.assertEqual(len(set(ids_a) & set(ids_b)), 0,
                         "request ids collided across two buffer instances")
        self.assertTrue(all(i.startswith("edge-") for i in ids_a))
        self.assertEqual(len(set(ids_a)), 3)


class TestJSONLStore(unittest.TestCase):
    def test_roundtrip(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "nested", "usage.jsonl")
            store = JSONLUsageStore(path)
            store.append([rec(1), rec(2, source="cache")])
            store.close()
            with open(path) as fh:
                lines = [json.loads(line) for line in fh]
            self.assertEqual(len(lines), 2)
            self.assertEqual(lines[0]["request_id"], "edge-1")
            self.assertEqual(lines[1]["source"], "cache")

    def test_append_creates_file_with_mode_0600(self):
        import stat
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "usage.jsonl")
            store = JSONLUsageStore(path)
            store.append([rec(1)])
            store.close()
            mode = stat.S_IMODE(os.stat(path).st_mode)
            self.assertEqual(mode & 0o077, 0)


class TestUpload(unittest.TestCase):
    def test_success_posts_payload(self):
        captured = {}

        class Resp:
            status = 200

            def __enter__(self):
                return self

            def __exit__(self, *a):
                return False

            def read(self, n=-1):
                return b"{}"

        def fake_urlopen(req, timeout):
            captured["url"] = req.full_url
            captured["auth"] = req.headers.get("Authorization")
            captured["body"] = json.loads(req.data)
            return Resp()

        with mock.patch("llmrx_client.usage.urllib.request.urlopen", fake_urlopen):
            upload_batch("http://s/client/v1/usage", "ecs_t", "edge", [rec(1)])

        self.assertEqual(captured["url"], "http://s/client/v1/usage")
        self.assertEqual(captured["auth"], "Bearer ecs_t")
        self.assertEqual(captured["body"]["client_name"], "edge")
        self.assertEqual(len(captured["body"]["records"]), 1)
        self.assertEqual(captured["body"]["records"][0]["request_id"], "edge-1")

    def test_failure_raises_for_requeue(self):
        def fake_urlopen(req, timeout):
            raise urllib.error.URLError("down")

        with mock.patch("llmrx_client.usage.urllib.request.urlopen", fake_urlopen):
            with self.assertRaises(urllib.error.URLError):
                upload_batch("http://s/client/v1/usage", "t", "e", [rec(1)])


class TestRecorderFlush(unittest.TestCase):
    def test_stop_flushes_remaining(self):
        from llmrx_client.usage import start_recorder

        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "usage.jsonl")
            store = JSONLUsageStore(path)
            buf = UsageBuffer(10)
            buf.add(rec(7))
            stop = threading.Event()
            start_recorder(buf, store, stop)
            # No 5s wait: stopping must flush synchronously via the final tick.
            stop.set()
            time.sleep(0.3)
            store.close()
            with open(path) as fh:
                lines = [json.loads(line) for line in fh]
            self.assertEqual(lines[0]["request_id"], "edge-7")
            self.assertEqual(len(buf), 0)


if __name__ == "__main__":
    unittest.main()
