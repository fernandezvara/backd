---
title: "App configuration"
description: "Feature flags, maintenance switches and limits for your apps: declared with JSON Schema, changed by admins and functions, read with caching."
icon: "sliders-horizontal"
weight: 215
toc: true
---

App configuration is one realm-level configuration for your client apps: feature flags, maintenance switches, promotions, limits. It is declared like collections (a JSON Schema and read rules), changed at runtime by administrators and functions only, and read by apps through one endpoint with caching.

It is not a secrets store (see [No secrets, no encryption](#no-secrets-no-encryption)), not a content store (the whole configuration is at most 64 KiB), and not a targeting system: there are no per-user or percentage rollouts. Time-based changes are your own [cron functions](../../functions/cron/).

| You want | It is |
|---|---|
| to turn a module off for everyone, now | app configuration |
| to change a limit or a message without a deploy | app configuration |
| a per-customer plan or entitlement | your own data (an app collection), or [organization](../../auth/) claims once they exist |
| a user's own preference | an app collection the user owns |
| an API key or a password | a [secret](../../functions/secrets/) |

## Declaring

A realm's `_realm/config/` folder holds `schema.json` and `rules.yaml`. It needs a realm with auth enabled, because the values live in the realm's system database.

```json
{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "maintenance": {
      "type": "object", "additionalProperties": false,
      "properties": {
        "enabled": { "type": "boolean", "default": false, "description": "Show the banner" },
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
    }
  }
}
```

- **Keys are paths** in dot notation (`features.reports.enabled`), and a group of keys is a **branch** (`features`). Names are letters, digits, underscores and dashes.
- **Every object** sets `"additionalProperties": false` and declares its `properties`, so a key that isn't declared can't be set.
- **Every value** has a `default`, or lists `"null"` among its types, so clients always receive a complete configuration. A `default` on an object, `$ref`, `allOf`, `anyOf`, `oneOf` and the other composition keywords are not supported: declare keys as nested objects.
- **Validation** is the same JSON Schema draft 2020-12 as collections: `type`, `enum`, `minimum`, `maxLength`, `pattern`, `format`, arrays of values.
- **Limits:** the whole configuration, defaults included, is at most 64 KiB, and no default may be a `secret:` reference.

### Read rules per path

```yaml
# rules.yaml
read:
  maintenance: "true"                  # anyone, without a session
  features: user != nil                # signed-in users
  features.reports: hasRole(user, "analyst")
```

- **The language** is the one of [collection rules](../../auth/rules/): `user`, `hasRole(user, …)`, `hasConsent(user, …)` and `now`, with the realm's roles and consents. There is no `document` or `data`.
- **Combining:** a path's effective rule is its own rule ANDed with every ancestor's, so a deeper path can only narrow. In the example, `features.reports.enabled` needs a signed-in user *and* the `analyst` role.
- **Default deny:** a path with no rule on itself or on an ancestor can't be read by apps, only by functions and administrators.
- **No write rules:** writing is fixed to administrators and functions that declare `config: write`.

## Reading

Apps read it with `GET /v1/{realm}/_config` (optionally `?paths=features,maintenance`): only the keys the caller's rules allow, defaults filled in, hidden keys absent without a hint. A path that names no declared key or group is `400 invalid_path`.

- **`ETag` and `304`:** the weak `ETag` covers the returned values, so a client revalidating with `If-None-Match` pays a `304`.
- **`cache_ttl`:** the seconds a client may use its copy without asking, set by `app_config.cache_ttl` in [`realm.yaml`](../realm/) (5 minutes by default).
- **Public caching:** an anonymous request whose keys are all readable by a rule that is literally `"true"` is `Cache-Control: public, max-age=<cache_ttl>` with `Vary: Authorization`, so a CDN can serve it. Everything else is `private, no-cache`.

## Changing values

Administrators with write access to the `config` area change values through the [admin API](../../../operate/administration/admin/#app-configuration) or `backd app-config set`. A change is validated as a whole against the schema and applied atomically, based on the version you read (`If-Match`), and kept in a history you can roll back.

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

## Live updates

Without realtime, a page sees a change within `cache_ttl`, or when it calls `client.config.load()` after the copy expired, which revalidates for the cost of a `304`. Lower `app_config.cache_ttl` for switches that must reach people quickly: a request that answers `304` is cheap.

With [realtime](../../api/realtime/) available, a page can follow the configuration instead, and a change reaches open pages within the realtime delay:

```js
await client.config.load(["maintenance"]);
const stop = client.config.live(["maintenance"]);   // or live() for everything the user reads
client.config.on("change", (paths) => rerender(paths));
```

- **How it works:** the client adds a `_config` subscription to the realm's realtime stream (it needs no `realtime: true` collection). When the configuration changes, backd sends a [`config_changed` event](../../api/realtime/#following-the-configuration) with the new version and the keys that changed *for that subscriber*; the client reads those branches again with `If-None-Match` and `change` fires with the keys whose value differs.
- **Rules apply:** a subscriber hears only of the keys their rules let them read. A change to a key they can't read produces no event, and the answer to "what changed" never names a hidden key. Anonymous callers can follow the branches whose rule is `true`, such as a public `maintenance`.
- **Access changes count:** when a user's roles or consents change, what they read of the configuration can change too, and they are told.
- **Cost:** one extra subscription on a stream the app may already have, and one conditional read per change and per subscriber, spread over a short random delay so a change does not reach the database all at once.

## Layered example

Four different questions look alike and belong in four places:

| Question | Where it lives | Who changes it |
|---|---|---|
| Is the Reports module working at all right now? | app configuration, `features.reports.enabled` | administrators or functions (a kill switch) |
| Did this customer pay for Reports? | your data: a field of the customer's document, or organization claims when organizations exist | a function after payment, administrators |
| Who inside the customer may use it? | a role (`analyst`) in `realm.yaml`, granted by an administrator | administrators |
| Does this person prefer the compact view? | an app collection the user owns (`preferences`) | the user |

They combine in one collection rule, so each layer answers its own question and the server enforces all of them:

```yaml
rules:
  read: >
    appConfig("features.reports.enabled") == true
    && hasRole(user, "analyst")
    && document.plan in ["pro", "team"]
```

And in the client, the same layers decide what to show: `client.config.getSync("features.reports.enabled", false)` for the module, the signed-in user's roles for the menu, and the document's own fields for the plan. What happens on:

- **an incident:** set `features.reports.enabled` to `false`; every read of `reports` is refused by the rule at once, and apps hide the module within `cache_ttl`.
- **a downgrade:** change the customer's `plan`; nothing else moves.
- **a team change:** change the person's roles; the configuration is untouched.

## Recipes

### A maintenance window with cron functions

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

Both changes show in the history with the function as actor, so "who turned maintenance on" always has an answer. The [Shelf tutorial](../../../tutorial/18-app-config/) runs this recipe.

### A promotion switched by a function

A promotion is a few values and a function that decides when it is on. Declare `promo.active`, `promo.percent_off` and `promo.ends_at` (a string date), then one scheduled function that sets them from your own source, or from a collection of campaigns:

```js
export default async (ctx) => {
  const now = new Date().toISOString();
  const { items } = await ctx.admin.db("main").collection("campaigns").list({
    where: { starts_at: { $lte: now }, ends_at: { $gt: now } },
    limit: 1,
  });
  const campaign = items[0];
  await ctx.admin.config.set(
    campaign
      ? { "promo.active": true, "promo.percent_off": campaign.percent_off, "promo.ends_at": campaign.ends_at }
      : { "promo.active": false },
  );
};
```

Run it every few minutes. A change that leaves the values as they were makes no new version, so a function that runs often leaves the history alone.

### A kill switch enforced in rules

Hiding a button protects nothing: anyone can send the request. Put the switch in the rule that guards the operation, so the server refuses it:

```yaml
rules:
  create: user != nil && appConfig("features.checkout.enabled") == true
```

`appConfig("a.b")` takes the key as a string literal naming a declared value, or startup fails. The value is read once for the request, so it is a constant to the database filter: a read rule that depends on it still becomes a query. The same call works in a function's `invoke` rule.

## No secrets, no encryption

App configuration is readable by whoever its rules allow, so whatever is in it is as public as its rules say. Encrypting it in the browser protects nothing: the key would have to be shipped next to the data. That is why `backd`:

- refuses a `secret:` reference in a default at startup (what it can detect), and documents the rule here;
- keeps the cache per user and clears it on sign-out, so another person on the same browser never sees what the previous one could read;
- marks public branches (rules that are literally `"true"`) cacheable by CDNs, and everything else `private, no-cache`.

Keys for providers belong in [secrets](../../functions/secrets/), read by functions on the server.

## Reference

| What | Where |
|---|---|
| `_realm/config/schema.json`, `rules.yaml` | [Data model](../config-dir/#app-configuration) and above |
| `app_config.cache_ttl` | [Realm settings](../realm/) |
| `GET /v1/{realm}/_config` | [Reading](#reading) and the OpenAPI document (`readAppConfig`) |
| `GET`, `PATCH /_admin/app-config`, `/history`, `/rollback` | [Admin API](../../../operate/administration/admin/#app-configuration) |
| `backd app-config get\|set\|reset\|history\|rollback` | [Command line](../../../operate/administration/cli/) |
| `client.config` | [JavaScript client](../../clients/js/#app-configuration), [Dart client](../../clients/dart/#app-configuration) |
| `ctx.config`, `ctx.admin.config`, `config: write` | [From functions](#from-functions), [function.yaml](../../functions/reference/) |
| `appConfig("a.b")` | [Rules](../../auth/rules/) |
| `config:` in `rules.test.yaml` | [Testing rules](../../auth/rules/) |
