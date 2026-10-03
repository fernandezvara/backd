# Production reference deployment

A tested reference for running backd in public: nginx with TLS, rate limits,
body size limits and correct client addresses; MongoDB as a single-node
replica set with authentication, a member key file and TLS; backd in `PROVISION_MODE=verify` as a least-privilege user, with a
one-shot provision job holding the admin credentials; encrypted backups.

```sh
./setup.sh                                    # certificates, key file and passwords (gitignored)
docker compose --env-file .env up -d --build
./test.sh                                     # end-to-end test on a separate copy
```

| File | Purpose |
|---|---|
| `compose.yaml` | The stack: mongo, provision (one-shot), backd, a worker, the functions executor and egress, Prometheus, nginx |
| `prometheus/` | Prometheus' scrape config and the sample alert rules |
| `nginx/backd.conf.template` | TLS, rate limits, body size, `X-Forwarded-For`, JSON errors |
| `ops/Dockerfile` | MongoDB tools plus the backd binary, for ops jobs |
| `ops/provision.sh`, `ops/grants.js` | `backd provision`, then per-collection grants for backd's user (and the backup user) |
| `ops/backup.sh`, `ops/restore.sh` | Encrypted backups (`mongodump` into age) and restores; Compose profile `ops` |
| `functions/netprobe/` | A function-only realm this deployment's own test calls, to prove the executor's network isolation from a real function |
| `setup.sh` | Generates `secrets/` and `.env` |
| `test.sh`, `test.compose.yaml` | The end-to-end test (`make prod-test`, run in CI) |

The documentation explains each part and has a hardening checklist:
Operations → Production deployment (`docs/content/docs/operations/production.md`)
and Operations → Backup and restore (`docs/content/docs/operations/backup.md`).
