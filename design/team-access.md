# Design: team-scoped access

- **Issues:** features 24.1–24.12 (milestone `24-team-access`)
- **Status:** decided
- **Extends:** [organizations.md](./organizations.md) (§2.1: the roles machinery is shared, and nothing here duplicates it), whose backlog item "data owned by a team" this is; related to [mfa-passwordless.md](./mfa-passwordless.md), [query-language.md](./query-language.md), [realtime.md](./realtime.md), [internal-functions.md](./internal-functions.md)

## 1. The idea

Organizations give companies their own data. Teams were designed as **role groups** inside an organization: a team carries organization roles to its members, and rules only check roles. That's not enough for apps with departments, where "Marta manages Sales" must let her see and approve **Sales'** data only.

This design adds, on top of organizations:

- **Team-scoped data:** documents can belong to a team (`_meta.team`), and rules can check membership of that team and **roles held within that team**.
- **Team roles with time windows and scoped grant rights:** "Marta may make someone a delegate in Sales from 1 to 15 August", and nothing more.
- **Membership hooks:** apps react when people join, leave or change roles.
- **`_meta.team` everywhere `_meta.org` works:** queries, aggregations and realtime.
- **MFA by organization role and team role.**
- **Company context in backd's own emails and pages:** system emails and hosted pages know which company they belong to, and an internal function supplies their template data (G8).
- **Per-organization template overrides:** company owners edit their own email templates, safely, with preview and test send (G9).

Everything reuses the organization machinery: memberships loaded once per request, no cache, pushdown, and immediate revocation.

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Team roles | declared in `realm.yaml` under `organizations.team_roles`; held per member per team; separate from organization roles carried by teams |
| Holders | any member of the organization can hold a team role in a team, even without being a team member (cover from another team) |
| Documents | `_meta.team`, server-owned, set at creation, membership checked; `team: required \| optional \| none` per collection |
| Rules | `inTeam(doc._meta.team)` (team member), `hasTeamRole(doc._meta.team, "manager")` (role in that team); pushed down |
| Time windows | team role grants can have `from` and `until`; outside the window they don't count |
| Scoped grants | team roles declare which team roles their holders may grant, in the same team only |
| Listing | team members and holders of a team role, honoring windows, in functions, the admin API and the client |
| Hooks | `organizations.on_member_joined`, `on_member_left`, `on_roles_changed` internal functions |
| Queries | `_meta.team` in `where`, `group_by` and realtime filters |
| MFA | `require_mfa: true` on organization roles and team roles; without MFA the role doesn't count |
| Company context | `org` on sign-in, magic link and reset (checked silently) and on invitations; stored with tokens |
| Template data | `email.data_hook` and `pages.data_hook`: internal functions returning `App` for system emails and pages; bounded; failures fall back, never block |
| Owner templates | per organization and locale, for kinds the realm declares customizable; logic-less syntax; sanitized; required placeholders; preview and test send |

## 3. Team roles (issue 24.1)

```yaml
# realm.yaml
organizations:
  roles:
    owner: { manage: [org, members, roles, teams] }
    hr:    { manage: [members, teams], require_mfa: true }
    member: {}
  team_roles:
    manager:  { grants: [delegate], require_mfa: true }
    delegate: { require_mfa: true }
```

- **What they are:** team roles are distinct from organization roles. A person can be a plain member of Sales, `manager` of Sales, and `delegate` of Support at the same time.
- **Storage:** they're stored on the organization membership: `team_roles: [{ team, role, from?, until?, granted_by, granted_at }]`, the same grant shape as direct organization roles plus `team`, resolved by the same resolver ([organizations.md](./organizations.md) §2.1). Team roles are scoped to their team and never grant organization or realm roles.
- **Declared roles:** `team_roles` use the same declaration, name rules and `require_mfa` as organization roles, and the same stale-role handling: a role removed from `realm.yaml` grants nothing and is reported.
- **Holding a role without membership:** a team role can be held **without** being a member of the team. A delegate from another team approves Sales requests without seeing Sales' team calendar.
- **Who grants:** the granting function of organizations (§5.1 there) with a scope of one team. Members with organization `manage: teams` can grant any team role in any team of the organization. Holders of a team role can grant only the roles listed in its `grants`, in teams where they hold it (§5).
- **Listing:**
  - `GET /v1/{realm}/_orgs/{org}/teams/{team}/members?role=manager` lists members and role holders, honoring windows, to members of the organization, with emails shown per `members_visible_to`;
  - functions use `ctx.admin.orgs.teams.members(team, { role })`;
  - the JS client uses `client.orgs.teams(org).members(team, { role })`.
