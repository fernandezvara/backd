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

## Administrators

Realm administrators with the `users` area use `/_admin/orgs`, described with the [admin API](../../auth/admin/), and [`backd orgs`](../../auth/cli/): list and show organizations, create one for a user, invite (with any role), add members without an invitation, replace claims and assign a manager to an organization left without one.
