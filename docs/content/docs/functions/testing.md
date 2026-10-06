---
title: "Build, dev and test"
description: "Bundle functions, iterate in dev mode, call them through a running backd and unit-test their logic."
icon: "checklist"
weight: 562
toc: true
---

## Building

Functions are bundled before `backd` starts: each function, with everything it imports (`lib/`, npm and JSR packages), becomes one JavaScript file.

```sh
CONFIG_DIR=./config backd functions build
# bundled blog/main/stats → stats-3f2a9c0d1e4b5a6c.js (1843 bytes)
```

- **On Windows and macOS**, `backd` itself runs, but its executor doesn't (see [what runs where](../running/#what-runs-where)): develop with the local stack's containers, which include one.
- The build runs `deno bundle` and needs **Deno 2.9.7** on the `PATH` (or the binary named by `DENO`); it refuses any other version, because bundles must match the Deno that runs them. Without Deno installed, run it in the official Deno image with a Linux `backd` binary (from the release archives):

  ```sh
  docker run --rm -v "$PWD/config:/config" -v "$PWD/backd:/usr/local/bin/backd:ro" \
    -e CONFIG_DIR=/config docker.io/denoland/deno:alpine-2.9.7 backd functions build
  ```
- Bundles go to `_functions/.build/`, one file per function named by its content hash, listed in `.build/manifest.json` with the Deno version and a hash of the project's sources. The same sources always give the same bundles.
- TypeScript is compiled, not type-checked; `backd functions build --check` type-checks too.
- Dependencies are downloaded only at build time. When `deno.lock` exists, the build fails if it's out of date, so every build uses exactly the pinned versions.
- If a function fails to bundle, the build stops and the previous bundles stay in place.

At startup, `backd` (`serve`, `provision`) refuses to run when a database's bundles are missing, were built with another Deno version, don't match the current sources, or were changed after the build, and says which database to rebuild. Build in CI before deploying, or commit `.build/` with the sources.

### Dev mode

`BACKD_DEV=true` rebuilds function sources in the background as you edit them, instead of running `backd functions build` by hand after every change:

```sh
CONFIG_DIR=./config BACKD_DEV=true HTTP_ADDR=127.0.0.1:8080 backd serve
```

- Every second, `backd` hashes each functions project's sources; when a hash changes it runs the same `deno bundle` that `backd functions build` does and starts serving the new bundle immediately — no restart. A broken source (e.g. a syntax error) is logged and retried on the next tick, and the last good bundle keeps serving.
- Only function code is watched. `schema.json`, `rules.yaml`, `function.yaml` and `realm.yaml` still need a restart, the same as any other config change.
- `backd` refuses to start with `BACKD_DEV=true` unless `HTTP_ADDR` is bound to localhost (`127.0.0.1:port`, `[::1]:port` or `localhost:port`) — dev mode must never be reachable off the machine — and logs a warning while it runs, as a reminder this is a local-only, hot-reload exception. The one exception is a container whose ports are published on the host's `127.0.0.1` only (the local docker stack): it sets `BACKD_DEV_ANY_ADDR=true`, and `backd` logs a warning.
- Functions with [`dev_only: true`](../reference/) run only in dev mode: without `BACKD_DEV=true`, `backd` refuses to start and names the function.
{{< hint danger >}}
Never set `BACKD_DEV=true` in production: it turns off the "bundles are frozen and checked at startup" guarantee the rest of the security model relies on.
{{< /hint >}}

### Testing a function through backd

`backd functions invoke` calls a function through a running `backd`, exactly the route and rules any other caller goes through — not a local shortcut — and prints its output:

```sh
backd functions invoke --function shop/orders/checkout --input cart.json --url https://api.example.com
# logs:
#   [log] charging card for 1250
# time: 118ms (executor: 84ms)
# {
#   "order": "o7",
#   "total": 1250
# }
```

