---
title: "Organizations"
description: "Create organizations, list their members and change their roles with the organizations API, as a signed-in user."
icon: "buildings"
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

An organization can require its members to use [multi-factor authentication](../../auth/mfa/): `PATCH /_orgs/{org}` with `{"require_mfa": true}`, by `manage: org`, with a fresh proof of identity, when the realm's [`allow_require_mfa`](../../configuration/realm/#organizations) is true (otherwise `409 require_mfa_not_allowed`). It is audited as `org.require_mfa`. The caller's own session needs a second factor too, or their access would end at once.

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

A team can also hold **team roles**, which are separate from the roles it carries: see [team roles](../team-roles/). `GET /_orgs/{org}/teams/{team}/members?role=` lists a team's members and role holders; `POST`, `PATCH` and `DELETE /_orgs/{org}/teams/{team}/roles` with `{"user", "role"}` (and `from` and `until` for a window) give, re-window and take a role: `manage: teams`, or a holder of a team role whose `grants` lists it, in their own team.

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

## Sign-in connections

A company can sign its people in with **its own** Google Workspace or Microsoft Entra tenant instead of the providers the realm configures. The realm switches the feature on with [`organizations.sso`](../../configuration/realm/#organizations); the managers of an organization (a role with `manage: sso`) then keep its **connections**: an application the company registered with the provider, a client secret, and the restriction that says who may come in. Everything answers `404` while the realm has it off.

| Request | Needs | Answer |
|---|---|---|
| `GET /_orgs/{org}/sso` | `manage: sso` | `200` with `items` (the connections), `providers` and `max_connections` |
| `GET /_orgs/{org}/sso/{provider}` | `manage: sso` | `200`, or `404` when there is none |
| `PUT /_orgs/{org}/sso/{provider}` | `manage: sso`, fresh proof | `200` with the connection; creates it or replaces every field |
| `DELETE /_orgs/{org}/sso/{provider}` | `manage: sso`, fresh proof | `204`; the client secret goes with it |

`{provider}` is `google` or `microsoft`. A `PUT` takes:

```json
{
  "client_id": "123-abc.apps.googleusercontent.com",
  "client_secret": "…",
  "hosted_domain": "acme.example",
  "email_domains": ["acme.example"],
  "join_roles": ["member"],
  "enabled": true
}
```

- **Who may come in** is cryptographic, not a claim: a Google connection names the Workspace `hosted_domain` (a personal Google account has none and is refused), a Microsoft one the company's `tenant` id (a GUID; `common`, `organizations` and `consumers` admit anyone and are refused). `email_domains` narrows it further and `native_client_ids` lists the extra client ids a mobile app uses.
- **The client secret is write-only.** Send it when you create the connection (it is required then) or change it; leave it out to keep the stored one. It is kept as an [organization secret](../../functions/secrets/): sealed like every secret, readable by nobody, not by the managers and not by functions. The answers say only `client_secret_set`. A connection without its secret is unavailable.
- **`join_roles`** are the organization roles a person receives on joining through the connection. They follow the same rule as [granting roles](#members): a manager can't give what they don't hold. With `manage: roles` any role can be named; with only `manage: sso`, only roles that open no capability.
- **The redirect address** to register with the provider is in the answer (`redirect_uri`, built from `email.public_url` or `BACKD_URL`; `redirect_path` when neither is set): `/v1/{realm}/_auth/orgs/{org id}/oauth/{provider}/callback`. It always names the organization by its id.
- At most `max_per_org` connections, one per provider (`409 sso_limit`); `503 unavailable` when the instance has no `BACKD_SECRETS_KEY` to keep the secret with; `409 org_not_active` for a deleted organization.
- Every change is audited as `org.sso.set` or `org.sso.deleted` with the provider, never the secret.

### Signing in through a connection

The people of the company sign in at the organization's own address, which is the realm's [provider flow](../../auth/providers/) with the connection's application, secret and restriction:

| Request | What it does |
|---|---|
| `GET /_auth/orgs/{org}/providers` | `{"items": ["google"]}`: the connections an app may offer for this organization now. An unknown organization, one without connections and one with the feature off all answer `{"items": []}` |
| `GET\|POST /_auth/orgs/{org}/oauth/{provider}/start` | Begins the redirect flow, as the realm's `start` does; `POST` with `"intent": "link"` links the connection to the signed-in user |
| `GET\|POST /_auth/orgs/{org}/oauth/{provider}/callback` | The address registered with the provider; `{org}` is always the organization's id here |
| `POST /_auth/orgs/{org}/oauth/{provider}/id-token` | Native apps: the token's audience is the connection's `client_id` or one of its `native_client_ids` |

`{org}` is the id or the slug (except in the callback). Nothing needs a session, and the rate limits are the sign-in ones.

- **Who comes in** is checked on the verified ID token, after its signature, issuer, audience and nonce: a Google token's `hd` must be the connection's Workspace domain and its address verified, a Microsoft token's `tid` must be the connection's tenant, and when the connection lists `email_domains` the address must be in one of them. Otherwise the flow ends at `redirect_to?backd_error=not_allowed` (`403 not_allowed` for a native token) and **nothing is created**.
- **An unavailable connection answers like one that isn't there.** An unknown organization, a provider it never connected, a disabled connection, one without its secret and an organization that is not active (`orphaned` or deleted) all answer the same `404`, so the answer doesn't say which organizations exist.
- **A callback finishes only what it started.** The attempt remembers the organization, so a sign-in begun for one organization cannot be finished at another's callback or at the realm's, and the realm's cannot be finished at an organization's: those end at the error page.
- **Identities are keyed by organization.** The person's identity is `(provider, organization, subject)`: the same Google account at two organizations, or at an organization and at the realm, is separate identities, and a user holds one per provider per organization. `GET /_auth/me` lists them with the organization's id.
- **It never links by address.** If an account already has the person's address, the flow ends at `backd_error=account_exists` even when Google says it is verified: an organization's managers control the connection, so their provider's word is not proof that an address is theirs. The person signs in the way they already do and links the connection with `intent: link`. Addresses from a connection are stored unverified.

{{< hint style="note" >}}
Joining the organization when a person signs in, the roles given on joining and the members who were removed come with the next step of the same release; until then signing in through a connection creates or finds the user without making them a member.
{{< /hint >}}

Realm administrators use the same under `/_admin/orgs/{org}/sso[/{provider}]` (the `users` area, no proof needed beyond their own session or key) and `backd orgs sso`, `sso-set` and `sso-delete`.

## JavaScript client

`backd.orgs` and `collection.inOrg(id)` wrap all of this: see [the client](../../clients/js/#organizations).

## Functions

Functions see the caller's memberships in `ctx.user.orgs`, create documents in an organization with `ctx.db`, and, with `admin: true`, manage organizations with `ctx.admin.orgs`: see [Organizations in functions](../functions/).

## Administrators

Realm administrators with the `users` area use `/_admin/orgs`, described with the [admin API](../../../operate/administration/admin/), and [`backd orgs`](../../../operate/administration/cli/): list organizations (`?status=active|orphaned|deleted` and `?q=` for part of a name or slug; each with its number of `members`) and show them, create one for a user, invite (with any role), add members without an invitation, replace claims, delete and reactivate organizations and assign a manager to an organization left without one.
