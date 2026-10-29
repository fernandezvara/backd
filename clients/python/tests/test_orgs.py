from __future__ import annotations

from typing import Any

import pytest

import backd

from .helpers import Server, error_response, go, make_client

ORG = {"id": "o1", "name": "Acme", "slug": "acme", "status": "active", "created_at": "t"}


def setup(mode: str, **kw: Any) -> tuple[Any, Server, backd.MemoryStorage]:
    server, storage = Server(), backd.MemoryStorage()
    storage.set("bds_x")
    return make_client(mode, server, storage=storage, **kw), server, storage


async def test_create_keeps_the_session_of_the_new_organization(mode: str) -> None:
    c, server, storage = setup(mode)
    server.on(
        "POST /_orgs", {**ORG, "session": {"token": "bds_org", "expires_at": "2030-01-01T00:00:00Z", "org": "o1"}}, 201
    )
    org = await go(c.orgs.create("Acme", slug="acme"))
    assert org["id"] == "o1" and server.body() == {"name": "Acme", "slug": "acme"}
    assert storage.slot("acme").get() == "bds_org"
    assert c.orgs.signed_in() == [{"ref": "acme", "id": "o1", "expires_at": "2030-01-01T00:00:00Z", "name": None}]

    server.on("POST /_orgs", {**ORG, "slug": None, "id": "o2", "session": {"token": "bds_o2", "expires_at": "x"}}, 201)
    await go(c.orgs.create("No slug"))
    assert storage.slot("o2").get() == "bds_o2"  # a slug-less organization is named by its id

    server.on("POST /_orgs", {**ORG, "id": "o3"}, 201)  # no session in the answer: nothing to keep
    await go(c.orgs.create("Plain"))
    assert "o3" not in storage.slots()


async def test_signed_in_lists_what_the_storage_holds(mode: str) -> None:
    c, server, storage = setup(mode)
    assert c.orgs.signed_in() == []
    storage.slot("acme").set("t1")
    storage.slot("meta:acme").set('{"org": "o1", "name": "Acme", "expires_at": "e"}')
    storage.slot("beta").set("t2")
    storage.slot("meta:beta").set("not json")
    assert c.orgs.signed_in() == [
        {"ref": "acme", "id": "o1", "expires_at": "e", "name": "Acme"},
        {"ref": "beta", "id": None, "expires_at": None, "name": None},
    ]
    assert (
        make_client(
            mode,
            server,
            storage=type("S", (), {"get": lambda s: None, "set": lambda s, t: None, "remove": lambda s: None})(),
        ).orgs.signed_in()
        == []
    )


async def test_organization_calls(mode: str) -> None:
    c, server, _ = setup(mode)
    server.on("GET /_orgs/o1", ORG)
    server.on("PATCH /_orgs/o1", ORG)
    server.on("POST /_orgs/o1/leave", None, 204)
    server.on("DELETE /_orgs/o1", None, 204)
    assert (await go(c.orgs.get("o1")))["name"] == "Acme"
    await go(c.orgs.update("o1", name="Acme Corp"))
    assert server.body() == {"name": "Acme Corp"} and "x-backd-reauth" not in server.last().headers
    c._proof = ("rt", 4e9)
    for change in ({"require_mfa": True}, {"sso_required": True}, {"session_max_age": 3600}):
        await go(c.orgs.update("o1", **change))
        assert server.last().headers["x-backd-reauth"] == "rt" and server.body() == change
    await go(c.orgs.update("o1", slug="acme2"))
    assert "x-backd-reauth" not in server.last().headers  # a rename needs no fresh proof
    await go(c.orgs.leave("o1"))
    await go(c.orgs.delete("o1"))
    assert server.last().headers["x-backd-reauth"] == "rt"
    server.on("GET /_orgs/zz", lambda r: error_response(403, "not_org_member"))
    with pytest.raises(backd.NotOrgMemberError):
        await go(c.orgs.get("zz"))