- **Loading:** team roles are loaded with memberships in the same per-request aggregation, with no cache, as in organizations §6.3.
- **Caps:** at most 50 team role grants per member.

## 4. Team-scoped documents and rules (issue 24.2)

```yaml
# collection.yaml
organization: required
team: required            # required | optional | none (default none); requires organization
on_team_delete:
  action: keep            # keep (documents stay, team id kept, readable by org-level rules) | delete
```

- **`_meta.team`:** server-owned, set at creation with `?team=<id>` (or `.inTeam(id)` in the JS client), in the same organization as `?org=`.
- **The creation check:** the caller must be a member of that team or hold a team role in it, unless the request comes from a function with admin access. A non-member answers `403 not_team_member`, the same as for an unknown team or a team of another organization. `?team=` needs `?org=`.
- **Immutable** through PATCH and PUT. Moving documents between teams uses the transfer of organizations §10, which takes `{ org, team? }` from the start (realm admins and `ctx.admin.db`); the same endpoint also adopts documents that have no team yet, so app functions can move a person's future requests.
- **Rule helpers:**

  | Helper | Meaning | Pushed down as |
  |---|---|---|
  | `inTeam(doc._meta.team)` | the caller is a member of the document's team | `_meta.team in [teams the caller belongs to]` |
  | `hasTeamRole(doc._meta.team, "manager")` | the caller holds that role in the document's team, now | `_meta.team in [teams where the caller holds manager, within its window]` |

- **Combining with organization helpers** (`hasOrgRole`, `orgClaims`) by `&&` and `||`, all pushed down.
- **Deleting a team:**
  - its team role grants are removed with it;
  - `on_team_delete` decides what happens to its documents;
  - `keep` leaves them with the old team id, readable through organization-level rules (HR);
  - startup requires `on_team_delete` on collections with `team` set.
- **Property tests:** the pushdown property tests gain team members, team role holders with and without windows, and holders outside the team.

## 5. Time windows and scoped grants (issue 24.3)

- **Windows:** a grant can carry `from` and `until` (RFC 3339, UTC; `until` must be after `from`, `400` otherwise). Windows are a property of the shared grant shape, so they work on any grant; this issue turns them on for team roles, and direct organization roles can use them with no change to the model.
  - When memberships are loaded for a request, grants outside their window are left out, compared with `now`.
  - Nothing has to run when a window opens or closes, and access starts and ends on the second.
  - Realtime streams (18.4) reload access when a window opens or closes: the instance schedules a reload for connected subscribers at the next boundary.
- **Scoped grants:**
  - `grants: [delegate]` on `manager` lets a manager of Sales grant, change the window of, and revoke `delegate` **in Sales only**;
  - they can't grant `manager`, and can't act in other teams;
  - `POST`, `PATCH` and `DELETE /v1/{realm}/_orgs/{org}/teams/{team}/roles` take the role, the member, `from` and `until`;
  - each grant and change is audited.
- **Delegation is bounded** (security review, 24.9): what a delegate gives can't end later than their own authority, and they can't change the window of their own grant; only `manage: teams` is unbounded.
- **Expired grants** stay visible in the membership history for 90 days, then are cleaned up.

## 6. Queries, aggregations and realtime (issue 24.4)

- **Queries and lists:** `_meta.team` is an allowed query field, and `?team=<id>` is a list shorthand.
- **Aggregations:** `_meta.team` can be declared in `aggregate.group_by` (21).
- **Realtime:** filters with `_meta.team` equality are grouping keys for subscriptions (18.3), so a change in Sales reaches only Sales' subscribers.
- **Indexes:** backd adds an index on `_meta.team` (with `_meta.org`) to collections with `team` set.
- **Access changes:** team role grants, window boundaries and team membership changes trigger the realtime access refresh (18.4).

## 7. Membership hooks (issue 24.5)

```yaml
# realm.yaml
organizations:
  hooks:
    on_member_joined:  core/members-joined     # <database>/<function>, internal and async
    on_member_left:    core/members-left
    on_roles_changed:  core/roles-changed
```

