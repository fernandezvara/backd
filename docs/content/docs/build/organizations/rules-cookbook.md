---
title: "Rules cookbook"
description: "Copy-ready access rules for organizations: members read, admins delete, roles named by the document, limits by plan and a second factor per organization."
icon: "book-open"
weight: 522
toc: true
---

Rules for collections with an [`organization:`](../../configuration/config-dir/#organizations). The helpers are `inOrg(org)`, `hasOrgRole(org, role)` and `orgClaims(org).claim`, described in [access rules](../../auth/rules/#organizations); `document._meta.org` is the organization of the document and `data._meta.org` the one a create is made in: the organization of the session.

## Members read

```yaml
rules:
  read: inOrg(document._meta.org)
```

Everyone who belongs to the organization reads its documents, and nobody else sees them, counts or lists. In a read rule this is a database filter.

## Editors write, admins delete

```yaml
rules:
  read:   inOrg(document._meta.org)
  create: hasOrgRole(data._meta.org, 'editor') || hasOrgRole(data._meta.org, 'admin')
  update: hasOrgRole(document._meta.org, 'editor') || hasOrgRole(document._meta.org, 'admin')
  delete: hasOrgRole(document._meta.org, 'admin')
```

Roles are checked in the document's organization only: an `admin` of one customer holds nothing in another. Effective roles include the ones from teams and `default_roles`.

## Authors edit their own, admins edit all

```yaml
rules:
  update: >
    user != nil && document._meta.owner == user.id && hasOrgRole(document._meta.org, 'editor')
    || hasOrgRole(document._meta.org, 'admin')
```

## The document names the role

When each document says who may touch it, put a role in a field:

```yaml
rules:
  read:   inOrg(document._meta.org)
  update: hasOrgRole(document._meta.org, document.audience)
```

With `audience: "finance"` on a document, only members holding `finance` in that organization update it. The role may be a field of the document anywhere the helper is used; declare the role names you use in `organizations.roles`, since a value that isn't one matches nobody.

## Limit by plan with claims

Claims are written by administrators or a function, never by members, so a rule can rely on them:

```yaml
rules:
  create: >
    hasOrgRole(data._meta.org, 'editor')
    && orgClaims(data._meta.org).plan in ['team', 'business']
```

A function sets the claim when a payment arrives (`ctx.admin.orgs.setClaims(org, { plan: 'team' })`, see [functions](../functions/)). Claims go inside a comparison (`==`, `!=`, `<`, `>=`, `in`), and the comparison is false for someone who isn't a member. For things a rule can't count, such as "at most 10 projects on the free plan", create through a [function](../../functions/) that reads the claim and counts.

## Feature flags

```yaml
rules:
  read: inOrg(document._meta.org) && 'beta' in orgClaims(document._meta.org).features
```

## A second factor for an organization

Nothing to write in rules. A manager sets `require_mfa` on the organization ([API](../api/#requiring-a-second-factor)) and a session without a second factor stops counting as a member there: `inOrg` and `hasOrgRole` are false for it, and the documents disappear from its lists. The same rules keep working.

## Anyone in some organization

`create` rules run in memory and see `user.orgs`, a map from organization id to `{roles, teams, claims}`:

```yaml
create: user != nil && len(user.orgs) > 0
```

## Collections without a fixed organization

With `organization: optional`, a document may have none. `inOrg(document._meta.org)` is false for those; add what you want for them:

```yaml
rules:
  read: inOrg(document._meta.org) || (user != nil && document._meta.owner == user.id)
```

{{< hint style="warning" >}}
Always put `inOrg` or `hasOrgRole` in a rule of a collection that belongs to organizations. A rule like `user != nil` lets any signed-in user read every organization's documents.
{{< /hint >}}

Try rules before deploying them with [`backd rules test`](../../auth/rules/#testing-rules).
