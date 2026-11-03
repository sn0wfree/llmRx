import json
import re
import unittest
import urllib.parse

from llmrx_client import ui as ui_mod
from tests.helpers import ClientHarness, MockUpstream, chat_request, make_cfg


class TestMasking(unittest.TestCase):
    def test_mask(self):
        self.assertEqual(ui_mod.mask(""), "(未设置)")
        self.assertEqual(ui_mod.mask("short"), "***")
        self.assertEqual(ui_mod.mask("sk-1234567890abcdef"), "sk-12***cdef")

    def test_parse_int_stops_at_non_digit_and_clamps(self):
        self.assertEqual(ui_mod.parse_int("3600abc"), 3600)
        self.assertEqual(ui_mod.parse_int(""), 0)
        self.assertEqual(ui_mod.parse_int("999999999999999"), 1_000_000_000)

    def test_format_rate(self):
        self.assertEqual(ui_mod.format_rate(0, 0), "—")
        self.assertEqual(ui_mod.format_rate(3, 1), "75.0%")
        self.assertEqual(ui_mod.format_rate(1, 0), "100.0%")


class TestStatusPage(unittest.TestCase):
    def _page(self, cfg_over=None):
        with MockUpstream([(200, {}, b"{}")]) as up:
            h = ClientHarness(make_cfg([up.url], **(cfg_over or {})))
            try:
                status, headers, data = h.request("GET", "/ui")
            finally:
                h.stop()
        return status, headers, data

    def test_renders_html_zero_external_assets(self):
        status, headers, body = self._page()
        self.assertEqual(status, 200)
        self.assertIn("text/html", headers.get("Content-Type", ""))
        text = body.decode()
        # No CDN/script/img/link references to the outside world: the
        # page must render identically with the network down.
        for tag in ("script", "link", "img", "iframe"):
            self.assertIsNone(re.search(rf"<{tag}\b", text, re.I),
                              f"unexpected <{tag}> in status page")
        externals = re.findall(r'(?:src|href)="(https?://[^"]+)"', text)
        self.assertEqual(externals, [])

    def test_shows_status_panels(self):
        _, _, body = self._page()
        text = body.decode()
        self.assertIn("llmRx Client", text)
        self.assertIn("缓存", text)
        self.assertIn("用量", text)
        self.assertIn("设置", text)
        self.assertIn("(未设置)", text)  # masked empty tokens

    def test_get_on_action_routes_is_405(self):
        with MockUpstream([(200, {}, b"{}")]) as up:
            h = ClientHarness(make_cfg([up.url]))
            try:
                s1, _, _ = h.request("GET", "/ui/settings")
                s2, _, _ = h.request("GET", "/ui/cache/purge")
            finally:
                h.stop()
        self.assertEqual(s1, 405)
        self.assertEqual(s2, 405)

    def test_settings_partial_update(self):
        with MockUpstream([(200, {}, b"{}")]) as up:
            h = ClientHarness(make_cfg([up.url], token="user-token",
                                       client_token="ecs-secret",
                                       client_name="edge-x"))
            try:
                # Only change TTL; everything else must survive.
                form = urllib.parse.urlencode({
                    "servers": "",            # blank → servers kept
                    "cache_ttl_sec": "1200",
                    "cache_max_items": "",
                    # cache_enabled unchecked
                    "fallback_mode": "",
                }).encode()
                status, headers, _ = h.request("POST", "/ui/settings", form,
                    {"Content-Type": "application/x-www-form-urlencoded"})
                self.assertEqual(status, 303)
                self.assertIn("flash=", headers["Location"])
            finally:
                h.stop()
        self.assertEqual(h.proxy.cfg.cache_ttl_sec, 1200)
        self.assertEqual(h.proxy.cfg.cache_max_items, 2000)   # unchanged
        self.assertFalse(h.proxy.cfg.cache_enabled)           # unchecked = off
        self.assertEqual(h.proxy.cfg.servers, [up.url])       # blank kept
        self.assertEqual(h.proxy.cfg.token, "user-token")     # credentials
        self.assertEqual(h.proxy.cfg.client_token, "ecs-secret")
        self.assertEqual(h.proxy.cfg.client_name, "edge-x")

    def test_settings_invalid_fallback_ignored(self):
        with MockUpstream([(200, {}, b"{}")]) as up:
            h = ClientHarness(make_cfg([up.url]))
            try:
                form = urllib.parse.urlencode({
                    "fallback_mode": "bogus"}).encode()
                h.request("POST", "/ui/settings", form,
                          {"Content-Type": "application/x-www-form-urlencoded"})
            finally:
                h.stop()
        self.assertEqual(h.proxy.cfg.fallback_mode, "")

    def test_purge_clears_cache(self):
        with MockUpstream([(200, {}, json.dumps(
                {"choices": [], "usage": {}}).encode())]) as up:
            h = ClientHarness(make_cfg([up.url]))
            try:
                h.chat(chat_request())
                self.assertEqual(h.proxy.cache.stats()["size"], 1)
                form = urllib.parse.urlencode({}).encode()
                status, headers, _ = h.request("POST", "/ui/cache/purge", form,
                    {"Content-Type": "application/x-www-form-urlencoded"})
                self.assertEqual(status, 303)
                self.assertIn("flash=", headers["Location"])
                self.assertEqual(h.proxy.cache.stats()["size"], 0)
            finally:
                h.stop()


from tests.helpers import chat_request  # noqa: E402

if __name__ == "__main__":
    unittest.main()
