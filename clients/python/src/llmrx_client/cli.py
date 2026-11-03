"""CLI: -config / -version / -enroll. Mirrors cmd/gateway-client/main.go.

Startup order matters: bind the socket before printing readiness, drain
for up to 15s on SIGINT/SIGTERM, and flush the usage buffer to JSONL on
the way out.
"""

from __future__ import annotations

import argparse
import platform
import signal
import sys
import threading
from http.client import HTTPSConnection  # noqa: F401 — document HTTPS support

from . import BUILD_COMMIT, BUILD_DATE, __version__
from . import control as control_mod
from . import enroll as enroll_mod
from .config import Config, ConfigError, apply_bundle, load_config, write_config
from .proxy import Proxy, serve as serve_proxy
from .usage import (
    JSONLUsageStore,
    UsageBuffer,
    start_recorder,
    start_uploader,
)

DEFAULT_CONFIG = "client.yml"


def build_version() -> str:
    commit = BUILD_COMMIT[:7] if BUILD_COMMIT else "none"
    return f"{__version__} ({commit}, {BUILD_DATE}, Python {platform.python_version()}, edge)"


def _fatal(message: str) -> "None":
    print(f"gateway-client: {message}", file=sys.stderr)
    raise SystemExit(1)


def _load_or_init_config(path: str, server_flag: str) -> Config:
    """Enroll path: a missing config starts bare (no normalize), like the
    Go client, so `-enroll` works before any config exists."""
    import os

    if os.path.exists(path):
        return load_config(path)
    return Config(listen="127.0.0.1:3425", cache_enabled=True, servers=(
        [server_flag] if server_flag else []))


def run_enroll(args) -> int:
    cfg = _load_or_init_config(args.config, args.server)
    base = args.server or (cfg.servers[0] if cfg.servers else "")
    if not base:
        _fatal("no server: pass -server https://llmrx.example.com "
               "(or set servers in the config file)")
    try:
        result = enroll_mod.enroll(base, args.enroll, args.enroll_name or "")
    except enroll_mod.EnrollError as exc:
        _fatal(str(exc))
    with cfg.mu():
        cfg.client_token = result["client_secret"]
        cfg.client_name = result["name"]
        if not cfg.usage_file:
            import os
            cfg.usage_file = os.path.join(
                os.path.dirname(os.path.abspath(args.config)), "usage.jsonl")
    try:
        write_config(args.config, cfg)
    except OSError as exc:
        _fatal(f"write config: {exc}")

    import os
    print(f'enrolled as "{result["name"]}"')
    print(f"  client_id: {result['client_id']}")
    print(f"  config:    {args.config} (mode {oct(os.stat(args.config).st_mode & 0o777)})")
    print("run the client without -enroll to start serving")
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="gateway-client",
        description="llmRx edge client: local OpenAI-compatible proxy "
                    "with cache, offline fallback and usage accounting.",
    )
    parser.add_argument("-config", default=DEFAULT_CONFIG,
                        help=f"config file path (default {DEFAULT_CONFIG})")
    parser.add_argument("-version", action="store_true",
                        help="print version and exit")
    parser.add_argument("-enroll", default="",
                        metavar="CODE",
                        help="one-time enroll code; enroll, write config, exit")
    parser.add_argument("-enroll-name", default="", metavar="NAME",
                        help="optional client label for -enroll")
    parser.add_argument("-server", default="", metavar="URL",
                        help="llmRx server base URL for -enroll")
    args = parser.parse_args(argv)

    if args.version:
        print(build_version())
        return 0

    if args.enroll:
        return run_enroll(args)

    # ---- serve path ------------------------------------------------------
    try:
        cfg = load_config(args.config)
        cfg.normalize()
    except ConfigError as exc:
        _fatal(str(exc))

    from .proxy import Proxy

    buffer = UsageBuffer(5000)
    stop = threading.Event()
    control = None
    if cfg.client_token:
        first_server = cfg.servers[0].rstrip("/") if cfg.servers else ""

        def on_bundle(bundle: dict) -> list[str]:
            return apply_bundle(cfg, bundle)

        control = control_mod.Control(
            first_server, cfg.client_name, cfg.client_token,
            version_fn=build_version, on_bundle=on_bundle)

    # The proxy holds the control handle so /healthz can report
    # config_version and control-plane status.
    proxy = Proxy(cfg, build_version(), buffer=buffer, control=control)
    proxy.cfg_path = args.config

    usage_store = None
    if cfg.usage_file:
        try:
            usage_store = JSONLUsageStore(cfg.usage_file)
        except OSError as exc:
            _fatal(f"open usage file: {exc}")

    try:
        server = serve_proxy(cfg, proxy)
    except OSError as exc:
        _fatal(f"listen {cfg.listen}: {exc}")

    if usage_store is not None:
        start_recorder(buffer, usage_store, stop)
    if control is not None and cfg.servers:
        t = threading.Thread(
            target=control.run, args=(stop, 30.0),
            name="control", daemon=True)
        t.start()
    start_uploader(buffer, cfg.usage_url, cfg.client_token,
                   cfg.client_name, cfg.usage_upload_sec, stop,
                   store=usage_store)

    def request_shutdown(signum, frame) -> None:
        print("shutting down: draining in-flight requests…")
        stop.set()
        threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGINT, request_shutdown)
    signal.signal(signal.SIGTERM, request_shutdown)

    print(f"llmRx client listening on {cfg.listen} → "
          f"{cfg.servers[0] if cfg.servers else '?'} (version {build_version()})")
    print(f"status page: http://{cfg.listen}/ui")

    try:
        server.serve_forever(poll_interval=0.5)
    finally:
        server.server_close()
        proxy.close(15.0)
        stop.set()
        if usage_store is not None:
            # Final persist: the recorder's last tick may not have fired.
            from .usage import _drain_to_store
            _drain_to_store(buffer, usage_store)
            usage_store.close()
        print("bye")
    return 0
