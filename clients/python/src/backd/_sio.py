"""The sync counterpart of ``_aio``."""

from __future__ import annotations

import time


def sleep(seconds: float) -> None:
    time.sleep(seconds)
