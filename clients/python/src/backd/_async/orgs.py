"""Organizations of the signed-in user: ``client.orgs``."""

from __future__ import annotations

import json
from collections.abc import Sequence
from typing import TYPE_CHECKING, Any, Literal, cast

from ..models import (
    Org,
    OrgConnection,
    OrgInvitation,
    OrgMember,
    OrgTeam,
    OrgTeamMember,
    OrgTemplate,
    OrgTemplateText,
)
from ..storage import SlotStorage

if TYPE_CHECKING:
    from .client import AsyncClient

__all__ = [
    "AsyncOrgInvitations",
    "AsyncOrgMembers",
    "AsyncOrgSso",
    "AsyncOrgTeams",
    "AsyncOrgTemplates",
    "AsyncOrgs",
    "connection_body",
]

Provider = Literal["google", "microsoft"]


def connection_body(
    client_id: str,
    *,
    client_secret: str | None = None,
    tenant: str | None = None,
    hosted_domain: str | None = None,
    email_domains: Sequence[str] | None = None,
    native_client_ids: Sequence[str] | None = None,
    join_roles: Sequence[str] | None = None,
    enabled: bool | None = None,
) -> dict[str, Any]:
    """The body of a connection call: what is left out is not sent."""
    given = {
        "client_secret": client_secret, "tenant": tenant, "hosted_domain": hosted_domain,
        "email_domains": email_domains, "native_client_ids": native_client_ids, "join_roles": join_roles,
        "enabled": enabled,
    }  # fmt: skip
    return {"client_id": client_id, **{k: v for k, v in given.items() if v is not None}}


def _compact(**fields: Any) -> dict[str, Any]:
    return {k: v for k, v in fields.items() if v is not None}


class AsyncOrgs:
    """Organizations of the signed-in user: ``client.orgs``. Members, teams, templates, invitations and connections
    hang off one organization: ``client.orgs.members(id)``, ``client.orgs.teams(id)``, ``client.orgs.invitations(id)``.
    A session works in the one organization it was opened for, so its documents need no organization named; teams of
    it are reached with ``collection.in_team(id)``."""

    def __init__(self, client: AsyncClient) -> None:
        self._client = client

    async def create(self, name: str, *, slug: str | None = None) -> Org:
        """Creates an organization; the caller holds the realm's ``creator_roles`` in it."""
        res = await self._client.request("POST", ["_orgs"], body=_compact(name=name, slug=slug))
        data = cast(dict[str, Any], res.data)
        session = data.get("session")
        # The organization comes with a session of its own: keep it, so ``client.org(slug)`` (its id when it has no
        # slug) is signed in.
        if session and session.get("token") and isinstance(self._client.root.storage, SlotStorage):
            org_client = self._client.org(data.get("slug") or data["id"])
            await org_client.auth._signed_in(
                {
                    "token": session["token"],
                    "session_id": "",
                    "expires_at": session.get("expires_at"),
                    "org": session.get("org"),
                }
            )
        return cast(Org, data)

    def signed_in(self) -> list[dict[str, Any]]:
        """The organizations this program holds a session for, as clients made with ``client.org``: one entry for
        each, with ``ref``, the ``id`` the sign-in answered (when it did), when the session ends (``expires_at``)
        and, once ``auth.org()`` has been asked, its ``name``. It is what an organization switcher lists; the API
        never lists the organizations a person is in."""
        storage = self._client.root.storage
        if not isinstance(storage, SlotStorage):
            return []
        out: list[dict[str, Any]] = []
        for ref in (n for n in storage.slots() if not n.startswith("meta:")):
            try:
                meta = json.loads(storage.slot("meta:" + ref).get() or "{}")
            except ValueError:
                meta = {}  # a damaged note is as if there was none
            out.append(
                {"ref": ref, "id": meta.get("org"), "expires_at": meta.get("expires_at"), "name": meta.get("name")}
            )
        return out

    async def get(self, org: str) -> Org:
        """One organization. :class:`ForbiddenError` (``not_org_member``) for a non-member and an unknown
        organization alike."""
        return cast(Org, (await self._client.request("GET", ["_orgs", org])).data)

    async def update(
        self,
        org: str,
        *,
        name: str | None = None,
        slug: str | None = None,
        require_mfa: bool | None = None,
        sso_required: bool | None = None,
        session_max_age: int | None = None,
    ) -> Org:
        """Renames an organization, changes its slug, sets ``require_mfa`` or shortens the lifetime of its sessions
        with ``session_max_age`` (seconds, at least 3600; ``0`` goes back to the realm's). The last two and
        ``require_mfa`` ask for a fresh proof of identity: see ``client.auth.with_proof``. ``sso_required=True`` makes
        the organization's connection the only way in: it needs a working connection and a session opened through
        one (``409 sso_not_ready``), and the answer counts the members who can't come in (``members_without_sso``,
        listed by ``members(id).list(sso=False)``)."""
        body = _compact(
            name=name, slug=slug, require_mfa=require_mfa, sso_required=sso_required, session_max_age=session_max_age
        )
        sensitive = require_mfa is not None or sso_required is not None or session_max_age is not None
        res = await self._client.request("PATCH", ["_orgs", org], body=body, reauth=sensitive)
        return cast(Org, res.data)

    async def leave(self, org: str) -> None:
        """Leaves an organization (allowed without a second factor; ``last_org_manager`` for the last manager)."""
        await self._client.request("POST", ["_orgs", org, "leave"])

    async def delete(self, org: str) -> None:
        """Deletes an organization: access ends at once and a job cleans up its documents. Needs ``manage: org``
        and a fresh proof of identity: wrap the call in ``client.auth.with_proof``."""
        await self._client.request("DELETE", ["_orgs", org], reauth=True)

    def members(self, org: str) -> AsyncOrgMembers:
        """The members of an organization: ``list``, ``update`` (direct roles) and ``remove``."""
        return AsyncOrgMembers(self._client, org)

    def templates(self, org: str) -> AsyncOrgTemplates:
        """The organization's own versions of the realm's emails (``email.customizable``): ``list``, ``get``,
        ``put``, ``delete``, ``preview`` and ``test``."""
        return AsyncOrgTemplates(self._client, org)

    def teams(self, org: str) -> AsyncOrgTeams:
        """The teams of an organization: groups of members that carry roles."""
        return AsyncOrgTeams(self._client, org)

    def sso(self, org: str) -> AsyncOrgSso:
        """The organization's own sign-in connections (``organizations.sso``): ``list``, ``get``, ``set`` and
        ``delete``, for holders of ``manage: sso``. People then sign in through them with
        ``client.auth.sign_in_with(provider, org=...)``."""
        return AsyncOrgSso(self._client, org)

    def invitations(self, org: str) -> AsyncOrgInvitations:
        """Invitations of an organization: ``client.orgs.invitations(id).create(...)``. The person who got a link
        previews it with ``client.auth.preview_invitation(token, org=...)`` and takes it by signing in to the
        organization with it."""
        return AsyncOrgInvitations(self._client, org)


