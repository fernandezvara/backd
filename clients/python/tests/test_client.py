from __future__ import annotations

import json
from typing import Any

import httpx
import pytest

import backd
from backd import Retry

from .helpers import URL, error_response, go, json_response, make_client


async def test_get_builds_the_address_and_parses_json(mode: str) -> None:
    seen: list[httpx.Request] = []

    def handler(req: httpx.Request) -> httpx.Response:
        seen.append(req)
        return json_response(200, {"ok": True})

    c = make_client(mode, handler, url=URL + "/")
    res = await go(c.request("GET", ["main", "a b", "x/y"], query={"limit": 5, "flag": True, "skip": None}))
    assert res.status == 200
    assert res.data == {"ok": True}
    r = seen[0]
    assert str(r.url) == f"{URL}/v1/shelf/main/a%20b/x%2Fy?limit=5&flag=true"
    assert r.headers["accept"] == "application/json"
    assert "authorization" not in r.headers
    await go(c.aclose() if mode == "async" else c.close())


async def test_json_body_and_raw_content(mode: str) -> None:
    seen: list[httpx.Request] = []

    def handler(req: httpx.Request) -> httpx.Response:
        seen.append(req)
        return httpx.Response(204)

    c = make_client(mode, handler, headers={"X-App": "shelf"})
    res = await go(c.request("POST", ["main", "posts"], body={"title": "é"}, headers={"X-One": "1"}))
    assert res.data is None
    assert json.loads(seen[0].content) == {"title": "é"}
    assert seen[0].headers["content-type"] == "application/json"
    assert seen[0].headers["x-app"] == "shelf"
    assert seen[0].headers["x-one"] == "1"
    await go(c.request("PUT", ["f"], content=b"\x00\x01"))
    assert seen[1].content == b"\x00\x01"
    assert seen[1].headers["content-type"] == "application/octet-stream"
    await go(c.request("PATCH", ["d"], body={"a": 1}, content_type="application/merge-patch+json"))
    assert seen[2].headers["content-type"] == "application/merge-patch+json"


async def test_credentials(mode: str) -> None:
    seen: list[httpx.Request] = []

    def handler(req: httpx.Request) -> httpx.Response:
        seen.append(req)
        return json_response(200, {})

    key = make_client(mode, handler, api_key="bdk_secret")
    assert key.has_api_key
    await go(key.request("GET", ["a"]))
    assert seen[-1].headers["authorization"] == "Bearer bdk_secret"
    await go(key.request("GET", ["a"], auth=False))
    assert "authorization" not in seen[-1].headers

    storage = backd.MemoryStorage()
    session = make_client(mode, handler, storage=storage)
    assert not session.has_api_key
    await go(session.request("GET", ["a"]))
    assert "authorization" not in seen[-1].headers
    storage.set("tok")
    await go(session.request("GET", ["a"]))
    assert seen[-1].headers["authorization"] == "Bearer tok"


async def test_raw_answer_is_returned_unread(mode: str) -> None:
    c = make_client(mode, lambda r: httpx.Response(200, content=b"bytes", headers={"Content-Type": "image/png"}))
    res = await go(c.request("GET", ["files"], raw=True))
    assert res.data is None
    assert res.response is not None
    assert res.headers["content-type"] == "image/png"
    body = await res.response.aread() if mode == "async" else res.response.read()
    assert body == b"bytes"


@pytest.mark.parametrize(
    ("status", "code", "cls"), [(404, "not_found", backd.NotFoundError), (412, "x", backd.VersionMismatchError)]
)
async def test_failures_raise_typed_errors(mode: str, status: int, code: str, cls: type) -> None:
    c = make_client(mode, lambda r: error_response(status, code))
    with pytest.raises(backd.BackdError) as ei:
        await go(c.request("GET", ["main", "x"]))
    assert type(ei.value) is cls
    assert ei.value.path == "main/x"


