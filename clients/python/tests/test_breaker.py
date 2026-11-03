import time
import unittest

from llmrx_client.breaker import Breaker


class TestBreaker(unittest.TestCase):
    def test_opens_after_threshold(self):
        b = Breaker(3, 30.0)
        b.failure()
        b.failure()
        self.assertTrue(b.allow())
        b.failure()
        self.assertFalse(b.allow())
        self.assertEqual(b.state(), (True, 3))

    def test_success_resets(self):
        b = Breaker(3, 30.0)
        b.failure()
        b.failure()
        b.success()
        b.failure()
        b.failure()
        self.assertFalse(b.state()[0])

    def test_half_open_probe_after_cooldown(self):
        b = Breaker(3, 60.0)
        b.failure()
        b.failure()
        b.failure()
        self.assertFalse(b.allow(time.time()))
        self.assertTrue(b.allow(time.time() + 61))
        # allow() does not mutate: still open, still cooled
        self.assertEqual(b.state(), (True, 3))

    def test_failed_probe_reopens_immediately(self):
        b = Breaker(3, 0.0)  # zero cooldown → instantly cooled
        b.failure(); b.failure(); b.failure()
        self.assertTrue(b.allow())
        b.failure()  # failed probe: fails 4 >= 3, re-opens
        self.assertTrue(b.state()[0])
        self.assertTrue(b.allow())  # cooldown 0 → probes again

    def test_state_tuple(self):
        b = Breaker(2, 30.0)
        self.assertEqual(b.state(), (False, 0))
        b.failure()
        self.assertEqual(b.state(), (False, 1))


if __name__ == "__main__":
    unittest.main()
