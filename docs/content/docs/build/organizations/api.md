---
title: "Organizations API"
description: "Create organizations, list their members and change their roles with the organizations API, as a signed-in user."
icon: "buildings"
weight: 350
toc: true
---

Organizations group users, such as the people of a customer's company. They are declared in [`realm.yaml`](../../configuration/realm/#organizations): which roles exist and what each may manage. Everything here is under `/v1/{realm}/_orgs`, needs a session (API keys can't use it), and is in the repository's `api/openapi.yaml` under the tag `organizations`.

## Creating and reading

| Request | Who | Answers |
|---|---|---|
| `POST /_orgs` `{"name", "slug"}` | anyone the realm's `create` rule allows, below `max_created_per_user` | `201` with the organization; the creator holds `creator_roles`. The answer also holds `session`, a new session scoped to it ([below](#signing-in-to-an-organization)) |
| `GET /_orgs/{org}` | members | `200` with name, slug, status and claims |
| `PATCH /_orgs/{org}` `{"name"?, "slug"?, "require_mfa"?, "sso_required"?, "session_max_age"?}` | `manage: org` | `200` |

There is no list of the caller's organizations: **a session belongs to one organization**, the one it was opened for, and `GET /_auth/me` names it. The app keeps one session per organization the person signs in to (the [clients](../../clients/js/#working-in-an-organization) do), and the [sign-in page](../sign-in-experience/) is where a person chooses.

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

- **Slugs** are required, and unique among organizations that aren't deleted (`409 slug_taken`): lowercase letters and digits separated by single `-`, at most 63 characters, and never something that looks like an id. People name the organization by its slug when they sign in. Organizations made before slugs were required get theirs with `backd orgs set-slug` (`PUT /_admin/orgs/{org}/slug`); startup warns about the ones without.
- **Claims** are facts about the organization that only realm administrators and functions write (`plan`, `seats`…). Members read them.
- **A non-member and an unknown organization answer the same** `403 not_org_member`, so the answer never says whether an organization exists.
- **`409 org_limit_reached`** means the caller created the most organizations they may.

## Signing in to an organization

In a realm with organizations **every session is scoped to one organization**: the token reaches that organization and no other, and documents, rules and `ctx.user.orgs` see only it. Sessions are scoped by signing in *to* an organization, at `/v1/{realm}/_auth/orgs/{org}/…`. These routes need no session and take the organization's **id or its slug** (case and surrounding spaces do not matter; a slug can never look like an id). Everything after signing in uses the id, which the answer returns as `org`.

