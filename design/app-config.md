# Design: app configuration

- **Issues:** features 22.1–22.8 (milestone `22-app-config`)
- **Status:** decided
- **Related designs:** [conditional-get.md](./conditional-get.md) (`ETag`s and `304`), [organizations.md](./organizations.md) (claims, roles, teams), [triggers-and-realtime.md](./triggers-and-realtime.md) (live updates), [internal-functions.md](./internal-functions.md) (cron functions)

## 1. The idea

One consistent, realm-level configuration for client apps (feature flags, maintenance switches, promotions, limits), instead of every app inventing a collection for it.

- **Declared like collections:** a JSON Schema describes the keys, types and defaults, and a `rules.yaml` decides who can read what, in the same expr language.
- **Changed at runtime by admins and functions only.** Every change is validated, atomic, audited, kept in history and reversible.
- **Read by apps through one endpoint,** whole or by branch. The JS client caches per user with an expiry, revalidates cheaply, and can follow changes live.
- **Nothing new for time:** maintenance windows and promotions are the developer's own cron functions.
- **No secrets, so no encryption:** whoever its rules allow can read it, and encrypting it in the browser would protect nothing, since the key would sit next to the data.

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Declaration | `<realm>/app-config/schema.json` (JSON Schema, defaults on every leaf, `additionalProperties: false`) |
| Visibility | `<realm>/app-config/rules.yaml`: `read` rules per path, ANDed with ancestors; default deny |
| Values | stored in `<realm>___system.app_config`; one value per key for the realm |
| Targeting | none built in; organization claims, roles and teams, and app collections cover it (§9) |
| Scheduling | none built in; cron functions |
| Writing | admins (`PATCH`, CLI) and functions with `config: write`; validated whole, atomic, `If-Match`, audited, history, rollback |
| Reading | `GET /v1/{realm}/_config[?paths=…]`, readable keys only, defaults filled, `ETag`, `cache_ttl` |
| Client | `load`, `get` (fetching when needed), cache per user with expiry and background revalidation, cleared on sign-out |
| Live updates | a realtime `_config` subscription |
| Rules | `appConfig("path")` usable in collection rules |

## 3. Declaring (issue 22.1)

```
<realm>/app-config/
  schema.json
  rules.yaml
```

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "maintenance": {
      "type": "object", "additionalProperties": false,
      "properties": {
        "enabled": { "type": "boolean", "default": false, "description": "Show the banner and block checkout" },
        "message": { "type": "string", "maxLength": 500, "default": "" }
      }
    },
    "features": {
      "type": "object", "additionalProperties": false,
      "properties": {
        "reports": {
          "type": "object", "additionalProperties": false,
          "properties": {
            "enabled":   { "type": "boolean", "default": true },
            "max_range": { "type": "string", "pattern": "^[0-9]+d$", "default": "365d" }
          }
        }
      }
    },
    "beta": {
      "type": "object", "additionalProperties": false,
      "properties": { "new_editor": { "type": "boolean", "default": false } }
    }
  }
}
```

```yaml
# rules.yaml
read:
  maintenance: "true"            # anyone, without a session
  features: user != nil          # signed-in users
  beta: hasRole("beta")
