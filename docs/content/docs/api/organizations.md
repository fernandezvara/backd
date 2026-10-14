---
title: "Organizations"
description: "Create organizations, list their members and change their roles with the organizations API, as a signed-in user."
icon: "domain"
weight: 350
toc: true
---

Organizations group users, such as the people of a customer's company. They are declared in [`realm.yaml`](../../configuration/realm/#organizations): which roles exist and what each may manage. Everything here is under `/v1/{realm}/_orgs`, needs a session (API keys can't use it), and is in the repository's `api/openapi.yaml` under the tag `organizations`.

## Creating and reading

| Request | Who | Answers |
|---|---|---|
| `POST /_orgs` `{"name", "slug"?}` | anyone the realm's `create` rule allows, below `max_created_per_user` | `201` with the organization; the creator holds `creator_roles` |
| `GET /_orgs` | any user | `200` with the caller's organizations, each with their effective roles |
| `GET /_orgs/{org}` | members | `200` with name, slug, status and claims |
| `PATCH /_orgs/{org}` `{"name"?, "slug"?}` | `manage: org` | `200` |

```json
{
  "id": "d3c9ljp8hc2g00b6s1m0",
  "name": "Acme Corp",
  "slug": "acme-corp",
  "status": "active",
  "require_mfa": false,
  "claims": { "plan": "pro" },
  "roles": ["member", "owner"],
  "effective_roles": [
    { "role": "member", "origin": ["default"] },
    { "role": "owner", "origin": ["direct"] }
  ],
  "teams": []
}
```

- **Slugs** are unique among organizations that aren't deleted (`409 slug_taken`): lowercase letters and digits separated by single `-`, at most 63 characters.
- **Claims** are facts about the organization that only realm administrators and functions write (`plan`, `seats`…). Members read them.
- **A non-member and an unknown organization answer the same** `403 not_org_member`, so the answer never says whether an organization exists.
- **`409 org_limit_reached`** means the caller created the most organizations they may.

## Members

| Request | Who | Answers |
|---|---|---|
| `GET /_orgs/{org}/members?limit=&after=` | members | `200` with a page sorted by user id |
| `PATCH /_orgs/{org}/members/{user}` `{"roles": [...]}` | `manage: members` or `manage: roles` | `200` with the member; replaces their direct roles |
| `DELETE /_orgs/{org}/members/{user}` | `manage: members` | `204` |
| `POST /_orgs/{org}/leave` | members | `204` |

A member lists their direct `roles`, their `effective_roles` with where each came from (`direct`, `default` or a team) and `overlap`, the roles held more than one way. Emails appear according to [`members_visible_to`](../../configuration/realm/#organizations).

{{< hint style="note" >}}
What each role may do follows two rules. With `manage: roles` any role can be given or taken away. With `manage: members` only roles that open no capability the caller lacks, and not for a member who holds capabilities the caller lacks: an administrator can't remove an owner. `403 forbidden` says the caller's roles don't allow it.
{{< /hint >}}

The last member who can manage the organization (`manage: org`) can't leave, be removed or lose that role: `409 last_org_manager`. Access ends at once when someone leaves or is removed; documents they created stay.

## Requiring a second factor

An organization can require its members to use [multi-factor authentication](../../auth/mfa/): `PATCH /_orgs/{org}` with `{"require_mfa": true}`, by `manage: org`, with a fresh proof of identity, when the realm's [`allow_require_mfa`](../../configuration/realm/#organizations) is true. It is audited as `org.require_mfa`. The caller's own session needs a second factor too, or their access would end at once.

- **A session without a second factor doesn't count** as a member there. Rule helpers (`inOrg`, `hasOrgRole`, `orgClaims`) are false for it and leave the organization out of lists; `?org=` on a create answers `403 not_org_member`; and the organization's own routes answer `403 org_requires_mfa`, so the app can start the enrollment.
- **Leaving stays possible** without one.
- **`GET /_orgs` marks it:** each organization has `require_mfa` and `mfa_met` (false when this session used no second factor).
- **Other organizations are unaffected:** the requirement is enforced where it matters, not at sign-in, so one strict organization doesn't force a second factor everywhere.
- **Managers see who is missing one:** the members list carries `mfa` for them.
- **API keys** acting on behalf of a user have no second factor, so they can't reach these organizations.

## Teams

A team is a group of members that **carries organization roles**: everyone in it holds those roles in addition to their own. Teams don't own documents, and access rules see roles, never team names.

| Request | Who | Answers |
|---|---|---|
| `GET /_orgs/{org}/teams` | members | `200` with the teams |
| `POST /_orgs/{org}/teams` `{"name", "roles"?}` | `manage: teams` | `201` |
| `PATCH /_orgs/{org}/teams/{team}` `{"name"?, "roles"?}` | `manage: teams` | `200` |
| `DELETE /_orgs/{org}/teams/{team}` | `manage: teams` | `204` |
| `PUT` and `DELETE /_orgs/{org}/teams/{team}/members/{user}` | `manage: teams` | `200` with the member |

- **Effective roles** are the direct roles plus the roles of the member's teams, read at each request, so a change applies at the next one. The members list shows where each role comes from (`direct`, `default` or the team's name) and marks, in `overlap`, a role held both directly and through a team.
- **Granting rules apply to teams too:** a team carries only roles the caller could give, and putting someone in a team gives them its roles, so the caller must be able to give those. An administrator can't put themselves in the owners' team.
- **The organization keeps a manager:** changing a team's roles, deleting it or taking a member out of it can't leave the organization without anyone who can manage it (`409 last_org_manager`).
- **Leaving** the organization removes the member from its teams; **deleting a team** removes it from every membership.
- **Caps:** `max_teams_per_org` and `max_teams_per_member` (`409 team_limit_reached`); names are unique in an organization (`409 team_name_taken`).

