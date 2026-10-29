from __future__ import annotations

from typing import Any

import httpx
import pytest

import backd
from backd.models import ConsentRef
from backd.oauth import challenge_of, load_pending

from .helpers import Server, error_response, go, make_client

USER = {"id": "u1", "email": "a@x.test", "email_verified": True, "roles": [], "locale": "en", "created_at": "t"}


def session(token: str = "bds_one", **extra: Any) -> dict[str, Any]:
    return {
        "token": token,
        "token_type": "Bearer",
        "session_id": "s1",
        "expires_at": "2030-01-01T00:00:00Z",
        "user": USER,
        **extra,
    }


def setup(mode: str, **kw: Any) -> tuple[Any, Server, backd.MemoryStorage]:
    server = Server()
    storage = kw.pop("storage", None) or backd.MemoryStorage()
    return make_client(mode, server, storage=storage, **kw), server, storage


async def test_login_stores_the_token_and_emits_events(mode: str) -> None:
    c, server, storage = setup(mode)
    server.on("POST /_auth/login", session())
    events: list[tuple[str, Any]] = []
    c.auth.on_auth_change(lambda e, s: events.append((e, s)))
    s = await go(c.auth.login("a@x.test", "dev-p4ssw0rd!"))
    assert s["token"] == "bds_one"
    assert storage.get() == "bds_one"
    assert c.auth.has_session()
    assert c.auth.token() == "bds_one"
    assert c.auth.current == s
    assert server.body() == {"email": "a@x.test", "password": "dev-p4ssw0rd!"}
    assert "authorization" not in server.last().headers  # the sign-in itself sends no credentials
    assert events == [("SIGNED_IN", s)]

    server.on("GET /_auth/me", {**USER, "identities": []})
    await go(c.auth.me())
    assert server.last().headers["authorization"] == "Bearer bds_one"


async def test_signup_variants(mode: str) -> None:
    c, server, storage = setup(mode)
    server.on("POST /_auth/signup", session("bds_new"))
    s = await go(c.auth.signup("a@x.test", "dev-p4ssw0rd!", locale="es", invitation="inv", consents={"terms": "1"}))
    assert s["token"] == "bds_new"
    assert server.body() == {
        "email": "a@x.test",
        "password": "dev-p4ssw0rd!",
        "consents": {"terms": "1"},
        "invitation": "inv",
        "locale": "es",
    }

    server.on("POST /_auth/signup", None, 202)
    storage.remove()
    with pytest.raises(backd.VerificationRequiredError):
        await go(c.auth.signup("b@x.test", "dev-p4ssw0rd!"))
    assert storage.get() is None

    server.on("POST /_auth/orgs/Acme/signup", session("bds_acme", org="o1"))
    await go(c.auth.signup("c@x.test", "dev-p4ssw0rd!", org=" Acme "))  # trimmed; the server ignores case


async def test_second_steps_are_values_for_sign_in_and_errors_for_login(mode: str) -> None:
    c, server, storage = setup(mode)
    server.on("POST /_auth/login", {"mfa_required": True, "challenge": "ch1", "methods": ["totp", "recovery_code"]})
    r = await go(c.auth.sign_in("a@x.test", "dev-p4ssw0rd!"))
    assert r == backd.MfaChallenge("ch1", ["totp", "recovery_code"])
    assert storage.get() is None
    with pytest.raises(backd.SecondStepRequiredError) as ei:
        await go(c.auth.login("a@x.test", "dev-p4ssw0rd!"))
    assert (ei.value.code, ei.value.challenge, ei.value.methods) == ("mfa_required", "ch1", ["totp", "recovery_code"])

    server.on("POST /_auth/mfa/verify", session("bds_mfa", new_user=False))
    s = await go(c.auth.verify_mfa("ch1", "totp", "123456"))
    assert s["token"] == "bds_mfa"
    assert server.body() == {"challenge": "ch1", "method": "totp", "code": "123456"}
    assert storage.get() == "bds_mfa"

    server.on("POST /_auth/login", {"mfa_enrollment_required": True, "challenge": "ch2"})
    assert await go(c.auth.sign_in("a@x.test", "dev-p4ssw0rd!")) == backd.EnrollmentChallenge("ch2")
    with pytest.raises(backd.SecondStepRequiredError) as ei2:
        await go(c.auth.login("a@x.test", "dev-p4ssw0rd!"))
    assert ei2.value.code == "mfa_enrollment_required"

    consents: list[ConsentRef] = [{"name": "terms", "version": "2"}]
    server.on("POST /_auth/login", {"consent_required": True, "challenge": "ch3", "consents": consents})
    assert await go(c.auth.sign_in("a@x.test", "dev-p4ssw0rd!")) == backd.ConsentChallenge("ch3", consents)
    with pytest.raises(backd.SecondStepRequiredError) as ei3:
        await go(c.auth.login("a@x.test", "dev-p4ssw0rd!"))
    assert (ei3.value.code, ei3.value.consents) == ("consent_required", consents)

    server.on("POST /_auth/consents/accept", session("bds_c"))
    await go(c.auth.accept_consents("ch3", {"terms": "2"}))
    assert server.body() == {"challenge": "ch3", "consents": {"terms": "2"}}
    assert storage.get() == "bds_c"


