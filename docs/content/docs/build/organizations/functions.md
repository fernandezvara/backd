---
title: "Organizations in functions"
description: "Read the caller's memberships with ctx.user.orgs, create documents in an organization with ctx.db, and manage organizations from a function with admin access."
icon: "buildings"
weight: 585
toc: true
---

[Organizations](../api/) are groups of users that documents can belong to. A function sees them in three places.

## The caller's memberships: `ctx.user.orgs`

In a realm with organizations, `ctx.user.orgs` is a map from organization id to what the caller is there:

```ts
const org = ctx.user?.orgs?.[orgId];
if (!org?.counts) throw ctx.error(403, "forbidden", "not a member that counts");
if (org.claims.plan === "free" && org.roles.includes("member")) { /* … */ }
```

| Field | Meaning |
|---|---|
| `name` | the organization's name |
| `roles` | the effective roles: direct, default and through teams |
| `teams` | the ids of the member's teams |
| `team_roles` | the [team roles](../team-roles/) that count now, as `{ team, role, from?, until? }`: windows that aren't open are left out, and so are roles that require a second factor the session lacks |
| `claims` | the organization's [claims](../api/#creating-and-reading) |
| `counts` | whether the membership counts for this session: false when the organization requires a second factor and the session used none |
| `require_mfa` | the organization's setting |

The memberships are read once, when the call starts, and are the same data [access rules](../../auth/rules/#organizations) use. Check `counts` before you trust `roles`.

## Creating documents in an organization

`ctx.db` acts as the caller, so a create names the organization the way an app does, and has the **same checks and rules**: the caller must be a member that counts (`403 not_org_member`), and `create` rules see `data._meta.org`:

```ts
await ctx.db("main").collection("projects").create({ title: "Plan" }, { org: orgId });
```

In a collection with [teams](../team-roles/#documents-that-belong-to-a-team) name the team too, `{ org: orgId, team: teamId }`: the caller must belong to it or hold a team role in it (`403 not_team_member`).

`ctx.admin.db` (a function with `admin: true`) may use any active organization, as the admin data route does.

## Managing organizations: `ctx.admin.orgs`

A function with `admin: true`, in a realm with organizations, has `ctx.admin.orgs`: full access, with the function as the actor in the [audit trail](../../../operate/administration/audit/). It is meant for flows such as "a payment arrived: create the organization and set its plan":

```ts
const org = await ctx.admin.orgs.create({ name: input.company, owner: input.user_id });
await ctx.admin.orgs.setClaims(org.id, { plan: "pro", seats: 10 });
await ctx.admin.orgs.invitations.create(org.id, { binding: "email", email: input.admin_email, roles: ["admin"], send: true });
```

| Call | Does |
|---|---|
| `create({ name, owner, slug? })` | creates an organization; `owner` (a user id) holds the realm's `creator_roles` |
| `get(id)`, `list({ status? })` | read organizations, whatever their status |
| `setClaims(id, claims)` | replaces the claims (at most 1 KiB) |
| `members(id)`, `addMember(id, { user, roles })`, `setRoles(id, user, roles)`, `removeMember(id, user)` | membership without an invitation; the last manager can't be removed |
| `assignOwner(id, user)` | makes the user a manager (for an orphaned organization) |
| `teams.list(id)`, `create`, `update`, `delete`, `addMember`, `removeMember` | [teams](../api/#teams) |
| `teams.members(id, team, { role? })` | the team's members and the holders of its [team roles](../team-roles/), in force now |
| `teams.grantRole(id, team, { user, role, from?, until? })`, `setRoleWindow`, `revokeRole(id, team, { user, role })` | give, re-window and take away a team role (RFC 3339 times); audited with the function as the actor |
| `invitations.create(id, input)`, `list(id)`, `delete(id, invitation)` | [invitations](../api/#invitations); `create` answers the `token` and `link` |

Errors of backd's own (an unknown organization, a name taken, the last manager) throw a `FunctionError` with its status and code. Review a function with `admin: true` as privileged code.

## Testing

[`@backd/functions-testing`](../../functions/testing/) fakes both: pass `user: { …, orgs }` for `ctx.user.orgs`, and `orgs: true` (or `orgs: { orgs: [...] }` to start from some) with `admin: true` for `ctx.admin.orgs`, kept in memory and checked as backd checks it. `orgChanges()` lists what the function changed.

```js
const { ctx, orgChanges } = createContext({ admin: true, orgs: true });
await handler(ctx);
assertEquals(orgChanges().map((c) => c.action), ["create", "setClaims"]);
```

The [generated types](../../clients/types/) type all of it: `ctx.user.orgs` and `ctx.admin.orgs` appear in the context of a function that has `admin: true` in a realm with organizations.
