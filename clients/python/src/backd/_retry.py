"""Retry policy shared by the sync and the async client."""

from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True)
class Retry:
    """How many times a call is repeated when the server answers 429 or 503.

    Only safe calls are repeated: reads, and writes that carry ``If-Match`` or an
    ``Idempotency-Key``. ``attempts`` are the extra tries after the first (0 turns retries off);
    ``max_delay`` is the longest wait between tries, in seconds.
    """

    attempts: int = 0
    max_delay: float = 30.0


def backoff(attempt: int, retry_after: float | None, max_delay: float) -> float:
    """The wait before the next try: what the server asked, else exponential from 0.5 s, up to ``max_delay``."""
    return min(retry_after if retry_after is not None else 0.5 * 2**attempt, max_delay)
