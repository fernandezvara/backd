---
title: "Local development"
description: "The repository's development stack: make dev, live config reload, HTTPS with a local CA, the example realms, and building backd from source."
icon: "terminal-window"
weight: 300
toc: true
---

## The development stack

The repository includes a development stack for building **on** backd: released images, your own configuration, and the documentation of your checkout. You need Docker (or Podman with `podman-compose`) and the repository checkout, because the stack's files live in it.

```sh
make dev-build     # once: pulls the images, builds the docs site's, makes the local CA and certificate, creates ./config
make dev           # starts everything (nothing is pulled), prints the addresses, follows the logs
```

`make dev-build` pulls `ghcr.io/fernandezvara/backd-executor:latest` and the other images, and creates a starter configuration in `./config` (a realm `app` with a sample `blog` database) if you have none. Ctrl-C stops the stack and keeps its data.

| Address | Serves |
|---|---|
| `https://localhost:8443/` | The welcome page: links to everything below, and the seeded accounts' sign-in details |
| `https://localhost:8443/local-seed/` | The `local-seed/` files the page reads (`stack.json`, `credentials.json`) — gitignored, localhost only |
| `https://localhost:8443/v1/…`, `/healthz`, `/readyz` | `backd`'s API, for the realms in your config |
| `https://localhost:8443/_ui/` | The [admin web interface](../../operate/administration/admin-ui/) |
| `https://localhost:8443/docs/` | The documentation of this checkout, with live reload: edit a page in `docs/` (or an example it quotes) and the open page reloads. The first start downloads Hugo's theme, so give it a minute |
| `http://localhost:3000` | Grafana, with backd's [dashboards](../../operate/metrics/#dashboards) |
| `http://localhost:9090` | Prometheus |
| `http://localhost:9001` | The MinIO console (`backd-dev` / `dev-p4ssw0rd!`), for [files](../../build/files/) |

Plain `http://localhost:8080` redirects to HTTPS. The stack runs MongoDB, the API, a **worker** of its own, the functions executor, egress, MinIO, Prometheus, Grafana, the documentation site (Hugo) and nginx. With `make dev-examples` the pages also link to the example apps.

### Your configuration, live

