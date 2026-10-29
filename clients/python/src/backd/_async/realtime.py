"""Live lists over one stream, with tasks: ``client.realtime``. The thread-based twin is written by hand in
``_sync/realtime.py``; the state and the rules both use are in ``_live.py``."""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, Any, cast

import httpx

from .._aio import sleep
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
    from collections.abc import Coroutine

    from .client import AsyncClient

__all__ = ["AsyncLiveView", "AsyncRealtime"]


class AsyncLiveView(LiveViewBase):
    """A live view of one or more subscriptions, kept by tasks of the running event loop."""

    def __init__(self, rt: AsyncRealtime, options: SubscribeOptions) -> None:
        super().__init__(rt, options)
        self._arealtime = rt
        self._tasks: set[asyncio.Task[None]] = set()

    def _spawn(self, coro: Coroutine[Any, Any, None]) -> None:
        task = asyncio.get_running_loop().create_task(coro)
        self._tasks.add(task)  # a task nothing refers to may be collected before it ends
        task.add_done_callback(self._tasks.discard)

    def close(self) -> None:
        super().close()
        for task in list(self._tasks):
            task.cancel()

    async def wait_ready(self, timeout: float | None = None) -> LiveState:
        """Returns the state once every subscription has loaded; raises the error that stopped the view first, or
        :class:`asyncio.TimeoutError` after ``timeout`` seconds."""
        done = asyncio.Event()
        stops = [
            self.on("status", lambda s: done.set() if s in ("live", "closed") else None),
            self.on("error", lambda e: done.set()),
        ]
        try:
            with self._rt.lock:
                if self._compute_status() in ("live", "closed"):
                    done.set()
            await asyncio.wait_for(done.wait(), timeout)
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
        self._spawn(self._load(s, generation, jitter))

    async def _load(self, s: Sub, generation: int, jitter: bool) -> None:
        """Reads a subscription's list. Events that arrive meanwhile wait in its buffer and are applied after, ignoring
        any whose version is not newer. A reload after a reset waits a random time first."""
        if jitter:
            await sleep(self._jitter_delay())
        with self._rt.lock:
            if self.closed or generation != s.generation:
                return
        try:
            items = await self._fetch(s)
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
                await sleep(1.0)
                self._start_load(s, False)
                return
            self._fail(err)
            return
        with self._rt.lock:
            self._loaded(s, generation, items)
        self._flush()

    async def _fetch(self, s: Sub) -> list[dict[str, Any]]:
        col = self._arealtime.client.db(s.spec["db"]).collection(s.spec["collection"])
        doc = s.spec.get("doc")
        if doc is not None:
            try:
                return [cast(dict[str, Any], await col.get(doc))]
            except BackdError as err:
                if err.status == 404:
                    return []
                raise
        load = s.spec.get("load") or {}
        out: list[dict[str, Any]] = []
        after: str | None = None
        while len(out) < s.limit:
            page = await col.list(
                where=s.spec.get("where"), order_by=load.get("sort"), limit=min(100, s.limit - len(out)), after=after
            )
            out.extend(page.items)
            if not page.has_more or not page.next_cursor:
                break
            after = page.next_cursor
        return out

    def _start_references(self, s: Sub) -> None:
        self._spawn(self._fetch_references(s))

    async def _fetch_references(self, s: Sub) -> None:
        """Reads the referenced ids the view doesn't hold, in one batched read per reference."""
        with self._rt.lock:
            pending = self._take_pending_references()
            if not pending or self.closed:
                return
        for name, (ref, ids) in pending.items():
            col = self._arealtime.client.db(ref.get("db") or s.spec["db"]).collection(ref["collection"])
            for i in range(0, len(ids), 100):
                chunk = ids[i : i + 100]
                try:
                    page = await col.list(where={"id": {"$in": chunk}}, limit=len(chunk))
                except Exception:
                    with self._rt.lock:
                        self._references_failed(name, chunk)
                    continue
                with self._rt.lock:
                    self._references_read(name, ref, page.items)
        self._flush()


class AsyncRealtime(RealtimeBase):
    """Live lists over one stream per client: ``client.realtime``."""

    def __init__(self, client: AsyncClient) -> None:
        super().__init__()
        self.client = client
        self._task: asyncio.Task[None] | None = None
        self._scheduled = False
        client.root._realtimes.append(self)
        client.on_close(self._close)

    def _new_view(self, options: SubscribeOptions) -> AsyncLiveView:
        return AsyncLiveView(self, options)

    def subscribe(self, specs: Any, options: SubscribeOptions | None = None) -> AsyncLiveView:
        asyncio.get_running_loop()  # the view runs in the event loop that is running now
        return cast(AsyncLiveView, super().subscribe(specs, options))

    def schedule(self) -> None:
        """Reconciles the stream with the subscriptions, once per burst of changes."""
        with self.lock:
            if self._scheduled:
                return
            self._scheduled = True
        asyncio.get_running_loop().call_soon(self._reconcile)

    def _reconcile(self) -> None:
        with self.lock:
            self._scheduled = False
            # The set changed (or this is the first subscription): a new stream with the same resume token.
            self.gen += 1
            if self._task is not None:
                self._task.cancel()
                self._task = None
            self.ready = False
            if not self.subs and self.config is None:
                self.running = False
                return
            views = list(self.views)
            for v in views:
                v._status()
            self.running = True
            self._task = asyncio.get_running_loop().create_task(self._connect(self.gen))
        for v in views:
            v._flush()

    def _close(self) -> None:
        """Ends every view and the stream (the client was closed)."""
        with self.lock:
            self.closed = True
            views = list(self.views)
            self.gen += 1
            if self._task is not None:
                self._task.cancel()
            self.running = False
            self.ready = False
        for v in views:
            v.close()

    async def _join(self) -> None:
        """Waits for the tasks to end: nothing is left running when the client is closed."""
        tasks = [t for t in [self._task] if t is not None]
        for v in list(self.views):
            tasks.extend(cast(AsyncLiveView, v)._tasks)
        for t in tasks:
            t.cancel()
        await asyncio.gather(*tasks, return_exceptions=True)

    async def _connect(self, gen: int) -> None:
        attempt = 0
        while gen == self.gen:
            reconnect_now = False
            try:
                with self.lock:
                    body = self.request_body()
                res = await self.client.open_stream(["_realtime"], body)
                attempt = 0
                reconnect_now = await self._read(res, gen)
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
            await sleep(wait)

    async def _read(self, res: httpx.Response, gen: int) -> bool:
        """Reads the stream until it ends. Returns True when the next stream should open at once (the server asked:
        ``reauth``, or a reset that needs the subscription made again)."""
        parser = SSEParser()
        try:
            async for chunk in res.aiter_bytes():
                for ev in parser.feed(chunk):
                    if gen != self.gen:
                        return False
                    again = self.handle(ev)
                    if again is not None:
                        return again
            return False
        except httpx.HTTPError as e:
            raise NetworkError(f"network error: {e}", code="network_error") from e
        finally:
            await res.aclose()
