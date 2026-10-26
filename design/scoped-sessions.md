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
| Signing in | only through `/v1/{realm}/_auth/orgs/{org}/…`; the unscoped `signup`, `magic-link` and `oauth` routes no longer exist in such realms, and `login` stays for **operators** alone (§5.5) |
| The organization in a path | the **id**. Only the routes that run before there is a session (§5) also take the **slug** people know (`acme`), because the person does not know an id; every route that needs a session takes the id only (this narrows "the id or the slug" of [org-sso.md §2](./org-sso.md#2-decisions-at-a-glance)) |
| The slug | **required** when an organization is created; it is the name people type, shown in URLs and emails |
| Forgot the company | `POST /_auth/orgs/find { email }` always answers `202` and emails the address its companies with sign-in links (§5.4) |
| Sign-in experience | company from the URL or one prefilled field, then the methods the organization allows; one generic failure message; the browser remembers companies, the API never lists them (§8.3) |
| No enumeration | there is **no lookup endpoint**: a slug is resolved inside the sign-in call, which answers the same for an unknown organization, a non-member and a wrong password (§5.3) |
| Choosing an organization | `?org=` and `inOrg()` are **removed**; the session decides |
| Access | one choke point, `Org.Counts(mfa, scope)`, used by rules, `ctx.user`, realtime and every `/_orgs/{org}` route |
| Other organizations | a session sees only its own: `GET /_orgs` goes and `/me` shows the one organization |
| Several organizations | one token each; the clients hold them with `client.org(id)` |
| Creating organizations | `POST /_auth/orgs` for a new person (account, organization and session in one call); `POST /_orgs` from a session answers a **second session** scoped to the new organization |
| Invitations | accepted **while signing in** (`invitation` in the org-bound `login`, `signup` and `oauth/start`); there is no separate accept call |
| Background work and API keys | jobs carry the scope of the session that queued them; a key acting on behalf of a user is bound to one organization |
| Enforcing the connection | `sso_required` on the organization (§6) replaces the membership-counting `require_sso` of #600 |
| Operators | realm administrators sign in with `POST /_auth/login` (admin role only, uniform failure otherwise); their session has no organization and reaches the admin API alone (§5.5) |
| Account-level actions | a session opened through a connection cannot change credentials, link identities, delete or export the account (`403 sso_session`) |
| Session lifetime | an organization may shorten the lifetime of its sessions (`session_max_age`); never longer than the realm's |
| Removing access | removing a member, leaving, deleting the organization or removing its connection revokes the sessions it scopes |
| Out of scope | SCIM, SAML, group mapping (§11) |

## 3. Configuration

No new realm key: `organizations.enabled` implies scoped sessions, and `organizations.sso` stays as [org-sso.md §4](./org-sso.md#4-connections-issue-303) defines it. Startup checks gain:

- `invitation_link` must contain `{org}` (the link is how the app learns which organization to sign in to); today it is optional;
- **the slug is required** to create an organization (`POST /_auth/orgs`, `POST /_orgs`, the admin API and `backd orgs create`), because it is what people type. It keeps today's format (lowercase letters and digits separated by single `-`, at most 63 characters) and may not look like an id (`[0-9a-v]{20}`), so a name can never be mistaken for another organization's id. Organizations that exist without one still sign in by id; startup lists them and `backd orgs set-slug <org> <slug>` sets one;
- `organizations.signin_link` (for example `"https://{slug}.app.example/signin"`; `{slug}` and `{org}` are replaced, and it must be within `allowed_redirects`) is where the emails of §5.4 send people. It is optional: without it (or without `email:`) `POST /_auth/orgs/find` answers `404`;
- the realm's `signup` (`open`, `invite`, `closed`) governs `POST /_auth/orgs` for new addresses (§5.2): an invite-only realm lets a person start a company with the realm's invitation; joining an existing organization always needs an invitation of that organization (or a connection that admits the person, [org-sso.md §6](./org-sso.md#6-accounts-and-joining-issue-305)). An account that exists without a team (made before the upgrade) is given one by an operator (`backd orgs create --owner`).

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

Sign-in limits stay per email and per client address, as they are. A limit per email *and organization* was considered and rejected: whoever guesses passwords for one account could rotate the organization they name, and every name would start from zero. The cost is that a spray at one person's address slows their sign-in to every organization, which is the case today too.

## 5. Signing in to an organization

Paths are relative to `/v1/{realm}`. **`{org}` is the organization id; the routes marked † also take the slug (§5.3).**

| Method and path | Purpose |
|---|---|
| `GET /_auth/orgs/{org}/providers` † | exists ([org-sso.md §5](./org-sso.md#5-signing-in-issue-304)); the answer also lists the organization's other methods: `{ items: ["google"], methods: ["password", "magic_link"], sso_required: false }`. An unknown, inactive or connection-less id answers as a plain organization, so the shape never shows that it exists |
| `POST /_auth/orgs/{org}/login` † | password; accepts `invitation` |
| `POST /_auth/orgs/{org}/signup` † | new account; **requires** `invitation` |
| `POST /_auth/orgs/{org}/magic-link` † | request a link; redeemed through the login-code exchange |
| `POST /_auth/orgs/{org}/reset-password/request` † | request a reset email |
| `…/oauth/{provider}/{start,callback,id-token}` († `start` and `id-token`) | exists; accepts `invitation`; realm-level providers sign in to the organization through the same routes |
| `POST /_auth/orgs` | a new person creates an account and an organization (§5.2) |
| `POST /_auth/orgs/find` | "which companies am I in?": always `202`; the answer goes to the mailbox (§5.4) |
| `POST /_auth/token` | redeem the one-time login code; the code carries the organization |
| `POST /_auth/mfa/verify` | second factor; the challenge carries the organization |
| `POST /_auth/orgs/{org}/invitations/preview` † | name and offer of an invitation, token in the body |

Unchanged, because they act on the token or an emailed token: `logout`, `logout-all`, `me`, `sessions`, `reauth`, `mfa/*`, `consents`, `email` change, `reset-password` (the emailed token), `verify-email`.

Removed in realms with organizations (`login` stays for operators, §5.5): `/_auth/signup`, `/_auth/magic-link`, `/_auth/oauth/…`, `/_auth/providers`, `/_orgs/invitations/accept`, the anonymous `POST /_orgs`, and the `org` field in their bodies (the path replaces it).

### 5.1 Rules shared by the routes

- **Membership is checked after the credentials.** A person who is not a member, an organization that does not exist and one that is not active all answer as a wrong password does (`401 invalid_credentials`, same timing: the password is hashed anyway).
- **MFA, consent and verification steps** work as today; the challenge records the organization and the session is created scoped to it.
- **Invitations are accepted here.** A valid `invitation` creates or extends the membership in the same step, after the first factor, with the binding rules of [organizations.md §7.1](./organizations.md#71-creating): an `email` binding needs the verified email, a `user` binding that user; an invitation for someone else answers `403 invitation_not_for_you`. A person signing in with a valid invitation is therefore *not* refused as a non-member. In a connection's flow the invitation also clears a `blocked` entry ([org-sso.md §6](./org-sso.md#6-accounts-and-joining-issue-305)).
- **`signup` always needs an invitation.** The account is born verified, as invitations by email already are ([account-lifecycle.md §4.5](./account-lifecycle.md#45-invitations-by-email-issue-49)).
- **Emails** (magic link, reset, verification) take the organization from the path, which replaces the optional `org` field of [team-access.md §10](./team-access.md#10-company-context-and-template-data-hooks-issue-2410); they are sent only to members, with the same answer and timing otherwise.
- **Previews** need no session: the token is 256 random bits and the call is limited per IP. Unknown, expired, revoked and used-up tokens answer the same `400 invalid_invitation`.

### 5.2 Creating an organization

- **A new person:** `POST /_auth/orgs { email, password, name, slug, invitation?, locale?, consents? }` creates the account (the sign-up table of [account-lifecycle.md §4.1](./account-lifecycle.md#41-sign-up) and the realm's `signup` mode: anyone when it is `open`, a person holding the `invitation` of the realm when it is `invite`, nobody when it is `closed`), the organization with the person as `creator_roles`, and a session scoped to it. With `require_verified_email: true` the answer is `202` and no session: the organization exists, the verification email is sent, and the person signs in to it afterwards. `organizations.create` is **not** evaluated for this first organization (a new account cannot satisfy `user.email_verified`); `max_created_per_user` still applies.
- **The slug** is asked for with the company name; the form suggests one from the name ("Acme Corp" → `acme-corp`). A taken slug answers `409 slug_taken` with no detail about the other organization. This is the one place where a slug shows that it exists: unique names cannot be handed out without saying so, and it is limited to 20 an hour per address.
- **A registered address:** answers as sign-up does today (`409 email_taken`, or the generic `202` when verification is required). The person signs in to one of their organizations and creates another from there.
- **From a session:** `POST /_orgs` (with `organizations.create` evaluated as today) creates the organization and answers it as before with `session: { token, expires_at, org }` added: **a new session scoped to the new organization**. The caller's own session stays as it is.

### 5.3 Finding the organization

People sign in to "Acme", not to `org_cq…`. The id is what routes take once there is a session; before it, the routes marked † also accept the slug, so `acme.app.com`, `app.example/acme` or a "company" field on the sign-in page are enough.

- **Resolved inside the call.** The slug is looked up as part of the sign-in itself, so there is no endpoint that answers "does this slug exist?". An unknown slug, the slug of an inactive organization, a non-member and a wrong password produce the same answer, with the same work done (the password is hashed anyway). Ids and slugs share one path segment: an id is tried first, and slugs cannot look like an id (§3).
- **What still shows:** only what a working connection needs to show. `providers` lists the buttons of an organization that has a connection and answers like a plain organization otherwise ([org-sso.md](./org-sso.md), review item A1, accepted there). Nothing is learnt about organizations without a connection.
- **Everything after sign-in uses the id.** The sign-in answer returns it (`org`), the clients keep it, redirects and emails carry it, and routes that need a session take the id only.
- **When the person does not know it,** the "which companies am I in?" email (§5.4) tells the owner of the address, and verified-domain discovery (#601) answers only for domains an organization has proved.

### 5.4 Which companies am I in?

A person on a new device may not remember the slug, or which of their companies they wanted. Nothing in the API lists organizations, so the answer goes where only the owner of the address can read it.

- **`POST /_auth/orgs/find { email, locale? }`** always answers `202 Accepted`, the same body and timing for an address with accounts, one without, and one that belongs to no organization.
- **The email** (`organizations-found`, an email kind with defaults and localized templates like the others) goes only to an address that has an account with memberships. It lists each active organization by name with its sign-in link (`organizations.signin_link` with the slug and id filled in), and says "if you didn't ask for this, ignore it". Organizations that require SSO are listed too; their link leads to the SSO button.
- **Limits:** 3 a hour per address and 30 per IP, using the shared email counters; the realm's email limits apply. (The review kept the per-IP number above the per-address one: an office behind one address must not lock its colleagues out of asking.)
- **Needs** `email:` in the realm.

### 5.5 Operators

Realm administrators (users holding a role with `admin`) are not members of anything: their power is the admin API, and the admin UI, the CLI and the tools sign them in with a user session. They sign in at the realm level, so no organization has to be invented for them:

- `POST /_auth/login` (and its second step, `POST /_auth/mfa/verify`) stays, **for users who hold an admin role**. For anyone else it answers `401 invalid_credentials` after the same work, as a wrong password does, so nothing says who is an operator.
- The session has no `org`. It reaches the admin API and, through `Counts(mfa, scope)`, no organization at all; an operator who needs a team's data signs in to that team like anyone.
- Operators are created as today (`backd bootstrap`, the admin API); the MFA a realm requires of its admin role works as it does.

## 6. Requiring the connection

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

- `createClient({ org })` binds the client to an organization, by id or by the slug people know (`org: 'acme'`). The sign-in calls send what the app gave; the answer carries the id, and from then on the client uses the id (the token slot is the id). Without `org`, the sign-in calls take it as an argument.
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
- **Finding the organization:** by slug in the sign-in calls (a URL per company, or a "company" field on the sign-in page); from an email address through verified-domain discovery (#601).

### 8.3 The sign-in experience

What a person sees decides whether this feels natural or like a chore, so the docs, the Shelf example and the clients follow these rules; a reference sign-in is part of 34.5.

| Person | Flow |
|---|---|
| Returning, company in the URL (`acme.app.com`, `/acme`) | the page already knows the company: it shows the methods that organization allows (`providers()`) and nothing else to type |
| Returning, one sign-in page | **Company** (prefilled with the last one used on this device, a "not yours?" link to change it), then the methods. On a device with one stored company, the field is already filled |
| Organization with SSO required | only "Continue with <Microsoft/Google>"; no password form that would fail |
| First time (new company) | email, password, **company name**; the slug is suggested from the name and can be edited; one call creates everything and signs in |
| Invited | the link carries the organization, so there is no company field: the page previews "Join Acme as editor?" and offers sign-in or sign-up |
| Another device, forgot the company | "Forgot your company?" under the field sends the `find` email of §5.4: the links in it open the right page |
| Several companies on this device | a switcher built from `client.orgs.signedIn()` (names are kept when the person signs in); picking one is instant, no new sign-in |

Rules for the page:
- **Ask the company only when the URL does not say it,** and keep it a single short field that accepts any case and surrounding spaces (`" Acme "` is `acme`).
- **Show the methods after the company, never before:** the sign-in options depend on it.
- **Say the same thing for every failure:** "We couldn't sign you in. Check the company, email and password." with the "Forgot your company?" and "Forgot your password?" links next to it. Do not tell which of the three was wrong; the API cannot.
- **Remember, don't list:** the browser remembers the companies it has signed in to (and their names); the API never lists others.
- **Come back to where the person was** after an SSO redirect or a magic link: the redirect already carries the organization and the page.
- **Sign-out** leaves the person on that company's sign-in page, with the company still filled in.

## 9. Documentation

- **New guide "The sign-in experience"** (in `build/organizations/`): the table and rules of §8.3 with copy-pasteable code for a company-in-the-URL page, a one-page sign-in with a company field, the switcher, the create-your-company page and the invitation page, in JavaScript and Dart.
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
- **Enumeration:** login, reset, magic-link and sign-up answers are uniform for ids and for slugs, and there is no lookup endpoint (§5.3). The only signal is `providers` for organizations that have a connection, which the review of milestone 30 accepted.
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
| 34.2 | Scoped sessions: the slug required and `backd orgs set-slug`, `sessions.org` enforced, `Counts`, `?org=` and `GET /_orgs` removed, jobs, keys, revocation, `session_max_age`, `session_unscoped` |
| 34.3 | Org-bound sign-in: `login`, `signup`, `magic-link`, reset, `providers`, invitations at sign-in, slugs in the pre-session routes with uniform answers, "which companies am I in?" (`organizations-found` email), `POST /_auth/orgs`, `POST /_orgs` with a second session |
| 34.4 | JavaScript and Dart clients: `createClient({ org })` (id or slug; the id is kept after sign-in), `client.org(id)`, slotted storage, `inOrg` removed, types |
| 34.5 | Examples, tutorial and documentation (Shelf, adminui, hr, a reference sign-in, "The sign-in experience", "Where do documents live?") |
| 34.6 | Require organization sign-in (`sso_required`, `sso_session`) — replaces #600 |
| 34.7 | Security review |

### Changes proposed to milestone `31-org-sso-extensions`

| Issue | Change |
|---|---|
| #597 Design: extensions | Depends on this design too; it must not describe `require_sso` as a membership filter any more |
| #598 Apple | No change; its routes follow the id-only rule |
| #599 Generic OIDC | No change; add that an owner-chosen issuer always refuses private, loopback, link-local and metadata addresses, and `https` only (outside `BACKD_DEV`) |
| #600 Require organization sign-in | **Close**, replaced by 34.6: a rule about how people may sign in to an organization (`400 sso_required`, `sso_session` limits), not a `403 org_requires_sso` filter on sessions |
| #601 Verified domains and discovery | Keep; it complements the "which companies am I in?" email of §5.4, which needs no proved domain. Linking an existing account by verified email only when the domain is verified for the organization **and** the account is already a member or invited, never to a stranger's account. The email lookup is the one way to find an organization from an address; it is the only discovery path in the API, limited per IP and answering only for verified domains |
| #602 Review and docs | Add leakage tests over scoped sessions and the enforcement paths; depends on 34.6 |

And one change to milestone `30-org-sso`, before it merges: **the organization routes that need a session (`/_orgs/{org}/sso`, `/_admin/orgs/{org}/sso`) accept the slug as well as the id**. They take the id only; the pre-session routes (`oauth/start`, `id-token`, `providers`) keep the slug (§5.3).

## 13. As built (34.6)

What the implementation decided where the design left room:

- **How a connection session is told apart.** Not by the provider's name, which the realm's own providers use too, but by its first method: `sso:{org}:{provider}` (`auth.ConnectionMethod`, `Session.Connection()`). The organization is in the name because a session can be copied to another organization (`POST /_orgs` keeps the methods): a connection session of A that opens one in B is still a connection session (so account-level actions stay refused) but is not B's connection's, so B's `sso_required` ends it. Removing a connection ends the sessions that carry its method, not those the realm's provider opened.
- **Where the rule is applied.** For `login`, `signup`, `magic-link` and `reset-password/request` it is one middleware on the organization's routes (`ssoGate`); the realm's own providers are refused in `provider()`, the one place that resolves the provider of a sign-in. The answer is `400 sso_required` before any credential is looked at, so it says the organization requires its connection, which `providers` says to anybody already; unknown, inactive and deleted organizations still answer like a wrong password. Codes and links issued before it was turned on live until they expire.
- **Sign-up is closed too.** An invited person who has no account makes a password account at `signup`, which is one of the ways the requirement closes; they join through the connection (the invitation is accepted in `oauth/start`).
- **Turning it on** needs `manage: org`, a fresh proof, a working connection (enabled, secret set, allowed by the realm) and a session of the caller opened through one of *this* organization's working connections (`409 sso_not_ready`). It counts the members with no identity on a connection, ends the organization's sessions not opened through a connection, and audits `org.sso_required` with both numbers. It is turned off by a manager with no further condition, and by realm administrators (`PATCH /_admin/orgs/{org}`, `backd orgs sso-required-off`), who cannot turn it on.
- **It releases itself.** Removing or disabling the last working connection (a manager removing it, `org.sso.disabled`, or `connections` replaced by a disabled one) clears `sso_required`, audited with the reason: otherwise the organization's people would have no way in until an administrator noticed. Administrators clear it as break glass for an organization that cannot reach its provider.
- **`403 sso_session`** is the answer of `accountReauthed`, used by every handler that asked for a proof to change the account (password, email, second factor and recovery codes, unlinking), by `DELETE /_auth/me`, the personal data export and the link intent of `oauth/start` and `id-token`; it comes before the re-authentication so the person is not asked for a proof that would not help. Organization management keeps `reauthed`: for someone who exists only through the connection the proof is the email code.
- **Not done.** User API keys are not a thing in this realm model (keys are an administrators' tool), so there is nothing to gate there. Self-service export and erasure for people who exist only through a connection stay a follow-up (§11).

## 14. Security review (issue 34.7)

Scope: cross-organization leakage over every surface of §4.2, the uniform failures, slug resolution inside sign-in, the "which companies am I in?" email and `409 slug_taken`, the confused-deputy paths (login codes, states, challenges), requiring the connection and the account-level limits. Method: reading the code of each path, then a test that tries it (`scope_leak_test.go`, `scope_review_test.go`, `sso_required_test.go`, `internal/auth/scopereaders_test.go`).

**What holds.**
- *Leakage.* A person with memberships in two organizations holds one session in each; lists, filters, counts, aggregates, reads and writes by id, creates, `/_orgs/{other}/…`, `/me`, rules (`user.orgs`) and `ctx.user.orgs` see only the session's own, and `?org=` is refused. Every reader of a person's memberships across organizations (`MyOrgs`, `OrgMembershipsOf`, …) is named in a test that fails when a new one appears, with the reason it may exist (erasure and account export are account-level; the `find` email goes to the mailbox; `NextAccessChange` returns a time).
- *Uniform failures.* An unknown organization, an inactive one, a non-member and a wrong password answer `401 invalid_credentials` after the same work; the slug is resolved inside the call, there is no lookup endpoint, and the preview answers one `400` for every token that does not work.
- *Confused deputy.* The organization travels with the thing that continues a sign-in, never with a request parameter: the OAuth state (`Org`, `ScopeOrg`; a callback of another place does not finish it), the login code, the MFA and consent challenges (`ViaOrg`) and the magic-link state. A connection session copied to a second organization (`POST /_orgs`) keeps its marker, which names the first organization.
- *Account-level limits.* Every handler that asks for a proof to change the account, deletes it, exports it, links a provider or sets up a second factor refuses a connection session before asking (`sso_session`).

**Findings fixed.**

| # | Finding | Fix |
|---|---|---|
| R1 | An attempt with the realm's own provider that began before an organization required its connection could finish after (the callback is the realm's, not the organization's) | The callback refuses it (`sso_required` to the app), and see R2 |
| R2 | A login code, magic link or challenge issued before the requirement could still open a session after it | `startSessionWith`, the one place a session is made, refuses a session scoped to an organization that requires its connection unless the first method is that organization's connection. Every earlier gate is now only the early, uniform answer |
| R3 | `POST /_auth/orgs` answered `409 slug_taken` with no limit, an unlimited slug oracle, and the sign-in routes that name an organization answered `sso_required` without one | 20 an hour per address at `POST /_auth/orgs`; 120 a minute per address across the password, magic link, reset and sign-up calls to organizations, the same for every name |
| R4 | `GET /_orgs/{org}/members?sso=false` was open to every member | It needs `manage: org`, the right that turns the requirement on |
| R5 | `GET /_auth/sessions` did not say which organization a session belongs to (§4.1) | Each item has `org` and, for a connection session, `connection` |

**Deviations from the design.** The audit record of a sign-in does not carry the organization: backd has no general sign-in record (only administrators' logins and `org.sso.login`, which targets the organization), so nothing was added. The `find` email is limited to 30 an hour per address of the client, not 3 (§5.4).

**Accepted risks.**

| # | Risk | Why |
|---|---|---|
| A1 | `409 slug_taken` at creation says a slug exists | Unique names cannot be handed out without it; limited to 20 an hour per address, and slugs are public names |
| A2 | Naming an organization at a sign-in route answers `400 sso_required` when it requires its connection | `providers` says the same to anybody, with a 60 a minute limit; the calls are limited to 120 a minute per address for every name |
| A3 | `POST /_auth/orgs/find` takes slightly longer for an address with an account (a lookup and a queued email) | Limited to 3 an hour per address and 30 per client; enumeration at that rate is not worth it and the answer is the same |
| A4 | The provider's administrator controls the mailboxes of their own domain, so for an account at that domain they can use password reset or a magic link wherever those are allowed | Inherent to email recovery of an address the company owns; an organization that wants no such door requires its connection, and `sso_session` stops them changing the account from the session the connection gives |
| A5 | The `org` field of the unscoped operator sign-in bodies only sets the context of the emails it causes | It tells nothing about who belongs where and the operators' routes are the only ones left |
| A6 | Cookie sessions hold one organization at a time | The browser sends every cookie of the origin; token mode has no such limit (§8.2) |
| A7 | Codes issued before `sso_required` was turned on are refused at redemption, but the early answer on the organization's routes is the same `400` for everybody who names it | See A2 |

