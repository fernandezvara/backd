from __future__ import annotations

import asyncio
import json
import time
from pathlib import Path
from typing import Any

import httpx
import pytest

import backd
from backd.config_storage import FileConfigStorage

from .helpers import Server, error_response, go, make_client

VALUES = {"maintenance": {"enabled": False}, "features": {"export": {"enabled": True}, "limit": 5}}


class Backend:
    """The `_config` route: answers the paths asked for, with an ETag and a cache_ttl."""

    def __init__(self, ttl: int = 60) -> None:
        self.values: dict[str, Any] = json.loads(json.dumps(VALUES))
        self.ttl = ttl
        self.calls: list[tuple[str | None, str | None]] = []
        self.fail = False

    def __call__(self, req: httpx.Request) -> httpx.Response:
        paths, tag = req.url.params.get("paths"), req.headers.get("if-none-match")
        self.calls.append((paths, tag))
        if self.fail:
            return error_response(503, "unavailable")
        wanted = {p.split(".")[0] for p in paths.split(",")} if paths else set(self.values)
        values = {k: v for k, v in self.values.items() if k in wanted}
        etag = '"' + json.dumps(values, sort_keys=True) + '"'
        if tag == etag:
            return httpx.Response(304, headers={"ETag": etag, "Cache-Control": "private, max-age=120"})
        return httpx.Response(
            200, json={"values": values, "version": "7", "cache_ttl": self.ttl}, headers={"ETag": etag}
        )


def setup(mode: str, ttl: int = 60, **kw: Any) -> tuple[Any, Backend]:
    server, backend = Server(), Backend(ttl)
    server.on("GET /_config", backend)
    return make_client(mode, server, **kw), backend


async def settle(cond: Any) -> None:
    """Waits for something a background revalidation does (a task or a thread)."""
    for _ in range(300):
        if cond():
            return
        await asyncio.sleep(0.01)
    raise AssertionError("it did not happen")


async def test_load_get_and_the_copy(mode: str) -> None:
    c, backend = setup(mode)
    assert c.config.get_sync("features.limit", 99) == 99  # never loaded: the fallback
    assert await go(c.config.load(["features", "maintenance"])) == VALUES
    assert backend.calls == [("features,maintenance", None)]
    assert await go(c.config.get("features.export.enabled")) is True
    assert await go(c.config.get("features.nope", default="d")) == "d"
    assert c.config.get_sync("maintenance.enabled", True) is False  # a False value is a value, not a miss
    assert len(backend.calls) == 1  # fresh: no request
    await go(c.config.load(["maintenance", "features"]))  # the same request, in another order
    assert len(backend.calls) == 1
    view = c.config.view("features,maintenance")
    view["features"]["limit"] = 1000  # a copy: the held values are safe
    assert c.config.get_sync("features.limit") == 5


async def test_a_key_of_a_group_never_loaded_is_fetched_first(mode: str) -> None:
    c, backend = setup(mode)
    assert await go(c.config.get("features.limit")) == 5
    assert backend.calls == [("features", None)]  # the group, not the whole configuration
    assert await go(c.config.get("features.export.enabled")) is True
    assert len(backend.calls) == 1
    assert await go(c.config.get("maintenance.enabled")) is False
    assert backend.calls[-1] == ("maintenance", None)


async def test_everything_covers_every_path(mode: str) -> None:
    c, backend = setup(mode)
    await go(c.config.load())
    assert backend.calls == [(None, None)]
    assert await go(c.config.get("maintenance.enabled")) is False
    assert await go(c.config.get("features.limit")) == 5
    assert len(backend.calls) == 1


async def test_refresh_always_asks_and_a_304_keeps_the_copy(mode: str) -> None:
    c, backend = setup(mode)
    await go(c.config.load(["features"]))
    assert await go(c.config.get("features.limit", refresh=True)) == 5
    assert backend.calls[-1][1] is not None  # sent the tag it held
    await go(c.config.load(["features"], refresh=True))
    assert len(backend.calls) == 3


async def test_a_stale_copy_is_returned_and_revalidated_in_the_background(mode: str) -> None:
    c, backend = setup(mode, ttl=0)
    await go(c.config.load(["features"]))
    backend.values["features"]["limit"] = 9
    changes: list[list[str]] = []
    c.config.on("change", changes.append)
    assert await go(c.config.get("features.limit")) == 5  # the copy held, at once
    await settle(lambda: changes)
    assert changes == [["features.limit"]]
    assert c.config.get_sync("features.limit") == 9
    assert len(backend.calls) == 2


