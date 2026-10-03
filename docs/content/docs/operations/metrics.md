---
title: "Metrics"
description: "Prometheus metrics from every backd process on a private port: how to turn them on, what they hold, and why the port must never be public."
icon: "monitoring"
weight: 415
toc: true
---

Every `backd` process can tell [Prometheus](https://prometheus.io/) how it is doing: requests, errors, database latency and, as the next steps land, functions, jobs and email. It is **off by default**.

## Turning it on

| Variable | Default | Meaning |
|---|---|---|
| `METRICS_ADDR` | empty (off) | Where this process serves `/metrics`, for example `:9090`. A listener of its own: it can't be `HTTP_ADDR` or `BACKD_INTERNAL_ADDR`. |
| `METRICS_TOKEN` | empty | At least 32 characters. When set, every request needs `Authorization: Bearer <token>`. Needs `METRICS_ADDR`. |

`backd serve` and `backd worker` each serve their own `/metrics`; a process that runs both (`serve --with-worker`) serves one. The listener answers `GET /metrics` and nothing else, and it is a separate server: whatever happens to it never touches the API.

```yaml
# prometheus.yml
scrape_configs:
  - job_name: backd
    static_configs:
      - targets: ["backd:9090"]
    authorization:            # only with METRICS_TOKEN
      credentials: "<the token>"
```

## The port must be private

The metrics hold no user ids, emails, addresses, tokens, key names, documents or request paths: labels come from fixed lists and from your configuration (route patterns, realm, database and function names). Still, they tell an attacker which realms and routes exist, how busy and how healthy the service is, and when it is slow. **Keep the port on a private network**: never publish it, never route it through the public proxy (the [production reference](../production/) answers `404` for `/metrics` at the edge, and its test checks it), and set `METRICS_TOKEN` when anything but Prometheus can reach the network it is on.

## What is measured

All names start with `backd_`; durations are in seconds.

| Metric | Labels | Meaning |
|---|---|---|
| `http_requests_total` | `method`, `route`, `status` | Requests answered. `route` is the router's pattern (`/v1/{realm}/{database}/{collection}`), never the real path; a request no route matched is `unmatched`. A method other than the common ones is `OTHER`. |
| `http_request_duration_seconds` | `method`, `route` | Time to answer (histogram). |
| `http_in_flight_requests` | | Requests being answered now. |
| `http_refusals_total` | `status` | Answers `401`, `403`, `429` and `503`. |
| `mongodb_operation_duration_seconds` | `operation`, `outcome` | Time MongoDB took to run each command (histogram). `operation` is the command (`find`, `insert`, `update`, `aggregate`, …, or `other`); `outcome` is `ok`, `error` or `timeout`. Measured by the driver for every database call, whatever part of backd made it. |
| `mongodb_up` | | `1` while the last check (every 15 seconds) of MongoDB worked, `0` when it failed. |
| `build_info` | `version`, `commit` | Always `1`. |

The Go runtime (`go_*`) and process (`process_*`) metrics are included.