`CONFIG_DIR` (default `./config`) is mounted **read-write** into the API, the worker and the build step, and the stack runs in [dev mode](../../build/functions/testing/#dev-mode): save a file and backd loads and validates it, keeping the previous configuration if the new one is invalid.

- **Realms, schemas, rules, triggers and templates** change without a restart.
- **Functions** are bundled again as you save, and `backd.d.ts` is written next to them for your editor.
- **The worker follows too**, so a changed async function or schedule is the one the next job runs.

Because backd writes into your directory (bundles, `backd.d.ts`), the containers run as you, so the files stay yours. Set another directory with `CONFIG_DIR=~/projects/shop/backd make dev`.

### The API and the worker, apart

The API (`backd`) and the worker (`worker`) are separate containers: a job queued by a request is run by the worker, and with the worker stopped it stays `queued`.

```sh
make dev-logs                 # everything
make dev-logs S=worker        # the worker alone (or backd, executor, egress, ...)
WORKERS=3 make dev            # three workers
```

In Grafana, the *Processes* dashboard shows the API, the workers, the executor and egress apart (`role` label), and Prometheus's targets page lists every worker.

### Commands

| Command | Does |
|---|---|
| `make dev-build` | Pulls the images, makes the certificates, creates `./config` if it is missing. Run it again to update. |
| `make dev` | Starts the stack and follows the logs. |
| `make dev-examples` | The same with the repository's [example realms and apps](../../build/examples/) (below), seeded — their users' passwords, TOTP QR and recovery codes are on the welcome page and in `local-seed/credentials.json`. |
| `make dev-down` | Stops it, keeping the data. |
| `make dev-reset` | Stops it and deletes the data (MongoDB, MinIO, Prometheus, Grafana). Your `./config` is not touched. |
| `make dev-logs` | Follows the logs; `S=worker` for one service. |
| `make dev-backd ARGS='…'` | Runs the `backd` command line in the API container, for example `bootstrap`. |

`BACKD_VERSION` pins the images (`BACKD_VERSION=v0.9.5 make dev-build`); the default is `latest`. The stack is for localhost and development: dev mode, fixed dev-only tokens and an open Grafana. Never use it for a deployment (see [Production deployment](../../operate/production/)).

To test the stack against a build of your checkout instead of a release, use the version `local`. `make dev-build` builds the executor image from the checkout (a snapshot of the Go code at that moment: run it again after you change code) and pulls the other images:

```sh
BACKD_VERSION=local make dev-build
BACKD_VERSION=local make dev-examples
```

The examples follow the code, so they need this when they use something newer than the last release; the stack says so if the released image refuses the configuration.

`docker-compose.dev.yml` is checked by `make dev` itself: change it, run the stack, and check the API (`/readyz`), the worker (stop it and see a job stay `queued`), a change to a function while it runs, and `make dev-down` and `make dev-reset` leaving nothing behind.

### The first administrator

A realm with no administrator has none to sign in as. Create one with the command line in the API container (it asks for a password):

```sh
make dev-backd ARGS='bootstrap --realm app --email you@example.com'
```

The starter realm requires a second factor of administrators, so their first sign-in enrolls one. Empty `mfa.required_roles` in `config/app/realm.yaml` while developing if you prefer.

### The example realms

`make dev-examples` runs the same stack on the repository's `examples/config`, with the example apps at `/example/` and the storage keys and administrators they need already set:

```
examples/config/
├── shop/
│   ├── realm.yaml
│   ├── orders/
│   │   ├── items/{schema.json, indexes.json}
│   │   └── clients/{schema.json, indexes.json}
│   └── billing/
│       └── invoices/{schema.json, indexes.json}
├── blog/
│   ├── realm.yaml
│   └── main/
│       └── posts/{schema.json, indexes.json, collection.yaml}
├── expenses/                   (and expenses-with-functions/: the same app with server-side functions)
│   ├── realm.yaml
│   └── main/
│       ├── groups/{schema.json, indexes.json, collection.yaml}
│       └── expenses/{schema.json, indexes.json, collection.yaml}
└── workshop/                   the functions cookbook: sync, jobs, cron and a webhook
    ├── realm.yaml
    └── main/
        ├── orders/, refunds/, receipts/, reports/, digests/, events/
        └── _functions/{order_total, refund, refund_receipt, export_orders, nightly_cleanup, daily_digest, payment_webhook}/
```

The tree shows the first examples; `examples/config` also holds `shelf` (the [tutorial](../../tutorial/)'s app, in its final state), `hr` (organizations), `idtest` (custom ids) and `adminui` (the admin interface's fixtures).

The seeder reads each realm's `realm.yaml` for what to create — the seeded
`roles.*.users`, the signup mode, `account.require_verified_email` and `storage:` —
so a new example realm needs no script changes; an optional `seed.json` next to the
`realm.yaml` holds what the realm file can't say (the app's path, demo accounts with
no role). See `scripts/seed.mjs` for the format.

Start with the examples, and let curl trust the local CA:

```sh
make dev-build && make dev-examples
export CURL_CA_BUNDLE=$PWD/docker/certs/ca.crt   # in another terminal: curl trusts the local CA
curl https://localhost:8443/readyz
```

Create, read, update and delete a document:

```sh
curl -X POST https://localhost:8443/v1/shop/orders/items \
  -H 'Content-Type: application/json' \
  -d '{"name": "Widget", "price": 12.5, "stock": 3}'
# → 201 {"id":"darf3h9ck12g00eg2hh0","_meta":{...},"name":"Widget",...}

curl https://localhost:8443/v1/shop/orders/items/darf3h9ck12g00eg2hh0
curl -X PATCH https://localhost:8443/v1/shop/orders/items/darf3h9ck12g00eg2hh0 \
  -H 'Content-Type: application/merge-patch+json' -d '{"stock": 2}'
curl -G https://localhost:8443/v1/shop/orders/items \
  --data-urlencode 'where={"name": {"$ilike": "wid%"}, "price": {"$lt": 20}}' \
  --data-urlencode 'order_by=-price' -d count=true
curl -X DELETE https://localhost:8443/v1/shop/orders/items/darf3h9ck12g00eg2hh0
```

The `shop` realm has `auth: disabled`, so these requests need no credentials.

