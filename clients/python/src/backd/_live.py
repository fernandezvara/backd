"""What the thread-based and the task-based realtime share: the stream's wire format, the state of a live list, and the
rules for keeping it. No I/O and no thread or task is started here; the two flavors (``_sync/realtime.py`` and
``_async/realtime.py``) add the reading of the stream and the loading of lists."""

from __future__ import annotations

import codecs
import json
import logging
import random
import re
import threading
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from functools import cmp_to_key
from typing import Any, Literal, TypedDict

from .errors import (
    AuthenticationError,
    BackdError,
    ForbiddenError,
    InvalidResumeTokenError,
    RealtimeNotEnabledError,
    SessionEndedError,
    ValidationError,
)

__all__ = [
    "LiveState",
    "LiveStatus",
    "LiveSubscriptionState",
    "ReferenceSpec",
    "SubscribeOptions",
    "SubscriptionSpec",
]

#: ``connecting``: the stream is opening (or reopening) and nothing has been loaded yet; ``live``: loaded and following;
#: ``resyncing``: reloading after a reset or a reconnection, while the last list stays available; ``closed``: stopped,
#: by ``close()`` or by an error.
LiveStatus = Literal["connecting", "live", "resyncing", "closed"]

_log = logging.getLogger("backd")

RECONNECT_BASE = 0.5  # seconds
MAX_BACKOFF = 30.0
#: The id the ``_config`` subscription has on the wire.
CONFIG_ID = "_config"


class _RefRequired(TypedDict):
    #: A field of the listed documents (a dot path) that holds the referenced document's id (or a list of ids).
    field: str
    collection: str


class ReferenceSpec(_RefRequired, total=False):
    """A reference the list's documents carry. ``db`` is the referenced collection's database (the subscription's by
    default); ``fields`` keeps only those fields of each referenced document (and its ``id`` and ``_meta``)."""

    db: str
    fields: list[str]


class _SpecRequired(TypedDict):
    db: str
    #: A collection that declares ``realtime: true``.
    collection: str


class SubscriptionSpec(_SpecRequired, total=False):
    """What to follow, and how to load it first.

    ``id`` names the subscription in ``state.subscriptions`` (its position by default). ``where`` is the filter, in
    the query language limited to comparisons, ``$in``, ``$nin``, ``$between`` and ``$isNull`` with ``$and``/``$or``:
    subscribe with an equality on a grouping field (``_meta.team``) to keep the server's work small. ``doc`` follows
    one document by id instead of a filter. ``load`` is ``{"sort": "-_meta.created_at" | [...], "limit": 100}``: the
    initial list and how the live list is kept (new documents are inserted in order; ``limit`` is the most it
    holds, 100 by default). ``references`` keeps referenced documents beside the list, by name."""

    id: str
    where: Mapping[str, Any]
    doc: str
    load: Mapping[str, Any]
    references: Mapping[str, ReferenceSpec]


@dataclass(frozen=True)
class SubscribeOptions:
    """``jitter``: a reload after a reset waits a random time up to this many seconds (default 1), so a reset of many
    clients doesn't make them all read at once. ``random``: source of randomness for the jitter (``random.random`` by
    default). ``max_backoff``: longest wait before reconnecting after a failure (default 30)."""

    jitter: float = 1.0
    random: Callable[[], float] | None = None
    max_backoff: float = MAX_BACKOFF


@dataclass
class LiveSubscriptionState:
    items: list[dict[str, Any]]
    status: LiveStatus


@dataclass
class LiveState:
    status: LiveStatus
    #: The first subscription's documents (the usual case: one).
    items: list[dict[str, Any]]
    #: Every subscription, by id.
    subscriptions: dict[str, LiveSubscriptionState]
    #: Referenced documents: by reference name, then id.
    references: dict[str, dict[str, dict[str, Any]]]


# -- the wire --------------------------------------------------------------------------------


@dataclass
class Event:
    event: str
    data: Any
    id: str | None = None


def parse_block(block: str) -> Event | None:
    """One server-sent event, from the text between two blank lines; ``None`` for a comment or anything else."""
    ev_id: str | None = None
    event = ""
    data: list[str] = []
    for line in block.split("\n"):
        if line.startswith(":"):
            continue
        if line.startswith("id:"):
            ev_id = line[3:].strip()
        elif line.startswith("event:"):
            event = line[6:].strip()
        elif line.startswith("data:"):
            data.append(line[5:].lstrip())
    if not event:
        return None
    parsed: Any = {}
    if data:
        try:
            parsed = json.loads("\n".join(data))
        except ValueError:
            return None
    return Event(event, parsed, ev_id)


