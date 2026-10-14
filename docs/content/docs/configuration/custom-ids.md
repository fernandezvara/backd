---
title: "Custom ids"
description: "Let clients choose the id of the documents they create: when to, how to keep it safe, and what it means for probing, personal data and change."
icon: "tag"
weight: 217
toc: true
---

By default `backd` generates the id of every document. A collection can instead take it from the client, for ids that mean something: an invoice number, a reference from another system, or the user's own id for one document per user. This page is about **when to use that and how to do it safely**; the declaration is in [`collection.yaml`](../config-dir/#custom-ids), and the requests and errors are in [Create](../../api/documents/#custom-ids).

## When to use them

| Use | Why |
|---|---|
| One document per user (profile, settings, preferences) | `custom_id: user` makes the id the user's own, so the document is found with `GET …/profiles/<user id>`, nothing else is needed to keep it unique, and backd refuses an id that isn't the caller's |
| A number or reference that exists outside backd | The document is addressed by the number you already print and receive; a second one with the same number is refused |
| Human-readable keys of reference data (`EUR`, `de-DE`) | Fixed vocabularies you create and own |

Don't use them for anything that might **change** (an id never changes), for anything that holds **personal data** (see [below](#personal-data-in-ids)), or only to avoid a unique index: a unique field in `indexes.json` works with generated ids and can change.

{{< hint style="tip" title="Best practice" >}}
Keep the pattern as narrow as the ids you really make: `INV-[0-9]{4}-[0-9]{4,6}` rather than `.*`. Everything inside the pattern is an id any caller allowed to create may try.
{{< /hint >}}

## The one document per user recipe

```yaml
# collection.yaml of "profiles"
custom_id: user
rules:
  read: user != nil
  create: user != nil
  update: user != nil && document.id == user.id
on_owner_delete:
  action: delete
```

```js
await backd.db('main').collection('profiles').create({ id: me.id, displayName: 'Ada' })
const mine = await backd.db('main').collection('profiles').get(me.id)
```

Compared with a `user_id` field and a unique index: the id **is** the key, so there is no field to keep honest, no index to build, a second create is `409 id_taken` by construction, and the rules don't have to compare a field with `user.id` on create. Shelf's [`members` directory](../../tutorial/08-async/#members-in-business-data) is this recipe.

## What a `409` tells a caller

An `id_taken` answer says that **a document with that id exists**, whoever owns it and whether or not the caller may read it. That is the price of letting clients choose ids, so `backd` limits what it gives away:

- **The `create` rule runs before the existence check.** Callers the rule doesn't allow get `403` for every id, existing or not. The rule can see the id as `data.id`, so it can also refuse ids a caller shouldn't use.
- **Duplicates are limited.** After the collection's `duplicate_limit` answers of `id_taken` in a minute (20 by default), the caller gets `429` with `Retry-After` for every create in the collection until the minute passes. The limit is per caller (user, API key, or address for anonymous callers) and per `backd` instance. `backd_id_conflicts_total` counts the duplicates per collection, which is where to look for someone enumerating.
- **A pattern with few possibilities can be enumerated.** `INV-[0-9]{4}` has ten thousand ids; a caller allowed to create can try them at the duplicate limit, slowly. If the existence of an id is sensitive, don't let callers create it directly.

{{< hint style="warning" >}}
For meaningful ids that anyone could try to guess (invoice numbers, order numbers), **close direct creation** (`create: false`) and create the documents from a [function](../../functions/) that decides or checks the id, with `ctx.admin.db`. Direct creation is fine when the rules tie the id to the caller, as `custom_id: user` does.
{{< /hint >}}

```ts
// the function makes the number; clients can't choose or probe it
const number = `INV-${year}-${String(next).padStart(4, "0")}`;
await ctx.admin.db("billing").collection("invoices").create({ id: number, customer, total });
```

`ctx.db` and `ctx.admin.db` creates take `id` like any other client and follow the same pattern and safety envelope; `ctx.admin.db` skips the rules, not the id checks.

## Personal data in ids

An id appears in URLs, in the logs of proxies and CDNs, in references from other documents, in exports and in backups, and it **never changes and can't be anonymized**. An [erase](../../auth/erasure/) of a user can rewrite fields; it can't remove an email address that is somebody's document id.

- **Don't put personal data in ids**: not an email, a name or a phone number. Use the user's id (`custom_id: user`) or a number that means nothing outside your system.
- If a collection must hold documents keyed by something personal, give it `on_owner_delete: delete` so the whole document goes when the user is erased, and keep in mind that the id remains in logs and backups for as long as those are kept.

## Changing an id

An id never changes: `PUT` and `PATCH` don't touch it. To change one:

1. **Create** a new document with the new id and the same fields.
2. **Move the references** in other documents.
3. **Delete** the old one.

Do it in a [batch](../../api/documents/#batch-writes) when everything is in the same database, so the old document is gone only if the new one was created.

## Adding or removing `custom_id`

- **Adding it to a collection that has documents.** The existing documents keep their generated ids, and still work. From the next restart every create must carry an `id` that matches (without it: `400 id_required`). `backd data check` reports the stored ids that don't match the pattern (path `id`, never the value), so you know which documents a pattern doesn't describe; it doesn't change them.
- **Removing it.** Existing documents keep their ids and work as before; new documents get generated ids, and a request that sends `id` is refused (`400 id_not_allowed`). Clients written for the old declaration need to stop sending it.
- **Changing the pattern** applies to new creates only. Existing ids are reported by `backd data check`, not rewritten.
- **Lists change order** with this feature: without `order_by` they are sorted by creation time, then id ([Querying](../../api/querying/#ordering-and-pagination)), whatever the collection's ids look like.
