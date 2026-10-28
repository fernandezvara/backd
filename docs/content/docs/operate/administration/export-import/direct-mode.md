---
title: "Direct mode: without a running backd"
description: "Export and import straight on MongoDB and CONFIG_DIR when the server is stopped or unreachable, what the master key is for, and how to read an old backup."
icon: "terminal"
weight: 40
toc: true
---

`--direct` runs the same export and import **in the command itself**, straight on MongoDB and `CONFIG_DIR`: use it when the server is stopped or can't be reached, or to read a backup.

```
backd export --direct --mongo-uri env:MONGO_URI --realm clinic --no-files --out ./clinic-offline
backd import --direct --mongo-uri env:MONGO_URI --realm clinic --from ./clinic-offline
```

| Flag | Meaning |
|---|---|
| `--direct` | Work on MongoDB and `CONFIG_DIR` (an environment variable, as for `serve`) |
| `--mongo-uri` | The MongoDB URI, or `env:<VAR>` / `file:<path>`; default `BACKD_MONGO_URI`, then `MONGO_URI` |
| `--master-key` | The secrets master key (`env:` / `file:`); default `BACKD_SECRETS_KEY` |
| `--storage-key`, `--storage-secret` | The storage credentials, instead of the realm's secrets (`env:` / `file:`) |
| `--public-storage-key`, `--public-storage-secret` | The same for the realm's [public storage](../../../providers/storage/#public-storage), if it has one |
| `--force` | Import while an instance serves the realm |

- **What it needs.** `CONFIG_DIR` is read to know the schemas, the file fields and the indexes; the functions' bundles are not built, so no Deno is needed. The realm must have authentication enabled. The command validates and writes exactly as the network path does, and nothing is provisioned or changed in MongoDB's structure.
- **The master key is not needed for data.** It **is** needed for two things: **files**, because the storage credentials are realm secrets (give `--storage-key` and `--storage-secret`, and the `--public-storage-*` pair for public files, instead, or leave files out with `--no-files`), and the **authentication data sealed by the realm** (authenticator secrets, push device tokens, Apple tokens) with `--include auth`: without `--master-key` they stay behind and the command says how many.
- **Refuses while the realm is served.** Every `backd serve` records a heartbeat in MongoDB (`backd___deployment.instances`, every 15 seconds); an offline import will not write while an instance that serves the realm was heard from in the last 45 seconds, because two writers could clash. `--force` writes anyway, with a warning. An export only reads and never refuses, but it is not a snapshot while the realm is being written.
- **Audited, as offline.** The records go into the realm's audit trail with the actor `cli:offline` and `offline: true`, like any other.
- **No credentials are asked for.** Whoever holds MongoDB and the configuration already holds more than any admin account: protect that access accordingly.

## Reading an old backup

You do not need a separate tool to read a `mongodump`: restore it into any MongoDB with `mongorestore`, point `--mongo-uri` at it and run `backd export --direct` with the configuration the backup was made under. The result is an ordinary package you can inspect, import into another instance or hand to an analyst as CSV.
