# Design: types and OpenAPI per realm

- **Issues:** features 23.1–23.8 (milestone `23-types-openapi`)
- **Status:** decided
- **Related designs:** every design that adds API or config surface extends the generator (see the definition of done in `roadmap.md`); [internal-functions.md](./internal-functions.md), [app-config.md](./app-config.md), [reference-expansion.md](./reference-expansion.md), [query-language.md](./query-language.md), [organizations.md](./organizations.md)

## 1. The idea

A realm's config already describes its API completely: collections and their schemas, references, files, functions and their inputs, configuration, templates, organization roles. backd turns that into:

- **TypeScript types for apps:** the JS client takes them as a type parameter and types everything, from document fields to function calls to configuration paths;
- **TypeScript types for functions:** a generated `backd.d.ts` and a `defineFunction` helper type `ctx.input`, `ctx.db`, `ctx.config`, `ctx.call` and templates, for TypeScript and plain JavaScript alike;
- **OpenAPI 3.1 documents per realm:** a public one (client API) and an admin one (admin API and internal functions), so standard generators can produce typed clients for Dart, Kotlin and Swift until hand-written clients exist.

**One generator, one source:** everything is produced from the same loaded config the server enforces. Fingerprints and CI checks catch drift; nothing is enforced at runtime.

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Delivery | `backd types` and `backd openapi` (files); `backd.d.ts` regenerated automatically in dev mode; OpenAPI also served by the instance |
| Dev mode | watches and reloads the whole realm config live, validated, swapped atomically, previous config kept on errors (first issue) |
| Functions | one `backd.d.ts` registry plus `defineFunction("db/name", handler)`; JavaScript via `// @ts-check`; name checked at startup |
| JS client | type-only: `createClient<Realm>()`; the runtime package is the same for every realm |
| Public OpenAPI | client-reachable collections, auth, non-internal functions, `_config`, realtime, organizations, devices; admin-readable unless `openapi.public: true` |
| Admin OpenAPI | admin API and internal functions; admins only |
| Drift | fingerprint in every generated file; `--check` and `--diff` in CI; development warning header; never enforced in production |

## 3. Live config reload in development (issue 23.1)

The generator needs a server that follows config changes while developers work. The first issue confirms what dev mode already does (it watches function code) and extends it to the whole realm:

- **Watched:** `realm.yaml`; every collection's `schema.json`, `collection.yaml`, `rules.yaml` and `indexes.json`; `function.yaml` and function code; `app-config/`; email, push and page templates; consent texts.
- **On change:**
  1. the realm's config is validated exactly as at startup;
  2. on success, it's swapped in atomically (requests in flight finish on the old config);
  3. on failure, the **previous config keeps running** and errors are printed, and also shown in the admin UI's warnings.

  Index changes are applied as at provisioning.
- **Reload event:** a successful reload emits an internal event with the realm and what changed, used by the type generator (and available to other dev tooling).
- **Development only** (`BACKD_DEV=true`). Production live reload stays backlog 7.17, which can reuse this work.

## 4. The generator (issue 23.2)

### 4.1 Mapping JSON Schema to TypeScript

| JSON Schema | TypeScript |
|---|---|
| `object` with `properties`, `required` | interface with optional and required members |
| `additionalProperties: false` | closed type |
| `array` / `items` | `T[]` |
| `enum`, `const` | literal unions |
| `type: ["string", "null"]` | `string \| null` |
| `oneOf`, `anyOf` | unions |
| local `$ref` / `$defs` | named types |
| `format: date-time` | `DateTimeString` (a branded `string`) |
| unsupported keywords | `unknown`, with a generator warning |

### 4.2 backd-specific types

