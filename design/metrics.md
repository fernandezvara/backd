# Design: Prometheus metrics

- **Issues:** feature 5.1 (#16)
- **Status:** decided
- **Related designs:** [internal-functions.md](./internal-functions.md) (functions, jobs, the internal listener), [email-delivery.md](./email-delivery.md) (email jobs), [account-lifecycle.md](./account-lifecycle.md) (erase jobs)

## 1. The idea

**Every backd process can tell Prometheus how it is doing, on an address that is never public.** Off by default; one variable turns it on. The numbers are counts, durations and queue sizes: never a user, an email, a token, a document or a request path with ids in it.

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Address | `METRICS_ADDR` (for example `:9090`); unset means no listener at all. Its own listener, not the public one and not the internal one (functions' callback credentials stay on theirs) |
| Who exposes | every command that runs a long process: `serve`, `worker`, `egress` and the executor each serve their own `/metrics` |
| Access | the metrics port is private: network isolation first, plus an optional bearer token (`METRICS_TOKEN`, compared in constant time). The docs say it must not be reachable from outside, even though nothing sensitive is in it |
| Library | `prometheus/client_golang`, with its own registry (no global default collectors that leak host details) |
| Labels | only bounded values: route *pattern*, method, status class or code, realm, database, function name, job kind, outcome. Unknown routes collapse to one label. Never a user id, email, address, token, key name, IP, document id, query or free text |
| Database latency | a must: a histogram for every MongoDB operation by operation name and outcome, measured with the driver's command monitor (one hook for every call; the operation is the MongoDB command name, a closed list, anything else is `other`) |
| Jobs | queue depth and oldest-job wait by kind and realm, from the database, **cached** (refreshed every 15 s by a background loop with a timeout, never on scrape) |
| Sessions | counters of sessions created and ended (by reason; sessions that expire on their own are not counted: MongoDB removes them), not a live count |
| Password hashing | its queue depth and wait come later (separate issue) |
| Production reference | a Prometheus container in the stack, scraping every backd process on start, with the metrics ports on a private network; a test checks that the series exist and that the public edge doesn't serve `/metrics` |
| Local stack | the same: the compose file starts a Prometheus configured to scrape on start |
| Alerts | sample rules shipped as a file and documented (stuck jobs, erase needs attention, 5xx rate, database latency) |

## 3. Metrics

Names start with `backd_`. Durations are seconds; histograms use fixed buckets (documented).

**API (`serve`)**

- `backd_http_requests_total{method,route,status}`: route is the router's pattern (`/v1/{realm}/{database}/{collection}`), unmatched requests are `route="unmatched"`. Status is the code.
- `backd_http_request_duration_seconds{method,route}`
- `backd_http_in_flight_requests`
- `backd_auth_refusals_total{status}` for 401, 403, 429 and 503 (also in the first family; this one is the quick alert).
- `backd_sessions_created_total{realm}` and `backd_sessions_ended_total{realm,reason}`
- `backd_rate_limited_total{realm,scope}` (login throttle, function limits, email limits)

**MongoDB (every process that uses it)**

- `backd_mongodb_operation_duration_seconds{operation,outcome}`: operation is a fixed set (`find`, `insert`, `update`, `delete`, `aggregate`, `count`, `transaction`, `index`, …); outcome is `ok`, `error` or `timeout`.
- `backd_mongodb_up` is 1 while the last ping worked.

**Functions and jobs (`serve` for calls, `worker` for jobs)**

- `backd_function_invocations_total{realm,function,status}` and `backd_function_duration_seconds{realm,function}` (status is the terminal status: ok, error, timeout, refused, …).
- `backd_function_refusals_total{realm,function,reason}` for limits (rate, concurrency, depth, idempotency conflicts).
- `backd_function_idempotent_replays_total{realm,function}`.
- `backd_executor_errors_total{kind}` (executor unreachable, runner crash, bundle problem).
- `backd_jobs_queue_depth{realm,kind}` and `backd_jobs_oldest_wait_seconds{realm,kind}` (cached, see above); kinds: `function`, `schedule`, `email`, `erase`.
- `backd_jobs_expired_leases_total`, `backd_jobs_completed_total{realm,kind,status}`.

**Email and erasure (added to the issue's list)**

- `backd_email_jobs_total{realm,kind,outcome}` (sent, retried, failed, limited).
- `backd_erase_jobs_total{realm,outcome}` and `backd_erase_needs_attention` (a gauge: jobs failed after their attempts, from the cached job query).

**Executor and egress**

- Executor: `backd_executor_runs_total{status}`, `backd_executor_run_duration_seconds`, `backd_executor_running`.
- Egress: `backd_egress_requests_total{outcome}` (allowed, denied by host, denied by address, error), without host names in labels.

**Build and process**

- `backd_build_info{version,commit,go_version}`, plus the Go runtime and process collectors (they hold no request data).

## 4. Security

- **Private by design.** The listener binds where `METRICS_ADDR` says, and the docs say plainly: keep it on a private network; never publish it, never proxy it. The public nginx has no route for it (the production test checks that `/metrics` on the public address is `404`).
- **Optional token.** With `METRICS_TOKEN`, every request needs `Authorization: Bearer <token>`, compared in constant time; without it, the endpoint answers anyone who can reach it. Prometheus supports the header natively.
- **Nothing sensitive in the data.** Label values come from a closed list or from the configuration (realm, database and function names are the operator's own, bounded by the config). A test renders every metric after exercising each flow and fails on anything that looks like an email, a token, an id or an address.
- **No cardinality attack.** Routes are patterns; an unmatched path is one label; user input never becomes a label. Histograms use fixed buckets.
- **No cost attack.** The endpoint is read-only and never queries the database on scrape: job gauges come from a cache refreshed by a background loop with its own timeout. The listener has read and write timeouts and no other routes.
- **Realm names.** They appear in labels. They aren't secret in backd (they are in every URL), but they are one more reason the port must be private.
- The listener is a separate server: a crash or overload of it never touches the API, and the API's rate limits and middleware don't apply to it (it has its own).

## 5. How it is built

- `internal/metrics`: the registry, the metric definitions and the HTTP handler (with the token check). Each process builds it from its settings and passes small interfaces (`Observer`s) to what it measures, so packages don't import a global.
- Request metrics are a middleware around the chi router, using the matched pattern after routing.
- Database timing wraps the calls to the driver in `internal/mongodb` in one place.
- A background `jobs` collector refreshes the queue gauges with an aggregation per realm; failures keep the previous values and bump a `backd_metrics_refresh_errors_total` counter.
- Settings: `METRICS_ADDR` (empty = off) and `METRICS_TOKEN`; validated at startup (a token without an address is an error; an address that is the public listener's is an error).

## 6. Deployment

- **Production reference:** `prometheus` service on a new private `metrics` network, with the `backd`, `worker`, `executor` and `egress` services on it as well (their metrics ports aren't published). Its config scrapes them on start; the data volume is persistent; the UI isn't published (bind to localhost for operators who tunnel in). The reference test checks that Prometheus has the targets up and that key series exist, and that nginx answers `404` for `/metrics`.
- **Local stack:** the same Prometheus, published on `127.0.0.1:9090`, scraping on start.
- **Alert rules:** `deploy/production/prometheus/alerts.yml` (sample): `BackdDown`, `BackdHighErrorRate`, `BackdMongoSlow`, `BackdJobsStuck`, `BackdEraseNeedsAttention`, `BackdEmailFailing`. Documented with the metrics reference.

## 7. Documentation

A page "Metrics" under Operations (every metric, labels, buckets, the scrape config, the alert rules, and the privacy rule), mentions in the production page and the hardening checklist ("the metrics port is private"), and `SECURITY.md` scope.

## 8. Steps

1. This design and the issue text.
2. `internal/metrics`, settings, the listener and token, request and build metrics, `serve` only; docs page started.
3. MongoDB latency.
4. Sessions, rate limits, functions and executor metrics.
5. Jobs (cached), email, erase; `worker` and `egress` expose theirs.
6. Prometheus in the production reference and the local stack, the alert rules, the tests.
7. Review of the endpoint itself (attack-script lines: the public address has no `/metrics`; the token is required when set), docs and `SECURITY.md`.

## 9. Later

Password-hashing queue depth and wait; OpenTelemetry tracing (#38).
