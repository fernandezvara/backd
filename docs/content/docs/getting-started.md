---
title: "Getting started"
description: "Run backd with docker compose, or build it from source."
icon: "rocket_launch"
weight: 100
toc: true
---

Core concepts:

```
realm            realm.yaml settings; shared user pool with single sign-on
 └─ database     one application's data  → MongoDB database <realm>__<database>
     └─ collection   documents validated by schema.json → one MongoDB collection
         └─ document  JSON object with server-owned id and _meta
```

## Quick start with docker compose

The repository includes a `docker-compose.yml` that runs `backd` and MongoDB with the sample config in `examples/config`, together with this documentation site and the JavaScript client's [example apps](../examples/). nginx serves all of them over HTTPS on one origin, `https://localhost:8443`. Plain `http://localhost:8080` redirects there. It also runs the [functions](../functions/) executor, egress and (folded into `backd`) a worker: the blog realm's sample `stats` function calls the executor for real (`curl https://localhost:8443/v1/blog/main/_func/stats`).

| Path | Serves |
|---|---|
| `/healthz`, `/readyz`, `/v1/…` | `backd`'s API |
| `/example/` | The [example apps](../examples/): a blog and a shared-expenses app |
| `/`, `/docs/…` | This documentation site, reloading as you edit `docs/` |

The sample config:

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
│       └── posts/{schema.json, indexes.json, rules.yaml}
├── expenses/                   (and expenses-with-functions/: the same app with server-side functions)
│   ├── realm.yaml
│   └── main/
│       ├── groups/{schema.json, indexes.json, rules.yaml}
│       └── expenses/{schema.json, indexes.json, rules.yaml}
└── workshop/                   the functions cookbook: sync, jobs, cron and a webhook
    ├── realm.yaml
    └── main/
        ├── orders/, refunds/, receipts/, reports/, digests/, events/
        └── _functions/{order_total, refund, refund_receipt, export_orders, nightly_cleanup, daily_digest, payment_webhook}/
```

```sh
make example
export CURL_CA_BUNDLE=$PWD/docker/certs/ca.crt   # in another terminal: curl trusts the local CA
curl https://localhost:8443/readyz
```

The first start builds the images and downloads the docs theme, which takes a few minutes; later starts are quick. To start the stack without `make example`, create the certificates first: `scripts/local-certs.sh && docker compose up --build`.

### HTTPS with a local CA

Like a [production deployment](../operations/production/), the local stack only serves HTTPS. Its certificate comes from a local certificate authority that `make example` manages with [certsfor](https://www.certsfor.dev), run as a container (`scripts/local-certs.sh`):

- **First run:** it creates the CA and a certificate for `localhost`, `127.0.0.1` and `::1`, from the templates in `docker/cfd/`.
- **Every run:** it asks certsfor for the certificate again, which renews it when less than 20% of its 90-day lifetime is left. Restarting `make example` is enough to keep it valid.
- **Files:** everything goes to `docker/certs/`, which git ignores: `ca.crt` (the CA certificate), `localhost.crt` and `localhost.key` (nginx's certificate and key) and certsfor's database with the CA's key.

Browsers warn about the certificate until you trust the CA. Either accept the warning once for `https://localhost:8443`, or add `docker/certs/ca.crt` to your trust store. `make example` prints the commands for your system when the CA isn't trusted yet:

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

The `blog` realm keeps the default `auth: enabled`, and its `posts` have [access rules](../auth/rules/): anyone can read published posts, and only signed-in users can write. A server-side service writes with an [API key](../auth/api-keys/), which bypasses the rules:

Keys are created by the realm's administrators. Create the first one with `backd bootstrap`, log in as them with the CLI in the container, and create a key:

```sh
docker compose exec backd /backd bootstrap --realm blog --email ops@example.com        # asks for a password
docker compose exec backd /backd login --realm blog --url http://localhost:8080 --email ops@example.com
KEY=$(docker compose exec -T backd /backd apikey create --realm blog --name demo --expires 1d)
curl -X POST https://localhost:8443/v1/blog/main/posts -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"title": "Hello", "published": true}'
curl https://localhost:8443/v1/blog/main/posts        # anonymous: published posts only
```

{{< hint style="tip" title="Already done for you" >}}
The compose file publishes ports 8443 and 8080 on `127.0.0.1` only, because the `shop` example realm has `auth: disabled`. Keep it that way; the dev-only tokens and keys in it are fixed and public.
{{< /hint >}}

To see web apps use these realms, open the [example apps](../examples/) at <https://localhost:8443/example/>: a [blog](../examples/blog/) on the `blog` realm, and a [shared-expenses app](../examples/expenses/) on the `expenses` realm.

