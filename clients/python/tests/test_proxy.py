import json
import threading
import time
import unittest

from tests.helpers import ClientHarness, MockUpstream, chat_request, make_cfg


class TestChatDataPlane(unittest.TestCase):
    def test_token_injected_when_agent_sent_none(self):
        with MockUpstream([(200, {}, b"{}")]) as up:
            h = ClientHarness(make_cfg([up.url], token="user-token"))
            try:
                h.chat(chat_request())
            finally:
                h.stop()
        sent = up.requests[0]
        self.assertEqual(sent["headers"].get("Authorization"), "Bearer user-token")
        self.assertEqual(sent["headers"].get("X-Llmrx-Client"), "test-edge")
        self.assertTrue(sent["headers"].get("X-Forwarded-For"))

    def test_caller_token_wins(self):
        with MockUpstream([(200, {}, b"{}")]) as up:
            h = ClientHarness(make_cfg([up.url], token="user-token"))
            try:
                h.chat(chat_request(), headers={"Authorization": "Bearer agent"})
            finally:
                h.stop()
        self.assertEqual(up.requests[0]["headers"].get("Authorization"),
                         "Bearer agent")

    def test_success_passthrough_and_proxy_usage(self):
        body = json.dumps({"choices": [], "usage": {"prompt_tokens": 5,
                                                    "completion_tokens": 7}})
        with MockUpstream([(200, {}, body.encode())]) as up:
            h = ClientHarness(make_cfg([up.url]))
            try:
                status, headers, data = h.chat(chat_request())
            finally:
                h.stop()
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(data)["usage"]["prompt_tokens"], 5)
        row = h.buffer.take_all()[0].to_json()
        self.assertEqual(row["source"], "proxy")
        self.assertEqual(row["status"], 200)
        self.assertEqual(row["model"], "gpt-test")
        # proxy rows carry no tokens: the server already billed them
        self.assertEqual(row["prompt_tokens"], 0)

    def test_cache_hit_only_for_deterministic_requests(self):
        with MockUpstream([(200, {}, json.dumps(
                {"choices": [], "usage": {"prompt_tokens": 5}}).encode())]) as up:
            h = ClientHarness(make_cfg([up.url]))
            try:
                s1, h1, _ = h.chat(chat_request())
                s2, h2, _ = h.chat(chat_request())          # cached
                s3, h3, _ = h.chat(chat_request(temperature=0.5))  # not cacheable
            finally:
                h.stop()
        self.assertEqual(h2.get("X-Llmrx-Cache"), "hit")
        self.assertNotIn("X-Llmrx-Cache", h3 or {})
        self.assertEqual(len(up.requests), 2)  # first + temperature=0.5

    def test_cache_disabled_flag_now_honored(self):
        with MockUpstream([(200, {}, json.dumps(
                {"choices": [], "usage": {}}).encode())]) as up:
            h = ClientHarness(make_cfg([up.url], cache_enabled=False))
            try:
                h.chat(chat_request())
                s, hd, _ = h.chat(chat_request())
            finally:
                h.stop()
        self.assertNotIn("X-Llmrx-Cache", hd)
        self.assertEqual(len(up.requests), 2)

    def test_streaming_not_cached_but_body_forwarded(self):
        """Regression: the Go client forwarded an empty body for every
        streaming request (docs/P18 §5.1)."""
        sse = (b"HTTPBODY")  # placeholder; MockUpstream serves it plainly
        with MockUpstream([(200, {"Content-Type": "text/event-stream"}, sse)]) as up:
            h = ClientHarness(make_cfg([up.url]))
            try:
                status, headers, data = h.chat(
                    chat_request(stream=True),
                    headers={"Accept": "text/event-stream"})
            finally:
                h.stop()
        self.assertEqual(status, 200)
        self.assertEqual(data, sse)
        sent = up.requests[0]
        self.assertEqual(sent["body"], chat_request(stream=True))
        self.assertEqual(sent["headers"].get("Accept"), "text/event-stream")
        # and nothing entered the cache
        self.assertEqual(h.proxy.cache.stats()["size"], 0)

    def test_offline_fallback_serves_cache(self):
        body = json.dumps({"choices": [], "usage": {"prompt_tokens": 9}})
        with MockUpstream([(200, {}, body.encode())]) as up:
            h = ClientHarness(make_cfg([up.url]))
            try:
                h.chat(chat_request())  # populate cache
                up._server.shutdown()   # upstream dies
                time.sleep(0.1)
                status, headers, data = h.chat(chat_request())
            finally:
                h.stop()
        self.assertEqual(status, 200)
        self.assertEqual(headers.get("X-Llmrx-Cache"), "hit")
        self.assertEqual(json.loads(data)["usage"]["prompt_tokens"], 9)
        rows = h.buffer.take_all()
        self.assertEqual(rows[-1].to_json()["source"], "cache")

    def test_offline_no_cache_gives_503_shape(self):
        with MockUpstream([(200, {}, b"{}")]) as up:
            h = ClientHarness(make_cfg([up.url], cache_enabled=False))
            try:
                up._server.shutdown()
                up._server.server_close()  # close the listener: refuse
                time.sleep(0.1)
                status, _, data = h.chat(chat_request())
            finally:
                h.stop()
        self.assertEqual(status, 503)
        doc = json.loads(data)
        self.assertEqual(doc["error"]["code"], "client_upstream_unreachable")
        self.assertIn("no cached response", doc["error"]["message"])
        row = h.buffer.take_all()[-1].to_json()
        self.assertEqual(row["status"], 0)
        self.assertEqual(row["source"], "proxy")

    def test_business_error_passthrough_without_breaker_trip(self):
        import faulthandler
        with MockUpstream([(401, {}, b'{"error":{"message":"bad key"}}')]) as up:
            h = ClientHarness(make_cfg([up.url]))
            try:
                for i in range(5):
                    faulthandler.dump_traceback_later(6, exit=True)
                    status, _, data = h.chat(chat_request())
                    faulthandler.cancel_dump_traceback_later()
                    self.assertEqual(status, 401)
                    self.assertIn("bad key", data.decode())
            finally:
                h.stop()
        open_b, fails = h.proxy.brk.state()
        self.assertFalse(open_b)
        self.assertEqual(fails, 0)

    def test_breaker_opens_after_three_5xx_not_two(self):
        """Regression: Go counted buffered 5xx twice and opened after 2
        (docs/P18 §5.2). One failure per request → opens on the third."""
        with MockUpstream([(500, {}, b"boom")]) as up:
            h = ClientHarness(make_cfg([up.url], cache_enabled=False))
            try:
                h.chat(chat_request())
                h.chat(chat_request())
                self.assertFalse(h.proxy.brk.state()[0],
                                 "must not open after 2 requests")
                h.chat(chat_request())
                self.assertTrue(h.proxy.brk.state()[0],
                                "must open after the 3rd request")
                self.assertEqual(h.proxy.brk.state()[1], 3)
                status, _, _ = h.chat(chat_request())
                self.assertEqual(status, 500)  # buffered chat always dials
                self.assertEqual(h.proxy.brk.state()[1], 4)
            finally:
                h.stop()

    def test_failover_to_second_server(self):
        with MockUpstream() as dead, MockUpstream([(200, {}, b"{}")]) as live:
            dead._server.shutdown()
            dead._server.server_close()  # close the listener: refuse, don't queue
            time.sleep(0.05)
            h = ClientHarness(make_cfg([dead.url, live.url]))
            try:
                status, _, _ = h.chat(chat_request())
            finally:
                h.stop()
        self.assertEqual(status, 200)
        self.assertEqual(len(live.requests), 1)

    def test_healthz_shape(self):
        with MockUpstream([(200, {}, b"{}")]) as up:
            h = ClientHarness(make_cfg([up.url]))
            try:
                status, _, data = h.request("GET", "/healthz")
            finally:
                h.stop()
        doc = json.loads(data)
        self.assertEqual(status, 200)
        for key in ("status", "server_open", "server_fails", "pending_usage",
                    "cache", "uptime_sec"):
            self.assertIn(key, doc)
        self.assertEqual(doc["status"], "ok")

    def test_method_not_allowed(self):
        with MockUpstream() as up:
            h = ClientHarness(make_cfg([up.url]))
            try:
                status, _, data = h.request("GET", "/v1/chat/completions")
            finally:
                h.stop()
        self.assertEqual(status, 405)
        self.assertEqual(json.loads(data)["error"]["message"],
                         "chat/completions requires POST")

    def test_unparseable_body_forwarded_verbatim(self):
        with MockUpstream([(400, {}, b'{"error":{"message":"bad json"}}')]) as up:
            h = ClientHarness(make_cfg([up.url]))
            try:
                status, _, data = h.chat(b"{not json}")
            finally:
                h.stop()
        self.assertEqual(status, 400)
        self.assertEqual(up.requests[0]["body"], b"{not json}")


