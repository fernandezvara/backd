"""The few places where the async client needs a different function from the sync one.
``scripts/unasync.py`` swaps this module for ``_sio`` when it writes ``_sync``."""

from __future__ import annotations

import asyncio
from collections.abc import Awaitable, Callable, Coroutine
from typing import Any, cast


async def sleep(seconds: float) -> None:
    await asyncio.sleep(seconds)


_tasks: set[asyncio.Task[None]] = set()


def spawn(fn: Callable[..., Awaitable[None]], *args: Any) -> None:
    """Runs ``fn(*args)`` in the background: a task here, a thread in the sync client."""
    task: asyncio.Task[None] = asyncio.get_running_loop().create_task(cast("Coroutine[Any, Any, None]", fn(*args)))
    _tasks.add(task)  # a task nothing refers to may be collected before it ends
    task.add_done_callback(_tasks.discard)
