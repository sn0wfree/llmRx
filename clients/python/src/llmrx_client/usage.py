"""Usage accounting: records, buffer, JSONL persistence, batched upload.

Mirrors internal/client/client.go's UsageRecord/usageBuffer and
internal/client/usage.go. The JSON field set here is the server's ingest
schema (internal/clientapi/ingest.go edgeUsageRecord): request_id is the
server-side idempotency key, ts is unix seconds, cost_usd is a number.
"""

from __future__ import annotations

import json
import os
import secrets
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
    """Field order matches the Go struct's marshal order.

    `persisted` is bookkeeping for the persist-vs-upload race (see
    UsageBuffer) and is never serialized.
    """

    request_id: str
    ts: int
    model: str = ""
    status: int = 0
    prompt_tokens: int = 0
    completion_tokens: int = 0
    cost_usd: float = 0
    source: str = SOURCE_PROXY
    latency_ms: int = 0
    persisted: bool = field(default=False, repr=False, compare=False)

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
    """In-memory queue; drops the oldest when full (never blocks).

    request_id must be unique across RESTARTS: the server deduplicates on
    client_name + request_id for 24h, so a counter that restarts at 1
    would make the server silently discard every row after a restart.
    The per-process nonce (start time + pid) keeps ids unique.
    """

    def __init__(self, max_items: int = 5000) -> None:
        self._max = max_items
        self._items: list[UsageRecord] = []
        self._mu = threading.Lock()
        self._seq = 0
        # start time + pid + randomness: two buffers created inside the
        # same second in the same process must still differ.
        self._nonce = f"{int(time.time())}{os.getpid()}{secrets.token_hex(4)}"

    def next_request_id(self, client_name: str) -> str:
        with self._mu:
            self._seq += 1
            return f"{client_name}-{self._nonce}-{self._seq}"

    def add(self, rec: UsageRecord) -> None:
        with self._mu:
            self._items.append(rec)
            if len(self._items) > self._max:
                del self._items[0 : len(self._items) - self._max]

    def unpersisted(self) -> list[UsageRecord]:
        """Rows not yet written to the JSONL store (does not remove).

        The recorder must NOT drain the buffer: the 5s persist tick and
        the 60s upload tick share it, and a persist-side take_all would
        starve the uploader — 82% of a 4h soak's rows were persisted
        locally and never uploaded because of exactly that race.
        """
        with self._mu:
            return [r for r in self._items if not r.persisted]

    def mark_persisted(self, rows: list[UsageRecord]) -> None:
        with self._mu:
            for r in rows:
                r.persisted = True

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
        # 0600 where the platform supports it (os.fchmod is Unix-only;
        # Windows enforces via ACLs). A missing fchmod must not kill the
        # client on platforms that cannot honour it.
        fchmod = getattr(os, "fchmod", None)
        if fchmod is not None:
            try:
                fchmod(self._fh.fileno(), 0o600)
            except OSError:
                pass
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
    """Persist rows that have not been written yet, without removing
    them from the buffer — the uploader owns removal."""
    if store is None:
        return
    rows = buffer.unpersisted()
    if not rows:
        return
    try:
        store.append(rows)
    except OSError:
        return  # leave unmarked; retried on the next tick
    buffer.mark_persisted(rows)


def start_uploader(buffer: UsageBuffer, usage_url_fn, client_token: str,
                   client_name: str, interval_sec: int,
                   stop: threading.Event,
                   store: JSONLUsageStore | None = None) -> threading.Thread:
    """Upload batches every interval_sec. Like the Go client, the first
    attempt happens after a full interval — no immediate flush — so a
    client that starts and stops quickly persists locally instead of
    hammering the server with an empty batch."""

    def run() -> None:
        while not stop.wait(max(interval_sec, 1)):
            _drain_to_server(buffer, usage_url_fn, client_token, client_name,
                             store)

    t = threading.Thread(target=run, name="usage-uploader", daemon=True)
    t.start()
    return t


def _drain_to_server(buffer: UsageBuffer, usage_url_fn, client_token: str,
                     client_name: str, store: JSONLUsageStore | None = None) -> None:
    url = usage_url_fn()
    if not url or not client_token:
        return
    batch = buffer.take(UPLOAD_BATCH)
    if not batch:
        return
    try:
        upload_batch(url, client_token, client_name, batch)
    except urllib.error.HTTPError as exc:
        exc.close()
        buffer.requeue_front(batch)
        return
    except (urllib.error.URLError, OSError, RuntimeError):
        buffer.requeue_front(batch)
        return
    # Uploaded rows that the 5s recorder has not written yet must land in
    # the JSONL too, or they exist nowhere once removed from the buffer.
    if store is not None:
        fresh = [r for r in batch if not r.persisted]
        if fresh:
            try:
                store.append(fresh)
            except OSError:
                pass
            buffer.mark_persisted(fresh)