- `--input <file>` reads the request body from a JSON file (`-` for standard input); without it, the body is `null`, same as an empty request.
- An [internal function](../internal/) has no `_func` route; `invoke` then runs it through the admin API (and says so), which needs an admin API key or an admin session.
- It authenticates the same way as `backd user`/`backd apikey`: a session stored by `backd login`, or `BACKD_API_KEY`.
- `--as <email>` calls on that user's behalf (an admin API key only, via [`X-Backd-On-Behalf-Of`](../../auth/api-keys/#acting-on-behalf-of-a-user)); without it, calls as whatever credential is active.
- With `BACKD_DEV=true` on the server, the response also carries the function's console logs and its time inside the executor (`X-Backd-Dev-Logs`, `X-Backd-Dev-Duration-Ms`); `invoke` prints them to standard error, output stays on standard output alone so it's safe to pipe. These headers are never sent outside dev mode — a production call never leaks another caller's function logs.

### Unit testing a function's logic

[`@backd/functions-testing`](https://github.com/fernandezvara/backd/tree/main/clients/functions-testing) gives `deno test` a fake `ctx`, so a function's own logic (validation, error codes, what it writes) can be tested without a running backd, an executor, or a network:

```ts
import { createContext } from "@backd/functions-testing";
import handler from "./index.ts";

Deno.test("checkout charges the cart's total", async () => {
  const { ctx, store } = createContext({ input: { cart: "c1" } });
  store.seed("shop", "carts", [{ id: "c1", items: [{ price: 500, qty: 2 }] }]);
  const output = await handler(ctx);
  assertEquals(output.total, 1000);
});
```

- `ctx.db`/`ctx.admin.db` are backed by an in-memory store shaped like the [JS client](../../clients/js/): the same methods, and the same `NotFoundError`/`VersionMismatchError` classes on a missing document or a failed `ifMatch`.
- `collection.files(id, field)` is faked too (`put`, `get`, `delete`, `link`), over the store's memory: see [Files in functions](../files/#testing).
- `emailMessage(overrides)` builds a message in the shape a realm's [delivery function](../email/#the-delivery-function) receives as `ctx.input`; `createContext({ email: true })` gives a function `ctx.email.send`, recorded and checked like backd would (a missing or system `kind`, a message without exactly one of `to_user` and `to`, too many recipients, and an `EmailLimitError` past the invocation cap; `sentEmails()` lists them), and `fakeEmail()` is that fake on its own. It allows any recipient, as backd does: test that your function never takes the recipient or the text from its input.
- `ctx.step()` and `ctx.progress()` are recorded: `const { ctx, steps } = createContext()`, then `steps()` lists each step with its `name`, `total`, `message`, last `current` and every `updates` entry, so a test can check that a long job reports how far it got. They refuse what the runner does (an empty name, a negative number).
- `ctx.call` is faked per test: `const { ctx, fakeCall, calls } = createContext({ calls: ["send-receipt"] })`, then `fakeCall("send-receipt", async (input) => ({ sent: true }))`. A fake can return a value, throw `ctx.error(...)` (the caller sees the same `FunctionError` as with a real callee), return `fakeJob({ output })` for an async callee, or throw `callTimedOut()`; `calls("send-receipt")` lists what the function called, with its input and options. Calling a name the test didn't fake throws, and with `calls` set, a name outside it fails with `call_not_declared` like backd does.
- It has no access rules (`ctx.db` here always has full access) and only a practical subset of the query language: good for testing what your function does, not a substitute for the server's own rule tests.
- The [cookbook](../cookbook/)'s functions have tests you can copy (`order_total/index.test.ts`, `export_orders/index.test.ts`, `lib/lib.test.ts`), run in CI with `make functions-testing-test`; what the fake can't do (batch writes, `ctx.request`, rules, idempotency) is exercised by `TestWorkshopExample`, which runs every function for real.
- Not published yet: import it by a relative path, or copy `src/index.js` into your project. See its [README](https://github.com/fernandezvara/backd/tree/main/clients/functions-testing) for the full API and what it deliberately leaves out.

### Editor types

`backd functions types` reads every function's `input.schema.json` and `output.schema.json` and prints JSDoc typedefs, best-effort, for editor autocomplete on `ctx.input` and the return value:

```sh
CONFIG_DIR=./config backd functions types > _functions/types.d.ts
```

```ts
/** @typedef {{"cart": string, "note": string=}} ShopOrdersCheckoutInput */
/** @typedef {{"order": string, "total": number}} ShopOrdersCheckoutOutput */
```

Paste the output into a `.js`/`.ts` file your editor picks up (or redirect it into one, as above); a function without either schema is skipped.
