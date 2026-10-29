"""Live lists over one stream, with threads: ``client.realtime``. Written by hand: a thread and a task differ in more
than syntax. The async twin is ``_async/realtime.py``; the state and the rules both use are in ``_live.py``."""

from __future__ import annotations

import threading
from typing import TYPE_CHECKING, Any, cast

import httpx

from .._live import (
    RECONNECT_BASE,
    LiveState,
    LiveViewBase,
    RealtimeBase,
    SSEParser,
    Sub,
    SubscribeOptions,
)
from ..errors import BackdError, ForbiddenError, NetworkError

if TYPE_CHECKING:
    from collections.abc import Callable

    from .client import Client

__all__ = ["LiveView", "Realtime"]

_JOIN_SECONDS = 5.0


class LiveView(LiveViewBase):
    """A live view of one or more subscriptions, kept by threads. Listeners are called from those threads, one at a
    time."""

    def __init__(self, rt: Realtime, options: SubscribeOptions) -> None:
        super().__init__(rt, options)
        self._realtime = rt
        self._threads: set[threading.Thread] = set()
        self._stop = threading.Event()

    def _spawn(self, target: Callable[..., None], *args: Any) -> None:
        def run() -> None:
            try:
                target(*args)
            finally:
                with self._rt.lock:
                    self._threads.discard(threading.current_thread())

        thread = threading.Thread(target=run, daemon=True, name="backd-live")
        with self._rt.lock:
            self._threads.add(thread)
        thread.start()

    def close(self) -> None:
        self._stop.set()
        super().close()

    def wait_ready(self, timeout: float | None = None) -> LiveState:
        """Returns the state once every subscription has loaded; raises the error that stopped the view first, or
        :class:`TimeoutError` after ``timeout`` seconds."""
        done = threading.Event()
        stops = [
            self.on("status", lambda s: done.set() if s in ("live", "closed") else None),
            self.on("error", lambda e: done.set()),
        ]
        try:
            with self._rt.lock:
                if self._compute_status() in ("live", "closed"):
                    done.set()
            if not done.wait(timeout):
                raise TimeoutError("the live view did not load in time")
        finally:
            for stop in stops:
                stop()
        if self.error is not None:
            raise self.error
        if self.closed:
            raise BackdError("the live view was closed", code="closed")
        return self.state

    # loading

    def _spawn_load(self, s: Sub, generation: int, jitter: bool) -> None:
        self._spawn(self._load, s, generation, jitter)

    def _load(self, s: Sub, generation: int, jitter: bool) -> None:
        """Reads a subscription's list. Events that arrive meanwhile wait in its buffer and are applied after, ignoring
        any whose version is not newer. A reload after a reset waits a random time first."""
        if jitter and self._stop.wait(self._jitter_delay()):
            return
        with self._rt.lock:
            if self.closed or generation != s.generation:
                return
        try:
            items = self._fetch(s)
        except Exception as err:
            with self._rt.lock:
                stale = self.closed or generation != s.generation
            if stale:
                return
            if isinstance(err, ForbiddenError) and s.ever_loaded:
                # Access was lost since the list was read (a member removed from a team): the list is empty for this
                # caller now. The view stays, and loads again if a later reset says so.
                with self._rt.lock:
                    self._access_lost(s)
                self._flush()
                return
            if isinstance(err, NetworkError) or (isinstance(err, BackdError) and err.status >= 500):
                # The list could not be read now: try again, after a pause.
                if not self._stop.wait(1.0):
                    self._start_load(s, False)
                return
            self._fail(err)
            return
        with self._rt.lock:
            self._loaded(s, generation, items)
        self._flush()

    def _fetch(self, s: Sub) -> list[dict[str, Any]]:
        col = self._realtime.client.db(s.spec["db"]).collection(s.spec["collection"])
        doc = s.spec.get("doc")
        if doc is not None:
            try:
                return [cast(dict[str, Any], col.get(doc))]
            except BackdError as err:
                if err.status == 404:
                    return []
                raise
        load = s.spec.get("load") or {}
        out: list[dict[str, Any]] = []
        after: str | None = None
        while len(out) < s.limit:
            page = col.list(
                where=s.spec.get("where"), order_by=load.get("sort"), limit=min(100, s.limit - len(out)), after=after
            )
            out.extend(page.items)
            if not page.has_more or not page.next_cursor:
                break
            after = page.next_cursor
        return out

    def _start_references(self, s: Sub) -> None:
        self._spawn(self._fetch_references, s)

    def _fetch_references(self, s: Sub) -> None:
        """Reads the referenced ids the view doesn't hold, in one batched read per reference."""
        with self._rt.lock:
            pending = self._take_pending_references()
            if not pending or self.closed:
                return
        for name, (ref, ids) in pending.items():
            col = self._realtime.client.db(ref.get("db") or s.spec["db"]).collection(ref["collection"])
            for i in range(0, len(ids), 100):
                chunk = ids[i : i + 100]
                try:
                    page = col.list(where={"id": {"$in": chunk}}, limit=len(chunk))
                except Exception:
                    with self._rt.lock:
                        self._references_failed(name, chunk)
                    continue
                with self._rt.lock:
                    self._references_read(name, ref, page.items)
        self._flush()