```

**The schema**
- **Keys are paths** (`features.reports.enabled`), and a **subset is a branch** (`features`).
- **Every object** declares `additionalProperties: false`, so undeclared keys can't be set.
- **Every leaf has a `default`,** or allows `null`, so clients always receive a complete configuration.
- **Validated** with backd's existing JSON Schema validator, the same as collections.
- **Size:** the whole configuration is at most 64 KiB.

**Rules**
- **The language** is the same expr language as collection rules, with `user`, `hasRole`, organization helpers, `hasConsent` and `now`. There's no `doc`.
- **Combining:** a path's effective rule is its own rule ANDed with every ancestor's rule, so deeper paths can only narrow.
- **Default deny:** a path with no rule on itself or any ancestor can't be read by clients, only by functions and admins.
- **No write rules:** writing is fixed to admins and functions.

**Startup checks**
- the schema compiles, every object has `additionalProperties: false`, and every leaf has a valid default or allows `null`;
- `rules.yaml` paths exist in the schema, and rules compile;
- no default contains a `secret:` reference;
- the defaults fit the size cap.

Both files are part of the config fingerprint.

## 4. Values, changes and history (issue 22.2)

- **Storage:** values that differ from their defaults are stored in `<realm>___system.app_config` with a configuration **version**.
- **Changing:**
  ```
  PATCH /v1/{realm}/_admin/app-config
  If-Match: "<version>"
  { "set": { "maintenance.enabled": true, "maintenance.message": "Back at 10:00" },
    "reset": ["features.reports"] }
  ```
  - the result is validated as a whole against the schema and applied atomically;
  - `reset` returns paths to their defaults;
  - a stale version answers `412`;
  - the answer is the new version.
- **Who:**
  - full admins, with no reauth because these are routine operational changes, but always audited;
  - read-only admins can read everything through `GET /v1/{realm}/_admin/app-config`, including keys clients can't see, along with the history.
- **CLI:** `backd app-config get|set|reset|history|rollback --realm <r>`, for example `backd app-config set --key maintenance.enabled --value true`.
- **History:** every change stores the version, the actor (admin or function), the time, and the changed paths with old and new values. The last 100 versions or 90 days are kept, whichever is longer.
- **Rollback:** `backd app-config rollback --version <version>` and `POST /v1/{realm}/_admin/app-config/rollback` restore a previous state as a new change.
- **Schema changes:** values that no longer validate after a deploy are reported at startup and in `GET /v1/{realm}/_admin/app-config`, and fall back to their defaults until fixed or reset. A deploy never breaks clients.

## 5. Reading (issue 22.3)

```
GET /v1/{realm}/_config
GET /v1/{realm}/_config?paths=features,maintenance
```

```json
{
  "values": { "maintenance": { "enabled": false, "message": "" }, "features": { "reports": { "enabled": true, "max_range": "365d" } } },
  "version": "c41",
  "cache_ttl": 300
}
```

- **Only readable keys,** with defaults filled in. Hidden paths are absent, with no error and no hint.
- **`paths`** selects branches. Unknown paths answer `400 invalid_path`; paths the caller can't read are silently absent.
- **`ETag`:** a weak `ETag` over the returned values, so `If-None-Match` gives `304` ([conditional-get.md](./conditional-get.md)).
- **`cache_ttl`:** from `realm.yaml`, `app_config.cache_ttl` (default 5 minutes).
- **Headers:**
  - anonymous requests whose result contains only paths whose effective rule is literally `"true"` get `Cache-Control: public, max-age=<cache_ttl>` with `Vary: Authorization`, so CDNs can serve them;
  - everything else is `private, no-cache`.

## 6. Server-side use (issue 22.4)

- **Functions:**
  - any function can read with `ctx.config.get(path)` (without rules) and `ctx.config.all()`;
  - functions declaring `config: write` in `function.yaml` get `ctx.admin.config.set({…})`, `reset([…])` and `history()`, listed at startup;
  - changes made by functions are recorded with the function as actor.
- **Rules:** collection rules can read configuration with `appConfig("features.reports.enabled")`. The value is fixed for the duration of a request, so pushdown treats it as a constant. This lets kill switches work on the server, not only in the UI.
- **Time-based changes** are cron functions. The docs include a maintenance-window recipe: two cron functions, or one with a check of `now`, that call `ctx.admin.config.set`.

## 7. JavaScript client (issue 22.5)

```js
await client.config.load(["features", "maintenance"]);   // or load() for everything readable
client.config.get("maintenance.enabled");                 // from cache; fetched first if not loaded
await client.config.get("beta.new_editor", { refresh: true });
client.config.on("change", (paths) => rerender(paths));
```

- **`load(paths?)`** fetches and caches. **`get(path)`** returns from the cache, fetching that path's branch first if it was never loaded. It returns a promise only in that case; `getSync(path)` returns the cached value or the default.
- **Cache:**
  - per realm and per user (or anonymous), in `localStorage` in browsers and in memory elsewhere, with the fetch time, `ETag` and `cache_ttl`;
  - **fresh** for `cache_ttl`, with no request;
  - **after expiry,** reads return the cached value immediately and revalidate in the background with `If-None-Match`, usually costing a `304`;
  - `refresh: true` forces a fetch;
  - storage access is wrapped so private browsing or blocked storage just falls back to memory.
- **Clearing:** on sign-out and user change, so another person on the same browser never sees the previous user's keys.
- **Typing:** a `backd types` follow-up can generate TypeScript types from `schema.json`. Until then `get` is untyped.

## 8. Live updates (issue 22.6)

- **With realtime enabled,** clients can subscribe to configuration: `client.config.live()` adds a `_config` subscription to the realtime stream.
- **On a change,** backd sends `config_changed` with the new version and changed paths, filtered by each subscriber's read rules. The client refetches those branches and emits `change`.
- **Changes reach open apps** within the realtime delay instead of `cache_ttl`.

## 9. What configuration is not

The docs make the boundaries explicit with a layered example.

| Question | Where it lives | Who changes it |
|---|---|---|
| Is the Reports module working at all right now? | app configuration `features.reports.enabled` | realm admins or functions (kill switch) |
| Did this customer pay for Reports? | organization claims (`features: ["reports"]`) | functions after payment, realm admins |
| Who inside the customer may use it? | organization role `analyst`, given through the team "Analytics" | the customer's managers |
| Does this person prefer the compact view? | an app collection (`preferences`) | the user |

The example shows all four combined in a collection rule:

```yaml
read: >
  appConfig("features.reports.enabled")
  && hasOrgRole(doc._meta.org, "analyst")
  && "reports" in orgClaims(doc._meta.org).features
