---
title: "18. App configuration"
description: "A maintenance banner every visitor sees, a switch that hides the export button and blocks the export function, changed by cron functions and administrators, cached by the client."
weight: 380
toc: true
---

Some things about Shelf should change **without a deploy**: a maintenance notice while the operators work, a feature that has to be switched off *now*. This chapter adds **app configuration**: a declared set of values that administrators and functions change at runtime, that apps read through one endpoint, and that rules can consult so a switch holds on the server too.

## Declare it

Create `config/shelf/app-config/schema.json`: a JSON Schema whose every value has a default, so the app always gets a complete configuration.

{{< example-file path="shelf/app-config/schema.json" >}}

And `config/shelf/app-config/rules.yaml`: who may read what. A key with no rule, on itself or an ancestor, can't be read by apps at all.

{{< example-file path="shelf/app-config/rules.yaml" >}}

The banner is public (`"true"`: even without a session); the `features` group needs a signed-in member. Restart `backd`: it refuses to start on a value without a default, an object that doesn't close its properties, or a rule for a path the schema doesn't declare.

## Read it in the app

```js
await backd.config.load(['maintenance', 'features'])
backd.config.getSync('maintenance.enabled', false)
backd.config.on('change', (paths) => render(paths))
```

The client keeps a copy per user, **fresh for the server's `cache_ttl`** (5 minutes unless `app_config.cache_ttl` in `realm.yaml` says otherwise) with no request at all; after that it serves the copy and revalidates it in the background with `If-None-Match`, which usually costs a `304` and no body. Signing out drops the copies, so the next person on the browser never sees what the previous one could read. The app shows the **maintenance banner** from `maintenance` and the **Export my assets** button from `features.export.enabled`, which a visitor can't read at all (the button isn't there signed out).

## A switch the server enforces

Hiding a button protects nothing: anyone can call the function. The `export` function puts the switch in its `invoke` rule, where the server reads it for each call:

{{< example-file path="shelf/main/_functions/export/function.yaml" >}}

With `features.export.enabled` off, the call is refused with `403` whatever the app shows. The same `appConfig("features.export.enabled")` works in collection rules, where it is a constant to the query, so a rule that depends on it is still a database filter.

## Change it

An administrator changes values with the CLI (or `PATCH /_admin/app-config`, or the client's admin API):

```
backd app-config set --realm shelf --key features.export.enabled --value false
backd app-config get --realm shelf
backd app-config history --realm shelf
backd app-config rollback --realm shelf --version c3
```

Each change is validated as a whole against the schema, applied atomically on the version you read (a concurrent change answers `412`), kept in the history with its actor and every path's old and new value, and audited. A rollback is itself a new version.

## A maintenance window is two cron functions

Time-based changes need nothing built in: scheduled functions with `config: write` do them, and the history names the function as the actor.

{{< example-file path="shelf/main/_functions/maintenance-open/function.yaml" >}}

{{< example-file path="shelf/main/_functions/maintenance-open/index.ts" >}}

`maintenance-close` is the same with `ctx.admin.config.reset(["maintenance"])`, which returns the group to its defaults. `backd functions invoke --function shelf/main/maintenance-open` runs one by hand, as the tour does.

## You should see

- Opening the window (by the function) shows the banner to a signed-out visitor, and closing it hides it again.
- Turning `features.export.enabled` off hides the **Export my assets** button for members and makes the `export` function answer `403`; turning it back on restores both.
- `backd app-config history` lists each change with `function:main/maintenance-open` or the administrator as the actor, and `rollback` restores an earlier state as a new version.
- A second read of the configuration with the `ETag` it returned answers `304`.

{{< hint style="note" >}}
Pushing a change to open pages instantly needs realtime updates, which come in a later milestone; until then a page sees a change within `cache_ttl`, or when it regains focus and the copy has expired.
{{< /hint >}}

The tour (`make shelf-tour`) checks all of it. Read [App configuration](../../configuration/app-config/) for the reference: the schema's limits, per-path rules, the admin API, the functions' `ctx.config` and the client's cache.