class SSEParser:
    """Turns the bytes of a stream into events, however they were cut into chunks."""

    def __init__(self) -> None:
        self._decoder = codecs.getincrementaldecoder("utf-8")(errors="replace")
        self._buf = ""

    def feed(self, chunk: bytes) -> list[Event]:
        self._buf += self._decoder.decode(chunk).replace("\r\n", "\n")
        out: list[Event] = []
        while (i := self._buf.find("\n\n")) >= 0:
            block, self._buf = self._buf[:i], self._buf[i + 2 :]
            ev = parse_block(block)
            if ev is not None:
                out.append(ev)
        return out


# -- documents -------------------------------------------------------------------------------


def at(doc: Any, path: str) -> Any:
    v = doc
    for p in path.split("."):
        if not isinstance(v, dict):
            return None
        v = v.get(p)
    return v


def ids_at(doc: Any, field_: str) -> list[str]:
    v = at(doc, field_)
    if isinstance(v, str):
        return [v]
    if isinstance(v, list):
        return [x for x in v if isinstance(x, str)]
    return []


def version_of(doc: Any) -> int:
    v = (doc.get("_meta") or {}).get("version") if isinstance(doc, dict) else None
    return v if isinstance(v, int) else 0


def trim_fields(doc: dict[str, Any], fields: list[str] | None) -> dict[str, Any]:
    if not fields:
        return doc
    out: dict[str, Any] = {"id": doc.get("id"), "_meta": doc.get("_meta")}
    for f in fields:
        v = at(doc, f)
        if v is not None:
            out[f] = v
    return out


def _compare(a: Any, b: Any) -> int:
    if a == b:
        return 0
    if a is None:
        return -1  # missing and null sort first, as in the database
    if b is None:
        return 1
    try:
        return -1 if a < b else 1
    except TypeError:  # mixed types: by type, then by text
        ka, kb = (type(a).__name__, str(a)), (type(b).__name__, str(b))
        return -1 if ka < kb else (1 if ka > kb else 0)


def _sort_keys(spec: Mapping[str, Any]) -> list[tuple[str, int]]:
    sort = (spec.get("load") or {}).get("sort") or "id"
    raw = [sort] if isinstance(sort, str) else list(sort)
    keys = [k.strip() for item in raw for k in str(item).split(",") if k.strip()]
    parsed = [(k[1:], -1) if k.startswith("-") else ((k[1:] if k.startswith("+") else k), 1) for k in keys]
    if not any(p == "id" for p, _ in parsed):
        parsed.append(("id", 1))
    return parsed


class Sub:
    """One subscription of a view."""

    def __init__(self, wire: str, name: str, spec: SubscriptionSpec, view: Any) -> None:
        #: The id the stream knows it by.
        self.wire = wire
        #: The id the app knows it by.
        self.name = name
        self.spec = spec
        self.view = view
        self.items: dict[str, dict[str, Any]] = {}
        self.loaded = False
        self.needs_load = True
        #: Events that arrived while the list was loading: ``("doc", doc)`` or ``("removed", id, version)``.
        self.buffer: list[tuple[Any, ...]] = []
        self.generation = 0
        self.ever_loaded = False

    @property
    def limit(self) -> int:
        return int((self.spec.get("load") or {}).get("limit") or 100)

    def ordered(self) -> list[dict[str, Any]]:
        items = list(self.items.values())
        if self.spec.get("doc") is not None:
            return items
        keys = _sort_keys(self.spec)

        def cmp(a: dict[str, Any], b: dict[str, Any]) -> int:
            for path, direction in keys:
                c = _compare(at(a, path), at(b, path))
                if c:
                    return c * direction
            return 0

        return sorted(items, key=cmp_to_key(cmp))

    def apply_now(self, ev: tuple[Any, ...]) -> None:
        if ev[0] == "removed":
            held = self.items.get(ev[1])
            if held is not None and version_of(held) <= ev[2]:
                del self.items[ev[1]]
            return
        doc = ev[1]
        held = self.items.get(doc["id"])
        if held is not None and version_of(held) >= version_of(doc):
            return
        self.items[doc["id"]] = doc

    def trim(self) -> None:
        """Keeps a list within its ``limit``, dropping what sorts last."""
        if self.spec.get("doc") is not None or len(self.items) <= self.limit:
            return
        self.items = {d["id"]: d for d in self.ordered()[: self.limit]}


