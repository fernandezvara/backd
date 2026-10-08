---
title: "7. Functions calling functions"
description: "notify is internal — no HTTP route, only callers that declare it. ctx.call, calls: and fakeCall tests."
weight: 270
toc: true
---

`publish` works. Now the owner should hear about it — and the same "write a notification" will soon be wanted by the digest, the cleanup, the importer. That's a building block, not an endpoint. `internal: true` makes it one.

## The notifications collection

A member reads their own notifications and can flip exactly one field:

{{< example-file path="shelf/main/notifications/collection.yaml" >}}

There is deliberately no `create` rule — nobody may write a notification directly. The only writer is a function.

Create the collection's other files, `schema.json` and `indexes.json`, with the `collection.yaml` above (they are plain listings in the repository):

{{< tutorial-files "main/notifications/schema.json main/notifications/indexes.json main/notifications/collection.yaml main/_functions/notify/function.yaml main/_functions/notify/index.ts main/_functions/notify/input.schema.json main/_functions/notify/index.test.ts" >}}

## notify, internal

{{< example-file path="shelf/main/_functions/notify/function.yaml" >}}

`internal: true` removes the HTTP route entirely — `POST /_func/notify` answers `404`, exactly like a function that doesn't exist. Its only callers are other functions that declare it, admin invokes, and scheduled runs.

## calls — a declared, closed graph

`publish` says the one function it may call:

```yaml
# publish/function.yaml — add at the end
calls: [notify]
```

and `publish/index.ts` calls it just before its `return`:

```ts
  // The owner is told — as an afterthought: a failing notification must
  // not undo or hide a publish that already happened.
  if (asset._meta?.owner) {
    try {
      await ctx.call("notify", {
        to_user: asset._meta.owner,
        kind: "asset.published",
        text: `“${asset.title}” was published`,
        asset_id,
      });
    } catch (err) {
      console.log(`the notification was not written: ${err instanceof Error ? err.message : err}`);
    }
  }
  return { asset_id, published_at };
```

Rebuild and restart as before (`docker compose run --rm functions-build && docker compose restart backd`). This is also the moment to save the finished `publish/index.test.ts` of chapter 5 — it asserts the call.

`ctx.call` is checked against that list: call something undeclared and the answer is `call_not_declared`; the graph can't cycle or nest deeper than 4 ([Internal functions](../../functions/internal/)). In the handler it's an `await` like any other — and deliberately *last*, wrapped in try/catch: a failing notification must not undo a publish that already happened.

## Testing the call

`fakeCall` registers a stand-in for `notify`; `calls` reports what the function actually asked for — the test asserts the message, not the plumbing:

```ts
const { ctx, store, fakeCall, calls } = createContext({ admin: true, user: curator, input: { asset_id: "a1" } })
store.seed("main", "assets", [{ id: "a1", title: "A draft", kind: "note", _meta: { owner: "u1" } }])
fakeCall("notify", () => ({ id: "n1" }))

await handler(ctx)
const [call] = calls("notify")
// call.input.to_user === "u1", kind === "asset.published", asset_id === "a1"
```

## The view

The app's Notifications section is live: your own notifications newest-first, the nav badge counts unread, **Mark all read** is a batch of `patch(id, { read_at })` — the one field the rules allow a client to write.

## Optional: a schema that grows

Publishing now has a second consumer — the owner, who gets a notification — and soon a third: someone will ask *who* published an asset. That is a new field, and a good moment to see how `backd` treats a schema change. Skip this section if you like; nothing later depends on it (and the repository's finished `examples/config/shelf` does not include it).

**1. Declare the field.** In `config/shelf/main/assets/schema.json`, add one property next to `published_at` (do not add it to `required`):

```json
"published_by": { "type": ["string", "null"], "description": "User id of the curator who published it, stamped by publish." },
```

**2. Keep clients away from it.** The rule that stops members writing `published_at` has to cover the new field too, or any member could name any curator. In the `rules:` of `assets/collection.yaml`:

```yaml
create: user != nil && data.published_at == nil && data.published_by == nil
update: >
  user != nil && document._meta.owner == user.id
  && !('published_at' in changed()) && !('published_by' in changed())
```

**3. Stamp it.** In `publish/index.ts`, the patch becomes:

```ts
await db.collection("assets").patch(asset_id, { published_at, published_by: ctx.user?.id ?? null });
```

Rebuild and restart (`docker compose run --rm functions-build && docker compose restart backd`). The log now says `collection validator updated` for `assets`: at startup `backd` compared `schema.json` with the database's validator and applied the difference. Check what it did:

- Assets published **before** the change are untouched: they come back without `published_by`, and editing them still works. A new optional field never breaks old documents.
- Publish a draft and read it back: `published_by` is the curator's id. Try to set it from a member session (`PATCH` with `{"published_by": "someone"}`) and the rule answers `403`.
- Reload the gallery: the new asset's card reads *published by curator@shelf.example*, the old one says nothing. The app (already written for the finished tutorial) turns the id into an address through the `members` directory that chapter 8 fills — so after chapter 8 each curator needs to have signed in once — and shows the line only when `published_by` is there.

That is the safe kind of change: **adding an optional field**. The unsafe kind — making something required that old documents lack — turns those documents read-only until they are migrated, because every write is validated against the *current* schema; [Schema changes](../../api/documents/#schema-changes) describes it, and in production the new schema is applied as a deploy step with [`backd provision`](../../operations/deploying/) rather than at startup.

## You should see

- `POST /_func/notify` answers `404` even signed in — internal means no route.
- Publishing a draft (as the curator) lands `asset.published` in the owner's notifications — and the badge counts it.
- `deno test` covers `notify` itself and `publish`'s call to it.
- Removing `calls: [notify]` from `publish/function.yaml` does not break publishing — the call sits in a `try/catch` — but the function log says `this function didn't declare main/notify in its \`calls\`` and no notification is written: the graph is enforced when the call is made.

Next: chapter 8 — `digest` runs as an async job, with `retry:` and a pollable status.
