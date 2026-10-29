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
