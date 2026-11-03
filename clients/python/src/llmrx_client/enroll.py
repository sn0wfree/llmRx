"""One-time-code enrollment.

Mirrors cmd/gateway-client/enroll.go: POST /client/v1/enroll, require
exactly HTTP 201, store client_secret as the bearer token, default the
usage file next to the config.
"""

from __future__ import annotations

import json
import urllib.error
import urllib.request

ENROLL_TIMEOUT = 20.0
READ_CAP = 64 * 1024


class EnrollError(Exception):
    pass


def enroll(base_url: str, code: str, name: str = "") -> dict:
    """Exchange a one-time code for a client credential pair.

    Returns {"client_id", "client_secret", "name"}; the caller stores
    client_secret as client_token. Server-side, the code burns on first
    use and identical failures are indistinguishable (anti-enumeration),
    so errors are surfaced verbatim, not interpreted.
    """
    payload = json.dumps(
        {"enroll_token": code, "client_name": name},
        separators=(",", ":"),
    ).encode()
    req = urllib.request.Request(
        base_url.rstrip("/") + "/client/v1/enroll",
        data=payload,
        method="POST",
        headers={"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=ENROLL_TIMEOUT) as resp:
            raw = resp.read(READ_CAP + 1)
            status = resp.status
    except urllib.error.HTTPError as exc:
        try:
            raw = exc.read(READ_CAP + 1)
            status = exc.code
        finally:
            exc.close()  # else the connection leaks until GC
        try:
            doc = json.loads(raw)
            message = doc.get("error", {}).get("message", "")
            code = doc.get("error", {}).get("code", "")
        except (ValueError, AttributeError):
            raise EnrollError(f"enroll rejected: status {status}: {raw!r}") from exc
        if message or code:
            raise EnrollError(f"enroll rejected ({status} {code}): {message}") from exc
        raise EnrollError(f"enroll rejected: status {status}: {raw!r}") from exc
    except (urllib.error.URLError, OSError) as exc:
        raise EnrollError(f"enroll: {exc}") from exc

    if len(raw) > READ_CAP:
        raise EnrollError("enroll: response too large")
    if status != 201:
        raise EnrollError(f"enroll rejected: status {status}: {raw!r}")

    try:
        doc = json.loads(raw)
    except ValueError as exc:
        raise EnrollError(f"enroll: invalid response body: {exc}") from exc
    secret = doc.get("client_secret") or ""
    if not secret:
        raise EnrollError("enroll: server returned an empty secret")
    return {
        "client_id": doc.get("client_id") or "",
        "client_secret": secret,
        "name": doc.get("name") or "",
    }
