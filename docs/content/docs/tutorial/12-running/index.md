---
title: "12. Running it"
description: "API keys for machines, the audit trail in the app, and the checklist before any of this faces the internet."
weight: 320
toc: true
---

The shelf works. The last chapter is about *operating* it: who acts without a session, who did what, and what to review before a real deployment.

## API keys

Sessions are for people; **API keys are for machines** — a CI job that reads the gallery, a monitor that calls a function. In the Admin view, *Create read:main key* makes one scoped to read-only on `main`, expiring in 90 days:

```js
backd.admin.keys.create({ name: 'ci-reader', expiresIn: '90d', scopes: ['read:main'] })
```

The full key (`bdk_…`) shows **once**, in the creation response — store it in a secret store; `backd` keeps only its prefix. `scopes` limit what a key reaches (`read`, `write`, `call`, optionally `:database` or `:database/name`); `revoke` stops it at once ([API keys](../../../auth/api-keys/)). With a key, an unattended script uses the same client: `createClient({ url, realm, apiKey })` — no signup, no session.

## The audit trail

Every admin-visible act is recorded — invites created and sent, keys created and revoked, functions invoked by hand, logins, erasures. `backd.admin.audit.list()` pages it newest first; the Admin view shows who (`user:<id>`, `key:<name>`, `cli:bootstrap`), what (`apikey.create`, `function.invoke_manual`, …) and the target. It never holds secrets or email bodies ([Audit](../../../auth/audit/)).

Try it: create a key, revoke it, refresh the feed — `apikey.create` and `apikey.revoke` both appear, attributed to you.

## Before the internet sees it

This tutorial ran on a dev stack: `BACKD_DEV` on, `email-capture` storing mail, demo accounts in `realm.yaml`, `dev-secret` webhook keys, `localhost` redirects. The [hardening checklist](../../../operations/checklist/) is the page to walk before a realm goes real — for this app specifically:

- Swap `email.function` to `mail/postmark` (or your provider), set its secrets — and `email-capture`'s `dev_only` already guards you: `backd` won't start with it outside dev mode.
- Set real function secrets (`IMPORT_WEBHOOK_SECRET`, Postmark's) — `dev-secret` signs nothing an attacker can't forge.
- Remove the seeded `roles.*.users` demo accounts; bootstrap your own admin.
- Give every API key a scope and an expiry — the app already creates them that way.
- Review each `rules.yaml` and run `backd rules test` in CI.

## You should see

- The Admin view lists real audit entries for everything you did in chapters 4–11.
- A new key authenticates `GET /v1/shelf/main/assets` but not a write — `403`, scoped.
- Revoking the key fails the same read with `401`, and `apikey.revoke` is in the feed.

That's the tutorial — the tour (`tour.js` in the repo root) walks a fresh clone through all of it against a live stack, and `make shelf-tour` replays it.
