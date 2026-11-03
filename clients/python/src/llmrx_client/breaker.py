"""Circuit breaker: open after N consecutive failures, cooldown, half-open.

Mirrors internal/client/client.go's breaker (threshold 3, cooldown 30s,
hard-coded in NewProxy). One deliberate divergence (docs/P18 §5.2): the
Python client counts at most one failure per request, so the breaker
opens after 3 consecutive failures rather than the Go client's effective
2 for buffered 5xx responses.
"""

from __future__ import annotations

import threading
import time


class Breaker:
    def __init__(self, threshold: int = 3, cooldown_sec: float = 30.0) -> None:
        self._threshold = threshold
        self._cooldown = cooldown_sec
        self._mu = threading.Lock()
        self._fails = 0
        self._open = False
        self._opened_at = 0.0

    def allow(self, now: float | None = None) -> bool:
        """Whether a dial may proceed. Does not mutate state: while open
        and un-cooled this is False; once cooled this is True (the probe),
        and a failed probe re-opens immediately because fails is already
        at threshold."""
        now = time.time() if now is None else now
        with self._mu:
            if not self._open:
                return True
            return now - self._opened_at >= self._cooldown

    def success(self) -> None:
        with self._mu:
            self._open = False
            self._fails = 0

    def failure(self) -> None:
        with self._mu:
            self._fails += 1
            if self._fails >= self._threshold:
                self._open = True
                self._opened_at = time.time()

    def state(self) -> tuple[bool, int]:
        with self._mu:
            return self._open, self._fails
