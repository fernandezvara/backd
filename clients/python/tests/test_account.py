from __future__ import annotations

import asyncio
from pathlib import Path
from typing import Any

import httpx
import pytest

import backd

from .helpers import Server, error_response, go, make_client


def setup(mode: str, **kw: Any) -> tuple[Any, Server, backd.MemoryStorage]:
    server, storage = Server(), backd.MemoryStorage()
    storage.set("bds_x")
    return make_client(mode, server, storage=storage, **kw), server, storage


async def settle(cond: Any) -> None:
    for _ in range(300):
        if cond():
            return
        await asyncio.sleep(0.01)
    raise AssertionError("it did not happen")


async def test_consents(mode: str) -> None:
    c, server, _ = setup(mode)
    server.on(
        "GET /_consents",
        {
            "items": [
                {"name": "terms", "required": True, "current": {"version": "2", "reaccept": False}, "locales": ["en"]}
            ]
        },
    )
    server.on(
        "GET /_consents/terms/2",
        {"name": "terms", "version": "2", "locale": "en", "text": "# T", "hash": "h", "current": True},
    )
    state = {"consents": {"terms": "2"}, "to_accept": []}
    server.on("GET /_auth/consents", {**state, "history": []})
    server.on("POST /_auth/consents", state)
    server.on("DELETE /_auth/consents/marketing", state)
    assert (await go(c.consents.current()))[0]["name"] == "terms"
    assert "authorization" not in server.last().headers  # a person reads the texts before having an account
    assert (await go(c.consents.text("terms", "2", "es")))["hash"] == "h"
    assert server.last().url.params["locale"] == "es" and "authorization" not in server.last().headers
    await go(c.consents.text("terms", "2"))
    assert not server.last().url.params
    assert (await go(c.consents.state()))["history"] == []
    assert server.last().headers["authorization"] == "Bearer bds_x"
    await go(c.consents.accept({"terms": "2"}))
    assert server.body() == {"consents": {"terms": "2"}}
    await go(c.consents.withdraw("marketing"))
    server.on("DELETE /_auth/consents/terms", lambda r: error_response(409, "consent_required_for_account"))
    with pytest.raises(backd.ConflictError):
        await go(c.consents.withdraw("terms"))


async def test_push_devices(mode: str) -> None:
    c, server, _ = setup(mode)
    device = {"id": "d1", "platform": "android", "name": "Pixel", "created_at": "t", "last_seen": "t"}
    server.on("POST /_auth/devices", device)
    server.on("GET /_auth/devices", {"items": [device]})
    server.on("DELETE /_auth/devices/d1", None, 204)
    server.on("DELETE /_auth/devices/d2", None, 204)
    assert c.push.device_id is None and await go(c.push.unregister()) is False
    out = await go(c.push.register("android", "fcm-token", name="Pixel"))
    assert out["id"] == "d1" and c.push.device_id == "d1"
    assert server.body() == {"platform": "android", "token": "fcm-token", "name": "Pixel"}
    await go(c.push.register("ios", "apns"))
    assert server.body() == {"platform": "ios", "token": "apns"}
    assert [d["id"] for d in await go(c.push.devices())] == ["d1"]
    assert await go(c.push.unregister("d2")) is True and c.push.device_id == "d1"
    assert await go(c.push.unregister()) is True and c.push.device_id is None


async def test_the_device_goes_with_the_session(mode: str) -> None:
    c, server, _ = setup(mode)
    server.on("POST /_auth/devices", {"id": "d1", "platform": "web", "name": None, "created_at": "t", "last_seen": "t"})
    server.on("POST /_auth/logout", None, 204)
    await go(c.push.register("web", "{}"))
    assert c.push.device_id == "d1"
    await go(c.auth.logout())
    assert c.push.device_id is None


async def test_register_on_launch(mode: str) -> None:
    c, server, storage = setup(mode)
    server.on(
        "POST /_auth/devices", {"id": "d1", "platform": "android", "name": None, "created_at": "t", "last_seen": "t"}
    )
    server.on(
        "POST /_auth/login", {"token": "bds_new", "session_id": "s", "expires_at": "2030-01-01T00:00:00Z", "user": {}}
    )
    errors: list[Exception] = []
    tokens: list[Any] = [{"platform": "android", "token": "t1", "name": "P"}]

    async def aget() -> Any:
        return tokens.pop(0) if tokens else None

    def sget() -> Any:
        return tokens.pop(0) if tokens else None

    stop = c.push.register_on_launch(aget if mode == "async" else sget, errors.append)
    await settle(lambda: c.push.device_id == "d1")  # signed in already: registered at once
    assert server.body() == {"platform": "android", "token": "t1", "name": "P"}

    tokens.append({"platform": "android", "token": "t2"})
    n = len(server.calls)
    await go(c.auth.login("a@x.test", "dev-p4ssw0rd!"))  # and again after a sign-in
    await settle(lambda: len(server.calls) >= n + 2)
    assert server.body() == {"platform": "android", "token": "t2"}
    assert errors == []

    stop()
    n = len(server.calls)
    tokens.append({"platform": "android", "token": "t3"})
    await go(c.auth.login("a@x.test", "dev-p4ssw0rd!"))
    await asyncio.sleep(0.05)
    assert len(server.calls) == n + 1  # only the login: stopped
    storage.remove()


