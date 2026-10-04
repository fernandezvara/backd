---
title: "2. Members"
description: "Turn authentication on: sign-up, sessions, roles, and an account page with password change and account deletion."
weight: 220
toc: true
---

Chapter 1's realm was open to anyone on localhost. Now members sign up and sign in — and the app learns who you are: your gallery, your assets, your account page.

## The realm grows

Replace `config/shelf/realm.yaml`:

```yaml
auth: enabled
signup: open

roles:
  admin:
    description: "Operators: users, invitations and API keys"
    admin: true
    users:
      - operator@shelf.example
  curator:
    description: Publishes assets and manages share links (chapter 5)
    users:
      - curator@shelf.example
```

- **`auth: enabled`** — users, sessions and roles exist; anonymous callers get `401`.
- **`signup: open`** — anyone may create an account ([alternatives](../../auth/users/): `invite`, `closed`). It is open **only for this chapter and the next**: chapter 4 closes it, and from then on every new member needs an invitation — which only an existing administrator can create.
- **`roles:`** — `admin` is the operator role (`admin: true` grants the admin API); `curator` matters from chapter 5. The `users:` lists are **seed assignments**: when someone signs up with a listed address, the role attaches. A shortcut for local stacks — never keep seeded users in a real realm.

## Apply, and create the three accounts

```sh
docker compose restart backd
```

Open `http://localhost:8080` and **sign up** (any password of 12 or more characters; the demo stack sends no email yet and `account.require_verified_email` is off, so nothing needs verifying — the `verify-email` and `welcome` messages only appear once chapter 10 wires email) as each of:

| Email | Why you need it |
|---|---|
| `operator@shelf.example` | the **administrator**: chapter 4 invites with it, chapters 10 and 12 use the Admin view |
| `curator@shelf.example` | the **curator**: publishes assets from chapter 5 on |
| your own address | a plain **member**, to see what the rules refuse |

Do it **now**. Once chapter 4 sets `signup: invite`, a new address can't sign up without an invitation, and nobody can create an invitation without an administrator — so an operator who hasn't signed up yet would lock you out ([the way back](../04-invitations/#locked-out) is `backd bootstrap`, but it is easier not to need it). Log out between accounts (**Log out** in the nav), or use a private window for each.

`backd bootstrap --realm shelf --email you@example.com` is how a *real* deployment creates its first admin, and the way out of that lock-out.

## Signing in

The app's **Sign in** button now does something: `backd.auth.signup()` or `.login()` returns a session the client keeps in `localStorage` (`storage: localStorageStorage('shelf')` — it survives reloads). `backd.auth.me()` on load restores it. A token in `localStorage` is readable by any script on the page, so a real app guards against XSS — or uses HttpOnly [session cookies](../../auth/sessions/#sessions-and-tokens), which the client supports with `cookies: true`.

## What the app gains

- **Nav state**: `backd.auth.me()` at load; sign-in button ↔ email + log out.
- **My assets**: `assets.list({ where: { "_meta.owner": user.id } })` — `_meta` fields are queryable like any other.
- **Account page**: `backd.auth.changePassword()` works now; email change and password reset are visible but disabled — they need the email stack of chapter 10.
- **Delete my account**: `backd.auth.deleteAccount()` *deactivates* — your email stays registered, your documents stay. Actual erasure is an admin action that applies each collection's policy ([Deleting and erasing users](../../auth/erasure/)):

{{< example-file path="shelf/main/assets/collection.yaml" >}}

`action: delete` says a member's assets — and their share links — go with them. The blog example shows the alternative (`anonymize`). Create it (and the same file for `shares`) with the other files of this chapter, then restart:

{{< tutorial-files "main/assets/collection.yaml main/shares/collection.yaml" >}}

## You should see

- `GET /v1/shelf/main/assets` without a session answers `401` — the chapter-1 curl now needs a sign-in first.
- Signing up and reloading keeps you signed in; **Log out** ends it.
- `operator@shelf.example` and `curator@shelf.example` exist, with the password you chose, next to your own account — and the operator's session has `roles: ["admin"]` (`backd.auth.me()`).
- **My assets** lists only what you created; the account page changes your password and the new one signs in.
- Deleting your account signs you out; signing up again with the same address says it's taken (deactivated, not gone).

Next: chapter 3 — `rules.yaml` decides who may touch what.
