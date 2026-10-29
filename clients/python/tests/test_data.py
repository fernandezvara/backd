from __future__ import annotations

import json
from typing import Any

import httpx
import pytest

import backd

from .helpers import Server, error_response, go, json_response, make_client


async def collect(it: Any) -> list[Any]:
    """Everything a generator or an async generator of either client yields."""
    if hasattr(it, "__aiter__"):
        return [x async for x in it]
    return list(it)


def posts(mode: str) -> tuple[Any, Server]:
    server = Server()
    return make_client(mode, server).db("main").collection("posts"), server


async def test_list_sends_every_parameter(mode: str) -> None:
    posts_, server = posts(mode)
    server.on(
        "GET /main/posts",
        {"items": [{"id": "1", "_meta": {}}], "limit": 5, "skip": 2, "has_more": True, "next_cursor": "c1", "total": 9},
    )
    page = await go(
        posts_.list(
            where={"price": backd.lt(20), "tags": backd.in_("a", "b")},
            order_by=["-_meta.created_at", "title"],
            limit=5,
            skip=2,
            count=True,
            deleted="include",
            file_links=True,
            expand=backd.expand("author"),
            language="es",
        )
    )
    q = server.last().url.params
    assert json.loads(q["where"]) == {"price": {"$lt": 20}, "tags": {"$in": ["a", "b"]}}
    assert q["order_by"] == "-_meta.created_at,title"
    assert (q["limit"], q["skip"], q["count"], q["deleted"], q["file_links"], q["expand"], q["language"]) == (
        "5", "2", "true", "include", "true", "author", "es",
    )  # fmt: skip
    assert (page.limit, page.skip, page.has_more, page.next_cursor, page.total) == (5, 2, True, "c1", 9)
    assert len(page) == 1 and [d["id"] for d in page] == ["1"]


async def test_list_sends_nothing_that_was_not_asked(mode: str) -> None:
    posts_, server = posts(mode)
    server.on("GET /main/posts", {"items": [], "limit": 20, "skip": 0, "has_more": False})
    page = await go(posts_.list(where='{"a":1}'))
    assert dict(server.last().url.params) == {"where": '{"a":1}'}
    assert page.next_cursor is None and page.total is None


async def test_iterate_follows_cursors(mode: str) -> None:
    posts_, server = posts(mode)

    def handler(req: httpx.Request) -> httpx.Response:
        after = req.url.params.get("after")
        assert "skip" not in req.url.params or after is None
        pages = {None: (["a", "b"], "c1"), "c1": (["c", "d"], "c2"), "c2": (["e"], None)}
        ids, nxt = pages[after]
        body = {"items": [{"id": i} for i in ids], "limit": 2, "skip": 0, "has_more": nxt is not None}
        if nxt:
            body["next_cursor"] = nxt
        return json_response(200, body)

    server.on("GET /main/posts", handler)
    docs = await collect(posts_.iterate(limit=2, where={"x": 1}))
    assert [d["id"] for d in docs] == list("abcde")
    assert [r.url.params["limit"] for r in server.calls] == ["2", "2", "2"]
    assert all("where" in r.url.params for r in server.calls)


async def test_iterate_falls_back_to_the_offset_without_a_cursor(mode: str) -> None:
    posts_, server = posts(mode)

    def handler(req: httpx.Request) -> httpx.Response:
        skip = int(req.url.params.get("skip", 0))
        items = [{"id": str(i)} for i in range(skip, min(skip + 2, 5))]
        return json_response(200, {"items": items, "limit": 2, "skip": skip, "has_more": skip + 2 < 5})

    server.on("GET /main/posts", handler)
    docs = await collect(posts_.iterate(limit=2))
    assert [d["id"] for d in docs] == ["0", "1", "2", "3", "4"]
    assert [r.url.params["skip"] for r in server.calls] == ["0", "2", "4"]


