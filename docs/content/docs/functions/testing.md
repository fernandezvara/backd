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

`BACKD_DEV=true` follows your config files while you edit them: function code is rebuilt and schemas, rules and the other config are reloaded, with no restart and no `backd functions build` by hand:

```sh
CONFIG_DIR=./config BACKD_DEV=true HTTP_ADDR=127.0.0.1:8080 backd serve
```

- Every second, `backd` checks the files under `CONFIG_DIR` (hidden entries such as `.git` and `.build`, and `node_modules`, are skipped): `realm.yaml`; each collection's `schema.json`, `collection.yaml`, `rules.yaml` and `indexes.json`; `function.yaml` and function code; `app-config/`; email, push and page templates; consent texts.
- When something changed, the whole config is loaded and validated exactly as at startup, function sources that changed are bundled with the same `deno bundle` that `backd functions build` does, and index changes are applied as at provisioning. Only then is the new config swapped in: requests already running finish on the old one, and the next ones use the new one. The log says `config reloaded` with the new fingerprint, the realms and the files that changed.
- A change that doesn't validate (a syntax error in a function, a broken `schema.json`) is **refused**: the previous config keeps running, the errors are logged, and the realm's [configuration view](../../auth/admin/) in the admin API and UI shows them as a `reload_failed` warning until the files are fixed. A failed change isn't retried until a file changes again.
- The `--with-worker` loop keeps the config it started with; restart to give it a new one.
- After each reload, every functions project's [`backd.d.ts`](../writing/#typed-functions) is rewritten, so the types of `defineFunction` follow your schemas.
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
- `collection.files(id, field)` is faked too (`put`, `get`, `delete`, `link`, and `version(name)` for [image versions](../files/#versions-of-an-image)), over the store's memory: see [Files in functions](../files/#testing).
- `emailMessage(overrides)` builds a message in the shape a realm's [delivery function](../email/#the-delivery-function) receives as `ctx.input`; `createContext({ email: true })` gives a function `ctx.email.send`, recorded and checked like backd would (a missing or system `kind`, a message without exactly one of `to_user` and `to`, too many recipients, and an `EmailLimitError` past the invocation cap; `sentEmails()` lists them), and `fakeEmail()` is that fake on its own. It allows any recipient, as backd does: test that your function never takes the recipient or the text from its input.
- `createContext({ config: { features: { export: { enabled: true } } } })` gives the function `ctx.config.get(path)` and `all()` over the values you give (nested like `schema.json`); `configWrite: true` also gives `ctx.admin.config.set()` and `history()`, which change those values and are listed by `configChanges()`. `reset()` is not faked: it needs the schema's defaults, so use `set()` to put one back.
- `createContext({ admin: true, orgs: true })` gives `ctx.admin.orgs` over organizations kept in memory (start from some with `orgs: { orgs: [{ id, name, claims, members, teams }] }`), checked as backd checks them (names and slugs are unique, the last manager stays), and `orgChanges()` lists what the function did. `ctx.user.orgs` is whatever `user.orgs` you pass. See [Organizations in functions](../organizations/).
- `createContext({ push: true })` gives a function `ctx.push.send`, recorded and checked like backd would before queueing (nobody, or more than one way, named; an `audience` the function didn't declare; a kind outside `push: { kinds }`; a field backd refuses; a payload over a provider's size), each failure a `FunctionError` with backd's code; `sentPushes()` lists what was sent, and `fakePush({ kinds, audience })` is that fake on its own. It sends nothing and counts no limits.
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