async def test_expired_password_is_a_typed_error(mode: str) -> None:
    c, server, _ = setup(mode)
    server.on(
        "POST /_auth/login",
        lambda r: httpx.Response(
            401,
            json={
                "error": {
                    "code": "password_expired",
                    "message": "expired",
                    "details": [{"path": "reset_email", "reason": "queued"}],
                }
            },
        ),
    )
    with pytest.raises(backd.PasswordExpiredError) as ei:
        await go(c.auth.login("a@x.test", "old"))
    assert ei.value.reset_email_queued


async def test_a_refused_token_ends_the_session(mode: str) -> None:
    c, server, storage = setup(mode)
    storage.set("bds_old")
    c._proof = ("p", 4e9)
    server.on("GET /_auth/me", lambda r: error_response(401, "unauthenticated"))
    events: list[str] = []
    c.auth.on_auth_change(lambda e, s: events.append(e))
    with pytest.raises(backd.AuthenticationError):
        await go(c.auth.me())
    assert storage.get() is None
    assert events == ["SESSION_EXPIRED"]
    assert not c.auth.has_fresh_proof()


async def test_credential_change_hooks_run_on_sign_in_and_out(mode: str) -> None:
    c, server, _ = setup(mode)
    server.on("POST /_auth/login", session())
    server.on("POST /_auth/logout", None, 204)
    n: list[int] = []
    c.on_credential_change(lambda: n.append(1))
    await go(c.auth.login("a@x.test", "dev-p4ssw0rd!"))
    await go(c.auth.logout())
    assert len(n) == 2