class AsyncOrgSso:
    """The sign-in connections of one organization."""

    def __init__(self, client: AsyncClient, org: str) -> None:
        self._client = client
        self._org = org

    async def list(self) -> dict[str, Any]:
        """The connections (``items``), with the ``providers`` and the number of connections the realm allows
        (``max_connections``). A realm with organization sign-in off answers :class:`NotFoundError`."""
        return cast(dict[str, Any], (await self._client.request("GET", ["_orgs", self._org, "sso"])).data)

    async def get(self, provider: Provider) -> OrgConnection:
        """One connection; a :class:`NotFoundError` when the organization has none with that provider."""
        res = await self._client.request("GET", ["_orgs", self._org, "sso", provider])
        return cast(OrgConnection, res.data)

    async def set(
        self,
        provider: Provider,
        *,
        client_id: str,
        client_secret: str | None = None,
        tenant: str | None = None,
        hosted_domain: str | None = None,
        email_domains: Sequence[str] | None = None,
        native_client_ids: Sequence[str] | None = None,
        join_roles: Sequence[str] | None = None,
        enabled: bool | None = None,
    ) -> OrgConnection:
        """Creates the connection or replaces every field of it. The client secret goes in and never comes out
        (required the first time; left out, the stored one is kept). Microsoft needs a ``tenant`` id (never
        ``common``, ``organizations`` or ``consumers``), Google a ``hosted_domain``. ``join_roles`` are given to
        people who join through it, nothing the setting manager could not give. Needs a fresh proof of identity:
        wrap the call in ``client.auth.with_proof``."""
        body = connection_body(
            client_id,
            client_secret=client_secret,
            tenant=tenant,
            hosted_domain=hosted_domain,
            email_domains=email_domains,
            native_client_ids=native_client_ids,
            join_roles=join_roles,
            enabled=enabled,
        )
        res = await self._client.request("PUT", ["_orgs", self._org, "sso", provider], body=body, reauth=True)
        return cast(OrgConnection, res.data)

    async def delete(self, provider: Provider) -> None:
        """Removes the connection and its client secret. Needs a fresh proof of identity."""
        await self._client.request("DELETE", ["_orgs", self._org, "sso", provider], reauth=True)


