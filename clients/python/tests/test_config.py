import json
import os
import stat
import tempfile
import unittest

from llmrx_client.config import (
    Config,
    ConfigError,
    apply_bundle,
    load_config,
    write_config,
)


class TestNormalize(unittest.TestCase):
    def test_empty_servers_is_the_problem(self):
        cfg = Config()
        with self.assertRaises(ConfigError) as ctx:
            cfg.normalize()
        self.assertEqual(
            str(ctx.exception),
            "client config: servers: at least one llmRx server URL is required",
        )

    def test_problems_sorted_and_collected(self):
        cfg = Config(servers=["ftp://x", "nope", "https://ok"])
        with self.assertRaises(ConfigError) as ctx:
            cfg.normalize()
        msg = str(ctx.exception)
        self.assertIn("servers[0]: 'ftp://x'", msg)
        self.assertIn("servers[1]: 'nope'", msg)
        self.assertNotIn("servers[2]", msg)
        self.assertLess(msg.index("servers[0]"), msg.index("servers[1]"))

    def test_defaults_applied(self):
        cfg = Config(servers=["http://s1"])
        cfg.normalize()
        self.assertEqual(cfg.listen, "127.0.0.1:3425")
        self.assertEqual(cfg.usage_upload_sec, 60)
        self.assertEqual(cfg.cache_ttl_sec, 3600)

    def test_blank_listen_counts_as_empty(self):
        cfg = Config(servers=["http://s1"], listen="   ")
        cfg.normalize()
        self.assertEqual(cfg.listen, "127.0.0.1:3425")


class TestUsageURL(unittest.TestCase):
    def test_derived_from_first_server(self):
        cfg = Config(servers=["http://s1/", "http://s2"])
        self.assertEqual(cfg.usage_url(), "http://s1/client/v1/usage")

    def test_explicit_endpoint_wins(self):
        cfg = Config(servers=["http://s1"], usage_endpoint="http://x/u")
        self.assertEqual(cfg.usage_url(), "http://x/u")

    def test_no_servers_no_url(self):
        self.assertEqual(Config().usage_url(), "")


class TestApplyBundle(unittest.TestCase):
    def _cfg(self) -> Config:
        return Config(
            listen="127.0.0.1:3425",
            servers=["http://old"],
            token="user-token",
            client_token="ecs_secret",
            client_name="edge-1",
            usage_upload_sec=60,
            cache_enabled=False,
            cache_ttl_sec=100,
            cache_max_items=10,
            fallback_mode="cache_then_fail",
            max_inflight=0,
        )

    def test_bundle_updates_data_plane_fields(self):
        cfg = self._cfg()
        apply_bundle(cfg, {
            "version": 2,
            "servers": ["http://new1", "http://new2"],
            "cache": {"enabled": True, "ttl_sec": 7200, "max_items": 500},
            "fallback": {"mode": "local"},
            "limits": {"max_inflight": 16},
        })
        self.assertEqual(cfg.servers, ["http://new1", "http://new2"])
        self.assertTrue(cfg.cache_enabled)
        self.assertEqual(cfg.cache_ttl_sec, 7200)
        self.assertEqual(cfg.cache_max_items, 500)
        self.assertEqual(cfg.fallback_mode, "local")
        self.assertEqual(cfg.max_inflight, 16)

    def test_credentials_never_touched(self):
        cfg = self._cfg()
        apply_bundle(cfg, {
            "version": 3,
            "servers": ["http://new"],
            "cache": {"enabled": False, "ttl_sec": 1, "max_items": 1},
            "fallback": {"mode": "cache_then_fail"},
            "limits": {"max_inflight": 1},
        })
        self.assertEqual(cfg.token, "user-token")
        self.assertEqual(cfg.client_token, "ecs_secret")
        self.assertEqual(cfg.client_name, "edge-1")
        self.assertEqual(cfg.listen, "127.0.0.1:3425")
        self.assertEqual(cfg.usage_upload_sec, 60)

    def test_empty_servers_kept(self):
        cfg = self._cfg()
        apply_bundle(cfg, {"version": 4, "cache": {"enabled": True,
                                                   "ttl_sec": 5,
                                                   "max_items": 5},
                           "fallback": {"mode": ""},
                           "limits": {"max_inflight": 0}})
        self.assertEqual(cfg.servers, ["http://old"])
        self.assertEqual(cfg.fallback_mode, "cache_then_fail")
        self.assertEqual(cfg.max_inflight, 0)

    def test_enabled_false_applied_unconditionally(self):
        cfg = self._cfg()
        cfg.cache_enabled = True
        apply_bundle(cfg, {"version": 5, "cache": {"enabled": False,
                                                   "ttl_sec": 0,
                                                   "max_items": 0},
                           "fallback": {}, "limits": {}})
        self.assertFalse(cfg.cache_enabled)
        self.assertEqual(cfg.cache_ttl_sec, 100)  # 0 does not overwrite


class TestConfigFile(unittest.TestCase):
    def test_roundtrip_and_mode_0600(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "client.yml")
            cfg = Config(servers=["http://s1"], token="t",
                         client_token="s", client_name="n",
                         usage_file=os.path.join(d, "u.jsonl"))
            write_config(path, cfg)
            mode = stat.S_IMODE(os.stat(path).st_mode)
            self.assertEqual(mode, 0o600)
            loaded = load_config(path)
            self.assertEqual(loaded.servers, ["http://s1"])
            self.assertEqual(loaded.token, "t")
            self.assertEqual(loaded.client_token, "s")
            self.assertEqual(loaded.client_name, "n")

    def test_missing_file_message(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "nope.yml")
            with self.assertRaises(ConfigError) as ctx:
                load_config(path)
            self.assertIn(f"read {path}", str(ctx.exception))

    def test_bad_yaml_message(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "broken.yml")
            with open(path, "w") as fh:
                fh.write("servers: [unclosed")
            with self.assertRaises(ConfigError) as ctx:
                load_config(path)
            self.assertIn(f"parse {path}", str(ctx.exception))


if __name__ == "__main__":
    unittest.main()
