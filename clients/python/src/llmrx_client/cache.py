"""Response cache for the edge client: LRU + per-put TTL.

Mirrors internal/client/client.go's Cache wrapper over the server's
memory cache. Entries are raw response bodies plus the usage block, so a
cache hit can serve the exact bytes the upstream produced *and* keep the
usage accounting truthful.

The key is this client's own canonicalisation — the cache is purely
local, so keys need not be byte-identical to the server's (or the Go
client's); they only need to be stable within this process. The
projection follows the same shape as internal/cache/cache.go: identity
fields only, tools and tool calls sorted, reasoning_content excluded.
"""

from __future__ import annotations

import hashlib
import json
import threading
import time
from collections import OrderedDict
from dataclasses import dataclass, field
from typing import Any

# Messages are reduced to identity fields; anything else (system time,
# request ids, reasoning traces) must not affect cacheability.
_MESSAGE_FIELDS = ("role", "content", "name", "tool_call_id", "cache_control", "refusal")


class TemperaturePositive(ValueError):
    """Mirrors cache.ErrTemperaturePositive."""


def canonical_key(req: dict) -> str:
    """Stable cache key for a chat request dict.

    Raises TemperaturePositive when temperature is set above zero — such
    requests must never be served from cache.
    """
    temperature = req.get("temperature")
    if isinstance(temperature, (int, float)) and not isinstance(temperature, bool):
        if temperature > 0:
            raise TemperaturePositive("temperature > 0")

    def norm_message(m: Any) -> Any:
        if not isinstance(m, dict):
            return m
        out = {k: m[k] for k in _MESSAGE_FIELDS if k in m}
        tool_calls = m.get("tool_calls")
        if isinstance(tool_calls, list):
            out["tool_calls"] = sorted(
                (
                    {
                        "id": tc.get("id"),
                        "function": {
                            "name": (tc.get("function") or {}).get("name"),
                            "arguments": (tc.get("function") or {}).get("arguments"),
                        },
                    }
                    for tc in tool_calls
                    if isinstance(tc, dict)
                ),
                key=lambda tc: (tc.get("id") or "", (tc["function"] or {}).get("name") or ""),
            )
        return out

    def norm_tools(tools: Any) -> Any:
        if not isinstance(tools, list):
            return []
        return sorted(
            (t for t in tools if isinstance(t, dict)),
            key=lambda t: json.dumps(t.get("function", {}).get("name") or "", sort_keys=True),
        )

    parts: list[Any] = [
        req.get("model") or "",
        [norm_message(m) for m in req.get("messages") or []],
        norm_tools(req.get("tools")),
    ]
    if req.get("response_format") is not None:
        parts.append(req.get("response_format"))
    stream_opts = req.get("stream_options") or {}
    include_usage = isinstance(stream_opts, dict) and bool(stream_opts.get("include_usage"))
    parts.append(include_usage)
    parts.append("stream" if req.get("stream") else "non-stream")

    blob = "\x00".join(
        json.dumps(p, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
        for p in parts
    )
    return hashlib.sha256(blob.encode()).hexdigest()


@dataclass
class Entry:
    status_code: int
    body: bytes
    stored_at: float = field(default_factory=time.time)
    expires_at: float = 0.0  # 0 = never
    usage: dict | None = None


class MemoryCache:
    """LRU with lazy TTL expiry and hit/miss statistics."""

    def __init__(self, max_items: int) -> None:
        self._max = max_items if max_items > 0 else DEFAULT_MAX_ITEMS
        self._items: OrderedDict[str, Entry] = OrderedDict()
        self._hits = 0
        self._misses = 0
        self._mu = threading.Lock()

    def get(self, key: str) -> Entry | None:
        now = time.time()
        with self._mu:
            entry = self._items.get(key)
            if entry is None:
                self._misses += 1
                return None
            if entry.expires_at and now >= entry.expires_at:
                del self._items[key]
                self._misses += 1
                return None
            self._items.move_to_end(key)
            self._hits += 1
            return entry

    def put(self, key: str, entry: Entry, ttl_sec: float) -> None:
        if ttl_sec and ttl_sec > 0:
            entry.expires_at = time.time() + ttl_sec
        with self._mu:
            # Evict before inserting so the cache actually holds max items
            # (front-push, drop from the LRU back while at capacity).
            self._items.pop(key, None)
            while len(self._items) >= self._max:
                self._items.popitem(last=False)
            self._items[key] = entry

    def purge(self) -> None:
        with self._mu:
            self._items.clear()

    def stats(self) -> dict:
        with self._mu:
            size = len(self._items)
            hits, misses = self._hits, self._misses
        total = hits + misses
        rate = (hits / total * 100) if total else 0.0
        return {
            "size": size,
            "hits": hits,
            "misses": misses,
            "hit_rate": round(rate, 1),
        }


DEFAULT_MAX_ITEMS = 2000
