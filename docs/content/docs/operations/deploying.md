---
title: "Deploying config"
description: "Build CONFIG_DIR once from a tag of your config repository, deploy the same artifact everywhere, and let backd refuse instances that run anything else."
icon: "rocket_launch"
weight: 405
toc: true
---

Everything `backd` serves comes from `CONFIG_DIR`: realms, schemas, indexes, access rules and [function](../../functions/) code. Every instance of a deployment must run exactly the same config. If two instances ran different rules or different function code, the same request could be allowed on one and refused on the other.

So the config is treated like code:

- it lives in **one config repository** per deployment;
- CI builds it at **one pinned tag** into an **immutable artifact**: an image with `backd` and the config inside;
- staging and production run that same artifact;
- `backd` checks it (see [what backd enforces](#what-backd-enforces)).

## The config repository

`backd template project --dir <directory> --realm <realm>` scaffolds this layout in one step — a sample database and function with tests, both Dockerfiles, `compose.yaml` and the CI workflow below — so you don't have to assemble it by hand; see [Starting from a template](../../configuration/config-dir/#starting-from-a-template).

```
my-backd-config/
  config/                    CONFIG_DIR
    shop/
      realm.yaml
      orders/
        items/schema.json …
        _functions/          functions, with deno.json, deno.lock, lib/
  Dockerfile                 the artifact: backd + config
  compose.yaml               a local stack to develop against
  .github/workflows/ci.yml   build, check, package
```

The `Dockerfile` starts from a released `backd` image and adds the config. `backd config check` runs during the image build, so an invalid config never becomes an image:

```dockerfile
FROM ghcr.io/fernandezvara/backd:v0.6.0
COPY config /config
ENV CONFIG_DIR=/config
RUN ["/backd", "config", "check"]
```

## Developing locally

Run MongoDB (a single-node [replica set](../#mongodb)) and `backd` on your config, with `PROVISION_MODE=apply` so every restart applies your changes:

```yaml
# compose.yaml
services:
  mongo:
    image: docker.io/library/mongo:8.2
    command: ["--replSet", "rs0", "--bind_ip_all"]
    healthcheck:
      test: ["CMD", "mongosh", "--quiet", "--eval", "try { rs.status() } catch (e) { rs.initiate({_id: 'rs0', members: [{_id: 0, host: 'mongo:27017'}]}) } if (!db.hello().isWritablePrimary) quit(1)"]
      interval: 2s
      retries: 30
  backd:
    build: .
    ports: ["127.0.0.1:8080:8080"]
    environment:
      MONGO_URI: mongodb://mongo:27017/?replicaSet=rs0
    depends_on:
      mongo: { condition: service_healthy }
```

After changing schemas, rules or `realm.yaml`, restart `backd` (`docker compose up -d --build backd`). After changing a function, run `backd functions build` first: `backd` refuses stale bundles — or run `backd` locally (not in the container) with `BACKD_DEV=true` to rebuild on save, and `backd functions invoke` to call one directly; see [Dev mode](../../functions/testing/#dev-mode).

Before pushing, run the same checks CI will run:

```sh
CONFIG_DIR=./config backd functions build     # if you have functions
CONFIG_DIR=./config backd config check
```

## CI: build, check, package

On every push, CI builds the function bundles, checks the config and publishes an image named after the commit. A GitHub Actions example:

```yaml
name: config
on:
  push:
    branches: [main]
    tags: ["v*"]
env:
  BACKD_VERSION: 0.6.0
  IMAGE: ghcr.io/my-org/backd-config
jobs:
  build:
    runs-on: ubuntu-latest
    permissions: { contents: read, packages: write }
    steps:
      - uses: actions/checkout@v4
      - uses: denoland/setup-deno@v2
        with: { deno-version: 2.9.7 }   # the Deno this backd version bundles with
      - name: Install backd
        run: |
          curl -fsSL "https://github.com/fernandezvara/backd/releases/download/v${BACKD_VERSION}/backd_${BACKD_VERSION}_linux_amd64.tar.gz" | tar -xz backd
          sudo mv backd /usr/local/bin/
      - run: backd functions build
        env: { CONFIG_DIR: ./config }
      - run: backd config check
        env: { CONFIG_DIR: ./config }
      - uses: docker/login-action@v3
        with: { registry: ghcr.io, username: "${{ github.actor }}", password: "${{ secrets.GITHUB_TOKEN }}" }
      - name: Image for this commit
        run: |
          docker build -t "$IMAGE:${GITHUB_SHA::12}" .
          docker push "$IMAGE:${GITHUB_SHA::12}"
      - name: On a tag, the same image under the tag (no rebuild)
        if: startsWith(github.ref, 'refs/tags/')
        run: docker buildx imagetools create -t "$IMAGE:${GITHUB_REF_NAME}" "$IMAGE:${GITHUB_SHA::12}"
```

`backd config fingerprint` prints the config's fingerprint; CI can print it next to the image name, so you can match a running instance to its build.

## Promoting and rolling back

1. **Tag** a commit (`v12`). CI publishes the image of that commit under the tag. Nothing is rebuilt.
2. **Staging:** run the provision step with the tagged image and an admin `MONGO_URI` (`backd provision`), then roll out the instances with `PROVISION_MODE=verify`.
3. **Production:** the same two steps with the **same image**.
4. **Rollback:** deploy the previous tag the same way: provision, then roll out.

Provision before rolling out, every time. The [production reference](../production/) does it with its provision job.

## What backd enforces

`backd provision` (and `backd serve` in `apply` mode) records the config's fingerprint in MongoDB, for each realm it serves: in the `backd___deployment` database, one record per realm, so separate deployments sharing a MongoDB cluster keep their own.

Instances in `PROVISION_MODE=verify` compare their config's fingerprint with the one recorded for each of their realms, and **refuse to start** when it differs or none was recorded:

```
backd serve: the config differs from the one last provisioned: this instance's config is 7c1e…, but
  realm shop: provisioned 4a90… (at 2026-09-28T10:00:00Z by backd v0.6.0)
provision this config (`backd provision`) or deploy the config that was provisioned
```

Instances that are already running aren't stopped: during a rollout, old instances keep serving until they're replaced.

{{< hint warning >}}
An instance left on an **old tag** keeps serving until it restarts, and then refuses to start against the newly provisioned config. Roll every instance to the new tag before the old ones can restart (a crash, a node drain), and don't provision a config you are not ready to deploy everywhere.
{{< /hint >}}

- The fingerprint is a hash of every file `backd` reads from `CONFIG_DIR`: `realm.yaml`, each collection's `schema.json`, `indexes.json` and `rules.yaml`, and each functions project with its bundles. Files `backd` ignores (`.git`, a `README.md`) don't change it, so the same tag gives the same fingerprint on any machine.
- Every instance logs its fingerprint at startup (`"msg":"config loaded"`) and reports it in [`/readyz`](../../api/#health-endpoints): `{"status":"ready","config":"<fingerprint>"}`.
- `backd config fingerprint` prints it without starting anything.
- The database user of instances in `verify` mode needs to read `backd___deployment.realms`; `backd databases --collections` lists it with the others for your grants. Only the provision step writes it.