- **`_meta`:** `owner`, `created_by`, `updated_by`, dates, `version`, plus `org`, `redacted` and `score` where the collection uses them.
- **File fields:** their details type (id, name, size, type, versions with status).
- **Encrypted fields:** typed as their plaintext, plus `| null` when the field has `reveal`.
- **References:** `_expanded` types per reference, with field selection narrowing them.
- **Queries:** `where` and `order_by` types per collection: allowed fields and the operators each type allows, including `$search` when a text index exists.
- **Aggregations:** declared `group_by` and `metrics` fields, and result names.
- **Functions:** input and output from `function.yaml` schemas, mode and `internal`.
- **App configuration:** paths and value types from `app-config/schema.json`.
- **Templates:** email and push kinds with their data shapes *(when templates declare data schemas; `Record<string, unknown>` otherwise)*.
- **Organizations:** role names, and claims as `Record<string, unknown>` *(claims have no schema yet)*.

### 4.3 Names

Types are addressed by path, `Realm["db"]["main"]["collections"]["orders"]["doc"]`, so no naming rules are needed. Friendly aliases (`MainOrders`, `MainOrdersWhere`) are generated, with collision checks that fall back to paths and a warning.

### 4.4 The command

- **Generating:** `backd types --realm <r> [--db <d>] --out <dir>` reads the local `CONFIG_DIR`, or a running instance with `--from <url>` (admin access).
- **Output:** `types.d.ts` plus `realm.json` (the fingerprint and generator version).
- **`--check`** exits non-zero with the list of differences when the files are stale.
- **`--diff`** prints type-level changes (field added, type changed, collection removed, function input changed) for pull request reviews.

## 5. Types for functions (issue 23.3)

```ts
// functions/orders/place.ts
import { defineFunction } from "@backd/functions";

export default defineFunction("orders/place", async (ctx) => {
  for (const item of ctx.input.items) { … }                      // from function.yaml input schema
  const order = await ctx.db.collection("orders").create({ … });  // typed document
  if (ctx.config.get("shop.promo_x.enabled")) { … }               // typed path and value
  await ctx.email.send({ to_user: order._meta.owner, kind: "order-placed", data: { … } });
  return { order_id: order.id };                                   // checked against the output schema
});
```

```js
// @ts-check  (plain JavaScript gets the same checking in editors)
import { defineFunction } from "@backd/functions";
export default defineFunction("orders/place", async (ctx) => { … });
```

- **`backd.d.ts`:** generated in each realm's functions folder. In dev mode it's regenerated after every successful reload; otherwise `backd types --functions` generates it. It declares the realm's registry: functions with input and output, collections per database, configuration, templates and organization roles.
- **`defineFunction(name, handler)`:** in `@backd/functions`, it's an identity wrapper at runtime (no cost) that looks the name up in the registry for types. Functions without it keep working untyped.
- **`ctx.call("db/name", input)`:** typed from the callee's input and output, and allowed only for functions listed in `calls:`.
- **Startup check:** a function whose `defineFunction` name doesn't match its own location fails startup, with the expected name.

## 6. Types in the JavaScript client (issue 23.4)

```ts
import { createClient } from "@backd/client";
import type { Realm } from "./backd/types";

const client = createClient<Realm>({ url, realm: "shop" });
const page = await client.db("main").collection("orders")
  .list({ where: { status: "paid", total: { $gte: 100 } }, order_by: "-total", expand: "customer(name)" });
page.items[0]._expanded.customer?.name;                         // string | undefined
const r = await client.functions.call("orders/place", { items }); // typed input and result
client.config.get("maintenance.enabled");                        // boolean
```

- **What's typed:** databases, collections, documents, `where` (fields, operators per type), `order_by`, `expand` with field selection and `_expanded`, aggregations, files, organizations, function calls, configuration paths and realtime subscriptions.
- **Type-only:** the runtime package is the same for every realm. Without the type parameter, everything works untyped.
- **Stale-types warning:** in development builds, the client sends `X-Backd-Types: <fingerprint>`. On a mismatch, the server answers with `X-Backd-Types-Stale: true` and the client logs "types are out of date, run backd types" once. Production builds send nothing.

## 7. OpenAPI documents (issues 23.5 and 23.6)

### 7.1 Public document