A team can also hold **team roles**, which are separate from the roles it carries: see [team roles](../../organizations/team-roles/). `GET /_orgs/{org}/teams/{team}/members?role=` lists a team's members and role holders; `POST`, `PATCH` and `DELETE /_orgs/{org}/teams/{team}/roles` with `{"user", "role"}` (and `from` and `until` for a window) give, re-window and take a role: `manage: teams`, or a holder of a team role whose `grants` lists it, in their own team.

{{< hint style="tip" title="Best practice" >}}
Give **management** roles (`owner`, `admin`) directly and **access** roles (`editor`, `viewer`) through teams. A member who holds `editor` both ways keeps it after being taken out of the team, so removal looks done and isn't; the members list marks those roles in `overlap`.
{{< /hint >}}

## Invitations

People join an organization by accepting an invitation. A manager (`manage: members`) creates one:

```json
POST /v1/acme/_orgs/{org}/invitations
{ "binding": "email", "email": "ana@example.com", "roles": ["editor"], "send": true, "expires_in": "7d" }
```

| `binding` | Fields | Who can accept |
|---|---|---|
| `user` | `user`: a user id | only that user |
| `email` | `email` | the signed-in user whose **verified** address matches |
| `none` | `max_uses` (default 1), `expires_in` (at most 30 days) | anyone signed in who holds the link |

The answer always has `id`, `token` and `link` (the realm's [`invitation_link`](../../configuration/realm/#organizations) with the token, or `null` when it sets none), and they are shown only now: a lost link can't be shown again, issue a new one instead. The app can show the link as a QR code, send it some other way or offer copy and paste. `send: true` also emails it to an `email` binding (the `org-invitation` [email](../../functions/email/)), within the realm's email limits; backd keeps that token sealed with the instance's secrets key (`BACKD_SECRETS_KEY`) until the invitation ends.

- **What can be given:** `roles` and `teams` follow the granting rules above, and a team counts as the roles it carries. An open link (`none`) can't give a role that manages the organization, directly or through a team.
- **Accepting:** with a session, `POST /_orgs/invitations/preview` says what a token offers (`{org, roles, teams}`, so an app can ask "Join Acme as editor?") and `POST /_orgs/invitations/accept` takes it, both with `{"token"}`. An existing member's roles and teams are added to. There is no hosted page: accepting needs a session.
- **Errors:** an unknown, expired, revoked or used-up token answers the same `400 invalid_invitation`; a binding that doesn't fit answers `403 invitation_not_for_you`, only after the token has proved valid.
- **Managing:** `GET /_orgs/{org}/invitations` (status, binding, uses and expiry, never tokens), `DELETE .../{id}` revokes, `POST .../{id}/link` issues a new token and invalidates the old one, `POST .../{id}/send` sends the email again. At most `max_pending_invitations` (500) pending ones per organization (`409 invitations_limit`).

## Deleting an organization

`DELETE /_orgs/{org}` needs `manage: org` and a fresh proof of identity ([re-authentication](../../auth/reauth/): `401 reauth_required` until the request carries `X-Backd-Reauth`). A realm administrator uses `DELETE /_admin/orgs/{org}` or `backd orgs delete`, after looking at what it will do with `GET /_admin/orgs/{org}/owned` (`backd orgs owned`: counts per database and collection, and each collection's policy).

- **Access ends at once.** The status becomes `deleted`, and memberships, teams and invitations are removed: nobody belongs to it any more, and its slug is free again.
- **Then a job cleans up** (`backd:org.delete`, resumable) by each collection's [`on_org_delete`](../../configuration/config-dir/#organizations): `delete` removes the organization's documents, soft-deleted ones too, and queues their files for deletion; `keep` leaves them, readable only by realm administrators.
- **The record stays** as `deleted`, with its name and dates, so `keep` documents still point at something readable. Audited as `org.delete` and, when the cleanup ends, `org.deleted` with counts and never content.
- **Bringing it back:** `POST /_admin/orgs/{org}/reactivate` (`backd orgs reactivate`) makes it `active` **without members**, under its former slug unless that is in use (give another in `slug`); assign an owner next. Documents kept under `keep` are visible to members again; those under `delete` are gone.

{{< hint style="warning" >}}
Deleting is not undoable for `delete` collections: reactivating an organization doesn't bring their documents back. Look at the report first.
{{< /hint >}}

## JavaScript client

`backd.orgs` and `collection.inOrg(id)` wrap all of this: see [the client](../../clients/js/#organizations).

## Functions

Functions see the caller's memberships in `ctx.user.orgs`, create documents in an organization with `ctx.db`, and, with `admin: true`, manage organizations with `ctx.admin.orgs`: see [Organizations in functions](../../functions/organizations/).

## Administrators

Realm administrators with the `users` area use `/_admin/orgs`, described with the [admin API](../../auth/admin/), and [`backd orgs`](../../auth/cli/): list organizations (`?status=active|orphaned|deleted` and `?q=` for part of a name or slug; each with its number of `members`) and show them, create one for a user, invite (with any role), add members without an invitation, replace claims, delete and reactivate organizations and assign a manager to an organization left without one.
