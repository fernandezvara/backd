---
title: "Team roles"
description: "Roles held within one team of an organization: declare them, give them to members, and list who holds what. Separate from organization roles and from the roles teams carry."
icon: "users-three"
weight: 523
toc: true
---

[Teams](../api/#teams) carry organization roles to everyone in them. That is enough for "the editors", but not for "Marta manages **Sales**": a manager of Sales must not manage Support. **Team roles** are roles held *within one team*, by a member of the organization, and they mean nothing outside it.

## Organization roles, teams and team roles

| | Organization role | Team | Team role |
|---|---|---|---|
| Declared in | `organizations.roles` | created through the API | `organizations.team_roles` |
| Holds | what the role allows in the organization | a group of members | a role in one team |
| Given by | `manage: members` or `manage: roles` | `manage: teams` | `manage: teams` |
| Gives access to | everything the role is checked for | the roles it carries | only what checks that team |

A team role never gives an organization or a realm role, and an organization role never gives a team role.

## Declare them

```yaml
# realm.yaml
organizations:
  roles:
    owner: { manage: [org, members, roles, teams] }
    member: {}
  team_roles:
    manager:
      description: Runs the team
      grants: [delegate]        # may give this role in the same team (see time windows)
      require_mfa: true         # counts only for sessions with a second factor
    delegate: {}
```

Names follow the rules of roles. `grants` may only list declared team roles. Startup names what is wrong. A team role removed from `realm.yaml` stays on the memberships and grants nothing, the way a removed organization role does.

## Give them

A member with `manage: teams` gives any team role in any team of the organization:

```http
POST /v1/acme/_orgs/{org}/teams/{team}/roles
{ "user": "<user id>", "role": "manager" }
```

`DELETE` on the same path with the same body takes it away. The user must belong to the organization; they need **not** belong to the team: a delegate from another team can approve Sales' requests without seeing Sales' team page. Granting what is held changes nothing, and each change is audited (`org.team_role_grant`, `org.team_role_revoke`). A member holds at most 50 team role grants (`409 team_limit_reached`).

Deleting a team removes the grants of its roles with it. Removing a member from the organization removes theirs.

## Time windows

A grant can carry `from` and `until` (RFC 3339), so access starts and ends on the second with nothing to run:

```http
POST /v1/acme/_orgs/{org}/teams/{team}/roles
{ "user": "<user id>", "role": "delegate", "from": "2026-08-01T00:00:00Z", "until": "2026-08-15T00:00:00Z" }
```

- **Open realtime streams follow the windows.** Nothing runs when a window opens or closes, and a [stream](../../api/realtime/#when-access-changes) reads the member's access again at that moment: a subscription whose rule depended on the role gets its `reset` with `access_changed` at the boundary, not minutes later.
- **Left out when not in force.** Memberships are read at each request, and a grant outside its window is left out of what rules see, of the listing, and of what the member may grant.
- **`until` must be after `from`** (`400` otherwise); both are optional.
- **`PATCH` on the same path replaces the window** of a held grant (`404` if it isn't held). Granting a role the member already holds changes nothing: use `PATCH`.
- **History:** an ended grant stays in the member's `team_roles` (with `active: false` and who granted it) for 90 days, then a sweep removes it.

## Let a team role give another

`grants` on a team role lets its holders manage exactly those roles, **in the teams where they hold it**:

```yaml
team_roles:
  manager:  { grants: [delegate] }
  delegate: {}
```

A manager of Sales may give `delegate` in Sales, change the window of it and take it away. They can't give `manager`, and they can't act in Support, even if they hold `manager` there. What a delegate gives **can't outlast their own authority**: if their grant ends on the 15th, what they give ends on or before it (a grant with no end, or one beyond, is `400`), and they can't change the window of their own grant, so cover can't be stretched from the inside. Members with `manage: teams` can do all of it in any team. Each change is audited with who made it (`org.team_role_grant`, `org.team_role_change`, `org.team_role_revoke`), and the grant records `granted_by`.

## Roles that require a second factor

`require_mfa: true` works on organization roles and on team roles, so a sensitive role can ask for a second factor without forcing it on the whole organization:

```yaml
organizations:
  roles:
    hr: { manage: [members], require_mfa: true }
  team_roles:
    manager: { grants: [delegate], require_mfa: true }
```

- **Without a second factor the role doesn't count.** `hasOrgRole(…, "hr")` and `hasTeamRole(…, "manager")` are false, the [pushdown](../../auth/rules/#organizations) leaves the organization out of what the role opens, and **plain membership remains**: the person still sees what `inOrg` shows.
- **The error names the role.** An endpoint that needs the role answers `403 org_role_requires_mfa` with the `role` (and the `team`) in `details`, so a client can start the [enrollment](../../auth/mfa/) instead of showing "forbidden". A person who lacks the capability anyway gets the usual `403 forbidden`.
- **One predicate.** A grant counts when its organization (`require_mfa` of the organization, which every member needs), its role and its window all count.
- **Managers see who is missing one:** the members list carries `mfa` and `mfa_roles` (the roles the member holds that require one) for people who manage the organization.
- **API keys on behalf of a user** have no second factor, so these roles don't count for them.

## Documents that belong to a team

A collection can say its documents belong to a team of their organization (`team:` in [`collection.yaml`](../../configuration/config-dir/#teams)), and rules can check it:

```yaml
# collection.yaml of absences
organization: required
on_org_delete: { action: delete }
team: required
on_team_delete: { action: keep }
rules:
  read: >
    inTeam(document._meta.team)
    || hasTeamRole(document._meta.team, 'manager')
    || hasOrgRole(document._meta.org, 'hr')
  create: inOrg(data._meta.org) && inTeam(data._meta.team)
  update: hasTeamRole(document._meta.team, 'manager')
```

| Helper | True when | In a read rule it becomes |
|---|---|---|
| `inTeam(document._meta.team)` | the caller belongs to the document's team | `_meta.team` is one of their teams |
| `hasTeamRole(document._meta.team, "manager")` | they hold that role in the document's team, now | `_meta.team` is one of the teams where they hold it |

- **Both are database filters in a read rule**, so lists and counts show only what the caller may see, and combine with `inOrg`, `hasOrgRole` and `orgClaims` by `&&`, `||` and `!`. In a read rule the first argument must be `document._meta.team`.
- **Creating:** `POST …?org=<org>&team=<team>`. A person who isn't a member of the team and holds no team role in it answers `403 not_team_member`, the same answer as for an unknown team or one of another organization (so it can't be used to find teams). `?team=` needs `?org=`, and `required` collections answer `400 team_required` without it. A function with admin access, or an administrator, may create in any team of the organization.
- **Fixed afterwards:** `PUT` and `PATCH` naming another `_meta.team` answer `400 immutable_field`. Administrators move a document with the [transfer](../../api/documents/#fixing-data-as-an-administrator), naming `team` (or `null` to take it out of its team). Moving to another organization leaves the team: name the team of the new one.
- **Deleting a team** removes the grants of its roles, and applies `on_team_delete` to its documents through a job.

## List who holds what

```http
GET /v1/acme/_orgs/{org}/teams/{team}/members?role=manager
```

Any member of the organization may list. The answer is a page of the members who belong to the team and of those who hold a team role in it; with `role`, only the holders of that role. Each item is a member as in the [members list](../api/#members), plus `in_team` and `roles_in_team` (the team roles they hold there now). Emails follow [`members_visible_to`](../../configuration/realm/#organizations).

| Where | Listing | Giving, re-windowing and taking away |
|---|---|---|
| Functions with `admin: true` | `ctx.admin.orgs.teams.members(org, team, { role })` | `grantRole`, `setRoleWindow`, `revokeRole` of `ctx.admin.orgs.teams` |
| JavaScript client | `client.orgs.teams(org).members(team, { role })` | `grantRole`, `setRoleWindow`, `revokeRole` of `client.orgs.teams(org)` |
| Administrators | `GET /_admin/orgs/{org}/teams/{team}/members` | `POST`, `PATCH` and `DELETE …/roles` |

`ctx.user.orgs[org].team_roles` lists the ones that count for the caller now, with their windows, and the [generated types](../../clients/types/) name the team roles (`team_roles`) and `_meta.team`.

A member sees their own in `GET /_orgs/{org}`, as `team_roles` (team id to role names).

{{< hint style="note" >}}
Team roles are read with the membership at each request, with no cache: a grant or a revocation applies at the member's next request.
{{< /hint >}}
