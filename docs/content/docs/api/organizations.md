---
title: "Organizations"
description: "Create organizations, list their members and change their roles with the organizations API, as a signed-in user."
icon: "domain"
weight: 350
toc: true
---

Organizations group users, such as the people of a customer's company. They are declared in [`realm.yaml`](../../configuration/realm/#organizations): which roles exist and what each may manage. Everything here is under `/v1/{realm}/_orgs`, needs a session (API keys can't use it), and is in the repository's `api/openapi.yaml` under the tag `organizations`.

## Creating and reading

| Request | Who | Answers |
|---|---|---|
| `POST /_orgs` `{"name", "slug"?}` | anyone the realm's `create` rule allows, below `max_created_per_user` | `201` with the organization; the creator holds `creator_roles` |
| `GET /_orgs` | any user | `200` with the caller's organizations, each with their effective roles |
| `GET /_orgs/{org}` | members | `200` with name, slug, status and claims |
| `PATCH /_orgs/{org}` `{"name"?, "slug"?}` | `manage: org` | `200` |

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

- **Slugs** are unique among organizations that aren't deleted (`409 slug_taken`): lowercase letters and digits separated by single `-`, at most 63 characters.
- **Claims** are facts about the organization that only realm administrators and functions write (`plan`, `seats`…). Members read them.
- **A non-member and an unknown organization answer the same** `403 not_org_member`, so the answer never says whether an organization exists.
- **`409 org_limit_reached`** means the caller created the most organizations they may.

## Members

| Request | Who | Answers |
|---|---|---|
| `GET /_orgs/{org}/members?limit=&after=` | members | `200` with a page sorted by user id |
| `PATCH /_orgs/{org}/members/{user}` `{"roles": [...]}` | `manage: members` or `manage: roles` | `200` with the member; replaces their direct roles |
| `DELETE /_orgs/{org}/members/{user}` | `manage: members` | `204` |
| `POST /_orgs/{org}/leave` | members | `204` |

A member lists their direct `roles`, their `effective_roles` with where each came from (`direct`, `default` or a team) and `overlap`, the roles held more than one way. Emails appear according to [`members_visible_to`](../../configuration/realm/#organizations).

{{< hint style="note" >}}
What each role may do follows two rules. With `manage: roles` any role can be given or taken away. With `manage: members` only roles that open no capability the caller lacks, and not for a member who holds capabilities the caller lacks: an administrator can't remove an owner. `403 forbidden` says the caller's roles don't allow it.
{{< /hint >}}

The last member who can manage the organization (`manage: org`) can't leave, be removed or lose that role: `409 last_org_manager`. Access ends at once when someone leaves or is removed; documents they created stay.

## Administrators

Realm administrators with the `users` area use `/_admin/orgs`, described with the [admin API](../../auth/admin/), and [`backd orgs`](../../auth/cli/): list and show organizations, create one for a user, add members without an invitation, replace claims and assign a manager to an organization left without one.
