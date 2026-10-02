---
title: "function.yaml reference"
description: "Every function.yaml key, the invoke rules, and what backd enforces."
icon: "settings"
weight: 554
toc: true
---

## function.yaml

Every key is optional. `backd` reads the file strictly: an unknown key or an invalid value stops startup, naming the file.

| Key | Default | Meaning |
|---|---|---|
| `mode` | `sync` | `sync`: the caller waits for the output. `async`: the caller gets a [job](../jobs/) to check later. `webhook`: for callbacks from other services (see [Webhooks](../webhooks/)) — needs `invoke` set to a rule that allows anonymous callers |
| `timeout` | `10s` (`sync`, `webhook`), `15m` (`async`) | The function is stopped after this long. At least `1s`; at most `60s` for `sync` and `webhook`, `24h` for `async` |
| `memory` | `128MB` | Memory for the function's JavaScript heap, from `32MiB` to `4GiB` |
| `max_output` | `1MiB` | The largest output accepted, from `1KiB` to `64MiB` (`15MiB` for `async`) |
| `concurrency` | `10` | How many calls of the function may run at once **on each `backd` instance** (1–1000). With several instances, up to that many times this value run at once |
| `rate_limit` | none | Calls allowed per caller, counted across instances: `{per: user \| ip, limit: <n>, window: <duration>}`, window from `1s` to `24h` |
| `idempotency` | `optional` | Whether calls must send an `Idempotency-Key` header: `optional` or `required` |
| `invoke` | none | Who may call the function (see below). Without it, only [API keys](../../auth/api-keys/) may call it |
| `admin` | `false` | `true` gives the function full access to the realm's data, whatever the caller may do. Keep it off unless the function needs it |
| `internal` | `false` | `true` removes the function's HTTP route: `_func/<name>` answers `404` to everyone, like a function that doesn't exist. Only `backd` and scheduled runs may call it (see [Internal functions](../internal/)). Can't be combined with `invoke` or `mode: webhook` |
| `dev_only` | `false` | `true` makes `backd` refuse to start with the function unless `BACKD_DEV=true`: for development helpers such as the [email capture](../email/#developing-without-a-provider) function, which must never run in production |
| `email` | `false` | `true` gives the function `ctx.email.send()`: [custom emails](../email/#sending-email-from-functions) through the realm's templates and delivery function, to any recipient, within the caps of `email.limits`. Needs `email` in `realm.yaml`; read the warning on that page first |
| `calls` | none | The functions of the same database this one lists as callable (see [Internal functions](../internal/#declaring-which-functions-it-may-call)); startup rejects unknown names, cycles and chains longer than 4 functions |
| `retry` | none | Tries a failed `async` function again: `{attempts, backoff, max_backoff}` (see [Retrying a failed job](../jobs/#retrying-a-failed-job)). A single attempt unless `attempts` is above 1 |
| `schedule` | none | A cron expression: the function [runs on a schedule](../cron/). Needs `mode: async` and at least one running [worker](../running/#running-the-worker) |
| `secrets` | none | Secrets the function may read: `NAME` (its own database's) or `realm.NAME` (the realm's). Names are upper-case letters, digits and `_` |
| `network` | none | Hosts the function may call: `host` or `host:port`, lower case, no scheme, path or wildcard. Without a list, a function can only call `backd` |

Durations are written as in [`realm.yaml`](../../configuration/realm/) (`30s`, `15m`, `1d`). Sizes need a unit: `B`, `KB`, `MB`, `GB` (powers of 1000) or `KiB`, `MiB`, `GiB` (powers of 1024).

```yaml
mode: sync
timeout: 30s
memory: 256MB
invoke: "user != nil && hasRole(user, 'staff')"
secrets: [STRIPE_KEY]
network: [api.stripe.com]
```

### Invoke rules

`invoke` is an expression in the same language as [access rules](../../auth/rules/), with the same checks at startup. It sees only `user` (`nil` for anonymous callers) and `hasRole(user, 'role')`, with roles declared in `realm.yaml`; there is no document. Every use of `user`'s fields needs a guard for anonymous callers, as in rules.

| `invoke` | Who may call |
|---|---|
| none | API keys only |
| `"user != nil"` | any signed-in user (and API keys) |
| `"user != nil && user.email_verified"` | signed-in users with a verified email |
| `"hasRole(user, 'staff')"` | users with the `staff` role |
| `"true"` | anyone, including anonymous callers |

`invoke` only applies in realms with `auth: enabled`. In a realm with `auth: disabled`, anyone who can reach `backd` may call its functions, as with its data.

**`mode: webhook` requires `invoke` to allow anonymous callers** (e.g. `"true"`, or a rule that only checks the request itself, never `user`) — refused at startup otherwise. A webhook sender (Stripe, GitHub, and the like) has no `backd` session or API key to send; the function must be reachable without one, and prove the request is genuine itself (see [Webhooks](../webhooks/)).

## What is enforced today

| Setting | Today |
|---|---|
| `timeout` | Enforced: the process is stopped at the deadline, and when the caller goes away |
| `memory` | Enforced: the JavaScript heap, plus the process's resident memory (heap + about 130 MiB) |
| `max_output` | Enforced |
| `invoke`, `admin`, input and output schemas | Enforced |
| `network` | Enforced by the allowlist and `backd egress` together (see [above](../network/#egress-the-network-allowlist-completed)); the third layer, network placement, is proven by a test harness and wired into the [production reference](../../operations/production/#functions-executor-egress-and-worker) — reproduce it yourself if you assemble your own deployment instead |
| `secrets` | Enforced: values are encrypted at rest, admin-managed, cached briefly and delivered only as `ctx.secrets` (see [Secrets](../secrets/)) |
| `concurrency`, `functions.max_concurrency` | Enforced, per instance (see [Concurrency limits](../calling/#concurrency-limits)) |
| `schedule` | Enforced by workers: one run per scheduled time across any number of them (see [Scheduled functions](../cron/)) |
| `rate_limit` | Enforced, shared across instances (see [Rate limits](../calling/#rate-limits)) |
| `idempotency` | Enforced (see [Idempotency](../calling/#idempotency)) |
| `mode: async` | Enforced (see [Async jobs](../jobs/)) |
| `mode: webhook` | Enforced (see [Webhooks](../webhooks/)) |

Each function process also can't read environment variables, files other than its own bundle, start processes or use FFI, and never shares memory with another call.
