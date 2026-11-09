---
title: "Direct mode: without a running backd"
description: "backd-server export and import work straight on MongoDB and CONFIG_DIR when the server is stopped or unreachable: what the master key is for, and how to read an old backup."
icon: "terminal"
weight: 40
toc: true
---

[`backd-server`](../../server/)'s `export` and `import` run the same export and import as the CLI's **in the command itself**, straight on MongoDB and `CONFIG_DIR`, with no running server: use them when the server is stopped or can't be reached, or to read a backup. They take the CLI's flags, plus these:

```
backd-server export --mongo-uri env:MONGO_URI --realm clinic --no-files --out ./clinic-offline
backd-server import --mongo-uri env:MONGO_URI --realm clinic --from ./clinic-offline
```

| Flag | Meaning |
|---|---|
| `--mongo-uri` | The MongoDB URI, or `env:<VAR>` / `file:<path>`; default `BACKD_MONGO_URI`, then `MONGO_URI` (`CONFIG_DIR` is the environment variable, as for `serve`) |
| `--master-key` | The secrets master key (`env:` / `file:`); default `BACKD_SECRETS_KEY` |
| `--storage-key`, `--storage-secret` | The storage credentials, instead of the realm's secrets (`env:` / `file:`) |
| `--public-storage-key`, `--public-storage-secret` | The same for the realm's [public storage](../../../providers/storage/#public-storage), if it has one |
| `--force` | Import while an instance serves the realm |

- **What it needs.** `CONFIG_DIR` is read to know the schemas, the file fields and the indexes; the functions' bundles are not built, so no Deno is needed. The realm must have authentication enabled. The command validates and writes exactly as the network path does, and nothing is provisioned or changed in MongoDB's structure.
- **The master key is not needed for data.** It **is** needed for two things: **files**, because the storage credentials are realm secrets (give `--storage-key` and `--storage-secret`, and the `--public-storage-*` pair for public files, instead, or leave files out with `--no-files`), and the **authentication data sealed by the realm** (authenticator secrets, push device tokens, Apple tokens) with `--include auth`: without `--master-key` they stay behind and the command says how many.
- **Refuses while the realm is served.** Every `backd-server serve` records a heartbeat in MongoDB (`backd___deployment.instances`, every 15 seconds); an offline import will not write while an instance that serves the realm was heard from in the last 45 seconds, because two writers could clash. `--force` writes anyway, with a warning. An export only reads and never refuses, but it is not a snapshot while the realm is being written.
- **Audited, as offline.** The records go into the realm's audit trail with the actor `cli:offline` and `offline: true`, like any other.
- **No credentials are asked for.** Whoever holds MongoDB and the configuration already holds more than any admin account: protect that access accordingly.

## Reading an old backup

You do not need a separate tool to read a `mongodump`: restore it into any MongoDB with `mongorestore`, point `--mongo-uri` at it and run `backd-server export` with the configuration the backup was made under. The result is an ordinary package you can inspect, import into another instance or hand to an analyst as CSV.
