---
title: "Getting started"
description: "What backd is, its core concepts and main features, and where to go next."
icon: "rocket-launch"
weight: 100
toc: true
---

`backd` is an open-source (MIT), config-driven backend: one stateless Go service over MongoDB. You declare **realms**, **databases** and **collections** as files on disk (a JSON Schema per collection, optional indexes and access rules) and `backd` serves a schema-validated, queryable REST API for them, with built-in users and sessions, API keys, an admin API, and [server-side functions](../build/functions/) for the logic clients can't be trusted with.

Core concepts:

```
realm            realm.yaml settings; shared user pool with single sign-on
 └─ database     one application's data  → MongoDB database <realm>__<database>
     └─ collection   documents validated by schema.json → one MongoDB collection
         └─ document  JSON object with server-owned id and _meta
```

{{< hint warning >}}
Realms with `auth: disabled` are open to anyone who can reach `backd`: never expose them to the internet. Before exposing any realm, go through the [hardening checklist](../operate/checklist/).
{{< /hint >}}

## What you get

- **Configuration as files.** Invalid config stops startup and names the file; `backd config check` runs the same checks in CI.
- **Schema-validated writes,** mirrored as a MongoDB validator, with field-level errors and optimistic concurrency (`ETag`, `If-Match`).
- **Queries without injection:** `where`, `order_by`, pagination and counts over an allowlisted query language.
- **Users, sessions and API keys** per realm, with brute-force protection, invitations, a second factor, consents and an audit trail.
- **Access rules** (`rules:` in `collection.yaml`) decided per document, with read rules applied inside the database query.
- **Server-side functions** in isolated Deno processes: sync calls, background jobs, cron schedules, webhooks, encrypted secrets, rate limits and a network allowlist.
- **Files, push, organizations and realtime:** S3-compatible storage per realm, FCM/APNs/Web Push senders, B2B teams and roles, and server-sent events of the changes a caller may read.
- **A production path:** a tested [reference deployment](../operate/production/), a config fingerprint so every instance runs the same configuration, and signed releases.

## Where to go next

| I want to… | Go to |
|---|---|
| Run a realm in a few commands: binary, template, compose | [Quickstart](../build/quickstart/) |
| Build a whole app, chapter by chapter | [Tutorial](../tutorial/) |
| Describe my data and use the API: schemas, auth, functions, files, push | [Build](../build/) |
| Run and administer a deployment: providers, monitoring, backup, hardening | [Operate](../operate/) |
| Work on backd itself: workflow, the `make dev` stack, architecture | [Contribute](../contribute/) |
| See complete applications | [Examples](../build/examples/) |
