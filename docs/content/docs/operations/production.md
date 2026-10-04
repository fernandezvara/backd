---
title: "Production deployment"
description: "A tested reference deployment: TLS, rate limits, client addresses, MongoDB with authentication and TLS, least-privilege provisioning, and a hardening checklist."
icon: "rocket_launch"
weight: 410
toc: true
---

`backd` deliberately leaves some protections to the infrastructure around it: TLS, request rate limits and sign-up spam, body size limits at the edge, and database security. The repository ships a reference deployment that puts all of these in place. It lives in [`deploy/production/`](https://github.com/fernandezvara/backd/tree/main/deploy/production), and CI tests it end to end on every change.

It uses Docker Compose, but each part maps directly onto other platforms: a Kubernetes ingress, a cloud load balancer, a managed MongoDB.

## Architecture

```
 internet ──443──▶ nginx ─────────▶ backd ─────────▶ MongoDB
          ──80───▶ (edge)  edge     (verify mode,   data   (auth + TLS,
                   TLS, limits,     least-privilege  net   internal network)
                   body size, XFF   user)                      ▲
                                       │                        │ admin user
                                       │  func net    provision job
                                       ▼                (backd provision + grants; one-shot)
                                    executor ──▶ egress ──▶ internet
                                    (one Deno       (func net + uplink)
                                     process/call)
                                       ▲
                                       │  func net
                                    worker (claims and runs async jobs)
                                       ▲
                                       │  func net: scrapes each :9090
                                    prometheus (metrics, alert rules)
```

- **nginx** is the only thing reachable from outside. It terminates TLS, redirects HTTP to HTTPS, rate-limits requests, caps body sizes and sets the client address that `backd` sees. Its access log (`edge` format) records the path without the query string, because the links in [emails](../../functions/email/#links-and-pages) carry their token there.
- **backd-admin** is the same image and database as backd, with the admin API turned on (`BACKD_ADMIN_API=true`) and the public instance's turned off. nginx sends only `/v1/{realm}/_admin` to it, and only from `ADMIN_ALLOW_FROM`; the CLI's `BACKD_URL` points at it. See [Admin API](#admin-api).
- **backd** runs with `PROVISION_MODE=verify` as a MongoDB user that can only read and write documents. It has a read-only filesystem, no Linux capabilities and CPU and memory limits.
- **MongoDB** requires TLS and authentication, and sits on an internal network with no published ports.
- A **provision job** runs before `backd` with admin credentials: it creates collections, validators and indexes, and grants `backd`'s user access to exactly the collections the config uses.
- **Backup and restore jobs** (Compose profile `ops`) write age-encrypted backups and restore them. See [Backup and restore](../backup/).
- **The functions executor, egress and a worker** run [functions](../../functions/) with the network placement the [security model](../../auth/security/#server-side-functions-egress-and-network-placement) requires: see [Functions](#functions-executor-egress-and-worker) below.

## Try it

You need Docker with Compose (or Podman with podman-compose), `openssl` and `curl`.

```sh
cd deploy/production
./setup.sh                                    # certificates and passwords
docker compose --env-file .env up -d --build
curl --cacert secrets/ca.pem https://localhost/v1/blog/main/posts
```

`setup.sh` writes what must never be committed, in `secrets/` and `.env` (both gitignored): a private CA and a certificate for MongoDB, the key file of MongoDB's replica set, a self-signed certificate for nginx, and random passwords. It never overwrites existing files. Set `SERVER_NAME=api.example.com` when running it to issue the nginx certificate for your host name.

{{< hint style="tip" title="Already done for you" >}}
This is the tested starting point, not a template to trust blindly: `make prod-test` checks every protection described on this page (TLS, limits, isolation, least privilege, backups and a restore). Change something, and run it again. Keep `secrets/` and `.env` out of version control.
{{< /hint >}}

The stack serves the `blog` realm from `examples/config`, **built into the images** rather than mounted: `app/Dockerfile` builds `backd` with the config (and checks it during the build), and `ops/Dockerfile` puts the same config in the provision job's image. Every container runs exactly the config of that checkout; see [Deploying config](../deploying/). To serve your own config, build your own config image (`FROM ghcr.io/fernandezvara/backd:<version>` + `COPY config /config`) and replace `examples/config/blog` in both Dockerfiles, or point the `backd` and `provision` services at your images.

`make prod-test` (or `deploy/production/test.sh`) starts a separate copy of the stack with fresh secrets and checks every protection described below: TLS versions, HSTS, redirects, 413 and 429 answers, client addresses with a spoofed `X-Forwarded-For`, MongoDB refusing plain and unauthenticated connections, what the service user may and may not do, `verify` mode refusing a database that differs from the config and a config other than the one provisioned, `/readyz` reporting the config's fingerprint, and an encrypted backup and restore. Then it removes the copy.

Run one-off jobs (provision, backup, restore) with `--no-deps` while the stack is up. Without it, podman-compose recreates MongoDB and stops the services that depend on it.

## TLS

nginx listens on 443 with TLS 1.2 and 1.3 only (Mozilla's "intermediate" profile), redirects port 80 to HTTPS, and sends `Strict-Transport-Security: max-age=63072000`.

Replace `secrets/tls/edge.crt` and `edge.key` with a real certificate, for example from Let's Encrypt (certbot or acme.sh, with a reload of nginx after renewal), or terminate TLS at your cloud load balancer instead. Only add `includeSubDomains` to the HSTS header if every subdomain serves HTTPS.

## Rate limits

`backd` itself only throttles [failed logins](../../auth/sessions/#brute-force-protection). Everything else is limited in nginx, per client address:

| Zone | Applies to | Rate | Burst |
|---|---|---|---|
| `auth` | `/v1/{realm}/_auth/` `signup`, `login`, `password`, `email` and the emailed-link flows (`verify-email`, `verify-email/resend`, `reset-password`, `reset-password/request`, `confirm-email-change`, `accept-invitation`) | 10 per minute | 5 |
| `api` | everything under `/v1/` (including the two above) | 20 per second | 40 |
| `conn` | open connections | 20 at a time | – |

Requests over a limit get `429` with `Retry-After: 60` and the same error envelope as `backd` (`"code": "too_many_requests"`), so clients handle both alike.

Signup, login and the account flows get the strictest limit because each one costs the server a 64 MiB argon2id hash, sends an email or creates an account. (The hosted pages of the emailed links share it: opening a link and submitting its form are two requests.) With `signup: open`, this limit is your main defence against sign-up spam; consider `signup: invite` if you don't need open registration.

Tune the numbers in `nginx/backd.conf.template` to your traffic:

- Limits are counted per nginx instance. With several instances, the effective limit multiplies; use your load balancer's or CDN's rate limiting if you need a global one.
- IPv6 clients are counted per address (/128), and one client often controls a whole /64. `backd`'s own login throttling groups IPv6 addresses by /64.
- Public collections that anonymous users can read can still be scraped within the `api` limit; cache them at a CDN if that matters. Document responses carry the `Vary` and `Cache-Control` headers that make this safe (see [Caching](../../api/#caching)).

## Admin API

What isn't served can't be attacked, so this reference splits the instances: the **public** `backd` runs with `BACKD_ADMIN_API=false` and serves no `/_admin` route at all (they answer `404` like any unknown route, even to someone who reaches it directly), and `backd-admin` is a second instance of the same image on the same database with the admin API on. nginx sends `/v1/{realm}/_admin` to it and nothing else; everything else goes to the public instance. The CLI and the admin UI use the internal instance: the operations container's `BACKD_URL` is `http://backd-admin:8080`. If you don't need the admin API on the internet-facing path, give a public instance `BACKD_ADMIN_API=false` and keep one instance with it on a private network.

The [admin API](../../auth/admin/) always needs an API key or an admin session. As a second layer, nginx can also restrict where it may be called from. Set `ADMIN_ALLOW_FROM` in `.env` to the one IP address or network your operators or back-office services use:

```sh
ADMIN_ALLOW_FROM=203.0.113.0/24
```

Requests from anywhere else get `404`, in `backd`'s error envelope, as if the route didn't exist. The default, `all`, leaves it open to any client with a valid key. For several networks, add more `allow` lines to the `_admin` location in `nginx/backd.conf.template`. The test checks both sides: the allowed address reaches `backd`, and others get `404`.

## Request body size

`client_max_body_size 1m` matches `backd`'s `MAX_BODY_BYTES` (1 MiB), so oversized bodies are refused at the edge with `413` (`"code": "payload_too_large"`) and never reach `backd`. Change both together. nginx also limits how long a client may take to send headers and body (10 seconds each), which blocks slow-send attacks.

## Client addresses

`backd` uses the client address for [login throttling](../../auth/sessions/#brute-force-protection) and logs it in the access log (`"client"`). Two settings must agree:

1. **nginx overwrites `X-Forwarded-For`** with the address it sees (`proxy_set_header X-Forwarded-For $remote_addr`). It never appends to it: nginx is the edge, so any value a client sent is untrustworthy.
2. **`TRUSTED_PROXIES` names exactly nginx's address**, and nothing else. `compose.yaml` gives nginx a fixed address on the edge network (`PROXY_IP`, default `172.30.80.10`) and uses that same variable for `TRUSTED_PROXIES`, so they can't drift apart.

{{< hint danger >}}
Don't set `TRUSTED_PROXIES` to whole private ranges (`10.0.0.0/8`, `172.16.0.0/12`…). Any other container or host in those ranges could then send requests with a forged `X-Forwarded-For` and pick its own address, dodging per-address login throttling or framing another client. Name the proxy's exact address.
{{< /hint >}}

If a load balancer or CDN sits in front of nginx, nginx must learn the client address from it first. Use nginx's `realip` module with the load balancer's exact addresses (`set_real_ip_from <lb address>; real_ip_header X-Forwarded-For;`), and keep `TRUSTED_PROXIES` set to nginx alone.

Check that nginx sees real client addresses: its access log shows them first on each line, and `backd`'s access log shows the same value as `"client"`. Some container networks replace the client address on published ports. Rootless Podman on a bridge network, for example, makes every request appear to come from nginx itself. All clients then share one rate-limit bucket. Use rootful Docker or Podman, host networking for nginx, or a load balancer that forwards the address.

## MongoDB with authentication and TLS

- `mongod` runs with `--tlsMode=requireTLS`: connections without TLS are refused. Its certificate comes from the private CA that `setup.sh` creates. Clients verify it (`tls=true&tlsCAFile=…` in `MONGO_URI`) but don't present certificates of their own.
- Authentication is on: `MONGO_INITDB_ROOT_USERNAME` and `MONGO_INITDB_ROOT_PASSWORD` create the admin user on first start. Only the provision job gets the admin credentials. `backd` gets its own user.
- MongoDB is only on the internal `data` network: nothing is published, and nginx can't reach it.
- It runs as a single-node [replica set](../#mongodb) (`--replSet=rs0`). With authentication, that needs a key file: `setup.sh` creates `secrets/mongo/keyfile`, and the container installs a private copy for the `mongodb` user before starting, because `mongod` refuses a key file others can read. The health check initiates the set on first start (as the admin user) and reports healthy once it is the primary.
- Replica set members connect to each other with TLS, presenting their own certificate, and a single member connects to itself (for example to commit index builds). So MongoDB's certificate must allow **client** authentication as well as server authentication (`extendedKeyUsage=serverAuth,clientAuth`); `setup.sh` issues it that way. With a server-only certificate, members refuse each other ("unsuitable certificate purpose") and index builds on collections with data never finish.

With a managed MongoDB (such as MongoDB Atlas), TLS and authentication are already required. Use the provider's connection string, restrict network access to your `backd` hosts, and create the custom role below for `backd`'s user.

`backd`'s connection string in the reference:

```sh
MONGO_URI=mongodb://backd:<password>@mongo:27017/?replicaSet=rs0&authSource=admin&tls=true&tlsCAFile=/etc/backd/tls/ca.pem
```

## Least-privilege provisioning

`backd` runs with `PROVISION_MODE=verify`. It never changes collections, validators or indexes; if MongoDB differs from the config, it refuses to start and lists the differences (see [Provisioning](../../operations/#provisioning)). Changes are applied by the provision job (`ops/provision.sh`), which runs with the admin user before `backd` starts:

1. `backd provision` creates or updates collections, validators, indexes and system databases.
2. `ops/grants.js` gives `backd`'s user a custom role, `backdApp`, allowing exactly this:

| On | Actions | Why |
|---|---|---|
| each collection that `backd databases --collections` lists | `find`, `insert`, `update`, `remove` | the API's document operations |
| each realm's `<realm>___system.audit` and `<realm>___system.invocations`, instead | `find`, `insert` only | the [audit trail](../../auth/audit/) and [invocation history](../../functions/logs/) are append-only, even for `backd` — not even its own user can change or delete a record; MongoDB's TTL monitor expires them |
| each realm's `<realm>___system.jobs`, instead | `find`, `insert`, `update` (no `remove`) | an [async job](../../functions/jobs/) is only ever claimed and completed in place, never deleted by `backd` itself; MongoDB's TTL monitor expires it |
| `backd___deployment.realms`, instead | `find` only | `verify` compares the provisioned [config fingerprint](../deploying/#what-backd-enforces); only the provision job records it |
| the same collections | `listIndexes` | `verify` compares indexes |
| each of their databases | `listCollections` | `verify` compares collections and validators |

The role names collections, not whole databases, because MongoDB lets a user with `insert` on a database create any collection in it. The user can't create collections or indexes, change validators, drop anything, or touch other databases. Collections removed from the config lose their grant the next time the job runs.

**Run the provision job after every config change** and before restarting `backd`:

```sh
docker compose --env-file .env run --rm --no-deps provision
docker compose --env-file .env up -d backd
```

If you use a different platform, the same two commands do the job: `backd provision` with an admin `MONGO_URI`, and the grants for each line of `backd databases --collections`.

## Administrators and API keys

Create the realm's first administrator once, with the running container (it writes to MongoDB with the least-privilege user):

```sh
docker compose --env-file .env exec backd /backd bootstrap --realm blog --email ops@example.com
```

From then on, administrators work through the admin API with the `backd` CLI ([command-line administration](../../auth/cli/)). The edge only lets `ADMIN_ALLOW_FROM` reach `/v1/{realm}/_admin`, so log in from there:

```sh
backd login --realm blog --url https://api.example.com --email ops@example.com
backd apikey create --realm blog --name billing-2026q4 --expires 90d
```

Or use the ops image, which has the CLI and reaches the internal instance (`backd-admin`) directly (the credentials file lives in the throwaway container, so log in and work in one run):

```sh
docker compose --env-file .env run --rm -it --no-deps -e BACKD_URL=http://backd-admin:8080 provision \
  sh -c 'backd login --realm blog --email ops@example.com && backd apikey create --realm blog --name billing-2026q4 --expires 90d'
```

Follow the [expiry and rotation policy](../../auth/api-keys/#expiry-and-rotation-policy): create keys with `--expires` (90 days or less), one per service, and rotate them before they expire. `backd` warns at startup about keys without an expiry or close to it.

## Rotating passwords

To change a MongoDB password, edit it in `.env`. For `backd`'s user and the `backup` user, run the provision job (it sets their passwords from `.env`), then recreate `backd`. For the admin user, change it in MongoDB first (`db.changeUserPassword`), then in `.env`.

## Functions: executor, egress and worker

The stack runs [functions](../../functions/) with the network placement the [security model](../../auth/security/#server-side-functions-egress-and-network-placement) describes as layer 3, wired up for real:

- **`executor`** runs one Deno process per function call. It's on its own network, `func`, whose only other member is `backd`; it has no route to `data` (MongoDB) or the internet at all — not even for a raw TCP connection that bypasses `backd egress` entirely (layer 3). Read-only filesystem, no Linux capabilities, a tmpfs for its bundle cache.
- **`egress`** is `func`'s only way out: it also joins `uplink` (a network with a route to the internet) and bridges the two, refusing loopback, private, link-local (cloud metadata), CGNAT, multicast and unspecified addresses (layer 2). `backd`'s own address (`EGRESS_ALLOW_PRIVATE`) is its one exception, so functions can still reach `backd` through it.
- **`worker`** claims and runs [async jobs](../../functions/jobs/), as its own deployment (scale it independently of `backd`) rather than folded in with `serve --with-worker` — the [local stack](#try-it) uses the smaller, folded-in form instead, since it's a single-user convenience, not a hardened deployment.
- **`prometheus`** scrapes the private [metrics](../metrics/) port of `backd`, `worker`, `executor` and `egress` (each asks for `METRICS_TOKEN`, which function processes never have) and evaluates the sample alert rules. It is on `func`, which has no route out, and its own web port listens on its container's loopback, so function processes can't reach it. Nothing about metrics is reachable from the internet.
- **`data`** gets a fixed subnet so MongoDB has a known address: not for functions to reach (they can't — that's the whole point), but so this deployment's own test (`test.sh`) can declare it in a function's `network:` allowlist and prove layer 3 refuses it anyway, from a real function call (`functions/netprobe`, a function-only realm that exists solely for this).

Sizing (`compose.yaml`'s `executor` service): `EXECUTOR_MAX_PROCESSES` functions running at once, each using its declared `memory` (128 MiB by default) plus about 130 MiB of overhead — size `mem_limit` for that total, and `pids_limit` for about 5 pids per running function (Deno's `--single-threaded` mode; see [Running the executor](../../functions/running/#running-the-executor)). The reference ships modest defaults (10 processes); raise `EXECUTOR_MAX_PROCESSES`, `mem_limit` and `pids_limit` together for more load, and consider `WORKER_CONCURRENCY` (unset here, default 10) if async jobs need more headroom than sync calls.

**A per-IP baseline rate limit on `/v1/{realm}/{database}/_func/`** (nginx's `func` zone, 2 requests/second with a burst of 5) sits in front of whatever a function itself declares (its own [`concurrency`](../../functions/calling/#concurrency-limits) or [`rate_limit`](../../functions/calling/#rate-limits)) — the only edge protection an anonymously-reachable function (most often a [`webhook`](../../functions/webhooks/)) gets besides its own declared limit.

**Secrets:** `BACKD_EXECUTOR_TOKEN`, `BACKD_CALLBACK_KEY` and `BACKD_EGRESS_KEY` are generated by `setup.sh` like the rest of `.env`. `BACKD_SECRETS_KEY`, also generated there, additionally decrypts every [function secret](../../functions/secrets/) ever stored — **back it up separately from database backups** (see [Backup and restore](../backup/)): losing it loses every stored secret, and storing it alongside an encrypted database backup defeats that encryption (the backup and its own key would travel together).

To serve your own functions, add a Deno project under your config's `<realm>/<database>/_functions/` (see [Functions](../../functions/)) and rebuild; `functions/netprobe` in this reference exists only for its own test and isn't a usable app — review or remove it the same way you would the blog example.

## Hardening checklist

Before going public, go through the [hardening checklist](../checklist/): it covers this deployment, and your realms, credentials and operations.