# -- the view --------------------------------------------------------------------------------


class LiveViewBase:
    """A live view of one or more subscriptions: the state, the rules that keep it, and the listeners. The flavors add
    how a list is loaded (``_start_load``) and how referenced documents are read (``_start_references``)."""

    def __init__(self, rt: RealtimeBase, options: SubscribeOptions) -> None:
        self._rt = rt
        self.options = options
        self.subs: list[Sub] = []
        self._handlers: dict[str, list[Callable[[Any], None]]] = {}
        self.closed = False
        #: The error that stopped the view, if one did.
        self.error: BaseException | None = None
        self._last_status: LiveStatus = "connecting"
        self._refs: dict[str, dict[str, dict[str, Any]]] = {}
        self._refs_asked: set[str] = set()
        self._pending_refs: dict[str, tuple[ReferenceSpec, list[str]]] = {}
        self._refs_pending = False
        self._outbox: list[tuple[str, Any]] = []
        self._change_pending = False
        self._dispatch = threading.RLock()

    # listeners

    def on(
        self, event: Literal["change", "status", "reset", "access_lost", "error"], fn: Callable[[Any], None]
    ) -> Callable[[], None]:
        """Listens: ``change`` (with the new :class:`LiveState`), ``status`` (a :data:`LiveStatus`), ``reset``
        (``{"subscription", "reason"}``, when the server asked for a reload), ``access_lost`` (``{"subscription"}``: a
        reload found the caller may no longer read it, and the list is empty) and ``error`` (an error that stopped
        the view). Callbacks run one at a time, outside any lock, so they can read the view. Returns a function that
        stops listening."""
        with self._rt.lock:
            self._handlers.setdefault(event, []).append(fn)

        def stop() -> None:
            with self._rt.lock:
                if fn in self._handlers.get(event, []):
                    self._handlers[event].remove(fn)

        return stop

    def _emit(self, event: str, arg: Any = None) -> None:
        self._outbox.append((event, arg))

    def _flush(self) -> None:
        """Calls the listeners of what happened. Called after every step that changes the state, outside the lock."""
        with self._dispatch:
            while True:
                with self._rt.lock:
                    if self._change_pending:
                        self._change_pending = False
                        if self._handlers.get("change"):
                            self._outbox.append(("change", self._snapshot()))
                    if not self._outbox:
                        return
                    pending, self._outbox = self._outbox, []
                    handlers = {k: list(v) for k, v in self._handlers.items()}
                for event, arg in pending:
                    for fn in handlers.get(event, []):
                        try:
                            fn(arg)
                        except Exception:
                            _log.exception("a live view %s listener failed", event)

    # state

    @property
    def state(self) -> LiveState:
        """The state now."""
        with self._rt.lock:
            return self._snapshot()

    @property
    def items(self) -> list[dict[str, Any]]:
        """The first subscription's documents, in order."""
        return self.state.items

    def _compute_status(self) -> LiveStatus:
        if self.closed:
            return "closed"
        if all(s.loaded for s in self.subs) and self._rt.ready:
            return "live"
        return "resyncing" if any(s.ever_loaded for s in self.subs) else "connecting"

    def _status(self) -> None:
        self._set_status(self._compute_status())

    def _set_status(self, s: LiveStatus) -> None:
        if s == self._last_status:
            return
        self._last_status = s
        self._emit("status", s)
        self._change_pending = True

    def _snapshot(self) -> LiveState:
        subscriptions: dict[str, LiveSubscriptionState] = {}
        for s in self.subs:
            if s.loaded:
                status: LiveStatus = "live" if self._rt.ready else "resyncing"
            else:
                status = "resyncing" if s.ever_loaded else "connecting"
            subscriptions[s.name] = LiveSubscriptionState(s.ordered(), status)
        refs = {name: dict(m) for name, m in self._refs.items()}
        first = subscriptions[self.subs[0].name].items if self.subs else []
        return LiveState(self._last_status, first, subscriptions, refs)

    def close(self) -> None:
        """Stops following. Safe to call more than once."""
        with self._rt.lock:
            if self.closed:
                return
            self.closed = True
            self._rt.remove(self)
            self._set_status("closed")
        self._flush()

    def _fail(self, err: BaseException) -> None:
        with self._rt.lock:
            if self.closed:
                return
            self.error = err
            self.closed = True
            self._rt.views.discard(self)
            for s in self.subs:
                self._rt.subs.pop(s.wire, None)
            self._set_status("closed")
            self._emit("error", err)
        self._flush()

    # events from the stream and the loads (call with the realtime lock held; flush afterwards)

    def _begin_load(self, s: Sub) -> int:
        s.needs_load = False
        s.loaded = False
        s.buffer = []
        s.generation += 1
        self._status()
        return s.generation

    def _loaded(self, s: Sub, generation: int, items: list[dict[str, Any]]) -> None:
        if self.closed or generation != s.generation:
            return
        s.items = {d["id"]: d for d in items}
        for ev in s.buffer:
            s.apply_now(ev)
        s.buffer = []
        s.loaded = True
        s.ever_loaded = True
        s.trim()
        self._collect_references(s)
        self._status()
        self._change_pending = True

    def _access_lost(self, s: Sub) -> None:
        s.items = {}
        s.buffer = []
        s.loaded = True
        self._emit("access_lost", {"subscription": s.name})
        self._status()
        self._change_pending = True

    def _apply(self, s: Sub, ev: tuple[Any, ...]) -> None:
        if self.closed:
            return
        if not s.loaded:
            s.buffer.append(ev)
            return
        s.apply_now(ev)
        s.trim()
        if ev[0] == "doc":
            self._collect_references(s)
        self._change_pending = True

    def _collect_references(self, s: Sub) -> None:
        """Finds referenced ids the view doesn't hold and queues one batched read per reference."""
        refs = s.spec.get("references")
        if not refs:
            return
        for name, ref in refs.items():
            held = self._refs.setdefault(name, {})
            for d in s.items.values():
                for ref_id in ids_at(d, ref["field"]):
                    key = f"{name}\0{ref_id}"
                    if ref_id not in held and key not in self._refs_asked:
                        self._refs_asked.add(key)
                        self._pending_refs.setdefault(name, (ref, []))[1].append(ref_id)
        if self._pending_refs and not self._refs_pending:
            self._refs_pending = True
            self._start_references(s)

    def _take_pending_references(self) -> dict[str, tuple[ReferenceSpec, list[str]]]:
        self._refs_pending = False
        pending, self._pending_refs = self._pending_refs, {}
        return pending

    def _references_read(self, name: str, ref: ReferenceSpec, docs: list[dict[str, Any]]) -> None:
        held = self._refs.setdefault(name, {})
        for d in docs:
            held[d["id"]] = trim_fields(d, ref.get("fields"))
        self._change_pending = True

    def _references_failed(self, name: str, ids: list[str]) -> None:
        for ref_id in ids:  # what can't be read stays unknown; the next change asks again
            self._refs_asked.discard(f"{name}\0{ref_id}")

    # the flavors

    def _start_load(self, s: Sub, jitter: bool) -> None:
        """Starts loading (or reloading) a subscription's list. The list is marked as loading at once, so events that
        arrive next wait in its buffer; the read itself runs in the background."""
        with self._rt.lock:
            if self.closed:
                return
            generation = self._begin_load(s)
        self._flush()
        self._spawn_load(s, generation, jitter)

    def _spawn_load(self, s: Sub, generation: int, jitter: bool) -> None:
        raise NotImplementedError

    def _start_references(self, s: Sub) -> None:
        raise NotImplementedError

    def _jitter_delay(self) -> float:
        rnd = self.options.random or random.random
        return float(rnd()) * self.options.jitter


