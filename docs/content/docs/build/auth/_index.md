---
title: "Authentication"
description: "Users, sign-in methods and passwords in each realm."
icon: "lock"
weight: 300
toc: true
---

Each realm with `auth: enabled` has its own pool of users and [API keys](api-keys/), stored in the realm's [system database](../../operate/#realm-system-databases). One sign-in is meant to work across all databases of that realm (single sign-on), and never in another realm.

## Sign-in methods: providers

A user holds only identity data: a unique email, whether that email is verified, roles, and whether the account is disabled. Credentials are not part of the user. Each way of signing in is a **provider** — a sign-in method (an identity) linked to the user:

- **`password`** — built in, nothing to declare; the realm's own accounts.
- **`magic_link`** — an emailed link or code, from the realm's `magic_link:` section ([Magic links](magic-links/)). It proves control of the mailbox, the same thing a password reset proves.
- **`google`, `microsoft`, `apple`** — remote providers, declared in the realm's `providers:` section with their credentials as [realm secrets](../functions/secrets/) ([Sign in with…](providers/)). `password` and `email` are reserved names: no provider may take them.

A user can have several methods at once, at most one of each kind — a password and Google, say. They all lead to the same user, so roles, rules and owned documents don't depend on how the user signed in. A user can exist without a password (an operator may create the account first), and can remove any method but the last ([`DELETE /_auth/identities/{provider}`](sessions/#sign-in-methods)); `GET /_auth/me` lists them. Application data about a user, such as a display name or avatar, belongs in your own collections, not in the user.

Whatever the first factor, a user with a second factor is still asked for it — a provider sign-in answers the same challenge a password does: [Multi-factor authentication](mfa/).

## The pages, in order

| Page | What it covers |
|---|---|
| [Users and passwords](users/) | Creating and managing a realm's users with `backd user` or the admin API |
| [Sign-up, login and sessions](sessions/) | The `/_auth` endpoints: sign up, log in, sessions, password reset, brute-force protection, sign-in methods, deleting the account |
| [Magic links](magic-links/) | Signing in from an email alone: the link, the code, account claiming |
| [Re-authentication](reauth/) | A fresh proof of identity before sensitive actions |
| [Multi-factor authentication](mfa/) | The TOTP second factor, recovery codes, `required_roles` — applies to every first factor, remote providers included |
| [Sign in with Google, Microsoft or Apple](providers/) | The redirect flow, native ID tokens, linking rules, `account.on_signup` |
| [Access rules](rules/) | Per-collection `rules:` in `collection.yaml`: which callers may read, create, update and delete |
| [Consents](consents/) | Versioned texts, records, re-acceptance, withdrawal, gating |
| [API keys](api-keys/) | Server-side services, with full access to the realm's data |

## Operating it

- [Command-line administration](../../operate/administration/cli/): `backd bootstrap`, `backd login`, `backd user`.
- [Admin API](../../operate/administration/admin/): server-side user management.
- [Audit trail](../../operate/administration/audit/) of security-sensitive actions.
- [Deactivating and erasing users](../../operate/administration/erasure/), with per-collection policies for their data; [compliance](../../operate/administration/compliance/) covers personal-data scope, retention and recipes.
- Provider sign-in in the clients: [JavaScript](../clients/js/#sign-in-with-google-microsoft-or-apple) and [Dart](../clients/dart/#sign-in-with-a-provider-on-desktop) — `signInWith`, `completeSignIn`, `signInWithIdToken`.

See the [security model](../../operate/administration/security/) for what `backd` protects, and the [hardening checklist](../../operate/checklist/) before exposing a realm. **Planned:** personal-data export as a downloadable package; other OpenID Connect providers (`type: oidc`).
