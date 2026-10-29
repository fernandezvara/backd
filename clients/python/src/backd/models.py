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
    "ConsentInfo",
    "ConsentRef",
    "ConsentState",
    "ConsentText",
    "DataExport",
    "Device",
    "DeviceInput",
    "Doc",
    "EnrollmentChallenge",
    "FileDetails",
    "FileField",
    "FileLink",
    "Identity",
    "JobData",
    "JobResult",
    "JobStep",
    "Me",
    "Meta",
    "MfaChallenge",
    "Org",
    "OrgConnection",
    "OrgInvitation",
    "OrgMember",
    "OrgTeam",
    "OrgTeamMember",
    "OrgTeamRoleGrant",
    "OrgTemplate",
    "OrgTemplateText",
    "Page",
    "PreparedUpload",
    "Profile",
    "ProviderSession",
    "ReauthMethod",
    "RoleOrigin",
    "Session",
    "SessionInfo",
    "SignInResult",
    "User",
    "VersionState",
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


# -- files -----------------------------------------------------------------------------------


class _FileRequired(TypedDict):
    #: ``fl_…``.
    id: str
    #: The sanitized display name.
    name: str
    #: Bytes.
    size: int
    #: Detected from the content.
    type: str
    #: Hex.
    sha256: str
    uploaded_at: str


class VersionState(TypedDict, total=False):
    """One version of an image: ``pending`` (a worker will make it), ``ready`` (made: ``id``, ``size``,
    ``type``, ``width``, ``height``), ``empty`` (only functions make it, none has yet), ``skipped`` (``reason``
    is ``not_an_image`` or ``unsupported_format``) or ``failed`` (``reason`` is ``too_large`` or
    ``decode_error``)."""

    status: Literal["pending", "ready", "empty", "skipped", "failed"]
    reason: str
    #: ``fv_…``, once ready.
    id: str
    size: int
    type: str
    width: int
    height: int
    #: What it was made with.
    params: dict[str, Any]
    fingerprint: str
    #: Made with parameters a function gave (``generate``), not the declared ones.
    custom: bool
    generated_at: str
    #: The copy's permanent address, on a public file once it is ready.
    url: str


class FileDetails(_FileRequired, total=False):
    """What backd keeps of a file in a document's file field."""

    #: Pixels, for an image (as a viewer sees it).
    width: int
    height: int
    #: The state of each version the field declares (``versions:`` in collection.yaml).
    versions: dict[str, VersionState]
    #: True for a file of a public field (``public: true``): it is read at ``url``, with no signed link.
    public: bool
    #: A public file's name as its address ends with it.
    key_name: str
    #: A public file's permanent address, or the signed link of ``file_links=True``.
    url: str
    #: When a signed ``url`` stops working (never set on a public file's address).
    expires_at: str


class FileLink(TypedDict):
    """A link to a file that works without credentials until it expires."""

    url: str
    #: RFC 3339; ``None`` for the address of a public file, which never expires.
    expires_at: str | None


class _FieldRequired(TypedDict):
    name: str
    upload: Literal["proxy", "direct"]
    download: Literal["presigned", "proxy"]
    multiple: bool
    #: 1 for a field that holds one file.
    max_files: int
    #: Bytes per file.
    max_size: int
    #: Allowed content types; empty allows any.
    types: list[str]


class FileField(_FieldRequired, total=False):
    """How a file field takes files: to choose between a proxy and a direct upload, and to refuse a file
    before sending it."""

    #: A direct field that can hold more: files above this many bytes are sent in parts.
    multipart_above: int


class PreparedUpload(TypedDict):
    """What a pending upload gives back: name ``ref`` in the field of a create, ``put`` or ``patch``."""

    id: str
    token: str
    #: ``{"upload": id, "token": token}``: what goes in the document's field.
    ref: dict[str, str]
    #: RFC 3339: unused, it expires then.
    expires_at: str
    #: What backd stored: ``name``, ``size``, ``type``, ``sha256``.
    file: dict[str, Any]


# -- jobs ------------------------------------------------------------------------------------


class _ResultRequired(TypedDict):
    #: ``ok``, ``function_error``, ``timeout``, ``memory``, ``cpu``, ``crash``, ``output_too_large`` or ``bundle``.
    status: str
    duration_ms: int