async def test_iterate_from_a_cursor_that_cannot_continue_fails(mode: str) -> None:
    posts_, server = posts(mode)
    server.on("GET /main/posts", {"items": [{"id": "1"}], "limit": 1, "skip": 0, "has_more": True})
    with pytest.raises(RuntimeError, match="next_cursor"):
        await collect(posts_.iterate(limit=1, after="c0"))


async def test_get_and_its_options(mode: str) -> None:
    posts_, server = posts(mode)
    server.on("GET /main/posts/p 1", {"id": "p 1", "title": "T"})
    doc = await go(posts_.get("p 1", deleted="only", file_links=True, expand="author(name)"))
    assert doc["title"] == "T"
    assert server.last().url.raw_path.endswith(
        b"/main/posts/p%201?deleted=only&file_links=true&expand=author%28name%29"
    )
    server.on("GET /main/posts/missing", lambda r: error_response(404, "not_found"))
    with pytest.raises(backd.NotFoundError):
        await go(posts_.get("missing"))


async def test_writes(mode: str) -> None:
    posts_, server = posts(mode)
    server.on("POST /main/posts", {"id": "n1", "title": "A"}, 201)
    server.on("PUT /main/posts/n1", {"id": "n1", "title": "B"})
    server.on("PATCH /main/posts/n1", {"id": "n1", "title": "C"})
    server.on("DELETE /main/posts/n1", None, 204)
    server.on("POST /main/posts/n1/restore", {"id": "n1"})

    await go(posts_.create({"title": "A"}, idempotency_key="k1"))
    assert server.body() == {"title": "A"}
    assert server.last().headers["idempotency-key"] == "k1"
    assert not server.last().url.params

    await go(posts_.replace("n1", {"title": "B"}, if_match=3))
    assert server.last().headers["if-match"] == '"3"'
    assert server.body() == {"title": "B"}

    await go(posts_.patch("n1", {"title": "C", "old": None}, if_match='"2", "3"'))
    assert server.last().headers["content-type"] == "application/merge-patch+json"
    assert server.last().headers["if-match"] == '"2", "3"'
    assert server.body() == {"title": "C", "old": None}

    assert await go(posts_.delete("n1", purge=True, if_match="*")) is None
    assert server.last().url.params["purge"] == "true" and server.last().headers["if-match"] == "*"
    await go(posts_.delete("n1"))
    assert "purge" not in server.last().url.params and "if-match" not in server.last().headers

    await go(posts_.restore("n1"))
    assert server.paths()[-1] == "POST /main/posts/n1/restore"


async def test_if_match_conflicts_and_custom_ids(mode: str) -> None:
    posts_, server = posts(mode)
    server.on("PUT /main/posts/n1", lambda r: error_response(412, "version_mismatch", "stale"))
    with pytest.raises(backd.VersionMismatchError):
        await go(posts_.replace("n1", {}, if_match=1))
    server.on("POST /main/posts", lambda r: error_response(409, "id_taken"))
    with pytest.raises(backd.IdTakenError):
        await go(posts_.create({"id": "mine"}))
    with pytest.raises(backd.ConflictError):  # an IdTakenError is a ConflictError
        await go(posts_.create({"id": "mine"}))
    server.on("POST /main/posts", lambda r: error_response(400, "id_required"))
    with pytest.raises(backd.InvalidIdError):
        await go(posts_.create({}))


async def test_team_and_org_scopes(mode: str) -> None:
    posts_, server = posts(mode)
    server.on("GET /main/posts", {"items": [], "limit": 20, "skip": 0, "has_more": False})
    server.on("GET /main/posts/_aggregate", {"groups": [], "total_groups": 0})
    server.on("POST /main/posts", {"id": "1"}, 201)
    team = posts_.in_team("t1")
    await go(team.list())
    assert server.last().url.params["team"] == "t1"
    await go(team.aggregate(group_by="a"))
    assert server.last().url.params["team"] == "t1"
    await go(team.create({}))
    assert server.last().url.params["team"] == "t1"
    await go(team.create({}, team="t2"))  # an explicit team wins
    assert server.last().url.params["team"] == "t2"
    await go(posts_.in_org("o1").create({}))
    assert dict(server.last().url.params) == {"org": "o1"}
    await go(posts_.create({}, org="o2"))
    assert dict(server.last().url.params) == {"org": "o2"}
    await go(posts_.create({}))
    assert not server.last().url.params