async def test_network_failure(mode: str) -> None:
    def boom(req: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("refused", request=req)

    c = make_client(mode, boom)
    with pytest.raises(backd.NetworkError) as ei:
        await go(c.request("GET", ["a"]))
    assert (ei.value.status, ei.value.code) == (0, "network_error")

    def slow(req: httpx.Request) -> httpx.Response:
        raise httpx.ReadTimeout("slow", request=req)

    with pytest.raises(backd.NetworkError) as ei2:
        await go(make_client(mode, slow).request("GET", ["a"]))
    assert ei2.value.code == "timeout"


class Slept:
    def __init__(self, monkeypatch: pytest.MonkeyPatch) -> None:
        self.waits: list[float] = []

        def record(s: float) -> None:
            self.waits.append(s)

        async def arecord(s: float) -> None:
            self.waits.append(s)

        import backd._async.client as ac
        import backd._sync.client as sc

        monkeypatch.setattr(ac, "sleep", arecord)
        monkeypatch.setattr(sc, "sleep", record)


async def test_retries_only_safe_calls_and_honours_retry_after(mode: str, monkeypatch: pytest.MonkeyPatch) -> None:
    slept = Slept(monkeypatch)
    calls: list[str] = []

    def handler(req: httpx.Request) -> httpx.Response:
        calls.append(req.method)
        if len(calls) < 3:
            return error_response(429, "rate_limited", **{"Retry-After": "2"})
        return json_response(200, {"n": len(calls)})

    c = make_client(mode, handler, retry=Retry(attempts=3, max_delay=1.5))
    res = await go(c.request("GET", ["a"]))
    assert res.data == {"n": 3}
    assert slept.waits == [1.5, 1.5]  # Retry-After 2, capped by max_delay

    calls.clear()
    with pytest.raises(backd.RetryableError):
        await go(c.request("POST", ["a"], body={}))  # not safe: tried once
    assert calls == ["POST"]

    calls.clear()
    res = await go(c.request("POST", ["a"], body={}, headers={"If-Match": '"1"'}))  # safe: conditional
    assert len(calls) == 3
    calls.clear()
    await go(c.request("POST", ["a"], body={}, headers={"Idempotency-Key": "k"}))
    assert len(calls) == 3


async def test_retries_stop_at_the_limit_and_back_off_exponentially(mode: str, monkeypatch: pytest.MonkeyPatch) -> None:
    slept = Slept(monkeypatch)
    n = 0

    def handler(req: httpx.Request) -> httpx.Response:
        nonlocal n
        n += 1
        return error_response(503, "unavailable")

    c = make_client(mode, handler, retry=Retry(attempts=2))
    with pytest.raises(backd.RetryableError):
        await go(c.request("GET", ["a"]))
    assert n == 3
    assert slept.waits == [0.5, 1.0]
    n = 0
    with pytest.raises(backd.RetryableError):
        await go(c.request("GET", ["a"], retry=Retry(attempts=0)))  # per call override
    assert n == 1


async def test_stale_types_are_sent_and_reported_once(mode: str) -> None:
    seen: list[httpx.Request] = []

    def handler(req: httpx.Request) -> httpx.Response:
        seen.append(req)
        return json_response(200, {}, **{"X-Backd-Types-Stale": "true"})

    stale: list[int] = []
    c = make_client(mode, handler, types="abc123", on_stale_types=lambda: stale.append(1))
    await go(c.request("GET", ["a"]))
    await go(c.request("GET", ["a"]))
    assert seen[0].headers["x-backd-types"] == "abc123"
    assert stale == [1]

    quiet = make_client(mode, handler)  # no fingerprint: nothing sent, nothing reported
    await go(quiet.request("GET", ["a"]))
    assert "x-backd-types" not in seen[-1].headers


async def test_stale_types_warn_in_the_log_by_default(mode: str, caplog: pytest.LogCaptureFixture) -> None:
    c = make_client(mode, lambda r: json_response(200, {}, **{"X-Backd-Types-Stale": "true"}), types="abc")
    with caplog.at_level("WARNING", logger="backd"):
        await go(c.request("GET", ["a"]))
    assert "out of date" in caplog.text


async def test_a_refused_session_calls_the_expiry_hook(mode: str) -> None:
    storage = backd.MemoryStorage()
    storage.set("tok")
    c = make_client(mode, lambda r: error_response(401, "unauthenticated"), storage=storage)
    expired: list[int] = []

    async def aexpired() -> None:
        expired.append(1)

    def expired_sync() -> None:
        expired.append(1)

    c._expired = aexpired if mode == "async" else expired_sync
    with pytest.raises(backd.AuthenticationError):
        await go(c.request("GET", ["a"]))
    assert expired == [1]
    with pytest.raises(backd.AuthenticationError):
        await go(c.request("GET", ["a"], expire=False))
    with pytest.raises(backd.AuthenticationError):
        await go(c.request("GET", ["a"], auth=False))  # no session was sent
    assert expired == [1]


async def test_invalid_json_in_a_success_answer(mode: str) -> None:
    c = make_client(mode, lambda r: httpx.Response(200, content=b"<html>"))
    with pytest.raises(backd.BackdError, match="not the JSON expected"):
        await go(c.request("GET", ["a"]))


async def test_options_are_checked(mode: str) -> None:
    with pytest.raises(TypeError, match="url"):
        make_client(mode, lambda r: httpx.Response(200), url="")
    with pytest.raises(TypeError, match="realm"):
        make_client(mode, lambda r: httpx.Response(200), realm="")


async def test_context_manager_closes_and_runs_the_closers() -> None:
    closed: list[str] = []
    client_kw: dict[str, Any] = {
        "url": URL,
        "realm": "shelf",
        "transport": httpx.MockTransport(lambda r: httpx.Response(200)),
    }
    async with backd.AsyncClient(**client_kw) as a:
        a.on_close(lambda: closed.append("a"))
    assert closed == ["a"]
    a.on_close(lambda: closed.append("late"))  # already closed: runs at once
    assert closed == ["a", "late"]
    await a.aclose()  # twice is fine
    with backd.Client(**client_kw) as s:
        s.on_close(lambda: closed.append("s"))
    assert closed[-1] == "s"
    s.close()
