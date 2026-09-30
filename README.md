# backd

`backd` is a config-driven backend service that exposes schema-validated REST APIs over document data stored in MongoDB.
Declare realms, databases and collections as directories on disk, each collection with a JSON Schema, and `backd` serves create, read, update, delete and query endpoints for them.

> [!WARNING]
> Realms with `auth: disabled` are open to anyone who can reach backd: never expose them to the internet.
> Before exposing any realm, go through the [hardening checklist](https://fernandezvara.github.io/backd/docs/operations/checklist/) (Operations → Hardening checklist in the docs).

## Status

Iteration 1 is complete: config-driven CRUD with schema validation, filtering, sorting, counting, declared indexes, optimistic concurrency (`ETag` / `If-Match`), request limits, timeouts and graceful shutdown.
Iteration 2 is complete (v0.2.0, not tagged yet): users per realm with single sign-on across the realm's databases, sessions, API keys, per-collection access rules (`rules.yaml`), roles, CORS, invitations and an admin API. See the [releases](https://github.com/fernandezvara/backd/releases).

Iteration 3 (API contract and JavaScript client) is complete: the OpenAPI spec in [`api/openapi.yaml`](api/openapi.yaml) is done and enforced by contract tests, and the JavaScript client in [`clients/js`](clients/js) covers auth, sessions, collections and the admin API, with an Alpine.js example app at https://localhost:8443/example/ (`make example`). Iteration 4 put the API, the docs (with live reload) and the example on that one origin behind nginx.

Secure administration (roadmap Phase S) is complete: key roles (`data`/`admin`), a full admin API (users, roles, API keys, invitations) usable without direct database access, and an append-only audit trail.

Server-side functions (roadmap Phase F, complete) let you run JavaScript or TypeScript next to your collections, reaching data through the same access rules as any other caller: `sync` calls, `async` background jobs with a worker role, and `webhook` callbacks from other services, and cron `schedule`s for async functions, with declared secrets, per-caller rate limits, per-instance concurrency limits, idempotent calls and invocation history. Their security review is complete; they run in realms with authentication with no flag needed. See [Functions](https://fernandezvara.github.io/backd/docs/functions/).

## Quick start

```sh
make example                     # backd + MongoDB with examples/config, docs, example app
export CURL_CA_BUNDLE=$PWD/docker/certs/ca.crt   # trust the local CA in curl
curl https://localhost:8443/readyz

curl -X POST https://localhost:8443/v1/shop/orders/items \
  -H 'Content-Type: application/json' \
  -d '{"name": "Widget", "price": 12.5}'
curl -G https://localhost:8443/v1/shop/orders/items \
  --data-urlencode 'where={"price": {"$lt": 20}}' \
  --data-urlencode 'order_by=-price' -d count=true
```

`make example` serves everything over HTTPS with a certificate from a local CA that it creates (and renews) in `docker/certs/` with [certsfor](https://www.certsfor.dev); it prints how to trust that CA in your browser or system. The data model lives in [`examples/config`](examples/config): one directory per realm (with a `realm.yaml`), database and collection, each collection with a `schema.json`. `backd template realm <name> --sample` creates a starter realm. The `shop` example has `auth: disabled` so the requests above need no key; `blog` has access rules (anyone reads published posts) and writes need a user or an API key, created by the realm's administrators (`docker compose exec backd /backd bootstrap --realm blog --email <email>` creates the first; see [Getting started](https://fernandezvara.github.io/backd/docs/getting-started/)).

## Security

See [SECURITY.md](SECURITY.md) to report vulnerabilities, and the security model in the docs (Authentication → Security model). To run backd in public, start from the tested reference in [`deploy/production`](deploy/production): TLS, rate limits, MongoDB with authentication and TLS, and a least-privilege database user.

## Upgrading

v0.2.0 is a breaking release: every realm needs a `realm.yaml`, and authentication is on by default. See [upgrading to v0.2.0](https://fernandezvara.github.io/backd/docs/operations/#upgrading-to-v020).

## Development

```sh
make test         # full test suite in docker (same as CI)
make test-local   # tests that need no external services
make build        # binary in bin/backd
make docs-serve   # documentation site at http://localhost:1313/backd/
make example      # everything on https://localhost:8443: API, docs (live reload), JS example app
make prod-test    # end-to-end test of the production reference deployment
make release-check  # build every release archive into dist/ without publishing
make hack-expenses  # attack the expenses example (needs make example running)
```

Documentation lives in [`docs/`](docs/) and is built with Hugo.

## License

[MIT](LICENSE)