async def test_a_background_failure_keeps_the_copy(mode: str) -> None:
    c, backend = setup(mode, ttl=0)
    await go(c.config.load(["features"]))
    backend.fail = True
    assert await go(c.config.get("features.limit")) == 5
    await settle(lambda: len(backend.calls) == 2)
    assert c.config.get_sync("features.limit") == 5
    with pytest.raises(backd.RetryableError):
        await go(c.config.load(["features"], refresh=True))


async def test_change_events_name_the_keys_that_differ(mode: str) -> None:
    c, backend = setup(mode)
    await go(c.config.load())
    seen: list[list[str]] = []
    stop = c.config.on("change", seen.append)
    c.config.on("change", lambda p: 1 / 0)  # a failing listener breaks nothing
    backend.values["features"]["limit"] = 6
    backend.values["maintenance"] = {"enabled": True, "message": "back soon"}
    await go(c.config.load(refresh=True))
    assert seen == [["features.limit", "maintenance.enabled", "maintenance.message"]]
    stop()
    backend.values["features"]["limit"] = 7
    await go(c.config.load(refresh=True))
    assert len(seen) == 1
    with pytest.raises(TypeError, match="events are"):
        c.config.on("nope", seen.append)


async def test_copies_belong_to_the_caller(mode: str) -> None:
    storage = backd.MemoryStorage()
    c, backend = setup(mode, storage=storage)
    await go(c.config.load(["features"]))
    storage.set("bds_a")
    await go(c.config.load(["features"]))  # a different caller: asked again
    assert len(backend.calls) == 2 and backend.calls[1][1] is None
    storage.set("bds_b")
    assert c.config.get_sync("features.limit") == 5  # sync reads don't look at the caller…
    await go(c.config.load(["features"]))
    assert len(backend.calls) == 3  # …the next load does
    c.config.clear()
    assert c.config.get_sync("features.limit", "gone") == "gone"


async def test_sign_out_forgets_the_copies(mode: str) -> None:
    storage = backd.MemoryStorage()
    storage.set("bds_a")
    c, backend = setup(mode, storage=storage)
    server_routes = c._http  # noqa: F841 - the route table lives on the Server above
    await go(c.config.load(["features"]))
    assert c.config.get_sync("features.limit") == 5
    c._credentials_changed()  # what a sign-in, a sign-out or an expired session does
    assert c.config.get_sync("features.limit", "gone") == "gone"
    assert backend.calls and len(backend.calls) == 1


async def test_copies_survive_a_restart_in_a_config_storage(mode: str, tmp_path: Path) -> None:
    store = FileConfigStorage(tmp_path / "config")
    c, backend = setup(mode, config_storage=store, storage=backd.MemoryStorage())
    await go(c.config.load(["features"]))
    again, backend2 = setup(mode, config_storage=FileConfigStorage(tmp_path / "config"))
    assert await go(again.config.get("features.limit")) == 5
    assert backend2.calls == []  # still fresh: no request at all
    old, _ = setup(mode, ttl=0, config_storage=FileConfigStorage(tmp_path / "old"))
    await go(old.config.load(["features"]))  # saved with a ttl of 0: stale as soon as it is read back
    stale, backend3 = setup(mode, config_storage=FileConfigStorage(tmp_path / "old"))
    assert await go(stale.config.get("features.limit")) == 5
    await settle(lambda: backend3.calls)
    assert backend3.calls[0][1] is not None  # revalidated with the tag it kept
    stale.config.clear()
    assert FileConfigStorage(tmp_path / "old").all_keys() == []
    assert len(backend.calls) == 1


async def test_a_damaged_saved_copy_is_ignored(mode: str) -> None:
    mem = backd.MemoryConfigStorage()
    c, backend = setup(mode, config_storage=mem)
    await go(c.config.load(["features"]))
    (key,) = mem.all_keys()
    mem.set(key, "not json")
    again, backend2 = setup(mode, config_storage=mem)
    assert await go(again.config.get("features.limit")) == 5
    assert len(backend2.calls) == 1
    mem.set(key, json.dumps({"features": {"etag": 1}}))
    third, backend3 = setup(mode, config_storage=mem)
    assert await go(third.config.get("features.limit")) == 5
    assert len(backend3.calls) == 1
    assert len(backend.calls) == 1


def test_file_config_storage(tmp_path: Path) -> None:
    s = FileConfigStorage(tmp_path / "d")
    assert s.get("a:b") is None and s.all_keys() == []
    s.set("a:b", '{"x": 1}')
    s.set("a:c", "2")
    assert s.get("a:b") == '{"x": 1}'
    assert sorted(s.all_keys()) == ["a:b", "a:c"]
    s.remove("a:b")
    s.remove("a:b")
    assert s.all_keys() == ["a:c"]
    assert oct((tmp_path / "d").stat().st_mode & 0o777) == "0o700"
    assert time.time() > 0