async def test_aggregate(mode: str) -> None:
    posts_, server = posts(mode)
    server.on(
        "GET /main/posts/_aggregate", {"groups": [{"key": {"c": "x"}, "count": 2, "sum_total": "5"}], "total_groups": 1}
    )
    out = await go(
        posts_.aggregate(
            where={"s": "paid"}, group_by=["c", "paid_at:month"], metrics=["count", "sum:total"], tz="Europe/Madrid",
            order_by="-sum_total", limit=20, language="en",
        )
    )  # fmt: skip
    q = server.last().url.params
    assert (q["group_by"], q["metrics"], q["tz"], q["order_by"], q["limit"], q["language"]) == (
        "c,paid_at:month", "count,sum:total", "Europe/Madrid", "-sum_total", "20", "en",
    )  # fmt: skip
    assert json.loads(q["where"]) == {"s": "paid"}
    assert out["groups"][0]["sum_total"] == "5"
    server.on("GET /main/posts/_aggregate", lambda r: error_response(400, "aggregate_not_enabled"))
    with pytest.raises(backd.AggregateNotEnabledError):
        await go(posts_.aggregate())


async def test_search(mode: str) -> None:
    posts_, server = posts(mode)
    server.on("GET /main/posts", {"items": [], "limit": 10, "skip": 0, "has_more": False})
    await go(posts_.search('red "running shoes" -kids', where={"status": "published"}, limit=10, skip=20))
    q = server.last().url.params
    assert json.loads(q["where"]) == {"status": "published", "$search": 'red "running shoes" -kids'}
    assert q["order_by"] == "_relevance" and q["limit"] == "10" and q["skip"] == "20"
    await go(posts_.search("x", where='{"a":1}'))
    assert json.loads(server.last().url.params["where"]) == {"a": 1, "$search": "x"}
    with pytest.raises(TypeError, match="text to search"):
        await go(posts_.search("  "))
    server.on("GET /main/posts", lambda r: error_response(400, "search_not_enabled"))
    with pytest.raises(backd.SearchNotEnabledError):
        await go(posts_.search("x"))


async def test_batch(mode: str) -> None:
    server = Server()
    db = make_client(mode, server).db("main")
    server.on("POST /main/_batch", {"results": [{"id": "1", "title": "A"}, {"id": "2"}]})
    ops: list[backd.BatchOperation] = [
        {"op": "create", "collection": "posts", "document": {"title": "A"}},
        {"op": "replace", "collection": "posts", "id": "2", "document": {}, "if_match": 4},
        {"op": "delete", "collection": "posts", "id": "3"},
    ]
    res = await go(db.batch(ops, idempotency_key="b1", team="t1"))
    assert [r["id"] for r in res] == ["1", "2"]
    sent = server.body()["operations"]
    assert sent[1]["if_match"] == '"4"' and "if_match" not in sent[0]
    assert ops[1]["if_match"] == 4  # the caller's operations are left alone
    assert server.last().headers["idempotency-key"] == "b1" and server.last().url.params["team"] == "t1"

    server.on("POST /main/_batch", lambda r: error_response(412, "version_mismatch", "operation 1: version mismatch"))
    with pytest.raises(backd.VersionMismatchError, match="operation 1"):
        await go(db.batch(ops))


async def test_collection_types_are_only_a_hint(mode: str) -> None:
    class Post(backd.Doc):
        title: str

    db = make_client(mode, Server()).db("main")
    typed = db.collection("posts", Post)
    assert typed.path == ["main", "posts"]
    assert db.collection("posts").path == typed.path
