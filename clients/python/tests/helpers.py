"""Tests run each case against both clients: the same body, ``await``ing only what is awaitable."""

from __future__ import annotations

import inspect
from collections.abc import Callable
from typing import Any

import httpx

import backd

Handler = Callable[[httpx.Request], httpx.Response]

URL = "http://backd.test"


def make_client(mode: str, handler: Handler, **kw: Any) -> Any:
    """A ``Client`` or an ``AsyncClient`` whose transport is ``handler``."""
    kw.setdefault("url", URL)
    kw.setdefault("realm", "shelf")
    kw["transport"] = httpx.MockTransport(handler)
    return backd.AsyncClient(**kw) if mode == "async" else backd.Client(**kw)


async def go(value: Any) -> Any:
    """The result of a call of either client."""
    return await value if inspect.isawaitable(value) else value


def json_response(status: int, body: Any = None, **headers: str) -> httpx.Response:
    return (
        httpx.Response(status, json=body, headers=headers)
        if body is not None
        else httpx.Response(status, headers=headers)
    )


def error_response(status: int, code: str, message: str = "nope", **headers: str) -> httpx.Response:
    return httpx.Response(status, json={"error": {"code": code, "message": message}}, headers=headers)


class Server:
    """A fake backd: routes by ``"METHOD /path"`` (after ``/v1/shelf``), remembers what it was asked."""

    def __init__(self) -> None:
        self.routes: dict[str, Callable[[httpx.Request], httpx.Response] | httpx.Response | Any] = {}
        self.calls: list[httpx.Request] = []

    def on(self, route: str, answer: Any = None, status: int = 200) -> None:
        """``answer``: a JSON body, a response, or a function of the request."""
        self.routes[route] = answer if callable(answer) or isinstance(answer, httpx.Response) else (status, answer)

    def __call__(self, req: httpx.Request) -> httpx.Response:
        self.calls.append(req)
        path = req.url.path.removeprefix("/v1/shelf")
        route = self.routes.get(f"{req.method} {path}")
        if route is None:
            return error_response(404, "not_found", f"no route {req.method} {path}")
        if isinstance(route, httpx.Response):
            return route
        if callable(route):
            return route(req)
        status, body = route
        return json_response(status, body)

    def last(self) -> httpx.Request:
        return self.calls[-1]

    def body(self, index: int = -1) -> Any:
        import json

        return json.loads(self.calls[index].content)

    def paths(self) -> list[str]:
        return [f"{r.method} {r.url.path.removeprefix('/v1/shelf')}" for r in self.calls]


class Feed(httpx.SyncByteStream, httpx.AsyncByteStream):
    """The body of a stream the test writes to as it goes (server-sent events), for either client."""

    def __init__(self) -> None:
        self._chunks: list[bytes] = []
        self.closed = False

    def push(self, text: str) -> None:
        self._chunks.append(text.encode())

    def event(self, name: str, data: Any = None, id: str | None = None) -> None:
        import json

        head = f"id: {id}\n" if id else ""
        self.push(f"{head}event: {name}\ndata: {json.dumps({} if data is None else data)}\n\n")

    def end(self) -> None:
        self.closed = True

    def __iter__(self) -> Any:
        import time

        while True:
            if self._chunks:
                yield self._chunks.pop(0)
            elif self.closed:
                return
            else:
                time.sleep(0.002)

    async def __aiter__(self) -> Any:
        import asyncio

        while True:
            if self._chunks:
                yield self._chunks.pop(0)
            elif self.closed:
                return
            else:
                await asyncio.sleep(0.002)

    def close(self) -> None:
        self.closed = True

    async def aclose(self) -> None:
        self.closed = True


class Streams:
    """A fake ``POST /_realtime``: every connection is a new :class:`Feed`, and the requests are remembered."""

    def __init__(self, server: Server) -> None:
        self.feeds: list[Feed] = []
        self.bodies: list[Any] = []
        self.fail: httpx.Response | None = None
        #: When set, ``fail`` is the answer only to the requests it says yes to (a server refuses what is wrong).
        self.fail_when: Any = None
        server.on("POST /_realtime", self._open)

    def _open(self, req: httpx.Request) -> httpx.Response:
        import json

        body = json.loads(req.content)
        self.bodies.append(body)
        if self.fail is not None and (self.fail_when is None or self.fail_when(body)):
            return self.fail
        feed = Feed()
        self.feeds.append(feed)
        return httpx.Response(200, headers={"Content-Type": "text/event-stream"}, stream=feed)

    @property
    def feed(self) -> Feed:
        return self.feeds[-1]
