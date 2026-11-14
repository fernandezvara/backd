"""What the sync and the async client share: how a call is addressed and what is sent. No I/O here."""

from __future__ import annotations

import re
from collections.abc import Mapping
from dataclasses import dataclass, field
from datetime import datetime
from typing import Any
from urllib.parse import quote, urlencode

import httpx

from ._retry import Retry
from .upload import Progress, Upload

QueryValue = str | int | float | bool | None


@dataclass
class Response:
    """The answer to a successful call."""

    status: int
    headers: httpx.Headers
    #: The parsed JSON body, or ``None`` for an empty one.
    data: Any = None
    #: With ``raw=True``: the answer, its body not read; the caller closes it.
    response: httpx.Response | None = None
    #: True when the server answered 304 and ``data`` is the body kept from an earlier read.
    from_cache: bool = False


@dataclass
class Request:
    """One call, as the resources describe it. ``path`` are the segments after ``/v1/{realm}``."""

    method: str
    path: list[str]
    query: Mapping[str, QueryValue] | None = None
    #: Sent as JSON.
    body: Any = None
    #: Sent as it is (a file's bytes), instead of ``body``.
    content: bytes | Upload | None = None
    content_type: str | None = None
    headers: Mapping[str, str] = field(default_factory=dict)
    #: Don't read the answer: it comes back as ``Response.response``, for a file's bytes.
    raw: bool = False
    #: Send credentials.
    auth: bool = True
    #: Treat a refused session token as expired.
    expire: bool = True
    #: A sensitive action: send the fresh proof ``auth.reauth`` gave, while it is good.
    reauth: bool = False
    #: ``False`` skips the revalidation cache for this read.
    cache: bool = True
    retry: Retry | None = None
    #: Called as an :class:`Upload` is sent.
    on_progress: Progress | None = None


def endpoint(base: str, realm: str, path: list[str], query: Mapping[str, QueryValue] | None = None) -> str:
    """The address of a call: ``{base}/v1/{realm}/{path…}?{query}``; each segment is encoded."""
    url = base + "/v1/" + "/".join(quote(s, safe="") for s in [realm, *path])
    pairs = [(k, _query_value(v)) for k, v in (query or {}).items() if v is not None]
    return url + "?" + urlencode(pairs) if pairs else url


def _query_value(v: QueryValue) -> str:
    if isinstance(v, bool):
        return "true" if v else "false"
    return str(v)


def normalize_org(ref: str | None) -> str:
    """An organization as the server compares it: case and surrounding spaces do not matter."""
    return (ref or "").strip().lower()


_FRACTION = re.compile(r"\.(\d{1,9})")


def parse_time(value: str) -> float:
    """An RFC 3339 time as epoch seconds (Python 3.10 reads neither ``Z`` nor nanoseconds)."""
    v = value.strip().replace("Z", "+00:00").replace("z", "+00:00")
    v = _FRACTION.sub(lambda m: "." + m.group(1)[:6].ljust(6, "0"), v, count=1)
    return datetime.fromisoformat(v).timestamp()


def session_gone(code: str) -> bool:
    """Whether the server no longer accepts the session: it expired or was ended (``unauthenticated``), or it
    belongs to no organization in a realm that has them (``session_unscoped``)."""
    return code in ("unauthenticated", "session_unscoped")


def is_safe(method: str, headers: Mapping[str, str]) -> bool:
    """Safe to repeat: reads, writes conditional on a version, and requests the server deduplicates by key."""
    names = {k.lower() for k in headers}
    return method == "GET" or "if-match" in names or "idempotency-key" in names


_SCHEMA_KEY = re.compile(r"^[a-z0-9]+(?:[-_][a-z0-9]+)*/[a-z0-9]+(?:[-_][a-z0-9]+)*$")


def schema_version_header(versions: Mapping[str, int] | None) -> str | None:
    """The value of ``X-Backd-Schema-Version`` for a map of ``<database>/<collection>`` to version, sorted so
    it is the same on every call; ``None`` when there is none."""
    if not versions:
        return None
    for key, n in versions.items():
        if not _SCHEMA_KEY.match(key) or isinstance(n, bool) or not isinstance(n, int) or n < 1:
            raise ValueError(f'schema_versions: {key!r} must be "<database>/<collection>" with a version of 1 or more')
    return ", ".join(f"{k}={versions[k]}" for k in sorted(versions))
