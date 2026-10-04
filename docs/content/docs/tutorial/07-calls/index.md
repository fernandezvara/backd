---
title: "7. Functions calling functions"
description: "notify is internal — no HTTP route, only callers that declare it. ctx.call, calls: and fakeCall tests."
weight: 270
toc: true
---

`publish` works. Now the owner should hear about it — and the same "write a notification" will soon be wanted by the digest, the cleanup, the importer. That's a building block, not an endpoint. `internal: true` makes it one.

## The notifications collection

A member reads their own notifications and can flip exactly one field:

{{< example-file path="shelf/main/notifications/rules.yaml" >}}

There is deliberately no `create` rule — nobody may write a notification directly. The only writer is a function.

Create the collection's other two files, `schema.json` and `indexes.json`, with the rules (they are plain listings in the repository):

{{< tutorial-files "main/notifications/schema.json main/notifications/indexes.json main/notifications/rules.yaml main/_functions/notify/function.yaml main/_functions/notify/index.ts main/_functions/notify/input.schema.json main/_functions/notify/index.test.ts" >}}

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

## You should see

- `POST /_func/notify` answers `404` even signed in — internal means no route.
- Publishing a draft (as the curator) lands `asset.published` in the owner's notifications — and the badge counts it.
- `deno test` covers `notify` itself and `publish`'s call to it.
- Removing `calls: [notify]` from `publish/function.yaml` fails `functions build` with `call_not_declared`-style validation — the graph is checked at startup, not at runtime.

Next: chapter 8 — `digest` runs as an async job, with `retry:` and a pollable status.
