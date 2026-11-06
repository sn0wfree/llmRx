"""Client configuration: YAML load, normalisation, atomic 0600 writes.

Mirrors internal/client/client.go's Config and internal/client/configfile.go.
Field names, defaults and validation messages are kept identical so a
config file written for the Go client parses here unchanged.
"""

from __future__ import annotations

import os
import tempfile
import threading
from dataclasses import dataclass, field

import yaml

DEFAULT_LISTEN = "127.0.0.1:3425"
DEFAULT_TTL_SEC = 3600
DEFAULT_UPLOAD_SEC = 60
DEFAULT_CACHE_ITEMS = 2000

FALLBACK_CACHE_THEN_FAIL = "cache_then_fail"
FALLBACK_LOCAL = "local"
_FALLBACK_MODES = (FALLBACK_CACHE_THEN_FAIL, FALLBACK_LOCAL)


class ConfigError(Exception):
    """All validation problems at once, Go-style."""


@dataclass
class Config:
    """YAML schema of client.yml. Defaults applied by normalize()."""

    listen: str = ""
    servers: list[str] = field(default_factory=list)
    token: str = ""
    client_token: str = ""
    client_name: str = ""
    usage_endpoint: str = ""
    usage_upload_sec: int = 0
    cache_enabled: bool = False
    cache_ttl_sec: int = 0
    cache_max_items: int = 0
    usage_file: str = ""
    fallback_mode: str = ""
    max_inflight: int = 0

    _mu: threading.RLock = field(default_factory=threading.RLock, repr=False)

    # -- loading ----------------------------------------------------------

    @classmethod
    def from_dict(cls, raw: dict) -> "Config":
        cfg = cls()
        for f in (
            "listen", "token", "client_token", "client_name",
            "usage_endpoint", "usage_file", "fallback_mode",
        ):
            v = raw.get(f)
            if isinstance(v, str):
                setattr(cfg, f, v)
        servers = raw.get("servers")
        if isinstance(servers, list):
            cfg.servers = [s for s in servers if isinstance(s, str)]
        for f in ("usage_upload_sec", "cache_ttl_sec", "cache_max_items", "max_inflight"):
            v = raw.get(f)
            if isinstance(v, int) and not isinstance(v, bool):
                setattr(cfg, f, v)
        if isinstance(raw.get("cache_enabled"), bool):
            cfg.cache_enabled = raw["cache_enabled"]
        return cfg

    # -- normalisation (mirrors Config.Normalize) -------------------------

    def normalize(self) -> None:
        """Apply defaults and validate. Raises ConfigError listing every
        problem at once, sorted, with the same wording as the Go client."""
        problems: list[str] = []

        with self._mu:
            if not self.servers:
                problems.append("servers: at least one llmRx server URL is required")
            for i, url in enumerate(self.servers):
                if not (url.startswith("http://") or url.startswith("https://")):
                    problems.append(
                        f"servers[{i}]: {url!r} must start with http:// or https://"
                    )
            if not self.listen.strip():
                self.listen = DEFAULT_LISTEN
            else:
                # "127.0.0.1" (host without a port) used to reach
                # make_server and crash on int("127.0.0.1") -- a bare
                # traceback at startup. Validate the host:port form
                # here, alongside every other config problem.
                host, _, port = self.listen.strip().rpartition(":")
                if not host:
                    host = "127.0.0.1"
                if not port.isdigit():
                    problems.append(
                        f"listen: {self.listen!r} must be host:port "
                        f"(e.g. {DEFAULT_LISTEN})")
                else:
                    # Keep the normalized host in the listen string so
                    # ":8787" resolves to 127.0.0.1 explicitly.
                    self.listen = f"{host}:{port}"
            if self.usage_upload_sec <= 0:
                self.usage_upload_sec = DEFAULT_UPLOAD_SEC
            if self.cache_ttl_sec <= 0:
                self.cache_ttl_sec = DEFAULT_TTL_SEC

        if problems:
            problems.sort()
            raise ConfigError("client config: " + "; ".join(problems))

    # -- concurrent accessors ---------------------------------------------

    def mu(self) -> threading.RLock:
        """Writer lock for multi-field updates (bundle apply, settings)."""
        return self._mu

    def servers_snapshot(self) -> list[str]:
        with self._mu:
            return list(self.servers)

    def fallback_mode_or_default(self) -> str:
        with self._mu:
            mode = self.fallback_mode
        return mode if mode in _FALLBACK_MODES else FALLBACK_CACHE_THEN_FAIL

    def usage_url(self) -> str:
        """Derived live from servers[0] so bundle updates retarget it."""
        with self._mu:
            servers, explicit = list(self.servers), self.usage_endpoint
        if explicit:
            return explicit
        if not servers:
            return ""
        return servers[0].rstrip("/") + "/client/v1/usage"