async def test_logout_removes_the_token_even_when_the_server_cannot_be_reached(mode: str) -> None:
    c, server, storage = setup(mode)
    storage.set("bds_x")

    def boom(req: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("down", request=req)

    server.on("POST /_auth/logout", boom)
    with pytest.raises(backd.NetworkError):
        await go(c.auth.logout())
    assert storage.get() is None

    storage.set("bds_y")
    server.on("POST /_auth/logout", lambda r: error_response(401, "unauthenticated"))
    await go(c.auth.logout())  # a session the server no longer knows is logged out already
    assert storage.get() is None


async def test_reauth_keeps_the_proof_and_sends_it_with_sensitive_calls(mode: str) -> None:
    c, server, storage = setup(mode)
    storage.set("bds_x")
    server.on("POST /_auth/password", None, 204)
    server.on("POST /_auth/reauth", {"reauth_token": "rt", "expires_at": "2099-01-01T00:00:00.123456789Z"})
    await go(c.auth.change_password("dev-p4ssw0rd!2"))
    assert "x-backd-reauth" not in server.last().headers
    assert not c.auth.has_fresh_proof()
    out = await go(c.auth.reauth({"method": "password", "password": "dev-p4ssw0rd!"}))
    assert out == {"reauth_token": "rt", "expires_at": "2099-01-01T00:00:00.123456789Z"}
    assert c.auth.has_fresh_proof()
    await go(c.auth.change_password("dev-p4ssw0rd!2"))
    assert server.last().headers["x-backd-reauth"] == "rt"
    assert server.body() == {"new_password": "dev-p4ssw0rd!2"}


async def test_with_proof_asks_once_and_retries(mode: str) -> None:
    c, server, storage = setup(mode)
    storage.set("bds_x")
    attempts = {"n": 0}

    def delete(req: httpx.Request) -> httpx.Response:
        attempts["n"] += 1
        if "x-backd-reauth" not in req.headers:
            return error_response(401, "reauth_required")
        return httpx.Response(204)

    server.on("DELETE /_auth/me", delete)
    server.on("GET /_auth/me", {**USER, "identities": [], "reauth_methods": ["password"]})
    server.on("POST /_auth/reauth", {"reauth_token": "rt", "expires_at": "2099-01-01T00:00:00Z"})
    asked: list[list[str]] = []

    if mode == "async":

        async def ask(methods: list[str]) -> Any:
            asked.append(methods)
            return {"method": "password", "password": "dev-p4ssw0rd!"}

        async def action() -> None:
            await c.auth.delete_account()

    else:

        def ask(methods: list[str]) -> Any:  # type: ignore[misc]
            asked.append(methods)
            return {"method": "password", "password": "dev-p4ssw0rd!"}

        def action() -> None:  # type: ignore[misc]
            c.auth.delete_account()

    await go(c.auth.with_proof(ask, action))
    assert asked == [["password"]]
    assert attempts["n"] == 2
    assert storage.get() is None  # the account is gone: signed out


async def test_other_errors_pass_through_with_proof(mode: str) -> None:
    c, server, _ = setup(mode)
    server.on("GET /_auth/me", lambda r: error_response(403, "forbidden"))

    async def aaction() -> Any:
        return await c.auth.me()

    def saction() -> Any:
        return c.auth.me()

    async def aask(m: list[str]) -> Any:
        raise AssertionError("not asked")

    def sask(m: list[str]) -> Any:
        raise AssertionError("not asked")

    with pytest.raises(backd.ForbiddenError):
        await go(c.auth.with_proof(aask if mode == "async" else sask, aaction if mode == "async" else saction))


async def test_account_calls(mode: str) -> None:
    c, server, storage = setup(mode)
    storage.set("bds_x")
    server.on("PATCH /_auth/me", USER)
    server.on("GET /_auth/sessions", {"items": [{"id": "s1", "current": True}]})
    server.on("DELETE /_auth/sessions/s/1", None, 204)
    server.on("POST /_auth/logout-all", None, 204)
    server.on("POST /_auth/reauth/email-code", None, 204)
    assert (await go(c.auth.update_me(locale="es")))["id"] == "u1"
    assert server.body() == {"locale": "es"}
    assert (await go(c.auth.sessions()))[0]["id"] == "s1"
    await go(c.auth.revoke_session("s/1"))
    await go(c.auth.request_reauth_code())
    await go(c.auth.logout_all())
    assert storage.get() is None


async def test_email_and_password_flows_send_no_credentials(mode: str) -> None:
    c, server, storage = setup(mode)
    storage.set("bds_x")
    for route in (
        "POST /_auth/verify-email/resend",
        "POST /_auth/verify-email",
        "POST /_auth/reset-password/request",
        "POST /_auth/reset-password",
        "POST /_auth/password/update",
        "POST /_auth/confirm-email-change",
        "POST /_auth/revert-email-change",
        "POST /_auth/accept-invitation",
        "POST /_auth/orgs/find",
    ):
        server.on(route, None, 204)
    await go(c.auth.resend_verification("a@x.test", redirect_to="https://app.test/ok"))
    await go(c.auth.verify_email("tok"))
    await go(c.auth.request_password_reset("a@x.test"))
    await go(c.auth.reset_password("tok", "dev-p4ssw0rd!"))
    await go(c.auth.update_password("a@x.test", "old", "dev-p4ssw0rd!", code="r", method="recovery"))
    await go(c.auth.confirm_email_change("tok"))
    await go(c.auth.revert_email_change("tok"))
    await go(c.auth.accept_invitation("tok", "dev-p4ssw0rd!", locale="en"))
    await go(c.auth.find_orgs("a@x.test"))
    assert all("authorization" not in r.headers for r in server.calls)
    assert server.body(0) == {"email": "a@x.test", "redirect_to": "https://app.test/ok"}
    assert server.body(4) == {
        "email": "a@x.test",
        "password": "old",
        "new_password": "dev-p4ssw0rd!",
        "code": "r",
        "method": "recovery",
    }
    assert server.body(7) == {"token": "tok", "password": "dev-p4ssw0rd!", "locale": "en"}

    server.on("POST /_auth/email", None, 204)
    c._proof = ("rt", 4e9)
    await go(c.auth.request_email_change("new@x.test"))
    assert server.last().headers["x-backd-reauth"] == "rt"
    assert server.last().headers["authorization"] == "Bearer bds_x"


async def test_mfa(mode: str) -> None:
    c, server, storage = setup(mode)
    storage.set("bds_x")
    server.on("GET /_auth/mfa", {"enabled": False})
    server.on("POST /_auth/mfa/totp/setup", {"setup_id": "su", "secret": "S"})
    server.on("POST /_auth/mfa/totp/confirm", {"recovery_codes": ["a", "b"]})
    server.on("DELETE /_auth/mfa/totp", None, 204)
    server.on("POST /_auth/mfa/recovery-codes", {"recovery_codes": ["c"]})
    assert (await go(c.auth.mfa.status()))["enabled"] is False
    assert (await go(c.auth.mfa.setup()))["setup_id"] == "su"
    assert server.last().headers["authorization"] == "Bearer bds_x"
    assert (await go(c.auth.mfa.confirm(setup_id="su", code="123456")))["recovery_codes"] == ["a", "b"]
    await go(c.auth.mfa.disable())
    assert await go(c.auth.mfa.regenerate_recovery_codes()) == ["c"]

    # With a challenge there is no session yet: no credentials, and the answer ends the sign-in.
    storage.remove()
    await go(c.auth.mfa.setup(challenge="ch"))
    assert server.body() == {"challenge": "ch"}
    assert "authorization" not in server.last().headers
    server.on("POST /_auth/mfa/totp/confirm", {**session("bds_enrolled"), "recovery_codes": ["r1"]})
    out = await go(c.auth.mfa.confirm(setup_id="su", code="1", challenge="ch"))
    assert out["recovery_codes"] == ["r1"]
    assert out["session"]["token"] == "bds_enrolled"
    assert "recovery_codes" not in out["session"]
    assert storage.get() == "bds_enrolled"
    server.on(
        "POST /_auth/mfa/totp/confirm",
        {"consent_required": True, "challenge": "ch9", "consents": [], "recovery_codes": ["r2"]},
    )
    out = await go(c.auth.mfa.confirm(setup_id="su", code="1", challenge="ch"))
    assert out["consent"] == backd.ConsentChallenge("ch9", [])


async def test_password_set_and_remove(mode: str) -> None:
    c, server, storage = setup(mode)
    storage.set("bds_x")
    server.on("POST /_auth/password", None, 204)
    server.on("DELETE /_auth/password", None, 204)
    c._proof = ("rt", 4e9)
    await go(c.auth.password.set("dev-p4ssw0rd!2"))
    await go(c.auth.password.remove())
    assert server.paths() == ["POST /_auth/password", "DELETE /_auth/password"]


async def test_magic_link_round_trip(mode: str) -> None:
    c, server, storage = setup(mode)
    server.on("POST /_auth/magic-link", {"request_id": "rq1"})
    server.on("POST /_auth/magic-link/verify", session("bds_ml"))
    with pytest.raises(backd.ValidationError) as ei:
        await go(c.auth.magic_link.verify_code("123456"))
    assert ei.value.code == "no_request"

    rid = await go(c.auth.magic_link.request("a@x.test", redirect_to="https://app.test/", locale="en"))
    assert rid == "rq1"
    sent = server.body()
    assert sent["email"] == "a@x.test" and sent["redirect_to"] == "https://app.test/"
    pending = load_pending(c.oauth_storage)
    assert pending is not None and pending.request_id == "rq1"
    assert sent["code_challenge"] == challenge_of(pending.verifier)

    s = await go(c.auth.magic_link.verify_code(" 123456 "))
    assert s["token"] == "bds_ml"
    assert server.body() == {"request_id": "rq1", "code": "123456", "code_verifier": pending.verifier}
    assert load_pending(c.oauth_storage) is None
    assert storage.get() == "bds_ml"


async def test_sign_in_with_a_provider_and_back(mode: str) -> None:
    c, server, storage = setup(mode)
    server.on("POST /_auth/oauth/google/start", {"authorize_url": "https://accounts.test/auth?x=1"})
    server.on("POST /_auth/token", session("bds_g", new_user=True, profile={"name": "A"}))
    opened: list[str] = []

    async def anav(url: str) -> None:
        opened.append(url)

    def snav(url: str) -> None:
        opened.append(url)

    url = await go(
        c.auth.sign_in_with(
            "google", redirect_to="http://localhost:9000/cb", navigate=anav if mode == "async" else snav
        )
    )
    assert url == "https://accounts.test/auth?x=1"
    assert opened == [url]
    start = server.body()
    assert start["redirect_to"] == "http://localhost:9000/cb" and start["intent"] == "signin"
    verifier = load_pending(c.oauth_storage).verifier  # type: ignore[union-attr]
    assert start["code_challenge"] == challenge_of(verifier)

    s = await go(c.auth.complete_sign_in("http://localhost:9000/cb?backd_code=one-time"))
    assert s["new_user"] is True
    assert server.body() == {"code": "one-time", "code_verifier": verifier}
    assert storage.get() == "bds_g"
    assert load_pending(c.oauth_storage) is None
    assert await go(c.auth.complete_sign_in("http://localhost:9000/cb?other=1")) is None


async def test_complete_sign_in_failures(mode: str) -> None:
    c, server, _ = setup(mode)
    server.on("POST /_auth/oauth/google/start", {"authorize_url": "https://p.test/"})
    with pytest.raises(backd.ValidationError) as ei:
        await go(c.auth.complete_sign_in("http://x/?backd_code=abc"))
    assert ei.value.code == "wrong_device"

    for code, cls, status in [
        ("account_exists", backd.ConflictError, 409),
        ("link_conflict", backd.ConflictError, 409),
        ("signup_closed", backd.ForbiddenError, 403),
        ("email_not_verified", backd.ForbiddenError, 403),
        ("cancelled", backd.AuthenticationError, 401),
        ("not_allowed", backd.NotAllowedError, 403),
        ("made_up", backd.BackdError, 502),
    ]:
        await go(c.auth.sign_in_with("google"))
        with pytest.raises(cls) as ei2:
            await go(c.auth.complete_sign_in(f"http://x/?backd_error={code}&backd_provider=google"))
        assert ei2.value.status == status
        assert ei2.value.code == (code if code != "made_up" else "provider_error")
        assert ei2.value.details == [{"path": "provider", "reason": "google"}]

    assert await go(c.auth.complete_sign_in("http://x/?backd_linked=github")) == {"linked": "github"}


async def test_link_provider_sends_the_session(mode: str) -> None:
    c, server, storage = setup(mode)
    storage.set("bds_x")
    server.on("POST /_auth/oauth/github/start", {"authorize_url": "https://p.test/"})
    await go(c.auth.link_provider("github"))
    assert server.last().headers["authorization"] == "Bearer bds_x"
    assert server.body()["intent"] == "link"
    server.on("DELETE /_auth/identities/github", None, 204)
    await go(c.auth.unlink_provider("github", org="acme"))
    assert server.last().url.params["org"] == "acme"


async def test_providers_options_and_id_tokens(mode: str) -> None:
    c, server, storage = setup(mode)
    server.on("GET /_auth/providers", {"items": ["google"], "methods": ["password"], "sso_required": False})
    server.on("GET /_auth/orgs/acme/providers", {"items": ["microsoft"], "sso_required": True})
    assert await go(c.auth.providers()) == ["google"]
    opts = await go(c.auth.options(org="acme"))
    assert opts == {"providers": ["microsoft"], "methods": [], "sso_required": True}

    server.on("POST /_auth/oauth/apple/id-token", session("bds_a"))
    s = await go(c.auth.sign_in_with_id_token("apple", id_token="idt", nonce="n", authorization_code="ac"))
    assert s["token"] == "bds_a"
    assert server.body() == {"id_token": "idt", "nonce": "n", "authorization_code": "ac"}
    storage.set("bds_a")
    server.on("POST /_auth/oauth/google/id-token", {"linked": "google"})
    assert await go(c.auth.link_provider_with_id_token("google", id_token="i", nonce="n")) == {"linked": "google"}
    assert server.body()["intent"] == "link"
    assert server.last().headers["authorization"] == "Bearer bds_a"


async def test_org_clients_have_their_own_token(mode: str) -> None:
    c, server, storage = setup(mode, org="acme")
    assert c.org_ref == "acme"
    server.on("POST /_auth/orgs/acme/login", session("bds_acme", org="o1"))
    await go(c.auth.login("a@x.test", "dev-p4ssw0rd!"))
    assert storage.get() == "bds_acme"

    root, server2, storage2 = setup(mode)
    beta = root.org(" Beta ")
    assert beta is root.org("beta") and beta is beta.org("beta")
    assert beta.org_ref == "beta" and beta.root is root
    server2.on("POST /_auth/orgs/beta/login", session("bds_beta", org="o2"))
    await go(beta.auth.login("a@x.test", "dev-p4ssw0rd!"))
    assert storage2.get() is None and storage2.slot("beta").get() == "bds_beta"
    assert beta.storage.get() == "bds_beta"
    assert storage2.slots() == ["beta", "meta:beta"] or "beta" in storage2.slots()
    with pytest.raises(TypeError):
        root.org(" ")

    class Plain:
        def get(self) -> str | None:
            return None

        def set(self, token: str) -> None: ...
        def remove(self) -> None: ...

    with pytest.raises(TypeError, match="per organization"):
        make_client(mode, Server(), storage=Plain()).org("x")


async def test_closing_an_org_client_closes_the_root(mode: str) -> None:
    root, _, _ = setup(mode)
    closed: list[int] = []
    root.org("beta").on_close(lambda: closed.append(1))
    await go(root.org("beta").aclose() if mode == "async" else root.org("beta").close())
    assert closed == [1]


async def test_sign_up_with_org_keeps_the_session_under_the_org(mode: str) -> None:
    c, server, storage = setup(mode)
    server.on(
        "POST /_auth/orgs",
        {**session("bds_new", org="o9"), "organization": {"id": "o9", "name": "Acme Corp", "slug": "acme-corp"}},
    )
    out = await go(
        c.auth.sign_up_with_org(email="a@x.test", password="dev-p4ssw0rd!", name="Acme Corp", slug="acme-corp")
    )
    assert out["organization"]["id"] == "o9"
    assert out["token"] == "bds_new"
    assert storage.get() is None
    assert storage.slot("acme-corp").get() == "bds_new"
    assert server.body() == {"email": "a@x.test", "password": "dev-p4ssw0rd!", "name": "Acme Corp", "slug": "acme-corp"}

    server.on("POST /_auth/orgs", None, 202)
    with pytest.raises(backd.VerificationRequiredError):
        await go(c.auth.sign_up_with_org(email="b@x.test", password="dev-p4ssw0rd!", name="B", slug="b"))


async def test_org_and_preview_invitation(mode: str) -> None:
    c, server, storage = setup(mode, org="acme")
    storage.set("bds_x")
    server.on("GET /_auth/me", {**USER, "identities": [], "org": {"id": "o1", "name": "Acme", "roles": ["owner"]}})
    org = await go(c.auth.org())
    assert org is not None and org["id"] == "o1"
    server.on("POST /_auth/orgs/acme/invitations/preview", {"org": {"id": "o1", "name": "Acme"}, "roles": ["editor"]})
    assert (await go(c.auth.preview_invitation("tok")))["roles"] == ["editor"]
    assert server.body() == {"token": "tok"}
    anon, _, _ = setup(mode)
    with pytest.raises(TypeError, match="organization is required"):
        await go(anon.auth.preview_invitation("tok"))
    server.on("GET /_auth/me", {**USER, "identities": []})
    assert await go(c.auth.org()) is None


async def test_a_failing_listener_does_not_break_the_others(mode: str, caplog: pytest.LogCaptureFixture) -> None:
    c, server, _ = setup(mode)
    server.on("POST /_auth/login", session())
    seen: list[str] = []

    def bad(e: str, s: Any) -> None:
        raise RuntimeError("boom")

    stop = c.auth.on_auth_change(bad)
    c.auth.on_auth_change(lambda e, s: seen.append(e))
    await go(c.auth.login("a@x.test", "dev-p4ssw0rd!"))
    assert seen == ["SIGNED_IN"]
    assert "listener failed" in caplog.text
    stop()
    stop()  # twice is fine


async def test_api_key_needs_no_sign_in(mode: str) -> None:
    c, server, _ = setup(mode, api_key="bdk_k")
    server.on("GET /_auth/me", {**USER, "identities": []})
    await go(c.auth.me())
    assert server.last().headers["authorization"] == "Bearer bdk_k"
    assert not c.auth.has_session()


async def test_file_storage_round_trip(mode: str, tmp_path: Any) -> None:
    store = backd.FileStorage(tmp_path / "session")
    c, server, _ = setup(mode, storage=store)
    server.on("POST /_auth/login", session("bds_file"))
    await go(c.auth.login("a@x.test", "dev-p4ssw0rd!"))
    again, server2, _ = setup(mode, storage=backd.FileStorage(tmp_path / "session"))  # a new process
    server2.on("GET /_auth/me", {**USER, "identities": []})
    await go(again.auth.me())
    assert server2.last().headers["authorization"] == "Bearer bds_file"
