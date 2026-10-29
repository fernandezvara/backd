"""Sign-up, login and session management: ``client.auth``. Source of ``_sync/auth.py``."""

from __future__ import annotations

import json
import logging
import time
from collections.abc import Awaitable, Callable, Mapping
from typing import TYPE_CHECKING, Any, Literal, TypedDict, TypeVar, cast

from .._core import parse_time
from ..errors import (
    AuthenticationError,
    SecondStepRequiredError,
    ValidationError,
    VerificationRequiredError,
)
from ..models import (
    ConsentChallenge,
    EnrollmentChallenge,
    Me,
    MfaChallenge,
    ProviderSession,
    ReauthMethod,
    Session,
    SessionInfo,
    SignInResult,
    User,
)
from ..oauth import Pending, load_pending, new_pkce, oauth_error, parse_return, save_pending
from ..storage import SlotStorage

if TYPE_CHECKING:
    from .client import AsyncClient

__all__ = ["AsyncAuth", "AsyncMagicLink", "AsyncMfa", "AuthEvent", "ReauthInput"]

T = TypeVar("T")

#: What changed: a sign-in (signup or login), a sign-out (logout, logout everywhere, account deleted), or the
#: server refusing the stored token (expired, revoked, or the user disabled).
AuthEvent = Literal["SIGNED_IN", "SIGNED_OUT", "SESSION_EXPIRED"]
AuthListener = Callable[[AuthEvent, Session | None], None]


class _ReauthRequired(TypedDict):
    method: ReauthMethod


class ReauthInput(_ReauthRequired, total=False):
    """A proof of identity: ``totp`` or ``recovery_code`` with a ``code``, ``password`` with a ``password``,
    or ``email_code`` with the ``code`` that ``request_reauth_code`` sent."""

    code: str
    password: str


def _compact(**fields: Any) -> dict[str, Any]:
    """A body without the fields that were not given."""
    return {k: v for k, v in fields.items() if v is not None}


def _auth_base(org: str | None) -> list[str]:
    """The path of the sign-in routes: the realm's, or those of one organization."""
    return ["_auth", "orgs", org] if org else ["_auth"]


class AsyncMfa:
    """The signed-in user's second factor (``client.auth.mfa``). Changes need a fresh proof: see ``auth.reauth``."""

    def __init__(self, auth: AsyncAuth) -> None:
        self._auth = auth

    async def status(self) -> dict[str, Any]:
        """``available``, ``enabled``, ``methods``, ``recovery_codes_left`` and ``required``."""
        return cast(dict[str, Any], (await self._auth._client.request("GET", ["_auth", "mfa"])).data)

    async def setup(self, *, challenge: str | None = None) -> dict[str, Any]:
        """Starts setting an authenticator app up: ``setup_id``, ``secret``, ``otpauth_uri``, ``qr`` (a PNG data
        URI) and ``expires_at``. With the ``challenge`` of an :class:`EnrollmentChallenge` there is no session yet."""
        res = await self._auth._client.request(
            "POST",
            ["_auth", "mfa", "totp", "setup"],
            body={"challenge": challenge} if challenge else None,
            reauth=not challenge,
            auth=not challenge,
        )
        return cast(dict[str, Any], res.data)

    async def confirm(self, *, setup_id: str, code: str, challenge: str | None = None) -> dict[str, Any]:
        """Turns it on with the first code of the app. Returns the ``recovery_codes``, shown once; with a
        ``challenge`` it also ends the sign-in and the client holds the session (``session`` is it), unless the
        user has consents to accept first: then ``consent`` is the :class:`ConsentChallenge` to finish with
        ``auth.accept_consents``."""
        body: dict[str, Any] = {"setup_id": setup_id, "code": code}
        if challenge:
            body["challenge"] = challenge
        res = await self._auth._client.request(
            "POST", ["_auth", "mfa", "totp", "confirm"], body=body, reauth=not challenge, auth=not challenge
        )
        data = cast(dict[str, Any], res.data)
        if data.get("consent_required"):
            consent = ConsentChallenge(data["challenge"], data.get("consents", []))
            return {"recovery_codes": data["recovery_codes"], "consent": consent}
        if data.get("token") or data.get("session_id"):
            codes = data["recovery_codes"]
            session = {k: v for k, v in data.items() if k != "recovery_codes"}
            return {"recovery_codes": codes, "session": await self._auth._signed_in(cast(Session, session))}
        return {"recovery_codes": data["recovery_codes"]}

    async def disable(self) -> None:
        """Turns it off (not for a role that requires it: ``mfa_required_by_role``)."""
        await self._auth._client.request("DELETE", ["_auth", "mfa", "totp"], reauth=True)

    async def regenerate_recovery_codes(self) -> list[str]:
        """Replaces the recovery codes; the new ones are shown once."""
        res = await self._auth._client.request("POST", ["_auth", "mfa", "recovery-codes"], reauth=True)
        return cast(list[str], cast(dict[str, Any], res.data)["recovery_codes"])