class TestPassthrough(unittest.TestCase):
    def test_models_forwarded_with_path_and_query(self):
        with MockUpstream([(200, {}, b'{"data":[]}')]) as up:
            h = ClientHarness(make_cfg([up.url], token="user-token"))
            try:
                status, _, data = h.request("GET", "/v1/models?limit=5")
            finally:
                h.stop()
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(data)["data"], [])
        sent = up.requests[0]
        self.assertEqual(sent["path"], "/v1/models?limit=5")
        self.assertEqual(sent["headers"].get("Authorization"), "Bearer user-token")

    def test_passthrough_post_body_forwarded(self):
        """Second half of the Go empty-body regression (docs/P18 §5.1)."""
        payload = json.dumps({"model": "text-embed", "input": ["hi"]}).encode()
        with MockUpstream([(200, {}, b'{"data":[[0.1]]}')]) as up:
            h = ClientHarness(make_cfg([up.url]))
            try:
                status, _, _ = h.request("POST", "/v1/embeddings", payload,
                                         {"Content-Type": "application/json"})
            finally:
                h.stop()
        self.assertEqual(status, 200)
        self.assertEqual(up.requests[0]["body"], payload)

    def test_passthrough_records_no_usage(self):
        with MockUpstream([(200, {}, b"{}")]) as up:
            h = ClientHarness(make_cfg([up.url]))
            try:
                h.request("GET", "/v1/models")
            finally:
                h.stop()
        self.assertEqual(len(h.buffer), 0)

    def test_sse_headers_survive_passthrough(self):
        with MockUpstream([(200, {"Content-Type": "text/event-stream",
                                  "X-Custom": "1"},
                            b"data: x\n\n")]) as up:
            h = ClientHarness(make_cfg([up.url]))
            try:
                status, headers, _ = h.request("GET", "/upstream/stream")
            finally:
                h.stop()
        self.assertEqual(headers.get("Content-Type"), "text/event-stream")
        self.assertEqual(headers.get("X-Custom"), "1")


class TestMaxInflight(unittest.TestCase):
    def test_limit_enforced(self):
        """Go never enforced max_inflight (docs/P18 §5.4); the Python
        client returns 503 when the cap is reached."""
        with MockUpstream([(200, {}, b"{}")]) as up:
            up.gate.clear()  # pin the first request in-flight upstream
            h = ClientHarness(make_cfg([up.url], max_inflight=1))
            result = {}

            def slow():
                try:
                    result["status"] = h.chat(chat_request())[0]
                except Exception as exc:  # noqa: BLE001
                    result["error"] = exc

            try:
                t = threading.Thread(target=slow, daemon=True)
                t.start()
                time.sleep(0.4)  # let it reach the upstream and block
                status, _, data = h.chat(chat_request())
                self.assertEqual(status, 503)
                self.assertEqual(json.loads(data)["error"]["code"],
                                 "client_overloaded")
                up.gate.set()
                t.join(timeout=10)
                self.assertEqual(result.get("status"), 200)
            finally:
                up.gate.set()
                h.stop()


if __name__ == "__main__":
    unittest.main()
