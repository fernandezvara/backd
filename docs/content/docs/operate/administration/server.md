---
title: "backd-server"
description: "backd-server runs the server roles (serve, worker, executor, egress, provision) and the tools that work on MongoDB or run Deno directly: bootstrap, functions build, offline export and import."
icon: "hard-drives"
weight: 506
toc: true
---

`backd-server` is the binary that runs backd: the server roles, and the tools that work on MongoDB or run Deno directly, with no running server in between. Everything else, the project tools and the commands that go through a realm's admin API, is the [`backd` CLI](../cli/). The rule is simple: **what opens MongoDB or runs Deno is `backd-server`**.

It runs on Linux only (the executor needs process limits and `/proc`), and it is what both images start: the `backd` image's entrypoint is `backd-server` with the command `serve`, and the `backd-executor` image's is `backd-server executor`. Both images also carry the `backd` CLI, so `docker compose exec backd backd whoami --realm blog` works where the server runs.

The conventions are the CLI's: [every value is a flag](../cli/#conventions), exit code `1` for a failure and `2` for a usage error, results on standard output and the rest on standard error. Configuration comes from the [environment](../../../build/configuration/#which-binary-reads-them), as for every role.

## Every command

`backd-server help` lists them in the same three groups, **Daemons**, **Server commands** and **General**; `backd-server <command> --help` shows a command's flags.

**Daemons**, the long-running roles, and `provision` (see [Deploying config](../../deploying/) and [Executor, workers and egress](../../functions-runtime/)):

| Command | Purpose |
|---|---|
| `backd-server serve [--with-worker]` | Load config, provision or verify MongoDB, serve HTTP; `--with-worker` also runs the worker role. With `BACKD_DEV=true` it builds the functions at startup and waits for a config that loads ([Dev mode](../../../build/functions/testing/#dev-mode)) |
| `backd-server provision [--disable-preimages --yes]` | Load config, apply provisioning once and exit |
| `backd-server worker` | Claim and run [async jobs](../../../build/functions/jobs/) and schedules; `WORKER_CONCURRENCY` caps how many run at once |
| `backd-server executor` | Run server-side functions (needs Deno: the `backd-executor` image) |
| `backd-server egress` | Run the egress role, the executor's only way out (see [Outbound network](../../../build/functions/network/)) |

**Tools that work on MongoDB or run Deno directly:**

| Command | Purpose |
|---|---|
| `backd-server functions build [--check]` | Bundle every function in `CONFIG_DIR` with Deno (see [Building](../../../build/functions/testing/#building)); in CI, run it in the `backd-executor` image, so the bundles are built with the Deno that runs them |
| `backd-server bootstrap --realm <realm> --email <email>` | Create a realm's [first administrator](../cli/#the-first-administrator), once per realm |
| `backd-server secret create\|rotate\|list --realm <realm>` | Create, rotate or list [field keys](../../../build/configuration/field-keys/) straight on MongoDB: how you create the key a field names when backd refuses to start without it, or restore keys after losing the secrets |
| `backd-server secret rotate-key` | Re-encrypt every realm's [secrets](../../../build/functions/secrets/) under a new master key (`BACKD_SECRETS_KEY` → `BACKD_SECRETS_NEW_KEY`) |
| `backd-server user mfa-reset --realm <realm> --email <email> --yes` | Break glass: remove the second factor of the only administrator of a realm who lost it, audited as `instance` ([When a user loses it](../../../build/auth/mfa/#when-a-user-loses-it)) |
| `backd-server export`, `backd-server import` | Export or import a realm's data with no running server ([Direct mode](../export-import/direct-mode/)) |
| `backd-server triggers rescan` | Re-emit a collection's recent changes to its triggers after a gap ([Gaps](../../change-feed/)); it may read a whole collection |
| `backd-server version` | Print the version |

## What they need

| Commands | Work on | Need |
|---|---|---|
| `serve`, `provision`, `worker` | MongoDB and `CONFIG_DIR` | `CONFIG_DIR`, `MONGO_URI`, and the role's settings |
| `executor`, `egress` | Neither | Their own settings, never `CONFIG_DIR` or MongoDB |
| `functions build` | `CONFIG_DIR` | Deno (`DENO`, or `deno` on the `PATH`) |
| `bootstrap`, `user mfa-reset`, `triggers rescan` | MongoDB, directly | `CONFIG_DIR`, `MONGO_URI`, a provisioned realm |
| `secret create\|rotate\|list`, `export`, `import` | MongoDB, directly, through the admin API's own code in this process | `CONFIG_DIR`, `MONGO_URI` (or `--mongo-uri`), and the master key for sealed data (`BACKD_SECRETS_KEY` or `--master-key`) |
| `secret rotate-key` | MongoDB, directly | `CONFIG_DIR`, `MONGO_URI`, `BACKD_SECRETS_KEY`, `BACKD_SECRETS_NEW_KEY` |

Run them where backd runs, with its environment: `docker compose exec backd backd-server bootstrap --realm blog --email ops@example.com`, or `kubectl exec` into a backd pod.
