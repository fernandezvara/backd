# backd

`backd` is an open-source (MIT), config-driven backend: one stateless Go service over MongoDB. You declare realms, databases and collections as files on disk — a JSON Schema, access rules and indexes per collection — and `backd` serves a schema-validated REST API for them, with users, sessions, API keys and server-side functions built in.

**Documentation → [fernandezvara.github.io/backd](https://fernandezvara.github.io/backd/docs/)** · [Getting started](https://fernandezvara.github.io/backd/docs/getting-started/) · [Quickstart](https://fernandezvara.github.io/backd/docs/build/quickstart/) · [Tutorial](https://fernandezvara.github.io/backd/docs/tutorial/)

## Features

- **Data model as files** — a directory per realm, database and collection, each collection with a `schema.json`, access rules and indexes; `backd config check` runs the startup validation in CI. [Configuration](https://fernandezvara.github.io/backd/docs/build/configuration/)
- **REST API** — CRUD and an allowlisted query language (`where`, `order_by`, pagination, counts), optimistic concurrency with `ETag`/`If-Match`, batch writes, [references](https://fernandezvara.github.io/backd/docs/build/api/references/) with `expand`, [aggregations](https://fernandezvara.github.io/backd/docs/build/api/aggregations/) and [full-text search](https://fernandezvara.github.io/backd/docs/build/api/search/). [HTTP API](https://fernandezvara.github.io/backd/docs/build/api/)
- **Auth built in** — users, sessions and API keys per realm; sign-in with Google, Microsoft or Apple, a second factor, magic links, per-document access rules, consents and an audit trail. [Authentication](https://fernandezvara.github.io/backd/docs/build/auth/)
- **Organizations** — groups of users for B2B apps: roles, teams, team roles with time windows, documents scoped to an organization or team, invitations and organization email templates. [Organizations](https://fernandezvara.github.io/backd/docs/build/organizations/)
- **Functions and triggers** — isolated Deno processes for privileged logic: sync calls, background jobs, cron schedules, webhooks, encrypted secrets, and functions that run when documents change. [Functions](https://fernandezvara.github.io/backd/docs/build/functions/), [Triggers](https://fernandezvara.github.io/backd/docs/build/functions/triggers/)
- **Realtime** — subscribe to a collection's changes over one server-sent event stream, with the caller's rules applied, resumable and bounded. [Realtime](https://fernandezvara.github.io/backd/docs/build/api/realtime/)
- **Files and push** — documents carry files in your own S3-compatible storage with image versions and quotas; push notifications reach FCM, APNs and browsers. [Files](https://fernandezvara.github.io/backd/docs/build/files/), [Push](https://fernandezvara.github.io/backd/docs/build/push/)
- **Clients and types** — `backd-js` on npm (browsers, Node and edge runtimes, admin API included) and `package:backd` on pub.dev (Flutter and standalone Dart); `backd types` generates TypeScript or Dart per realm and `backd openapi` an OpenAPI 3.1 document for everything else. [Clients](https://fernandezvara.github.io/backd/docs/build/clients/)
- **A production path** — tested reference deployment (TLS, least-privilege MongoDB, encrypted backups), a config fingerprint every instance enforces, and signed releases. [Operate](https://fernandezvara.github.io/backd/docs/operate/)

## Quick start

The shortest path to a realm you can build against — see the [Quickstart](https://fernandezvara.github.io/backd/docs/build/quickstart/) for the full walkthrough. Get `backd` from [GitHub Releases](https://github.com/fernandezvara/backd/releases) (binaries for Linux, macOS and Windows) or the image `ghcr.io/fernandezvara/backd`, then generate a project and run its compose stack:

```sh
backd template project --dir ./my-app --realm demo
cd my-app

export EXECUTOR_TOKEN=$(openssl rand -base64 32)
export CALLBACK_KEY=$(openssl rand -base64 32)
export EGRESS_KEY=$(openssl rand -base64 32)
docker compose up --build -d          # MongoDB + backd + the functions executor

docker compose exec backd /usr/local/bin/backd bootstrap --realm demo --email me@example.com
backd login --realm demo --url http://127.0.0.1:8080 --email me@example.com
KEY=$(backd apikey create --realm demo --name local --expires 1d)   # asks for your password again

curl -X POST http://127.0.0.1:8080/v1/demo/main/posts \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"title": "Hello", "published": true}'
curl http://127.0.0.1:8080/v1/demo/main/posts -H "Authorization: Bearer $KEY"
```

(`backd login` asks for the bootstrap password and enrolls an authenticator app — the template's `admin` role requires a second factor.)

To explore instead, this checkout ships example realms and applications on a local HTTPS stack:

```sh
BACKD_VERSION=local make dev-build && BACKD_VERSION=local make dev-examples   # https://localhost:8443 — the apps index is at /example/
```

`BACKD_VERSION=local` builds the images from your checkout, which the examples need when they use something newer than the last release. The stack creates (and renews) a local CA in `docker/certs/` and prints how to trust it. Under [`examples/config`](examples/config), the `shop` realm needs no key (`auth: disabled` — never expose such a realm) and `blog` answers reads to anyone while writes want a user or an API key; `make dev-backd ARGS='bootstrap --realm blog --email <email>'` creates the first administrator.

## Documentation

| I want to… | Go to |
|---|---|
| Understand what backd is | [Getting started](https://fernandezvara.github.io/backd/docs/getting-started/) |
| Fix an error I got | [Troubleshooting](https://fernandezvara.github.io/backd/docs/troubleshooting/) |
| Run a realm end to end | [Quickstart](https://fernandezvara.github.io/backd/docs/build/quickstart/) |
| Build a complete app, chapter by chapter | [Tutorial](https://fernandezvara.github.io/backd/docs/tutorial/) |
| Reference: API, auth, functions, files, push, clients | [Build](https://fernandezvara.github.io/backd/docs/build/) |
| Deploy, administer, monitor and back up | [Operate](https://fernandezvara.github.io/backd/docs/operate/) |
| See complete applications | [Examples](https://fernandezvara.github.io/backd/docs/build/examples/) |
| Work on backd itself | [Contribute](https://fernandezvara.github.io/backd/docs/contribute/) |

## Security

Report vulnerabilities per [SECURITY.md](SECURITY.md). The [security model](https://fernandezvara.github.io/backd/docs/operate/administration/security/) explains what `backd` protects, and the [hardening checklist](https://fernandezvara.github.io/backd/docs/operate/checklist/) what to check before exposing a realm; [`deploy/production`](deploy/production) is a tested reference for a public deployment (TLS, rate limits, least-privilege MongoDB).

## Development

```sh
make test         # full test suite in docker (same as CI)
make test-local   # tests that need no external services
make build        # binary in bin/backd
make docs-serve   # documentation site at http://localhost:1313/backd/
make example      # backd's own stack, built from your checkout: API, docs (live reload), example apps
```

Other targets cover the development stack (`make dev`), the production end-to-end test (`make prod-test`), release archives (`make release-check`), and the attack and tour scripts (`make hack-expenses*`, `make workshop-tour`, `make shelf-tour`). The workflow is documented in [Contribute → Local development](https://fernandezvara.github.io/backd/docs/contribute/local-development/). Documentation lives in [`docs/`](docs/) and is built with Hugo.

## License

[MIT](LICENSE)
