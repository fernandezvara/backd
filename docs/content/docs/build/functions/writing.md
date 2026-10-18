---
title: "Writing a function"
description: "A function's layout, its handler, the ctx object, reading and writing data, errors and schemas."
icon: "code"
weight: 552
toc: true
---

## Layout

Each database may have one `_functions` directory: a Deno project for that application's functions.

```
<realm>/<database>/
  posts/                    collections, as usual
  _functions/
    deno.json               the project's Deno config (imports), optional
    deno.lock               pinned dependency versions, written by Deno
    lib/                    code shared by the functions (not a function)
    stats/                  one folder per function, named like collections
      function.yaml         how the function runs (all keys optional)
      index.ts              the code: index.ts or index.js
      input.schema.json     optional JSON Schema of the input
      output.schema.json    optional JSON Schema of the output
    .build/                 bundles, written by `backd functions build`
```

- Function names follow the same rules as collection names; `lib` is reserved for shared code.
- The module's **default export** is the function: it receives the call's context and returns the output (any JSON value).

```ts
type Context = {
  input: unknown;
  user: { id: string; email: string; email_verified: boolean; roles: string[] } | null;
};

export default async function handler(ctx: Context) {
  return { hello: ctx.user?.email ?? "anonymous" };
}
```

Create a starter with:

```sh
CONFIG_DIR=./config backd template function --realm blog --database main --name stats
```

## The ctx object

```ts
export default async function handler(ctx) {
  ctx.input            // the request body (sync/async only; see ctx.request for webhook)
  ctx.request           // webhook only: { body, headers } — see Webhooks
  ctx.user             // { id, email, email_verified, roles, mfa, methods, consents }, or null
  ctx.idempotencyKey   // the Idempotency-Key header, or null
  ctx.requestId        // the request's X-Request-ID
  ctx.secrets.STRIPE_KEY // a declared secret (see Secrets)
  ctx.db("orders")     // the realm's data, as the caller (see below)
  ctx.admin.db("orders") // full access: admin: true, and always for scheduled runs
  ctx.call("send-receipt", input) // another function of this database, listed in `calls` (see Internal functions)
  ctx.email.send({ kind, to_user, data }) // a custom email: only with `email: true` (see Email)
  ctx.admin.db("orders").batch([...]) // several writes, atomically (see below)
  ctx.step("send", { total: 500 })  // a named step of a long job (see Jobs: Reporting progress)
  ctx.progress(120, "sending")      // how far the current step got
  throw ctx.error(409, "out_of_stock", "Not enough stock", [{ path: "items[0]", reason: "sold out" }])
}
```

- **`ctx.input`** is the request body (`sync`/`async` only); **`ctx.request`** replaces it for `webhook` — see [Webhooks](../webhooks/) for its shape and a full example.
- **`ctx.db(name)`** is a database of the [JavaScript client](../../clients/js/), acting as the caller: their access rules and ownership apply, exactly as if they made the request. For an API key caller, it has the key's full access. It also has [`batch(operations)`](../../api/documents/#batch-writes), for changing several documents (even across collections) all-or-nothing — "create order and decrement stock" can't half-happen.
- **`ctx.admin.db(name)`** exists only when `function.yaml` has `admin: true` (and always in [scheduled runs](../cron/), which have no caller). It has full access to the realm's data, whatever the caller may do; what it writes records the function (`func:<realm>/<database>/<name>`) in `_meta.created_by` and `updated_by`.
- Both work only during the call: their credentials expire at the function's deadline, and stop working at once if the caller is disabled or their key is revoked.
- **`ctx.error(status, code, message, details)`** makes an error for the caller: a 4xx status, a lower-case code, a message, and optional details as `[{path, reason}]`. Anything else the function throws is a `500 function_failed`.
- Whatever the function logs with `console` goes to `backd`'s log (`"msg":"function log"`), tagged with the request ID and the function.

