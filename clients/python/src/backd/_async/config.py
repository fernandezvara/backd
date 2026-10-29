"""The realm's app configuration: ``client.config``. Source of ``_sync/config.py``."""

from __future__ import annotations

import copy
import hashlib
import json
import logging
import re
import threading
import time
from collections.abc import Callable
from dataclasses import dataclass
from typing import TYPE_CHECKING, Any, Literal

from .._aio import spawn
from ..config_storage import ConfigStorage

if TYPE_CHECKING:
    from .client import AsyncClient

__all__ = ["AsyncConfig", "flatten"]

_PREFIX = "backd.config:"
_log = logging.getLogger("backd")


@dataclass
class _Entry:
    """One cached answer: the values of the paths asked for, with what revalidates them."""

    etag: str
    #: Nested, as the server returned them.
    values: dict[str, Any]
    version: str
    #: Epoch seconds.
    fetched_at: float
    #: Seconds the copy is fresh for.
    ttl: float


def flatten(values: dict[str, Any], prefix: str = "", out: dict[str, Any] | None = None) -> dict[str, Any]:
    """The leaves of a nested document by dot path."""
    out = {} if out is None else out
    for k, v in (values or {}).items():
        p = f"{prefix}.{k}" if prefix else k
        if isinstance(v, dict):
            flatten(v, p, out)
        else:
            out[p] = v
    return out


def _at(values: dict[str, Any], path: str) -> Any:
    cur: Any = values
    for k in path.split("."):
        if not isinstance(cur, dict) or k not in cur:
            return None
        cur = cur[k]
    return cur


def _normalize(paths: list[str] | str | None) -> str:
    """The key of a request: its paths, trimmed, sorted and without repeats; '' for everything."""
    items = paths if isinstance(paths, list) else str(paths or "").split(",")
    return ",".join(sorted({p.strip() for p in items if p.strip()}))


_MAX_AGE = re.compile(r"max-age=(\d+)", re.IGNORECASE)


