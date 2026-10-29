"""Sizes such as ``128MB`` or ``1.5MiB``: the one grammar of the server and the other clients
(``clients/testdata/size-vectors.json`` is run by all of them)."""

from __future__ import annotations

import math
import re
from typing import Literal

__all__ = ["SizeFamily", "format_bytes", "parse_size"]

#: Which units a size takes: ``decimal`` (B, KB..TB, powers of 1000), ``binary`` (B, KiB..TiB,
#: powers of 1024) or ``both`` (the default).
SizeFamily = Literal["both", "decimal", "binary"]

_UNITS = {
    "B": 1,
    "KB": 10**3,
    "MB": 10**6,
    "GB": 10**9,
    "TB": 10**12,
    "KiB": 1 << 10,
    "MiB": 1 << 20,
    "GiB": 1 << 30,
    "TiB": 1 << 40,
}
_NAMES = {
    "both": "B, KB, MB, GB, TB, KiB, MiB, GiB, TiB",
    "decimal": "B, KB, MB, GB, TB",
    "binary": "B, KiB, MiB, GiB, TiB",
}
_PATTERN = re.compile(r"([0-9]+)(?:\.([0-9]+))?(B|KB|MB|GB|TB|KiB|MiB|GiB|TiB)?")
_MAX_INPUT = 64
_MAX_SAFE = (1 << 63) - 1


def parse_size(value: str, *, family: SizeFamily = "both", allow_bytes: bool = False) -> int:
    """The bytes a size stands for.

    A unit follows the number directly and is case-sensitive; a decimal is fine when the result is
    a whole number of bytes (``0.1KiB`` is not). ``family`` limits the units; ``allow_bytes`` also
    accepts a bare whole number of bytes (``"1048576"``). Raises :class:`ValueError` naming what
    is accepted.
    """
    s = str(value).strip()
    m = _PATTERN.fullmatch(s) if len(s) <= _MAX_INPUT else None
    if m is None or (m.group(3) is None and (not allow_bytes or m.group(2) is not None)):
        example = "128MB" if family == "decimal" else "128MiB"
        lead = "a whole number of bytes or " if allow_bytes else ""
        raise ValueError(
            f'invalid size "{value}": want {lead}a number and a unit ({_NAMES[family]}), such as {example}'
        )
    unit = m.group(3) or "B"
    binary = unit.endswith("iB")
    if (family == "decimal" and binary) or (family == "binary" and not binary and unit != "B"):
        raise ValueError(f'invalid size "{value}": {unit} is not accepted here, use one of {_NAMES[family]}')
    frac = m.group(2) or ""
    total = int(m.group(1) + frac) * _UNITS[unit]
    scale: int = 10 ** len(frac)
    if total % scale:
        raise ValueError(f'invalid size "{value}": not a whole number of bytes')
    n = total // scale
    if n > _MAX_SAFE:
        raise ValueError(f'invalid size "{value}": too large')
    return n


def format_bytes(n: int, family: SizeFamily = "both") -> str:
    """Bytes for people: the largest unit whose value is at least 1, up to TB or TiB, with at most
    two decimals and no trailing zeros (``1.5 KiB``, ``5 MiB``). ``both`` prints binary. It is for
    display and can lose digits."""
    decimal = family == "decimal"
    base = 1000 if decimal else 1024
    units = ["B", "KB", "MB", "GB", "TB"] if decimal else ["B", "KiB", "MiB", "GiB", "TiB"]
    if n < base:
        return f"{n} B"
    v, i = float(n), 0
    while i < len(units) - 1:
        if _round2(v) < base:
            break
        v /= base
        i += 1
    return f"{_round2(v):.2f}".rstrip("0").rstrip(".") + " " + units[i]


def _round2(v: float) -> float:
    return math.floor(v * 100 + 0.5) / 100
