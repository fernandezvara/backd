---
title: "Authentication"
description: "Users, sign-in methods and passwords in each realm."
icon: "lock"
weight: 300
toc: true
---

Each realm with an `auth` section has its own pool of users and [API keys](api-keys/), stored in the realm's [system database](../../operate/#realm-system-databases). One sign-in is meant to work across all databases of that realm (single sign-on), and never in another realm.

## Strategies

A realm says how people sign in with named **strategies** in `auth.strategies`. A strategy is an ordered list of methods: the first proves who the person is, and a later one (`totp`) adds a step.

```yaml
auth:
  strategies:
    password: [password]               # email and password
    password_totp: [password, totp]    # and then an authenticator code
    google: [google]                   # a provider declared in auth.providers
```

- The methods are `password`, `magic_link`, `totp` (never first), `sso` (an organization's own [connection](../../operate/organization-sign-in/); needs `organizations.sso`) and the names of the providers in `auth.providers`.
- **Choosing one.** The sign-in routes take a `strategy` (`POST /_auth/login`, `POST /_auth/magic-link`, the provider routes, and the same under `/_auth/orgs/{org}/…`). It may be left out while exactly one strategy starts with that method; otherwise the answer is `400 strategy_required`. A name that does not exist or starts with another method is `400 unknown_strategy`. `GET /_auth/strategies` (no credentials) lists the names and their methods, so an app can draw its sign-in screen.
- **Steps.** Every method must succeed, in order. After the first, a `totp` step answers with a challenge (`next: ["totp"]`) that `POST /_auth/mfa/verify` trades for the next step or the session; a person with no authenticator enrols as part of signing in ([Multi-factor authentication](mfa/)). A person's own authenticator never adds a step to a strategy that lacks `totp`.
- **Roles.** A role can list the `strategies` it accepts (`roles.<name>.strategies`; none listed means all). A role is **active** in a session only if the session's strategy is one of them: `user.roles` in rules and functions, the admin API and the admin interface see the active roles only. A person who signs in with a strategy their role does not list still gets a session, without that role, and the answer's `roles_inactive` says which roles stayed off, so the app can offer a stronger way in. Listing is explicit: a stronger strategy does not carry a weaker one's roles unless the role lists it.
- **The session** records its `strategy`, its `methods` and `mfa`.
- `auth: disabled` runs a realm without authentication; a realm with neither is refused at startup. The keys that used to sit at the top of `realm.yaml` (`signup`, `sessions`, `password`, `login_throttle`, `providers`, `sign_in`, `magic_link`, `reauth`) now sit under `auth:`, and `mfa` is `auth.totp`; the old keys fail to start with a message that names the new place.

## Sign-in methods: providers

A user holds only identity data: a unique email, whether that email is verified, roles, and whether the account is disabled. Credentials are not part of the user. Each way of signing in is a **provider** — a sign-in method (an identity) linked to the user:

- **`password`** — built in, nothing to declare; the realm's own accounts.
- **`magic_link`** — an emailed link or code, from the realm's `auth.magic_link` section ([Magic links](magic-links/)). It proves control of the mailbox, the same thing a password reset proves.
- **`google`, `microsoft`, `apple`** — remote providers, declared in the realm's `auth.providers` section with their credentials as [realm secrets](../functions/secrets/) ([Sign in with…](providers/)). `password` and `email` are reserved names: no provider may take them.

A user can have several methods at once, at most one of each kind — a password and Google, say. They all lead to the same user, so roles, rules and owned documents don't depend on how the user signed in. A user can exist without a password (an operator may create the account first), and can remove any method but the last ([`DELETE /_auth/identities/{provider}`](sessions/#sign-in-methods)); `GET /_auth/me` lists them. Application data about a user, such as a display name or avatar, belongs in your own collections, not in the user.

Whatever the first factor, a user with a second factor is still asked for it — a provider sign-in answers the same challenge a password does: [Multi-factor authentication](mfa/).

## The pages, in order

| Page | What it covers |
|---|---|
| [Users and passwords](users/) | Creating and managing a realm's users with `backd user` or the admin API |
| [Sign-up, login and sessions](sessions/) | The `/_auth` endpoints: sign up, log in, sessions, password reset, brute-force protection, sign-in methods, deleting the account |
| [Magic links](magic-links/) | Signing in from an email alone: the link, the code, account claiming |
| [Re-authentication](reauth/) | A fresh proof of identity before sensitive actions |
| [Multi-factor authentication](mfa/) | The TOTP second factor, recovery codes, roles that need it — applies to every first factor, remote providers included |
| [Sign in with Google, Microsoft or Apple](providers/) | The redirect flow, native ID tokens, linking rules, `account.on_signup` |
| [Access rules](rules/) | Per-collection `rules:` in `collection.yaml`: which callers may read, create, update and delete |
| [Consents](consents/) | Versioned texts, records, re-acceptance, withdrawal, gating |
| [API keys](api-keys/) | Server-side services, with full access to the realm's data |

## Operating it

- [Command-line administration](../../operate/administration/cli/): `backd-server bootstrap`, `backd login`, `backd user`.
- [Admin API](../../operate/administration/admin/): server-side user management.
- [Audit trail](../../operate/administration/audit/) of security-sensitive actions.
- [Deactivating and erasing users](../../operate/administration/erasure/), with per-collection policies for their data; [compliance](../../operate/administration/compliance/) covers personal-data scope, retention and recipes.
- Provider sign-in in the clients: [JavaScript](../clients/js/#sign-in-with-google-microsoft-or-apple) and [Dart](../clients/dart/#sign-in-with-a-provider-on-desktop) — `signInWith`, `completeSignIn`, `signInWithIdToken`.

See the [security model](../../operate/administration/security/) for what `backd` protects, and the [hardening checklist](../../operate/checklist/) before exposing a realm. **Planned:** personal-data export as a downloadable package; other OpenID Connect providers (`type: oidc`).