class Realtime(RealtimeBase):
    """Live lists over one stream per client: ``client.realtime``."""

    def __init__(self, client: Client) -> None:
        super().__init__()
        self.client = client
        self._thread: threading.Thread | None = None
        self._response: httpx.Response | None = None
        self._timer: threading.Timer | None = None
        self._closing = threading.Event()
        client.root._realtimes.append(self)
        client.on_close(self._close)

    def _new_view(self, options: SubscribeOptions) -> LiveView:
        return LiveView(self, options)

    def subscribe(self, specs: Any, options: SubscribeOptions | None = None) -> LiveView:
        return cast(LiveView, super().subscribe(specs, options))

    def schedule(self) -> None:
        """Reconciles the stream with the subscriptions, once per burst of changes."""
        with self.lock:
            if self._timer is not None or self._closing.is_set():
                return
            self._timer = threading.Timer(0.005, self._reconcile)
            self._timer.daemon = True
            self._timer.start()

    def _drop_stream(self) -> None:
        """Ends the stream that is open, if one is: closing the answer wakes the thread that is reading it."""
        response, self._response = self._response, None
        if response is not None:
            try:
                response.close()
            except Exception:  # already gone
                pass

    def _reconcile(self) -> None:
        with self.lock:
            self._timer = None
            if self._closing.is_set():
                return
            # The set changed (or this is the first subscription): a new stream with the same resume token.
            self.gen += 1
            self._drop_stream()
            self.ready = False
            if not self.subs and self.config is None:
                self.running = False
                return
            views = list(self.views)
            for v in views:
                v._status()
            self.running = True
            self._thread = threading.Thread(target=self._connect, args=(self.gen,), daemon=True, name="backd-realtime")
            self._thread.start()
        for v in views:
            v._flush()

    def _close(self) -> None:
        """Ends every view and the stream (the client was closed), and waits for the threads: none is left running."""
        self._closing.set()
        with self.lock:
            timer, self._timer = self._timer, None
            views = list(self.views)
            self.gen += 1
            self._drop_stream()
            thread = self._thread
            self.running = False
            self.ready = False
        if timer is not None:
            timer.cancel()
        for v in views:
            v.close()
        threads = [thread] if thread is not None else []
        for v in views:
            threads.extend(cast(LiveView, v)._threads)
        for t in threads:
            if t is not threading.current_thread():
                t.join(_JOIN_SECONDS)

    def _join(self) -> None:
        """(The threads were joined when the client closed.)"""

    def _connect(self, gen: int) -> None:
        attempt = 0
        while gen == self.gen:
            reconnect_now = False
            try:
                with self.lock:
                    body = self.request_body()
                res = self.client.open_stream(["_realtime"], body)
                with self.lock:
                    if gen != self.gen:
                        res.close()
                        return
                    self._response = res
                attempt = 0
                reconnect_now = self._read(res, gen)
            except BackdError as err:
                if gen != self.gen:
                    return
                action = self.classify(err)
                if action in ("restart", "continue"):
                    continue
                if action == "stop":
                    self.fail(err)
                    return
            if gen != self.gen:
                return
            self.stream_over(gen)
            if reconnect_now:
                continue
            wait = min(RECONNECT_BASE * 2**attempt, self.max_backoff())
            attempt += 1
            if self._closing.wait(wait) or gen != self.gen:
                return

    def _read(self, res: httpx.Response, gen: int) -> bool:
        """Reads the stream until it ends. Returns True when the next stream should open at once (the server asked:
        ``reauth``, or a reset that needs the subscription made again)."""
        parser = SSEParser()
        try:
            for chunk in res.iter_bytes():
                for ev in parser.feed(chunk):
                    if gen != self.gen:
                        return False
                    again = self.handle(ev)
                    if again is not None:
                        return again
            return False
        except httpx.HTTPError as e:
            if gen != self.gen:
                return False  # the stream was closed on purpose
            raise NetworkError(f"network error: {e}", code="network_error") from e
        finally:
            try:
                res.close()
            except Exception:  # already gone
                pass
