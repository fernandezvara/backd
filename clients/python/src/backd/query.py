"""The query language: helpers for ``where``, the ``expand`` builder and ``fill_buckets``.

A ``where`` is a plain dict (or the JSON text of one): field names to a value (equality) or to the
operators these helpers build; ``$or`` and ``$and`` take lists of filters. The client only
serializes it; the server checks it against the collection.

    where={"status": "active", "price": between(10, 20)}
"""

from __future__ import annotations

import json
import re
from collections.abc import Mapping, Sequence
from datetime import date, datetime, timedelta, timezone
from typing import Any, Literal

__all__ = [
    "Where",
    "and_",
    "between",
    "bucket_key",
    "contains",
    "ends_with",
    "eq",
    "expand",
    "fill_buckets",
    "gt",
    "gte",
    "icontains",
    "iends_with",
    "ilike",
    "in_",
    "is_null",
    "istarts_with",
    "like",
    "lt",
    "lte",
    "ne",
    "nin",
    "not_null",
    "or_",
    "search",
    "starts_with",
]

#: A filter in backd's query language.
Where = Mapping[str, Any]


def eq(v: Any) -> dict[str, Any]:
    return {"$eq": v}


def ne(v: Any) -> dict[str, Any]:
    return {"$ne": v}


def gt(v: Any) -> dict[str, Any]:
    return {"$gt": v}


def gte(v: Any) -> dict[str, Any]:
    return {"$gte": v}


def lt(v: Any) -> dict[str, Any]:
    return {"$lt": v}


def lte(v: Any) -> dict[str, Any]:
    return {"$lte": v}


def in_(*values: Any) -> dict[str, Any]:
    return {"$in": list(values)}


def nin(*values: Any) -> dict[str, Any]:
    return {"$nin": list(values)}


def between(lo: Any, hi: Any) -> dict[str, Any]:
    """Values from ``lo`` to ``hi``, both included."""
    return {"$between": [lo, hi]}


def is_null() -> dict[str, Any]:
    return {"$isNull": True}


def not_null() -> dict[str, Any]:
    return {"$notNull": True}


def like(s: str) -> dict[str, Any]:
    return {"$like": s}


def ilike(s: str) -> dict[str, Any]:
    return {"$ilike": s}


def starts_with(s: str) -> dict[str, Any]:
    return {"$startsWith": s}


def istarts_with(s: str) -> dict[str, Any]:
    return {"$istartsWith": s}


def ends_with(s: str) -> dict[str, Any]:
    return {"$endsWith": s}


def iends_with(s: str) -> dict[str, Any]:
    return {"$iendsWith": s}


def contains(s: str) -> dict[str, Any]:
    return {"$contains": s}


def icontains(s: str) -> dict[str, Any]:
    return {"$icontains": s}


def or_(*filters: Where) -> dict[str, Any]:
    """Documents that match any of the filters."""
    return {"$or": list(filters)}


def and_(*filters: Where) -> dict[str, Any]:
    """Documents that match all of the filters."""
    return {"$and": list(filters)}


def search(text: str) -> dict[str, Any]:
    """A full-text ``$search`` for a ``where``: words, ``"quoted phrases"`` and ``-excluded`` words.
    ``Collection.search`` is the same with the best match first."""
    return {"$search": text}


def where_json(w: Where | str | None) -> str | None:
    """The ``where`` parameter: the text as it is, or the JSON encoding of the filter."""
    if w is None:
        return None
    if isinstance(w, str):
        return w
    return json.dumps(w, separators=(",", ":"), ensure_ascii=False, default=_default)


def _default(o: Any) -> Any:
    """Dates are sent the way the API writes them (RFC 3339)."""
    if isinstance(o, datetime):
        return o.isoformat().replace("+00:00", "Z")
    if isinstance(o, date):
        return o.isoformat()
    raise TypeError(f"the where can't be encoded: {type(o).__name__}")


def csv(v: str | Sequence[str] | None) -> str | None:
    """A list parameter (``order_by``, ``group_by``, ``metrics``) as the comma-separated text the API takes."""
    if v is None or isinstance(v, str):
        return v
    return ",".join(v)


def if_match_value(v: int | str) -> str:
    """``3`` → ``"3"``; strings (``*``, ``"3"``, ``"2", "3"``) are sent as given."""
    return f'"{v}"' if isinstance(v, int) else v


_NAME = re.compile(r"[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*")