```

It also shows them combined in the client, and what happens on an incident, a downgrade and a team change.

**Not:**
- a secrets store (refused at startup where detectable, and documented);
- a content store (size cap);
- a per-user or percentage targeting system (backlog).

## 10. Shelf (issue 22.7)

- **Maintenance:** a public `maintenance` branch drives a banner, switched by a cron function that opens and closes a maintenance window.
- **Feature flag:** a `features.export.enabled` flag, readable by signed-in users, hides the export button and blocks the export function through `appConfig()`.
- **Cache:** the chapter shows the client cache, the `304` revalidation, and an instant change with realtime.
- **Tests:** Shelf code and tests are updated, and earlier chapters still pass.

## 11. Security considerations

- **Readable by design:** configuration is readable by whoever its rules allow, so no secrets belong in it. Defaults are checked for secret references, and the docs explain why there's no encryption.
- **Default deny** for clients, and rules ANDed down the tree.
- **Writes only by admins and declared functions,** validated, audited and reversible.
- **Caches** are per user, cleared on sign-out, and public only for branches whose rule is literally `true`.

## 12. Documentation (issue 22.8)

- **Guide:**
  - declaring keys with JSON Schema, rules per path, reading and caching;
  - changing values, history and rollback;
  - functions and `appConfig()`;
  - live updates.
- **The layered example** (§9), including organization claims and teams. That part needs organizations (14) and is added when they exist if this ships first.
- **Recipes:** a maintenance window with cron functions, a promotion switched by a function, a kill switch enforced in rules.
- **Why no secrets and no encryption.**
- **References:** `schema.json` and `rules.yaml` for configuration, the admin API, the CLI, the client and OpenAPI.

## 13. Issues

| id | Issue | Depends on |
|---|---|---|
| 22.1 | Declaring app configuration | — |
| 22.2 | Values, changes and history | 22.1 |
| 22.3 | Reading app configuration | 22.2, 20.1 |
| 22.4 | App configuration in functions and rules | 22.3 |
| 22.5 | App configuration in the JavaScript client | 22.3, 4.7 |
| 22.6 | Live configuration updates | 22.5, 27.13 |
| 22.7 | Shelf maintenance banner and feature flag | 22.4, 22.5 |
| 22.8 | App configuration documentation | 22.7 |

**When to build:** right after conditional GET (20). 22.6 follows realtime (27.13).

## 14. Alternatives considered

- **Values only in config files, or a free-form JSON document:** rejected for a declared schema with runtime values.
- **A custom type system in YAML:** replaced by JSON Schema, reused from collections.
- **Visibility words (`public`, `signed_in`):** replaced by ordinary read rules.
- **Overrides per organization, or full targeting:** rejected for now; claims, roles and teams cover the needs.
- **Built-in scheduled values:** rejected; cron functions keep the logic with the developer.
- **Memory-only caching, or long caching without revalidation:** rejected.
- **Whole-document replacement, or no history:** rejected.
- **Encrypting the browser cache:** rejected because it would protect nothing.

## 15. Follow-ups

- Percentage rollouts and per-organization overrides, if apps need them.
- Generated TypeScript types for configuration (with `backd types`).
- Admin UI: a configuration editor with validation, history and rollback.
