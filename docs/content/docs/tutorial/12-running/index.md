---
title: "12. Running it"
description: "API keys for machines, the audit trail in the app, and the checklist before any of this faces the internet."
weight: 320
toc: true
---

The shelf works. The last chapter is about *operating* it: who acts without a session, who did what, and what to review before a real deployment.

## API keys

(This chapter needs the Admin view, so sign in as `operator@shelf.example`.)

Sessions are for people; **API keys are for machines** — a CI job that reads the gallery, a monitor that calls a function. In the Admin view, type a name (say `ci-reader`) and press *Create read:main key*: it makes one scoped to read-only on `main`, expiring in 90 days:

```js
backd.admin.apiKeys.create({ name: 'ci-reader', expiresIn: '90d', scopes: ['read:main'] })
```

The full key (`bdk_…`) shows **once**, in the creation response — store it in a secret store; `backd` keeps only its prefix. `scopes` limit what a key reaches (`read`, `write`, `call`, optionally `:database` or `:database/name`); `revoke` stops it at once ([API keys](../../auth/api-keys/)). With a key, an unattended script uses the same client: `createClient({ url, realm, apiKey })` — no signup, no session.

## The audit trail

Every admin-visible act is recorded — invites created and sent, keys created and revoked, functions invoked by hand, logins, erasures. `backd.admin.audit.list()` pages it newest first; the Admin view shows who (`user:<id>`, `key:<name>`, `cli:bootstrap`), what (`apikey.create`, `function.invoke_manual`, …) and the target. It never holds secrets or email bodies ([Audit](../../auth/audit/)).

Try it: create a key, revoke it, refresh the feed — `apikey.create` and `apikey.revoke` both appear, attributed to you.

## Before the internet sees it

This tutorial ran on a dev stack: `BACKD_DEV` on, `email-capture` storing mail, demo accounts in `realm.yaml`, `dev-secret` webhook keys, `localhost` redirects. The [hardening checklist](../../operations/checklist/) is the page to walk before a realm goes real — for this app specifically:

- Swap `email.function` to `mail/postmark` (or your provider), set its secrets — and `email-capture`'s `dev_only` already guards you: `backd` won't start with it outside dev mode.
- Set real function secrets (`IMPORT_WEBHOOK_SECRET`, Postmark's) — `dev-secret` signs nothing an attacker can't forge.
- Remove the seeded `roles.*.users` demo accounts and create your own administrator with `backd bootstrap` (chapter 4). Removing a seed from `realm.yaml` does **not** take the role away from the account that already has it — remove it from that user with the admin API (`DELETE /_admin/users/{id}/roles/{role}`, see [Admin API](../../auth/admin/)).
- Keep `signup: invite`: it is the right setting for a team library. Anything else puts the door back.
- Give every API key a scope and an expiry — the app already creates them that way.
- Review each `rules.yaml` and run `backd rules test` in CI.

## You should see

- The Admin view lists real audit entries for everything you did in chapters 4–11.
- A new key authenticates `GET /v1/shelf/main/assets` but not a write — `403`, scoped.
- Revoking the key fails the same read with `401`, and `apikey.revoke` is in the feed.

## What Shelf is now

| Collection | Who reads | Who writes |
|---|---|---|
| `assets` | members: published + their own drafts; curators: every draft | members create/edit/delete their own drafts; `publish` stamps `published_at` |
| `shares` | the creator | only `share` creates; the creator deletes |
| `notifications` | the recipient (may set `read_at`) | only `notify` |
| `members` | every member | each member their own row |
| `imports` | nobody | only `import` |
| `mail/outbox` | anyone (dev only) | only `email-capture` |

| Function | Mode | Who calls it |
|---|---|---|
| `publish` | sync | curators (idempotency key required) |
| `share` | sync | any member (idempotency key required) |
| `share-open` | sync | anyone, rate-limited |
| `preview` | sync, `network:` | any member |
| `notify` | sync, internal | other functions only |
| `digest` | async, scheduled daily, sends email | operators, and the schedule |
| `cleanup` | async, internal, scheduled 03:00 UTC | the schedule, or by hand |
| `import` | webhook | the signed feed |

## Where next

- **Deploy it:** [Deploying](../../operations/deploying/) and [Production](../../operations/production/) (TLS, separate networks for the executor and egress, secrets), then the [checklist](../../operations/checklist/) above.
- **Operate it:** [Backups](../../operations/backup/), [Metrics](../../operations/metrics/) (Prometheus, with dashboards in the repository), and the [audit](../../auth/audit/) feed you saw.
- **Go deeper on functions:** the [cookbook](../../functions/cookbook/) (payments, exports, retries) and [Testing functions](../../functions/testing/).
- **Use it from your own app:** the [JavaScript client](../../clients/js/) and the [API reference](../../api/) (`api/openapi.yaml`).
- **Test the tutorial itself:** on a checkout, `make shelf-tour` replays everything in this tutorial against a running stack (`clients/js/examples/shelf/tour.js`).
