from __future__ import annotations

import httpx
import pytest

from .helpers import Server, go, json_response, make_client


def posts_server(seen: list[httpx.Request]) -> Server:
    """A server whose answer for a post depends on who asked, with the same ETag for everyone."""
    server = Server()

    def post(req: httpx.Request) -> httpx.Response:
        seen.append(req)
        who = req.headers.get("x-backd-on-behalf-of", "key")
        return httpx.Response(200, json={"id": "p1", "title": f"seen by {who}"}, headers={"ETag": '"1"'})

    server.on("GET /main/posts/p1", post)
    return server


async def test_as_user_sends_the_header_and_keeps_the_key(mode: str) -> None:
    seen: list[httpx.Request] = []
    client = make_client(mode, posts_server(seen), api_key="bdk_secret")
    ada = client.as_user("u_ada")
    assert (await go(ada.db("main").collection("posts").get("p1")))["title"] == "seen by u_ada"
    assert (await go(client.db("main").collection("posts").get("p1")))["title"] == "seen by key"
    assert seen[0].headers["x-backd-on-behalf-of"] == "u_ada"
    assert seen[0].headers["authorization"] == "Bearer bdk_secret"
    assert "x-backd-on-behalf-of" not in seen[1].headers
    assert ada.on_behalf_of == "u_ada" and client.on_behalf_of is None


async def test_each_acting_client_has_a_cache_of_its_own(mode: str) -> None:
    seen: list[httpx.Request] = []
    client = make_client(mode, posts_server(seen), api_key="bdk_secret")
    ada, bob = client.as_user("u_ada"), client.as_user("u_bob")
    await go(ada.db("main").collection("posts").get("p1"))
    got = await go(bob.db("main").collection("posts").get("p1"))
    assert "if-none-match" not in seen[1].headers, "bob must not revalidate with ada's tag"
    assert got["title"] == "seen by u_bob"
    assert ada.cache_stats().entries == 1 and bob.cache_stats().entries == 1 and client.cache_stats().entries == 0


async def test_as_user_needs_a_key_an_id_and_cannot_be_chained(mode: str) -> None:
    server = Server()
    with pytest.raises(TypeError, match="API key"):
        make_client(mode, server).as_user("u1")
    keyed = make_client(mode, server, api_key="bdk_secret")
    ada = keyed.as_user("u_ada")
    with pytest.raises(TypeError, match="already acts as a user"):
        ada.as_user("u_bob")
    with pytest.raises(TypeError, match="user id is required"):
        keyed.as_user("  ")


async def test_an_acting_client_refuses_the_account_routes_before_sending(mode: str) -> None:
    server = Server()
    server.on("GET /_auth/me", json_response(200, {"id": "u1"}))
    ada = make_client(mode, server, api_key="bdk_secret").as_user("u_ada")
    with pytest.raises(TypeError, match="cannot use the admin API or the account routes"):
        await go(ada.auth.me())
    assert server.calls == []
