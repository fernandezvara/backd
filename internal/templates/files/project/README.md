# __REALM__

A [backd](https://github.com/fernandezvara/backd) config repository: `config/` is `CONFIG_DIR`, everything else runs or checks it. Created by `backd template project`. `AGENTS.md` explains the layout and rules to AI coding assistants working in this repo.

## Layout

```
config/__REALM__/
  realm.yaml
  _realm/                      the email, pages, push and compliance texts the realm shows
  __DATABASE__/
    posts/                     a sample collection: schema, rules, indexes
    _functions/
      deno.json
      lib/testing.js           a fake ctx for deno test (see below)
      stats/                   a sample function
        function.yaml
        index.ts
        index.test.ts
compose.yaml                   MongoDB, backd, the executor, egress and MinIO — released images, local dev
.github/workflows/backd.yml    "Backd config check": deno test + build + config check + rules test
.gitignore                     generated function artifacts (.build/, .backd/) stay out of git
AGENTS.md                      repo guide for AI coding assistants
```

## Develop locally

1. Create a `.env` file next to `compose.yaml` with the three secrets it needs — once; compose reads `.env` automatically on every run, so it survives new terminals and sessions (`.env` is gitignored):

   ```sh
   cat > .env <<EOF
   EXECUTOR_TOKEN=$(openssl rand -base64 32)
   CALLBACK_KEY=$(openssl rand -base64 32)
   EGRESS_KEY=$(openssl rand -base64 32)
   EOF
   ```

   The three are required: if one is missing, `docker compose` stops before starting anything and names it.

   On Docker (a rootful daemon, e.g. Docker Desktop) also add `DEV_UID=$(id -u)` and `DEV_GID=$(id -g)` to `.env`, so files backd generates into `config/` belong to you; on rootless podman the default already maps to your user.

2. Bring the stack up — nothing is built, all images are pulled releases:

   ```sh
   docker compose up
   ```

   backd listens on <http://127.0.0.1:8080> with `BACKD_DEV=true`: it follows `config/` live — save a change to a schema, `collection.yaml` or `realm.yaml` and it reloads in a moment; save a function and it's bundled again automatically (the `functions-build` one-shot only matters at `up`, since backd refuses missing bundles). It also runs the worker role (`serve --with-worker`), so async jobs and cron schedules work here.

   <http://127.0.0.1:8080/> itself is a small landing page, only while `BACKD_DEV=true`: the realms and databases currently loaded, links into the admin UI and each realm's OpenAPI, `/healthz`/`/readyz` and the docs — plus whatever `BACKD_DEV_LINKS` adds (MinIO console by default).

3. Create the first administrator, once the realm is provisioned:

   ```sh
   docker compose exec backd /backd bootstrap --realm __REALM__ --email ops@example.com
   ```

4. Test a function's own logic without any of the above — no MongoDB, no running backd:

   ```sh
   deno test config/__REALM__/__DATABASE__/_functions/
   ```

   `_functions/lib/testing.js` is a fake `ctx` (vendored from `clients/functions-testing` in the backd repository): `ctx.db` is an in-memory store shaped like the JS client. See `stats/index.test.ts` for the pattern, and its header comment for what it does and doesn't cover.

5. Call the sample function directly:

   ```sh
   backd functions invoke --function __REALM__/__DATABASE__/stats --url http://127.0.0.1:8080
   ```

## MinIO

The stack includes an S3-compatible store for realms' `storage:` (see the commented `storage:` block in `config/__REALM__/realm.yaml`). Dev-only defaults, overridable in `compose.yaml` or via env:

| Variable | Default | What it is |
|---|---|---|
| `MINIO_ROOT_USER` | `backd-dev` | MinIO admin user |
| `MINIO_ROOT_PASSWORD` | `dev-p4ssw0rd!` | MinIO admin password |

- Console: <http://127.0.0.1:9001> (sign in with the values above; create your bucket there)
- S3 API: <http://127.0.0.1:9000> from your browser (presigned URLs), `http://minio:9000` from backd
- Point a realm's `storage:` at it with `provider: minio`, `endpoint: http://minio:9000`, `public_endpoint: http://localhost:9000`, `download: presigned`, and `access_key`/`secret_key` as `secret:` references (`backd secret set`).

## CI

`.github/workflows/backd.yml` — **Backd config check** — runs on every push inside the released `backd-executor` image (pin a tag in the file to fix the version): `deno test` each function, `backd functions build` + `backd config check`, `backd rules test`. It builds and publishes nothing. It's deliberately named `backd.yml`, not `ci.yml`: that name stays free for your own pipeline.

## Before production

This local stack skips several things the [hardening checklist](https://fernandezvara.github.io/backd/docs/operate/checklist/) covers for a real deployment: TLS, `admin.allowed_networks`, real secrets management (`BACKD_SECRETS_KEY`, backed up separately from the database), rate limiting in front of `/v1/`, and monitoring. Read it before this goes anywhere but your machine. [Deploying config](https://fernandezvara.github.io/backd/docs/operate/deploying/) explains how a config image is packaged and promoted, and the [production reference](https://github.com/fernandezvara/backd/tree/main/deploy/production) is a complete, tested starting point.
