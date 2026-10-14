---
title: "Team roles"
description: "Roles held within one team of an organization: declare them, give them to members, and list who holds what. Separate from organization roles and from the roles teams carry."
icon: "groups"
weight: 523
toc: true
---

[Teams](../../api/organizations/#teams) carry organization roles to everyone in them. That is enough for "the editors", but not for "Marta manages **Sales**": a manager of Sales must not manage Support. **Team roles** are roles held *within one team*, by a member of the organization, and they mean nothing outside it.

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

A manager of Sales may give `delegate` in Sales, change the window of it and take it away. They can't give `manager`, and they can't act in Support, even if they hold `manager` there. Members with `manage: teams` can do all of it in any team. Each change is audited with who made it (`org.team_role_grant`, `org.team_role_change`, `org.team_role_revoke`), and the grant records `granted_by`.

## List who holds what

```http
GET /v1/acme/_orgs/{org}/teams/{team}/members?role=manager
```

Any member of the organization may list. The answer is a page of the members who belong to the team and of those who hold a team role in it; with `role`, only the holders of that role. Each item is a member as in the [members list](../../api/organizations/#members), plus `in_team` and `roles_in_team` (the team roles they hold there now). Emails follow [`members_visible_to`](../../configuration/realm/#organizations).

| Where | Call |
|---|---|
| Functions with `admin: true` | `ctx.admin.orgs.teams.members(org, team, { role })` |
| JavaScript client | `client.orgs.teams(org).members(team, { role })` |
| Administrators | `GET /_admin/orgs/{org}/teams/{team}/members`, `POST`, `PATCH` and `DELETE …/roles` |

A member sees their own in `GET /_orgs/{org}`, as `team_roles` (team id to role names).

{{< hint style="note" >}}
Team roles are read with the membership at each request, with no cache: a grant or a revocation applies at the member's next request.
{{< /hint >}}