async def test_members(mode: str) -> None:
    c, server, _ = setup(mode)
    members = c.orgs.members("o1")
    server.on("GET /_orgs/o1/members", {"items": [{"user": "u1"}], "has_more": True, "next": "u1"})
    page = await go(members.list(limit=10, after="u0", sso=False))
    assert page["next"] == "u1" and dict(server.last().url.params) == {"limit": "10", "after": "u0", "sso": "false"}
    await go(members.list())
    assert not server.last().url.params
    server.on("PATCH /_orgs/o1/members/u 1", {"user": "u 1", "roles": ["editor"]})
    server.on("DELETE /_orgs/o1/members/u 1", None, 204)
    assert (await go(members.update("u 1", ["editor"])))["roles"] == ["editor"] and server.body() == {
        "roles": ["editor"]
    }
    await go(members.remove("u 1"))
    assert server.paths()[-1] == "DELETE /_orgs/o1/members/u 1"


async def test_teams(mode: str) -> None:
    c, server, _ = setup(mode)
    teams = c.orgs.teams("o1")
    team = {"id": "t1", "name": "Ops", "roles": ["r"], "created_at": "t"}
    server.on("GET /_orgs/o1/teams", {"items": [team]})
    server.on("POST /_orgs/o1/teams", team, 201)
    server.on("PATCH /_orgs/o1/teams/t1", team)
    server.on("DELETE /_orgs/o1/teams/t1", None, 204)
    assert await go(teams.list()) == [team]
    await go(teams.create("Ops", roles=["r"]))
    assert server.body() == {"name": "Ops", "roles": ["r"]}
    await go(teams.update("t1", name="Ops 2"))
    assert server.body() == {"name": "Ops 2"}
    await go(teams.delete("t1"))

    server.on("GET /_orgs/o1/teams/t1/members", {"items": [], "has_more": False})
    await go(teams.members("t1", role="lead", limit=5, after="u1"))
    assert dict(server.last().url.params) == {"role": "lead", "limit": "5", "after": "u1"}

    server.on("POST /_orgs/o1/teams/t1/roles", {"user": "u1"})
    server.on("PATCH /_orgs/o1/teams/t1/roles", {"user": "u1"})
    server.on("DELETE /_orgs/o1/teams/t1/roles", {"user": "u1"})
    await go(teams.grant_role("t1", "u1", "lead", from_="2026-01-01T00:00:00Z", until="2026-02-01T00:00:00Z"))
    assert server.body() == {
        "user": "u1",
        "role": "lead",
        "from": "2026-01-01T00:00:00Z",
        "until": "2026-02-01T00:00:00Z",
    }
    await go(teams.grant_role("t1", "u1", "lead"))
    assert server.body() == {"user": "u1", "role": "lead"}
    await go(teams.set_role_window("t1", "u1", "lead", until="2027-01-01T00:00:00Z"))
    assert server.body() == {"user": "u1", "role": "lead", "until": "2027-01-01T00:00:00Z"}
    await go(teams.revoke_role("t1", "u1", "lead"))
    assert server.last().method == "DELETE" and server.body() == {"user": "u1", "role": "lead"}

    server.on("PUT /_orgs/o1/teams/t1/members/u1", {"user": "u1"})
    server.on("DELETE /_orgs/o1/teams/t1/members/u1", {"user": "u1"})
    await go(teams.add_member("t1", "u1"))
    await go(teams.remove_member("t1", "u1"))
    assert server.paths()[-2:] == ["PUT /_orgs/o1/teams/t1/members/u1", "DELETE /_orgs/o1/teams/t1/members/u1"]
    server.on("POST /_orgs/o1/teams/t1/roles", lambda r: error_response(403, "org_role_requires_mfa"))
    with pytest.raises(backd.OrgRoleRequiresMFAError):
        await go(teams.grant_role("t1", "u1", "lead"))