## Releases

Each tagged version is published automatically:

- binaries for Linux, macOS and Windows (amd64 and arm64), with a `checksums.txt`, on the repository's [GitHub Releases](https://github.com/fernandezvara/backd/releases) page;
- a container image for linux/amd64 and linux/arm64: `ghcr.io/fernandezvara/backd:<version>`, and `latest` for the newest stable version.

Each release is checked and verifiable:

- archives come with `checksums.txt` and an SPDX SBOM each (`*.sbom.json`);
- the image is scanned for known vulnerabilities before it's published, carries SBOM and provenance attestations, and is signed with [cosign](https://docs.sigstore.dev/) using GitHub's identity (no keys to manage). Verify it with:

```sh
cosign verify ghcr.io/fernandezvara/backd:v0.2.0 \
  --certificate-identity-regexp '^https://github.com/fernandezvara/backd/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

The first release will be v0.2.0. Until it's tagged, build from source. `backd version` prints the version of a binary or image.

## Building from source

`backd` needs Go 1.27.1 or later.

```sh
git clone https://github.com/fernandezvara/backd
cd backd
make build        # produces bin/backd
```

Or build the container image:

```sh
docker build -t backd .
```

## Commands

| Command | Purpose |
|---|---|
| `backd serve` (default) | Load config, provision or verify MongoDB, serve HTTP |
| `backd serve --with-worker` | Also run the worker role (below) in the same process |
| `backd worker` | Claim and run [async jobs](../functions/jobs/) (`mode: async`); `WORKER_CONCURRENCY` caps how many run at once |
| `backd provision` | Load config, apply provisioning once and exit |
| `backd template realm --realm <realm> [--sample]` | Create a commented `realm.yaml` (and, with `--sample`, a sample database) |
| `backd template database --realm <realm> --database <database> [--sample]` | Create a database directory (and, with `--sample`, a sample collection) |
| `backd template function --realm <realm> --database <database> --name <name>` | Create a [function](../functions/) in the database's `_functions` project |
| `backd template email-capture --realm <realm> --database <database>` | Add a development [email](../functions/email/#developing-without-a-provider) function that stores each email in an `outbox` collection |
| `backd template project --dir <directory> --realm <realm>` | Create a complete config repository: realm, sample database and function (with tests), Dockerfiles, a local dev stack and CI (see [Starting from a template](../configuration/config-dir/#starting-from-a-template)) |
| `backd functions build [--check]` | Bundle the functions in `CONFIG_DIR` with Deno (see [Functions](../functions/testing/#building)) |
| `backd functions invoke\|history\|logs\|jobs --function <realm>/<database>/<name>` | Call a function through a running `backd`, and see its recent calls, its console output and its async and scheduled jobs (see [Calling functions](../functions/calling/), [Async jobs](../functions/jobs/))  |
| `backd executor` | Run server-side [functions](../functions/running/#running-the-executor) for `backd` (needs Deno; the `backd-executor` image has it) |
| `backd egress` | Run the [egress role](../functions/network/#egress-the-network-allowlist-completed), the only way a function process reaches the network |
| `backd config check`, `backd config fingerprint` | Validate `CONFIG_DIR` as startup would, or print its fingerprint (see [Deploying config](../operations/deploying/)) |
| `backd version` | Print the version |
| `backd databases [--collections]` | Print the MongoDB databases (or collections) the config uses, one per line (needs only `CONFIG_DIR`; see [least-privilege deployments](../operations/#least-privilege-deployments)) |
| `backd bootstrap --realm <realm> --email <email>` | Create a realm's first administrator (see [Command-line administration](../auth/cli/)) |
| `backd login --realm <realm>`, `logout`, `whoami` | Sign in to a running `backd` as an administrator and keep the session for the commands below |
| `backd user <command> --realm <realm> …` | Manage a realm's users through the admin API (see [Users and passwords](../auth/users/)) |
| `backd apikey <command> --realm <realm> …` | Manage a realm's API keys through the admin API (see [API keys](../auth/api-keys/)) |

## Your own config

Start a config tree from templates, then start `backd` on it:

`backd` needs MongoDB 8.2 as a replica set; a single node is fine. [MongoDB](../operations/#mongodb) shows how to start one.

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

`backd` itself serves plain HTTP (on `:8080` by default; here `9090`, to stay clear of the local stack). Put TLS in front of it as in the [production reference](../operations/production/).

See [Data model](../configuration/config-dir/#starting-from-a-template) for details. See [Operations](../operations/) for provisioning and [Documents](../api/documents/) for the REST endpoints.