class AsyncMagicLink:
    """Sign-in by a link or a code emailed to the user (``client.auth.magic_link``)."""

    def __init__(self, auth: AsyncAuth) -> None:
        self._auth = auth

    async def request(
        self,
        email: str,
        *,
        redirect_to: str | None = None,
        locale: str | None = None,
        org: str | None = None,
        consents: Mapping[str, str] | None = None,
    ) -> str:
        """Asks for a message with a link and a 6-digit code. Always succeeds, whatever the address is. The
        link ends at ``redirect_to`` (``complete_sign_in`` finishes it); the code is typed in this program with
        ``verify_code``. The PKCE verifier waits in ``oauth_storage`` for both. Returns the request id.

        ``consents`` are accepted for the account a new address gets. ``org`` is the organization the person
        signs in to (an id or a slug; the client's own when it was made with one): only its members are sent a
        link, and the session it gives is scoped to it."""
        client = self._auth._client
        verifier, challenge = new_pkce()
        body = _compact(
            email=email, redirect_to=redirect_to, code_challenge=challenge, locale=locale, consents=consents
        )
        res = await client.request("POST", [*self._auth.base(org), "magic-link"], body=body, auth=False)
        request_id = cast(dict[str, Any], res.data)["request_id"]
        save_pending(client.oauth_storage, Pending(verifier, "magic-link", "signin", request_id))
        return cast(str, request_id)

    async def verify_code(self, code: str) -> SignInResult:
        """Signs in with the code of the message, in the program that asked. The answer is the session or a
        second step, like ``sign_in``'s. A wrong code is ``invalid_code`` (five end the request); a request that
        is unknown, used or expired is ``invalid_magic_link``."""
        client = self._auth._client
        pending = load_pending(client.oauth_storage)
        if pending is None or not pending.request_id:
            raise ValidationError("ask for a link first, in this client: request(email)", status=400, code="no_request")
        body = {"request_id": pending.request_id, "code": code.strip(), "code_verifier": pending.verifier}
        res = await client.request("POST", ["_auth", "magic-link", "verify"], body=body, auth=False)
        client.oauth_storage.remove()
        return await self._auth._step(res.data)


class _Password:
    """Setting and removing the password of an account that may sign in another way (``auth.password``)."""

    def __init__(self, auth: AsyncAuth) -> None:
        self._auth = auth

    async def set(self, new_password: str) -> None:
        await self._auth.change_password(new_password)

    async def remove(self) -> None:
        await self._auth._client.request("DELETE", ["_auth", "password"], reauth=True)


