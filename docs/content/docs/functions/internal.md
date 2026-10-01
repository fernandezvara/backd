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

{{< hint style="tip" title="Best practice" >}}
Keep a function internal unless something outside needs to call it. `internal`, `calls`, `admin`, `secrets` and `network` together show in one place what each function may do.
{{< /hint >}}
