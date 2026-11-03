import json
import unittest
import urllib.error
from unittest import mock

from llmrx_client.config import Config, apply_bundle
from llmrx_client.control import Control, VerifyError, extract_raw_member, verify_bundle
from tests.helpers import MockServerAPI, sign_bundle

TOKEN = "ecs_test_secret"


class TestRawMemberExtraction(unittest.TestCase):
    def test_extracts_raw_config_bytes(self):
        raw = sign_bundle({"version": 1, "note": "a<b&c"}, TOKEN)
        raw_config, config = extract_raw_member(raw, "config")
        self.assertEqual(config["version"], 1)
        # The raw slice is byte-identical to what the server HMAC'd —
        # re-serializing in Python would reorder/escape differently.
        expected = json.dumps({"version": 1, "note": "a<b&c"},
                              separators=(",", ":"), ensure_ascii=False).encode()
        self.assertEqual(raw_config, expected)

    def test_unexpected_envelope_rejected(self):
        with self.assertRaises(VerifyError):
            extract_raw_member(b'not json', "config")
        # No config member at all → rejected.
        with self.assertRaises(VerifyError):
            extract_raw_member(b'{"zzz":1,"other":2}', "config")
        with self.assertRaises(VerifyError):
            extract_raw_member(b'{}', "config")

    def test_config_position_independent(self):
        """The member may appear anywhere in the envelope; the raw bytes
        extracted for the HMAC are still exactly what was serialized."""
        raw = json.dumps(
            {"signature": "x", "config": {"version": 3}, "algorithm": "hmac-sha256"},
            separators=(",", ":")).encode()
        raw_config, config = extract_raw_member(raw, "config")
        self.assertEqual(config["version"], 3)
        self.assertEqual(raw_config, b'{"version":3}')


class TestVerify(unittest.TestCase):
    def test_valid_signature_passes(self):
        raw = sign_bundle({"version": 2, "servers": ["http://s"]}, TOKEN)
        bundle = verify_bundle(raw, TOKEN)
        self.assertEqual(bundle["version"], 2)

    def test_tampered_payload_rejected(self):
        raw = sign_bundle({"version": 2, "servers": ["http://s"]}, TOKEN)
        doc = json.loads(raw)
        doc["config"]["servers"] = ["http://evil"]
        tampered = json.dumps(doc, separators=(",", ":")).encode()
        with self.assertRaises(VerifyError):
            verify_bundle(tampered, TOKEN)

    def test_wrong_key_rejected(self):
        raw = sign_bundle({"version": 1}, TOKEN)
        with self.assertRaises(VerifyError):
            verify_bundle(raw, "ecs_other_client")

    def test_unknown_algorithm_rejected(self):
        raw = json.dumps({"config": {"version": 1}, "signature": "ab" * 32,
                          "algorithm": "hmac-md5"}, separators=(",", ":")).encode()
        with self.assertRaises(VerifyError):
            verify_bundle(raw, TOKEN)

    def test_garbage_signature_rejected(self):
        raw = json.dumps({"config": {"version": 1}, "signature": "nothex",
                          "algorithm": "hmac-sha256"}, separators=(",", ":")).encode()
        with self.assertRaises(VerifyError):
            verify_bundle(raw, TOKEN)


class TestControlRounds(unittest.TestCase):
    def _control(self, api: MockServerAPI, applied: int = 0, on_bundle=None):
        return Control(api.url, "edge-1", TOKEN,
                       version_fn=lambda: "test", on_bundle=on_bundle)

    def test_heartbeat_reports_applied_version(self):
        with MockServerAPI() as api:
            ctl = self._control(api)
            ctl.round_once()
            self.assertEqual(api.heartbeats[-1]["config_version"], 0)
            self.assertEqual(api.heartbeats[-1]["client_version"], "test")
            self.assertEqual(ctl.applied_version(), 1)  # bundle applied

    def test_second_round_skips_fetch_when_current(self):
        with MockServerAPI() as api:
            ctl = self._control(api)
            ctl.round_once()
            rounds = len(api.heartbeats)
            ctl.round_once()  # applied == server version → no refetch
            self.assertEqual(len(api.heartbeats), rounds + 1)

    def test_downgrade_refused(self):
        with MockServerAPI() as api:
            ctl = self._control(api)
            ctl.round_once()
            applied = ctl.applied_version()
            api.bundle = dict(api.bundle, version=applied - 1)
            api.need_config = True
            with self.assertRaises(VerifyError):
                ctl.round_once()
            # Last-good config stays applied
            self.assertEqual(ctl.applied_version(), applied)
            self.assertIn("refusing downgrade", ctl.last_error())

    def test_equal_version_reapplied_not_refused(self):
        with MockServerAPI() as api:
            ctl = self._control(api)
            ctl.round_once()
            self.assertEqual(ctl.applied_version(), 1)
            # Server says the client is stale even though versions match;
            # equal version must re-apply (only strictly-less is refused).
            with mock.patch.object(ctl, "heartbeat", return_value=(1, True)):
                ctl.round_once()  # must not raise
            self.assertEqual(ctl.applied_version(), 1)

    def test_applied_version_set_before_callback(self):
        seen = {}

        def cb(bundle):
            seen["version_at_callback"] = ctl.applied_version()
            return ["note"]

        with MockServerAPI() as api:
            ctl = self._control(api, on_bundle=cb)
            ctl.round_once()
            self.assertEqual(seen["version_at_callback"], 1)

    def test_wrong_token_cannot_fetch(self):
        with MockServerAPI() as api:
            ctl = Control(api.url, "edge-1", "ecs_wrong",
                          version_fn=lambda: "t")
            with self.assertRaises(urllib.error.HTTPError):
                ctl.round_once()
            self.assertIn("401", ctl.last_error())

    def test_bundle_applies_to_cfg(self):
        cfg = Config(servers=["http://old"], cache_enabled=False)
        notes: list[str] = []

        def cb(bundle):
            notes.extend(apply_bundle(cfg, bundle))
            return notes

        with MockServerAPI(bundle={
            "version": 7,
            "servers": ["http://a", "http://b"],
            "cache": {"enabled": True, "ttl_sec": 1200, "max_items": 300},
            "fallback": {"mode": "cache_then_fail"},
            "limits": {"max_inflight": 8},
        }) as api:
            ctl = self._control(api, on_bundle=cb)
            ctl.round_once()
        self.assertEqual(cfg.servers, ["http://a", "http://b"])
        self.assertTrue(cfg.cache_enabled)
        self.assertEqual(cfg.cache_ttl_sec, 1200)
        self.assertEqual(cfg.max_inflight, 8)
        self.assertEqual(ctl.applied_version(), 7)


if __name__ == "__main__":
    unittest.main()
