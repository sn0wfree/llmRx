"""Control plane: heartbeat, signed config bundle, downgrade rejection.

Mirrors internal/client/control.go. The critical detail is signature
verification: the Go server signs the exact `json.Marshal` bytes of the
bundle (struct field order, omitempty, default HTML escaping), so the
Python client must verify against the RAW bytes of the `config` member
from the response — re-serializing a parsed dict cannot reproduce those
bytes. Verification failure and version downgrades both keep the
last-good config in force.
"""

from __future__ import annotations

import hashlib
import hmac
import json
import threading
import time
import urllib.error
import urllib.request

HEARTBEAT_READ_CAP = 64 * 1024
CONFIG_READ_CAP = 1024 * 1024
REQUEST_TIMEOUT = 10.0
DEFAULT_INTERVAL = 30.0
ALGORITHM = "hmac-sha256"


class VerifyError(Exception):
    """Signature, algorithm or downgrade rejection — config not applied."""


def extract_raw_member(body: bytes, key: str) -> tuple[bytes, dict]:
    """Return the raw bytes of a top-level object member plus its parsed
    value. The server emits the outer object in struct order with
    `config` first, so the member starts at a known offset; anything else
    is treated as an unexpected envelope and rejected by the caller.
    """
    prefix = b'{"' + key.encode() + b'":'
    if not body.startswith(prefix):
        raise VerifyError(f"unexpected bundle envelope (wanted {key} first)")
    start = len(prefix)
    text = body[start:].decode("utf-8", "strict")
    obj, end = json.JSONDecoder().raw_decode(text)
    return body[start:start + end], obj


def verify_bundle(body: bytes, client_token: str) -> dict:
    """Verify the signed envelope and return the bundle dict.

    Raises VerifyError on algorithm mismatch, signature mismatch or a
    non-object config. Comparison is constant-time.
    """
    raw_config, config = extract_raw_member(body, "config")

    envelope = json.loads(body.decode("utf-8", "strict"))
    if not isinstance(config, dict):
        raise VerifyError("bundle config is not an object")
    algorithm = envelope.get("algorithm")
    if algorithm != ALGORITHM:
        raise VerifyError(f"config: unknown algorithm {algorithm!r}")
    signature = envelope.get("signature")
    if not isinstance(signature, str):
        raise VerifyError("config: missing signature")

    expected = hmac.new(
        client_token.encode(), raw_config, hashlib.sha256
    ).hexdigest()
    if not hmac.compare_digest(expected, signature):
        raise VerifyError("config: signature mismatch")
    return config


class Control:
    """Polls heartbeat + config against a server URL fixed at
    construction (bundle server updates do not retarget the poller —
    same as the Go client)."""

    def __init__(self, base_url: str, client_name: str, client_token: str,
                 version_fn, on_bundle=None) -> None:
        self.base = base_url.rstrip("/")
        self.client_name = client_name
        self.client_token = client_token
        self.version_fn = version_fn
        self.on_bundle = on_bundle  # callable(bundle_dict, notes) -> None
        self._applied = 0
        self._last_err = ""
        self._last_ok = ""
        self._mu = threading.Lock()

    # -- status -------------------------------------------------------------

    def applied_version(self) -> int:
        with self._mu:
            return self._applied

    def last_error(self) -> str:
        with self._mu:
            return self._last_err

    def last_ok(self) -> str:
        with self._mu:
            return self._last_ok

    def _set_err(self, err: str) -> None:
        with self._mu:
            self._last_err = err

    def _set_ok(self) -> None:
        now = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        with self._mu:
            self._last_err = ""
            self._last_ok = now

    # -- wire -----------------------------------------------------------------

    def _headers(self) -> dict:
        return {
            "Authorization": f"Bearer {self.client_token}",
            "X-Llmrx-Client": self.client_name,
            "Content-Type": "application/json",
        }

    def heartbeat(self) -> tuple[int, bool]:
        payload = json.dumps(
            {"client_version": self.version_fn(),
             "config_version": self.applied_version()},
            separators=(",", ":"),
        ).encode()
        req = urllib.request.Request(
            f"{self.base}/client/v1/heartbeat", data=payload, method="POST",
            headers=self._headers())
        with urllib.request.urlopen(req, timeout=REQUEST_TIMEOUT) as resp:
            raw = resp.read(HEARTBEAT_READ_CAP + 1)
        if len(raw) > HEARTBEAT_READ_CAP:
            raise RuntimeError("heartbeat: response too large")
        if not raw:
            raise RuntimeError("heartbeat: status 200 with empty body")
        doc = json.loads(raw)
        return int(doc.get("server_version") or 0), bool(doc.get("need_config"))

    def fetch_config(self) -> dict:
        req = urllib.request.Request(
            f"{self.base}/client/v1/config", method="GET",
            headers=self._headers())
        with urllib.request.urlopen(req, timeout=REQUEST_TIMEOUT) as resp:
            raw = resp.read(CONFIG_READ_CAP + 1)
        if len(raw) > CONFIG_READ_CAP:
            raise RuntimeError("config: response too large")
        return verify_bundle(raw, self.client_token)

    def round_once(self) -> None:
        """One heartbeat + (if needed) fetch/verify/apply round. Failures
        are recorded in last_error, then re-raised for the run loop."""
        try:
            self._round()
        except urllib.error.HTTPError as exc:
            self._set_err(f"heartbeat: status {exc.code}")
            raise
        except VerifyError as exc:
            self._set_err(str(exc))
            raise
        except (urllib.error.URLError, OSError, RuntimeError) as exc:
            reason = getattr(exc, "reason", None) or exc
            self._set_err(f"control round: {reason}")
            raise

    def _round(self) -> None:
        _, need = self.heartbeat()
        if not need:
            self._set_ok()
            return
        bundle = self.fetch_config()
        version = int(bundle.get("version") or 0)
        applied = self.applied_version()
        if version < applied:
            err = f"config: refusing downgrade from {applied} to {version}"
            self._set_err(err)
            raise VerifyError(err)
        # Applied version lands BEFORE the callback fires, mirroring the
        # Go client's ordering.
        with self._mu:
            self._applied = version
        notes: list[str] = []
        if self.on_bundle is not None:
            notes = self.on_bundle(bundle) or []
        line = f"config bundle v{version} applied"
        if bundle.get("servers"):
            line += f" (servers: {bundle['servers']})"
        print(line)
        for note in notes:
            print(f"  {note}")

    def run(self, stop: threading.Event, interval: float = DEFAULT_INTERVAL) -> None:
        """Immediate first round, then one round per interval."""
        while True:
            try:
                self.round_once()
                self._set_ok()
            except VerifyError as exc:
                self._set_err(str(exc))
            except (urllib.error.URLError, OSError, RuntimeError,
                    ValueError, KeyError) as exc:
                reason = getattr(exc, "reason", None) or exc
                self._set_err(f"control round: {reason}")
            if stop.wait(max(interval, 1)):
                return
