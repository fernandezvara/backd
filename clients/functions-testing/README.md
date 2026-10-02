# @backd/functions-testing

A fake `ctx` for testing [backd](https://github.com/fernandezvara/backd) [functions](../../docs/content/docs/functions/_index.md) with `deno test`, without a running backd, an executor or a network.

> Working name, not published anywhere yet. Import it by a relative path from a function's project, e.g. `import { createContext } from "../../../../clients/functions-testing/src/index.js"` (adjust the depth to your `CONFIG_DIR` layout), or copy `src/index.js` into your own project.

```ts
import { createContext } from "@backd/functions-testing";
import handler from "./index.ts";

Deno.test("checkout charges the cart's total", async () => {
  const { ctx, store } = createContext({ input: { cart: "c1" } });
  store.seed("shop", "carts", [{ id: "c1", items: [{ price: 500, qty: 2 }] }]);

  const output = await handler(ctx);

  assertEquals(output.total, 1000);
});

Deno.test("checkout refuses an unknown cart", async () => {
  const { ctx } = createContext({ input: { cart: "missing" } });
  await assertRejects(() => handler(ctx), (e: any) => e.status === 404);
});
```

## What it gives you

- `ctx.input`, `ctx.user`, `ctx.secrets`, `ctx.idempotencyKey`, `ctx.requestId`: whatever `createContext(opts)` is given.
- `ctx.error(status, code, message, details)`: the same shape the real runner gives, so `throw ctx.error(409, "out_of_stock", "...")` and asserting on `e.status`/`e.code` works exactly as it does against a real backd.
- `ctx.db(name)` (and `ctx.admin.db(name)` with `{ admin: true }`): shaped exactly like the [JS client](../js/) — `.collection(name).list/iterate/get/create/replace/patch/delete` — backed by an in-memory `MemoryStore` instead of HTTP. `NotFoundError` and `VersionMismatchError` are the real client's classes (imported, not reimplemented), so `assertRejects(fn, NotFoundError)` works the same way it would against a real backd.
- `ctx.call(name, input, options)`: faked per test. `createContext(opts)` also returns `fakeCall(name, handler)` and `calls(name)`; `opts.calls` is the function's `calls` list, so a name outside it fails with `call_not_declared` as backd does. A fake can return a value, throw `ctx.error(...)` (a `FunctionError`), return `fakeJob({ id, output })` for an async callee, or throw `callTimedOut()`; `calls(name)` returns the recorded `{ input, options }` of each call. A name that isn't faked throws.
- `ctx.email.send({ kind, data, to_user | to, cc, bcc })`: present with `createContext({ email: true })`. Any recipient is allowed (a user's id, or addresses), so there is no list of who a function may write to: test that yours doesn't take the recipient or the text from its input. It records what the function sends, returns `{ id, status: 'queued' }`, and refuses what backd would: a missing or system `kind`, a message without exactly one of `to_user` and `to`, invalid addresses, too many recipients, and past the realm's cap for an invocation an `EmailLimitError` (`code: 'email_limited'`). `createContext({ email: { perInvocation, recipientsPerMessage } })` sets those caps (defaults 50 and 10). `createContext` also returns `sentEmails()`, and `fakeEmail(caps)` is the same fake on its own (`send`, `sent()`).
- `emailMessage(overrides)`: a message in the shape a realm's delivery function receives as `ctx.input`, to test a delivery function: `createContext({ input: emailMessage({ kind: 'reset-password' }) })`.
- `store.seed(database, collection, docs)` to set up data before calling the handler, and `store.all(database, collection)` to inspect what it wrote. Pass the same `store` to several `createContext` calls to share state, e.g. testing that a create shows up in a later list.

## What it doesn't do

This is a test double for a function's own logic, not a reimplementation of backd:

- **No access rules.** `ctx.db` here always has full access, as if every collection had no rules and every document belonged to the caller. Rules are backd's job and are covered by the server's own tests; this helper is for testing what *your function* does, not for re-verifying rules.
- **A subset of the query language.** `where` supports equality and `$eq`, `$ne`, `$gt`, `$gte`, `$lt`, `$lte`, `$in`, `$nin`, `$contains`, `$icontains`, `$exists`, and a top-level `$or`/`$and` of such objects (not nested further). Good enough for typical test setups; not a spec-complete implementation of [querying](../../docs/content/docs/api/querying.md).
- **No batch writes, idempotency, rate limits or concurrency limits.** Those apply at the HTTP layer, not inside `ctx`.

## Development

```sh
deno test clients/functions-testing/src/   # from the repository root
```

or `make functions-testing-test`, which runs it in the same Deno image the executor is tested with.
