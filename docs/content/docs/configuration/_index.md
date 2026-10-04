---
title: "Configuration"
description: "Environment variables that control backd at runtime."
icon: "settings"
weight: 200
toc: true
---

All runtime configuration comes from environment variables. At startup `backd` checks all of them and reports every problem at once, then exits with a non-zero status if any are invalid.

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `CONFIG_DIR` | yes | — | Root of the realm/database/collection tree |
| `MONGO_URI` | yes | — | MongoDB connection string. MongoDB must run as a replica set (a single node is fine; see [MongoDB](../operations/#mongodb)) |
| `HTTP_ADDR` | no | `:8080` | Listen address |
| `PROVISION_MODE` | no | `apply` | `apply` creates or updates collections, validators and indexes; `verify` only compares them and refuses to start on differences |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn` or `error` (case-insensitive) |
| `MAX_BODY_BYTES` | no | `1048576` | Maximum request body size in bytes; larger bodies get `413` |
| `MONGO_OP_TIMEOUT` | no | `10s` | Deadline for the storage work of each document request; exceeding it returns `503` |
| `SHUTDOWN_TIMEOUT` | no | `15s` | How long in-flight requests may take to finish after `SIGTERM` |
| `TRUSTED_PROXIES` | no | none | Comma-separated IP addresses or CIDR networks of your reverse proxies; only they may set `X-Forwarded-For` (see [client addresses behind a proxy](../operations/#client-addresses-behind-a-proxy)) |
| `PASSWORD_HASH_CONCURRENCY` | no | `GOMAXPROCS` (which follows a container's CPU limit), capped by half the memory limit / 64 MiB | Maximum password hashes running at once in one process (see [Users and passwords](../auth/users/#password-storage)) |
| `BACKD_EXECUTOR_URL`, `BACKD_EXECUTOR_TOKEN`, `BACKD_INTERNAL_ADDR`, `BACKD_CALLBACK_URL`, `BACKD_CALLBACK_KEY` | with functions | — (`:8081` for the address) | The functions executor and the internal listener: see [Running the executor](../functions/running/#running-the-executor) |
| `BACKD_SECRETS_KEY` (or `BACKD_SECRETS_KEY_FILE`) | with functions that declare secrets | — | The master key functions' [secrets](../functions/secrets/#storage-and-the-master-key) are encrypted under |
| `BACKD_ADMIN_API` | no | `true` | `false` makes this instance serve no `/v1/{realm}/_admin` route: they answer `404` like any unknown route. For public instances when an internal instance has the admin API (see [Production deployment](../operations/production/#admin-api)) |
| `BACKD_ADMIN_UI` | no | `false` | `true` serves the admin web interface at `/_ui/`; it needs the admin API on the same instance, or startup fails (the interface itself is not part of this version yet) |
| `BACKD_DEV` | no | `false` | `true` rebuilds function sources automatically as they change (see [Dev mode](../functions/testing/#dev-mode)); `serve` refuses to start unless `HTTP_ADDR` is bound to localhost. Local development only |
| `BACKD_DEV_ANY_ADDR` | no | `false` | `true` lets `BACKD_DEV=true` run on an address that isn't localhost, for a container whose ports are published on the host's localhost only (the local docker stack). Never set it where anything else can reach `backd` |
| `WORKER_CONCURRENCY` | no | `10` | [Async jobs](../functions/jobs/) one worker process runs at once, on `backd worker` or `backd serve --with-worker` |

`backd functions build` also reads `DENO`, the Deno binary to bundle with (default: `deno` on the `PATH`); `BACKD_DEV` reads the same variable. `backd executor` and `backd egress` have their own variables (see [Running the executor](../functions/running/#running-the-executor) and [Egress](../functions/network/#egress-the-network-allowlist-completed)); `backd worker` reuses `BACKD_EXECUTOR_URL`/`BACKD_EXECUTOR_TOKEN`/`BACKD_CALLBACK_URL`/`BACKD_CALLBACK_KEY` (see [Running the worker](../functions/running/#running-the-worker)).

Durations use Go syntax, for example `500ms`, `10s` or `1m30s`.

Example:

```sh
CONFIG_DIR=./config MONGO_URI='mongodb://localhost:27017/?replicaSet=rs0' backd serve
```
