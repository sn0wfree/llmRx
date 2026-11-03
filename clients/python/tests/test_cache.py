import time
import unittest

from llmrx_client.cache import (
    Entry,
    MemoryCache,
    TemperaturePositive,
    canonical_key,
)


def req(**over):
    doc = {"model": "m", "temperature": 0,
           "messages": [{"role": "user", "content": "hi"}]}
    content = over.pop("content", None)
    if content is not None:
        doc["messages"][0]["content"] = content
    doc.update(over)
    return doc


class TestKey(unittest.TestCase):
    def test_stable_and_content_sensitive(self):
        self.assertEqual(canonical_key(req()), canonical_key(req()))
        self.assertNotEqual(canonical_key(req()), canonical_key(req(content="x")))
        self.assertNotEqual(canonical_key(req()), canonical_key(req(model="m2")))

    def test_temperature_positive_rejected(self):
        with self.assertRaises(TemperaturePositive):
            canonical_key(req(temperature=0.7))

    def test_temperature_absence_equals_zero(self):
        a = canonical_key({"model": "m", "messages": [{"role": "u", "content": "x"}]})
        b = canonical_key({"model": "m", "temperature": 0,
                           "messages": [{"role": "u", "content": "x"}]})
        self.assertEqual(a, b)

    def test_stream_flag_participates(self):
        self.assertNotEqual(canonical_key(req()),
                            canonical_key(req(stream=True)))

    def test_reasoning_content_excluded(self):
        a = canonical_key(req())
        b = canonical_key(req(messages=[
            {"role": "user", "content": "hi", "reasoning_content": "secret"}]))
        self.assertEqual(a, b)

    def test_tool_order_normalized(self):
        ta = {"type": "function", "function": {"name": "alpha"}}
        tb = {"type": "function", "function": {"name": "beta"}}
        self.assertEqual(canonical_key(req(tools=[ta, tb])),
                         canonical_key(req(tools=[tb, ta])))


class TestMemoryCache(unittest.TestCase):
    def test_put_get_roundtrip(self):
        c = MemoryCache(4)
        c.put("k", Entry(200, b"body", usage={"prompt_tokens": 3}), 60)
        e = c.get("k")
        self.assertIsNotNone(e)
        self.assertEqual(e.body, b"body")
        self.assertEqual(e.usage["prompt_tokens"], 3)

    def test_ttl_expiry_lazy(self):
        c = MemoryCache(4)
        c.put("k", Entry(200, b"x"), 0.05)
        time.sleep(0.08)
        self.assertIsNone(c.get("k"))
        self.assertEqual(c.stats()["misses"], 1)

    def test_lru_eviction(self):
        c = MemoryCache(2)
        c.put("a", Entry(200, b"1"), 60)
        c.put("b", Entry(200, b"2"), 60)
        c.get("a")  # a becomes most-recent
        c.put("c", Entry(200, b"3"), 60)
        self.assertIsNone(c.get("b"))
        self.assertIsNotNone(c.get("a"))
        self.assertIsNotNone(c.get("c"))
        self.assertEqual(c.stats()["size"], 2)

    def test_stats_hit_rate(self):
        c = MemoryCache(4)
        self.assertEqual(c.stats()["hit_rate"], 0.0)
        c.put("k", Entry(200, b"x"), 60)
        c.get("k")
        c.get("missing")
        s = c.stats()
        self.assertEqual((s["hits"], s["misses"]), (1, 1))
        self.assertEqual(s["hit_rate"], 50.0)

    def test_purge(self):
        c = MemoryCache(4)
        c.put("k", Entry(200, b"x"), 60)
        c.purge()
        self.assertIsNone(c.get("k"))


if __name__ == "__main__":
    unittest.main()