- **When they run:**
  - `on_member_joined` runs after an invitation is accepted or a member is added directly. Its input holds the organization, user, invitation id (if any), roles, teams and team roles.
  - `on_member_left` runs after leaving, removal or erasure. Its input holds the organization, user and reason.
  - `on_roles_changed` runs after changes to organization roles, teams or team roles (including grants with windows, not their opening and closing). Its input holds the organization, user, before and after.
- **How:** they run through the job queue like `account.on_signup` (internal, async, with `retry:`), with no user in `ctx`. The functions must be internal and async, which startup checks.
- **Order:** the queue doesn't promise order, so every event carries a `seq`, increasing per organization and user. A handler that must not apply a stale event compares it with the last one it saw. The docs say so.

## 8. MFA by role (issue 24.6)

- **Declaring:** `require_mfa: true` on organization roles and team roles.
- **Effect:** for sessions without MFA, **those roles don't count**. Helpers like `hasOrgRole(…, "hr")` and `hasTeamRole(…, "manager")` are false, the pushdown leaves them out, and plain membership remains, so the person still sees their own data.
- **Errors:** endpoints that need the role, and functions checking it, answer `403 org_role_requires_mfa` (with the role) so clients can prompt enrollment.
- **Combining with organization MFA:** combined with the organization-wide `require_mfa` (14.7), which forces MFA on every member of that organization. All three levels are the same predicate in the membership loader (organizations.md §4), so a grant counts only if its organization, its role and its window all count.
- **Visibility:** managers see which holders of MFA-required roles haven't enrolled.

## 9. Functions, client and types (issue 24.7)

- **`ctx.user.orgs[org]`** includes `team_roles`, active now, with their windows.
- **`ctx.admin.orgs.teams`:** `members(team, { role })`, `grantRole`, `revokeRole`.
- **JS client:**
  - `client.orgs.teams(org).members(team, { role })`, `grantRole`, `revokeRole`;
  - `.inTeam(id)` on creates and queries;
  - typed errors.
- **Types (23):** team role names typed, and `_meta.team` included where collections use it.

## 10. Company context and template data hooks (issue 24.10)

backd's own emails (organization invitations, verification, password reset, magic links, MFA, consent and export notices) and hosted pages are realm-wide today. Apps serving many companies need them to look like each company's.

- **Company context:**
  - sign-in, `POST /v1/{realm}/_auth/magic-link`, `POST /v1/{realm}/_auth/reset-password/request` and sign-up accept an optional `org`;
  - it's used only if the organization exists and, except for invitations, the address belongs to a member; otherwise it's **silently ignored**, with the same answer and timing, so it can't be used to probe membership;
  - invitations carry their organization;
  - the organization is stored with the token (like `redirect_to`), so later emails and pages in that flow keep it.
- **Template data hooks:**
  ```yaml
  # realm.yaml
  email:
    data_hook: core/email-data      # internal sync function, optional
  pages:
    data_hook: core/email-data      # may be the same function
  ```
  - **When they run:** before rendering a system email or hosted page, backd calls the hook with `{ kind, locale, user, org }`.
  - **What they return:** the result is available to templates as `{{.App}}`, and as `{{App}}` in organization templates (§11).
  - **Bounds:** a time limit of 2 s and a result of at most 16 KiB. On failure or timeout, rendering continues **without** `App` (realm defaults), so emails are never delayed or dropped by a hook, and failures are logged.
  - **Startup checks:** hooks are internal sync functions.
  - **Pages only with a company context:** a hosted page calls its hook only when it has an organization (its token or request carried one), so a public page that anyone can open, such as the one of a wrong token, never makes the app's function run (security review, 24.9).
- **Hosted pages** use `App` for branding, and their redirects keep the company's path when `redirect_to` came from a company page.
- **Data sent by functions:** emails sent by functions keep using `ctx.email.send({ data })`. An optional `org` there selects organization templates (§11) and is passed to the hook too.

## 11. Per-organization template overrides (issue 24.11)

```yaml
# realm.yaml
email:
  customizable: [org-invitation, magic-link, reset-password, verify-email, absence-requested, absence-decided, absence-reminder]
  overrides:
    permission: manage_org          # who may edit: holders of manage: org (default)
```

