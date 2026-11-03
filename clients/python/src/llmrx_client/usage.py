"""Usage accounting: records, buffer, JSONL persistence, batched upload.

Mirrors internal/client/client.go's UsageRecord/usageBuffer and
internal/client/usage.go. The JSON field set here is the server's ingest
schema (internal/clientapi/ingest.go edgeUsageRecord): request_id is the
server-side idempotency key, ts is unix seconds, cost_usd is a number.
"""

from __future__ import annotations

import json
import os
import threading
import time
import urllib.error
import urllib.request
from dataclasses import dataclass, field

SOURCE_PROXY = "proxy"
SOURCE_CACHE = "cache"
SOURCE_LOCAL = "local"
VALID_SOURCES = frozenset({SOURCE_PROXY, SOURCE_CACHE, SOURCE_LOCAL})

UPLOAD_BATCH = 200
UPLOAD_TIMEOUT = 10.0
PERSIST_TICK = 5.0


def valid_source(s: str) -> bool:
    return s in VALID_SOURCES


@dataclass
class UsageRecord:
    """Field order matches the Go struct's marshal order."""

    request_id: str
    ts: int
    model: str = ""
    status: int = 0
    prompt_tokens: int = 0
    completion_tokens: int = 0
    cost_usd: float = 0
    source: str = SOURCE_PROXY
    latency_ms: int = 0

    def to_json(self) -> dict:
        return {
            "request_id": self.request_id,
            "ts": self.ts,
            "model": self.model,
            "status": self.status,
            "prompt_tokens": self.prompt_tokens,
            "completion_tokens": self.completion_tokens,
            "cost_usd": self.cost_usd,
            "source": self.source,
            "latency_ms": self.latency_ms,
        }


class UsageBuffer:
    """In-memory queue; drops the oldest when full (never blocks)."""

    def __init__(self, max_items: int = 5000) -> None:
        self._max = max_items
        self._items: list[UsageRecord] = []
        self._mu = threading.Lock()
        self._seq = 0

    def next_request_id(self, client_name: str) -> str:
        with self._mu:
            self._seq += 1
            return f"{client_name}-{self._seq}"

    def add(self, rec: UsageRecord) -> None:
        with self._mu:
            self._items.append(rec)
            if len(self._items) > self._max:
                del self._items[0 : len(self._items) - self._max]

    def take(self, n: int) -> list[UsageRecord]:
        with self._mu:
            out, self._items = self._items[:n], self._items[n:]
        return out

    def take_all(self) -> list[UsageRecord]:
        return self.take(len(self._items))

    def requeue_front(self, batch: list[UsageRecord]) -> None:
        """Put a failed batch back, preserving order, at the front."""
        if not batch:
            return
        with self._mu:
            self._items[:0] = batch
            if len(self._items) > self._max:
                del self._items[self._max:]

    def __len__(self) -> int:
        with self._mu:
            return len(self._items)


class JSONLUsageStore:
    """Append-only JSONL sink; one record per line, single write per batch."""

    def __init__(self, path: str) -> None:
        directory = os.path.dirname(os.path.abspath(path))
        os.makedirs(directory, mode=0o755, exist_ok=True)
        self._fh = open(path, "ab", buffering=0)  # noqa: SIM115 — owned for process lifetime
        try:
            os.fchmod(self._fh.fileno(), 0o600)
        except OSError:
            pass  # best-effort; some filesystems refuse fchmod
        self._mu = threading.Lock()

    def append(self, records: list[UsageRecord]) -> None:
        if not records:
            return
        lines = "".join(
            json.dumps(r.to_json(), separators=(",", ":")) + "\n" for r in records
        ).encode()
        with self._mu:
            self._fh.write(lines)

    def close(self) -> None:
        with self._mu:
            self._fh.close()


def upload_batch(
    usage_url: str,
    client_token: str,
    client_name: str,
    records: list[UsageRecord],
) -> None:
    """POST one batch. Raises on transport error or status >= 300 so the
    caller requeues; mirrors DrainUsage's success definition (< 300)."""
    payload = json.dumps(
        {
            "client_name": client_name,
            "records": [r.to_json() for r in records],
        },
        separators=(",", ":"),
    ).encode()
    req = urllib.request.Request(
        usage_url,
        data=payload,
        method="POST",
        headers={
            "Authorization": f"Bearer {client_token}",
            "Content-Type": "application/json",
        },
    )
    with urllib.request.urlopen(req, timeout=UPLOAD_TIMEOUT) as resp:
        if resp.status >= 300:
            raise RuntimeError(f"usage upload: status {resp.status}")


def start_recorder(buffer: UsageBuffer, store: JSONLUsageStore | None,
                   stop: threading.Event) -> threading.Thread:
    """Persist the buffer to JSONL every 5s; flush on stop."""

    def run() -> None:
        while not stop.wait(PERSIST_TICK):
            _drain_to_store(buffer, store)
        _drain_to_store(buffer, store)

    t = threading.Thread(target=run, name="usage-recorder", daemon=True)
    t.start()
    return t


def _drain_to_store(buffer: UsageBuffer, store: JSONLUsageStore | None) -> None:
    if store is None:
        return
    batch = buffer.take_all()
    if not batch:
        return
    try:
        store.append(batch)
    except OSError:
        buffer.requeue_front(batch)


def start_uploader(buffer: UsageBuffer, usage_url_fn, client_token: str,
                   client_name: str, interval_sec: int,
                   stop: threading.Event) -> threading.Thread:
    """Upload batches every interval_sec. Like the Go client, the first
    attempt happens after a full interval — no immediate flush — so a
    client that starts and stops quickly persists locally instead of
    hammering the server with an empty batch."""

    def run() -> None:
        while not stop.wait(max(interval_sec, 1)):
            _drain_to_server(buffer, usage_url_fn, client_token, client_name)

    t = threading.Thread(target=run, name="usage-uploader", daemon=True)
    t.start()
    return t


def _drain_to_server(buffer: UsageBuffer, usage_url_fn, client_token: str,
                     client_name: str) -> None:
    url = usage_url_fn()
    if not url or not client_token:
        return
    batch = buffer.take(UPLOAD_BATCH)
    if not batch:
        return
    try:
        upload_batch(url, client_token, client_name, batch)
    except (urllib.error.URLError, OSError, RuntimeError):
        buffer.requeue_front(batch)
