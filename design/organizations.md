# Design: organizations and teams

- **Issues:** features 14.1–14.13 (milestone `14-organizations`)
- **Status:** decided
- **Related designs:** [account-lifecycle.md](./account-lifecycle.md) (erasure, `on_owner_delete`), [mfa-passwordless.md](./mfa-passwordless.md) (session MFA flag, reauth), [email-delivery.md](./email-delivery.md) (invitation emails, limits), [file-storage.md](./file-storage.md) (deletion queue), [org-sso.md](./org-sso.md) (an organization's own sign-in providers)

## 1. The idea

B2B apps need "admin of **this** customer only". Realm roles are realm-wide, and member arrays on documents don't say who belongs to a customer or what they may do there. Apps end up building strange hacks.

backd adds **organizations**:

- **Built in per realm**, owned by backd like users.
- **Documents can belong to one,** recorded in `_meta.org`, next to `_meta.owner`.
- **Members hold organization roles** that the realm declares, given directly or through **teams** (groups that carry roles).
- **Rules check roles, never team names,** and stay pushed down to MongoDB, because the caller's memberships are loaded once per request.

Principles carried over:
- **Server-owned metadata:** clients never write `_meta`.
- **Explicit data policies** per collection, as with user erasure.
- **Immediate revocation:** no caches of who belongs where.
- **Least privilege:** organization managers never need realm admin.
- **Documentation is a deliverable of its own (14.11).** There are several new concepts, and they have to be clear.

**What organizations are not:** a second level of data ownership. Teams as role groups don't own documents. Data owned by a team (`_meta.team`) is [team-access.md](./team-access.md), built on top of this.

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Model | `<realm>___system.orgs`, `org_members`, `org_teams`, `org_invitations`; organization roles declared in `realm.yaml`, separate from realm roles |
| Documents | `_meta.org`, server-owned, set at creation (`?org=`), indexed; `organization: required \| optional \| none` per collection |
| Moving documents | never through PATCH or PUT; an explicit transfer by **realm admins only** |
| Roles | several per member; capabilities `manage: [org, members, roles, teams]` |
| Rules | `inOrg`, `hasOrgRole`, `orgClaims`, `user.orgs`; pushed down as `_meta.org in [...]` |
| Creating | `organizations.create` rule (template: `user.email_verified`), `creator_roles` (`[owner]`), `max_created_per_user` (5); realm admins and functions can always create |
| Joining | invitations bound to a `user`, an `email` or `none` (open link); link and id always returned; email optional; accepted only with a session |
| Teams | groups of members carrying organization roles; effective roles are direct roles plus team roles; rules see roles only |
| Deleting | `on_org_delete: { action: delete \| keep }` per collection; access ends at once; resumable job; report first |
| Member erasure | organization documents stay with the organization, with the owner cleared; `on_owner_delete.in_org: apply` opts in; the last manager can't leave; organizations without managers become `orphaned` |
| Loading memberships | lazily for each request that needs them, no cache, at most 100 per user |
| MFA | an organization can set `require_mfa`; sessions without MFA don't count as members there |
| Organization data | minimal record plus **claims** (1 KiB, realm admins and functions only) readable by rules; profiles live in app collections |
| Structure | flat organizations; teams are role groups, not a second level |
| Reuse | organization roles reuse the realm roles machinery (declaration, capabilities, granting rules, assignment, audit, invitations); one resolver for effective roles |
| Inheritance | a user's organization roles come from direct grants, from teams and from the organization's `default_roles`; organizations and teams never carry **realm** roles |
| Callers | API keys have no memberships of their own (a key acting on behalf of a user uses that user's); realm admins use `/_admin`; their data-API session is an ordinary user |
| Slugs and caps | a slug is released when the organization is deleted; deleted organizations still count against `max_created_per_user` |

### 2.1 What already exists and is reused

Realm roles already exist: declared in `realm.yaml`, held by users, checked in rules, with granting rules and audit. Organizations and teams are **new holders of the same idea, scoped to one organization**, so they are built on that code and not next to it. Wherever a row below says "same", there is **one implementation**, with the scope (realm or organization) as an input.

| Already built (realm roles) | In organizations and teams |
|---|---|
| Role names, `description`, validation (`realm.yaml` `roles:`) | same, under `organizations.roles` and `organizations.team_roles` |
| Capabilities of a role (`admin: true \| read \| [areas]`) | `manage: [org, members, roles, teams]`; same model: a role opens a set of capabilities |
| "A role is granted only by someone who holds everything it opens; someone holding rights the caller lacks can't be changed" | the granting rules (§5.1): same function, called with the caller's capabilities in that organization |
| Assignment through admin API and CLI, stored in the database, audited | memberships and role grants: same store pattern, audited |
| Seed assignments (`users:` in `realm.yaml`) | not carried over: organizations are data, not configuration |
| Startup warning about roles `realm.yaml` no longer declares | stale organization roles: same warning (§3.1) |
| `hasRole(user, 'x')`, effective from the next request | `hasOrgRole(org, 'x')`: same semantics, no cache |
| Invitations with roles (token hash, expiry, email, limits) | organization invitations (§7): same service with an organization scope |
| Re-authentication, audit, email limits, MFA flag, erasure policies | reused as they are |

**Where a user's roles come from** (resolved once per request by one resolver, which also records the origin of each role):

1. **Realm roles:** held by the user, realm-wide. Unchanged. `user.roles`.
2. **Direct organization roles:** granted to the member in one organization.
3. **Team roles carried by teams:** the member inherits the organization roles of each team they belong to (§8).
4. **Default roles:** `organizations.default_roles` are held by **every member** of every organization, so `member: {}` doesn't have to be granted one by one.
5. **Team roles** (team access) are held per team, see [team-access.md](./team-access.md).

**Inheritance never crosses scopes:** an organization or a team can't carry a **realm** role (a team could otherwise make someone a realm administrator), and realm roles grant nothing inside an organization. An organization role has no meaning outside its organization. Realm admins act through the admin API, not through organizations.


### 3.1 `realm.yaml`

The template writes this section **commented**, with a link to the organizations guide.

```yaml
organizations:
  enabled: true
  roles:                              # organization roles, separate from realm roles
    owner:   { manage: [org, members, roles, teams] }
    admin:   { manage: [members, teams] }
    editor:  {}
    member:  {}
  create: user.email_verified         # expr rule: who may create organizations ("false": realm admins and functions only)
  default_roles: [member]             # held by every member, no grant needed
  creator_roles: [owner]
  max_created_per_user: 5
  max_memberships_per_user: 100
  max_teams_per_org: 100
  max_teams_per_member: 20
  max_pending_invitations: 500        # per organization
  allow_require_mfa: true
  members_visible_to: members         # members | managers (holders of any role with a `manage` capability): who sees other members' emails
  invitation_link: "https://app.acme.example/join?token={token}&org={org}"   # where invitation links point
```

**Startup checks:**
- role names are unique and capabilities known;
- at least one role has `manage: org`;
- `creator_roles` and `default_roles` exist, and `creator_roles` include a role with `manage: org`;
- default roles carry no `manage` capability (everyone would hold it);
- the `create` rule compiles;
- `invitation_link` is within `allowed_redirects` and contains `{token}` (`{org}`, the organization id, is also available; one realm-wide link, so apps with a domain per company route from the id in the link or build their own URL from the `token` in the answer, §7.1);
- every collection using organizations declares `on_org_delete`;
- the realm has authentication enabled;
- a collection with `organization` set can't use `custom_id: user` (one document per user across every organization makes no sense); `custom_id: { pattern }` ids stay unique across the whole collection, and the docs say that `409 id_taken` doesn't depend on the organization.

**Roles that disappear:** a role removed or renamed in `realm.yaml` stays in stored memberships. It grants nothing, is listed at startup and by the admin API (`GET /_admin/orgs/stale-roles`, `backd orgs list --stale-roles`), and nothing is rewritten.

### 3.2 `collection.yaml`

```yaml
organization: required        # required | optional | none (default none)
on_org_delete:
  action: delete              # delete | keep
on_owner_delete:
  action: delete
  in_org: keep                # keep (default): organization documents stay, owner cleared | apply: same action as outside organizations
```

## 4. Data model

| Store | Fields |
|---|---|
| `orgs` | `id`, `name`, `slug` (optional, unique per realm), `created_by`, `created_at`, `require_mfa`, `status` (`active`, `orphaned`, `deleted`), `claims` (≤ 1 KiB) |
| `org_members` | `org`, `user`, `roles` (direct, as grants `{ role, from?, until?, granted_by, granted_at }`), `teams` (ids), `joined_at`, `invited_by`; unique on (`org`, `user`); index on `user` |
| `org_teams` | `id`, `org`, `name` (unique per organization), `roles`, `created_at` |
| `org_invitations` | `id`, `org`, `token_hash`, `binding` (`user`, `email`, `none`), `user` or `email`, `roles`, `teams`, `max_uses`, `uses`, `expires_at`, `created_by`, `revoked_at` |
| documents | `_meta.org`, indexed by backd in every collection with `organization` set |

- **Effective roles** of a member: `roles ∪ roles of every team in teams`, leaving out grants outside their window (`from`, `until`, used by team access, [team-access.md](./team-access.md) §5). The API accepts plain role names, which create grants without a window.
- **Shared with team access:** one membership loader decides which grants *count* (organization status, `require_mfa` of the organization, of the role, window); one `canGrant` function applies the granting rules (§5.1, and `grants:` of team roles); membership events (`member.joined`, `member.left`, `roles.changed`) are emitted here and consumed by audit and, later, by hooks. `_meta.org` and `_meta.team` are system fields, usable in `indexes.json` (`[_meta.org, slug]` is how a field is unique per organization), and every organization collection gets `(_meta.org, _meta.created_at, id)`.
- **Deleted organizations** keep their record as `status: deleted`, with name and dates, so `keep` documents still point at something readable for realm admins and a realm admin can reactivate the organization later (§5.2).
- **Slugs:** unique among organizations that aren't deleted. Deleting releases the slug (the record keeps it as `former_slug`); reactivating needs it to be free, otherwise the admin picks another.
- **Caps:** `max_created_per_user` counts every organization the user created, deleted ones included, so creating and deleting can't be used to get around it. Realm admins and functions aren't limited.

## 5. Organizations and memberships (issue 14.1)

All of these are under `/v1/{realm}/_orgs` and require a session. **A non-member and an unknown organization get the same `403 not_org_member`** on every endpoint under `/_orgs/{org}`, so the answer never says whether an organization exists. `403 org_requires_mfa` is only ever returned to members.

| Endpoint | Who | Behavior |
|---|---|---|
| `POST /_orgs` with name and optional slug | anyone passing `organizations.create`, under the cap | Creates the organization with the caller as a member holding `creator_roles`. |
| `GET /_orgs` | any user | Lists the caller's organizations, with effective roles, teams, `require_mfa` and whether the session meets it. |
| `GET /_orgs/{org}` | members | Name, slug, status and claims (read-only; writes stay with realm admins and functions). |
| `PATCH /_orgs/{org}` | `manage: org` | Name and slug. `require_mfa` is covered in §11. |
| `GET /_orgs/{org}/members` | members | User id, direct roles, teams and effective roles with where each came from. Emails are shown according to `members_visible_to`, and MFA status to managers only. |
| `PATCH /_orgs/{org}/members/{user}` | `manage: members`, or `manage: roles` | Changes direct roles (§5.1). |
| `DELETE /_orgs/{org}/members/{user}` | `manage: members` | Removes a member (§5.1). |
| `POST /_orgs/{org}/leave` | members | Leaves the organization, except the last manager (§9). Allowed even without MFA when the organization requires it. |

### 5.1 Granting rules

- **`manage: members`:** can invite, remove members and revoke invitations. It can only grant roles **whose capabilities the caller holds** (the rule realm roles follow for admin areas: a role that opens nothing can be given by anyone with `manage: members`, and `owner` only by someone who can do everything it can), and can't remove or change a member who holds a capability the caller lacks, so an admin can't remove an owner.
- **`manage: roles`:** can grant or remove any role, including roles with capabilities.
- **`manage: teams`:** see §8.
- **`manage: org`:** covers rename, slug, `require_mfa` and deletion.
- **`manage: sso`:** added by [org-sso.md](./org-sso.md): sets, replaces and removes the organization's sign-in connections and their client secrets, with reauth. Not part of any default role set; the example gives it to `owner` and `admin`. The roles a connection gives on joining follow the granting rules above.

### 5.2 Realm admins

- **Admin API:** needs the `users` area (organizations are people grouped, so no area of their own); `GET`, `POST` and `DELETE /_admin/orgs[/{org}]`; members and teams under `/_admin/orgs/{org}/...`; `PUT /_admin/orgs/{org}/claims`; `POST /_admin/orgs/{org}/owner` to assign a manager to an orphaned organization.
- **CLI:** `backd orgs list|show|create|members|add-member|claims-set|assign-owner|delete|owned|reactivate` (every value is a flag, so `claims-set` and `add-member` are single words).
- **Reactivating:** `POST /_admin/orgs/{org}/reactivate` (`backd orgs reactivate`) sets a `deleted` organization back to `active` **without members**, since memberships were removed; the realm admin then assigns an owner (`POST /_admin/orgs/{org}/owner`). Documents under `keep` become visible again to members; documents under `delete` are gone. Audited.
- **Bypassing invitations:** realm admins and `ctx.admin` can create organizations for any user and **add members directly**. This is administrative power and is audited.

## 6. Documents in organizations and rules (issue 14.2)

### 6.1 Creating and querying

- **Creating:** `POST .../{collection}?org=<id>`, or `.inOrg(id)` in the JS client, sets `_meta.org`.
  - **Membership first:** backd checks the caller is a counting member (§11) before evaluating `create`. A non-member or unknown organization answers `403 not_org_member`, the same either way.
  - **Missing organization:** `required` without `?org=` answers `400 org_required`.
  - **Collections with `none`:** `?org=` answers `400 org_not_allowed`.
- **Updating:** `_meta.org` can't be changed by PATCH or PUT (`400 immutable_field`). Use a transfer (§10).
- **Who can't:** an API key without a user has no memberships, so `?org=` with it answers `403 not_org_member` (a key acting on behalf of a user uses that user's memberships, without MFA); they reach organization data only through `ctx.admin.db` in functions or the admin API. A realm admin's session in the data API is an ordinary user: realm admin power lives in `/_admin`.
- **Batches:** a batch create takes `?org=` (and `?team=`) from the request for all its operations; operations don't carry their own.
- **Files and exports:** files follow the rules of the document that holds them; exports and counts follow the read rule like lists.
- **Querying:** `_meta.org` is an allowed query field. `GET .../{collection}?org=<id>` is shorthand for that filter, ANDed with `where`; the read rule still decides visibility, so an organization the caller can't see just returns an empty list. This is how apps switch between organizations.

### 6.2 Rule helpers

| Helper | Meaning | Pushed down as |
|---|---|---|
| `inOrg(doc._meta.org)` | the caller is a counting member of the document's organization | `_meta.org in [counting organizations]` |
| `hasOrgRole(doc._meta.org, "editor")` | the caller's effective roles there include `editor` | `_meta.org in [organizations where effective roles include editor]` |
| `hasOrgRole(doc._meta.org, doc.audience_role)` | role named by the document | `$or` over the caller's (organization, role) pairs, each `{_meta.org: o, audience_role: r}` |
| `orgClaims(doc._meta.org).plan == "pro"` | a claim of the document's organization, for counting memberships only; always inside a comparison (`==`, `!=`, `<`…, `in`) with a value that doesn't depend on the document, and false for organizations the caller doesn't belong to | `_meta.org in [counting organizations whose claim matches]` |
| `user.orgs` | map from organization to `{ roles, teams, claims }` | in-memory only; meant for `create` and `invoke` rules, not lists |

- **What "counting" means:** the organization is active (or orphaned), and its `require_mfa` is met by the session.
- **Team names:** rules never get them, only effective roles. `user.orgs[o].teams` exists for apps, and the docs say not to use it in rules.
- **On create** the document's organization is `data._meta.org`, the one in `?org=`.
- **Tests:** the existing property tests (pushdown against in-memory evaluation) gain callers with memberships, teams, claims and MFA-required organizations.

### 6.3 Loading memberships

- **Lazy:** one aggregation per request, made only when an organization helper or `user.orgs` is evaluated. It reads `org_members` by user, with the user's teams and their roles and the organizations' `status`, `require_mfa` and `claims`.
- **No cache:** changes to roles, teams and members apply on every instance at the next request.
- **Bounded:** at most `max_memberships_per_user` (100) memberships and `max_teams_per_member` (20) teams per member, so the query and the pushed-down lists stay small.
- **Functions:** `ctx.user.orgs` holds the same data, loaded once per call.

## 7. Invitations (issue 14.3)

### 7.1 Creating

`POST /_orgs/{org}/invitations` (needs `manage: members`):

```json
{ "binding": "email", "email": "ana@example.com", "roles": ["editor"], "teams": [], "send": true, "expires_in": "7d" }
```

| Binding | Fields | Who can accept |
|---|---|---|
| `user` | `user` (a user id) | only that user |
| `email` | `email` | the signed-in user whose **verified** email matches |
| `none` | `max_uses` (default 1), `expires_in` (at most 30 days) | anyone signed in who holds the link |

- **The answer always includes the link and id:** `{ id, token, link, expires_at }`. `link` is `invitation_link` with the token. The app can show it as a QR code, send it through another channel, or offer copy and paste.
- **Email is optional:** `send: true` sends the `org-invitation` email, and only for `email` bindings. Sends use the email limits.
- **What can be granted:** `roles` and `teams` follow the granting rules (§5.1); a team counts as the **union of the roles it carries**, which the caller must hold. Open links (`none`) can't grant roles with `manage` capabilities, directly or through a team.
- **Storage:** the token is stored hash-only. Invitations expire after 7 days by default. When an email carries the link the worker needs the token to write it, so that one is also kept **sealed with the instance's secrets key** until the invitation is used up, revoked or expired (sending needs `BACKD_SECRETS_KEY`).

### 7.2 Accepting

- **Preview:** `POST /_orgs/invitations/preview` with the token, with a session, returns the organization name and the roles and teams on offer, so the app can ask "Join Acme as editor?". Previews need a session, so anonymous callers can't probe tokens.
- **Accepting:** `POST /_orgs/invitations/accept` with the token, with a session.
  - **Checks:** binding, expiry, uses, the caller's membership cap and the organization's `require_mfa` (the member is added, but doesn't count until the session has MFA).
  - **Effect:** creates or extends the membership. An existing member's roles and teams are added to.
- **Errors:** unknown, expired, revoked and used-up tokens answer the same `400 invalid_invitation`. A binding mismatch answers `403 invitation_not_for_you`, only after the token has proved valid.
- **No hosted acceptance:** hosted pages never accept, because acceptance needs a session.

### 7.3 Managing

- **Listing:** `GET /_orgs/{org}/invitations` shows status, binding, uses and expiry, never tokens.
- **Revoking:** `DELETE .../{id}`.
- **New link:** `POST .../{id}/link` issues a new token and invalidates the old one, since a lost link can't be shown again.
- **Resending:** `POST .../{id}/send` resends the email for `email` bindings.

## 8. Teams (issue 14.4)

Teams are **groups of members that carry organization roles**. They don't own data, and rules never see their names. (Team-scoped data, [team-access.md](./team-access.md), is the exception: documents carry `_meta.team` and rules check `inTeam` and `hasTeamRole`, by id; role-group teams here still only grant organization roles.)

| Endpoint | Who |
|---|---|
| `POST /_orgs/{org}/teams` with name and roles | `manage: teams` |
| `GET /_orgs/{org}/teams` | members |
| `PATCH /_orgs/{org}/teams/{team}` (name, roles) | `manage: teams`; roles follow the granting rules (§5.1) |
| `DELETE /_orgs/{org}/teams/{team}` | `manage: teams` |
| `PUT` and `DELETE /_orgs/{org}/teams/{team}/members/{user}` | `manage: teams`; the user must be a member |

- **Effective roles** are direct roles plus the roles of the member's teams, loaded in the same request (§6.3) and effective at the next request.
- **Leaving the organization** removes the member from its teams. Deleting a team removes it from every membership.
- **Caps:** `max_teams_per_org` (100) and `max_teams_per_member` (20).
- **Showing where roles come from:** the members list returns each effective role with its origin (`direct` or the team names), so UIs and audits can explain why someone has access.

### 8.1 Direct roles versus teams

Invitations and managers can give **direct roles or teams**. The docs explain, with examples, why **mixing both for the same purpose is a bad idea**:

- **Access survives removal:** a member with `editor` directly **and** through the "Editors" team keeps `editor` after being removed from the team. Removal looks done but isn't.
- **Audits get harder:** answering "who can edit?" means checking two places, and team changes no longer describe access completely.
- **Recommended pattern:** use **direct roles for management** (`owner`, `admin`) and **teams for access roles** (`editor`, `viewer`) in organizations that use teams; small organizations can use direct roles only.
- **Visible overlap:** the members list marks a role given both directly and through a team, and the admin UI can warn about it.

## 9. Leaving, removal and member erasure (issues 14.1 and 14.13)

- **Leaving or removal:** documents in the organization stay, and the person loses access at once (no cache). While the organization has a sign-in connection, a removed member is also kept from rejoining through it ([org-sso.md §6](./org-sso.md#6-accounts-and-joining-issue-305)); accepting an invitation lifts that.
- **Erasure of a user:**
  - the user's memberships and team memberships are deleted;
  - invitations bound to them are deleted, while invitations they created stay, with `created_by` cleared;
  - documents with `_meta.org` set are **kept** with `_meta.owner` cleared, unless the collection sets `on_owner_delete.in_org: apply`;
  - `pull` fields still remove the user's id everywhere.
- **The last manager** (the last member with `manage: org`) can't leave or be removed (`409 last_org_manager`). If that user is erased, the organization becomes `orphaned`:
  - its remaining members keep access;
  - it's listed by the admin API, `backd orgs list --orphaned` and the startup report;
  - realm admins assign a new manager.
- **Report before erasing a user:** the erasure report (`/_admin/users/{id}/owned`) adds the organizations the user manages and documents that stay with organizations.

## 10. Transfers (issue 14.5)

- **Realm admins only:**
  - `POST /_admin/data/{db}/{collection}/{id}/_transfer` with `{ org, team? }` (`org: null` when the collection is `optional`); changing the organization clears `_meta.team` unless a team of the target is given, and collections that require a team need one;
  - `backd data transfer --realm --db --collection --id --org`;
  - `ctx.admin.db` in functions (declaring admin access is already a realm admin decision in config).
- **Request and answer:** `{ "org": "<id>" | null, "team": "<id>" | null }`; the answer is the document (`200`) with its new `_meta`. Fields left out keep their value, except that changing the organization clears `_meta.team` unless `team` is given.
- **Errors:** `404` for an unknown document, target organization or target team; `409 org_not_active` for an organization that isn't `active`; `400 org_not_allowed` for a collection with `organization: none`; `400 org_required` for `null` on `required`; `400 team_required` when the collection requires a team and none results; `400 team_not_allowed`; `412` for a failed `If-Match`.
- **Effects:** the target must exist and be active, and the collection must allow it. The transfer increments `_meta.version`, accepts `If-Match` and is audited with both organizations. Files and image versions travel with the document unchanged.
- **Bulk transfers** run through `ctx.admin.db` in a job.
- **Adopting organizations on an existing collection:** documents that exist before `organization:` is set have no `_meta.org`, and with `required` they become invisible to members (no helper matches). Realm admins assign them with `POST /_admin/data/{db}/{collection}/_assign_org` (`backd data assign-org --org --where`), a resumable job that sets `_meta.org` on documents **without one** matching `where`, and never moves documents that already have one. The body is `{ "org": "<id>", "team": "<id>"?, "where": {…}? }` (`where` goes through the usual allowlist; `team` is needed when the collection requires one); `?dry_run=true` answers `{ matched }` and changes nothing; otherwise `202` with `{ job, matched }`, and the job's result counts `assigned` and `skipped`. Audited with counts. Startup reports collections with `organization` set that hold documents without `_meta.org`.

## 11. MFA required by an organization (issue 14.7)

- **Setting:** `PATCH /_orgs/{org}` with `require_mfa: true`, by `manage: org` **with reauth**, allowed when `organizations.allow_require_mfa` is true. It's audited.
- **Effect:** for sessions without MFA, the membership **doesn't count**.
  - helpers return false and the pushdown leaves the organization out;
  - the organization's endpoints answer `403 org_requires_mfa`, except leave;
  - `GET /_orgs` marks the organization so the client can prompt enrollment.
- **Other organizations** are unaffected.
- **Showing who's missing:** managers see which members have no MFA. API keys acting on behalf of users have no MFA, so they can't reach these organizations, and the docs say so.

## 12. Deleting organizations (issue 14.6)

- **Who:** `DELETE /_orgs/{org}` by `manage: org` with reauth, or a realm admin (`DELETE /_admin/orgs/{org}`, `backd orgs delete`).
- **At once:** the status becomes `deleted`, and memberships, teams and invitations are removed, so access ends immediately.
- **Then a resumable job** (`origin: backd:org.delete`) goes through every collection with `organization` set:
  - `delete` removes the documents, **soft-deleted ones included**, and queues their files for deletion;
  - `keep` leaves them, readable only by realm admins (no member remains).
- **Report first:** `GET /_admin/orgs/{org}/owned` and `backd orgs owned` give counts per database and collection, and what each policy will do.
- **Connections:** the organization's sign-in connections and their secrets are removed in the same job ([org-sso.md §7](./org-sso.md#7-lifecycle-and-portability-issue-306)); an `orphaned` organization's connections answer unavailable.
- **Audit:** `org.deleted` with counts, never content.

## 13. Organization claims

- **Writing:** realm admins (`PUT /_admin/orgs/{org}/claims`, `backd orgs claims set`) and functions (`ctx.admin.orgs.setClaims`) only. Members can read them, never write them.
- **Size:** at most 1 KiB, any JSON object. Typical uses are `plan`, `seats` and `features`.
- **Rules** read them with `orgClaims(doc._meta.org).<key>`, pushed down (§6.2). For example, a `create` rule limiting items on the free plan uses `orgClaims(...)` with a count kept by a function.
- **Profiles** (logo, address, settings) belong in an app collection with `organization: required`, as user profiles do.

## 14. Functions (issue 14.8)

- **`ctx.user.orgs`:** the caller's memberships (effective roles, teams, claims, whether each counts).
- **Creating in an organization:** `ctx.db.collection(c).create(doc, { org })` applies the same checks and rules as HTTP.
- **`ctx.admin.orgs`:** `create`, `get`, `list`, `setClaims`, `addMember` (direct, audited), `removeMember`, `teams.*`, `invitations.create` and `delete`, for flows such as "payment received, create the organization and set `plan`".
- **`@backd/functions-testing`** fakes memberships, teams and claims, so rules and functions can be unit tested.

## 15. JavaScript client (issue 14.9)

- **Types (milestone 23):** generated types name the realm's organization roles (`OrgRole`), and `?org=` is typed on collections that use organizations. The per-realm OpenAPI document adds `/_orgs` and the `org` parameter (issue 14.2 produces the OpenAPI part, 14.9 the types).
- **Organizations:** `client.orgs.create()`, `list()`, `get()`, `update()`, `leave()` and `delete()` (which runs the reauth helper).
- **Members and teams:** `client.orgs.members(org).list()`, `update()` and `remove()`; `client.orgs.teams(org)` for create, update, delete and members.
- **Invitations:** `client.orgs.invitations(org).create()` returns the link and id; `list()`, `revoke()` and `newLink()`; `client.orgs.invitations.preview(token)` and `accept(token)`.
- **Documents:** `.inOrg(id)` on create and on queries.
- **Errors:** `org_requires_mfa` surfaces as a typed error the app can use to start MFA setup. All new codes are typed `BackdError`s.

## 16. Shelf tutorial (issue 14.10)

In Shelf, a team shares a set of items. With this feature **each Shelf team is an organization**:

- **Teams and items:** creating a team creates an organization (creator is `owner`). Items use `organization: required` and `on_org_delete: delete`. Members read items with `inOrg`, and only `hasOrgRole(doc._meta.org, "admin")` can delete other people's items.
- **Replacing the old model:** the tutorial's previous way of sharing is replaced, and the chapter explains why: access ends the moment someone leaves, and there's no array to keep in sync.
- **Joining:** members join through invitations, shown as a copyable link and as an email.
- **Inside a team:** an "Editors" team inside an organization grants `editor`, showing the recommended split between direct management roles and teams for access.
- **Switching** between a user's teams with `?org=`.
- **Advanced steps:** a team requiring MFA; a `plan` claim set by a function that limits the number of items; leaving with the last-manager rule; deleting a team with its report.

## 17. Security considerations

- **Rules first:** every helper is pushed down. A non-member never sees, counts or guesses another organization's documents, and `not_org_member` is the same for unknown organizations.
- **Immediate revocation:** no membership cache anywhere.
- **Server-owned ownership:** `_meta.org` is set only at creation after a membership check, and moved only by realm admins.
- **Privilege escalation:** grant-what-you-hold, `manage: roles` for anything else, no changes to more powerful members, and no management roles on open links.
- **Invitations:** tokens hash-only, binding checked after validity, uniform errors, previews need a session, open links single use by default with an expiry cap.
- **Creation abuse:** a creation rule plus caps on organizations, memberships, teams and pending invitations.
- **MFA per organization** is enforced on access, not at sign-in.
- **Claims:** trusted because only realm admins and functions can write them.
- **Audit:** organization creation, deletion, rename, `require_mfa`, member added, removed and role changes, team changes, invitations created, accepted and revoked, transfers, claims changes; ids only.

## 18. Documentation (issue 14.11)

The other issues update reference docs. This issue makes the feature **understandable**:

- **Concepts page:** realm roles versus organization roles; what `_meta.org` is and who sets it; effective roles (direct plus teams); claims; the three invitation bindings and when to use each; deleting an organization versus erasing a member.
- **Diagrams:** the data model, the "who can see this document" flow and the invitation flow.
- **"Build a B2B app" recipe**, step by step: configure roles, mark collections, write rules, create the organization at sign-up, invite, switch organizations.
- **Rules cookbook:** members read, admins delete, role named by the document, plan limits with claims, organization MFA.
- **Direct roles versus teams (§8.1),** with the removal example and the recommended pattern.
- **"What happens when…" table:** member leaves, member erased, last manager erased, organization deleted, document transferred, `require_mfa` turned on, role removed from a team.
- **Limits and errors** with what to do about each.
- **Check:** someone who hasn't seen the design builds the recipe from the docs alone, and every gap found is fixed.

## 19. Issues

| id | Issue | Depends on |
|---|---|---|
| 14.1 | Organizations and memberships (starts by extracting the shared roles code, §2.1) | — |
| 14.2 | Documents in organizations, rule helpers and pushdown | 14.1 |
| 14.3 | Organization invitations | 14.1, 4.2 |
| 14.4 | Teams as role groups | 14.2, 14.3 |
| 14.5 | Transfers by realm admins | 14.2 |
| 14.6 | Deleting organizations | 14.2, 13.4 |
| 14.13 | Member erasure and orphaned organizations | 14.2, 4.6 |
| 14.14 | Organizations in the admin UI | 14.5, 14.6, 14.13 |
| 14.7 | MFA required by an organization | 14.2, 13.1, 13.4 |
| 14.8 | Organizations in functions | 14.4 |
| 14.9 | Organizations in the JavaScript client | 14.4, 14.7, 4.7 |
| 14.10 | Shelf teams as organizations | 14.6, 14.13, 14.8, 14.9 |
| 14.11 | Organizations documentation | 14.10 |
| 14.12 | Security review of organizations | 14.5, 14.11 |

## 20. Alternatives considered

- **Organizations as app collections with a lookup helper:** rejected because they break pushdown and every app rebuilds invitations. **One database per organization:** rejected because databases come from config folders.
- **An organization field declared per collection:** replaced by `_meta.org`, server-owned and the same everywhere.
- **Organization changeable through `update`, or never movable:** replaced by a realm-admin transfer.
- **One role per member in a fixed order, or free-form roles:** rejected for named roles with `manage` capabilities.
- **Only realm admins create organizations, or anyone without limits:** replaced by a creation rule and caps.
- **Direct adds without consent:** for realm admins only. **Email-only invitations:** replaced by link and id plus optional email.
- **A restore window for deleted organizations, or never deleting documents:** rejected; explicit policies instead, and realm admins can reactivate an organization **without its members** (§5.2).
- **Applying `on_owner_delete` inside organizations by default:** rejected because it would delete team data.
- **Teams as role groups:** accepted here. **Teams as a second ownership level:** accepted as a separate layer in [team-access.md](./team-access.md). **Nesting teams:** rejected for these milestones (backlog).
- **Memberships in the session, or cached:** rejected because removal must be immediate.
- **Organization MFA at sign-in:** rejected because one strict organization would force MFA everywhere.
- **Free-form organization data editable by managers:** rejected because it can't be trusted for plans.

## 21. Backlog (outside these milestones)

- Nothing in this list is part of milestone 14. The admin UI has its own issue (14.14).
- Joining by verified email domain, with its own safeguards.
- Push and email to all members of an organization or team (`audience` filters by membership).
- Personal data export (backlog) gains organization data for managers.