def load_config(path: str) -> Config:
    """Read and decode client.yml. Error wording follows main.go."""
    try:
        with open(path, "rb") as fh:
            data = fh.read()
    except OSError as exc:
        raise ConfigError(
            f"read {path}: {exc} (start from the sample in docs/P15-CLIENT.md)"
        ) from exc
    try:
        raw = yaml.safe_load(data)
    except yaml.YAMLError as exc:
        raise ConfigError(f"parse {path}: {exc}") from exc
    if raw is None:
        raw = {}
    if not isinstance(raw, dict):
        raise ConfigError(f"parse {path}: expected a YAML mapping")
    return Config.from_dict(raw)


def _restrict_to_owner(fd: int) -> None:
    """Best-effort 0600 on the given fd.

    os.fchmod is Unix-only (AttributeError on Windows, which enforces
    permissions via ACLs instead), so probe for it rather than calling
    it unconditionally — a missing attribute must not break enroll.
    """
    fchmod = getattr(os, "fchmod", None)
    if fchmod is None:
        return
    try:
        fchmod(fd, 0o600)
    except OSError:
        pass


def write_config(path: str, cfg: Config) -> None:
    """Persist atomically with mode 0600, like WriteConfigFile.

    Temp-file-then-rename so a crash mid-write cannot leave a truncated
    config; the 0600 mode is applied to the temp file so a config holding
    the client secret is never world-readable, even for an instant.
    """
    with cfg.mu():
        payload = yaml.safe_dump(
            {
                "listen": cfg.listen,
                "servers": cfg.servers,
                "token": cfg.token,
                "client_token": cfg.client_token,
                "client_name": cfg.client_name,
                "usage_endpoint": cfg.usage_endpoint,
                "usage_upload_sec": cfg.usage_upload_sec,
                "cache_enabled": cfg.cache_enabled,
                "cache_ttl_sec": cfg.cache_ttl_sec,
                "cache_max_items": cfg.cache_max_items,
                "usage_file": cfg.usage_file,
                "fallback_mode": cfg.fallback_mode,
                "max_inflight": cfg.max_inflight,
            },
            default_flow_style=False,
            sort_keys=False,
            allow_unicode=True,
        ).encode()

    directory = os.path.dirname(os.path.abspath(path))
    os.makedirs(directory, mode=0o700, exist_ok=True)

    fd, tmp = tempfile.mkstemp(prefix=".client-", suffix=".yml", dir=directory)
    try:
        _restrict_to_owner(fd)
        with os.fdopen(fd, "wb") as fh:
            fh.write(payload)
            fh.flush()
            os.fsync(fh.fileno())
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        finally:
            raise


def apply_bundle(cfg: Config, bundle: dict) -> list[str]:
    """Apply a verified config bundle. Returns human-readable change notes.

    Credentials are local-only: token, client_token, client_name, listen,
    usage_file, usage_endpoint and usage_upload_sec are never touched.
    """
    notes: list[str] = []
    with cfg.mu():
        servers = bundle.get("servers")
        if isinstance(servers, list) and servers:
            cfg.servers = [s for s in servers if isinstance(s, str) and s]
            if cfg.servers:
                notes.append("servers updated")

        cache = bundle.get("cache") or {}
        if isinstance(cache, dict):
            # Applied unconditionally, matching the Go client: a bundle
            # is authoritative about the flag even when it disables it.
            enabled = cache.get("enabled")
            if isinstance(enabled, bool):
                cfg.cache_enabled = enabled
            ttl = cache.get("ttl_sec")
            if isinstance(ttl, int) and not isinstance(ttl, bool) and ttl > 0:
                cfg.cache_ttl_sec = ttl
            max_items = cache.get("max_items")
            if isinstance(max_items, int) and not isinstance(max_items, bool) and max_items > 0:
                cfg.cache_max_items = max_items

        fallback = bundle.get("fallback") or {}
        if isinstance(fallback, dict):
            mode = fallback.get("mode")
            if isinstance(mode, str) and mode in _FALLBACK_MODES:
                cfg.fallback_mode = mode

        limits = bundle.get("limits") or {}
        if isinstance(limits, dict):
            max_inflight = limits.get("max_inflight")
            if (
                isinstance(max_inflight, int)
                and not isinstance(max_inflight, bool)
                and max_inflight > 0
            ):
                cfg.max_inflight = max_inflight

    return notes