async def test_templates(mode: str) -> None:
    c, server, _ = setup(mode)
    templates = c.orgs.templates("o1")
    text = {"subject": "s", "text": "t", "html": "<p>h</p>"}
    server.on("GET /_orgs/o1/templates", {"customizable": {"invite": {"required": ["Link"]}}, "items": []})
    server.on("GET /_orgs/o1/templates/invite/es", {"kind": "invite", "locale": "es", "version": 2, **text})
    server.on("PUT /_orgs/o1/templates/invite/es", {"kind": "invite", "locale": "es", "version": 3, "removed": []})
    server.on("DELETE /_orgs/o1/templates/invite/es", None, 204)
    server.on("POST /_orgs/o1/templates/invite/es/preview", {**text, "removed": []})
    server.on("POST /_orgs/o1/templates/invite/es/test", None, 204)
    assert "invite" in (await go(templates.list()))["customizable"]
    assert (await go(templates.get("invite", "es")))["version"] == 2
    assert (await go(templates.put("invite", "es", text)))["version"] == 3 and server.body() == text
    await go(templates.delete("invite", "es"))
    await go(templates.preview("invite", "es", draft=text, data="real"))
    assert server.body() == {**text, "data": "real"}
    await go(templates.preview("invite", "es"))
    assert server.body() == {}
    await go(templates.test("invite", "es", draft=text))
    assert server.body() == text
    server.on("PUT /_orgs/o1/templates/invite/es", lambda r: error_response(400, "invalid_template"))
    with pytest.raises(backd.ValidationError):
        await go(templates.put("invite", "es", text))


async def test_invitations(mode: str) -> None:
    c, server, _ = setup(mode)
    inv = c.orgs.invitations("o1")
    server.on(
        "POST /_orgs/o1/invitations", {"id": "i1", "binding": "email", "token": "tok", "link": "https://app/x"}, 201
    )
    server.on("GET /_orgs/o1/invitations", {"items": [{"id": "i1"}]})
    server.on("DELETE /_orgs/o1/invitations/i1", None, 204)
    server.on("POST /_orgs/o1/invitations/i1/link", {"id": "i1", "token": "tok2", "link": None})
    server.on("POST /_orgs/o1/invitations/i1/send", None, 204)
    made = await go(
        inv.create("email", email="a@x.test", roles=["editor"], teams=["t1"], send=True, expires_in="2d", max_uses=3)
    )
    assert made["token"] == "tok"
    assert server.body() == {
        "binding": "email",
        "email": "a@x.test",
        "roles": ["editor"],
        "teams": ["t1"],
        "send": True,
        "expires_in": "2d",
        "max_uses": 3,
    }
    await go(inv.create("none"))
    assert server.body() == {"binding": "none"}
    assert [i["id"] for i in await go(inv.list())] == ["i1"]
    await go(inv.revoke("i1"))
    assert (await go(inv.new_link("i1")))["token"] == "tok2"
    await go(inv.send("i1"))


async def test_sso_connections(mode: str) -> None:
    c, server, _ = setup(mode)
    sso = c.orgs.sso("o1")
    conn = {"provider": "google", "client_id": "cid", "client_secret_set": True}
    server.on("GET /_orgs/o1/sso", {"items": [conn], "providers": ["google"], "max_connections": 2})
    server.on("GET /_orgs/o1/sso/google", conn)
    server.on("PUT /_orgs/o1/sso/google", conn)
    server.on("DELETE /_orgs/o1/sso/google", None, 204)
    assert (await go(sso.list()))["max_connections"] == 2
    assert (await go(sso.get("google")))["client_id"] == "cid"
    c._proof = ("rt", 4e9)
    await go(
        sso.set(
            "google",
            client_id="cid",
            client_secret="s3",
            hosted_domain="acme.test",
            email_domains=["acme.test"],
            native_client_ids=["n"],
            join_roles=["viewer"],
            enabled=True,
        )
    )
    assert server.body() == {
        "client_id": "cid",
        "client_secret": "s3",
        "hosted_domain": "acme.test",
        "email_domains": ["acme.test"],
        "native_client_ids": ["n"],
        "join_roles": ["viewer"],
        "enabled": True,
    }
    assert server.last().headers["x-backd-reauth"] == "rt"
    await go(sso.set("google", client_id="cid"))
    assert server.body() == {"client_id": "cid"}  # the stored secret is kept
    await go(sso.delete("google"))
    assert server.last().headers["x-backd-reauth"] == "rt"
    server.on("GET /_orgs/o1/sso/microsoft", lambda r: error_response(404, "not_found"))
    with pytest.raises(backd.NotFoundError):
        await go(sso.get("microsoft"))
