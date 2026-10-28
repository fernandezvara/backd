---
title: "Documentation"
description: "What backd is, and where to find things."
icon: "house"
weight: 1
toc: true
---

`backd` is an open-source (MIT), config-driven backend: one stateless Go service over MongoDB. You declare **realms**, **databases** and **collections** as files on disk (a JSON Schema per collection, optional indexes and access rules) and `backd` serves a schema-validated, queryable REST API for them, with built-in users and sessions, API keys, an admin API, and [server-side functions](build/functions/) for the logic clients can't be trusted with.

{{< hint warning >}}
Realms with `auth: disabled` are open to anyone who can reach `backd`: never expose them to the internet. Before exposing any realm, go through the [hardening checklist](operate/checklist/).
{{< /hint >}}

## Where to start

| I want to… | Go to |
|---|---|
| Try it in a few minutes | [Getting started](getting-started/) |
| Build a whole app, chapter by chapter | [Tutorial](tutorial/) |
| Build my own backend: schemas, HTTP API, users, functions, files, push | [Build](build/) |
| Run and administer a deployment: providers, monitoring, backup, hardening | [Operate](operate/) |
| Work on backd itself: workflow, architecture, API contract | [Contribute](contribute/) |
| See complete applications | [Examples](build/examples/) |
| Fix an error I met | [Troubleshooting](troubleshooting/) |

## What you get

- **Configuration as files.** Invalid config stops startup and names the file; `backd config check` runs the same checks in CI.
- **Schema-validated writes,** mirrored as a MongoDB validator, with field-level errors and optimistic concurrency (`ETag`, `If-Match`).
- **Queries without injection:** `where`, `order_by`, pagination and counts over an allowlisted query language.
- **Users, sessions and API keys** per realm, with brute-force protection, invitations, network restrictions and an audit trail; administration through an [admin API](operate/administration/admin/) and the [command line](operate/administration/cli/), never database credentials.
- **Access rules** (`rules:` in `collection.yaml`) decided per document, with read rules applied inside the database query.
- **Server-side functions** in isolated Deno processes: sync calls, [background jobs](build/functions/jobs/), [cron schedules](build/functions/cron/), [webhooks](build/functions/webhooks/), encrypted secrets, rate limits, idempotency and a network allowlist.
- **A production path:** a tested [reference deployment](operate/production/) (TLS, least-privilege MongoDB, encrypted backups), a [config fingerprint](operate/deploying/) so every instance runs the same configuration, and signed releases.

## Versions

The current version is **v0.9.5**. What each release added and what breaks is in the [release notes](https://github.com/fernandezvara/backd/releases) and the [changelog](https://github.com/fernandezvara/backd/blob/main/CHANGELOG.md); how to move a running deployment to a newer one is in the *Upgrading to* sections of [Operate](operate/).

`backd` has no installed base yet: until a stable release is declared, breaking changes are allowed and are listed in the release notes.
