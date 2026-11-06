"""Unit tests for the upstream dialing helpers (P20 6.1).

These were only exercised incidentally through the HTTP tests; the
header-building and failover-ordering rules deserve direct coverage.
"""

from __future__ import annotations

import unittest

from llmrx_client.upstream import (
    HOP_SKIP,
    ServerIndex,
    UpstreamError,
    attempt_servers,
    copy_request_headers,
    remote_ip,
)


class TestRemoteIp(unittest.TestCase):
    def test_strips_port(self):
        self.assertEqual(remote_ip("192.168.1.5:8443"), "192.168.1.5")
        self.assertEqual(remote_ip("[::1]:8080"), "[::1]")

    def test_no_port(self):
        self.assertEqual(remote_ip("10.0.0.9"), "10.0.0.9")

    def test_empty(self):
        self.assertEqual(remote_ip(""), "")


class TestCopyRequestHeaders(unittest.TestCase):
    def _build(self, headers, client_name="edge", token="tok",
               peer="10.1.2.3:5555"):
        dst: dict = {}
        copy_request_headers(dst, headers, client_name, token, peer)
        return dst

    def test_injection_headers_added(self):
        dst = self._build({"Content-Type": "application/json"})
        self.assertEqual(dst["X-Llmrx-Client"], "edge")
        self.assertEqual(dst["X-Forwarded-For"], "10.1.2.3")
        self.assertEqual(dst["Authorization"], "Bearer tok")

    def test_hop_headers_skipped(self):
        dst = self._build({"Host": "evil", "Content-Length": "999",
                           "Connection": "close"})
        self.assertNotIn("Host", dst)
        self.assertNotIn("Content-Length", dst)
        self.assertNotIn("Connection", dst)

    def test_caller_token_wins(self):
        dst = self._build({"Authorization": "Bearer agent"}, token="cfg")
        self.assertEqual(dst["Authorization"], "Bearer agent")

    def test_no_token_no_auth_header(self):
        dst = self._build({}, token="")
        self.assertNotIn("Authorization", dst)

    def test_xff_preserved_when_present(self):
        dst = self._build({"X-Forwarded-For": "203.0.113.9"}, peer="10.0.0.1:1")
        self.assertEqual(dst["X-Forwarded-For"], "203.0.113.9")

    def test_multi_value_collapses_to_last(self):
        class Multi:
            def items(self):
                return [("X-Repeat", "first"), ("X-Repeat", "second")]

        dst = self._build(Multi())
        self.assertEqual(dst["X-Repeat"], "second")


class TestServerIndex(unittest.TestCase):
    def test_sticky(self):
        idx = ServerIndex()
        self.assertEqual(idx.get(), 0)
        idx.set(2)
        self.assertEqual(idx.get(), 2)


class TestAttemptServers(unittest.TestCase):
    def test_empty_servers(self):
        resp, err = attempt_servers([], ServerIndex(), "GET", "/", "", None, {})
        self.assertIsNone(resp)
        self.assertEqual(err, "no servers configured")


if __name__ == "__main__":
    unittest.main()
