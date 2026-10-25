---
title: "Where do documents live?"
description: "Organization, optional or none: which collections belong to an organization, what that means for sign-in, rules and clients, and why an app built on organizations keeps its data in organization collections."
icon: "folders"
weight: 520
toc: true
---

In a realm with [organizations](../), a session belongs to one organization (the people sign in *to* it, see [Signing in to an organization](../api/#signing-in-to-an-organization)). Every collection says how its documents relate to that:

```yaml
# collection.yaml
organization: required      # required | optional | none (the default)
on_org_delete:
  action: delete            # delete | keep
```

| `organization:` | A document… | Who sees it | Use it for |
|---|---|---|---|
| `required` | always belongs to one organization (`_meta.org`), the session's | the rules decide, among the people of that organization: `inOrg(document._meta.org)`, `hasOrgRole(…)` | **the data of the product**: projects, invoices, pins, tickets, anything a customer's people share |
| `optional` | may belong to one, or to nobody | the same rules; documents with no organization are the owner's own | data that is sometimes shared with a team and sometimes private (drafts, notes) |
| `none` | belongs to nobody but its owner (`_meta.owner`) | the owner, and whatever the rules add | data that is **the person's, across organizations**: a profile, notification preferences, a device list |

## What the session does

- **Creating.** In an `required` or `optional` collection the new document's organization is the one the session belongs to; the client does not say it. Creating in a `none` collection never involves an organization.
- **Reading.** A person sees what the rules let them see *in the session's organization*: another organization's documents don't exist for this session, even when the same person belongs to it. To work in another organization, sign in to it (`client.org('beta')`).
- **Documents of the person alone** (`none`) stay reachable from every session of that person: they are theirs, and owner rules apply.
- **Leaving.** When someone leaves an organization or is removed, their sessions in it end, and its documents stay with the organization.

## Which one?

Most of an app built on organizations belongs in `required` collections. Ask of each collection: *if this person left the company, should this stay with the company?* If yes, it is `required`. If it goes with the person (their profile, their settings), it is `none`.

A serious multi-tenant product rarely has much of the second kind. Putting a customer's data in a `none` collection with owner rules is how data ends up readable only by the person who wrote it, and lost to the company when they leave. Keep it in the organization, and give people roles inside it.

## A worked example

The Shelf app (the [tutorial's chapter 20](../../../tutorial/20-teams/)) has both:

- `pins` are `required`: a team's pins, readable by that team's members and nobody else. A pin made in "Docs" can't be read from a session in "Elsewhere".
- `assets`, `shares`, `notifications` and `profiles` are `none`: a person's own library, links, alerts and profile. They follow the person into every team they sign in to.

A pin and an asset can therefore live side by side in one screen: the team's pins come from the session's organization, the person's profile from their own documents. Changing the team (another sign-in) swaps the first and keeps the second.

## Choosing for a new collection

| If… | Declare |
|---|---|
| a customer's people share it | `organization: required`, `on_org_delete: { action: delete }` |
| the company should keep it when the customer leaves the platform | `organization: required`, `on_org_delete: { action: keep }` (only realm administrators read it afterwards) |
| it is private to a person, in or out of teams | `organization: none` (no `on_org_delete` needed) |
| it may be either | `organization: optional`, and say in the rules what a document with no organization means |

See [`collection.yaml`](../../configuration/config-dir/#organizations) for the keys and [the rule helpers](../../auth/rules/#organizations).
