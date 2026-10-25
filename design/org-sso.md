# Design: organization sign-in (SSO) with Google and Microsoft

- **Issues:** features 30.2–30.7, docs 30.8, review 30.9 (milestone `30-org-sso`); continuation in milestone `31-org-sso-extensions`
- **Status:** decided
- **Related designs:** [identity-providers.md](./identity-providers.md) (the flow this reuses), [organizations.md](./organizations.md) (members, roles, capabilities, deletion), [export-import.md](./export-import.md) (auth data in packages), [field-encryption.md](./field-encryption.md) (independent: see §2), [breach-response.md](./breach-response.md) (secrets in the runbook), [account-lifecycle.md](./account-lifecycle.md) (sign-up modes, erasure)

## 1. The idea

Today a realm has one set of sign-in providers, declared in `realm.yaml`, with client secrets held as realm secrets. A company that uses backd's organizations wants its own people to sign in with **its** Google Workspace or Microsoft Entra tenant, to be joined to **its** organization when they do, and to configure that itself, without the realm operator editing files or handling the company's secrets.

An organization **connection** is a provider configured for one organization: its client id, a client secret kept as an organization-scoped secret, and the restriction that says who may come in. Signing in through it uses the same redirect flow, login code and session as any provider; what is new is the address, the credentials, the restriction and the join.

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Secrets | A third scope for the existing secrets store: **organization**, owned by the realm, written by the organization's managers, **never readable by anyone** (not by them, not by functions); realm admins can replace and delete them. No dependency on field encryption (that protects document fields; secrets already use the instance's secrets key) |
| Addresses | `/v1/{realm}/_auth/orgs/{org}/oauth/{provider}/{start,callback,id-token}`: inside the reserved `_auth` namespace, so it can't clash with a database, and it needs no session. `{org}` is the id (the slug is also accepted by the routes that run before there is a session, which resolve it inside the sign-in so that nothing answers "does this slug exist?"; [scoped-sessions.md](./scoped-sessions.md) §5.3) |
| Who manages | Roles that hold a new `manage: sso` capability (the docs' example gives it to `owner` and `admin`), with reauth for changes |
| Providers | `google` and `microsoft` only (milestone 30). Apple and generic OpenID Connect follow in `31-org-sso-extensions` |
| Realm control | Off unless `organizations.sso.enabled: true` in `realm.yaml`; the realm lists the allowed providers and caps connections per organization |
| Who may come in | Cryptographic restrictions, not claimed domains: Google's `hd` (Workspace domain) or Microsoft's `tid` (a tenant id, never `common`), checked on the verified ID token |
| Accounts | An organization sign-in **never links to an existing account by email**. A person with an account links explicitly (`intent=link`) |
| Joining | At the first sign-in through the connection, with the connection's `join_roles`; a member a manager removed stays out until invited again |
| Identity key | `identities` gain an optional `org`; unique on `(provider, org, subject)`; a user holds one identity per provider per organization |

### 2.1 Addresses

Two prefixes, each already in use, and the rule that picks one: **`_orgs` is the organizations API** (session required, a reserved top-level name so it can't clash with a database), and **`_auth` is the sign-in API** (no session, its sub-resources are plain words: `oauth`, `mfa`, `devices`). The sign-in address therefore reads `_auth/orgs/…` (the resource `orgs` inside `_auth`), never `_auth/_orgs/…`.

| Purpose | Address |
|---|---|
| Sign in through a connection | `/v1/{realm}/_auth/orgs/{org}/oauth/{provider}/{start,callback,id-token}` |
| Providers a client may offer (public) | `/v1/{realm}/_auth/providers` and `/v1/{realm}/_auth/orgs/{org}/providers` |
| Manage a connection (managers) | `/v1/{realm}/_orgs/{org}/sso` and `…/sso/{provider}` |
| Manage a connection (realm admins) | `/v1/{realm}/_admin/orgs/{org}/sso` and `…/sso/{provider}` |
| Redirect address to register with the provider | `{public URL}/v1/{realm}/_auth/orgs/{org id}/oauth/{provider}/callback` |

## 3. Organization secrets (issue 30.2)

- **Storage:** the same collection and cipher as realm and database secrets. The document has `org` instead of `database`; the id is `org:<org id>:<NAME>` (two colons, so it can't equal a database secret's `<database>:<NAME>`). The cache key includes the scope.
- **Names:** the usual `NAME` rule. Connections use fixed names (`SSO_GOOGLE_CLIENT_SECRET`, `SSO_MICROSOFT_CLIENT_SECRET`).
- **References:** `registry.SecretRef` gains `Org`, set only by the sign-in code. `function.yaml` has no syntax for it, so functions can't reach organization secrets.
- **Writing:** the connection endpoint (§4) takes `client_secret` in the body and stores it; there is no general secrets API for organization managers. Realm admins: `backd secret set --realm r --org ORG NAME`, `backd secret list` (names and dates only), `backd secret delete`.
- **Rotation of the instance key** re-seals every scope; the existing rotation path must include this one.

## 4. Connections (issue 30.3)

`org_connections` (auth data, so in the realm's system database):

| Field | Notes |
|---|---|
| `id`, `org`, `provider` | unique on (`org`, `provider`), and `id` is `<org>:<provider>`; `provider` is `google` or `microsoft` |
| `client_id` | the organization's application with the provider |
| `tenant` | Microsoft: a tenant id (GUID). Required; `common`, `organizations` and `consumers` are refused |
| `hosted_domain` | Google: the Workspace domain. Required; a consumer account has no `hd` and is refused |
| `email_domains` | optional extra restriction on the verified email's domain |
| `join_roles` | roles given on joining; checked with the granting rules of [organizations.md §5.1](./organizations.md): a manager can't give what they don't hold |
| `enabled`, `blocked` | `blocked` is the ids of members removed by a manager, up to 1000 |
| `created_by`, `updated_by`, `created_at`, `updated_at` | `updated_by` is who last set it: the one whose capabilities `join_roles` were checked against (§9) |

- **API:** `GET /v1/{realm}/_orgs/{org}/sso`, `PUT /v1/{realm}/_orgs/{org}/sso/{provider}` (with `client_secret` when it is set or changed), `DELETE /v1/{realm}/_orgs/{org}/sso/{provider}`; realm admins have the same under `/v1/{realm}/_admin/orgs/{org}/sso` and `backd orgs sso`, `sso-set`, `sso-delete` (every CLI value is a flag, so the subcommands are single words, as `claims-set`). `GET …/sso/{provider}` reads one. Changes need `manage: sso` and reauth; every one is audited (`org.sso.set`, `org.sso.deleted`) without the secret.
- **Response:** never the secret; whether it is set, and the redirect address to register with the provider.
- **Validation:** the provider-field rules of `realm.yaml` (client id patterns, GUID tenants) come from one function extracted from the realm loader, so there is a single validator.
- **Realm switch:** `organizations.sso: { enabled, providers: [google, microsoft], max_per_org }`. With it off the whole surface answers `404`.

## 5. Signing in (issue 30.4)

- **Start:** `GET|POST /v1/{realm}/_auth/orgs/{org}/oauth/{provider}/start` like the realm's, with the same inputs. An unknown, disabled or unavailable connection (no secret, organization not `active`) answers like a missing provider: the same `404` for all of them, never saying which (the realm's own providers keep `503 provider_unavailable`). `503` is left for backd's public address being unknown, which is the same for every organization.
- **Which providers (realm and organization, one API):** `GET /v1/{realm}/_auth/providers` and `GET /v1/{realm}/_auth/orgs/{org}/providers` are public, share one handler and answer the same shape, `{"items": ["google", "microsoft"]}`: the names of the external providers a client may offer right now, nothing else (no ids, tenants or domains). Apps use it to choose which buttons to show; today they hard-code them.
  - **"Right now":** a provider is listed only if it is configured *and* usable (its secrets are set; for an organization also: SSO enabled, organization active, connection enabled), by the same check that makes `start` answer `503 provider_unavailable`, so the list and `start` can't disagree.
  - **Same code, different place:** the realm's providers come from `realm.yaml`, an organization's from its connections; both pass through the one resolver below, so the realm gains the endpoint through the same change.
  - **No enumeration:** an unknown organization, one without connections and one with SSO off all answer `{"items": []}`. Rate limits are the sign-in ones; the answer is cacheable for a minute. The list holds only external providers (password and magic links are not providers in this sense).
- **State:** `OAuthState.Org` is stored; the callback requires it to equal the organization in its path, so a code started for one organization can't be finished at another.
- **Resolution:** one function, `providerSet(r)`, returns the providers of the place the request is about (the realm or one organization), where their secrets live and the organization; `providerFor(r)` picks one from it and the list endpoint lists it. It builds a `registry.Provider` from the connection; the exchange, the ID-token verification, login codes and PKCE do not change.
- **Restriction:** after verification, Google's `hd` or Microsoft's `tid` must match, and the email must be verified (Google: `email_verified`; Microsoft: a tenant account's address, with `tid` proving the tenant). Otherwise `backd_error=not_allowed` and nothing is created. Addresses that come through a connection are stored **unverified** for both providers: Google's assertion proves the Workspace, and a Microsoft address is whatever the tenant's administrator set, so neither lets an organization's managers mark an address of this realm as verified.
- **Native apps:** `…/id-token` accepts the connection's `native_client_ids` (a field of the connection) as audiences, with the same restriction.
- **Identity:** `identities.org` (empty for realm providers). The unique index changes to `(provider, org, subject)`; documents without `org` stay valid, so there is no data rewrite, only a `backd provision`, which also drops the two old unique indexes (`(provider, subject)` and `(user_id, provider)`) once the new ones exist, because they would refuse a second identity with the same provider. The *one identity per provider* rule counts per organization.

## 6. Accounts and joining (issue 30.5)

| Situation | Result |
|---|---|
| identity `(provider, org, subject)` exists | sign in as its user |
| not linked, email matches an existing user | `account_exists`: the person signs in another way and links with `intent=link` through the connection. No automatic link, ever, in this milestone ([identity-providers.md §6](./identity-providers.md) stays as is for realm providers) |
| not linked, no such user | create the user and identity, if the connection allows it (always, for an allowed person: the restriction is the gate, **even when the realm's sign-up is `closed`**, because the realm switch in §4 is the operator's consent) |
| user not a member of the organization, not in `blocked` | join with `join_roles`, only if this sign-in created the identity (first sight) or the user links the connection |
| user in `blocked` | signed in, **not** joined |

- **Why no email link:** the organization's manager controls the connection, and a provider they control could assert anyone's address. Tenant and Workspace restrictions prove the *company* of the person, not that the email belongs to an account in this realm.
- **Removal:** a manager removing a member (or a member being removed by any path) adds them to `blocked` while a connection exists; accepting an invitation to the organization removes them from it. Leaving by oneself does not block.
- **Sessions** record the organization they were opened through (`LoginCode` and session `org`; the second-step and consent challenges carry it as `via_org`, since `org` there already names a magic link's company context). Milestone `34-scoped-sessions` makes it the scope of the session and enforces it ([scoped-sessions.md](./scoped-sessions.md)).
- **MFA:** the provider's own second factor isn't trusted; the organization's `require_mfa` applies as it does for any member.
- **Audit:** `org.sso.login` (user, provider, whether this sign-in joined them) and, for a join, the existing `org.member_add` with `by: sso`. A connection switched off because its setter lost the roles is `org.sso.disabled`.

## 7. Lifecycle and portability (issue 30.6)

- **Organization deleted or not active:** connections and their secrets are removed on deletion (in the existing resumable job); while `orphaned` they answer unavailable. Identities bound to the organization stay (they are the user's), but can't sign in once the connection is gone.
- **User erased:** their org-bound identities go with them, as every identity does.
- **Export and import:** `org_connections` is one more auth part (`--include auth`). Secrets never travel: after an import a connection answers unavailable until its secret is set again. Anonymized packages keep connections with client ids blanked.
- **Backups:** nothing new; the secrets are sealed with the instance key as before.

## 8. Clients and tools (issue 30.7)

- **JS client:** `auth.oauth.start({ org, provider, … })` and the id-token call take `org`; the typed error for `not_allowed`; `listSso`/`setSso` helpers under `orgs`.
- **Admin UI:** an organization page tab "Sign-in" to read the redirect address, set the connection and the secret, and see who joined through it.

## 9. Security considerations

- **Account takeover:** no email linking; restriction by verified tenant/Workspace; the secret only ever sealed and never returned; a state bound to its organization; reauth for changes.
- **Enumeration:** every failure to resolve a connection answers the same for organizations that don't exist, aren't enabled or have no secret.
- **Abuse by an organization manager:** the cap per organization, fixed provider hosts (no issuer URL is accepted in this milestone, so no server-side request to a chosen host), and the realm switch.
- **Roles on join:** `join_roles` can't exceed what the setting manager holds, and is re-checked at every join (a manager who lost the role doesn't leave a powerful default behind: a connection whose creator no longer holds the roles is disabled).
- **Logs:** never tokens, codes or secrets.

## 10. Issues

| Issue | Title |
|---|---|
| 30.1 | Design: organization sign-in |
| 30.2 | Organization-scoped secrets |
| 30.3 | Connections: store, API, CLI and the realm switch |
| 30.4 | Organization sign-in routes, restrictions and identities |
| 30.5 | Joining, linking and blocked members |
| 30.6 | Lifecycle, export/import and audit |
| 30.7 | JavaScript client and admin UI |
| 30.8 | Docs |
| 30.9 | Security review |

## 11. Alternatives considered

- **`/org/{id}/_auth/…` addresses:** can clash with a database called `org`; `_auth` is already reserved and unauthenticated.
- **One realm callback and the organization only in the state:** works, but the redirect address the manager registers is then identical for every organization, so it says nothing and a mix-up is easy.
- **Org secrets through field encryption:** needs the whole field-encryption milestone for a handful of values the secrets store already seals.
- **Restricting by claimed email domain:** a domain claim without proof lets an organization hand-pick who may join; the verified `tid`/`hd` is proof for free. Verified domains come with milestone 31.
- **Linking by email for verified domains:** safe only with proof of domain ownership; deferred to 31.

## 12. Follow-ups

Milestone `31-org-sso-extensions`: Apple (a team per organization), generic OpenID Connect (issuer allow-list and egress), enforced sign-in per organization (`sso_required`, designed in [scoped-sessions.md](./scoped-sessions.md) §6, replacing the membership-counting `require_sso`), verified domains (DNS proof, realm-admin approval) with automatic email linking and "which organization is this email?" discovery, and review.

## 13. As built (milestone 30)

Where the implementation differs from, or adds to, the text above:

- **Addresses stored unverified.** New users from a connection are stored with `email_verified: false` for both providers (§5): Google's word proves the Workspace, not that a managed address is theirs to claim in this realm.
- **One `404` for every unavailable connection**, not `404 or 503`; `503` stays for backd's public address being unknown (§5).
- **Secrets** of an organization are stored with the `database` field set to their scope (`org:<id>`), so the collection's unique `(database, name)` index holds one per organization; the document also has `org`.
- **Identities** replace two unique indexes (`(provider, subject)` and `(user_id, provider)`) with ones that include `org`; `backd provision` drops the old ones (§5).
- **CLI**: `backd orgs sso`, `sso-set`, `sso-delete` (single words, as `claims-set`); `updated_by` is kept on connections beside `created_by`; the connection id is `<org>:<provider>`.
- **Audit**: a join is the existing `org.member_add` with `by: sso`; `org.sso.login` and `org.sso.disabled` are new (§6, §7).
- **Challenges** carry the organization of the sign-in as `via_org` (the field `org` already names a magic link's company context).
- **Admin UI**: a *Sign-in* section of the organization page, not a tab (the page has none); it also lists the last twenty `org.sso.login` events.
- **Not in this milestone**: the Dart client's `org` parameters (the JavaScript client has them), Apple and generic OpenID Connect, `require_sso`, and verified domains (milestone 31).

## 14. Security review (issue 30.9)

Scope: takeover paths, state binding, restriction checks, enumeration, secret handling and rotation, `join_roles` escalation, blocked members, import of connections and abuse by a manager. The code was read against each, and the ones that could be tested are tests. Findings and what became of them:

| # | Finding | Result |
|---|---|---|
| R1 | **Pre-emption of a mailbox through a Microsoft tenant.** The `email` claim is whatever the tenant's administrator wrote. A malicious manager with their own tenant could sign in an account whose address is a victim's, not yet registered in the realm; the account is created with the victim's address (unverified) and the manager's tenant identity. If the victim later resets the password or verifies the address, they keep the account, and the attacker keeps a way into it. | **Fixed.** A Microsoft address is accepted only on the domain of the token's `preferred_username` (the user principal name, whose domain a tenant has to verify). A token without it, or with another domain, is `not_allowed` and creates nothing. Google needs no such rule: an address of a Workspace is on a domain the Workspace verified, and the token's `hd` is checked. Guests with an address on another domain are refused until verified domains (milestone 31). |
| R2 | **`join_roles` bypassed the granting rules.** `manage: sso` alone could hand every person of a company any role that opens no capability (such a role can still carry data access through `hasOrgRole`), which invitations reserve to `manage: members`. | **Fixed.** The roles follow `checkGrant`'s rule at set time and at every join: `manage: roles` any, `manage: members` the roles that open nothing the setter lacks, `manage: sso` alone none. |
| R3 | **Imported connections were trusted blindly.** A package could carry an id other than `<org>:<provider>` (a connection `OrgConnections` lists but no lookup finds), a shared tenant or a malformed domain. | **Fixed.** An import validates them with the same rules as the API (a blank client id is allowed: anonymized exports blank them). Defense in depth already held: `Admits` never matches a shared tenant. |
| R4 | `email_domains` compared the raw address, not the one the account is made with. | **Fixed.** Both use `NormalizeEmail`; an address with two `@` is refused. |
| R5 | **Organization secrets and the unique index** (found in 30.7 by the browser test): two organizations could not hold the same secret name. | **Fixed** in 30.7, with a MongoDB test. |
| A1 | The list of providers of an organization (`/_auth/orgs/{org}/providers`) tells anyone who can guess a slug that the organization has a working connection. | **Accepted.** It is what an app needs to show the buttons; every other failure answers the same (an empty list), ids are random, it is limited to 60 a minute per address, and operators who keep customer names secret can choose unguessable slugs. |
| A2 | A manager with a tenant can create as many accounts in the realm as they have users, in a `closed` realm too. | **Accepted.** The realm turned the feature on (that is the consent of §6); each sign-in is audited (`org.sso.login`), and deleting the connection stops it. A per-organization cap on members is not part of this milestone. |
| A3 | The blocked list keeps the newest 1000 and only blocks while a connection exists. | **Accepted.** Documented; an older removal can rejoin by linking, and an invitation is the supported way back. |
| A4 | A token that was issued for one connection's client id is accepted by another connection that names the same client id and domain (the audience is the only thing the id-token endpoint can check). | **Accepted.** Both connections are managers' own configuration; to hold such a token a person has to be an admitted member of the company already. |

Checked and found sound: state is bound to its organization (org-to-org, org-to-realm and realm-to-org finishes end at the error page); unavailable connections answer identically; the realm's `TrustsEmail` is false for connections, so a known address is `account_exists` even when Google says verified; client secrets are never returned, logged (tests scan the log and audit trail), exported or readable by functions (`function.yaml` cannot name the scope); rotation of the instance key re-seals them; no issuer URL or host is accepted, so a manager cannot make backd call an address of their choosing; list sizes and the secret length are bounded; removed members are blocked and an invitation lifts it; a connection whose setter loses the roles is switched off before any account is made.

