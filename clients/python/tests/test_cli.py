"""CLI tests (P20 6.1).

cli.py had no direct coverage: entry-point wiring, version output, and
the enroll path were only exercised through the E2E shell script.
"""

from __future__ import annotations

import io
import json
import os
import stat
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from unittest import mock

from llmrx_client import cli


class TestBuildVersion(unittest.TestCase):
    def test_format(self):
        v = cli.build_version()
        self.assertIn("edge", v)
        self.assertIn("Python", v)
        # <ver> (<commit>, <date>, Python x.y.z, edge)
        self.assertEqual(v.count("("), 1)
        self.assertEqual(v.count(")"), 1)
        self.assertTrue(v.rstrip().endswith("edge)"))


class TestVersionFlag(unittest.TestCase):
    def test_version_prints_and_exits_zero(self):
        buf = io.StringIO()
        with redirect_stdout(buf):
            rc = cli.main(["-version"])
        self.assertEqual(rc, 0)
        out = buf.getvalue()
        self.assertIn("edge", out)
        self.assertIn("Python", out)

    def test_version_does_not_touch_config(self):
        """-version must short-circuit before reading any config file."""
        buf = io.StringIO()
        with redirect_stdout(buf):
            rc = cli.main(["-version", "-config", "/nonexistent/x.yml"])
        self.assertEqual(rc, 0)


class TestServePathErrors(unittest.TestCase):
    def test_missing_config_is_fatal_exit_1(self):
        err = io.StringIO()
        with redirect_stderr(err), redirect_stdout(io.StringIO()):
            with self.assertRaises(SystemExit) as ctx:
                cli.main(["-config", "/nonexistent/definitely-not-here.yml"])
        self.assertEqual(ctx.exception.code, 1)
        self.assertIn("read /nonexistent", err.getvalue())

    def test_invalid_listen_is_fatal(self):
        with tempfile.TemporaryDirectory() as d:
            cfg = os.path.join(d, "client.yml")
            with open(cfg, "w") as fh:
                fh.write("listen: 127.0.0.1\nservers:\n  - http://s1\n")
            err = io.StringIO()
            with redirect_stderr(err), redirect_stdout(io.StringIO()):
                with self.assertRaises(SystemExit) as ctx:
                    cli.main(["-config", cfg])
            self.assertEqual(ctx.exception.code, 1)
            self.assertIn("must be host:port", err.getvalue())

    def test_no_servers_is_fatal(self):
        with tempfile.TemporaryDirectory() as d:
            cfg = os.path.join(d, "client.yml")
            with open(cfg, "w") as fh:
                fh.write("listen: 127.0.0.1:3999\n")
            err = io.StringIO()
            with redirect_stderr(err), redirect_stdout(io.StringIO()):
                with self.assertRaises(SystemExit) as ctx:
                    cli.main(["-config", cfg])
            self.assertEqual(ctx.exception.code, 1)
            self.assertIn("servers: at least one", err.getvalue())


class TestEnrollPath(unittest.TestCase):
    def test_enroll_without_server_is_fatal(self):
        with tempfile.TemporaryDirectory() as d:
            cfg = os.path.join(d, "client.yml")
            err = io.StringIO()
            with redirect_stderr(err), redirect_stdout(io.StringIO()):
                with self.assertRaises(SystemExit) as ctx:
                    cli.main(["-enroll", "code", "-config", cfg])
            self.assertEqual(ctx.exception.code, 1)
            self.assertIn("no server", err.getvalue())

    def test_enroll_success_writes_config_0600_and_usage_path(self):
        with tempfile.TemporaryDirectory() as d:
            cfg = os.path.join(d, "sub", "client.yml")
            fake = {"client_id": "ec_1", "client_secret": "ecs_secret",
                    "name": "edge-x"}
            buf = io.StringIO()
            with mock.patch("llmrx_client.enroll.enroll",
                            return_value=fake) as enroll_mock:
                with redirect_stdout(buf):
                    rc = cli.main(["-enroll", "code",
                                   "-server", "https://srv",
                                   "-config", cfg])
            self.assertEqual(rc, 0)
            enroll_mock.assert_called_once()
            self.assertIn('enrolled as "edge-x"', buf.getvalue())

            self.assertTrue(os.path.exists(cfg))
            self.assertEqual(stat.S_IMODE(os.stat(cfg).st_mode), 0o600)
            from llmrx_client.config import load_config
            saved = load_config(cfg)
            self.assertEqual(saved.client_token, "ecs_secret")
            self.assertEqual(saved.client_name, "edge-x")
            self.assertEqual(saved.servers, ["https://srv"])
            # usage file defaults next to the config
            self.assertEqual(saved.usage_file, os.path.join(d, "sub",
                                                            "usage.jsonl"))

    def test_enroll_failure_is_fatal(self):
        from llmrx_client.enroll import EnrollError

        with tempfile.TemporaryDirectory() as d:
            cfg = os.path.join(d, "client.yml")
            err = io.StringIO()
            with mock.patch("llmrx_client.enroll.enroll",
                            side_effect=EnrollError("enroll rejected (401 x)")):
                with redirect_stderr(err), redirect_stdout(io.StringIO()):
                    with self.assertRaises(SystemExit) as ctx:
                        cli.main(["-enroll", "bad", "-server", "https://s",
                                  "-config", cfg])
            self.assertEqual(ctx.exception.code, 1)
            self.assertIn("enroll rejected", err.getvalue())
            self.assertFalse(os.path.exists(cfg), "config written on failure")

    def test_enroll_existing_config_keeps_servers(self):
        with tempfile.TemporaryDirectory() as d:
            cfg = os.path.join(d, "client.yml")
            with open(cfg, "w") as fh:
                fh.write("listen: 127.0.0.1:3999\n"
                         "servers:\n  - http://existing\n"
                         "usage_file: /tmp/u.jsonl\n")
            fake = {"client_id": "ec_2", "client_secret": "ecs_2",
                    "name": "edge-y"}
            with mock.patch("llmrx_client.enroll.enroll",
                            return_value=fake):
                with redirect_stdout(io.StringIO()):
                    rc = cli.main(["-enroll", "code", "-config", cfg])
            self.assertEqual(rc, 0)
            from llmrx_client.config import load_config
            saved = load_config(cfg)
            self.assertEqual(saved.servers, ["http://existing"])
            self.assertEqual(saved.usage_file, "/tmp/u.jsonl")
            self.assertEqual(saved.client_token, "ecs_2")


class TestLoadOrInitConfig(unittest.TestCase):
    def test_missing_file_yields_bare_config(self):
        with tempfile.TemporaryDirectory() as d:
            cfg = cli._load_or_init_config(os.path.join(d, "nope.yml"),
                                           "http://srv")
            self.assertEqual(cfg.servers, ["http://srv"])
            self.assertEqual(cfg.listen, "127.0.0.1:3425")
            self.assertTrue(cfg.cache_enabled)

    def test_missing_file_no_server_flag(self):
        with tempfile.TemporaryDirectory() as d:
            cfg = cli._load_or_init_config(os.path.join(d, "nope.yml"), "")
            self.assertEqual(cfg.servers, [])


if __name__ == "__main__":
    unittest.main()
