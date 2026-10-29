"""The sync counterpart of ``_aio``."""

from __future__ import annotations

import threading
import time
from collections.abc import Callable
from typing import Any, TypeVar

T = TypeVar("T")


def sleep(seconds: float) -> None:
    time.sleep(seconds)


def spawn(fn: Callable[..., None], *args: Any) -> None:
    """Runs ``fn(*args)`` in the background: a thread here, a task in the async client."""
    threading.Thread(target=fn, args=args, daemon=True).start()


def run_blocking(fn: Callable[..., T], *args: Any) -> T:
    return fn(*args)