- **API:**
  - `PUT /v1/{realm}/_orgs/{org}/templates/{kind}/{locale}` with subject, text and HTML;
  - `GET` lists overrides and versions, and `DELETE` returns to the realm's file;
  - versioned (last 20 versions) and audited.
- **Safe syntax:**
  - **logic-less**, Mustache style: `{{App.name}}`, `{{Data.days}}`, `{{Link}}`, sections `{{#App.logo}}…{{/App.logo}}` and inverted sections, with no code, includes, partials or helpers;
  - variables are always escaped;
  - HTML passes an **allowlist sanitizer**: formatting tags, tables, links (`https` and `mailto`), images from `https` or the realm's storage, limited inline styles; no scripts, forms or iframes.
- **Checks on save:**
  - rendered with sample data for the kind; errors returned with line and column;
  - **required placeholders** per kind (token kinds need `{{Link}}`, invitations their link and organization name);
  - size cap (64 KiB per template);
  - only kinds listed in `customizable`.
- **Rendering:** for emails with an organization context (§10, invitations, or `ctx.email.send` with `org`), backd uses the organization's override for that locale, else the realm's file. If an override fails at send time, the file is used and the organization's admins are notified through an `email-template-failed` notice, at most once per day per kind.
- **Preview:** `POST …/templates/{kind}/{locale}/preview` with a draft (or nothing, for the saved version), and `sample` or `real` data (real calls the data hook for the caller's organization, at most 30 an hour per organization). It returns subject, text and HTML without sending. Clients show it in a sandboxed frame.
- **Test send:** `POST …/templates/{kind}/{locale}/test` with a draft or the saved version. It sends to the **requesting user's own address only**, with "[TEST]" in the subject, through the realm's delivery function, at most 10 per hour per organization, and audited.

## 12. Documentation (issue 24.8)

- **Concepts:** organization roles versus teams as role groups versus team roles; `inTeam` versus `hasTeamRole`; holding a team role without membership.
- **Recipes:**
  - departments with managers;
  - delegation with dates;
  - cover from another team;
  - moving a person between teams;
  - membership hooks for onboarding and offboarding;
  - MFA for sensitive roles.
- **Company context and owner templates:** passing `org`, writing a data hook, customizable kinds, the template syntax and sanitizer, required placeholders, preview and test send.
- **A worked example:** the HR absence module ([`hr-app/absence-design.md`](../hr-app/absence-design.md)).

## 13. Issues

| id | Issue | Depends on |
|---|---|---|
| 24.1 | Team roles | 14.4 |
| 24.2 | Team-scoped documents and rules | 24.1, 14.2, 14.5 |
| 24.3 | Time windows and scoped team role grants | 24.1 |
| 24.4 | Teams in queries, aggregations and realtime | 24.2, 21.2, 18.3 |
| 24.5 | Organization membership hooks | 14.1, 4.2a |
| 24.6 | MFA by organization role and team role | 24.1, 14.7 |
| 24.7 | Team access in functions, the client and types | 24.3, 14.9, 23.2 |
| 24.8 | Team access documentation | 24.7, 24.11 |
| 24.10 | Company context and template data hooks | 14.3, 4.2, 4.2f, 4.2a |
| 24.11 | Per-organization template overrides | 24.10, 4.2g |
| 24.9 | Security review of team access | 24.8 |
| 24.12 | Team roles in the admin UI | 24.3, 24.6, 14.14 |

**When to build:** right after organizations (14); 24.4 follows 21 and 18.

## 14. Alternatives considered

- **Copying approvers into each document,** synced by functions: rejected as the hack it is; access wouldn't end immediately.
- **Each team as its own organization:** rejected because it breaks company-wide views.
- **Cron functions granting and removing delegates:** rejected because they're late or fail; windows are data.
- **Polling for new members:** rejected for hooks.
- **Organization-wide MFA only:** rejected as too blunt; role-level MFA added.
- **A declared branding mapping read by backd's core:** replaced by data hooks, so backd never reads app collections by configuration.
- **Functions sending every email with pre-rendered content:** rejected; it reopens the open-relay risk and makes every app rebuild token emails.
- **Go templates for owners:** rejected for a logic-less syntax with sanitizing.

## 15. Backlog (outside these milestones)

- Nested teams (a hierarchy), if apps need "manager of a manager".
- Field-level read rules without encryption (backlog, outside these milestones).
- Nothing in this list is part of milestone 24. The admin UI has its own issue (24.12).
