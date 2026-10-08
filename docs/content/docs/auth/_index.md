---
title: "Authentication"
description: "Users, sign-in methods and passwords in each realm."
icon: "lock"
weight: 500
toc: true
---

Each realm with `auth: enabled` has its own pool of users and [API keys](api-keys/), stored in the realm's [system database](../operations/#realm-system-databases). One sign-in is meant to work across all databases of that realm (single sign-on), and never in another realm.

## What's available

| Feature | Status |
|---|---|
| [Users and passwords](users/), managed with `backd user` or the admin API | available |
| [Command-line administration](cli/): `backd bootstrap`, `backd login` | available |
| [Sign-up, login and sessions](sessions/) (`/v1/{realm}/_auth/…`) | available |
| [API keys](api-keys/) for server-side services, with full access to the realm's data | available |
| [Access rules](rules/) per collection (`rules:` in `collection.yaml`) | available |
| [Admin API](admin/) for server-side user management | available |
| [Audit trail](audit/) of security-sensitive actions | available |
| [Deactivating and erasing users](erasure/), with per-collection policies for their data | available |

See the [security model](security/) for what `backd` protects, and the [hardening checklist](../operations/checklist/) before exposing a realm.
| [Brute-force protection](sessions/#brute-force-protection) on login | available |

## Users and sign-in methods

A user holds only identity data: a unique email, whether that email is verified, roles, and whether the account is disabled. Credentials are not part of the user. Each way of signing in is a separate *sign-in method* (an identity) linked to the user:

- **password** is one kind;
- sign-in with external providers (Google, Microsoft, Apple, …) is planned and will add more kinds.

A user can have several at once, at most one of each kind: a password and a provider, say. They all lead to the same user, so roles, rules and owned documents don't depend on how the user signed in. A user can exist without a password, for example when an operator creates the account first and sets the password later, and can remove any method but the last ([`DELETE /_auth/identities/{provider}`](sessions/#sign-in-methods)).

Application data about a user, such as a display name or avatar, belongs in your own collections, not in the user.
