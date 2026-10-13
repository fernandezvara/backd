---
title: "App configuration"
description: "Feature flags, maintenance switches and limits for your apps: declared with JSON Schema, changed by admins and functions, read with caching."
icon: "tune"
weight: 215
toc: true
---

App configuration is one realm-level configuration for your client apps: feature flags, maintenance switches, promotions, limits. It is declared like collections (a JSON Schema and read rules), changed at runtime by administrators and functions only, and read by apps through one endpoint with caching.

It is not a secrets store (it is readable by whoever its rules allow, and sits in the browser's cache), not a content store (the whole configuration is at most 64 KiB), and not a targeting system: there are no per-user or percentage rollouts. Time-based changes are your own [cron functions](../../functions/cron/).

## Declaring

A realm's `app-config/` folder holds `schema.json` and `rules.yaml`; see [Data model](../config-dir/#app-configuration) for the format and the startup checks. A short example:

```yaml
# rules.yaml
read:
  maintenance: "true"                  # anyone, without a session
  features: user != nil                # signed-in users
  beta: hasRole(user, "beta")
```

## Reading

Apps read it with `GET /v1/{realm}/_config` (optionally `?paths=features,maintenance`): only the keys the caller's rules allow, defaults filled in, hidden keys absent without a hint.

- **`ETag` and `304`:** the weak `ETag` covers the returned values, so a client revalidating with `If-None-Match` pays a `304`.
- **`cache_ttl`:** the seconds a client may use its copy without asking, set by `app_config.cache_ttl` in [`realm.yaml`](../realm/) (5 minutes by default).
- **Public caching:** an anonymous request whose keys are all readable by a rule that is literally `"true"` is `Cache-Control: public, max-age=<cache_ttl>` with `Vary: Authorization`, so a CDN can serve it. Everything else is `private, no-cache`.

## Changing values

Administrators with write access to the `config` area change values through the [admin API](../../auth/admin/#app-configuration) or `backd app-config set`. A change is validated as a whole against the schema and applied atomically, based on the version you read (`If-Match`), and kept in a history you can roll back.

```
backd app-config set --realm shop --key maintenance.enabled --value true
backd app-config history --realm shop
backd app-config rollback --realm shop --version c4
```

Values that stop validating after a deploy (a key's type changed, a key was removed) are reported at startup and by `backd app-config get`, and read as their defaults until fixed or reset: a deploy never breaks clients.

## From functions

Any function reads the whole configuration, with no read rules, through `ctx.config`:

```js
const enabled = await ctx.config.get("features.export.enabled");
const everything = await ctx.config.all();
```

A function that declares `config: write` in its `function.yaml` can change values with `ctx.admin.config.set({ "maintenance.enabled": true })`, `reset(["features.export"])` and `history(limit)`. Its changes are validated like an administrator's, recorded with the function as the actor, and the functions holding the grant are listed at startup. A value that doesn't fit throws a `FunctionError` (`validation_error`) with the failing paths.

## In rules

Collection rules read the configuration with `appConfig("features.export.enabled")`, so a kill switch holds on the server and not only in the UI:

```yaml
rules:
  create: user != nil && appConfig("features.export.enabled") == true
```

The key must be a string literal naming a declared value. The value is read once for the request, so it is a constant to the query: a rule that depends on it still becomes a database filter.

## Recipe: a maintenance window with cron functions

Two scheduled functions open and close a window, and the app shows the public `maintenance` branch:

```yaml
# open/function.yaml
mode: async
schedule: "0 2 * * 6"        # Saturday 02:00
config: write
```

```js
// open/index.ts
export default async (ctx) => {
  await ctx.admin.config.set({
    "maintenance.enabled": true,
    "maintenance.message": "Back at 04:00",
  });
};
```

```yaml
# close/function.yaml
mode: async
schedule: "0 4 * * 6"
config: write
```

```js
// close/index.ts
export default async (ctx) => {
  await ctx.admin.config.reset(["maintenance"]);
};
```

Both changes show in the history with the function as actor, so "who turned maintenance on" always has an answer.
