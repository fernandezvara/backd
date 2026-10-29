---
title: "Configuration"
description: "Environment variables that control backd at runtime."
icon: "gear-six"
weight: 100
toc: true
---

All runtime configuration comes from environment variables. At startup `backd` checks all of them and reports every problem at once, then exits with a non-zero status if any are invalid.

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `CONFIG_DIR` | yes | — | Root of the realm/database/collection tree |
| `MONGO_URI` | yes | — | MongoDB connection string. MongoDB must run as a replica set (a single node is fine; see [MongoDB](../../operate/#mongodb)) |
| `HTTP_ADDR` | no | `:8080` | Listen address |
| `PROVISION_MODE` | no | `apply` | `apply` creates or updates collections, validators and indexes; `verify` only compares them and refuses to start on differences |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn` or `error` (case-insensitive) |
| `MAX_BODY_BYTES` | no | `1048576` | Maximum request body size, bytes or a [size](#sizes-and-units) such as `1MiB`; larger bodies get `413` |
| `BACKD_MAX_UPLOAD_BYTES` | no | `104857600` | Largest file a [proxy upload](../files/transfers/#uploading) may carry (100 MiB), bytes or a [size](#sizes-and-units). A field's own `max_size` applies as well; the smaller wins |
| `BACKD_IMAGE_MAX_PIXELS` | no | `40000000` | Most pixels (width × height) an image may have to get [versions](../files/file-fields/#image-versions): 40 megapixels. Read from the file's header, so a larger image is refused without being decoded. A field's `max_pixels` may only lower it, and a higher one stops startup |
| `BACKD_EXPAND_MAX_DEPTH` | no | `2` | Levels of [references](config-dir/#references) an `expand` follows: 1 or 2 |
| `BACKD_EXPAND_MAX_PATHS` | no | `5` | Entries of one `expand` parameter |
| `BACKD_EXPAND_MAX_ARRAY` | no | `100` | Most ids of one `many` reference that an expansion follows; the rest is returned as `truncated`. A reference's `max_expand` may only lower it, and a higher one stops startup |
| `BACKD_EXPAND_MAX_DOCS` | no | `2000` | Most expanded documents in one response; more is `400 expand_too_large`, before any partial data. The four values are logged at startup |
| `BACKD_ENV` | no | none | What this instance is for: `production`, `staging`… (1 to 32 characters of `a-z`, `0-9`, `-`, `_`). Only [imports](../../operate/administration/export-import/seeding-staging/) use it, to warn about a package that doesn't fit: one that isn't anonymized into `staging`, or an anonymized one into `production` |
| `BACKD_DEPLOYMENT_ID` | no | `default` | Names this backd deployment (1 to 32 characters of `a-z`, `0-9`, `-`, `_`). Only needed when several backd deployments share one MongoDB cluster: it keeps their [trigger dispatchers](../../operate/change-feed/#the-trigger-dispatcher) apart |
| `BACKD_TRIGGER_MAX_CHAIN` | no | `16` | How deep a chain of writes caused by [trigger](config-dir/#triggers) runs may go (1 to 64). A realm's `triggers.max_chain` may only lower it, and a higher one stops startup |
| `BACKD_AGGREGATE_TIME` | no | `5s` | The most time one [aggregation](config-dir/#aggregations) may run (100ms to 1m). A collection's `aggregate.limits.time` may only lower it, and a higher one stops startup |
| `BACKD_AGGREGATE_MAX_GROUPS` | no | `1000` | The most groups one aggregation may return (1 to 10000). A collection may only lower it |
| `BACKD_IMAGE_TIMEOUT` | no | `30s` | How long a worker may take to decode one image and make all its versions; over it, the versions fail with `timeout` |
| `BACKD_IMAGE_CONCURRENCY` | no | derived | How many images one worker process makes versions of at once. Derived from the container's limits: half the CPUs, and no more than fit in half the memory at 4 bytes per pixel of the largest allowed image (at least 1); the value in effect is logged at startup. See [sizing the worker](../files/file-fields/#sizing-the-worker) |
| `MONGO_OP_TIMEOUT` | no | `10s` | Deadline for the storage work of each document request; exceeding it returns `503` |
| `SHUTDOWN_TIMEOUT` | no | `15s` | How long in-flight requests may take to finish after `SIGTERM` |
| `TRUSTED_PROXIES` | no | none | Comma-separated IP addresses or CIDR networks of your reverse proxies; only they may set `X-Forwarded-For` (see [client addresses behind a proxy](../../operate/#client-addresses-behind-a-proxy)) |
| `PASSWORD_HASH_CONCURRENCY` | no | `GOMAXPROCS` (which follows a container's CPU limit), capped by half the memory limit / 64 MiB | Maximum password hashes running at once in one process (see [Users and passwords](../auth/users/#password-storage)) |
| `BACKD_URL` | when the realms send email or use sign-in providers | none | `backd`'s public address (`https://api.example.com`), the base of the links in emails and of the providers' [callback URLs](../auth/providers/). A realm's `email.public_url` replaces it. Startup stops, naming the realm, when it needs one and has neither |
| `BACKD_EXECUTOR_URL`, `BACKD_EXECUTOR_TOKEN`, `BACKD_EGRESS_KEY`, `BACKD_INTERNAL_ADDR`, `BACKD_CALLBACK_URL`, `BACKD_CALLBACK_KEY` | with functions | — (`:8081` for the address) | The functions executor and the internal listener: see [Running the executor](../../operate/functions-runtime/#running-the-executor) |
| `BACKD_SECRETS_KEY` (or `BACKD_SECRETS_KEY_FILE`) | with functions that declare secrets | — | The master key functions' [secrets](../functions/secrets/#storage-and-the-master-key) are encrypted under |
| `BACKD_ADMIN_API` | no | `true` | `false` makes this instance serve no `/v1/{realm}/_admin` route: they answer `404` like any unknown route. For public instances when an internal instance has the admin API (see [Production deployment](../../operate/production/#admin-api)) |
| `BACKD_EVENTS` | no | `true` | `false` stops this instance from reading MongoDB's change stream for [realtime](../../operate/change-feed/): set it on API replicas when dedicated instances serve subscriptions. Nothing is read until there is something to serve, so `true` costs an idle instance nothing |
| `BACKD_TRIGGERS` | no | `true` | `false` keeps this instance from holding the lease of the trigger dispatcher ([triggers](../../operate/change-feed/#the-trigger-dispatcher)); `false` on instances that only serve requests |
| `BACKD_REALTIME_MAX_SUBSCRIPTIONS_PER_STREAM` | no | `20` | Subscriptions one [realtime](../api/realtime/#limits) stream may hold. A realm may lower it |
| `BACKD_REALTIME_MAX_STREAMS_PER_USER` | no | `5` | Open streams per user or API key (anonymous callers: per address). A realm may lower it |
| `BACKD_REALTIME_MAX_STREAMS` | no | `10000` | Open realtime streams on this instance |
| `BACKD_REALTIME_BUFFER` | no | `262144` | Bytes a stream may have waiting to be sent (and at most 1,000 events) before it is reset with `slow_consumer` |
| `BACKD_REALTIME_MEMORY` | no | `268435456` | Bytes (or a [size](#sizes-and-units) such as `256MiB`) all streams' buffers may hold on this instance; over it, the largest buffers are reset |
| `BACKD_REALTIME_HEARTBEAT` | no | `25s` | How often an idle stream sends a comment so proxies keep it open (at least `1s`) |
| `BACKD_ADMIN_UI` | no | `false` | `true` serves the admin web interface at `/_ui/` when the binary was built with it (release images are); it needs the admin API on the same instance, or startup fails |
| `BACKD_ADMIN_UI_IDLE` | no | `30m` | how long the admin interface may sit untouched before it signs out and revokes its session |
| `BACKD_DEV` | no | `false` | `true` reloads the config and rebuilds function sources automatically as the files change (see [Dev mode](../functions/testing/#dev-mode)); `serve` refuses to start unless `HTTP_ADDR` is bound to localhost. Local development only |
| `BACKD_DEV_LINKS` | no | none | Extra links on the [dev-mode landing page](../functions/testing/#dev-mode) (only while `BACKD_DEV=true`), as `Label=URL` pairs separated by `;`, for the tools next to the API, for example `MinIO console=http://127.0.0.1:9001` |
| `BACKD_DEV_ANY_ADDR` | no | `false` | `true` lets `BACKD_DEV=true` run on an address that isn't localhost, for a container whose ports are published on the host's localhost only (the local [development stack](../../contribute/local-development/)). Never set it where anything else can reach `backd` |
| `WORKER_CONCURRENCY` | no | `10` | [Async jobs](../functions/jobs/) one worker process runs at once, on `backd worker` or `backd serve --with-worker` |
| `METRICS_ADDR`, `METRICS_TOKEN` | no | — | Where this process serves `/metrics`, and the bearer token it may require (see [Metrics](../../operate/metrics/)) |

`backd functions build` also reads `DENO`, the Deno binary to bundle with (default: `deno` on the `PATH`); `BACKD_DEV` reads the same variable. `backd executor` and `backd egress` have their own variables (see [Running the executor](../../operate/functions-runtime/#running-the-executor) and [Egress](../functions/network/#egress-the-network-allowlist-completed)); `backd worker` reuses `BACKD_EXECUTOR_URL`/`BACKD_EXECUTOR_TOKEN`/`BACKD_CALLBACK_URL`/`BACKD_CALLBACK_KEY` (see [Running the worker](../../operate/functions-runtime/#running-the-worker)).

The pages of this section show YAML in pieces: a snippet of `collection.yaml` assumes the schema fields, roles and secrets it names exist in the rest of the configuration. `backd config check` is the judge of a whole tree. The complete, tested trees are under `examples/config`.

Durations use Go syntax, for example `500ms`, `10s` or `1m30s`.

## Sizes and units

Every limit measured in bytes is written as a number and a unit, with no space between them: `512KiB`, `10MiB`, `1.5GB`. The units come in two families that differ by about 7% at `TB`/`TiB`:

| Family | Units | Each step is |
|---|---|---|
| Decimal | `B`, `KB`, `MB`, `GB`, `TB` | 1,000 times the one before: a `KB` is 1,000 bytes |
| Binary | `B`, `KiB`, `MiB`, `GiB`, `TiB` | 1,024 times the one before: a `KiB` is 1,024 bytes |

- **Both families are accepted** unless a field says otherwise. [`memory`](../functions/reference/) of a function takes binary units only, because the runtime counts in MiB.
- **Units are case-sensitive** and follow the number directly: `1mb`, `1 MiB` and `1Mib` are refused.
- **Decimals are fine** (`1.5MiB` is 1,572,864 bytes) as long as the result is a whole number of bytes: `0.1KiB` is refused.
- **Environment variables** that end in `_BYTES` or `_MEMORY` also take a bare number of bytes: `1048576` and `1MiB` are the same value.
- **A size is never negative** and has no sign, exponent or thousands separator. Each setting says its smallest value.

{{< hint style="tip" title="Best practice" >}}
Write binary units (`MiB`, `GiB`) for limits you compare with memory or what a bucket reports, and decimal units (`MB`, `GB`) when matching a provider's price list. What backd prints back (the admin interface and `backd storage usage`) uses binary units.
{{< /hint >}}

Example:

```sh
CONFIG_DIR=./config MONGO_URI='mongodb://localhost:27017/?replicaSet=rs0' backd serve
```
