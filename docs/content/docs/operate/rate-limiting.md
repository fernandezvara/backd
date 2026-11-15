---
title: "Rate limiting"
description: "backd's built-in request limits: the classes, what each counts, the instance, realm and API key settings, the 429 and RateLimit headers, and when a proxy is still needed."
icon: "gauge"
weight: 325
toc: true
---

`backd` limits requests itself, in each instance's memory. It protects an instance from a runaway script, a bug in an app or a casual flood. It is not a billing meter and not a defence against a distributed attack, and it does not replace the limits of your [proxy](#what-it-does-not-do), which stay in front.

## How it works

Every request in `/v1/{realm}/…` belongs to a **class**, and a class is a bucket of requests that refills over time: a client may burst up to the whole allowance and then runs at the refill rate. Over the limit, the answer is `429 too_many_requests`.

| Class | Which requests | Counted by |
|---|---|---|
| `anonymous` | any request **without credentials** | the client address |
| `auth` | requests without credentials to `/_auth`: sign-up, sign-in, steps, reset, magic links | the client address |
| `read` | `GET` and `HEAD` of a signed-in user or an API key | the user, or the API key |
| `write` | everything else on the data routes, `_batch` and function calls | the user, or the API key |
| `files` | file transfers (`…/_files/…`) | the user, or the API key |
| `admin` | the admin API (`/_admin`) | the user, or the API key |

Defaults, per minute: `anonymous` 120, `read` 600, `write` 120, `auth` 30, `files` 120, `admin` 300. They are meant to be out of the way of a normal app, not to be a quota.

- **Two stages.** A request without credentials is counted against its address before anything else happens, so a flood of guesses never reaches password hashing. Once the caller is known, it is counted against the user or the API key. A credential that is refused (a guessed token) counts against the address.
- **Client address.** Behind a proxy it is the one `TRUSTED_PROXIES` finds (see [client addresses](../#client-addresses-behind-a-proxy)); IPv6 is counted by `/64`, because one client usually controls a whole one.
- **A key acting as a user** counts against the **key**, never against the user it acts as, so `X-Backd-On-Behalf-Of` does not give a key more room.
- **Not limited:** `/healthz`, `/readyz` and `/metrics`, preflight (`OPTIONS`) requests, the internal listener (the calls functions make with `ctx.db`), the admin UI's static files, and anything outside `/v1/`. A realtime stream counts as one `read` when it opens.
- **Memory is bounded.** An instance keeps at most 100,000 buckets (about 12 MiB) and drops the idle ones. When the table is full, addresses it has not seen share one bucket per shard instead of growing it; `backd_rate_limiter_buckets` and `backd_rate_limiter_overflow_total` show it.

{{< hint style="warning" >}}
Limits are **per instance**. With N instances behind a load balancer a client can make up to N times the limit. For an exact, global limit use the proxy, a gateway or a CDN.
{{< /hint >}}

## What a client sees

A refused request is `429 too_many_requests` in the usual error envelope, with `Retry-After` (seconds). The answers of limited requests carry the state of the bucket they used, so a client can slow down before it is refused:

```http
HTTP/1.1 200 OK
RateLimit-Limit: 600
RateLimit-Remaining: 597
RateLimit-Reset: 4
```

`RateLimit-Reset` is the seconds until the bucket is full again. The headers are exposed to browsers (CORS). The four clients retry `429` with `Retry-After` when `retry` is on (off by default) and give the delay in the error (`retryAfter`, `retry_after`, `RetryAfter`).

## Configuration

Three levels, each replacing the one above for what it names.

**The instance**, with environment variables:

| Variable | Default | Meaning |
|---|---|---|
| `BACKD_RATE_LIMIT` | `on`; `off` in dev mode (`BACKD_DEV`) | `on` or `off`, for the whole instance |
| `BACKD_RATE_LIMIT_ANONYMOUS`, `_READ`, `_WRITE`, `_AUTH`, `_FILES`, `_ADMIN` | the defaults above | `<requests>/<window>`, such as `600/1m` or `30/10s` (a window from 1s to 24h), or `none` for no limit in that class |

**A realm**, in `realm.yaml`, replaces the instance's rate for the classes it names (it may raise or lower them, and cannot switch the instance's limiter off):

```yaml
rate_limit:
  read: 1200/1m
  write: 240/1m
  anonymous: none
```

**An API key**, for a service that legitimately sends more than a browser. It replaces every class for that key:

```sh
backd apikey create --realm shop --name importer --rate-limit 3000/1m
backd apikey create --realm shop --name batch --rate-limit none
```

The key's limit is shown by `backd apikey list` and recorded in the [audit trail](../administration/audit/) with the key's creation. A malformed value or an unknown class stops the load, with the key and what is valid. At start, `backd-server serve` logs the effective limits once (and the realms that set their own).

## What it does not do

- **It is not global.** See the warning above.
- **It does not replace the proxy.** The [production reference](../production/docker-compose/#rate-limits) keeps its nginx limits in front, which are exact and also protect TLS and connections; `backd`'s are a second line behind them.
- **It does not limit the sign-in throttle's job.** [Failed logins](../../build/auth/sessions/#brute-force-protection) are still counted per account and address, in MongoDB, across instances; a sign-in request passes both. Function [rate limits](../../build/functions/calling/#rate-limits) and concurrency limits are unchanged.
- **It forgets on restart.** Buckets are memory: a restarted instance starts full.
- **It does not meter usage.** There are no quotas or plans; storage quotas are separate.

## Troubleshooting

| You see | Likely cause | What to do |
|---|---|---|
| `429` from an app behind a proxy, for different users, all at once | the proxy's address is the client: `TRUSTED_PROXIES` is not set, so every user shares one `anonymous` or `auth` bucket | list the proxy in [`TRUSTED_PROXIES`](../#client-addresses-behind-a-proxy) |
| `429` in your tests or scripts | the limiter is on in production mode | `BACKD_RATE_LIMIT=off` for that stack, or raise the class; dev mode starts with it off |
| a service gets `429` on `read` while users do not | one API key carries all of a service's traffic | give the key its own limit with `--rate-limit`, or `none` |
| `backd_rate_limiter_overflow_total` grows | many different addresses or keys at once, or a limit set far too low | look at the access log's `rate_limited` field; consider the proxy |
