# Design: internal functions

- **Issues:** design 4.1c (#7) → feature 4.2a (#8)
- **Status:** decided
- **Related designs:** [email-delivery.md](./email-delivery.md) (first user of internal functions), [account-lifecycle.md](./account-lifecycle.md) (`account.on_delete` hook)

## 1. Why

Some functions must never be reachable over HTTP: email delivery, clean-up hooks, helpers shared by several public functions, scheduled jobs that nobody should trigger by hand. Today every function has a `_func/{name}` route and the only guard is its `invoke` rule. Internal functions have **no HTTP route at all**, and are called only by backd itself, by scheduled runs, by other functions, or by an administrator on purpose.

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Marking | `internal: true` in `function.yaml` (default `false`) |
| HTTP route | `POST _func/{name}` answers `404`, exactly like a function that doesn't exist |
| Invalid combinations | `internal: true` with an `invoke` rule, or with `mode: webhook`, fails startup; `schedule:` is allowed |
| Who calls | backd (events such as email delivery, account clean-up), scheduled runs, other functions in the **same database**, admins through an admin-only route |
| Declaring calls | `calls: [name, …]` in the caller's `function.yaml`; startup rejects unknown names, cycles and chains longer than 4 |
| Identity | the callee inherits the caller's user; with no user, `ctx.db` is anonymous; admin access is always the callee's own |
| Sync / async | the callee's `mode` decides; `ctx.call()` mirrors the JS client's `fn()` |
| Calls from backd | queued first, made by a worker; email as an email job whose message is never stored |
| `invoke` rules and `rate_limit` | `calls` is the permission: a callee's `invoke` rule is not evaluated for nested calls; `rate_limit` applies to HTTP calls only |
| Manual runs | `POST /v1/{realm}/_admin/functions/{database}/{name}/invoke`, admin only, audited, for **any** function (internal or public) |
| Testing | `ctx.call` fakes in `@backd/functions-testing` |

## 3. Configuration

```yaml
# _functions/send-welcome/function.yaml — an internal function
internal: true          # no HTTP route; _func/send-welcome answers 404
mode: async             # sync | async  (webhook is rejected for internal functions)
timeout: 30s
admin: true             # this function's own admin access (ctx.admin.db)
secrets: [POSTMARK_TOKEN]
network: [api.postmarkapp.com]
```

```yaml
# _functions/checkout/function.yaml — a public function that calls internal ones
mode: sync
invoke: "user != nil"
calls: [reserve-stock, send-receipt]   # same database only
```

**Startup checks** (each fails startup and names the file):
- `internal: true` together with `invoke`.
- `internal: true` together with `mode: webhook`.
- A name in `calls` that is not a function of the same database.
- A cycle in the call graph built from every `calls` list (e.g. `a → b → a`).
- A call chain longer than **4 functions** (the entry function plus three nested calls).

## 4. Calling another function

```js
// inside a function
const receipt = await ctx.call('send-receipt', { order_id }, { idempotencyKey: `receipt-${order_id}` });
```

`ctx.call(name, input, options?)` behaves like the JS client's `client.db(...).fn(...)`:

| Callee `mode` | What `ctx.call` does | Returns |
|---|---|---|
| `sync` | runs the callee and waits | the callee's output |
| `async` | enqueues a job (at least once, retries per the callee's `retry:`) | a job handle: `id`, `status()`, `wait({ timeout })` |

- **Timeout:** a sync callee runs with `min(callee timeout, caller's remaining time)`. `job.wait()` is also bounded by the caller's remaining time.
- **Errors:** a `FunctionError` thrown by the callee reaches the caller as a thrown `FunctionError` with the same `status`, `code`, `message` and `details`; anything else arrives as `function_failed`. A timeout arrives as `function_timeout`.
- **Idempotency:** `idempotencyKey` behaves as on HTTP calls (24 h, scoped to callee and caller identity; for async callees the same key returns the same job).
- **Limits:** the callee's own `concurrency` and the realm caps apply. `rate_limit` guards callers on the internet, so it applies to HTTP calls only; nested and backd-originated calls are bounded by the call graph and `concurrency`.
- **`invoke` rules:** `calls` in the caller's `function.yaml` is the permission. The callee's `invoke` rule (if it is a public function) is not evaluated for a nested call, because it guards HTTP; the callee still runs as the inherited user, so data rules apply.
- **Same database only:** a function calls functions of its own database. Cross-database needs (email, account clean-up) are covered by backd's own calls (§6) and by `ctx.email.send()` ([email-delivery.md](./email-delivery.md)).
- **No waiting deadlock:** if the executor has no free process for a nested call (`EXECUTOR_MAX_PROCESSES` reached), the call fails immediately with `503 unavailable` instead of waiting. A parent holding a process never waits for a child that can't start.
- **Enforcement at run time:** the invocation's callback token lists the allowed callees, so a name built dynamically in code that is not in `calls` is refused (`403 call_not_declared`, thrown inside the caller as a `FunctionError`), even though the config check passed.

### 4.1 How a nested call reaches the executor

1. The callback token that backd mints for each invocation carries, besides realm and caller, the list of **allowed callees** (the caller's `calls`), the current **depth**, the invocation id (`parent_id`) and the `origin`.
2. `ctx.call` posts to a new route on the **internal listener** (`POST /_internal/v1/{realm}/{database}/_call/{name}`, accepted only with a callback token, like `ctx.db`). backd checks the allowed list and the depth (also at run time, not only at startup), then runs the callee through the executor, or enqueues a job for an async callee.
3. The callee gets its own envelope and its own callback token: user inherited from the caller (or none), allowed callees from **its** `calls`, depth + 1, `parent_id` set. Admin access is the callee's own (§5).
4. The caller keeps its executor process while it waits for a sync callee; that is why the chain is capped at 4 and a nested call that finds no free process fails at once instead of queueing.

## 5. Identity and permissions

Each rule below is an acceptance criterion.

1. **With a user.** The callee sees the caller's `ctx.user`; its `ctx.db` acts as that user (their rules and ownership apply), exactly as through on-behalf-of.
2. **Without a user.** When called by backd itself, by a scheduled run, or by a function whose own `ctx.user` is `null`: `ctx.user` is `null`, `ctx.db` acts as **anonymous** (rules see `user == nil`, the same semantics as an anonymous HTTP caller), and `ctx.input` is whatever the caller passed.
3. **Admin access is the callee's own.** `ctx.admin.db` exists only if the callee declares `admin: true`. A caller's admin access is never passed down. Scheduled runs keep today's behavior (admin access always present).
4. **Attribution.** Writes through `ctx.admin.db` are recorded as the callee (`func:<database>/<callee>`); writes through `ctx.db` as the user, or as anonymous.
5. **Traceability.** Every nested call is its own invocation in history, with the caller's `request_id`, a `parent_id` (the caller's invocation id) and an `origin` (new fields of the invocation records, shown by the admin invocations API and `backd functions history`): `http`, `function`, `cron`, `admin`, or `backd:<event>` (e.g. `backd:email.verify-email`, `backd:account.on_delete`).

## 6. Calls from backd

backd itself calls internal functions for its own events: email delivery ([email-delivery.md](./email-delivery.md)) and the account clean-up hook ([account-lifecycle.md](./account-lifecycle.md)). These calls **never run in a request path**: they are queued first, and a worker makes them whatever the callee's `mode`. Account clean-up is an ordinary async job. Email is queued as an *email job* that holds no message; the worker renders the message and invokes the delivery function directly, so the message (with its token link) is never stored in a job's input. The callee has no user (`ctx.user` is `null`) and `origin` names the event.

## 7. Running an internal function by hand

`POST /v1/{realm}/_admin/functions/{database}/{name}/invoke`

- Accepted from admin API keys and admin-role sessions; `admin.allowed_networks` and per-user/per-key networks apply.
- Body: `{ "input": { … }, "as": "user@example.com" }`. With `as`, the function runs with that user as `ctx.user`; without it, there is no user.
- Answers like `_func` (output for sync, `202` and a job for async). It accepts **any** function, internal or public, so operators have one way to run anything: the `invoke` rule is not evaluated (the credential is an admin one) and `rate_limit` does not apply.
- **Audited:** `function.invoke_manual` with actor, target (`<database>/<name>`) and the identity it ran as (user **id**, never email). Internal calls between functions are **not** audited: they are ordinary runtime activity, visible in invocation history.
- `backd functions invoke` uses this route automatically when the target is internal.
- Consequence for scheduled functions: `admin: true` is no longer needed just to test one by hand; the comment in the cron template changes accordingly.

## 8. Testing

`@backd/functions-testing` gains:

```js
const ctx = fakeCtx({ user: { id: 'u1' } });
ctx.fakeCall('send-receipt', async (input) => ({ sent: true }));
ctx.fakeCall('reserve-stock', () => { throw new FunctionError(409, 'out_of_stock', 'No stock'); });

await checkout({ order_id: 'o1' }, ctx);
assert.deepEqual(ctx.calls('send-receipt'), [{ input: { order_id: 'o1' }, options: {} }]);
```

- Fakes can return values, throw `FunctionError`, or simulate a timeout.
- `ctx.calls(name)` lists the recorded calls; calling an undeclared or unfaked function throws.

## 9. Security considerations

- Internal functions are invisible over HTTP (`404`), so they can't be probed or abused directly.
- Least privilege is visible in config: `internal`, `calls`, `admin`, `secrets`, `network` per function.
- Admin access never propagates through a call chain.
- The call graph is bounded (no cycles, depth 4), and nested calls fail fast instead of exhausting the executor.
- Manual runs need an admin credential, respect network restrictions and are audited.
- A job started by a nested call is readable at `_jobs/{id}` only by its owner (the inherited user) and by API keys; an internal function's job id reaches an end user only if a public function returns it.

## 10. Documentation

- `function.yaml` reference: `internal`, `calls`; the startup checks.
- Functions guide: "Internal functions" page (when to use them, `ctx.call`, identity rules with and without a user, sync vs async, limits, errors) with a tested example in `examples/config/workshop`.
- Admin API and CLI pages: the invoke route, `as`, audit; the `origin` and `parent_id` fields of invocation history.
- Security model page: internal functions and call graphs.
- `api/openapi.yaml`: the admin invoke route; `404` for internal functions on `_func`.

## 11. Acceptance criteria (issue 4.2a)

- Every row of §2 and every rule of §5 implemented.
- Tests:
  - an internal function answers `404` over HTTP to anonymous callers, sessions, `data` keys and `admin` keys;
  - each startup check of §3 fails with the file named;
  - identity rules 1–5, including the no-user case and `ctx.db` acting as anonymous;
  - a dynamic call to an undeclared function is refused;
  - nested timeout uses the caller's remaining time;
  - the fail-fast `503` when no executor process is free;
  - `FunctionError` propagates with status and code;
  - manual runs of internal and public functions: admin only, network restrictions applied, audit record written without email;
  - a nested call does not evaluate the callee's `invoke` rule; `rate_limit` is not applied to nested calls;
  - the callback token carries allowed callees, depth and `parent_id`; the internal `_call` route refuses everything else;
  - `@backd/functions-testing` fakes.
- Docs and OpenAPI updated (§10); the JS client's `client.admin` gets `invokeFunction()`.

## 12. Alternatives considered

- **Reusing `invoke` to mean internal** (e.g. `"false"`): conflates "nobody may call it" with "unreachable", and answers `401`/`403`, revealing the function exists.
- **A separate `_functions/_internal/` folder:** breaks "one folder per function, everything in `function.yaml`".
- **Callee always without a user:** every internal function would become privileged.
- **Caller chooses identity (`ctx.admin.call`):** two ways to call; the callee can't know which identity it gets.
- **No `calls` declaration:** cycles and typos only found when the code path runs.
- **Caller chooses sync/async (`ctx.enqueue`):** `mode` would stop meaning anything for internal functions.
- **No manual runs in production:** operators lose the ability to re-run a failed clean-up.

## 13. Follow-ups

- f2 (notify when a job completes) can call an internal function as its `on_complete` target.
- Retries for async functions (`retry:`) and `dev_only:` are general function features introduced by [email-delivery.md](./email-delivery.md) (issues 4.2c and 4.2d).
