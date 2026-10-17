---
title: "Team access recipes"
description: "Departments with managers, delegation with dates, cover from another team, moving people between teams, membership hooks and MFA for sensitive roles."
icon: "book-open"
weight: 527
toc: true
---

Recipes for [team roles](../team-roles/) and team-scoped documents. Each one is small and says what it relies on; the [HR absences](../hr-absences/) page puts them together in a tested example.

## Concepts

| | What it is | Rules ask |
|---|---|---|
| **Organization role** | what a person is in the company (`owner`, `hr`, `member`) | `hasOrgRole(document._meta.org, "hr")` |
| **Team** | a group of members. As a *role group* it carries organization roles to everyone in it; as a *document owner* it is where documents live (`_meta.team`) | `inTeam(document._meta.team)` |
| **Team role** | what a person is *within one team* (`manager` of Sales) | `hasTeamRole(document._meta.team, "manager")` |

- **A team can be both.** The roles a team *carries* ([teams](../api/#teams)) are organization roles, given to everyone in it. Team roles are separate: held by a person in one team, giving nothing outside it. A team that carries roles and a person who holds a team role in it don't interact.
- **`inTeam` vs `hasTeamRole`.** `inTeam` is "they belong to the team": everyone in Sales. `hasTeamRole(…, "manager")` is "they hold that role there, now": the manager, whether or not they belong. Combine them with `&&` and `||`: `inTeam(document._meta.team) || hasTeamRole(document._meta.team, "manager")`.
- **A role without membership.** A team role is held by a member of the *organization*, not necessarily of the team: a delegate from Support approves Sales' requests without seeing Sales' calendar or having its roles.

## Departments with managers

Documents carry the department (`team: required`), employees create in their own, managers read and decide for theirs:

```yaml
# collection.yaml
organization: required
on_org_delete: { action: delete }
team: required
on_team_delete: { action: keep }
rules:
  read: >
    user != nil && (document._meta.owner == user.id
      || hasTeamRole(document._meta.team, 'manager')
      || hasOrgRole(document._meta.org, 'hr'))
  create: user != nil && inTeam(data._meta.team)
  update: user != nil && hasTeamRole(document._meta.team, 'manager')
```

```http
POST /v1/acme/_orgs/{org}/teams/{sales}/roles
{ "user": "<marta>", "role": "manager" }
```

Lists are filtered inside the query, so a manager's list holds exactly their department's documents. `GET …/teams/{sales}/members?role=manager` lists the managers.

## Delegation with dates

A manager is away for two weeks. Declare that managers may hand out `delegate`, and give it a window:

```yaml
team_roles:
  manager:  { grants: [delegate] }
  delegate: {}
```

```http
POST /v1/acme/_orgs/{org}/teams/{sales}/roles          # as Marta, a manager of Sales
{ "user": "<dario>", "role": "delegate", "from": "2026-08-01T00:00:00Z", "until": "2026-08-15T00:00:00Z" }
```

Dario's access starts and ends **on the second**, with nothing scheduled: grants outside their window are left out when memberships are read. Marta can change the window (`PATCH`) or end it early (`DELETE`), but she can't grant `manager`, and she can't act in Support. An ended grant stays visible for 90 days.

## Cover from another team

Dario belongs to Support and approves for Sales: that is a `delegate` grant in Sales' team, given to someone who isn't a member of Sales. The rules above already say `hasTeamRole(document._meta.team, 'delegate')`; nothing else changes, and Dario's own Support documents stay Support's.

## Moving a person between teams

Moving a person is a team membership change, not a data migration:

```http
DELETE /v1/acme/_orgs/{org}/teams/{sales}/members/{user}
PUT    /v1/acme/_orgs/{org}/teams/{support}/members/{user}
```

What they *own* stays where it was (documents have a team of their own); the access they had through Sales ends at their next request. To move their documents too, an administrator or a function with admin access uses the [transfer](../../api/documents/#fixing-data-as-an-administrator) with `{ "org": …, "team": … }`, which is also how documents with no team yet get one. Team roles are per team: grant the ones they need in the new one.

## Onboarding and offboarding with hooks

```yaml
organizations:
  hooks:
    on_member_joined: core/onboard
    on_member_left:   core/offboard
    on_roles_changed: core/sync-access
```

`onboard` creates the person's workspace from the event's teams; `offboard` archives it, and `sync-access` compares `before` and `after` to mirror what they hold elsewhere. Compare `seq` to drop a late event: see [membership hooks](../membership-hooks/).

## MFA for sensitive roles

```yaml
organizations:
  roles:
    hr: { manage: [members, teams], require_mfa: true }
  team_roles:
    manager: { grants: [delegate], require_mfa: true }
```

Without a second factor the role doesn't count: `hasTeamRole(…, "manager")` is false and the filter leaves the department out, but the person still sees their own documents. An endpoint that needs the role answers `403 org_role_requires_mfa` naming it, so your app can start the enrollment. Managers see who holds such roles without one (`mfa_roles` of the members list). See [roles that require a second factor](../team-roles/#roles-that-require-a-second-factor).