class AsyncOrgMembers:
    """The members of one organization."""

    def __init__(self, client: AsyncClient, org: str) -> None:
        self._client = client
        self._org = org

    async def list(
        self, *, limit: int | None = None, after: str | None = None, sso: Literal[False] | None = None
    ) -> dict[str, Any]:
        """A page of members sorted by user id: ``items``, ``has_more`` and ``next`` (pass it as ``after``).
        ``sso=False`` lists only the members with no identity on one of the organization's connections."""
        query = {"limit": limit, "after": after, "sso": "false" if sso is False else None}
        res = await self._client.request("GET", ["_orgs", self._org, "members"], query=query)
        return cast(dict[str, Any], res.data)

    async def update(self, user: str, roles: Sequence[str]) -> OrgMember:
        """Replaces a member's direct roles. With ``manage: members`` only roles that open nothing the caller
        lacks; with ``manage: roles`` any."""
        res = await self._client.request("PATCH", ["_orgs", self._org, "members", user], body={"roles": list(roles)})
        return cast(OrgMember, res.data)

    async def remove(self, user: str) -> None:
        """Removes a member; their access ends at once."""
        await self._client.request("DELETE", ["_orgs", self._org, "members", user])


class AsyncOrgTeams:
    """The teams of one organization."""

    def __init__(self, client: AsyncClient, org: str) -> None:
        self._client = client
        self._org = org

    def _path(self, *rest: str) -> list[str]:
        return ["_orgs", self._org, "teams", *rest]

    async def list(self) -> list[OrgTeam]:
        res = await self._client.request("GET", self._path())
        return cast("list[OrgTeam]", cast(dict[str, Any], res.data)["items"])

    async def create(self, name: str, *, roles: Sequence[str] | None = None) -> OrgTeam:
        res = await self._client.request("POST", self._path(), body=_compact(name=name, roles=roles))
        return cast(OrgTeam, res.data)

    async def update(self, team: str, *, name: str | None = None, roles: Sequence[str] | None = None) -> OrgTeam:
        res = await self._client.request("PATCH", self._path(team), body=_compact(name=name, roles=roles))
        return cast(OrgTeam, res.data)

    async def delete(self, team: str) -> None:
        """Deletes a team: it leaves every membership, and its roles go with it."""
        await self._client.request("DELETE", self._path(team))

    async def members(
        self, team: str, *, role: str | None = None, limit: int | None = None, after: str | None = None
    ) -> dict[str, Any]:
        """The members of a team and the holders of its team roles, in force now: ``items``, ``has_more`` and
        ``next``. With ``role``, only the holders of that role (members of the organization who need not belong to
        the team). Emails follow the realm's ``members_visible_to``."""
        query = {"role": role, "limit": limit, "after": after}
        res = await self._client.request("GET", self._path(team, "members"), query=query)
        return cast(dict[str, Any], res.data)

    async def grant_role(
        self, team: str, user: str, role: str, *, from_: str | None = None, until: str | None = None
    ) -> OrgTeamMember:
        """Gives a member a team role in a team: with ``manage: teams``, any; otherwise a holder of a team role of
        that team whose ``grants`` lists it. ``from_`` and ``until`` (RFC 3339) set the window it counts in.
        :class:`OrgRoleRequiresMFAError` when the role that lets the caller does and the session has no second
        factor."""
        body = _compact(user=user, role=role, **{"from": from_}, until=until)
        res = await self._client.request("POST", self._path(team, "roles"), body=body)
        return cast(OrgTeamMember, res.data)

    async def set_role_window(
        self, team: str, user: str, role: str, *, from_: str | None = None, until: str | None = None
    ) -> OrgTeamMember:
        """Replaces the window of a team role the member holds (:class:`NotFoundError` if they don't)."""
        body = _compact(user=user, role=role, **{"from": from_}, until=until)
        res = await self._client.request("PATCH", self._path(team, "roles"), body=body)
        return cast(OrgTeamMember, res.data)

    async def revoke_role(self, team: str, user: str, role: str) -> OrgTeamMember:
        """Takes a team role from a member: access through it ends at their next request."""
        res = await self._client.request("DELETE", self._path(team, "roles"), body={"user": user, "role": role})
        return cast(OrgTeamMember, res.data)

    async def add_member(self, team: str, user: str) -> OrgMember:
        """Puts a member of the organization in a team; they gain its roles."""
        return cast(OrgMember, (await self._client.request("PUT", self._path(team, "members", user))).data)

    async def remove_member(self, team: str, user: str) -> OrgMember:
        """Takes a member out of a team."""
        return cast(OrgMember, (await self._client.request("DELETE", self._path(team, "members", user))).data)