- **Covered:**
  - every collection a client can reach (at least one rule isn't literally `false`), with typed list, get, create, update, delete and the features it enables (files, aggregation, search, conditional GET);
  - schemas from `schema.json`, plus `_meta` and `_expanded`;
  - auth endpoints for the methods the realm enables;
  - non-internal functions with input and output schemas;
  - `_config` with the configuration schema;
  - realtime, organizations and devices when enabled;
  - error responses with backd's error codes.
- **Not covered:** internal functions, the admin API, rules, and system collections.
- **Format:** OpenAPI 3.1, which uses JSON Schema 2020-12 directly, so schemas carry over without translation.
- **Access:**
  - `GET /v1/{realm}/_openapi.json` is readable by realm admins by default;
  - `openapi: { public: true }` in `realm.yaml` makes it public;
  - `backd openapi --realm <r>` always generates it from the local config.
- **CI:** generated clients for Dart, Kotlin and Swift (openapi-generator) are built against a sample realm, so the document stays usable by real generators.

### 7.2 Admin document

- **What:** `backd openapi --admin` and `GET /v1/{realm}/_admin/openapi.json`, readable by admins only.
- **Covers:** the admin API (users, keys, organizations, configuration, exports, jobs, functions) **and internal functions**, invocable by admins, with their input and output schemas. It's useful for internal tooling and for generating admin clients.

## 8. Drift

- **Fingerprint:** every generated file records the realm's config fingerprint and the generator version.
- **CI:** `backd types --check` and `backd openapi --check` fail when generated files differ from what the current config would produce. The docs include a ready-made CI step.
- **Development:** function types regenerate automatically, and app types get the stale-types warning (§6).
- **Production:** nothing is enforced at runtime. Server validation protects data regardless of client types.

## 9. Shelf (issue 23.7)

- **Adopting types:** the Shelf app uses `createClient<Realm>()` with generated types, and Shelf functions use `defineFunction`.
- **Drift check:** Shelf's CI runs `backd types --check`.
- **What the chapter shows:** adding a field to `schema.json`, seeing function types update live in dev mode, the app's stale-types warning, regenerating, and `--diff` in a pull request.

## 10. Documentation (issue 23.8)

- **Typing apps:** generating types, `createClient<Realm>()`, what's typed, CI checks.
- **Typing functions:** `defineFunction` in TypeScript and JavaScript, the registry, regeneration in dev mode.
- **OpenAPI:** what each document covers, access, generating clients for Dart, Kotlin and Swift with openapi-generator, and what generated clients don't do (auth flows with PKCE, realtime, caching) until hand-written clients exist.
- **Drift:** the CI step, `--diff` in reviews, and the development warning.
- **Mapping reference:** JSON Schema to TypeScript, with unsupported keywords listed.

## 11. Issues

| id | Issue | Depends on |
|---|---|---|
| 23.1 | Live config reload in development | — |
| 23.2 | The type generator | 23.1 |
| 23.3 | Types for functions | 23.2 |
| 23.4 | Types in the JavaScript client | 23.2, 4.7 |
| 23.5 | Per-realm OpenAPI | 23.2 |
| 23.6 | Admin OpenAPI with internal functions | 23.5 |
| 23.7 | Shelf adopts generated types | 23.3, 23.4 |
| 23.8 | Types and OpenAPI documentation | 23.6, 23.7 |

**When to build:** right after app configuration (22), before organizations. Later milestones extend the generator and OpenAPI for what they add, as the definition of done now says.

## 12. Alternatives considered

- **API-only delivery, or files only:** rejected for one generator used three ways.
- **A types file per function, or realm-wide types only:** rejected for a registry plus `defineFunction`.
- **A generated runtime client per realm, or hand-written type parameters:** rejected for type-only generation.
- **A public document with everything, or CLI only:** rejected for a client-facing public document, an admin document, and access settings.
- **CI checks only, or rejecting stale clients:** rejected for fingerprints, CI checks and development warnings.

## 13. Follow-ups

- Hand-written clients for Dart, Kotlin and Swift (auth, realtime, caching) on top of the generated ones.
- Schemas for organization claims and template data, so they get exact types.
- Production live reload (7.17) reusing 23.1.
- Typed SDK for the admin API in the CLI's own code.