| Request | Does |
|---|---|
| `POST /_auth/orgs/{org}/login` `{"email", "password", "invitation"?}` | the first factor, as `POST /_auth/login`, for a member of the organization; the session, or the second step, is scoped to it |
| `POST /_auth/orgs/{org}/signup` `{"email", "password", "invitation"}` | an account for someone the organization invited; the invitation is required, whatever the realm's `signup` says |
| `POST /_auth/orgs/{org}/magic-link` | a link and a code, [as for the realm](../../auth/magic-links/), sent only to members |
| `POST /_auth/orgs/{org}/reset-password/request` | a reset email from the organization's page; members get the organization's [context](../organization-context/) |
| `POST /_auth/orgs/{org}/invitations/preview` `{"token"}` | what an invitation offers, before signing in |
| `GET\|POST /_auth/orgs/{org}/oauth/{provider}/start` | a provider, [the organization's own connection](#sign-in-connections) or the realm's, signing in to this organization |
| `GET /_auth/orgs/{org}/providers` | the providers to offer, and `methods`: `password` and, with magic links, `magic_link`; `sso_required` says the organization [requires its connection](#requiring-the-connection) (then only its providers, no `methods`) |
| `POST /_auth/orgs` `{"email", "password", "name", "slug", "invitation"?}` | a **new person** starts a company: the account, the organization (the person is its creator) and a session scoped to it, in one call. The realm's `signup` decides who may: anyone when `open`, whoever holds an `invitation` of the realm (`bdi_…`) when `invite`, nobody when `closed`. `organizations.create` is not asked for this first organization, `max_created_per_user` is. `409 slug_taken` makes nothing |
| `POST /_auth/orgs/find` `{"email"}` | "which companies am I in?" (below) |

- **The person must belong to the organization, or hold its invitation.** A wrong password, an address with no account, a stranger, an organization that does not exist or is deleted, and an invitation that is not theirs all answer the same `401 invalid_credentials`, after the same work, and count the same toward the [sign-in limits](../../auth/sessions/). Nothing in the answer says who belongs where. For the same reason there is no endpoint that tells whether a slug exists.
- **Invitations are accepted as part of the sign-in.** `invitation` in `login` (an `email` or `user` binding must fit the person; an open link fits anyone), in `signup`, or in `oauth/start` (`?invitation=bdo_…`) uses it and makes the person a member before the session is opened. An invitation sent to the address of a new account makes it verified; an open link does not.
- **A provider of the realm signs in to an organization too:** the person must be a member (or hold the invitation) when they come back from the provider, otherwise the flow ends with `backd_error=not_allowed`, the same for an organization that is not there. A connection of the organization takes the place of the realm's provider of the same name.
- **A second session:** `POST /_orgs` leaves the caller's session as it is and answers a new one for the organization it made, `session: {token, expires_at, org}`, for the app to keep beside the other.
- **Operators sign in to the realm.** A realm administrator (a user with an admin role) signs in at `POST /_auth/login`, which answers `401 invalid_credentials` to everybody else; their session belongs to no organization and reaches the admin API alone. Everything else in the realm, including `/_auth/signup`, the unscoped magic link and the realm's own `oauth/{provider}/start`, answers `404`: a person always signs in to an organization.
- **Sessions from before** the realm had organizations, or from before they were scoped, answer `401 session_unscoped`: the person signs in again (the clients treat it as an expired session). See [Upgrading](../../../operate/#upgrading-from-v092-to-the-next-release).

### Limits

Starting a company with `POST /_auth/orgs` is limited to 20 an hour per address, because a slug that is taken is the one place a slug shows that it exists. The password, magic link, reset and sign-up calls to organizations share a limit of 120 a minute per address, the same for every organization name, so a script cannot walk names cheaply. Over either: `429`.

### Which companies am I in?

For someone on a new device who does not remember the slug. `POST /_auth/orgs/find` always answers `202`, whatever the address, so it says nothing to whoever asked; an address with memberships gets the `organizations-found` [email](../../functions/email/) listing its organizations by name, each with the link to its sign-in page. It needs `email:` and `organizations.signin_link` in [`realm.yaml`](../../configuration/realm/#organizations) (`https://app.acme.example/{slug}/signin`; `{slug}` is the organization's id when it has no slug); without it the route answers `404`. At most 3 requests an hour per address and 30 per client address (`429`).

## Members

| Request | Who | Answers |
|---|---|---|
| `GET /_orgs/{org}/members?limit=&after=` | members | `200` with a page sorted by user id; `sso=false` (`manage: org`) lists only those with no identity on a connection (see [requiring the connection](#requiring-the-connection)) |
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

- **A session without a second factor doesn't count** as a member there. Rule helpers (`inOrg`, `hasOrgRole`, `orgClaims`) are false for it, so its documents are empty and creating answers `403`; and the organization's own routes answer `403 org_requires_mfa`, so the app can start the enrollment.
- **Leaving stays possible** without one.
- **`GET /_auth/me` marks it:** its `org` has `require_mfa` and `mfa_met` (false when this session used no second factor).
- **Other organizations are unaffected:** the requirement is enforced where it matters, not at sign-in, so one strict organization doesn't force a second factor everywhere.
- **Managers see who is missing one:** the members list carries `mfa` for them.
- **API keys** acting on behalf of a user have no second factor, so they can't reach these organizations.

## How long sessions last

An organization can shorten the lifetime of the sessions opened in it: `PATCH /_orgs/{org}` with `{"session_max_age": 28800}` (seconds, at least 3600, never more than the realm's [`sessions.max_lifetime`](../../configuration/realm/#keys); `0` goes back to the realm's), by `manage: org`, with a fresh proof of identity. The expiry of a session is fixed when it is opened, from the organization's value, and never extended, so the change applies to the sessions opened from then on. It is also how a company limits how long someone removed at their identity provider keeps access until the provider can tell backd directly. Audited as `org.update`.

## API keys on behalf of a user

A key that acts for a user (`X-Backd-On-Behalf-Of`) is **bound to one organization when it is made**: `backd apikey create --org acme-corp` (`org` in `POST /_admin/apikeys`, by id or slug). It counts only that organization, as a session scoped to it would. A key without one counts none, so it can't reach documents of collections that belong to organizations. Keys that act on their own still bypass the rules, as before.

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
- **Accepting:** `POST /_auth/orgs/{org}/invitations/preview` `{"token"}` says what a token offers (`{org, roles, teams}`, so an app can ask "Join Acme as editor?") and the token is taken by [signing in to the organization with it](#signing-in-to-an-organization) (`invitation` in `login`, `signup` or `oauth/start`). An existing member's roles and teams are added to. There is no hosted page.
- **Errors:** the preview answers an unknown, expired, revoked or used-up token the same `400 invalid_invitation`. Signing in with one that doesn't work, or that is not the person's, is the uniform `401 invalid_credentials`.
- **Managing:** `GET /_orgs/{org}/invitations` (status, binding, uses and expiry, never tokens), `DELETE .../{id}` revokes, `POST .../{id}/link` issues a new token and invalidates the old one, `POST .../{id}/send` sends the email again. At most `max_pending_invitations` (500) pending ones per organization (`409 invitations_limit`).

## Deleting an organization

`DELETE /_orgs/{org}` needs `manage: org` and a fresh proof of identity ([re-authentication](../../auth/reauth/): `401 reauth_required` until the request carries `X-Backd-Reauth`). A realm administrator uses `DELETE /_admin/orgs/{org}` or `backd orgs delete`, after looking at what it will do with `GET /_admin/orgs/{org}/owned` (`backd orgs owned`: counts per database and collection, and each collection's policy).

- **Access ends at once.** The status becomes `deleted`, and memberships, teams and invitations are removed: nobody belongs to it any more, and its slug is free again.
- **Then a job cleans up** (`backd:org.delete`, resumable) by each collection's [`on_org_delete`](../../configuration/config-dir/#organizations): `delete` removes the organization's documents, soft-deleted ones too, and queues their files for deletion; `keep` leaves them, readable only by realm administrators.
- **Its sign-in connections and their client secrets are removed** by the same job (the connections were unavailable from the moment the organization was deleted, and so are those of an `orphaned` one). The audit record `org.deleted` counts them as `connections`. A reactivated organization starts without connections.
- **The record stays** as `deleted`, with its name and dates, so `keep` documents still point at something readable. Audited as `org.delete` and, when the cleanup ends, `org.deleted` with counts and never content.
- **Bringing it back:** `POST /_admin/orgs/{org}/reactivate` (`backd orgs reactivate`) makes it `active` **without members**, under its former slug unless that is in use (give another in `slug`); assign an owner next. Documents kept under `keep` are visible to members again; those under `delete` are gone.

{{< hint style="warning" >}}
Deleting is not undoable for `delete` collections: reactivating an organization doesn't bring their documents back. Look at the report first.
{{< /hint >}}

## Sign-in connections

A company can sign its people in with **its own** Google Workspace or Microsoft Entra tenant instead of the providers the realm configures (the step-by-step version is [the sign-in guide](../sign-in/); the operator's side, [Organization sign-in for operators](../../../operate/organization-sign-in/)). The realm switches the feature on with [`organizations.sso`](../../configuration/realm/#organizations); the managers of an organization (a role with `manage: sso`) then keep its **connections**: an application the company registered with the provider, a client secret, and the restriction that says who may come in. Everything answers `404` while the realm has it off.

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
- **`join_roles`** are the organization roles a person receives on joining through the connection. They follow the same rule as [granting roles](#members): a manager can't give what they don't hold. With `manage: roles` any role can be named; with `manage: members` (and `manage: sso`) only roles that open no capability the manager lacks; with only `manage: sso`, none (`403 forbidden`), because the roles go to every person of the company. Leave `join_roles` empty and the default roles are all they get.
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
- **Erasing a user removes their identities through organizations too**, with every other sign-in method; nothing of theirs can sign in afterwards.
- **Identities are keyed by organization.** The person's identity is `(provider, organization, subject)`: the same Google account at two organizations, or at an organization and at the realm, is separate identities, and a user holds one per provider per organization. `GET /_auth/me` lists them with the organization's id.
- **It never links by address.** If an account already has the person's address, the flow ends at `backd_error=account_exists` even when Google says it is verified: an organization's managers control the connection, so their provider's word is not proof that an address is theirs. The person signs in the way they already do and links the connection with `intent: link`. Addresses from a connection are stored unverified.

### Joining

| The person | What happens |
|---|---|
| Has no account, and the connection admits them | An account is made, **whatever the realm's `signup` mode** (`closed` and `invite` included: the connection's restriction is the gate and the realm switched the feature on), and they join the organization with the connection's `join_roles` |
| Has an account with the same address | `account_exists`: they sign in the way they already do and link the connection with `intent: link`, which joins them |
| Signs in again | Nothing changes: they are not made a member again, and keep the roles they have |
| Was removed by a manager | Signed in, **not** joined, until an invitation to the organization is accepted. Leaving by oneself does not block |

- Joining happens only when the sign-in makes the identity (a new person, or the explicit link). Someone who is already a member keeps their roles, and a blocked person is never rejoined by signing in or by linking.
- **Removal blocks.** While the organization has a connection, a member a manager (or an administrator) removes is kept in the connection's blocked list (the newest 1000), and accepting an invitation to the organization lifts it.
- **The roles are checked again at every join.** If the manager who last set the connection no longer holds the capabilities of its `join_roles`, backd disables the connection (`org.sso.disabled`) and the sign-in ends with `provider_error`, so a manager who lost a role doesn't leave a powerful default behind. A realm administrator's connection is not affected, and neither is one whose roles open nothing.
- **Sessions remember how they were opened.** A session opened through a connection records it as its first method, `sso:{org}:{provider}` (also across a second step or a consent), not the provider's name alone, which the realm's own providers use too. That is how [requiring the connection](#requiring-the-connection) and the limits on [account-level actions](#what-a-connection-session-cannot-do) tell it apart.
- Audit: every sign-in is `org.sso.login` with `joined`, and a join is `org.member_add` with `by: sso`.

### Requiring the connection

An organization can make its connection the only way in: `PATCH /_orgs/{org}` with `{"sso_required": true}`, by `manage: org`, with a fresh proof of identity.

- **It can't lock its manager out.** It needs a working connection (enabled, with its secret set) and that the session of the caller was opened through one: otherwise `409 sso_not_ready`.
- **What it does.** `login`, `signup`, `magic-link`, `reset-password/request` and the realm's own providers, under `/_auth/orgs/{org}/…`, answer `400 sso_required`; the connection's own routes keep working, and so does leaving. `GET /_auth/orgs/{org}/providers` says `sso_required: true`, lists only the connection's providers and no `methods`, so the app shows just those buttons. Codes already issued by a link or an earlier sign-in still work until they expire (minutes).
- **Who is left outside.** Members who joined another way (an invited contractor, say) can't come in until they sign in through the connection. `GET /_orgs/{org}/members?sso=false` lists the members with no identity on the connection before you turn it on, and the answer of the `PATCH` counts them (`members_without_sso`). The sessions of the organization that were not opened through a connection end when it is turned on (`sessions_revoked`).
- **Turning it off** (`false`) needs no connection. Removing or disabling the last working connection turns it off by itself, so the people are not left with no way in.
- **Break glass.** A realm administrator clears it with `PATCH /_admin/orgs/{org}` `{"sso_required": false}` or `backd orgs sso-required-off`, for an organization that can't reach its provider. Administrators can't turn it on. All of it is audited as `org.sso_required`.
- **`require_mfa` still applies.** The provider's own second factor is not trusted for it.

### What a connection session cannot do

A session opened through a connection proves "the provider of this organization says this is the person", not that the account is theirs: the provider's administrator controls it. So that session cannot change what protects the account, which would reach the person's other organizations too. Changing or removing the password, the email address, the second factor and its recovery codes, unlinking a provider, linking one, deleting the account and requesting the personal data export answer `403 sso_session`, before any proof is asked for. Organization management works, with a re-authentication by email code for someone who exists only through the connection. A person who wants credentials of their own signs in with a password or a magic link from an organization that allows them. Administrators export and erase accounts for people who exist only through a connection.

Connections move with the realm: `--include auth` exports and imports them as `org_connections`, without their client secrets (see [Moving users](../../../operate/administration/export-import/moving-an-app/#moving-users)), so an imported connection stays unavailable until its secret is set again.

Realm administrators use the same under `/_admin/orgs/{org}/sso[/{provider}]` (the `users` area, no proof needed beyond their own session or key) and `backd orgs sso`, `sso-set` and `sso-delete`.

## JavaScript client

`backd.orgs`, `createClient({ org })` and `client.org(ref)` wrap all of this: see [the client](../../clients/js/#organizations).

## Functions

Functions see the caller's memberships in `ctx.user.orgs`, create documents in an organization with `ctx.db`, and, with `admin: true`, manage organizations with `ctx.admin.orgs`: see [Organizations in functions](../functions/).

## Administrators

Realm administrators with the `users` area use `/_admin/orgs`, described with the [admin API](../../../operate/administration/admin/), and [`backd orgs`](../../../operate/administration/cli/): list organizations (`?status=active|orphaned|deleted` and `?q=` for part of a name or slug; each with its number of `members`) and show them, create one for a user, invite (with any role), add members without an invitation, replace claims, delete and reactivate organizations and assign a manager to an organization left without one.
