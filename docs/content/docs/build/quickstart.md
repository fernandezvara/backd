---
title: "Quickstart"
description: "From zero to a running realm in a few commands: get backd, generate a project from the template, and run its compose stack locally."
icon: "play"
weight: 50
toc: true
---

This page is the shortest honest path to a `backd` you can build against: download the binary, generate a project, run it with Docker, call it.

## 1. Get backd

Each tagged version is published automatically:

- binaries for Linux, macOS and Windows (amd64 and arm64), with a `checksums.txt`, on the repository's [GitHub Releases](https://github.com/fernandezvara/backd/releases) page;
- a container image for linux/amd64 and linux/arm64: `ghcr.io/fernandezvara/backd:<version>`, and `latest` for the newest stable version.

`backd version` prints what you got — from the binary, or from the image, whose entrypoint *is* `backd`:

```sh
docker run --rm ghcr.io/fernandezvara/backd:v0.8.5 version
```

Checksums, SBOMs and signature verification are an operations concern: [Deploying config → Verifying a release](../../operate/deploying/#verifying-a-release).

## 2. Start a project

`backd template project` creates a complete config repository in one step — a realm with a sample database (a `posts` collection and a `stats` function with tests), Dockerfiles, a local development stack and a CI workflow:

```sh
backd template project --dir ./my-app --realm demo
cd my-app
```

```
config/demo/
  realm.yaml
  main/
    posts/                 a sample collection: schema, rules, indexes
    _functions/stats/      a sample function, with a deno test
Dockerfile                 the artifact to deploy: backd + your config
Dockerfile.executor        the functions executor's image
compose.yaml               MongoDB, backd, the executor and egress
.github/workflows/ci.yml   test, check and publish on every push
```

`<directory>` must not exist yet: the command creates the whole tree. For smaller pieces instead — a realm or a database inside a `CONFIG_DIR` you already have — see [Starting from a template](../configuration/config-dir/#starting-from-a-template).

## 3. Run it

The generated `compose.yaml` runs MongoDB 8.2 (a single-node replica set), `backd serve --with-worker` (the API plus the worker that runs async jobs and schedules), the functions executor and egress.

**Docker Desktop** (macOS, Windows): it ships the Compose plugin, so the same commands below work in any terminal where `docker` is on the path. Once started, the stack appears in the Dashboard under **Compose** — one project per `compose.yaml` — where you can watch each service's logs, restart a service or stop the whole stack. On Linux, plain Docker Engine plus the Compose plugin does the same.

Set the three secrets the stack needs (once per shell):

```sh
export EXECUTOR_TOKEN=$(openssl rand -base64 32)
export CALLBACK_KEY=$(openssl rand -base64 32)
export EGRESS_KEY=$(openssl rand -base64 32)
```

Then bring it up:

```sh
docker compose up --build
```

`backd` listens on <http://127.0.0.1:8080>. Any `backd` command also runs in the stack: `docker compose exec backd /backd <command>` inside the running API container, or `docker compose run --rm backd <command>` for a one-off container (`docker compose run --rm backd version` needs the three secrets set, like `up`). Create the realm's first administrator (it asks for a password):

```sh
docker compose exec backd /backd bootstrap --realm demo --email me@example.com
```

Sign in with the CLI — the template's `admin` role requires a second factor, so the first login enrolls an authenticator app — and create an API key for calls:

```sh
backd login --realm demo --url http://127.0.0.1:8080 --email me@example.com
KEY=$(backd apikey create --realm demo --name local --expires 1d)
```

Create, read, update, query and delete a document:

```sh
curl -X POST http://127.0.0.1:8080/v1/demo/main/posts \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"title": "Hello", "published": true}'
# → 201 {"id":"darf3h9ck12g00eg2hh0","_meta":{...},"title":"Hello",...}

curl -G http://127.0.0.1:8080/v1/demo/main/posts -H "Authorization: Bearer $KEY" \
  --data-urlencode 'where={"title": {"$ilike": "hel%"}}' \
  --data-urlencode 'order_by=-title' -d count=true
```

Call the sample function:

```sh
backd functions invoke --function demo/main/stats --url http://127.0.0.1:8080
```

The generated `README.md` in the project covers the same steps, plus promoting to staging and rolling back.

### The iterate loop

| You changed… | Do |
|---|---|
| `realm.yaml`, a schema, `collection.yaml` rules | `docker compose up -d --build backd` |
| A function's code | `CONFIG_DIR=./config backd functions build`, then rebuild `backd` — or run `backd` locally with `BACKD_DEV=true` and skip the rebuilds (see [Build, dev and test](../functions/testing/#dev-mode)) |
| Nothing: just testing a function's logic | `deno test config/demo/main/_functions/` — no MongoDB, no running backd |

## 4. Without Docker

`backd` needs MongoDB 8.2 as a replica set; a single node is fine ([Operate → MongoDB](../../operate/#mongodb) shows how to start one). Then the binary alone is enough:

```sh
mkdir config && export CONFIG_DIR=$PWD/config
backd template realm --realm demo --sample
export MONGO_URI='mongodb://localhost:27017/?replicaSet=rs0'   # a single-node replica set
backd provision
backd bootstrap --realm demo --email me@example.com          # the first administrator; asks for a password
HTTP_ADDR=127.0.0.1:9090 backd serve &
backd login --realm demo --url http://127.0.0.1:9090 --email me@example.com
KEY=$(backd apikey create --realm demo --name local --expires 1d)
curl http://127.0.0.1:9090/v1/demo/main/posts -H "Authorization: Bearer $KEY"
```

`backd` itself serves plain HTTP (`:8080` by default; `9090` here); put TLS in front of it as in the [production reference](../../operate/production/).

## Commands you will use

`backd help` groups its commands by who uses them. The **develop** group is the one an application developer reaches for:

| Command | Purpose |
|---|---|
| `backd template project\|realm\|database\|function\|email-capture` | Create a config tree, or a piece of one (see [Starting from a template](../configuration/config-dir/#starting-from-a-template)) |
| `backd config check`, `backd config fingerprint` | Validate `CONFIG_DIR` as startup would, or print its fingerprint |
| `backd rules test` | Run each collection's `rules.test.yaml` without a database (see [Testing rules](../auth/rules/#testing-rules)) |
| `backd databases [--collections]` | Print the MongoDB databases (or collections) the config uses |
| `backd types`, `backd openapi` | Generate TypeScript types or an OpenAPI document from a realm (see [Clients](../clients/types/)) |
| `backd functions build\|invoke\|history\|logs\|jobs` | Bundle, call and inspect [server-side functions](../functions/) |

…and `backd bootstrap` and `backd login` for the first administrator and a session, as above. Administering a realm — users, API keys, organizations, secrets, the audit trail — is in [Command-line administration](../../operate/administration/cli/), which covers every command.

{{< hint note >}}
**What happens in the shadows.** The generated `compose.yaml` mirrors the repository's own development stack: same network shape (the executor can only reach `backd` and egress), `serve --with-worker`, dev-mode bundling. That stack — `make dev`, live config reload, Grafana, the local certificate authority — is for working on `backd` itself and is documented in [Contribute → Local development](../../contribute/local-development/).
{{< /hint >}}
