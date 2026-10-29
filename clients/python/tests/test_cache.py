from __future__ import annotations

from typing import Any

import httpx
import pytest

import backd
from backd.cache import CacheOptions, Entry, RevalidationCache, cacheable

from .helpers import Server, go, json_response, make_client


def etagged(body: Any, etag: str = '"v1"', **headers: str) -> httpx.Response:
    return httpx.Response(200, json=body, headers={"ETag": etag, **headers})


class Doc:
    """A document that answers 304 to the tag it gave."""

    def __init__(self) -> None:
        self.version = 1
        self.sent: list[str | None] = []

    def __call__(self, req: httpx.Request) -> httpx.Response:
        tag = req.headers.get("if-none-match")
        self.sent.append(tag)
        if tag == f'"v{self.version}"':
            return httpx.Response(304, headers={"ETag": tag})
        return etagged({"id": "p1", "n": self.version}, f'"v{self.version}"')


def doc_list(doc: Doc, req: httpx.Request) -> httpx.Response:
    res = doc(req)
    if res.status_code == 304:
        return res
    return etagged(
        {"items": [{"id": "p1"}], "limit": 20, "skip": 0, "has_more": False},
        res.headers["etag"] + req.url.query.decode(),
    )


def make(mode: str, **kw: Any) -> tuple[Any, Server, Doc, Any]:
    server, doc = Server(), Doc()
    server.on("GET /main/posts/p1", doc)
    server.on("GET /main/posts", lambda r: doc_list(doc, r))
    client = make_client(mode, server, **kw)
    return client, server, doc, client.db("main").collection("posts")


async def test_an_unchanged_read_costs_a_304(mode: str) -> None:
    c, _, doc, posts = make(mode)
    first = await go(posts.get("p1"))
    second = await go(posts.get("p1"))
    assert first == second == {"id": "p1", "n": 1}
    assert doc.sent == [None, '"v1"']
    assert c.cache_stats() == backd.CacheStats(hits=1, misses=1, entries=1, bytes=c.cache_stats().bytes)
    assert c.cache_stats().bytes > 0
    doc.version = 2
    assert (await go(posts.get("p1")))["n"] == 2  # changed: the whole body again, replacing the entry
    assert c.cache_stats().entries == 1 and c.cache_stats().misses == 2


async def test_lists_are_cached_per_query(mode: str) -> None:
    c, _, doc, posts = make(mode)
    await go(posts.list(limit=5))
    await go(posts.list(limit=5))
    await go(posts.list(limit=6))
    assert doc.sent[0] is None and doc.sent[1] is not None  # the second read sent the tag the first one got
    assert c.cache_stats().entries == 2 and c.cache_stats().hits == 0


async def test_the_response_says_where_it_came_from(mode: str) -> None:
    c, _, _, _ = make(mode)
    a = await go(c.request("GET", ["main", "posts", "p1"]))
    b = await go(c.request("GET", ["main", "posts", "p1"]))
    assert (a.from_cache, a.status) == (False, 200)
    assert (b.from_cache, b.status, b.data) == (True, 200, {"id": "p1", "n": 1})
    assert b.headers["etag"] == '"v1"'


async def test_what_is_not_cacheable_is_never_kept(mode: str) -> None:
    c, server, doc, posts = make(mode)
    server.on("GET /main/posts/p1/files", doc)
    server.on("GET /main/_admin", doc)
    await go(posts.get("p1", expand="author"))
    await go(posts.get("p1", file_links=True))
    await go(posts.list(expand="a"))
    await go(c.request("GET", ["main", "posts", "p1", "files"]))
    await go(c.request("GET", ["main", "_admin"]))
    await go(c.request("GET", ["main", "posts", "p1"], raw=True))
    assert c.cache_stats().entries == 0
    assert all(tag is None for tag in doc.sent)


async def test_cache_can_be_turned_off(mode: str) -> None:
    off, _, doc, posts = make(mode, cache=False)
    await go(posts.get("p1"))
    await go(posts.get("p1"))
    assert doc.sent == [None, None]
    assert off.cache_stats() == backd.CacheStats(0, 0, 0, 0)
    _, _, doc2, posts2 = make(mode)
    await go(posts2.get("p1"))
    await go(posts2.get("p1", cache=False))  # this read skips the cache
    assert doc2.sent == [None, None]


async def test_no_store_and_missing_etag_are_not_kept(mode: str) -> None:
    server = Server()
    server.on("GET /main/a", etagged({"a": 1}, **{"Cache-Control": "private, no-store"}))
    server.on("GET /main/b", json_response(200, {"b": 1}))
    c = make_client(mode, server)
    await go(c.request("GET", ["main", "a"]))
    await go(c.request("GET", ["main", "b"]))
    assert c.cache_stats().entries == 0 and c.cache_stats().misses == 2


