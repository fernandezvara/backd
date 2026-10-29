"""Realtime against a real backd: a live view sees the create a moment after it happens, and closing leaves nothing
running."""

from __future__ import annotations

import asyncio
import threading
import time
from typing import Any

from .conftest import Users, go


async def wait_for(cond: Any, what: str, seconds: float = 15) -> None:
    deadline = time.monotonic() + seconds
    while not cond():
        assert time.monotonic() < deadline, f"{what} never happened"
        await asyncio.sleep(0.05)


async def test_a_live_view_delivers_a_new_document(users: Users, mode: str) -> None:
    client, _ = await users.sign_up("live")
    posts = client.db("app").collection("posts")
    before = set(threading.enumerate())

    view = posts.subscribe(where={"published": True})
    errors: list[object] = []
    view.on("error", errors.append)
    await go(view.wait_ready(15))

    created = await go(posts.create({"title": "live-post", "published": True}))
    await wait_for(lambda: any(d["id"] == created["id"] for d in view.items), "the new document arriving")
    assert not errors

    patched = await go(posts.patch(created["id"], {"title": "live-post-2"}))
    await wait_for(lambda: any(d.get("title") == "live-post-2" for d in view.items), "the update arriving")
    await go(posts.patch(created["id"], {"published": False}))  # it stops matching: it leaves the view
    await wait_for(lambda: all(d["id"] != patched["id"] for d in view.items), "the removal")

    view.close()
    await go(client.aclose() if mode == "async" else client.close())
    if mode == "sync":
        await wait_for(
            lambda: not [t for t in set(threading.enumerate()) - before if t.name.startswith("backd-")],
            "the threads ending",
        )
