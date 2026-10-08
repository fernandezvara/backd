# __REALM__

A [backd](https://github.com/fernandezvara/backd) config repository: `config/` is `CONFIG_DIR`, everything else builds, tests and deploys it. Created by `backd template project`.

## Layout

```
config/__REALM__/
  realm.yaml
  __DATABASE__/
    posts/                     a sample collection: schema, rules, indexes
    _functions/
      deno.json
      lib/testing.js           a fake ctx for deno test (see below)
      stats/                   a sample function
        function.yaml
        index.ts
        index.test.ts
Dockerfile                     the artifact this project deploys: backd + config
Dockerfile.executor            the functions executor's image
compose.yaml                   MongoDB, backd, the executor and egress, for local dev
.github/workflows/ci.yml       build, check, package on every push and tag
```

## Develop locally

1. Set the three secrets `compose.yaml` needs (once):

   ```sh
   export EXECUTOR_TOKEN=$(openssl rand -base64 32)
   export CALLBACK_KEY=$(openssl rand -base64 32)
   export EGRESS_KEY=$(openssl rand -base64 32)
   ```

2. After changing a function's code, bundle it — `backd` refuses stale bundles:

   ```sh
   CONFIG_DIR=./config backd functions build
   # without Deno or backd installed locally:
   docker run --rm -v "$PWD/config:/config" -v "$PWD/backd:/usr/local/bin/backd:ro" \
     -e CONFIG_DIR=/config docker.io/denoland/deno:alpine-2.9.7 backd functions build
   ```

3. Bring the stack up:

   ```sh
   docker compose up --build
   ```

   `backd` listens on <http://127.0.0.1:8080>, and also runs the worker role (`serve --with-worker`), so async jobs and cron schedules work here. After changing `realm.yaml`, a schema or the rules in `collection.yaml`, repeat with `docker compose up -d --build backd`. To skip the rebuild-per-change loop for function code specifically, run `backd` locally (not in the stack) with `BACKD_DEV=true` — see the [functions docs](https://fernandezvara.github.io/backd/docs/functions/testing/#dev-mode).

4. Create the first administrator, once the realm is provisioned:

   ```sh
   docker compose exec backd /backd bootstrap --realm __REALM__ --email ops@example.com
   ```

5. Test a function's own logic without any of the above — no MongoDB, no running backd:

   ```sh
   deno test config/__REALM__/__DATABASE__/_functions/
   ```

   `_functions/lib/testing.js` is a fake `ctx` (vendored from `@backd/functions-testing`, not yet published): `ctx.db` is an in-memory store shaped like the JS client. See `stats/index.test.ts` for the pattern, and its header comment for what it does and doesn't cover.

6. Call the sample function directly:

   ```sh
   backd functions invoke --function __REALM__/__DATABASE__/stats --url http://127.0.0.1:8080
   ```

## CI: build, check, package

`.github/workflows/ci.yml` runs on every push: `deno test`s each function, bundles them, checks the config (`backd config check`), and publishes an image named after the commit to `ghcr.io`. Set `IMAGE` in the workflow to your own `ghcr.io/<org>/<repo>` first.

## Promoting and rolling back

1. **Tag** a commit (`v12`). CI publishes that commit's image under the tag — nothing is rebuilt.
2. **Staging:** run `backd provision` with the tagged image and an admin `MONGO_URI`, then roll out instances with `PROVISION_MODE=verify`.
3. **Production:** the same two steps with the same image.
4. **Rollback:** deploy the previous tag the same way — provision, then roll out.

`backd` refuses to start in `verify` mode against a config that wasn't provisioned, or that differs from what was: see [Deploying config](https://fernandezvara.github.io/backd/docs/operations/deploying/) for the full mechanism (`backd config fingerprint`, `/readyz`, what backd checks).

## Before production

This local stack skips several things the [hardening checklist](https://fernandezvara.github.io/backd/docs/operations/checklist/) covers for a real deployment: TLS, `admin.allowed_networks`, secrets management (`BACKD_SECRETS_KEY`, backed up separately from the database), rate limiting in front of `/v1/`, and monitoring. Read it before this goes anywhere but your machine. The [production reference](https://github.com/fernandezvara/backd/tree/main/deploy/production) is a complete, tested starting point.
