---
title: "Is backd for you?"
description: "Where backd fits and where it doesn't, how it compares with other backends, and the list of what it does not do (yet)."
icon: "list-checks"
weight: 110
toc: true
---

Read this before you invest an afternoon. `backd` is a config-driven REST backend over MongoDB; it is good at a particular kind of application, and it leaves some things out on purpose or for now.

## A good fit when

- Your data is **documents**, and you want each collection described by a JSON Schema that the server enforces.
- You want **who may read and write what** decided on the server by [access rules](../build/auth/rules/), not in each client.
- You want the whole backend to be **files in git**: realms, schemas, rules, functions and email texts that you review, test in CI and deploy as one unit.
- You **self-host**, on Docker, Kubernetes or a plain host, and you can run MongoDB (a replica set, even a single node).
- You build **multi-tenant** or business-to-business products: [organizations](../build/organizations/), teams, and company sign-in with Google Workspace or Microsoft.
- You need **server-side logic** you can write in TypeScript: [functions](../build/functions/), background jobs, cron, webhooks and triggers.
- You want people to sign in **without a password**: [passkeys](../build/auth/passkeys/), magic links or Google, Microsoft and Apple, with a second factor where a role needs one.
- Your schema **will change** while apps you can't update are in the field: a collection can keep its [schema in versions](../build/configuration/evolving-a-schema/), with migrations the server runs in the background and a minimum version that tells old apps to update.
- You must answer **compliance** questions: an audit trail, user erasure, personal-data export, field encryption.
- Your clients are in JavaScript, Dart/Flutter, Go or Python, or can use a generated [OpenAPI](../build/api/openapi/) client.

## A poor fit when

- Your data is **relational** and you need SQL joins and transactions across everything. `backd` has [references](../build/api/references/) and one multi-document transaction per database ([`_batch`](../build/api/documents/)), not a relational engine.
- You want a **hosted service** with nothing to run. `backd` is software you operate.
- You need **GraphQL**, or **SAML** sign-in, today. Both are decided against (see below).
- You need **SCIM provisioning**, **geospatial queries** or **atomic update operators** now. They are not built (table below).
- You need your server logic in a **language other than TypeScript or JavaScript**. Functions run in Deno.
- You want to run the server **natively on macOS or Windows**. `backd-server` runs on Linux; on other systems use the container image (the `backd` command line runs everywhere).
- You build a **Flutter web** app. The Dart client needs a `dart:io` platform, so it covers mobile, desktop and standalone Dart; Flutter web is a follow-up.

## How it compares

A short, outside view; check each project's own documentation for what it does today.

| Alternative | Reach for it when | Reach for `backd` instead when |
|---|---|---|
| **Firebase** | You want a hosted service, mature mobile SDKs and offline sync, and you are happy in Google's ecosystem. | You want to self-host, keep the configuration in git and enforce schemas and rules in files you review. |
| **Supabase** | Your data fits Postgres and SQL, or you want its extensions and ecosystem. | Your data is documents and you want MongoDB. |
| **PocketBase** | You want the smallest thing that works: one binary with an embedded database, for a small project. | You want several stateless instances over MongoDB, organizations and company sign-in, and a job queue for functions. |
| **Appwrite** | You want a self-hosted platform with a console-first workflow and many SDKs. | You want a file-first workflow where the console is optional and the config is the source of truth. |
| **Your own API** (Express, Go, …) | You need full control or logic that doesn't look like CRUD with rules. | Most of what you'd write is CRUD, users, rules, files and notifications, and you would rather declare it. |

## What `backd` does not do (yet)

One line each, so you can tell early. "Not built" means no code exists today; the status column says what to do meanwhile.

| Missing | Meanwhile | Status |
|---|---|---|
| **Sign in with any OpenID Connect provider** (Okta, Keycloak, Auth0, …), for the realm and for organizations | Google, Microsoft and Apple work for the realm; Google and Microsoft for organizations | Planned: [#157](https://github.com/fernandezvara/backd/issues/157), [#599](https://github.com/fernandezvara/backd/issues/599) |
| **Apple sign-in for organizations** | Use Google or Microsoft for the company's sign-in | Planned: [#598](https://github.com/fernandezvara/backd/issues/598) |
| **SCIM provisioning** (users created and removed by a company's identity provider) | [Invitations](../build/organizations/) and company sign-in | Design only |
| **Atomic update operators** (increment, push, add to set) | `PATCH` is a JSON Merge Patch; use `If-Match` and retry, or a [function](../build/functions/) | Not built |
| **Built-in request rate limiting** | Failed logins and function calls are limited; put a reverse proxy in front for the rest ([Operate](../operate/#rate-limiting-and-abuse)) | Delegated to the proxy |
| **Acting on behalf of a user from a client library** | The server accepts `X-Backd-On-Behalf-Of`; call it with your own HTTP client | Planned: [#33](https://github.com/fernandezvara/backd/issues/33) |
| **Outbound webhooks** as a declared feature | A trigger plus a function that calls out through [egress](../build/functions/) | Not built |
| **Guest users that later upgrade to accounts** | Anonymous access through rules, or an [invitation](../build/organizations/) | Not built |
| **Geospatial queries** | Filter by coordinates yourself, or in a function | Not built |
| **Per-document revision history** | The change feed and the [audit trail](../operate/administration/audit/) | Not built |
| **OpenTelemetry tracing** | Prometheus metrics and structured logs | Planned: [#38](https://github.com/fernandezvara/backd/issues/38) |
| **Function logs in backd's own log output** | `backd functions logs` | Planned: [#139](https://github.com/fernandezvara/backd/issues/139) |
| **A database other than MongoDB** | MongoDB (a replica set) | Planned for DocumentDB only: [#43](https://github.com/fernandezvara/backd/issues/43) |

## Decided against

These questions come back, so the answers are recorded:

- **GraphQL.** The REST API, its OpenAPI document and the typed clients cover the use; rules and the query allowlist would have to be rebuilt for a second protocol.
- **Sending email from backd over SMTP.** Email goes through [delivery functions](../build/functions/email/), so a function (and the network allowlist) decides where messages go.
- **SAML.** OpenID Connect covers the common identity providers; SAML would be revisited for a named need.
- **Reloading configuration in production.** Config is loaded at startup and a change needs a restart, so every instance runs the same configuration. Dev mode reloads while you edit.

## Stability

`backd` is at version 0.x. No stable release is declared, so a minor release may contain breaking changes; they are listed first in the [changelog](https://github.com/fernandezvara/backd/blob/main/CHANGELOG.md) with what to change. Pin versions in production and read the changelog before upgrading.

## Next

[Run the Quickstart](../build/quickstart/) to see it working, or read [Getting started](../getting-started/) for the concepts first.
