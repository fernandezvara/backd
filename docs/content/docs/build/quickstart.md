---
title: "Quickstart"
description: "From zero to a running realm in a few commands: get backd, generate a project from the template, and run its compose stack locally."
icon: "play"
weight: 50
toc: true
---

This page is the shortest honest path to a `backd` you can build against: install the command line, generate a project, run it with Docker, call it.

**Quickstart or Tutorial?** This page gets one realm running and ends with a few `curl` calls. The [Tutorial](../../tutorial/) builds a whole application, chapter by chapter, on its own stack (it needs only Docker, not this setup), and is the better start if you want to learn by building.

## What you need

- **Docker with Compose** (Docker Desktop, or Docker Engine and the Compose plugin; rootless Podman with `podman-compose` also works). The first `up` pulls the images for MongoDB, MinIO and `backd`, so allow for a download; later runs start from the local copies.
- **The `backd` command line**, from the [release archives](https://github.com/fernandezvara/backd/releases) (Linux, macOS, Windows). The server itself runs in the containers.
- **A shell with `openssl` and `curl`** (on Windows, WSL2).
- **An authenticator app on your phone** (any TOTP app). The template's `admin` role requires a second factor, so the first `backd login` asks you to enroll one before you see a document. A development realm without that step is planned ([#889](https://github.com/fernandezvara/backd/issues/889)).

## The short version

From an empty directory. Two commands ask you for something: `backd-server bootstrap` asks for a new password, and `backd login` asks for that password and then a code from your authenticator app. `apikey create` asks for the password once more, as a fresh proof for a sensitive action.

```sh
backd template project --dir ./my-app --realm demo && cd my-app
cat > .env <<EOF
EXECUTOR_TOKEN=$(openssl rand -base64 32)
CALLBACK_KEY=$(openssl rand -base64 32)
EGRESS_KEY=$(openssl rand -base64 32)
EOF
docker compose up -d
until curl -sf http://127.0.0.1:8080/readyz >/dev/null; do sleep 2; done   # MongoDB takes a moment
docker compose exec backd backd-server bootstrap --realm demo --email me@example.com
backd login --realm demo --url http://127.0.0.1:8080 --email me@example.com
KEY=$(backd apikey create --realm demo --name local --expires 1d)
curl -X POST http://127.0.0.1:8080/v1/demo/main/posts \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"title": "Hello", "published": true}'
```

The last command answers `201` with the new document. On Docker (not rootless Podman) also add `DEV_UID` and `DEV_GID` to `.env`, as step 3 explains. If a command is refused, see [Troubleshooting](../../troubleshooting/).

The rest of the page explains each step, then the Docker-free setup and the commands you will use.

## 1. Get backd

Each tagged version is published automatically:

- binaries on the repository's [GitHub Releases](https://github.com/fernandezvara/backd/releases) page, with a `checksums.txt`: **`backd`**, the command line, for Linux, macOS and Windows (amd64 and arm64), and **`backd-server`**, which runs the server, for Linux. On your own machine you need `backd`; the stack below runs `backd-server` in containers;
- a container image for linux/amd64 and linux/arm64: `ghcr.io/fernandezvara/backd:<version>`, and `latest` for the newest stable version.

`backd version` prints what you got. The image's entrypoint is `backd-server` (it also carries `backd`):

```sh
docker run --rm ghcr.io/fernandezvara/backd:v0.9.9 version
```

Checksums, SBOMs and signature verification are an operations concern: [Deploying config → Verifying a release](../../operate/deploying/#verifying-a-release).

## 2. Start a project

`backd template project` creates a complete config repository in one step — a realm with a sample database (a `posts` collection and a `stats` function with tests), a local development stack on the released images (nothing is built), a check-only GitHub workflow and an `AGENTS.md` for AI coding assistants:

```sh
backd template project --dir ./my-app --realm demo
cd my-app
```

```
config/demo/
  realm.yaml
  _realm/                  email, pages, push and compliance texts the realm shows
  main/
    posts/                 a sample collection: schema, rules, indexes
    _functions/stats/      a sample function, with a deno test
compose.yaml               MongoDB, backd, the executor, egress and MinIO
.github/workflows/backd.yml  "Backd config check" on every push
AGENTS.md                  the repo guide AI coding assistants read
```

`<directory>` must not exist yet: the command creates the whole tree. For smaller pieces instead — a realm or a database inside a `CONFIG_DIR` you already have — see [Starting from a template](../configuration/config-dir/#starting-from-a-template).

## 3. Run it

The generated `compose.yaml` runs MongoDB 8.2 (a single-node replica set), `backd-server serve --with-worker` (the API plus the worker that runs async jobs and schedules) in dev mode, the functions executor, egress and MinIO. All backd services run the released `backd-executor` image — it ships Deno, which dev mode needs to re-bundle functions on save; nothing is compiled or built.

**Docker Desktop** (macOS, Windows): it ships the Compose plugin, so the same commands below work in any terminal where `docker` is on the path. Once started, the stack appears in the Dashboard under **Compose** — one project per `compose.yaml` — where you can watch each service's logs, restart a service or stop the whole stack. On Linux, plain Docker Engine plus the Compose plugin does the same.

Write the three secrets the stack needs to a `.env` file next to `compose.yaml` — once; compose reads it automatically on every run (and it's gitignored):

```sh
cat > .env <<EOF
EXECUTOR_TOKEN=$(openssl rand -base64 32)
CALLBACK_KEY=$(openssl rand -base64 32)
EGRESS_KEY=$(openssl rand -base64 32)
EOF
```

On Docker (a rootful daemon, e.g. Docker Desktop) also add `DEV_UID=$(id -u)` and `DEV_GID=$(id -g)` to `.env`, so files backd generates into `config/` belong to you; on rootless podman the default already maps to your user.

Then bring it up — the three are required, so a missing one stops compose before anything starts and names it:

```sh
docker compose up
```

`backd` listens on <http://127.0.0.1:8080>, and `BACKD_DEV=true` makes it follow `config/` live: a saved change reloads in a moment, functions re-bundle automatically. Any `backd` command also runs in the stack: `docker compose exec backd /backd <command>` inside the running API container, or `docker compose run --rm backd <command>` for a one-off container (`docker compose run --rm backd version` needs the three secrets set, like `up`). Create the realm's first administrator (it asks for a password):

```sh
docker compose exec backd backd-server bootstrap --realm demo --email me@example.com
```

Sign in with the CLI. The template's `admin` role requires a second factor, so this first login enrolls an authenticator app (see [Multi-factor authentication](../auth/mfa/) for what it shows and asks). Then create an API key for calls:

```sh
backd login --realm demo --url http://127.0.0.1:8080 --email me@example.com
KEY=$(backd apikey create --realm demo --name local --expires 1d)
```

`apikey create` asks for your password again, as a fresh proof for a sensitive action, and prints the key once on standard output: that is what `$KEY` holds. Something refused along the way? See [Troubleshooting](../../troubleshooting/).

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

The generated `README.md` in the project covers the same steps, plus MinIO and how to point a realm's `storage:` at it.

### The iterate loop

Dev mode does the reload for you — there is no rebuild step:

| You changed… | Do |
|---|---|
| `realm.yaml`, a schema, `collection.yaml` rules | Save the file — backd reloads the config in a moment |
| A function's code | Save the file — the bundle is rebuilt automatically (see [Build, dev and test](../functions/testing/#dev-mode)) |
| Nothing: just testing a function's logic | `deno test config/demo/main/_functions/` — no MongoDB, no running backd |

## 4. Without Docker

The server needs MongoDB 8.2 as a replica set; a single node is fine ([Operate → MongoDB](../../operate/#mongodb) shows how to start one). Install both release binaries, `backd` and `backd-server`; `backd-server` runs on Linux only (on macOS and Windows use the containers above, or WSL2):

```sh
mkdir config && export CONFIG_DIR=$PWD/config
backd template realm --realm demo --sample
export MONGO_URI='mongodb://localhost:27017/?replicaSet=rs0'   # a single-node replica set
backd-server provision
backd-server bootstrap --realm demo --email me@example.com          # the first administrator; asks for a password
HTTP_ADDR=127.0.0.1:9090 backd-server serve &
backd login --realm demo --url http://127.0.0.1:9090 --email me@example.com
KEY=$(backd apikey create --realm demo --name local --expires 1d)
curl http://127.0.0.1:9090/v1/demo/main/posts -H "Authorization: Bearer $KEY"
```

`backd` itself serves plain HTTP (`:8080` by default; `9090` here); put TLS in front of it as in the [production reference](../../operate/production/).

## Commands you will use

`backd help` groups its commands by who uses them. The **Project** group is the one an application developer reaches for:

| Command | Purpose |
|---|---|
| `backd template project\|realm\|database\|function\|email-capture` | Create a config tree, or a piece of one (see [Starting from a template](../configuration/config-dir/#starting-from-a-template)) |
| `backd config check`, `backd config fingerprint` | Validate `CONFIG_DIR` as startup would, or print its fingerprint |
| `backd rules test` | Run each collection's `rules.test.yaml` without a database (see [Testing rules](../auth/rules/#testing-rules)) |
| `backd databases [--collections]` | Print the MongoDB databases (or collections) the config uses |
| `backd types`, `backd openapi` | Generate TypeScript types or an OpenAPI document from a realm (see [Clients](../clients/types/)) |
| `backd functions invoke\|history\|logs\|jobs` | Call and inspect [server-side functions](../functions/) through a running backd |
| `backd-server functions build` | Bundle the functions (CI and the container do it; dev mode does it for you) |

…and `backd-server bootstrap` and `backd login` for the first administrator and a session, as above. Administering a realm — users, API keys, organizations, secrets, the audit trail — is in [Command-line administration](../../operate/administration/cli/), which covers every command.

{{< hint note >}}
**What happens in the shadows.** The generated `compose.yaml` mirrors the repository's own development stack: same network shape (the executor can only reach `backd` and egress), `serve --with-worker`, dev-mode bundling. That stack — `make dev`, live config reload, Grafana, the local certificate authority — is for working on `backd` itself and is documented in [Contribute → Local development](../../contribute/local-development/).
{{< /hint >}}
