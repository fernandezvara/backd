"""The revalidation cache: the last body and ``ETag`` of each document and list read, kept in memory so the
next read can send ``If-None-Match`` and take a ``304``. Nothing here is served without asking the server:
every read still goes out, and the server (which runs the rules) decides."""

from __future__ import annotations

import re
import threading
from collections import OrderedDict
from collections.abc import Mapping
from dataclasses import dataclass
from typing import Any
from urllib.parse import urlsplit

import httpx

__all__ = ["CacheOptions", "CacheStats", "Entry", "RevalidationCache", "cacheable"]


@dataclass(frozen=True)
class CacheOptions:
    """How much the cache keeps: ``max_entries`` (default 200) and ``max_bytes`` of bodies (default 5 MiB)."""

    max_entries: int = 200
    max_bytes: int = 5 * 1024 * 1024


@dataclass(frozen=True)
class CacheStats:
    """What ``client.cache_stats()`` reports."""

    #: Reads the server answered ``304`` to: the cached body was used.
    hits: int
    #: Cacheable reads that came back in full.
    misses: int
    entries: int
    bytes: int


@dataclass
class Entry:
    etag: str
    #: The body, as received.
    body: bytes
    headers: httpx.Headers


class RevalidationCache:
    """An LRU of the last body and ``ETag`` of reads, bounded by entries and bytes. Safe for threads."""

    def __init__(self, options: CacheOptions | None = None) -> None:
        o = options or CacheOptions()
        self.max_entries = o.max_entries
        self.max_bytes = o.max_bytes
        #: Insertion order is recency: the oldest is first.
        self._entries: OrderedDict[str, Entry] = OrderedDict()
        self._bytes = 0
        self.hits = 0
        self.misses = 0
        #: Who the entries belong to (see :meth:`scope`); a different one empties the cache.
        self._owner = ""
        self._lock = threading.RLock()

    def scope(self, owner: str) -> None:
        """Empties the cache when the caller isn't the one its entries were read as. ``owner`` identifies the
        credentials of the request and is never sent anywhere."""
        with self._lock:
            if owner != self._owner:
                self.clear()
                self._owner = owner

    def get(self, key: str) -> Entry | None:
        with self._lock:
            e = self._entries.get(key)
            if e is not None:
                self._entries.move_to_end(key)  # most recently used
            return e

    def set(self, key: str, entry: Entry) -> None:
        with self._lock:
            self.delete(key)
            # A body that alone would exceed the bound is not worth keeping.
            if len(entry.body) > self.max_bytes:
                return
            self._entries[key] = entry
            self._bytes += len(entry.body)
            while self._entries and (len(self._entries) > self.max_entries or self._bytes > self.max_bytes):
                self.delete(next(iter(self._entries)))

    def delete(self, key: str) -> None:
        with self._lock:
            e = self._entries.pop(key, None)
            if e is not None:
                self._bytes -= len(e.body)

    def drop_path(self, path: str) -> None:
        """Drops every entry of a path, whatever its query: a document after a write to it."""
        with self._lock:
            for key in [k for k in self._entries if urlsplit(k).path == path]:
                self.delete(key)

    def clear(self) -> None:
        with self._lock:
            self._entries.clear()
            self._bytes = 0

    def hit(self) -> None:
        with self._lock:
            self.hits += 1

    def miss(self) -> None:
        with self._lock:
            self.misses += 1

    def stats(self) -> CacheStats:
        with self._lock:
            return CacheStats(self.hits, self.misses, len(self._entries), self._bytes)


_NO_STORE = re.compile(r"no-store", re.IGNORECASE)


def no_store(cache_control: str | None) -> bool:
    """Whether the server asked for the answer not to be kept."""
    return bool(cache_control and _NO_STORE.search(cache_control))


def cacheable(method: str, path: list[str], query: Mapping[str, Any] | None, raw: bool) -> bool:
    """Whether a request is a document or list read the cache may keep: ``[database, collection]`` or
    ``[database, collection, id]``, without the parameters that make an answer per-response (signed file
    links, expansions)."""
    if method != "GET" or raw or len(path) < 2 or len(path) > 3:
        return False
    if any(s.startswith("_") for s in path):
        return False
    q = query or {}
    return q.get("file_links") is None and q.get("expand") is None
