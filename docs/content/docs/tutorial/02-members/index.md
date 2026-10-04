---
title: "2. Members"
description: "Turn authentication on: sign-up, sessions, roles, and an account page with password change and account deletion."
weight: 220
toc: true
---

Chapter 1's realm was open to anyone on localhost. Now members sign up and sign in — and the app learns who you are: your gallery, your assets, your account page.

## The realm grows

Replace `config/shelf/realm.yaml`:

{{< example-file path="shelf/realm.yaml" >}}

- **`auth: enabled`** — users, sessions and roles exist; anonymous callers get `401`.
- **`signup: open`** — anyone may create an account ([alternatives](../../auth/users/): `invite`, `closed`).
- **`roles:`** — `admin` is the operator role (`admin: true` grants the admin API); `curator` matters from chapter 5. The `users:` lists are **seed assignments**: the first time `curator@shelf.example` signs up, the role attaches. A shortcut for local stacks — never keep seeded users in a real realm.

`backd bootstrap --realm shelf --email you@example.com` is the other way to get a first admin, and what a fresh deployment uses.

## Apply and sign up

```sh
docker compose restart backd
```

The app's **Sign in** button now does something: `backd.auth.signup()` or `.login()` returns a session the client keeps in `localStorage` (`storage: localStorageStorage('shelf')` — it survives reloads). `backd.auth.me()` on load restores it.

Sign up twice: once as yourself, once as `curator@shelf.example` — both work because `signup: open`; the seeded address silently gains its role. (Any password ≥ 12 characters; the demo stack doesn't send email yet, so verification is skipped until chapter 10.)

## What the app gains

- **Nav state**: `backd.auth.me()` at load; sign-in button ↔ email + log out.
- **My assets**: `assets.list({ where: { "_meta.owner": user.id } })` — `_meta` fields are queryable like any other.
- **Account page**: `backd.auth.changePassword()` works now; email change and password reset are visible but disabled — they need the email stack of chapter 10.
- **Delete my account**: `backd.auth.deleteAccount()` *deactivates* — your email stays registered, your documents stay. Actual erasure is an admin action that applies each collection's policy ([Deleting and erasing users](../../auth/erasure/)):

{{< example-file path="shelf/main/assets/collection.yaml" >}}

`action: delete` says a member's assets — and their share links — go with them. The blog example shows the alternative (`anonymize`).

## You should see

- `GET /v1/shelf/main/assets` without a session answers `401` — the chapter-1 curl now needs a sign-in first.
- Signing up and reloading keeps you signed in; **Log out** ends it.
- `curator@shelf.example` and `operator@shelf.example` sign in with any password ≥ 12 chars on first use.
- **My assets** lists only what you created; the account page changes your password and the new one signs in.
- Deleting your account signs you out; signing up again with the same address says it's taken (deactivated, not gone).

Next: chapter 3 — `rules.yaml` decides who may touch what.
