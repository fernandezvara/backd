# backd

`backd` is a config-driven backend service that exposes schema-validated REST APIs over document data stored in MongoDB.
Declare realms, databases and collections as directories on disk, each collection with a JSON Schema, and `backd` serves create, read, update, delete and query endpoints for them.

## Features

- **Config-driven data model** — realms, databases and collections declared as directories, with a JSON Schema per collection and config validation at startup (`backd config check`). [Configuration](https://fernandezvara.github.io/backd/docs/configuration/)
- **Document API** — schema-validated CRUD plus `where`, `order_by`, pagination and counts over an allowlisted query language; optimistic concurrency via `ETag`/`If-Match`. [HTTP API](https://fernandezvara.github.io/backd/docs/api/)
- **Built-in auth** — users, sessions and API keys per realm, sign-in with Google, Microsoft and Apple, a second factor and magic links, per-document access rules, consents, brute-force protection and an audit trail. [Authentication](https://fernandezvara.github.io/backd/docs/auth/)
- **Server-side functions** — isolated Deno processes for privileged logic: sync calls, background jobs, cron schedules, webhooks, encrypted secrets and a network allowlist. [Functions](https://fernandezvara.github.io/backd/docs/functions/)
- **Organizations** — groups of users for B2B apps: roles, teams, team roles with time windows, documents that belong to an organization or a team, membership hooks and company email templates. [Organizations](https://fernandezvara.github.io/backd/docs/organizations/)
- **References, aggregations and search** — return referenced documents with `expand`, summarize with `_aggregate`, and search prose with a weighted text index; declared in config, with the rules applied first. [References](https://fernandezvara.github.io/backd/docs/api/references/), [Aggregations](https://fernandezvara.github.io/backd/docs/api/aggregations/), [Search](https://fernandezvara.github.io/backd/docs/api/search/)
- **Files and push** — documents carry files in your own S3-compatible storage, with image versions and quotas; push notifications reach FCM, APNs and browsers. [Files](https://fernandezvara.github.io/backd/docs/files/), [Push](https://fernandezvara.github.io/backd/docs/push/)
- **App configuration** — values your apps read, with a schema, read rules, history and rollback, and conditional GET. [App configuration](https://fernandezvara.github.io/backd/docs/configuration/app-config/)
- **Types and OpenAPI** — `backd types` generates TypeScript from the config, and `backd openapi` an OpenAPI 3.1 document per realm. [Typing](https://fernandezvara.github.io/backd/docs/clients/types/)
- **JavaScript client** — typed client (`backd-js`, released on the same version as the server) for the document API, auth and functions. [Clients](https://fernandezvara.github.io/backd/docs/clients/)
- **Production path** — tested reference deployment (TLS, least-privilege MongoDB, encrypted backups) and a config fingerprint so every instance runs the same configuration. [Operations](https://fernandezvara.github.io/backd/docs/operations/)

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

`make example` serves everything over HTTPS with a certificate from a local CA that it creates (and renews) in `docker/certs/` with [certsfor](https://www.certsfor.dev); it prints how to trust that CA in your browser or system.

- **Data model** — [`examples/config`](examples/config) holds one directory per realm (with a `realm.yaml`), database and collection, each collection with a `schema.json`. `backd template realm <name> --sample` creates a starter realm.
- **`shop` realm** — `auth: disabled`, so the requests above need no key.
- **`blog` realm** — access rules let anyone read published posts; writes need a user or an API key, created by the realm's administrators. `docker compose exec backd /backd bootstrap --realm blog --email <email>` creates the first one.

See [Getting started](https://fernandezvara.github.io/backd/docs/getting-started/) for a full walkthrough.

## Security

See [SECURITY.md](SECURITY.md) to report vulnerabilities, and the security model in the docs (Authentication → Security model). To run backd in public, start from the tested reference in [`deploy/production`](deploy/production): TLS, rate limits, MongoDB with authentication and TLS, and a least-privilege database user.

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
make hack-expenses-functions  # attack the expenses-with-functions example (needs make example running)
make workshop-tour  # walk the workshop tour without a browser and check each step (needs make example running)
make shelf-tour     # replay the Shelf tutorial's checks against the tutorial's own stack
```

Documentation lives in [`docs/`](docs/) and is built with Hugo.

## License

[MIT](LICENSE)
