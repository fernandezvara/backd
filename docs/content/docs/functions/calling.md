---
title: "Calling functions"
description: "Call a function over HTTP, from the JavaScript client or the CLI; the answers, limits, rate limits and idempotency."
icon: "api"
weight: 553
toc: true
---

## Calling a function

```sh
curl -X POST https://api.example.com/v1/shop/orders/_func/checkout \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"cart": "c1"}'
# → 200 {"order": "o7", "total": 1250}
```

- The body is the function's input (`ctx.input`), any JSON value; an empty body is `null`. The answer is its output. **`webhook` functions are different:** see [Webhooks](../webhooks/) — the raw body reaches `ctx.request.body` unparsed, `Content-Type` isn't required to be JSON, and the function's own response is returned as-is.
- In realms with `auth: enabled`, [API keys](../../auth/api-keys/) may always call; other callers need the function's [`invoke` rule](../reference/#invoke-rules) (`webhook` always needs one that allows anonymous callers). With `X-Backd-On-Behalf-Of`, an API key calls as that user.
- With `input.schema.json`, invalid input answers `400 validation_error`; with `output.schema.json`, an output that doesn't match answers `500 invalid_output` (and is logged). Neither applies to `webhook`.
- An `Idempotency-Key` header reaches the function as `ctx.idempotencyKey`, and also deduplicates the call itself (see [Idempotency](#idempotency)).
- `sync` functions run immediately and answer with their output, as above. `async` functions ([below](../jobs/)) answer `202` with a job to poll. `webhook` functions ([below](../webhooks/)) set their own response.

| Answer | When |
|---|---|
| `200` | A `sync` function returned; the body is its output |
| `202` | An `async` function was queued; poll `Location` for its result (see [Async jobs](../jobs/)) |
| *(the function's own choice)* | A `webhook` function's own response (see [Webhooks](../webhooks/)) |
| `4xx` with the function's code | The function threw `ctx.error(status, code, message)` (for `async`, this lands in the job's `result` instead; `webhook` functions answer errors through their own response, not `ctx.error`) |
| `400 validation_error` | The input doesn't match `input.schema.json` |
| `400 idempotency_key_required` | `function.yaml` has `idempotency: required` and the call has no `Idempotency-Key` |
| `401`, `403` | The `invoke` rule doesn't allow the caller |
| `404 not_found` | No such function |
| `409 request_in_progress` | A call with this `Idempotency-Key` is still running (see [Idempotency](#idempotency)) |
| `422 idempotency_key_reused` | This `Idempotency-Key` was already used with different input |
| `429 too_many_requests` | A `sync` or `async` function, or its realm, is at its [concurrency limit](#concurrency-limits) on this instance (`Retry-After: 1`); or the caller is over the function's [rate limit](#rate-limits) (`Retry-After` set to when it resets) |
| `500 function_failed` | The function threw another error, crashed, or went over its memory or CPU limit. The stack trace is in `backd`'s log only |
| `500 invalid_output` | The output is larger than `max_output`, or doesn't match `output.schema.json` |
| `500 secret_missing` | The function declares a secret that isn't set for its database or realm |
| `503 unavailable` | The executor isn't configured, doesn't answer, or runs its maximum of functions (`Retry-After: 1`); or a `webhook` function is at its [concurrency limit](#concurrency-limits) (`Retry-After: 1` — `503`, not `429`, since webhook senders retry on 5xx) |
| `504 function_timeout` | The function didn't finish within its `timeout`; it was stopped |

## From the JavaScript client and the command line

The same call, three ways:

```sh
# HTTP
curl -X POST https://api.example.com/v1/workshop/main/_func/order_total \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"order_id": "d3c9ljp8hc2g00b6s1m0"}'

# The command line: the same route and rules, with the session `backd login` stored (or BACKD_API_KEY)
backd functions invoke --function workshop/main/order_total --input order.json --url https://api.example.com
```

```js
// The JavaScript client
const { total } = await backd.db('main').fn('order_total', { order_id: id })
```

- `db.fn(name, input, opts)` returns the output of a `sync` function, and a `Job` handle for an `async` one ([Async jobs](../jobs/#knowing-when-a-job-finished)). `opts.idempotencyKey` sets the `Idempotency-Key` header. See [the client](../../clients/js/#functions).
- `backd functions invoke` needs `--function <realm>/<database>/<name>`, prints the output (and, when the server runs with `BACKD_DEV=true`, the function's logs and timing), and `--as <email>` calls on a user's behalf with an admin API key.
- **On behalf of a user.** An API key may add `X-Backd-On-Behalf-Of: <user id>` to call as that user: their `invoke` rule and their access rules apply. See [API keys](../../auth/api-keys/#acting-on-behalf-of-a-user).

## Concurrency limits

Two limits, both enforced by `backd` before it asks the executor to run anything:

- **`concurrency`** in `function.yaml` (default 10, 1–1000): how many calls of *that* function may run at once.
- **`functions.max_concurrency`** in `realm.yaml`: how many calls of *any* of the realm's functions may run at once, together. Unset means unlimited (bounded only by each function's own `concurrency`, and the executor's own cap below).

Either limit answers `429 too_many_requests` with `Retry-After: 1` once it's full for `sync`; a `webhook` function answers `503` instead (webhook senders retry on 5xx, not 4xx), and an `async` job waits in its queue rather than being refused.

**These limits are per instance, not a limit for the whole deployment.** Each `backd` process counts only the calls it is currently running. With `N` instances behind a load balancer, up to `N ×` the value can run across the deployment at once — there is no cross-instance coordination, on purpose (that would need a shared counter for every call, adding latency and a dependency on every invocation). If you need a deployment-wide cap, size these per-instance limits accordingly (limit ÷ number of instances). If what you actually need is a cap per *caller* rather than total throughput, that's [rate limits](#rate-limits) (`rate_limit` in `function.yaml`, shared across instances), not this.

The **executor** also caps its own total running processes (`EXECUTOR_MAX_PROCESSES`), independent of any one function or realm — the backstop if `concurrency`/`max_concurrency` are set too high for the machine. Size it, and its container's pids and memory limits, generously: each running function costs about 5 pids with `--single-threaded` (Deno's own threads plus the process itself); a 2048-pids container failed under 100 concurrent calls at 20 threads per process during F0's spike, which is why functions run `--single-threaded`.

## Rate limits

`rate_limit` in `function.yaml` caps how often **one caller** may call a function, so a function anyone can call — one that sends email, say — can't be abused as a relay by a single caller running it as many times as they like:

```yaml
rate_limit:
  per: user      # user or ip
  limit: 10
  window: 1m     # 1s to 24h
```

```json
{
  "error": {
    "code": "too_many_requests",
    "message": "this function's rate limit was reached; retry later",
    "request_id": "darf9mq5mh5g00aq9df0"
  }
}
```

- `per: user` counts by the signed-in user's id; a caller with no session (or an API key acting for no one in particular) falls back to `per: ip`, so the limit still applies before anyone signs in. `per: ip` always counts by client address.
- Over the limit answers `429 too_many_requests` with `Retry-After` set to when the window resets (unlike concurrency's fixed `Retry-After: 1`, which doesn't know when a slot will free up).
- **Shared across every `backd` instance** — the opposite of [concurrency limits](#concurrency-limits), which are deliberately per instance. Counters live in the realm's system database (`<realm>___system`, so `rate_limit` needs `auth: enabled`), reusing the same "N events per window" mechanism login throttling uses, under its own key namespace. Only functions that declare a `rate_limit` pay the extra write; a function without one is never throttled.
- **Startup warns** for any function whose `invoke` rule allows anonymous callers and declares no `rate_limit` — it names the function, so you can decide whether that's intentional.
- This is a deliberate exception to "rate limiting is the reverse proxy's job" (see the [hardening checklist](../../operations/checklist/)): only `backd` knows *who* is calling, since the proxy in front of it only sees IP addresses and can't tell two users on the same NAT apart, or the same user across IPs. The proxy baseline (a per-IP limit in front of `/v1/`) still matters and is unaffected by this.

## Idempotency

An `Idempotency-Key` header makes a retried call safe: a double-click, a client timeout that resends the request, or a proxy retry never runs a function twice for the same operation.

```sh
curl -X POST https://api.example.com/v1/shop/orders/_func/charge \
  -H "Authorization: Bearer $TOKEN" -H "Idempotency-Key: order-42-charge" \
  -H 'Content-Type: application/json' -d '{"amount": 1250}'
# → 200 {"charged": 1250}   (the function actually ran)

curl -X POST https://api.example.com/v1/shop/orders/_func/charge \
  -H "Authorization: Bearer $TOKEN" -H "Idempotency-Key: order-42-charge" \
  -H 'Content-Type: application/json' -d '{"amount": 1250}'
# → 200 {"charged": 1250}   (the same response, replayed — the function did not run again)
```

- **Same key, same input → the stored response, replayed** without running the function again (for `async`, the same job id, not a new job). **Same key, different input → `422 idempotency_key_reused`**: the key almost certainly meant a different operation, so backd refuses to guess which one you wanted. **Same key while the first call is still running → `409 request_in_progress`.**
- **Scoped to the function and the caller** (the signed-in user, or the API key acting for no one in particular): two different callers can never collide on the same client-chosen key, even by coincidence. A caller picks their own keys — a UUID per logical operation is the usual choice (e.g. one per order, generated once and reused across retries of that same order).
- `idempotency: required` in `function.yaml` refuses calls with no key (`400 idempotency_key_required`); `optional` (the default) makes deduplication opt-in per call.
- The key reaches the function as `ctx.idempotencyKey`, so it can be forwarded to a downstream idempotent API — Stripe's own `Idempotency-Key` on the charge you make from inside the function, for example — for the same protection all the way through.
- **Responses are stored only for calls with a key** — a deliberate second exception to "backd never stores a function's input or output" (the first is [async job results](../jobs/)) — kept for 24 hours (fixed, not configurable). A key you never send leaves nothing behind.
- **A failure that isn't really an outcome — the executor unreachable, busy, or a concurrency/rate limit hit — releases the key** instead of remembering it, so a genuine retry can actually run the function; only a real result (the function ran to completion, however it ended: success, its own thrown error, a timeout, a crash) is replayed.
- **Deduplicating [webhooks](../webhooks/)** is a different problem `Idempotency-Key` doesn't solve directly, since the sender chooses the request, not you (and most webhook senders don't send an `Idempotency-Key` at all): use the event id the sender already includes (e.g. Stripe's `event.id`) as a unique key in your own collection instead (a unique index refuses a duplicate insert). See [Webhooks](../webhooks/) for a worked example.
