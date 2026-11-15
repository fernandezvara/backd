from __future__ import annotations

import httpx
import pytest

from .helpers import Server, go, make_client


def make(mode: str) -> tuple[Server, object]:
    server = Server()
    doc = {"id": "p1", "title": "T"}
    server.on("GET /main/posts/p1", doc)
    server.on("GET /main/posts", {"items": [doc], "limit": 20, "skip": 0, "has_more": False})
    server.on("POST /main/posts", doc, status=201)
    server.on("PUT /main/posts/p1", doc)
    server.on("PATCH /main/posts/p1", doc)
    server.on("POST /main/posts/p1/restore", doc)
    client = make_client(mode, server)
    return server, client.db("main").collection("posts")


def query_of(req: httpx.Request) -> dict[str, str]:
    return dict(req.url.params)


async def test_fields_go_on_reads_as_one_parameter(mode: str) -> None:
    server, posts = make(mode)
    one = await go(posts.get("p1", fields=["title", "address.city"]))  # type: ignore[attr-defined]
    page = await go(posts.list(fields=["title"], limit=5))  # type: ignore[attr-defined]
    assert query_of(server.calls[0])["fields"] == "title,address.city"
    assert query_of(server.calls[1]) == {"fields": "title", "limit": "5"}
    assert one["title"] == "T" and page.items[0]["id"] == "p1"


async def test_fields_go_on_the_answers_of_writes(mode: str) -> None:
    server, posts = make(mode)
    await go(posts.create({"title": "T"}, fields=["title"], idempotency_key="k1", team="blue"))  # type: ignore[attr-defined]
    await go(posts.replace("p1", {"title": "T"}, fields=["title"], if_match=1))  # type: ignore[attr-defined]
    await go(posts.patch("p1", {"title": "U"}, fields=["_meta.version"], if_match=2))  # type: ignore[attr-defined]
    await go(posts.restore("p1", fields=["title"]))  # type: ignore[attr-defined]
    created = server.calls[0]
    assert query_of(created) == {"fields": "title", "team": "blue"} and created.headers["idempotency-key"] == "k1"
    assert query_of(server.calls[1])["fields"] == "title" and server.calls[1].headers["if-match"] == '"1"'
    assert query_of(server.calls[2])["fields"] == "_meta.version"
    assert query_of(server.calls[3])["fields"] == "title"


async def test_without_fields_nothing_is_added(mode: str) -> None:
    server, posts = make(mode)
    await go(posts.get("p1"))  # type: ignore[attr-defined]
    assert "fields" not in query_of(server.calls[0])


async def test_fields_must_be_a_non_empty_list_of_paths(mode: str) -> None:
    server, posts = make(mode)
    for bad in ([], ["title", " "], "title"):
        with pytest.raises(TypeError, match="fields"):
            await go(posts.get("p1", fields=bad))  # type: ignore[attr-defined]
    assert server.calls == []