class JobResult(_ResultRequired, total=False):
    """How an ``async`` job ended, once its ``status`` is ``done``: the same shape a ``sync`` call's own answer
    would carry for the same outcome."""

    #: The function's output, when ``status`` is ``ok``.
    output: Any
    #: The status a ``sync`` call would have answered with; absent only when ``status`` is ``ok``.
    http_status: int
    #: The function's own code (``function_error``) or one of backd's own; absent only when ``status`` is ``ok``.
    code: str
    message: str
    details: list[Any]


class JobStep(TypedDict):
    """One step the function reported with ``ctx.step()``, and how far it got with ``ctx.progress()``."""

    #: 1 for the first step of the attempt (it counts steps left out too).
    n: int
    name: str
    status: Literal["running", "done", "failed", "timed_out", "cancelled"]
    started_at: str
    #: ``None`` while running.
    ended_at: str | None
    duration_ms: int | None
    #: How far the step got.
    current: int
    #: How much there is to do, when the function said.
    total: int | None
    message: str | None
    updated_at: str


class _JobRequired(TypedDict):
    id: str
    #: ``<database>/<name>``.
    function: str
    status: Literal["queued", "running", "done"]
    created_at: str
    result: JobResult | None


class JobData(_JobRequired, total=False):
    """A job as the API returns it: ``POST .../_func/{name}``'s ``202`` body, or ``GET .../_jobs/{id}``."""

    #: How many times a worker has started it.
    attempts: int
    #: When a failed attempt will be retried (the function's ``retry`` policy); ``None`` otherwise.
    next_attempt_at: str | None
    #: What the running (or last) attempt reported with ``ctx.step()``, the last being the current step; at most 100.
    steps: list[JobStep]
    #: How many were left out of the middle when there were more than 100.
    steps_omitted: int


# -- organizations, consents, push, account --------------------------------------------------


class RoleOrigin(TypedDict):
    """Where an effective role came from: ``direct``, ``default`` or a team's name."""

    role: str
    origin: list[str]


class _OrgRequired(TypedDict):
    id: str
    name: str
    slug: str | None
    status: Literal["active", "orphaned", "deleted"]
    created_at: str


class Org(_OrgRequired, total=False):
    """An organization, as the signed-in user sees it."""

    #: The organization requires a second factor of its members.
    require_mfa: bool
    #: The organization's people can only sign in through its connection.
    sso_required: bool
    #: Only in the answer that turned ``sso_required`` on: members with no identity on a connection.
    members_without_sso: int
    #: Only in that answer: the sessions that did not come through a connection, which ended.
    sessions_revoked: int
    #: False when it requires a second factor and this session used none.
    mfa_met: bool
    #: Written by administrators and functions; read-only here.
    claims: dict[str, Any]
    #: The user's effective role names there.
    roles: list[str]
    effective_roles: list[RoleOrigin]
    #: The user's teams (ids).
    teams: list[str]
    #: The team roles that count now, by team id.
    team_roles: dict[str, list[str]]


class _GrantRequired(TypedDict):
    team: str
    role: str
    granted_at: str
    #: Whether it counts now.
    active: bool


# ``from`` is a keyword: the functional syntax keeps the wire name.
_GrantOptional = TypedDict(
    "_GrantOptional",
    {
        # The grant counts from then (always, when absent) and until then (for ever, when absent).
        "from": str,
        "until": str,
        # A user id; absent for administrators and functions.
        "granted_by": str,
    },
    total=False,
)


class OrgTeamRoleGrant(_GrantRequired, _GrantOptional):
    """A team role held by a member: a role within one team, which doesn't need membership of it."""


class _MemberRequired(TypedDict):
    user: str
    #: Direct roles.
    roles: list[str]
    teams: list[str]
    joined_at: str


class OrgMember(_MemberRequired, total=False):
    """A member of an organization."""

    #: Per the realm's ``members_visible_to``.
    email: str
    team_roles: list[OrgTeamRoleGrant]
    effective_roles: list[RoleOrigin]
    #: Roles held directly and also through a team or by default.
    overlap: list[str]
    #: Whether they have a second factor; only for managers.
    mfa: bool
    mfa_roles: list[str]
    invited_by: str


class OrgTeamMember(OrgMember, total=False):
    """A member or role holder of one team."""

    in_team: bool
    roles_in_team: list[str]


