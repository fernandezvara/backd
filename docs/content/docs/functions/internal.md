---
title: "Internal functions"
description: "Functions with no HTTP route, for work that only backd, scheduled runs and other functions may start."
icon: "lock"
weight: 557
toc: true
---

Some functions must never be reachable from the internet: a clean-up that deletes data, a helper shared by several public functions, a scheduled job nobody should trigger by hand. An **internal** function has no HTTP route at all.

## Marking a function internal

```yaml
# _functions/cleanup/function.yaml
internal: true
mode: async
```

`POST /v1/<realm>/<database>/_func/cleanup` now answers `404 not_found`, exactly like a function that doesn't exist, to every caller: anonymous, signed in, API keys of either role. Nothing reaches the executor, and the answer doesn't reveal that the function exists.

`internal: true` can't be combined with an `invoke` rule (there is no HTTP call to guard) or with `mode: webhook` (a webhook is called over HTTP by its sender); `schedule` is allowed. `backd` refuses to start with either combination and names the file.

## Declaring which functions it may call

A function lists the functions of its own database it may call in `calls`:

```yaml
# _functions/checkout/function.yaml
invoke: "user != nil"
calls: [reserve-stock, send-receipt]
```

At startup `backd` rejects a name that isn't a function of the same database, a function that calls itself, a cycle (`a -> b -> a`) and a chain longer than 4 functions, naming the file and the cycle.

## Calling another function

```js
// inside a function that lists send-receipt in `calls`
const receipt = await ctx.call("send-receipt", { order_id }, { idempotencyKey: `receipt-${order_id}` });
```

`ctx.call(name, input, options)` works like the [JavaScript client's](../../clients/js/#functions) `db.fn()`, and what it returns depends on the callee's `mode`:

| Callee `mode` | What `ctx.call` does | Returns |
|---|---|---|
| `sync` | runs the callee and waits | the callee's output |
| `async` | queues a [job](../jobs/) | a job handle: `id`, `status()`, `wait({ timeout })` |

- **Timeout.** A sync callee runs for the shorter of its own `timeout` and the time its caller has left, so a callee never outlives the function waiting for it. `job.wait()` is bounded by the caller's own deadline too.
- **Errors.** If the callee throws `ctx.error(status, code, message)`, the caller gets a thrown error with the same status, code, message and details (a `FunctionError`: let it end the caller to pass it on, or catch it). Anything else arrives as a `BackdError` with `function_failed`, `function_timeout` or `unavailable`.
- **Idempotency.** `idempotencyKey` behaves as on HTTP calls: for 24 hours, the same key and input give the same answer (for an async callee, the same job).
- **No waiting on yourself.** The caller keeps its executor process while it waits for a sync callee. If the executor has no free process for the callee, the call fails at once with `503 unavailable` instead of waiting, so a parent never waits for a child that can't start.
- **Declared calls only.** The credential a running function holds lists exactly its `calls`. A name built at run time that isn't listed is refused with `403 call_not_declared`, even though the startup check passed. The same goes for functions of another database, and for chains nested deeper than 4 (`call_too_deep`).
- **`invoke` rules and rate limits guard HTTP.** Listing a function in `calls` is the permission: its `invoke` rule isn't evaluated when another function calls it, and its `rate_limit` doesn't apply. Its `concurrency` and the realm's limits do.

## Who the callee acts as

1. **With a user.** The callee sees the caller's `ctx.user`, and its `ctx.db` acts as that user: their access rules and ownership apply, as if they had made the calls themselves.
2. **Without a user.** When the caller has no user (an anonymous call, or an API key that isn't acting on behalf of anyone), `ctx.user` is `null` and the callee's `ctx.db` acts as an anonymous caller. An API key's own full access is **not** passed down.
3. **Admin access is the callee's own.** `ctx.admin.db` exists in the callee only if its own `function.yaml` has `admin: true`. A caller's admin access is never inherited.
4. **Attribution.** What the callee writes through `ctx.admin.db` is recorded as the callee; through `ctx.db`, as the user (or anonymous).
5. **Traceability.** Every nested call is its own record in the [invocation history](../logs/), with the same `request_id`, a `parent_id` and an `origin` of `function`.

## Running a function by hand

An administrator runs any function, internal or public, through the admin API: to re-run a clean-up that failed, or to test a scheduled function without waiting for the clock.

```sh
backd functions invoke --function workshop/main/cleanup --input input.json --as ana@example.com
```

- `backd functions invoke` tries the normal `_func` route and, when it answers `404` (an internal function), runs the function through `POST /v1/<realm>/_admin/functions/<database>/<name>/invoke` instead, and says so. It needs an admin API key or an admin session; `admin.allowed_networks` and a user's `admin_networks` apply.
- `--as <email>` runs the function with that user as `ctx.user`; without it there is no user and `ctx.db` acts as anonymous. A function with a [`schedule`](../cron/) run with no user acts as its schedule would, with full access as itself, so it needs no `admin: true` just to be tested.
- The function's `invoke` rule isn't evaluated (the credential is an admin one) and its `rate_limit` doesn't apply; its input schema does, and so does `idempotency: required`. `webhook` functions can't be run this way.
- Every run is [audited](../../auth/audit/) as `function.invoke_manual`, with the user's id, never their email. Calls between functions are not audited: they show in the [invocation history](../logs/).
- From the [JavaScript client](../../clients/js/#functions): `await backd.admin.invokeFunction('main/cleanup', { input, as })`.

{{< hint style="tip" title="Best practice" >}}
Keep a function internal unless something outside needs to call it. `internal`, `calls`, `admin`, `secrets` and `network` together show in one place what each function may do.
{{< /hint >}}