- **`ctx.email.send()`** exists only when `function.yaml` has `email: true`. It queues an email of one of the realm's own kinds, to a user (`to_user`) or addresses (`to`, `cc`, `bcc`), and returns the job's `{ id, status }`. Anyone the function can be made to write to receives mail from your domain: read [Sending email from functions](../email/#sending-email-from-functions).
- **`ctx.secrets`** holds the [secrets](../secrets/) the function declares, decrypted, keyed exactly as declared.

## Who is calling: sessions and identity

A function never receives the caller's session token, and nothing in the request can choose who the function acts as. `backd` authenticates the request first, exactly as for any other route, and only then runs the function (after the [`invoke` rule](../reference/#invoke-rules) allows the call). From that authenticated caller it builds two things, which the function gets and can't change:

- **`ctx.user`**: `{ id, email, email_verified, roles, mfa, methods, consents }` of the signed-in user, read from the database when the call starts; `consents` maps each [consent](../../auth/consents/) the user holds to the version they accepted (an empty object for none); `mfa` and `methods` say how the session was made (a second factor was used; the first factor and the second) and are `false` and `["key"]` for an API key. It is `null` when there is no user: an anonymous call, or an API key that isn't acting on behalf of anyone.
- **`ctx.db`**: a client that acts as the caller. Its credential is a short-lived token signed by `backd` with `BACKD_CALLBACK_KEY`, naming the realm, the function and the caller's user or API key. It works only on the internal listener (the public API refuses it), and stops working at the function's deadline plus 5 seconds.

What that means for each kind of call:

| Who calls | `ctx.user` | `ctx.db` acts as | `ctx.admin.db` |
|---|---|---|---|
| A signed-in user | that user | that user: their [rules](../../auth/rules/) and ownership apply | only with `admin: true` |
| An API key | `null` | the key: full access to the data routes | only with `admin: true` |
| An API key with `X-Backd-On-Behalf-Of` | that user | that user (the key's own access isn't added) | only with `admin: true` |
| Nobody (anonymous, where `invoke` allows it, always for [webhooks](../webhooks/)) | `null` | an anonymous caller | only with `admin: true` |
| An [async job](../jobs/) | its caller, read again when the job runs | the same, read again when the job runs | only with `admin: true` |
| A [scheduled run](../cron/) | `null` | an anonymous caller | always |

{{< hint note >}}
Nothing the caller sends can set `ctx.user`: not the body, not a header. `X-Backd-On-Behalf-Of` is honored only for an API key (a session that sends it is refused), and only an API key holder can use it. What the caller **does** control is `ctx.input`, `ctx.request` (webhooks), and the `Idempotency-Key` and `X-Request-ID` headers (`ctx.idempotencyKey`, `ctx.requestId`, restricted to letters, digits and `._:-`). Treat all of those as untrusted input; trust `ctx.user`.
{{< /hint >}}

Some details worth knowing:

- **`ctx.user` is a snapshot, `ctx.db` is live.** If an administrator disables the user, or revokes the API key, while the function runs, the next `ctx.db` call is refused. A job whose caller was deleted before it runs starts with `ctx.user` set to `null` and acts as an anonymous caller.
- **A function can't exceed its caller** through `ctx.db`: every read and write goes through the same rules as a request made by the caller. The one deliberate exception is `ctx.admin.db`, below.
- **The token reaches only the realm's data routes**, not databases of other realms, and only while the call runs. A function that leaks it leaks, for seconds, what its caller could already do.
- **Decide with `ctx.user`, don't copy it from the input.** If a function needs to know who is asking (for an owner field, a role check), read `ctx.user.id` and `ctx.user.roles`. A `user_id` in the input proves nothing.

```ts
export default async function handler(ctx) {
  if (!ctx.user) throw ctx.error(401, "sign_in_required", "Sign in first");
  if (!ctx.user.roles.includes("staff")) throw ctx.error(403, "staff_only", "Staff only");
  // ctx.db("orders") acts as ctx.user: the rules still apply to every call
}
```

{{< hint style="tip" title="Best practice" >}}
Prefer `ctx.db` to `ctx.admin.db` whenever the caller's own rights are enough: the rules then protect you from your own mistakes. Reach for `admin: true` only for what no caller may do alone, check who is asking with `ctx.user` first, and review the function's code as you would review a privileged script.
{{< /hint >}}

## Reading and writing data

`ctx.db("main")` and `ctx.admin.db("main")` are databases of the [JavaScript client](../../clients/js/#collections): `.collection(name)` gives the same methods you would use from a browser, only server-side.

| Call | Does |
|---|---|
| `get(id, { expand })` | one document (throws a not-found error if the caller can't read it) |
| `list({ where, orderBy, limit, skip, after, expand })` | one page: `{ items, has_more, next_cursor }`; `limit` is 1–100; pass `next_cursor` back as `after` for the next page |
| `iterate({ where, orderBy })` | every match, page by page: `for await (const doc of collection.iterate({ … }))` |
| `create(doc)`, `patch(id, patch)`, `replace(id, doc)`, `delete(id)` | one write; `patch(id, patch, { ifMatch: doc._meta.version })` fails if someone changed the document meanwhile |
| `db.batch([{ op, collection, … }])` | up to 100 writes across collections in one transaction: all or none |

`where` is the [query language](../../api/querying/): `{ status: "draft", "_meta.created_at": { $lt: cutoff } }`. Through `ctx.db`, the caller's [read rule](../../auth/rules/#read-rules-are-database-filters) also filters every list, so a function can't leak what its caller couldn't see.

### Expanding references

A collection that [declares references](../../configuration/config-dir/#references) returns the referenced documents next to the result when you ask with `expand`, as the [client](../../clients/js/#expanding-references) does:

```ts
const orders = ctx.db("main").collection("orders");
const order = await orders.get(id, { expand: "customer(name,email)" });
order._expanded?.customer;                    // { id, name, email }, or null
const page = await orders.list({ where: { status: "open" }, expand: "customer(name),lines" });
```

- **`ctx.db` expands as the caller:** each referenced document has to pass its own collection's read rule for that caller, and is `null` otherwise. **`ctx.admin.db` expands with admin access**, and returns documents the caller of the function could not read: use it deliberately, and select only the fields you mean to return.
- **The same limits and errors** as for any client (`InvalidExpandError`, `ExpandTooLargeError`); each path costs one query on the server, whatever the page size. See [Expanding references](../../api/documents/#expanding-references).

### Aggregating and searching

A function summarizes with `aggregate` and searches with `search`, as the [client](../../clients/js/#aggregations-and-search) does:

```ts
const orders = ctx.db("main").collection("orders");
const { groups } = await orders.aggregate({ groupBy: "status", metrics: ["count", "sum:total"] });
const found = await orders.search("late delivery -refunded", { limit: 10 });
```

- **`ctx.db` aggregates and searches as the caller:** the read rules apply before grouping and searching, so a function can't total what its caller couldn't list. **`ctx.admin.db` has admin access** and counts every document: return only what you mean to.
- **The same declarations and limits** as for any client: the collection's [`aggregate:`](../../configuration/config-dir/#aggregations) and text index, `aggregate_too_expensive` past the time limit.

## Errors

Throw `ctx.error(status, code, message, details?)` for anything the caller should understand: a `4xx` status, a lower-case code, a message and optional `[{path, reason}]` details. The caller gets exactly that (for a job, it lands in the job's `result`). Anything else the function throws, including a failed `ctx.db` call, becomes a generic `500 function_failed`, with the stack trace in `backd`'s log only.

{{< hint warning >}}
That last part surprises people: a `NotFoundError` from `ctx.db` that you don't catch does **not** reach the caller as a 404. The cookbook's `lib/relay.ts` re-throws a caught client error as the equivalent `ctx.error`, so the caller sees what they would calling the collection themselves:
{{< /hint >}}

{{< example-file path="workshop/main/_functions/lib/relay.ts" >}}

```ts
try {
  order = await ctx.db("main").collection("orders").get(order_id);
} catch (err) {
  relay(err, ctx.error);   // 404 for an order that isn't theirs, 409 for a conflict, …
}
```

## Input and output schemas

Put `input.schema.json` and `output.schema.json` next to `function.yaml` and `backd` validates both: input that doesn't match answers `400 validation_error` before your code runs, and an output that doesn't match answers `500 invalid_output` (and is logged). They also feed [editor types](../testing/#editor-types).

{{< example-file path="workshop/main/_functions/order_total/input.schema.json" >}}

{{< example-file path="workshop/main/_functions/order_total/output.schema.json" >}}

Schemas don't apply to `webhook` functions, which take a raw body. A scheduled run has no input, so a schema for a function that is also called by hand must allow `null` (see `daily_digest` in the [cookbook](../cookbook/#a-daily-digest-on-a-schedule)).

## Typed functions

`backd types --realm <realm> --functions` writes a `backd.d.ts` into each database's `_functions/` folder: the realm's [generated types](../../clients/types/) plus a `defineFunction` that gives each handler a context typed from your config. Wrap the handler and name it `<database>/<function>`:

```ts
// main/_functions/place/index.ts
import { defineFunction } from "@backd/functions";

export default defineFunction("main/place", async (ctx) => {
  const sku = ctx.input.items[0];                          // from input.schema.json
  const price = await ctx.call("price", { sku });          // only functions listed in calls:
  const order = await ctx.db("main").collection("orders")  // the collection's document types
    .create({ status: "new", items: [{ sku, qty: 1 }], total: price.amount });
  return { order_id: order.id };                           // checked against output.schema.json
});
```

Plain JavaScript gets the same checking in editors with `// @ts-check`. What the context holds follows `function.yaml`: `ctx.admin` only with `admin: true` (and `ctx.admin.config` with `config: write`), `ctx.email` and `ctx.push` only when declared, `ctx.request` only for webhooks, and `ctx.secrets` has exactly the names the function lists.

- **No setup for builds.** `defineFunction` returns its handler unchanged, so it costs nothing at run time. `backd functions build` maps `@backd/functions` to a small module it keeps in the project's hidden `.backd/` folder; add the mapping yourself (`"@backd/functions": "./.backd/functions.js"` under `imports` in `deno.json`) for `deno test` and editors that read `deno.json`.
- **The name is checked.** A `defineFunction` whose name isn't the function's own location stops startup and says the name to use.
- **Functions without it keep working**, untyped.
- **In dev mode** (`BACKD_DEV=true`) `backd.d.ts` is rewritten after every config reload, so a change to a schema shows up in your editor as you save it ([Dev mode](../testing/#dev-mode)). Otherwise run the command above, commit `backd.d.ts`, and run it with `--check` in CI next to `backd config check`: it fails when the file is missing or no longer matches the config.

## Privileged changes: admin and batch

Some changes no user rule should allow: marking an order refunded, moving money, granting a role. Put them in a function with `admin: true`, restrict who may call it with `invoke`, and make the change atomic with `batch`:

{{< example-file path="workshop/main/_functions/refund/function.yaml" >}}

{{< example-file path="workshop/main/_functions/refund/index.ts" >}}

- `ctx.admin.db` has full access, whatever the caller may do. What it writes records the function (`func:<realm>/<database>/<name>`) in `_meta.created_by` and `updated_by`, so the change is attributable.
- `ifMatch: order._meta.version` makes the patch fail if another request changed the order after the function read it, which aborts the whole batch: no half-refunds.
- `idempotency: required` makes a retried request with the same `Idempotency-Key` return the first answer instead of refunding twice ([Idempotency](../calling/#idempotency)).

{{< hint warning >}}
`admin: true` is the one place where **your code**, not a rule, decides what is allowed. Keep such functions small, restrict who can call them with `invoke`, and validate every input yourself (an `input.schema.json` helps): whoever can call the function can make it do anything it can do.
{{< /hint >}}

## Logging

Whatever the function writes with `console.log`, `console.error` and friends is captured, masked of any declared secret's exact value, tagged with the request id, and kept in the [function's log](../logs/) for a week by default. Never log a document's contents you would not want in a log.

## Shared code and dependencies

Put code several functions share in `_functions/lib/` (it is not a function): the cookbook's `lib/csv.ts`, `lib/signature.ts` and `lib/relay.ts` are examples. Import npm and JSR packages the Deno way (`import { z } from "npm:zod"`, or a mapping in `deno.json`); they are downloaded and pinned at [build time](../testing/#building), never at run time, and the function's network access is limited to what it [declares](../network/).