class AsyncOrgTemplates:
    """The email templates of one organization: its own text for the kinds the realm lets companies customize, in the
    logic-less syntax (``{{Link}}``, ``{{App.name}}``, ``{{#App.logo}}…{{/App.logo}}``). Needs the capability
    ``email.overrides.permission`` names (``manage: org`` by default)."""

    def __init__(self, client: AsyncClient, org: str) -> None:
        self._client = client
        self._org = org

    def _path(self, *rest: str) -> list[str]:
        return ["_orgs", self._org, "templates", *rest]

    async def list(self) -> dict[str, Any]:
        """The kinds that can be customized, with the placeholders each must print (``customizable``), and the
        organization's overrides, without their text (``items``)."""
        return cast(dict[str, Any], (await self._client.request("GET", self._path())).data)

    async def get(self, kind: str, locale: str) -> OrgTemplate:
        """One template with its versions; :class:`NotFoundError` if the organization has none."""
        return cast(OrgTemplate, (await self._client.request("GET", self._path(kind, locale))).data)

    async def put(self, kind: str, locale: str, template: OrgTemplateText) -> OrgTemplate:
        """Saves a version. A template with a problem raises a :class:`ValidationError` (``invalid_template``) whose
        ``details`` say where (``text``: ``line 3, column 5: …``)."""
        res = await self._client.request("PUT", self._path(kind, locale), body=template)
        return cast(OrgTemplate, res.data)

    async def delete(self, kind: str, locale: str) -> None:
        """Goes back to the realm's own template of the kind."""
        await self._client.request("DELETE", self._path(kind, locale))

    async def preview(
        self,
        kind: str,
        locale: str,
        *,
        draft: OrgTemplateText | None = None,
        data: Literal["sample", "real"] | None = None,
    ) -> dict[str, Any]:
        """Renders a draft, or the saved version without one, without sending anything: ``subject``, ``text``,
        ``html`` and ``removed``. ``data="real"`` calls the realm's data hook. Show ``html`` in a sandboxed frame."""
        body = {**(draft or {}), **_compact(data=data)}
        res = await self._client.request("POST", self._path(kind, locale, "preview"), body=body)
        return cast(dict[str, Any], res.data)

    async def test(self, kind: str, locale: str, *, draft: OrgTemplateText | None = None) -> None:
        """Sends a draft (or the saved version) to your own address, marked [TEST]. At most 10 per hour for the
        organization (:class:`RetryableError`, 429)."""
        await self._client.request("POST", self._path(kind, locale, "test"), body=dict(draft or {}))


class AsyncOrgInvitations:
    """The invitations of one organization."""

    def __init__(self, client: AsyncClient, org: str) -> None:
        self._client = client
        self._org = org

    def _path(self, *rest: str) -> list[str]:
        return ["_orgs", self._org, "invitations", *rest]

    async def create(
        self,
        binding: Literal["user", "email", "none"],
        *,
        user: str | None = None,
        email: str | None = None,
        roles: Sequence[str] | None = None,
        teams: Sequence[str] | None = None,
        send: bool | None = None,
        expires_in: str | None = None,
        max_uses: int | None = None,
    ) -> OrgInvitation:
        """Invites. ``binding`` says who may accept: one ``user`` (a user id), whoever holds a verified ``email``
        address, or ``none``: anyone signed in with the link (``max_uses``, 1 by default). ``teams`` are team ids;
        ``send=True`` also emails an ``email`` binding; ``expires_in`` is a duration such as ``2d`` (default ``7d``,
        at most ``30d``). The answer holds the ``token`` and the ``link`` (the realm's ``invitation_link``), shown
        only now: show them as a QR code, send them another way, or offer copy and paste."""
        body = _compact(
            binding=binding, user=user, email=email, roles=roles, teams=teams, send=send,
            expires_in=expires_in, max_uses=max_uses,
        )  # fmt: skip
        return cast(OrgInvitation, (await self._client.request("POST", self._path(), body=body)).data)

    async def list(self) -> list[OrgInvitation]:
        """Status, binding, uses and expiry, newest first; never tokens."""
        res = await self._client.request("GET", self._path())
        return cast("list[OrgInvitation]", cast(dict[str, Any], res.data)["items"])

    async def revoke(self, invitation_id: str) -> None:
        await self._client.request("DELETE", self._path(invitation_id))

    async def new_link(self, invitation_id: str) -> OrgInvitation:
        """A new token and link for a pending invitation; the old link stops working."""
        return cast(OrgInvitation, (await self._client.request("POST", self._path(invitation_id, "link"))).data)

    async def send(self, invitation_id: str) -> None:
        """Sends the email of an invitation bound to an address again."""
        await self._client.request("POST", self._path(invitation_id, "send"))
