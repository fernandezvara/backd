# Design: built-in request rate limiting

- **Issues:** #980–#984 (milestone 47-rate-limiting)
- **Status:** decided (in-memory limiter per instance; configuration as in §4). The starting numbers of §4 are tuned in 47.2.
- **Related designs:** [metrics.md](./metrics.md), [kubernetes.md](./kubernetes.md); the sign-in throttle (`auth/sessions` "brute-force protection") and the function limits stay as they are

## 1. The problem

backd limits failed sign-ins and the functions that declare a limit, and nothing else; the documentation tells operators to put a proxy in front. The production reference has one, but the Kubernetes and DigitalOcean deployments, the compose template and anyone running the binary behind a bare load balancer ship without. A newcomer's API is open to a loop in a script.

## 2. What it is, and what it is not

A **per-instance, in-memory token bucket** applied in the HTTP layer. It protects an instance from a runaway client, a bug or a casual flood; it is not a billing meter and not a DDoS defence.

- **Per instance.** With N instances behind a load balancer the effective limit is up to N times the setting. The docs say so next to the number. A global, exact limit needs a proxy or a gateway; the nginx reference stays as defence in depth.
- **No MongoDB write per request.** The shared counter that the sign-in throttle uses (`login_attempts`) costs a write per call and would double the write load of the API. It stays where it is: sign-in, email, function limits.
- **Restart forgets.** Buckets are memory; a restarted instance starts full.

## 3. Who is limited, and in which class

| Class | Routes | Keyed by |
|---|---|---|
| `anonymous` | any route, request without credentials | client address |
| `read` | `GET`/`HEAD` of data, files metadata, aggregates, functions that are reads | the principal |
| `write` | `POST`/`PUT`/`PATCH`/`DELETE` of data, `_batch`, function calls | the principal |
| `auth` | `/_auth/*` (sign-in, sign-up, steps, reset), before the caller is known | client address |
| `files` | file transfers (uploads and downloads of bytes) | the principal, else the address |
| `admin` | `/_admin/*` | the principal |

- **The principal** is the user id of a session, or the id of an API key; an `as_user` request counts against the **key**, not the user it acts as. Anonymous requests have no principal, so they use `anonymous` by address.
- **The client address** comes from `clientIP`, which already believes `X-Forwarded-For` only from `TRUSTED_PROXIES`. IPv4 is keyed by address, IPv6 by its `/64`.
- **Two stages.** An address bucket runs first, before the caller is identified, so a flood of guesses never reaches password hashing; the principal bucket runs once the caller is known.
- **Exempt:** `/healthz`, `/readyz`, `/metrics`, `OPTIONS` preflights, the internal listener (the executor's `ctx.db` and callback calls would otherwise count against an end user), the static admin UI, and API keys that declare `rate_limit: none`. A realtime stream counts as one `read` when it opens; limiting how many streams stay open is a separate setting that already exists.
- **Memory is bounded.** The table holds at most 100,000 buckets per instance and drops idle ones after their window. When it is full, new anonymous addresses share an overflow bucket instead of growing the table; an attack with many addresses degrades to a shared limit, not to memory exhaustion.

## 4. Configuration

Instance defaults, in the environment (the instance's limits, like `BACKD_AGGREGATE_TIME`):

| Variable | Default | Meaning |
|---|---|---|
| `BACKD_RATE_LIMIT` | `on` (`off` in dev mode) | `on` or `off`, for the whole instance |
| `BACKD_RATE_LIMIT_<CLASS>` | see below | `<requests>/<window>`, e.g. `600/1m`, for `ANONYMOUS`, `READ`, `WRITE`, `AUTH`, `FILES`, `ADMIN` |

Starting values, per minute: `anonymous` 120, `read` 600, `write` 120, `auth` 30, `files` 120, `admin` 300. The bucket holds the whole window's allowance, so a client may burst to it and then runs at the refill rate.

Per realm, in `realm.yaml`, overriding the instance (a realm may raise or lower, never switch the instance off):

```yaml
rate_limit:
  read: 1200/1m
  write: 240/1m
```

Per API key: `backd apikey create … --rate-limit 3000/1m` (or `none`) for services that legitimately send more than a browser. Changing a key's limit is an audited change like its other settings.

A config that names an unknown class or a malformed value stops the load, with the key and what is valid. `backd-server serve` logs the effective limits once at start. Dev mode starts with the limiter off so tests and local scripts that hammer the API behave as before; the test stacks and CI set `BACKD_RATE_LIMIT=off` explicitly where they need to.

## 5. What the caller sees

- `429 too_many_requests` (the existing code), with `Retry-After` in seconds and the same body shape as the other limits.
- `RateLimit-Limit`, `RateLimit-Remaining` and `RateLimit-Reset` on responses of limited classes, so a client can slow down before it is refused. They describe the bucket the request used.
- The four clients already retry `429` and `503` when `retry` is on and honour `Retry-After`; the issue of 47.3 checks each does, and that the error carries the delay.
- The access log marks a refused request; `backd_rate_limited_total` gets a `class` label (it exists as `rate_limited` by kind today) and a gauge reports the table size.

## 6. Interactions

- **The sign-in throttle** is separate and stays: it counts failures per account and address, in MongoDB, across instances. A sign-in request passes both.
- **Function `rate_limit:`** is unchanged. A function call also counts as a `write` for its caller unless the function is declared a read.
- **Idempotent replays** (`Idempotency-Key`) count as requests; a retry loop that hits a `429` should wait for `Retry-After`.
- **Proxy limits** apply before backd's; a request refused there never arrives.

## 7. Order of work

The limiter as a package with its own tests and benchmarks (token bucket, eviction, the cap), then the middleware and the three configuration levels with the exemptions, then the defaults of the deployments and the test stacks, then docs: the "Rate limiting and abuse" section of Operate, the hardening checklist, the configuration reference, `realm.md`, the API errors page, the metrics page, the security model page.
