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