async def test_register_on_launch_reports_failures_and_waits_for_a_token(mode: str) -> None:
    c, server, _ = setup(mode)
    server.on("POST /_auth/devices", lambda r: error_response(500, "boom"))
    errors: list[Exception] = []
    tokens: list[Any] = [None, {"platform": "ios", "token": "t"}]

    async def aget() -> Any:
        return tokens.pop(0)

    def sget() -> Any:
        return tokens.pop(0)

    c.push.register_on_launch(aget if mode == "async" else sget, errors.append)
    await settle(lambda: len(tokens) == 1)
    assert server.calls == []  # no token yet: nothing registered, nothing failed
    assert errors == []


async def test_export_flow(mode: str, monkeypatch: pytest.MonkeyPatch) -> None:
    c, server, _ = setup(mode)
    import backd._async.account as aa
    import backd._sync.account as sa

    async def asleep(s: float) -> None:
        return None

    monkeypatch.setattr(aa, "sleep", asleep)
    monkeypatch.setattr(sa, "sleep", lambda s: None)
    states = iter(["queued", "running", "ready"])
    server.on("POST /_auth/me/export", {"id": "e1", "status": "queued", "created_at": "t"}, 202)
    server.on(
        "GET /_auth/me/exports/e1",
        lambda r: httpx.Response(200, json={"id": "e1", "status": next(states), "created_at": "t"}),
    )
    server.on("GET /_auth/me/exports", {"items": [{"id": "e1", "status": "ready", "created_at": "t"}]})
    c._proof = ("rt", 4e9)
    assert (await go(c.account.request_export()))["id"] == "e1"
    assert server.last().headers["x-backd-reauth"] == "rt"
    seen: list[str] = []
    ready = await go(c.account.wait_for_export("e1", on_progress=lambda e: seen.append(e["status"])))
    assert ready["status"] == "ready" and seen == ["queued", "running", "ready"]
    assert [e["id"] for e in await go(c.account.exports())] == ["e1"]


async def test_export_that_fails_or_never_ends(mode: str, monkeypatch: pytest.MonkeyPatch) -> None:
    c, server, _ = setup(mode)
    import backd._async.account as aa
    import backd._sync.account as sa

    async def asleep(s: float) -> None:
        return None

    monkeypatch.setattr(aa, "sleep", asleep)
    monkeypatch.setattr(sa, "sleep", lambda s: None)
    for status, code in [("failed", "export_failed"), ("expired", "export_expired")]:
        server.on("GET /_auth/me/exports/e1", {"id": "e1", "status": status, "created_at": "t"})
        with pytest.raises(backd.BackdError) as ei:
            await go(c.account.wait_for_export("e1"))
        assert ei.value.code == code
    server.on("GET /_auth/me/exports/e1", {"id": "e1", "status": "running", "created_at": "t"})
    with pytest.raises(backd.BackdError) as ei2:
        await go(c.account.wait_for_export("e1", poll_interval=5, timeout=1))
    assert ei2.value.code == "export_timeout"


async def test_download_by_link_and_through_the_api(mode: str, tmp_path: Path) -> None:
    c, server, _ = setup(mode)
    server.on(
        "GET /_auth/me/exports/e1/download",
        httpx.Response(200, json={"url": "https://bucket.test/zip", "expires_at": "2030-01-01T00:00:00Z"}),
    )
    server.on("GET /zip", httpx.Response(200, content=b"PK-by-link"))
    dl = await go(c.account.download_export("e1"))
    assert (dl.url, dl.expires_at) == ("https://bucket.test/zip", "2030-01-01T00:00:00Z")
    assert server.last().headers["accept"] == "application/json"
    assert await go(dl.read()) == b"PK-by-link"
    assert "authorization" not in server.last().headers  # the storage never sees backd's credentials
    out = tmp_path / "export.zip"
    assert await go(dl.save(out)) == 10 and out.read_bytes() == b"PK-by-link"
    with (tmp_path / "f.zip").open("wb") as f:
        await go(dl.save(f))
    assert (tmp_path / "f.zip").read_bytes() == b"PK-by-link"

    server.on("GET /zip", httpx.Response(403))
    with pytest.raises(backd.BackdError) as ei:
        await go((await go(c.account.download_export("e1"))).read())
    assert (ei.value.status, ei.value.code) == (403, "download_failed")

    server.on(
        "GET /_auth/me/exports/e2/download",
        httpx.Response(200, content=b"PK-direct", headers={"Content-Type": "application/zip"}),
    )
    direct = await go(c.account.download_export("e2"))
    assert direct.url is None and await go(direct.read()) == b"PK-direct"

    server.on("GET /_auth/me/exports/e3/download", lambda r: error_response(409, "export_not_ready"))
    with pytest.raises(backd.ConflictError):
        await go(c.account.download_export("e3"))
