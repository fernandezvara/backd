# Design: scoped sessions

- **Issues:** milestone `34-scoped-sessions` (to be created, see §12); replaces #600 of `31-org-sso-extensions`
- **Status:** proposed (for review)
- **Builds on:** [org-sso.md](./org-sso.md) (connections, org-bound OAuth routes, `sessions.org`, `identities.org`; milestone `30-org-sso`)
- **Changes:** [organizations.md](./organizations.md) (`?org=`, listing and creating organizations, invitations), [org-sso.md](./org-sso.md) (slugs in addresses, the unenforced `sessions.org`), [mfa-passwordless.md](./mfa-passwordless.md) (what a session remembers), [account-lifecycle.md](./account-lifecycle.md) (sign-up), [team-access.md](./team-access.md) §10 (company context), [compliance.md](./compliance.md) (export)
- **Related designs:** [identity-providers.md](./identity-providers.md), [email-delivery.md](./email-delivery.md), [internal-functions.md](./internal-functions.md)

## 1. The idea

A session belongs to a user, and a user belongs to any number of organizations. One token reaches all of them, and the caller picks the organization on every request with `?org=`. [org-sso.md](./org-sso.md) lets an organization bring its own Google Workspace or Microsoft tenant, and records on the session which organization's connection opened it ("nothing enforces it yet"). Without enforcement the token that Acme's tenant vouched for still reaches Beta's data, which Acme's tenant has nothing to do with.

The fix is a different shape of session, not a check bolted onto access:

- **In a realm that has organizations, every session is scoped to exactly one of them.** It is created by signing in *to* that organization and everything the token reaches is inside it.
- **A person has one session per organization,** each created through that organization's own sign-in.
- **An organization can require its connection:** its members can then only enter through it.

There is no unscoped mode and no `?org=` to pick an organization, so nothing has to be kept in sync between two ways of working.