class AsyncConfig:
    """The realm's app configuration: flags, switches and limits. Values come from ``GET /v1/{realm}/_config``,
    which answers only what the signed-in user's rules allow, with defaults filled in.

    Copies are kept per realm and per user (in memory, and in ``config_storage`` when the client has one): fresh
    for the ``cache_ttl`` the server gave, with no request; after that reads return the copy at once and
    revalidate it in the background with ``If-None-Match``, which usually costs a ``304``. They are dropped on
    sign-out and when another user signs in.

        client.config.load(["features", "maintenance"])
        client.config.get("maintenance.enabled")      # from the copy; fetched first if never loaded
        client.config.on("change", lambda paths: render(paths))
    """

    def __init__(self, client: AsyncClient, storage: ConfigStorage | None = None) -> None:
        self._client = client
        self._store = storage
        #: Entries by the paths they were asked with ('' is everything).
        self._entries: dict[str, _Entry] = {}
        #: The caller the entries belong to.
        self._owner = ""
        self._lock = threading.RLock()
        self._listeners: list[Callable[[list[str]], None]] = []
        self._error_listeners: list[Callable[[Exception], None]] = []

    # -- reading ---------------------------------------------------------------------------

    async def load(self, paths: list[str] | str | None = None, *, refresh: bool = False) -> dict[str, Any]:
        """Fetches the configuration (or the groups and keys named) and keeps it. Without a request while the
        copy is fresh; with ``refresh=True`` always asks the server. Returns the values, nested."""
        key = _normalize(paths)
        self._scope()
        entry = self._entries.get(key)
        if entry is not None and not refresh and self._fresh(entry):
            return self.view(key)
        await self.fetch(key)
        return self.view(key)

    async def get(self, path: str, *, refresh: bool = False, default: Any = None) -> Any:
        """One value. From the copy held, at once (a stale copy is revalidated in the background); a key whose
        group was never loaded is fetched first. ``refresh=True`` always asks the server. A key the user can't
        read, or that doesn't exist, is ``default``."""
        held = self._cover(path)
        if held is None or refresh:
            self._scope()  # may bring back the copy a previous run saved
            held = self._cover(path)
            if held is None or refresh:
                # A refresh asks again for the copy that holds the key; a key never loaded, for its group.
                await self.fetch(held[0] if held else path.split(".")[0])
                held = self._cover(path)
                return self._value(held[1] if held else None, path, default)
        if not self._fresh(held[1]):
            spawn(self.revalidate, held[0])
        return self._value(held[1], path, default)

    def get_sync(self, path: str, default: Any = None) -> Any:
        """One value from the copy held, with no request at all: ``default`` when it was never loaded."""
        held = self._cover(path)
        return self._value(held[1] if held else None, path, default)

    @staticmethod
    def _value(entry: _Entry | None, path: str, default: Any) -> Any:
        v = _at(entry.values, path) if entry else None
        return default if v is None else v

    def view(self, key: str = "") -> dict[str, Any]:
        """A copy of the values held for a request key (the paths it was loaded with, comma separated; empty for
        everything)."""
        e = self._entries.get(_normalize(key))
        return copy.deepcopy(e.values) if e else {}

    def on(self, event: Literal["change", "error"], listener: Callable[[Any], None]) -> Callable[[], None]:
        """``change``: calls the listener with the dot paths whose value changed when a read brings a different
        configuration. ``error``: with the error that ended a live follow. Returns a function that stops
        listening."""
        if event not in ("change", "error"):
            raise TypeError("client.config.on: the events are 'change' and 'error'")
        listeners: list[Any] = self._listeners if event == "change" else self._error_listeners
        listeners.append(listener)

        def stop() -> None:
            if listener in listeners:
                listeners.remove(listener)

        return stop

    # -- keeping it ------------------------------------------------------------------------

    def _fresh(self, e: _Entry) -> bool:
        return time.time() < e.fetched_at + e.ttl

    def _cover(self, path: str) -> tuple[str, _Entry] | None:
        """The entry that holds a path: the whole configuration, or a group that contains it."""
        with self._lock:
            all_ = self._entries.get("")
            if all_ is not None:
                return "", all_
            for key, entry in self._entries.items():
                for p in key.split(","):
                    if p and (path == p or path.startswith(p + ".")):
                        return key, entry
        return None

    def _scope(self) -> str:
        """Who the copies belong to: the credentials of the client, hashed (nothing secret is stored)."""
        c = self._client
        credential = ""
        if c._api_key:
            credential = "key:" + c._api_key
        else:
            token = c.storage.get()
            if token:
                credential = "session:" + token
        owner = hashlib.sha256(credential.encode()).hexdigest()[:16] if credential else "anonymous"
        with self._lock:
            if owner != self._owner:
                self._entries.clear()
                self._owner = owner
                self._restore()
        return owner

    def _storage_key(self) -> str:
        return f"{_PREFIX}{self._client.realm}:{self._owner}"

    def _restore(self) -> None:
        if self._store is None:
            return
        try:
            raw = self._store.get(self._storage_key())
            if not raw:
                return
            for k, e in (json.loads(raw) or {}).items():
                if isinstance(e, dict) and isinstance(e.get("etag"), str) and isinstance(e.get("values"), dict):
                    self._entries[k] = _Entry(
                        e["etag"], e["values"], str(e.get("version", "")), float(e["fetched_at"]), float(e["ttl"])
                    )
        except (OSError, ValueError, KeyError, TypeError):
            _log.debug("backd: the saved configuration is unreadable; starting from nothing")

    def _persist(self) -> None:
        if self._store is None:
            return
        try:
            data = {k: vars(e) for k, e in self._entries.items()}
            self._store.set(self._storage_key(), json.dumps(data))
        except OSError:
            _log.debug("backd: the configuration could not be saved; the copy in memory still works")

    def clear(self) -> None:
        """Forgets every copy of this realm, for every user: on sign-out and when another user signs in."""
        with self._lock:
            self._entries.clear()
            self._owner = ""
        if self._store is None:
            return
        try:
            for k in self._store.all_keys():
                if k.startswith(f"{_PREFIX}{self._client.realm}:"):
                    self._store.remove(k)
        except OSError:
            _log.debug("backd: the saved configuration could not be removed")

    async def fetch(self, key: str) -> _Entry:
        """Asks the server (with the tag of the copy held) and keeps the answer."""
        owner = self._owner
        held = self._entries.get(key)
        headers = {"If-None-Match": held.etag} if held else {}
        res = await self._client.request("GET", ["_config"], query={"paths": key or None}, headers=headers, cache=False)
        with self._lock:
            if owner != self._owner:
                # The user changed while the request was out: what came back is not theirs to keep.
                return held or _Entry("", {}, "", 0, 0)
            if res.status == 304 and held is not None:
                held.fetched_at = time.time()
                m = _MAX_AGE.search(res.headers.get("Cache-Control", ""))
                held.ttl = float(m.group(1)) if m else held.ttl
                self._persist()
                return held
            body = res.data or {}
            ttl = body.get("cache_ttl")
            entry = _Entry(
                etag=res.headers.get("ETag", ""),
                values=body["values"] if isinstance(body.get("values"), dict) else {},
                version=str(body.get("version", "")),
                fetched_at=time.time(),
                ttl=float(ttl) if isinstance(ttl, int | float) else 0.0,
            )
            before = flatten(held.values if held else {})
            self._entries[key] = entry
            self._persist()
            changed: list[str] = []
            if held is not None:
                after = flatten(entry.values)
                changed = sorted(
                    p
                    for p in {*before, *after}
                    if json.dumps(before.get(p), sort_keys=True) != json.dumps(after.get(p), sort_keys=True)
                )
        if changed:
            self._emit(changed)
        return entry

    async def revalidate(self, key: str) -> None:
        try:
            await self.fetch(key)
        except Exception:  # offline or refused: the copy held keeps serving, and the next read tries again
            _log.debug("backd: the configuration could not be revalidated", exc_info=True)

    def _emit(self, paths: list[str]) -> None:
        for listener in list(self._listeners):
            try:
                listener(paths)
            except Exception:  # a failing listener must not break the client or the other listeners
                _log.exception("a client.config change listener failed")

    def _emit_error(self, err: Exception) -> None:
        for listener in list(self._error_listeners):
            try:
                listener(err)
            except Exception:
                _log.exception("a client.config error listener failed")