def expand(*parts: str | tuple[str, Sequence[str]]) -> str:
    """Builds the ``expand`` option of a read: the references to return next to the result, in
    ``_expanded``, with the fields to return of each. Nothing is expanded unless you pass this (or a
    string) as ``expand``; the client never adds it by itself.

        expand(("author", ["name", "avatar"]), "tags")   # "author(name,avatar),tags"
    """
    out: list[str] = []
    for part in parts:
        name, fields = (part, None) if isinstance(part, str) else part
        if not _NAME.fullmatch(name):
            raise TypeError(f"backd: {json.dumps(name)} is not a reference name to expand")
        if fields is None:
            out.append(name)
            continue
        if isinstance(fields, str) or not fields or not all(_NAME.fullmatch(f) for f in fields):
            raise TypeError(f"backd: the fields of {name} must be a non-empty list of field names")
        out.append(f"{name}({','.join(fields)})")
    return ",".join(out)


# -- aggregation buckets ---------------------------------------------------------------------

BucketUnit = Literal["day", "week", "month", "year"]

_KEY = {
    "day": re.compile(r"\d{4}-\d{2}-\d{2}"),
    "week": re.compile(r"\d{4}-W\d{2}"),
    "month": re.compile(r"\d{4}-\d{2}"),
    "year": re.compile(r"\d{4}"),
}


def bucket_key(d: date | datetime, unit: BucketUnit) -> str:
    """The bucket key of a UTC date, in the text form the server uses."""
    if isinstance(d, datetime):
        d = d.astimezone(timezone.utc).date() if d.tzinfo else d.date()
    if unit == "day":
        return f"{d.year:04d}-{d.month:02d}-{d.day:02d}"
    if unit == "week":
        iso = d.isocalendar()
        return f"{iso.year:04d}-W{iso.week:02d}"
    if unit == "month":
        return f"{d.year:04d}-{d.month:02d}"
    if unit == "year":
        return f"{d.year:04d}"
    raise TypeError(f"backd: unknown bucket unit {json.dumps(unit)}")


def _key_start(key: str, unit: BucketUnit) -> date:
    if unit == "day":
        return date.fromisoformat(key)
    if unit == "month":
        return date.fromisoformat(key + "-01")
    if unit == "year":
        return date(int(key), 1, 1)
    year, week = key.split("-W")
    return date.fromisocalendar(int(year), int(week), 1)


def _next(unit: BucketUnit, d: date) -> date:
    if unit == "day":
        return d + timedelta(days=1)
    if unit == "week":
        return d + timedelta(days=7)
    if unit == "month":
        return date(d.year + d.month // 12, d.month % 12 + 1, 1)
    return date(d.year + 1, 1, 1)


def _to_key(v: str | date | datetime, unit: BucketUnit, what: str) -> str:
    if isinstance(v, str) and _KEY[unit].fullmatch(v):
        return v
    if isinstance(v, str):
        try:
            v = datetime.fromisoformat(v.replace("Z", "+00:00"))
        except ValueError:
            raise TypeError(
                f"backd: {what} is not a date or a {unit} key like {bucket_key(date(1970, 1, 1), unit)}"
            ) from None
    return bucket_key(v, unit)


def fill_buckets(
    groups: Sequence[Mapping[str, Any]],
    *,
    field: str,
    unit: BucketUnit,
    from_: str | date | datetime,
    to: str | date | datetime,
    empty: Mapping[str, Any] | None = None,
) -> list[dict[str, Any]]:
    """Fills the buckets an aggregation left out, so a chart has a point for every day, week, month or year
    between ``from_`` and ``to`` (inclusive). The server doesn't return empty buckets; this adds them to groups
    grouped by one date field and nothing else (``group_by="paid_at:month"``). A bucket with no group gets
    ``count: 0`` and the values of ``empty`` (for sums, ``{"sum_total": "0"}``).

    ``from_`` and ``to`` are bucket keys in the zone of the aggregation (``"2026-01"``), or dates, which name
    the bucket they fall in as seen in UTC; the groups are returned in key order. A ``None`` group (documents
    with no date) is kept at the front."""
    if unit not in _KEY:
        raise TypeError(f"backd: unit must be day, week, month or year, got {json.dumps(unit)}")
    name = f"{field}:{unit}"
    for g in groups:
        keys = list(g["key"])
        if keys != [name]:
            raise TypeError(f"backd: fill_buckets needs groups grouped only by {name}, got {json.dumps(keys)}")
    start, end = _to_key(from_, unit, "from_"), _to_key(to, unit, "to")
    if start > end:
        raise TypeError(f"backd: from_ ({start}) is after to ({end})")
    by_key = {g["key"][name]: dict(g) for g in groups}
    out: list[dict[str, Any]] = []
    if None in by_key:
        out.append(by_key[None])
    limit = 5000
    cursor = _key_start(start, unit)
    i = 0
    while True:
        key = bucket_key(cursor, unit)
        if key > end:
            break
        if i >= limit:
            raise ValueError(f"backd: more than {limit} buckets between {start} and {end}: use a larger unit")
        out.append(by_key.get(key) or {"key": {name: key}, "count": 0, **(empty or {})})
        cursor = _next(unit, cursor)
        i += 1
    return out