{{< hint warning >}}
A realm with `auth: disabled` has **no authentication at all**: anyone who can reach the port can read, change and delete its data. It exists for this walkthrough. Never expose one to the internet.
{{< /hint >}}

The `blog` realm keeps the default `auth: enabled`, and its `posts` have [access rules](../../build/auth/rules/): anyone can read published posts, and only signed-in users can write. A server-side service writes with an [API key](../../build/auth/api-keys/), which bypasses the rules:

Keys are created by the realm's administrators. Create the first one with `backd bootstrap`, log in as them with the CLI in the container, and create a key:

```sh
make dev-backd ARGS='bootstrap --realm blog --email ops@example.com'        # asks for a password
make dev-backd ARGS='login --realm blog --url http://localhost:8080 --email ops@example.com'
KEY=$(make -s dev-backd ARGS='apikey create --realm blog --name demo --expires 1d')
curl -X POST https://localhost:8443/v1/blog/main/posts -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"title": "Hello", "published": true}'
curl https://localhost:8443/v1/blog/main/posts        # anonymous: published posts only
```

{{< hint style="tip" title="Already done for you" >}}
`docker-compose.dev.yml` publishes ports 8443 and 8080 on `127.0.0.1` only, because the `shop` example realm has `auth: disabled`. Keep it that way; the dev-only tokens and keys in it are fixed and public.
{{< /hint >}}

To see web apps use these realms, open the [example apps](../../build/examples/) at <https://localhost:8443/example/>: a [blog](../../build/examples/blog/) on the `blog` realm, and a [shared-expenses app](../../build/examples/expenses/) on the `expenses` realm.

## HTTPS with a local CA

Like a [production deployment](../../operate/production/), the local stack only serves HTTPS. Its certificate comes from a local certificate authority that `make dev-build` and `make dev` manage with [certsfor](https://www.certsfor.dev), run as a container (`scripts/local-certs.sh`):

- **First run:** it creates the CA and a certificate for `localhost`, `127.0.0.1` and `::1`, from the templates in `docker/cfd/`.
- **Every run:** it asks certsfor for the certificate again, which renews it when less than 20% of its 90-day lifetime is left. Restarting `make dev` is enough to keep it valid.
- **Files:** everything goes to `docker/certs/`, which git ignores: `ca.crt` (the CA certificate), `localhost.crt` and `localhost.key` (nginx's certificate and key) and certsfor's database with the CA's key.

Browsers warn about the certificate until you trust the CA. Either accept the warning once for `https://localhost:8443`, or add `docker/certs/ca.crt` to your trust store. `make dev` prints the commands for your system when the CA isn't trusted yet:

| Where | How |
|---|---|
| Debian, Ubuntu | `sudo cp docker/certs/ca.crt /usr/local/share/ca-certificates/backd-local-ca.crt && sudo update-ca-certificates` |
| Fedora, RHEL | `sudo cp docker/certs/ca.crt /etc/pki/ca-trust/source/anchors/backd-local-ca.crt && sudo update-ca-trust` |
| macOS | `sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain docker/certs/ca.crt` |
| Windows | `certutil -addstore -f ROOT docker\certs\ca.crt` (as administrator) |
| Firefox | Settings → Privacy & Security → Certificates → View Certificates → Authorities → Import |
| Chrome or Chromium on Linux | `certutil -d sql:$HOME/.pki/nssdb -A -t C,, -n "backd local CA" -i docker/certs/ca.crt` |
| curl | `--cacert docker/certs/ca.crt`, or `export CURL_CA_BUNDLE=$PWD/docker/certs/ca.crt` |

{{< hint warning >}}
The CA's private key is in `docker/certs/`: anyone who has it can issue certificates your machine trusts. Trust the CA only on your own machine, and never copy `docker/certs/` elsewhere.
{{< /hint >}}

To start over, remove the CA from your trust store and delete `docker/certs/`; the next run creates a new one. The local stack doesn't send `Strict-Transport-Security`, because browsers would apply it to every site on `localhost`.

## Building backd from source

`backd` needs Go 1.27.3 or later.

```sh
git clone https://github.com/fernandezvara/backd
cd backd
make build        # produces bin/backd
```

Or build the container image:

```sh
docker build -t backd .
```

The [Contribute](../) page covers the rest of the workflow: the two local stacks, testing, CI and releasing.
