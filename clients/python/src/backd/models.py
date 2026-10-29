"""The shapes the API answers with, as ``TypedDict``s (they are plain dicts at run time), and the
values a sign-in step is returned as."""

from __future__ import annotations

from collections.abc import Iterator
from dataclasses import dataclass, field
from typing import Any, Generic, Literal, TypedDict, TypeVar

__all__ = [
    "AggregateGroup",
    "AggregateResult",
    "BatchOperation",
    "ConsentChallenge",
    "ConsentRef",
    "Doc",
    "EnrollmentChallenge",
    "Identity",
    "Me",
    "Meta",
    "MfaChallenge",
    "Page",
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


# -- documents -------------------------------------------------------------------------------


class _MetaRequired(TypedDict):
    #: RFC 3339.
    created_at: str
    updated_at: str


class Meta(_MetaRequired, total=False):
    """Server-owned fields of a document."""

    #: Increases on every write; missing only on very old documents.
    version: int
    #: Id of the user who created it (realms with auth enabled).
    owner: str | None
    #: ``user:<id>``, ``key:<name>`` or ``anonymous``.
    created_by: str
    updated_by: str
    #: The organization the document belongs to (collections with ``organization``).
    org: str
    #: The team of the organization the document belongs to (collections with ``team``).
    team: str
    #: The relevance to a ``$search``, in a list sorted with ``order_by="_relevance"``. Never stored.
    score: float
    #: Only on soft-deleted documents (collections with ``soft_delete``).
    deleted_at: str
    deleted_by: str
    #: When it is removed for good (``soft_delete.retention``).
    purge_at: str
    #: The encrypted fields the caller may not see: they come back ``None``, and a ``None`` here is hidden,
    #: not empty. Absent when nothing is hidden.
    redacted: list[str]
    #: Set on a write made by a trigger run: where it sits in a chain of such writes.
    chain: dict[str, Any]


class _DocRequired(TypedDict):
    id: str
    _meta: Meta


class Doc(_DocRequired, total=False):
    """A stored document: the collection's fields plus ``id`` and ``_meta``. The generated types
    (``backd types --lang python``) extend it with the collection's own fields. ``_expanded`` is there on
    a read with ``expand``: the referenced document (``None`` when it is missing or unreadable), a list of
    those for a ``many`` reference, or ``{"items": [...], "truncated": True}`` when the list was longer than
    the server's limit."""

    _expanded: dict[str, Any]


D = TypeVar("D")


@dataclass
class Page(Generic[D]):
    """A page of documents, as the API returns it."""

    items: list[D]
    limit: int
    skip: int
    has_more: bool
    #: With ``has_more``: pass it as ``after`` to get the next page. ``None`` when the ``order_by`` can't be
    #: followed by a cursor (arrays, objects, mixed types): use ``skip`` then.
    next_cursor: str | None = None
    #: With ``count=True``; counts the whole list, not what is after ``after``.
    total: int | None = None

    def __iter__(self) -> Iterator[D]:
        return iter(self.items)

    def __len__(self) -> int:
        return len(self.items)


class _BatchRequired(TypedDict):
    op: Literal["create", "replace", "patch", "delete"]
    collection: str


class BatchOperation(_BatchRequired, total=False):
    """One write for ``db.batch(...)``, applied atomically with the others. ``create`` and ``replace`` need
    ``document``; ``patch`` needs ``patch`` (a JSON Merge Patch). ``replace``, ``patch`` and ``delete`` need
    ``id`` and accept ``if_match``. A ``create`` in a collection with ``custom_id`` carries the new
    document's ``id``."""

    id: str
    document: dict[str, Any]
    patch: dict[str, Any]
    if_match: int | str


class _GroupRequired(TypedDict):
    key: dict[str, str | int | float | bool | None]


class AggregateGroup(_GroupRequired, total=False):
    """One group: ``key`` has an entry per ``group_by`` field (``None`` for documents without a value), and the
    metrics are named ``<metric>_<field>``: ``count``, ``sum_total``, ``avg_total``, ``count_distinct__meta.owner``.
    Sums and averages are exact decimal strings."""

    count: int


class AggregateResult(TypedDict):
    groups: list[AggregateGroup]
    #: How many groups there were before ``limit``.
    total_groups: int