class OrgTeam(TypedDict):
    id: str
    name: str
    roles: list[str]
    created_at: str


class OrgTemplateText(TypedDict):
    """The three parts of an email an organization wrote itself, in the logic-less syntax."""

    subject: str
    text: str
    html: str


class OrgTemplate(TypedDict, total=False):
    """An organization's version of a kind of email in a language."""

    kind: str
    locale: str
    version: int
    updated_at: str
    updated_by: str
    #: Only when one template is read.
    subject: str
    text: str
    html: str
    #: Previous ones, newest first (at most 20).
    versions: list[dict[str, Any]]
    #: Only in the answer to a save: what the HTML sanitizer drops from the sample.
    removed: list[str]


class OrgInvitation(TypedDict, total=False):
    id: str
    #: Who may accept: ``user``, ``email`` (whoever holds a verified address) or ``none`` (anyone with the link).
    binding: Literal["user", "email", "none"]
    status: Literal["pending", "used", "revoked", "expired"]
    user: str
    email: str
    roles: list[str]
    teams: list[str]
    max_uses: int
    uses: int
    expires_at: str
    #: Only in the answer to a create or a new link: shown once.
    token: str
    link: str | None


class OrgConnection(TypedDict, total=False):
    """An organization's sign-in connection: its own Google Workspace or Microsoft Entra tenant as a provider for
    its people. The client secret is write-only: only whether it is set comes back."""

    provider: Literal["google", "microsoft"]
    client_id: str
    #: Microsoft: the organization's tenant id.
    tenant: str
    #: Google: the Workspace domain.
    hosted_domain: str
    email_domains: list[str]
    native_client_ids: list[str]
    join_roles: list[str]
    enabled: bool
    client_secret_set: bool
    #: The address to register with the provider (when backd knows its public address).
    redirect_uri: str
    redirect_path: str
    created_at: str
    updated_at: str


class ConsentInfo(TypedDict):
    """A consent the realm asks for, with the version in force."""

    name: str
    #: Needed for an account; optional ones are given and withdrawn at will.
    required: bool
    #: ``version``; ``reaccept`` (people who accepted an older one are asked again); ``reaccept_by`` (a date).
    current: dict[str, Any]
    #: The languages its text exists in.
    locales: list[str]


class ConsentText(TypedDict):
    """The exact text of a version."""

    name: str
    version: str
    #: The language of the text: the best the realm has for the one asked for.
    locale: str
    #: Markdown.
    text: str
    #: SHA-256 of the text: what a consent record keeps as proof.
    hash: str
    #: The version in force.
    current: bool


class _StateRequired(TypedDict):
    #: Consent name to the version the user holds.
    consents: dict[str, str]
    #: Required consents to accept (again).
    to_accept: list[ConsentRef]


class ConsentState(_StateRequired, total=False):
    """What the signed-in user holds and has still to accept."""

    #: With ``consents.state()``, oldest first.
    history: list[dict[str, Any]]


class _DeviceRequired(TypedDict):
    id: str
    platform: Literal["android", "ios", "web"]
    name: str | None
    created_at: str
    last_seen: str


class Device(_DeviceRequired):
    """A device registered for push notifications. Never carries the token."""


class _DeviceInputRequired(TypedDict):
    platform: Literal["android", "ios", "web"]
    #: The FCM registration token, the APNs device token or, for ``web``, the subscription as JSON.
    token: str


class DeviceInput(_DeviceInputRequired, total=False):
    """What an app hands over to register a device. ``name`` is a label to show the user, such as "Ana's Pixel"."""

    name: str


class _ExportRequired(TypedDict):
    id: str
    #: ``ready`` can be downloaded until ``expires_at``; ``failed`` may be asked for again at once.
    status: Literal["queued", "running", "ready", "failed", "expired"]
    created_at: str


class DataExport(_ExportRequired, total=False):
    """An export of the signed-in user's personal data."""

    finished_at: str
    #: When the package is deleted (ready only).
    expires_at: str
    #: Bytes of the zip (ready only).
    size: int
    #: What it holds (ready only): records per part, and ``files``.
    counts: dict[str, int]
    #: What it is doing now (queued or running, as ``account.export(id)`` shows it).
    step: str
