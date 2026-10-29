"""The shapes the API answers with, as ``TypedDict``s (they are plain dicts at run time), and the
values a sign-in step is returned as."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Literal, TypedDict

__all__ = [
    "ConsentChallenge",
    "ConsentRef",
    "EnrollmentChallenge",
    "Identity",
    "Me",
    "MfaChallenge",
    "Profile",
    "ProviderSession",
    "ReauthMethod",
    "Session",
    "SessionInfo",
    "SignInResult",
    "User",
]

ReauthMethod = Literal["totp", "recovery_code", "password", "email_code"]


class ConsentRef(TypedDict):
    """A consent and the version it asks for."""

    name: str
    version: str


class User(TypedDict):
    """A realm user as the user sees themselves."""

    id: str
    email: str
    email_verified: bool
    roles: list[str]
    #: The user's language, one the realm lists (used for their emails).
    locale: str
    #: RFC 3339.
    created_at: str


class Identity(TypedDict):
    """One way the user signs in: a password or an external provider."""

    provider: str
    email: str
    email_verified: bool
    created_at: str
    last_used_at: str | None


class _MeRequired(User):
    identities: list[Identity]
    reauth_methods: list[ReauthMethod]
    sign_in_methods: list[str]


class Me(_MeRequired, total=False):
    """The signed-in user with their sign-in methods, oldest first.

    With ``consents`` declared in the realm it also has ``consents`` (name to the version held) and
    ``to_accept``; for a session scoped to an organization, ``org`` says which and what the person is there.
    """

    consents: dict[str, str]
    to_accept: list[ConsentRef]
    org: dict[str, Any]


class Profile(TypedDict, total=False):
    """What a provider says of a person who just signed up, handed over once. Treat it as a hint."""

    name: str
    given_name: str
    family_name: str
    #: A URL.
    picture: str


class _SessionRequired(TypedDict):
    session_id: str
    #: RFC 3339; pushed forward as the session is used.
    expires_at: str
    user: User


class Session(_SessionRequired, total=False):
    """A new session. The client has already stored its ``token`` (``bds_…``)."""

    token: str
    token_type: Literal["Bearer"]
    #: The organization the session is scoped to: its token reaches that organization and no other.
    org: str


class ProviderSession(Session, total=False):
    """The session a sign-in with a provider gives: a login's, and whether it made the user."""

    new_user: bool
    profile: Profile


class _SessionInfoRequired(TypedDict):
    id: str
    created_at: str
    last_used_at: str
    expires_at: str
    #: The session making this request.
    current: bool


class SessionInfo(_SessionInfoRequired, total=False):
    """One of the user's sessions."""

    #: The id of the organization the session is scoped to (none for an operator's).
    org: str
    #: The provider of the organization's connection that opened it.
    connection: str


@dataclass(frozen=True)
class MfaChallenge:
    """A sign-in that needs a code of the user's second factor: send it with ``auth.verify_mfa``."""

    challenge: str
    methods: list[str] = field(default_factory=list)


@dataclass(frozen=True)
class EnrollmentChallenge:
    """A sign-in of a user whose role requires a second factor and who has none: set one up with
    ``auth.mfa.setup(challenge=…)`` and ``auth.mfa.confirm(…)``, which gives the session."""

    challenge: str


@dataclass(frozen=True)
class ConsentChallenge:
    """A sign-in that is done but has consents to accept first: show their texts
    (``client.consents.text``) and send the acceptance with ``auth.accept_consents``."""

    challenge: str
    consents: list[ConsentRef] = field(default_factory=list)


#: What a first factor leads to: a session, or a second step.
SignInResult = Session | ProviderSession | MfaChallenge | EnrollmentChallenge | ConsentChallenge