Principles carried over:
- **One user per email in the realm** (the unique index on `users.email`). A person has memberships, with different roles in each organization; organizations never get their own copies of a person.
- **Server-owned state:** the scope is in the session on the server. A token carries nothing a client can edit.
- **Immediate revocation,** no caches of who belongs where ([organizations.md §6.3](./organizations.md#63-loading-memberships)).
- **Uniform failures:** signing in to an organization you do not belong to looks exactly like a wrong password.

**What this is not:** SCIM, SAML or group-to-role mapping (§11), or a user table per organization.

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Scope | a realm with `organizations.enabled` has **only scoped sessions**: `sessions.org` is always set. Realms without organizations are unchanged |
| Signing in | only through `/v1/{realm}/_auth/orgs/{org}/…`; the unscoped `login`, `signup`, `magic-link` and `oauth` routes no longer exist in such realms |
| The organization in a path | always the **id**; no route accepts a slug (this removes "the id or the slug" from [org-sso.md §2](./org-sso.md#2-decisions-at-a-glance)) |
| How people find their organization | the id is for programs. People know the **slug** (`acme`): one public call, `GET /_auth/orgs/lookup?slug=acme`, answers `{ id, name }` once, and the app or the client keeps the id. The clients do it for `createClient({ org: { slug } })` (§5.3) |
| Choosing an organization | `?org=` and `inOrg()` are **removed**; the session decides |
| Access | one choke point, `Org.Counts(mfa, scope)`, used by rules, `ctx.user`, realtime and every `/_orgs/{org}` route |
| Other organizations | a session sees only its own: `GET /_orgs` goes and `/me` shows the one organization |
| Several organizations | one token each; the clients hold them with `client.org(id)` |
| Creating organizations | `POST /_auth/orgs` for a new person (account, organization and session in one call); `POST /_orgs` from a session answers a **second session** scoped to the new organization |
| Invitations | accepted **while signing in** (`invitation` in the org-bound `login`, `signup` and `oauth/start`); there is no separate accept call |
| Background work and API keys | jobs carry the scope of the session that queued them; a key acting on behalf of a user is bound to one organization |
| Enforcing the connection | `sso_required` on the organization (§6) replaces the membership-counting `require_sso` of #600 |
| Account-level actions | a session opened through a connection cannot change credentials, link identities, delete or export the account (`403 sso_session`) |
| Session lifetime | an organization may shorten the lifetime of its sessions (`session_max_age`); never longer than the realm's |
| Removing access | removing a member, leaving, deleting the organization or removing its connection revokes the sessions it scopes |
| Out of scope | SCIM, SAML, group mapping (§11) |

## 3. Configuration

No new realm key: `organizations.enabled` implies scoped sessions, and `organizations.sso` stays as [org-sso.md §4](./org-sso.md#4-connections-issue-303) defines it. Startup checks gain:

- `invitation_link` must contain `{org}` (the link is how the app learns which organization to sign in to); today it is optional;
- `organizations.lookup: true \| false` (default `true`) switches the slug lookup of §5.3 off, for realms whose customers' names are confidential (they then keep the id in their own app, or use verified-domain discovery, #601);
- the realm's `signup` (`open`, `invite`, `closed`) governs `POST /_auth/orgs` for new addresses (§5.2); joining an existing organization always needs an invitation (or a connection that admits the person, [org-sso.md §6](./org-sso.md#6-accounts-and-joining-issue-305)).

## 4. Scoped sessions

### 4.1 The session

`sessions.org` already exists and records the organization whose connection opened the session. It now means **the scope**, is set by every sign-in, and never changes. Nothing else about a session changes: the token is the same random `bkd_…` string stored hash-only, with the same idle timeout, maximum lifetime, `methods` and `mfa` ([mfa-passwordless.md §4.2](./mfa-passwordless.md#42-sessions-remember-how-they-were-created)).

- A session with no `org` in a realm with organizations (one that existed before the upgrade) answers `401 session_unscoped` on every route except `logout`; the person signs in again. No attempt is made to convert tokens.
- `GET /_auth/sessions` shows the organization of each session; the audit record of a sign-in carries it; `backd user` shows it.
- A session opened through a connection is told apart by `methods` (the provider's name) together with `org`.

### 4.2 What a scoped session reaches

Every surface that reads memberships goes through `Org.Counts(mfa, scope)` (today `Org.Counts(mfa)`, `internal/auth/orgs.go`): an organization counts only if it is active (or orphaned), its `require_mfa` is met, **and it is the session's organization**. The first issue starts by finding every path that reads memberships without it; a property test with callers holding several memberships proves the other organizations never appear.

| Surface | Behaviour |
|---|---|
| Rules | `inOrg`, `hasOrgRole`, `orgClaims` work as before; `user.orgs` has exactly one entry and `user.org` is its id. Pushdown becomes `_meta.org == <scope>`, which indexes serve better than `in [..]` |
| Creating documents | `organization: required` and `optional` collections take `_meta.org` from the session. `?org=` is not accepted (`400 unknown_parameter`). `organization: none` collections are unaffected |
| Documents outside any organization | `organization: none` collections with owner rules stay reachable: they are the person's own. The docs state plainly that apps built on organizations keep their data in organization collections (§8) |
| `GET /_orgs`, `/_orgs/{other}/…` | `GET /_orgs` is removed; `/me` returns `org`. `/_orgs/{org}/…` for any other organization answers `403 not_org_member`, as for a non-member |
| Realtime | the stream is bound to the token; revocation closes it; subscriptions follow the same rules |
| Files | follow the document that holds them |
| Functions and hooks | `ctx.user.org` is the scope and `ctx.user.orgs` has one entry; `ctx.admin.db` is unaffected |
| Aggregations | the `org` parameter goes (the scope applies); `team` stays |

### 4.3 Background work keeps the scope

A job queued by a scoped session records the organization with its caller (`Job.CallerOrg`, beside `CallerActor` and `CallerUserID`) and the worker loads memberships with that scope, so work started in Acme cannot read Beta when it runs later. Cron and system jobs have no caller and are unaffected.

### 4.4 API keys

A key acting on behalf of a user (`X-Backd-On-Behalf-Of`) is **bound to one organization when it is created** (`backd apikey create --org <id>`, `org` in the admin API) and counts only that organization. Keys without a user still have no memberships. Changing a key's organization needs reauth, like widening it.

### 4.5 Ending access

`Counts` already denies a session the moment its membership, its organization or its MFA condition fails. Revocation is hygiene on top:

- removing a member, a member leaving, or the organization being deleted revokes that person's sessions scoped to it;
- an `orphaned` organization keeps its members' sessions, as it keeps their access;
- removing or disabling a connection revokes the sessions it opened.

### 4.6 Session lifetime

`PATCH /_orgs/{org}` accepts `session_max_age` (`manage: org`, with reauth; at least one hour, never longer than the realm's `sessions.max_lifetime`). The expiry of a session is fixed when it is created, from its organization's value, and never extended. This is what ends the backd access of someone removed at their identity provider until SCIM exists (§11).

### 4.7 Rate limits

Sign-in limits that were per email and per IP become per email *and organization*, plus per IP, so a spray against one tenant does not lock a person out of another.

## 5. Signing in to an organization

Paths are relative to `/v1/{realm}`. **`{org}` is always the organization id.**

| Method and path | Purpose |
|---|---|
| `GET /_auth/orgs/{org}/providers` | exists ([org-sso.md §5](./org-sso.md#5-signing-in-issue-304)); the answer also lists the organization's other methods: `{ items: ["google"], methods: ["password", "magic_link"], sso_required: false }`. An unknown, inactive or connection-less id answers as a plain organization, so the shape never shows that it exists |
| `POST /_auth/orgs/{org}/login` | password; accepts `invitation` |
| `POST /_auth/orgs/{org}/signup` | new account; **requires** `invitation` |
| `POST /_auth/orgs/{org}/magic-link` | request a link; redeemed through the login-code exchange |
| `POST /_auth/orgs/{org}/reset-password/request` | request a reset email |
| `…/oauth/{provider}/{start,callback,id-token}` | exists; accepts `invitation`; realm-level providers sign in to the organization through the same routes |
| `GET /_auth/orgs/lookup?slug=` | the id and name of the organization with that slug (§5.3) |
| `POST /_auth/orgs` | a new person creates an account and an organization (§5.2) |
| `POST /_auth/token` | redeem the one-time login code; the code carries the organization |
| `POST /_auth/mfa/verify` | second factor; the challenge carries the organization |
| `POST /_auth/orgs/{org}/invitations/preview` | name and offer of an invitation, token in the body |

Unchanged, because they act on the token or an emailed token: `logout`, `logout-all`, `me`, `sessions`, `reauth`, `mfa/*`, `consents`, `email` change, `reset-password` (the emailed token), `verify-email`.

Removed in realms with organizations: `/_auth/login`, `/_auth/signup`, `/_auth/magic-link`, `/_auth/oauth/…`, `/_auth/providers`, `/_orgs/invitations/accept`, the anonymous `POST /_orgs`, and the `org` field in their bodies (the path replaces it).

### 5.1 Rules shared by the routes

- **Membership is checked after the credentials.** A person who is not a member, an organization that does not exist and one that is not active all answer as a wrong password does (`401 invalid_credentials`, same timing: the password is hashed anyway).
- **MFA, consent and verification steps** work as today; the challenge records the organization and the session is created scoped to it.
- **Invitations are accepted here.** A valid `invitation` creates or extends the membership in the same step, after the first factor, with the binding rules of [organizations.md §7.1](./organizations.md#71-creating): an `email` binding needs the verified email, a `user` binding that user; an invitation for someone else answers `403 invitation_not_for_you`. A person signing in with a valid invitation is therefore *not* refused as a non-member. In a connection's flow the invitation also clears a `blocked` entry ([org-sso.md §6](./org-sso.md#6-accounts-and-joining-issue-305)).
- **`signup` always needs an invitation.** The account is born verified, as invitations by email already are ([account-lifecycle.md §4.5](./account-lifecycle.md#45-invitations-by-email-issue-49)).
- **Emails** (magic link, reset, verification) take the organization from the path, which replaces the optional `org` field of [team-access.md §10](./team-access.md#10-company-context-and-template-data-hooks-issue-2410); they are sent only to members, with the same answer and timing otherwise.
- **Previews** need no session: the token is 256 random bits and the call is limited per IP. Unknown, expired, revoked and used-up tokens answer the same `400 invalid_invitation`.

### 5.2 Creating an organization

- **A new person:** `POST /_auth/orgs { email, password, name, slug?, locale?, consents? }` creates the account (the sign-up table of [account-lifecycle.md §4.1](./account-lifecycle.md#41-sign-up) and the realm's `signup` mode), the organization with the person as `creator_roles`, and a session scoped to it. With `require_verified_email: true` the answer is `202` and no session: the organization exists, the verification email is sent, and the person signs in to it afterwards. `organizations.create` is **not** evaluated for this first organization (a new account cannot satisfy `user.email_verified`); `max_created_per_user` still applies.
- **A registered address:** answers as sign-up does today (`409 email_taken`, or the generic `202` when verification is required). The person signs in to one of their organizations and creates another from there.
- **From a session:** `POST /_orgs` (with `organizations.create` evaluated as today) creates the organization and answers `{ org, token, expires_at }`: **a new session scoped to the new organization**. The caller's own session stays as it is.

### 5.3 Finding the organization

People sign in to "Acme", not to `org_cq…`. The id is what routes take; the slug is what people (and URLs: `acme.app.com`, `app.example/acme`) know.

- **`GET /_auth/orgs/lookup?slug=acme`** answers `{ id, name }` for an active organization with that slug, and the same `404` for an unknown slug, an organization without a slug, an inactive one, and a realm with `organizations.lookup: false`. Public, cacheable for a minute, limited per IP (30 a minute).
- **What it reveals:** that a slug is in use and the organization's display name, to anyone who guesses the slug. That is the same exposure [org-sso.md](./org-sso.md) accepted for the providers list (review item A1), moved to one place; operators who keep customer names secret choose unguessable slugs or set `organizations.lookup: false`.
- **Everything else takes the id.** The app calls the lookup once per page load or install and keeps the id; the redirect and the emails use ids.
- **From an email address** the way to find the organization is verified-domain discovery (#601), which answers only for domains an organization has proved.



Replaces #600. Because sessions are scoped, enforcing a connection is a rule about *how a person may sign in to this organization*, not a second membership filter.

- **Setting:** `PATCH /_orgs/{org}` with `sso_required: true` (`manage: org`, with reauth), the way `require_mfa` is set. It needs an enabled connection and that the person setting it has signed in through one (`verified_at` on the connection), so an owner cannot lock themselves out.
- **Effect:** `login`, `magic-link`, `reset-password/request` and the realm-level providers of that organization answer `400 sso_required`; `providers` says `sso_required: true` so the app shows only the connection's buttons. Leaving stays allowed.
- **Who is affected:** members who joined another way (an invited contractor, say) can no longer enter until they sign in through the connection. Before enabling it, managers see who has no identity on the connection (`GET /_orgs/{org}/members?sso=false`); the response of `PATCH` lists the count.
- **Existing sessions** of the organization that were not opened through a connection are revoked when it is enabled.
- **Break glass:** realm admins clear it (`PATCH /_admin/orgs/{org}`, `backd org sso required off`), audited.
- **Interplay:** an organization's `require_mfa` still applies; the provider's own second factor is not trusted ([org-sso.md §6](./org-sso.md#6-accounts-and-joining-issue-305)).

## 7. Account-level actions

Changing a password, an email, a sign-in method, MFA, API keys, deleting or exporting the account concern the *person*, not the organization, and work from any scoped session **except one opened through a connection**.

A connection session proves "the identity provider of organization A says this is ana@acme.com". It does not prove the person owns the account, and A's provider must not be able to change the credentials that protect the person's other organizations. So for those sessions:

- the endpoints of [mfa-passwordless.md §6.2](./mfa-passwordless.md#62-re-authentication) that need reauth for account changes (MFA, password change or removal, email change, unlinking a provider, API keys), `DELETE /_auth/me` and the personal data export answer `403 sso_session`: *this action needs a sign-in with your own credentials*;
- organization management (`manage: …`) works, with reauth by `email_code` (a person who exists only through a connection has no password or MFA);
- someone who wants credentials of their own uses password reset or a magic link from an organization that allows them.

The export would gather the person's documents in *every* organization, so Acme's provider must not be able to ask for it. People who exist only through a connection cannot export or erase their account themselves in this milestone; administrators do it for them (`backd user export`, `backd user erase`, [compliance.md](./compliance.md)). The docs say so, and it is a follow-up (§11).

## 8. Clients (JavaScript and Dart)

### 8.1 One organization

```js
const client = createClient({ url, realm, org: orgIdFromHost, storage })
await client.auth.login({ email, password })     // POST /_auth/orgs/{org}/login
await client.auth.oauth.start({ provider })      // …/oauth/{provider}/start
await client.collection('profiles').create({})   // no org argument: the session decides
```

- `createClient({ org })` binds the client to an organization, by id (`org: 'org_…'`) or by the slug people know (`org: { slug: 'acme' }`, resolved once through the lookup of §5.3 and cached; `await client.ready` waits for it). `client.orgs.lookup('acme')` does the same by hand. Without `org`, the sign-in calls take it as an argument.
- `client.auth.session.org` is the organization id (synchronous, from the sign-in answer). `await client.auth.org()` returns `{ id, name, roles, teams }` from `/me`.
- `Collection.inOrg()` is **removed**, and `inTeam` no longer needs a preceding call. Generated types lose `inOrg`.

### 8.2 Several organizations

```js
const acme = client.org('org_a')     // same url, realm, transport and cache; its own token
const beta = client.org('org_b')
await acme.auth.login({ email, password })
await beta.auth.oauth.start({ provider: 'microsoft' })
client.orgs.signedIn()               // [{ id, expiresAt }]: organizations with a stored token
await acme.auth.logout()             // signs out of acme only
```

- `client.org(id)` returns a cached client sharing everything except the token slot and the events (`session`, `expired` are per organization). `logoutAll()` still revokes all of the person's sessions and says so.
- `TokenStorage` takes an optional slot (`get(slot)`, `set(slot, token)`, `clear(slot)`); the default slot keeps today's behaviour for custom storages. localStorage keys become `backd:{realm}:{org}`. Dart's `TokenStorage` and `FileTokenStorage` follow.
- **Cookie sessions** (`cookies: true`) hold one organization at a time, because the browser sends every cookie of the origin and the server cannot tell which one a request means. Switching organization means signing in again. Token mode has no such limit.
- **Realtime** opens one connection per organization client; apps normally subscribe only on the active one.
- **The redirect:** the state already remembers the organization; the callback adds `backd_org` beside `backd_code`, and `completeSignIn()` (and `completeOnLoad`) saves the token in that organization's slot.
- **Finding the organization:** by slug through the lookup (a URL per company, or a "company" field on the sign-in page); from an email address through verified-domain discovery (#601).

## 9. Documentation

- **New page "Where do documents live?"** (in `build/organizations/`): `organization: required | optional | none` as a decision table, what each means for sign-in, rules and clients, and Shelf's profile as the worked example. It states that apps built on organizations keep their data in `required` collections; `none` is for data that belongs to the person across organizations.
- **Updated:** every page that mentions `?org=` or `inOrg` (`build/organizations/*`, `build/api/*`, `build/clients/*`, `build/configuration/realm.md`, `config-dir.md`), `build/auth/sessions.md`, `providers.md`, the organization sign-in guide of milestone 30, the Shelf tutorial chapters that use organizations (20, 22, 24), the Upgrading page and the CHANGELOG.
- **OpenAPI:** every route of §5, `400 sso_required`, `401 session_unscoped`, `403 sso_session`, and the removal of `org` parameters.

## 10. Upgrading and security

**Upgrading.** A breaking change for realms that use organizations, taken on purpose: there is no compatibility mode.
- `backd provision` needs no new collections; it creates the indexes that `_meta.org` equality uses.
- Existing sessions answer `401 session_unscoped`; people sign in again.
- Apps switch to the org-bound sign-in calls and drop `?org=` and `inOrg()`; realm admins read the organization ids from `GET /_admin/orgs`.
- `examples/config/shelf`, `adminui` and `hr` declare `organizations:`; they, the tutorial chapters and `tour.js` change in the same milestone.
- Realms without `organizations.enabled` change nothing.

**Security.**
- **Cross-organization leakage:** one choke point, property-tested across every surface of §4.2, plus a test that fails when a store method reads memberships without a scope.
- **Enumeration:** `providers`, login, reset and magic-link answers are uniform and take ids, which are unguessable. The slug lookup is the one place where a guessed slug confirms an organization (§5.3): limited per IP, uniform `404`, and switchable off.
- **Confused deputy:** login codes, attempts and MFA challenges carry the organization, and the session is built from them, not from a request parameter.
- **Lock-out:** `sso_required` needs a successful connection sign-in first, and realm admins can clear it.
- **Stale provider membership:** `session_max_age`; SCIM later.
- **Stolen tokens:** a token reaches one organization only; that is the point.

## 11. Follow-ups

- **SCIM provisioning** (deprovisioning that ends access at once, group-to-role mapping): needs a study of its own before it is scheduled.
- SAML for organizations whose provider cannot do OpenID Connect.
- Re-authentication of connection sessions through the provider (`prompt=login`), instead of an email code.
- Self-service export and erasure for people who exist only through a connection.
- Trusting a provider's second factor for `require_mfa`.

## 12. Issues

New milestone `34-scoped-sessions`, after `30-org-sso` has merged:

| Issue | Title |
|---|---|
| 34.1 | Design: scoped sessions |
| 34.2 | Scoped sessions: `sessions.org` enforced, `Counts`, `?org=` and `GET /_orgs` removed, jobs, keys, revocation, `session_max_age`, `session_unscoped` |
| 34.3 | Org-bound sign-in: `login`, `signup`, `magic-link`, reset, `providers`, invitations at sign-in, the slug lookup, `POST /_auth/orgs`, `POST /_orgs` with a second session |
| 34.4 | JavaScript and Dart clients: `createClient({ org })` (id or slug), `client.org(id)`, slotted storage, `inOrg` removed, types |
| 34.5 | Examples, tutorial and documentation (Shelf, adminui, hr, "Where do documents live?") |
| 34.6 | Require organization sign-in (`sso_required`, `sso_session`) — replaces #600 |
| 34.7 | Security review |

### Changes proposed to milestone `31-org-sso-extensions`

| Issue | Change |
|---|---|
| #597 Design: extensions | Depends on this design too; it must not describe `require_sso` as a membership filter any more |
| #598 Apple | No change; its routes follow the id-only rule |
| #599 Generic OIDC | No change; add that an owner-chosen issuer always refuses private, loopback, link-local and metadata addresses, and `https` only (outside `BACKD_DEV`) |
| #600 Require organization sign-in | **Close**, replaced by 34.6: a rule about how people may sign in to an organization (`400 sso_required`, `sso_session` limits), not a `403 org_requires_sso` filter on sessions |
| #601 Verified domains and discovery | Keep. Linking an existing account by verified email only when the domain is verified for the organization **and** the account is already a member or invited, never to a stranger's account. The email lookup is the one way to find an organization from an address; it is the only discovery path in the API, limited per IP and answering only for verified domains |
| #602 Review and docs | Add leakage tests over scoped sessions and the enforcement paths; depends on 34.6 |

And one change to milestone `30-org-sso`, before it merges: **#669 (30.4) accepts the slug as well as the id in `{org}`**. With the lookup of §5.3, `…/orgs/{org}/…` takes the id only and the slug is resolved in one place, instead of every route having two ways to name an organization.
