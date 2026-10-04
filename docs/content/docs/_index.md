---
title: "Documentation"
description: "What backd is, and where to find things."
icon: "home"
weight: 1
toc: true
---

`backd` is an open-source (MIT), config-driven backend: one stateless Go service over MongoDB. You declare **realms**, **databases** and **collections** as files on disk (a JSON Schema per collection, optional indexes and access rules) and `backd` serves a schema-validated, queryable REST API for them, with built-in users and sessions, API keys, an admin API, and [server-side functions](functions/) for the logic clients can't be trusted with.

{{< hint warning >}}
Realms with `auth: disabled` are open to anyone who can reach `backd`: never expose them to the internet. Before exposing any realm, go through the [hardening checklist](operations/checklist/).
{{< /hint >}}

## Where to start

| I want to… | Go to |
|---|---|
| Try it in a few minutes | [Getting started](getting-started/) |
| Build a whole app, chapter by chapter | [Tutorial](tutorial/) |
| Describe my data: schemas, indexes, realms | [Configuration](configuration/) |
| Read, write and query documents | [HTTP API](api/) and the [JavaScript client](clients/js/) |
| Control who can do what: users, sessions, API keys, rules, administration | [Authentication](auth/) |
| Run server-side logic: privileged changes, background jobs, cron, webhooks | [Functions](functions/) (start with the [Quickstart](functions/quickstart/)) |
| Put it in production, back it up, harden it | [Operations](operations/) |
| See complete applications | [Examples](examples/) |
| Work on backd itself | [Development](development/) |

## What you get

- **Configuration as files.** Invalid config stops startup and names the file; `backd config check` runs the same checks in CI.
- **Schema-validated writes,** mirrored as a MongoDB validator, with field-level errors and optimistic concurrency (`ETag`, `If-Match`).
- **Queries without injection:** `where`, `order_by`, pagination and counts over an allowlisted query language.
- **Users, sessions and API keys** per realm, with brute-force protection, invitations, network restrictions and an audit trail; administration through an [admin API](auth/admin/) and the [command line](auth/cli/), never database credentials.
- **Access rules** (`rules.yaml`) decided per document, with read rules applied inside the database query.
- **Server-side functions** in isolated Deno processes: sync calls, [background jobs](functions/jobs/), [cron schedules](functions/cron/), [webhooks](functions/webhooks/), encrypted secrets, rate limits, idempotency and a network allowlist.
- **A production path:** a tested [reference deployment](operations/production/) (TLS, least-privilege MongoDB, encrypted backups), a [config fingerprint](operations/deploying/) so every instance runs the same configuration, and signed releases.

## Versions

- **v0.2.0** added users, single sign-on per realm, access rules and secure administration. See [upgrading to v0.2.0](operations/#upgrading-to-v020).
- **v0.3.0** adds server-side functions, the flag-based command line, listing of jobs, and this documentation's Functions section. See the [release notes](https://github.com/fernandezvara/backd/releases) for what changed and what breaks.
- **v0.4.0** adds email (verification, password reset, address changes and invitations, through a delivery function you write or the Postmark example), hosted pages for the emailed links, custom emails from functions, erasure (deactivate by default, erase on request), Prometheus metrics with Grafana dashboards, the example attack scripts that CI runs, and the JavaScript client on npm (`backd-js`). See [upgrading to v0.4.0](operations/#upgrading-to-v040).
- **v0.5.0** adds cursor pagination (`after` and `next_cursor`), API keys limited to grants (`scopes`), HttpOnly session cookies for browser apps, `Idempotency-Key` on document creates and batches, dates stored as real dates (`x-backd-store: date`), `backd rules test`, shorter sessions for administrators, a login throttle that holds across instances, and [a tutorial](tutorial/) that builds a complete app. The JavaScript client moves to 0.4.0 with the matching options. See [upgrading to v0.5.0](operations/#upgrading-to-v050).

`backd` has no installed base yet: until a stable release is declared, breaking changes are allowed and are listed in the [release notes](https://github.com/fernandezvara/backd/releases).
