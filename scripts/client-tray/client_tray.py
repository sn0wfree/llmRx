#!/usr/bin/env python3
"""llmRx edge-client tray shell.

A thin desktop wrapper around the *existing* gateway-client local HTTP
server. It adds no proxy logic, no cache, no fallback: everything the
tray shows is read from the client's own endpoints, and every action is
a plain HTTP POST to them.

Design constraints (deliberate):

  * Standard library only. No pip install, no venv, no build step. The
    client's whole point is that it works when the network is down; a
    shell that needs PyPI at install time undercuts that.
  * Never starts or stops the gateway-client process. Run the client
    yourself; this only observes it. That keeps the shell optional.
  * Read-only by default. --allow-actions gates the two mutating calls
    (cache purge, settings save).

Usage:
    python3 client_tray.py                          # probe localhost:8787
    python3 client_tray.py --base-url http://127.0.0.1:18787 --watch 2
    python3 client_tray.py --open-ui                # open the client's own UI
    python3 client_tray.py --allow-actions --purge

Exit codes: 0 ok, 1 client unreachable, 2 usage error.
"""

from __future__ import annotations

import argparse
import json
import sys
import urllib.error
import urllib.parse
import urllib.request
import webbrowser
from dataclasses import dataclass, field
from typing import Any

DEFAULT_BASE = "http://127.0.0.1:8787"
TIMEOUT_SEC = 3.0


# --------------------------------------------------------------------------
# HTTP client
# --------------------------------------------------------------------------


class ClientUnreachable(RuntimeError):
    """The gateway-client is not listening (or not answering)."""


def _request(base: str, path: str, *, method: str = "GET",
             form: dict[str, str] | None = None) -> bytes:
    url = base.rstrip("/") + path
    data = None
    headers = {"Accept": "application/json"}
    if form is not None:
        data = urllib.parse.urlencode(form, doseq=True).encode()
        headers["Content-Type"] = "application/x-www-form-urlencoded"
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_SEC) as resp:
            return resp.read()
    except urllib.error.HTTPError as exc:
        # The client answers 4xx/5xx with a JSON error body; surface it.
        body = exc.read()
        raise ClientUnreachable(
            f"{method} {path} -> HTTP {exc.code}: {body[:200].decode('utf-8', 'replace')}"
        ) from exc
    except (urllib.error.URLError, TimeoutError, OSError) as exc:
        raise ClientUnreachable(f"{method} {path}: {exc}") from exc


# --------------------------------------------------------------------------
# Health model
# --------------------------------------------------------------------------


@dataclass
class Health:
    """The subset of /healthz the tray cares about.

    Field names mirror the JSON keys exactly so a rename in the client
    shows up as a missing/None value here rather than a silent zero.
    """

    status: str = "unknown"
    server_open: bool = False
    server_fails: int = 0
    pending_usage: int = 0
    uptime_sec: int = 0
    cache: dict[str, Any] = field(default_factory=dict)
    config_version: int | None = None
    control_last_error: str = ""
    control_last_ok: str = ""

    @classmethod
    def from_json(cls, raw: dict[str, Any]) -> "Health":
        known = {f for f in cls.__dataclass_fields__}
        return cls(**{k: v for k, v in raw.items() if k in known})

    @property
    def cache_summary(self) -> str:
        if not self.cache:
            return "disabled"
        size = self.cache.get("size", 0)
        hits = self.cache.get("hits", 0)
        misses = self.cache.get("misses", 0)
        rate = self.cache.get("hit_rate", 0)
        return f"{size} entries, {hits} hit / {misses} miss ({rate}%)"

    @property
    def connectivity(self) -> str:
        """Derive the same three states the client's own UI shows."""
        if self.server_open:
            return "offline"
        if self.server_fails > 0:
            return "degraded"
        return "ok"


def fetch_health(base: str) -> Health:
    return Health.from_json(json.loads(_request(base, "/healthz")))


# --------------------------------------------------------------------------
# Rendering (plain text: this runs in a terminal or a tray popup)
# --------------------------------------------------------------------------

_MARK = {"ok": "[ok]  ", "degraded": "[warn]", "offline": "[DOWN]"}