class AsyncAuth:
    """Sign-up, login and session management: ``client.auth``."""

    def __init__(self, client: AsyncClient) -> None:
        self._client = client
        self._listeners: list[AuthListener] = []
        #: The session this client signed in with, as the sign-in answered (``org`` is the organization it is
        #: scoped to), or ``None``. It is kept in memory only: a program that has just started knows nothing
        #: until it asks the server (:meth:`me`, :meth:`org`).
        self.current: Session | None = None
        #: The signed-in user's second factor: ``client.auth.mfa``.
        self.mfa = AsyncMfa(self)
        #: Sign-in by an emailed link and code: ``client.auth.magic_link``.
        self.magic_link = AsyncMagicLink(self)
        #: Setting and removing the password.
        self.password = _Password(self)

    def base(self, org: str | None = None) -> list[str]:
        """The path of the sign-in routes for an organization: the one given, else the client's, else the realm's."""
        return _auth_base((org or "").strip() or self._client.org_ref)

    # -- sign-up and sign-in ---------------------------------------------------------------

    async def signup(
        self,
        email: str,
        password: str,
        *,
        invitation: str | None = None,
        locale: str | None = None,
        redirect_to: str | None = None,
        org: str | None = None,
        consents: Mapping[str, str] | None = None,
    ) -> Session:
        """Creates an account and signs in. Realms with ``signup: invite`` need an ``invitation`` token.
        ``locale`` (such as ``es`` or ``es-MX``) is the language to use for the user; the server maps it silently
        to one the realm lists. ``redirect_to`` is where the page after the verification link may send the user
        (it must be within the realm's ``email.allowed_redirects``).

        A realm that requires verified addresses creates the account but starts no session: this raises
        :class:`VerificationRequiredError`, nothing is stored, and the user signs in after following the link in
        the email they were sent. A user who has a second step to take raises :class:`SecondStepRequiredError`
        carrying the challenge. ``consents`` are the consents accepted (``{"terms": "2026-10"}``): every required
        one of the realm must be there at its current version, or this raises :class:`ConsentRequiredError`.
        ``org`` is the organization the person joins: the sign-up goes to ``/_auth/orgs/{org}/signup``, which
        needs the organization's ``invitation``, and the session is scoped to it."""
        body = _compact(
            email=email,
            password=password,
            consents=consents,
            invitation=invitation,
            locale=locale,
            redirect_to=redirect_to,
        )
        res = await self._client.request("POST", [*self.base(org), "signup"], body=body, auth=False)
        if res.status == 202:
            raise VerificationRequiredError(
                "the account was created: verify the email address, then sign in",
                status=202,
                code="verification_required",
            )
        return self._session(await self._step(res.data))

    async def sign_up_with_org(
        self,
        *,
        email: str,
        password: str,
        name: str,
        slug: str,
        invitation: str | None = None,
        locale: str | None = None,
        redirect_to: str | None = None,
        consents: Mapping[str, str] | None = None,
    ) -> dict[str, Any]:
        """Creates an account and an organization for a new person and signs in to it: how a company starts.
        The realm's sign-up must be open; ``slug`` is the name people will type to sign in (``acme-corp``) and is
        unique (``slug_taken``, a ``ConflictError``, makes nothing). The session is scoped to the new
        organization and stored by the client; the answer holds it with the ``organization``. A realm that
        verifies addresses first creates both and raises :class:`VerificationRequiredError`."""
        body = _compact(
            email=email,
            password=password,
            name=name,
            slug=slug,
            invitation=invitation,
            locale=locale,
            redirect_to=redirect_to,
            consents=consents,
        )
        res = await self._client.request("POST", ["_auth", "orgs"], body=body, auth=False)
        if res.status == 202:
            raise VerificationRequiredError(
                "the account and the organization were created: verify the email address, then sign in",
                status=202,
                code="verification_required",
            )
        data = dict(cast(dict[str, Any], res.data))
        organization = data.pop("organization", None)
        # The session is the new organization's: kept under its own client (named as the person will type it:
        # the slug) when the storage can, else in this one.
        target = self._client.org(slug).auth if isinstance(self._client.root.storage, SlotStorage) else self
        session = target._session(await target._step(data))
        return {**session, "organization": organization}

    async def find_orgs(self, email: str, *, locale: str | None = None) -> None:
        """Asks the server to email an address the organizations it belongs to, each with the link to its
        sign-in page: for someone on a new device who does not remember the company. It always succeeds,
        whatever the address is; the realm needs ``organizations.signin_link``."""
        await self._accepted(["_auth", "orgs", "find"], _compact(email=email, locale=locale))

    async def preview_invitation(self, token: str, *, org: str | None = None) -> dict[str, Any]:
        """What an invitation to an organization offers, before the person has signed in: the ``org`` (id and
        name), the ``roles`` and the ``teams``, so the app can ask "Join Acme as editor?". Every token that does
        not work fails alike with ``invalid_invitation``."""
        base = self.base(org)
        if len(base) < 3:
            raise TypeError("preview_invitation: the organization is required (client `org` or the `org` argument)")
        res = await self._client.request("POST", [*base, "invitations", "preview"], body={"token": token}, auth=False)
        return cast(dict[str, Any], res.data)

    async def org(self) -> dict[str, Any] | None:
        """The organization this session is scoped to, and what the person is there (``id``, ``name``, ``slug``,
        ``roles``, ``teams``); ``None`` for a session that is not scoped. It asks the server, so it is right
        after a restart too."""
        org = (await self.me()).get("org")
        if org and org.get("name") and self._client.org_ref:
            await self._remember({"name": org["name"], "org": org["id"]})
        return org

    async def _step(self, data: Any) -> SignInResult:
        """What a first factor answered: the session (stored), or the second step the user has to take."""
        d = data or {}
        if d.get("mfa_required"):
            return MfaChallenge(d["challenge"], d.get("methods") or [])
        if d.get("mfa_enrollment_required"):
            return EnrollmentChallenge(d["challenge"])
        if d.get("consent_required"):
            return ConsentChallenge(d["challenge"], d.get("consents") or [])
        return await self._signed_in(d)

    def _session(self, result: SignInResult) -> Session:
        """Turns a step into the session a method promises, or raises :class:`SecondStepRequiredError`."""
        if isinstance(result, ConsentChallenge):
            raise SecondStepRequiredError(
                "consents must be accepted: show client.consents.text(name, version) and call "
                "auth.accept_consents(challenge, consents)",
                status=200,
                code="consent_required",
                challenge=result.challenge,
                consents=cast("list[dict[str, str]]", result.consents),
            )
        if isinstance(result, EnrollmentChallenge):
            raise SecondStepRequiredError(
                "this account must set up a second factor: auth.mfa.setup(challenge=…)",
                status=200,
                code="mfa_enrollment_required",
                challenge=result.challenge,
            )
        if isinstance(result, MfaChallenge):
            raise SecondStepRequiredError(
                "a code of the second factor is needed: auth.verify_mfa(challenge, method, code)",
                status=200,
                code="mfa_required",
                challenge=result.challenge,
                methods=result.methods,
            )
        return result

    async def verify_mfa(self, challenge: str, method: Literal["totp", "recovery_code"], code: str) -> ProviderSession:
        """Finishes a sign-in that answered with an :class:`MfaChallenge`: the code of the user's authenticator
        app (``totp``) or a recovery code (``recovery_code``). Returns the session (a provider's also has
        ``new_user``, a recovery code ``recovery_codes_left``). Raises ``invalid_code`` (a ``ValidationError``;
        ``details`` has the attempts left), ``invalid_challenge`` after five wrong ones or five minutes, and
        ``mfa_locked`` (a ``RetryableError``, 429) after ten wrong codes in an hour."""
        res = await self._client.request(
            "POST",
            ["_auth", "mfa", "verify"],
            body={"challenge": challenge, "method": method, "code": code},
            auth=False,
        )
        return cast(ProviderSession, await self._signed_in(res.data))

    async def accept_consents(self, challenge: str, consents: Mapping[str, str]) -> ProviderSession:
        """Finishes a sign-in that answered with a :class:`ConsentChallenge`: the consents the user accepted, as
        ``{name: version}``, with every one the challenge named at its version. Returns the session. Raises
        :class:`ConsentRequiredError` when one is missing (the challenge stays valid) and ``invalid_challenge``
        after five minutes."""
        res = await self._client.request(
            "POST", ["_auth", "consents", "accept"], body={"challenge": challenge, "consents": consents}, auth=False
        )
        return cast(ProviderSession, await self._signed_in(res.data))

    async def with_proof(
        self,
        ask: Callable[[list[str]], Awaitable[ReauthInput]],
        action: Callable[[], Awaitable[T]],
    ) -> T:
        """Runs a sensitive action, asking the user for a fresh proof when the server wants one: ``ask`` gets the
        proofs the user can give (``totp``, ``recovery_code``, ``password`` or ``email_code``) and returns the one
        to use, as :meth:`reauth` takes it; the action is then tried once more."""
        try:
            return await action()
        except AuthenticationError as e:
            if e.code != "reauth_required":
                raise
        me = await self.me()
        await self.reauth(await ask(list(me.get("reauth_methods", []))))
        return await action()

    async def sign_in(
        self, email: str, password: str, *, org: str | None = None, invitation: str | None = None
    ) -> SignInResult:
        """Signs in with email and password. The answer is the session, or, for a user with a second factor, a
        :class:`MfaChallenge` to finish with :meth:`verify_mfa`, or an :class:`EnrollmentChallenge`: check its
        type. Nothing is stored until a session. ``org`` is the organization the person signs in to (an id or a
        slug; the client's own when it was made with one): the session, or the second step, is scoped to it. A
        person who is not a member, an organization that is not there and a wrong password all fail alike, with
        ``invalid_credentials``. A right password that an administrator expired raises
        :class:`PasswordExpiredError`."""
        body = _compact(email=email, password=password, invitation=invitation)
        res = await self._client.request("POST", [*self.base(org), "login"], body=body, auth=False)
        return await self._step(res.data)

    async def login(
        self, email: str, password: str, *, org: str | None = None, invitation: str | None = None
    ) -> Session:
        """Signs in with email and password and promises a session: a user who needs a second step makes this
        raise :class:`SecondStepRequiredError` (use :meth:`sign_in` to get the step as a value)."""
        return self._session(await self.sign_in(email, password, org=org, invitation=invitation))

    # -- providers -------------------------------------------------------------------------

    async def sign_in_with(
        self,
        provider: str,
        *,
        redirect_to: str | None = None,
        invitation: str | None = None,
        locale: str | None = None,
        consents: Mapping[str, str] | None = None,
        org: str | None = None,
        navigate: Callable[[str], Awaitable[None]] | None = None,
    ) -> str:
        """Starts signing in with a provider the realm lists (``google``, ``microsoft`` or ``apple``): makes a
        PKCE verifier, keeps it in ``oauth_storage`` for the return, and returns the provider's address, which
        ``navigate`` is given when it is (``webbrowser.open``, for a tool). The person comes back to
        ``redirect_to`` (it must be within the realm's ``sign_in.allowed_redirects``; the realm's
        ``sign_in.app_url`` when omitted), where :meth:`complete_sign_in` finishes the sign-in. ``org`` signs in
        through the connection of that organization instead of the realm's own providers."""
        return await self._start_oauth(provider, "signin", redirect_to, invitation, locale, consents, org, navigate)

    async def link_provider(
        self,
        provider: str,
        *,
        redirect_to: str | None = None,
        org: str | None = None,
        navigate: Callable[[str], Awaitable[None]] | None = None,
    ) -> str:
        """Adds a provider to the signed-in user as another way to sign in. The same flow as
        :meth:`sign_in_with`; :meth:`complete_sign_in` then returns ``{"linked": provider}``."""
        return await self._start_oauth(provider, "link", redirect_to, None, None, None, org, navigate)

    async def _start_oauth(
        self,
        provider: str,
        intent: Literal["signin", "link"],
        redirect_to: str | None,
        invitation: str | None,
        locale: str | None,
        consents: Mapping[str, str] | None,
        org: str | None,
        navigate: Callable[[str], Awaitable[None]] | None,
    ) -> str:
        client = self._client
        verifier, challenge = new_pkce()
        body = _compact(
            redirect_to=redirect_to,
            code_challenge=challenge,
            intent=intent,
            invitation=invitation,
            locale=locale,
            consents=consents,
        )
        base = _auth_base(org) if intent == "link" else self.base(org)
        res = await client.request("POST", [*base, "oauth", provider, "start"], body=body, auth=intent == "link")
        save_pending(client.oauth_storage, Pending(verifier, provider, intent))
        url = cast(str, cast(dict[str, Any], res.data)["authorize_url"])
        if navigate is not None:
            await navigate(url)
        return url

    async def complete_sign_in(self, uri: str) -> SignInResult | dict[str, str] | None:
        """Finishes a sign-in with a provider (or a magic link) on the address backd sent the person back to,
        ``uri``. It reads the outcome from the address: a sign-in is traded for a session (stored by the client,
        like a login's) and returned, with ``new_user`` and, for a new user, the ``profile`` the provider gave;
        linking returns ``{"linked": provider}``; a failure raises the error the code means (``account_exists``
        and ``link_conflict`` are a ``ConflictError``, ``signup_closed``, ``email_not_verified`` and
        ``signin_refused`` a ``ForbiddenError``, ``cancelled`` an ``AuthenticationError``, …; ``code`` is the
        code). Returns ``None`` for an address that has nothing to finish.

        An address that does not belong to a sign-in this client started (no PKCE verifier waits) raises a
        ``ValidationError`` whose ``code`` is ``wrong_device``."""
        ret = parse_return(uri)
        if ret is None:
            return None
        storage = self._client.oauth_storage
        pending = load_pending(storage)
        storage.remove()
        if ret.error:
            raise oauth_error(ret.error, ret.provider or (pending.provider if pending else None))
        if ret.linked:
            return {"linked": ret.linked}
        if pending is None:
            raise ValidationError(
                "this address does not belong to a sign-in this client started: finish it where you started it",
                status=400,
                code="wrong_device",
            )
        res = await self._client.request(
            "POST", ["_auth", "token"], body={"code": ret.code, "code_verifier": pending.verifier}, auth=False
        )
        # A sign-in to an organization from a client that is not bound to one belongs to that organization's client.
        client = self._client
        target = self
        if ret.org and not client.org_ref and isinstance(client.root.storage, SlotStorage):
            target = client.org(ret.org).auth
        return await target._step(res.data)

    async def unlink_provider(self, provider: str, *, org: str | None = None) -> None:
        """Removes one of the user's ways to sign in (``password`` included). The last one can't be removed: that
        raises a ``ConflictError`` whose ``code`` is ``last_sign_in_method``. ``org`` selects the one that came
        through an organization's connection (a user holds one per provider per organization)."""
        await self._client.request(
            "DELETE", ["_auth", "identities", provider], query={"org": org} if org else None, reauth=True
        )

    async def providers(self, *, org: str | None = None) -> list[str]:
        """The providers the app may offer right now, for the realm or, with ``org``, for one organization's
        connections: names such as ``google`` and ``microsoft``, only those that are configured and usable."""
        return cast(list[str], (await self.options(org=org))["providers"])

    async def options(self, *, org: str | None = None) -> dict[str, Any]:
        """What an organization's sign-in page can offer: ``providers`` and the other ways in, ``methods``
        (``password`` and, when the realm has them, ``magic_link``), and ``sso_required``: the organization's
        people sign in through its connection only; then ``providers`` holds just its buttons and ``methods``
        is empty. ``org`` defaults to the client's own; without either the realm's providers are listed."""
        res = await self._client.request("GET", [*self.base(org), "providers"], auth=False)
        d = cast(dict[str, Any], res.data)
        return {
            "providers": d.get("items") or [],
            "methods": d.get("methods") or [],
            "sso_required": d.get("sso_required") is True,
        }

    async def sign_in_with_id_token(
        self,
        provider: str,
        *,
        id_token: str,
        nonce: str,
        authorization_code: str | None = None,
        invitation: str | None = None,
        locale: str | None = None,
        consents: Mapping[str, str] | None = None,
        org: str | None = None,
    ) -> SignInResult:
        """Signs in with the ID token a mobile app got from the platform sign-in (Sign in with Apple, Google
        Sign-In, MSAL), with the raw ``nonce`` it started that sign-in with. For Apple, also the
        ``authorization_code`` its SDK gave. Fails with ``invalid_token`` (an ``AuthenticationError``) when the
        token does not verify, and with ``account_exists``, ``signup_closed``, … as :meth:`complete_sign_in`."""
        body = _compact(
            id_token=id_token,
            nonce=nonce,
            authorization_code=authorization_code,
            invitation=invitation,
            locale=locale,
            consents=consents,
        )
        res = await self._client.request(
            "POST", [*self.base(org), "oauth", provider, "id-token"], body=body, auth=False
        )
        return await self._step(res.data)

    async def link_provider_with_id_token(
        self, provider: str, *, id_token: str, nonce: str, authorization_code: str | None = None, org: str | None = None
    ) -> dict[str, Any]:
        """Adds a provider to the signed-in user with the ID token of the platform sign-in."""
        body = _compact(id_token=id_token, nonce=nonce, authorization_code=authorization_code, intent="link")
        res = await self._client.request("POST", [*_auth_base(org), "oauth", provider, "id-token"], body=body)
        return cast(dict[str, Any], res.data)

    # -- email flows -----------------------------------------------------------------------

    async def _accepted(self, path: list[str], body: dict[str, Any]) -> None:
        """A call that needs no session and answers with no body."""
        await self._client.request("POST", path, body=_compact(**body), auth=False)

    async def resend_verification(self, email: str, *, redirect_to: str | None = None) -> None:
        """Asks for the verification email again. Always succeeds, whatever the address is (unknown, disabled,
        already verified), so it can't be used to find out who is registered."""
        await self._accepted(["_auth", "verify-email", "resend"], {"email": email, "redirect_to": redirect_to})

    async def verify_email(self, token: str) -> None:
        """Verifies an address with the token of the link in the email: for apps that host their own page
        (``email.links`` in ``realm.yaml``). Starts no session. An expired, used or unknown token raises a
        ``ValidationError`` whose ``code`` is ``invalid_token``."""
        await self._accepted(["_auth", "verify-email"], {"token": token})

    async def request_password_reset(
        self, email: str, *, redirect_to: str | None = None, org: str | None = None
    ) -> None:
        """Asks for a password reset email. Always succeeds, whatever the address is. Raises a
        ``RetryableError`` once the realm's limits for reset requests (per address and per client) are reached.
        ``org`` is the organization whose page the person asks from: its members get the organization's context
        in the email."""
        await self._accepted(
            [*self.base(org), "reset-password", "request"], {"email": email, "redirect_to": redirect_to}
        )

    async def reset_password(self, token: str, password: str) -> None:
        """Sets a new password with the token of a reset link. Ends every session of the user, verifies their
        address and starts no session: log in afterwards. A password the policy refuses raises a
        ``ValidationError`` and leaves the token usable; a bad token has the ``invalid_token`` code."""
        await self._accepted(["_auth", "reset-password"], {"token": token, "password": password})

    async def update_password(
        self,
        email: str,
        password: str,
        new_password: str,
        *,
        code: str | None = None,
        method: Literal["totp", "recovery"] | None = None,
    ) -> None:
        """Changes the password with the current one, signed out: for a password that expired for the user
        alone (``password_expired`` at sign-in) or one the user wants to replace without a session. An account
        with a second factor also gives ``code`` (a recovery code with ``method="recovery"``). Ends every session
        of the user and starts none: log in afterwards. A password the realm's expiry covers (a leak) is refused
        with ``password_expired``: use :meth:`request_password_reset`."""
        await self._accepted(
            ["_auth", "password", "update"],
            {"email": email, "password": password, "new_password": new_password, "code": code, "method": method},
        )

    async def request_email_change(self, new_email: str, *, redirect_to: str | None = None) -> None:
        """Asks to change the signed-in user's email address (realms with ``account.allow_email_change``). Needs a
        fresh proof: call :meth:`reauth` first. Nothing changes until the link sent to the new address is used
        (see :meth:`confirm_email_change`)."""
        await self._client.request(
            "POST", ["_auth", "email"], body=_compact(new_email=new_email, redirect_to=redirect_to), reauth=True
        )

    async def confirm_email_change(self, token: str) -> None:
        """Confirms an email change with the token sent to the new address: the address changes and every session
        of the user ends."""
        await self._accepted(["_auth", "confirm-email-change"], {"token": token})

    async def revert_email_change(self, token: str) -> None:
        """Undoes an email change with the token sent to the old address: restores it, ends every session and
        makes the password unusable until it is reset (a reset email is sent to the restored address)."""
        await self._accepted(["_auth", "revert-email-change"], {"token": token})

    async def accept_invitation(self, token: str, password: str, *, locale: str | None = None) -> None:
        """Accepts an invitation that was emailed (admin ``invitations.send``), with the token of its link:
        creates the account for the invited address, already verified, and starts no session."""
        await self._accepted(["_auth", "accept-invitation"], {"token": token, "password": password, "locale": locale})

    # -- the session -----------------------------------------------------------------------

    async def logout(self) -> None:
        """Ends the current session. The stored token is removed even if the server can't be reached."""
        try:
            await self._client.request("POST", ["_auth", "logout"], expire=False)
        except AuthenticationError:
            pass  # a session the server no longer knows is logged out already
        finally:
            await self._signed_out("SIGNED_OUT")

    async def logout_all(self) -> None:
        """Ends every session of the user, on every device."""
        await self._client.request("POST", ["_auth", "logout-all"])
        await self._signed_out("SIGNED_OUT")

    async def me(self) -> Me:
        """The signed-in user, with their sign-in methods in ``identities``; for a session scoped to an
        organization, ``org`` says which and what the person is there."""
        return cast(Me, (await self._client.request("GET", ["_auth", "me"])).data)

    async def update_me(self, *, locale: str | None = None) -> User:
        """Changes the signed-in user's own settings: today their language, which must be one the realm lists
        (case is ignored). Anything else is a ``ValidationError`` with code ``invalid_locale`` and the allowed
        languages in ``details``."""
        res = await self._client.request("PATCH", ["_auth", "me"], body=_compact(locale=locale))
        return cast(User, res.data)

    async def delete_account(self) -> None:
        """Deletes the signed-in user's account: it is **deactivated** (disabled, its sessions end) and all data
        is kept. Erasing a user's data is an administrator's action. Needs a fresh proof: call :meth:`reauth`
        first (a ``reauth_required`` error otherwise)."""
        await self._client.request("DELETE", ["_auth", "me"], reauth=True)
        await self._signed_out("SIGNED_OUT")

    async def change_password(self, new_password: str) -> None:
        """Changes the password. This session stays valid; all others end. Needs a fresh proof: call
        :meth:`reauth` first."""
        await self._client.request("POST", ["_auth", "password"], body={"new_password": new_password}, reauth=True)

    async def reauth(self, proof: ReauthInput) -> dict[str, str]:
        """Proves who the signed-in user is again, before a sensitive action (changing the password or the
        address, deleting the account, unlinking a sign-in method, managing the second factor, creating an API
        key as an administrator). ``method`` is the strongest proof the user has (see ``reauth_methods`` of
        :meth:`me`): ``totp`` or ``recovery_code`` with a second factor, ``password``, or ``email_code`` after
        :meth:`request_reauth_code`. The client keeps the proof and sends it with the sensitive requests while it
        is good (the realm's ``reauth.ttl``, 5 minutes by default), so one prompt covers a settings page. A wrong
        proof is an ``AuthenticationError`` (``invalid_credentials``) or a ``ValidationError``
        (``invalid_code``) and counts against the same limits as signing in. A password an administrator expired
        is a :class:`PasswordExpiredError`. Returns ``reauth_token`` and ``expires_at``."""
        body = _compact(method=proof["method"], code=proof.get("code"), password=proof.get("password"))
        res = await self._client.request("POST", ["_auth", "reauth"], body=body)
        data = cast(dict[str, str], res.data)
        self._client._proof = (data["reauth_token"], parse_time(data["expires_at"]))
        return {"reauth_token": data["reauth_token"], "expires_at": data["expires_at"]}

    def has_fresh_proof(self) -> bool:
        """Whether the client holds a proof that is still good (see :meth:`reauth`)."""
        proof = self._client._proof
        return proof is not None and proof[1] > time.time()

    async def request_reauth_code(self) -> None:
        """Emails a code to a user who has no password and no second factor, to give to :meth:`reauth` with
        ``method="email_code"``."""
        await self._client.request("POST", ["_auth", "reauth", "email-code"])

    async def sessions(self) -> list[SessionInfo]:
        """The user's active sessions, newest first."""
        res = await self._client.request("GET", ["_auth", "sessions"])
        return cast("list[SessionInfo]", cast(dict[str, Any], res.data)["items"])

    async def revoke_session(self, session_id: str) -> None:
        """Ends one of the user's sessions, for example a lost device."""
        await self._client.request("DELETE", ["_auth", "sessions", session_id])

    def token(self) -> str | None:
        """The stored session token, if any."""
        return self._client.storage.get() or None

    def has_session(self) -> bool:
        """Whether this client holds a token. A program that has just started knows nothing more: ask the
        server with :meth:`me`."""
        return bool(self._client.storage.get())

    def on_auth_change(self, listener: AuthListener) -> Callable[[], None]:
        """Calls ``listener(event, session)`` on sign-in, sign-out and session expiry. Returns a function that
        stops listening."""
        self._listeners.append(listener)

        def stop() -> None:
            if listener in self._listeners:
                self._listeners.remove(listener)

        return stop

    # -- state -----------------------------------------------------------------------------

    async def _signed_in(self, session: Any) -> Session:
        client = self._client
        client._proof = None
        client._credentials_changed()  # what was read as someone else (or no one) isn't theirs
        token = session.get("token")
        if token:
            client.storage.set(token)
        self.current = session
        await self._remember({"org": session.get("org"), "expires_at": session.get("expires_at")})
        self._emit("SIGNED_IN", session)
        return cast(Session, session)

    async def _remember(self, fields: dict[str, Any]) -> None:
        """Keeps a note of the session beside the token of an organization's client."""
        client = self._client
        storage = client.root.storage
        if not client.org_ref or not isinstance(storage, SlotStorage):
            return
        note = storage.slot("meta:" + client.org_ref)
        try:
            held = json.loads(note.get() or "{}")
        except ValueError:
            held = {}  # a damaged note is replaced
        note.set(json.dumps({**held, **{k: v for k, v in fields.items() if v is not None}}))

    async def _signed_out(self, event: AuthEvent) -> None:
        client = self._client
        client._proof = None
        client._credentials_changed()
        client.storage.remove()
        self.current = None
        storage = client.root.storage
        if client.org_ref and isinstance(storage, SlotStorage):
            storage.slot("meta:" + client.org_ref).remove()
        self._emit(event, None)

    async def _expired(self) -> None:
        """Called by the client when the server refuses the stored token."""
        await self._signed_out("SESSION_EXPIRED")

    def _emit(self, event: AuthEvent, session: Session | None) -> None:
        for listener in list(self._listeners):
            try:
                listener(event, session)
            except Exception:  # a failing listener must not break the client or the other listeners
                logging.getLogger("backd").exception("an on_auth_change listener failed")