async def test_callers_own_if_none_match_gets_the_304(mode: str) -> None:
    c, _, _, _ = make(mode)
    await go(c.request("GET", ["main", "posts", "p1"]))
    res = await go(c.request("GET", ["main", "posts", "p1"], headers={"If-None-Match": '"v1"'}))
    assert res.status == 304 and res.data is None and not res.from_cache


async def test_a_write_drops_the_document_from_the_cache(mode: str) -> None:
    c, server, doc, posts = make(mode)
    server.on("PATCH /main/posts/p1", {"id": "p1"})
    await go(posts.get("p1"))
    await go(posts.list())
    assert c.cache_stats().entries == 2
    await go(posts.patch("p1", {"n": 2}))
    assert c.cache_stats().entries == 1  # the document is gone; the list revalidates anyway
    await go(posts.get("p1"))
    assert doc.sent[-1] is None


async def test_another_caller_never_sees_the_entries(mode: str) -> None:
    storage = backd.MemoryStorage()
    c, server, doc, posts = make(mode, storage=storage)
    server.on(
        "POST /_auth/login", {"token": "bds_2", "session_id": "s", "expires_at": "2030-01-01T00:00:00Z", "user": {}}
    )
    storage.set("bds_1")
    await go(posts.get("p1"))
    assert c.cache_stats().entries == 1
    await go(c.auth.login("a@x.test", "dev-p4ssw0rd!"))  # a sign-in empties the cache
    assert c.cache_stats().entries == 0
    await go(posts.get("p1"))
    assert doc.sent[-1] is None

    # even without a sign-in call: the token changed under the client
    storage.set("bds_3")
    await go(posts.get("p1"))
    assert doc.sent[-1] is None
    assert c.cache_stats().entries == 1


async def test_api_key_and_session_entries_are_apart(mode: str) -> None:
    server, doc = Server(), Doc()
    server.on("GET /main/posts/p1", doc)
    key = make_client(mode, server, api_key="bdk_1")
    await go(key.request("GET", ["main", "posts", "p1"]))
    anon = make_client(mode, server)
    await go(anon.request("GET", ["main", "posts", "p1"]))
    assert doc.sent == [None, None]  # separate clients, separate caches


def test_lru_by_entries_and_bytes() -> None:
    c = RevalidationCache(CacheOptions(max_entries=2, max_bytes=10))
    h = httpx.Headers()
    c.set("a", Entry('"1"', b"1234", h))
    c.set("b", Entry('"2"', b"1234", h))
    assert c.get("a") is not None  # a is now the most recent
    c.set("c", Entry('"3"', b"12", h))
    assert c.get("b") is None and c.get("a") is not None and c.get("c") is not None
    c.set("d", Entry('"4"', b"123456", h))  # 4 + 2 + 6 > 10: the oldest go
    assert c.stats().bytes <= 10
    c.set("huge", Entry('"5"', b"x" * 11, h))  # alone over the bound: not kept
    assert c.get("huge") is None
    c.scope("one")
    c.set("k", Entry('"6"', b"1", h))
    c.scope("one")
    assert c.get("k") is not None
    c.scope("two")
    assert c.stats() == backd.CacheStats(0, 0, 0, 0)
    c.set("k", Entry('"6"', b"1", h))
    c.set("k", Entry('"7"', b"22", h))  # replaced, not added
    assert c.stats().entries == 1 and c.stats().bytes == 2
    c.delete("k")
    c.delete("k")
    assert c.stats().entries == 0


def test_cacheable_rules() -> None:
    assert cacheable("GET", ["db", "c"], None, False)
    assert cacheable("GET", ["db", "c", "id"], {"limit": 1, "expand": None}, False)
    assert not cacheable("POST", ["db", "c"], None, False)
    assert not cacheable("GET", ["db"], None, False)
    assert not cacheable("GET", ["db", "c", "id", "x"], None, False)
    assert not cacheable("GET", ["_auth", "me"], None, False)
    assert not cacheable("GET", ["db", "c", "_aggregate"], None, False)
    assert not cacheable("GET", ["db", "c"], {"file_links": True}, False)
    assert not cacheable("GET", ["db", "c"], {"expand": "a"}, False)
    assert not cacheable("GET", ["db", "c"], None, True)


async def test_clear_cache(mode: str) -> None:
    c, _, _, posts = make(mode)
    await go(posts.get("p1"))
    c.clear_cache()
    assert c.cache_stats().entries == 0
    with pytest.raises(AssertionError):
        assert c.cache_stats().entries == 1