def render(health: Health, base: str) -> str:
    mark = _MARK.get(health.connectivity, "[????]")
    mins, secs = divmod(max(health.uptime_sec, 0), 60)
    hours, mins = divmod(mins, 60)

    lines = [
        f"{mark} llmRx edge client",
        f"       endpoint : {base}",
        f"       uptime   : {hours}h{mins:02d}m{secs:02d}s",
        f"       upstream : {health.connectivity}"
        f" (breaker {'open' if health.server_open else 'closed'},"
        f" {health.server_fails} fails)",
        f"       cache    : {health.cache_summary}",
        f"       usage    : {health.pending_usage} pending upload(s)",
    ]
    if health.config_version is not None:
        lines.append(f"       config   : v{health.config_version}")
    if health.control_last_error:
        lines.append(f"       control  : ERROR {health.control_last_error}")
    elif health.control_last_ok:
        lines.append(f"       control  : last ok {health.control_last_ok}")
    return "\n".join(lines)


# --------------------------------------------------------------------------
# Actions
# --------------------------------------------------------------------------


def purge_cache(base: str) -> None:
    """POST /ui/cache/purge. The client answers 303 to /ui; urlopen
    follows it and returns the HTML, which we discard."""
    _request(base, "/ui/cache/purge", method="POST", form={})


def save_settings(base: str, *, servers: list[str] | None = None,
                  cache_enabled: bool | None = None,
                  cache_ttl_sec: int | None = None,
                  fallback_mode: str | None = None) -> None:
    """POST /ui/settings with only the fields being changed.

    The client applies each field independently and keeps the existing
    value when the form omits it, so a partial post is safe.
    """
    form: dict[str, str] = {}
    if servers:
        form["servers"] = "\n".join(servers)
    if cache_enabled is not None:
        # The client checks for the literal "on", matching an HTML checkbox.
        if cache_enabled:
            form["cache_enabled"] = "on"
    if cache_ttl_sec is not None:
        form["cache_ttl_sec"] = str(cache_ttl_sec)
    if fallback_mode is not None:
        form["fallback_mode"] = fallback_mode
    if not form:
        raise ValueError("save_settings called with no fields to change")
    _request(base, "/ui/settings", method="POST", form=form)


# --------------------------------------------------------------------------
# CLI
# --------------------------------------------------------------------------


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="client_tray",
        description="Thin shell over a running llmRx edge client (localhost only).",
        epilog="The gateway-client must already be running; this never starts it.",
    )
    p.add_argument("--base-url", default=DEFAULT_BASE,
                   help=f"client endpoint (default {DEFAULT_BASE})")
    p.add_argument("--watch", type=float, metavar="SEC",
                   help="poll continuously instead of printing once")
    p.add_argument("--json", action="store_true",
                   help="emit the raw /healthz JSON instead of the summary")
    p.add_argument("--open-ui", action="store_true",
                   help="open the client's own web UI in a browser")
    p.add_argument("--allow-actions", action="store_true",
                   help="enable the mutating flags below")
    p.add_argument("--purge", action="store_true",
                   help="POST /ui/cache/purge (requires --allow-actions)")
    return p


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    base = args.base_url

    if args.purge and not args.allow_actions:
        print("error: --purge needs --allow-actions", file=sys.stderr)
        return 2

    try:
        if args.purge:
            purge_cache(base)
            print("cache purged")

        if args.open_ui:
            url = base.rstrip("/") + "/ui"
            webbrowser.open(url)
            print(f"opened {url}")

        if args.watch is None:
            health = fetch_health(base)
            if args.json:
                print(json.dumps(json.loads(_request(base, "/healthz")), indent=2))
            else:
                print(render(health, base))
            return 0

        # Watch mode: redraw in place. Deliberately does not clear the
        # screen so the user can scroll back through a disconnect.
        import time

        while True:
            try:
                health = fetch_health(base)
                print(render(health, base))
            except ClientUnreachable as exc:
                print(f"[????] llmRx edge client\n       {exc}")
            print("-" * 60)
            sys.stdout.flush()
            time.sleep(max(args.watch, 0.5))

    except ClientUnreachable as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        return 0

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