# -- the manager -----------------------------------------------------------------------------


@dataclass
class ConfigFollower:
    """Who hears of the configuration on the stream."""

    #: The branches followed; none: everything the caller reads.
    paths: list[str]
    #: The stream is open (again): what was missed while it was down is read now.
    ready: Callable[[], None]
    #: Keys that changed; none: any of them might have.
    changed: Callable[[list[str]], None]
    #: The subscription was refused or the stream ended for good.
    error: Callable[[BaseException], None]


_SUB_AT = re.compile(r"^subscriptions\[(\d+)\]")


class RealtimeBase:
    """Live lists over one stream per client: every subscription of every view is multiplexed over
    ``POST /v1/{realm}/_realtime``, each list is loaded stream-first (events received while it loads are applied
    after, ignoring versions that are not newer), kept sorted, resumed after a disconnection, reloaded when the
    server resets it (with jitter), and the stream is opened again at the end of the credential's lifetime and
    when the set of subscriptions changes."""

    def __init__(self) -> None:
        self.lock = threading.RLock()
        self.subs: dict[str, Sub] = {}
        self.views: set[LiveViewBase] = set()
        self._seq = 0
        self.token: str | None = None
        #: Counts the streams: a loop of an older one ends.
        self.gen = 0
        self.ready = False
        self.running = False
        self.closed = False
        self.config: ConfigFollower | None = None

    @property
    def open(self) -> int:
        """The number of open views."""
        return len(self.views)

    def _new_view(self, options: SubscribeOptions) -> LiveViewBase:
        raise NotImplementedError

    def schedule(self) -> None:
        raise NotImplementedError

    def follow_config(self, follower: ConfigFollower) -> Callable[[], None]:
        """Adds a ``_config`` subscription to the stream, which tells the follower when what the caller reads of the app
        configuration changes. One follower at a time; ``client.config.live()`` is what uses it. Returns a function
        that removes it."""
        with self.lock:
            self.config = follower
        self.schedule()

        def stop() -> None:
            with self.lock:
                if self.config is not follower:
                    return
                self.config = None
            self.schedule()

        return stop

    def subscribe(self, specs: list[SubscriptionSpec], options: SubscribeOptions | None = None) -> Any:
        """Follows collections live. The returned view loads each subscription, keeps its list current and tells you
        when it changes::

            live = client.realtime.subscribe([
                {"db": "main", "collection": "items", "where": {"_meta.team": team_id},
                 "load": {"sort": "-_meta.created_at", "limit": 50}},
            ])
            live.on("change", lambda state: render(state.items))
            ...
            live.close()
        """
        if not specs:
            raise TypeError("realtime.subscribe: at least one subscription is required")
        if self.closed:
            raise RuntimeError("realtime.subscribe: the client is closed")
        with self.lock:
            view = self._new_view(options or SubscribeOptions())
            names: set[str] = set()
            for i, spec in enumerate(specs):
                if not spec.get("db") or not spec.get("collection"):
                    raise TypeError("realtime.subscribe: each subscription needs `db` and `collection`")
                name = spec.get("id", str(i))
                if name in names:
                    raise TypeError(f'realtime.subscribe: duplicate subscription id "{name}"')
                names.add(name)
                self._seq += 1
                sub = Sub(f"s{self._seq}", name, spec, view)
                view.subs.append(sub)
                self.subs[sub.wire] = sub
            self.views.add(view)
        self.schedule()
        return view

    def remove(self, view: LiveViewBase) -> None:
        with self.lock:
            self.views.discard(view)
            for s in view.subs:
                self.subs.pop(s.wire, None)
        self.schedule()

    def max_backoff(self) -> float:
        return min([MAX_BACKOFF, *(v.options.max_backoff for v in self.views)])

    def request_body(self) -> dict[str, Any]:
        subscriptions: list[dict[str, Any]] = []
        for s in self.subs.values():
            item: dict[str, Any] = {"id": s.wire, "db": s.spec["db"], "collection": s.spec["collection"]}
            if s.spec.get("doc") is not None:
                item["doc"] = s.spec["doc"]
            elif s.spec.get("where"):
                item["where"] = s.spec["where"]
            subscriptions.append(item)
        if self.config is not None:
            subscriptions.append({"id": CONFIG_ID, "config": {"paths": self.config.paths}})
        body: dict[str, Any] = {"subscriptions": subscriptions}
        if self.token:
            body["resume_from"] = self.token
        return body

    def classify(self, err: BackdError) -> Literal["restart", "stop", "continue", "retry"]:
        """What a failure to open or read the stream means: ``restart`` open another stream at once with a fresh
        start (the resume token was refused), ``continue`` open another at once (the refusal ended only the view or the
        configuration subscription that asked), ``stop`` end every view, ``retry`` wait and open another."""
        if isinstance(err, InvalidResumeTokenError):
            # The token is not good (another credential, a rotated key): start over from a reload.
            with self.lock:
                self.token = None
                for s in self.subs.values():
                    s.needs_load = True
            return "restart"
        refusal = (RealtimeNotEnabledError, ForbiddenError, ValidationError, AuthenticationError)
        if not isinstance(err, refusal):
            return "retry"
        # A refusal that names a subscription ends the view that asked for it; the others go on.
        detail = (err.details[0]["path"] if err.details else "") or ""
        m = _SUB_AT.match(detail)
        with self.lock:
            index = int(m.group(1)) if m else -1
            subs = list(self.subs.values())
            bad = subs[index] if 0 <= index < len(subs) else None
            if m and bad is None and self.config is not None and index == len(subs):
                # The refusal is of the configuration subscription (the realm has none): only it ends.
                follower, self.config = self.config, None
                ended = True
            else:
                follower, ended = None, False
            many = len(self.views) > 1
        if ended and follower is not None:
            follower.error(err)
            return "stop" if not self.subs else "continue"
        if bad is not None and many:
            bad.view._fail(err)
            return "continue"
        return "stop"

    def fail(self, err: BaseException) -> None:
        """Ends every view (and the configuration follower) with ``err``."""
        with self.lock:
            follower, self.config = self.config, None
            self.gen += 1
            self.running = False
            self.ready = False
            views = list(self.views)
        if follower is not None:
            follower.error(err)
        for v in views:
            v._fail(err)

    def stream_over(self, gen: int) -> None:
        """The stream of this generation ended or failed: the views are no longer live."""
        with self.lock:
            if gen != self.gen:
                return
            self.ready = False
            views = list(self.views)
            for v in views:
                v._status()
        for v in views:
            v._flush()

    def handle(self, ev: Event) -> bool | None:
        """Applies one event of the stream. Returns what the read of the stream should return when it is over: ``True``
        (open the next stream at once), ``False`` (it ended), or ``None`` (keep reading)."""
        views: set[LiveViewBase] = set()
        loads: list[tuple[Sub, bool]] = []
        config_calls: list[Callable[[], None]] = []
        result: bool | None = None
        failure: BackdError | None = None
        with self.lock:
            if ev.id:
                self.token = ev.id
            data = ev.data if isinstance(ev.data, dict) else {}
            if ev.event == "ready":
                self.ready = True
                for s in self.subs.values():
                    if s.needs_load or not s.loaded:
                        loads.append((s, False))
                views |= self.views
                for v in self.views:
                    v._status()
                if self.config is not None:
                    config_calls.append(self.config.ready)
            elif ev.event == "config_changed":
                if self.config is not None:
                    follower = self.config
                    paths = data.get("paths")
                    changed = paths if isinstance(paths, list) else []
                    config_calls.append(lambda: follower.changed(changed))
            elif ev.event in ("created", "updated"):
                target = self.subs.get(data.get("sub", ""))
                if target is not None:
                    target.view._apply(target, ("doc", data.get("doc")))
                    views.add(target.view)
            elif ev.event == "removed":
                target = self.subs.get(data.get("sub", ""))
                if target is not None:
                    target.view._apply(target, ("removed", data.get("id"), data.get("version", 0)))
                    views.add(target.view)
            elif ev.event == "reset":
                again = False
                for sid in data.get("subs") or []:
                    dropped = self.subs.get(sid)
                    if dropped is None:
                        continue
                    dropped.view._emit("reset", {"subscription": dropped.name, "reason": data.get("reason")})
                    views.add(dropped.view)
                    if data.get("reason") == "config_changed":
                        # The server dropped it: it has to be made again, on a new stream, and loaded after it.
                        dropped.needs_load = True
                        again = True
                    else:
                        loads.append((dropped, True))
                result = True if again else None
            elif ev.event == "session_ended":
                failure = _session_ended()
                result = False
            elif ev.event == "reauth":
                result = True  # the credential's lifetime is over: reconnect, which sends the current one
        if failure is not None:
            self.fail(failure)
        for load, jitter in loads:
            load.view._start_load(load, jitter)
        for v in views:
            v._flush()
        for call in config_calls:
            call()
        return result


def _session_ended() -> BackdError:
    return SessionEndedError(
        "the session, user or API key behind the stream is gone: sign in again", status=401, code="session_ended"
    )
