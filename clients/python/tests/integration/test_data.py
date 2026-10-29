"""Documents against a real backd and MongoDB: create, read, patch, the rules, the query language, batch."""

from __future__ import annotations

from typing import Any

import pytest

import backd

from .conftest import API_KEY, Users, go


async def collect(it: Any) -> list[Any]:
    if hasattr(it, "__aiter__"):
        return [x async for x in it]
    return list(it)


async def test_crud_rules_and_where(users: Users) -> None:
    client, _ = await users.sign_up("crud")
    posts = client.db("app").collection("posts")

    post = await go(posts.create({"title": "hello", "slug": "hello", "published": True}))
    assert (await go(posts.get(post["id"])))["title"] == "hello"

    # The update rule forbids touching `status`.
    with pytest.raises(backd.ForbiddenError):
        await go(posts.patch(post["id"], {"status": "review"}))

    mine = await go(posts.list(where={"title": "hello", "published": True}, limit=10))
    assert post["id"] in [d["id"] for d in mine.items]

    # If-Match: a stale version is refused.
    with pytest.raises(backd.VersionMismatchError):
        await go(posts.patch(post["id"], {"title": "x"}, if_match=post["_meta"]["version"] + 5))
    updated = await go(posts.patch(post["id"], {"title": "hello again"}, if_match=post["_meta"]["version"]))
    assert updated["title"] == "hello again"

    # Another user cannot read an unpublished post they do not own.
    other, _ = await users.sign_up("other")
    await go(posts.patch(post["id"], {"published": False}))
    with pytest.raises(backd.BackdError):
        await go(other.db("app").collection("posts").get(post["id"]))

    # Deleting is for administrators (the delete rule); the admin key of the tests is one.
    with pytest.raises(backd.ForbiddenError):
        await go(posts.delete(post["id"]))
    admin = users.anonymous(api_key=API_KEY)
    await go(admin.db("app").collection("posts").delete(post["id"]))
    with pytest.raises(backd.NotFoundError):
        await go(posts.get(post["id"]))


async def test_iterate_follows_the_cursor(users: Users) -> None:
    client, _ = await users.sign_up("pages")
    posts = client.db("app").collection("posts")
    for i in range(5):
        await go(posts.create({"title": f"page-{i}", "slug": f"page-{i}", "published": True}))
    seen = await collect(posts.iterate(where={"title": {"$startsWith": "page-"}}, order_by="title", limit=2))
    mine = [d for d in seen if d["title"].startswith("page-")]
    assert len(mine) >= 5


async def test_batch_applies_create_and_patch_atomically(users: Users) -> None:
    client, _ = await users.sign_up("batch")
    db = client.db("app")
    posts = db.collection("posts")
    stock = await go(posts.create({"title": "stock", "published": True}))
    results = await go(
        db.batch(
            [
                {"op": "create", "collection": "posts", "document": {"title": "second", "published": True}},
                {"op": "patch", "collection": "posts", "id": stock["id"], "patch": {"title": "stock-updated"}},
            ]
        )
    )
    assert len(results) == 2 and results[0]["title"] == "second"
    assert (await go(posts.get(stock["id"])))["title"] == "stock-updated"

    # One bad operation and none applies.
    with pytest.raises(backd.BackdError):
        await go(
            db.batch(
                [
                    {"op": "patch", "collection": "posts", "id": stock["id"], "patch": {"title": "never"}},
                    {"op": "patch", "collection": "posts", "id": "does-not-exist", "patch": {"title": "x"}},
                ]
            )
        )
    assert (await go(posts.get(stock["id"])))["title"] == "stock-updated"


async def test_the_revalidation_cache_costs_a_304(users: Users) -> None:
    client, _ = await users.sign_up("cache")
    posts = client.db("app").collection("posts")
    post = await go(posts.create({"title": "cached", "published": True}))
    await go(posts.get(post["id"]))
    await go(posts.get(post["id"]))
    assert client.cache_stats().hits >= 1
