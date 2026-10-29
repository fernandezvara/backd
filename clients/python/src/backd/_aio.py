"""The few places where the async client needs a different function from the sync one.
``scripts/unasync.py`` swaps this module for ``_sio`` when it writes ``_sync``."""

from __future__ import annotations

import asyncio


async def sleep(seconds: float) -> None:
    await asyncio.sleep(seconds)
