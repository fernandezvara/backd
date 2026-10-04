---
title: "4. Invitations"
description: "signup: invite closes the realm; operators hand out one-time links — emailed once chapter 10 configures delivery."
weight: 240
toc: true
---

A team's library shouldn't be open to whoever finds it. One word in `realm.yaml` closes the door; invitations are how people get in anyway.

## Close the door

```yaml
signup: invite
```

Existing accounts keep working — this only governs **new** sign-ups. From now on `POST /_auth/signup` without a valid `invitation` token answers `403`.

## Locked out?

If you skipped chapter 2's accounts, nobody holds the `admin` role and nobody can invite. Create the operator where `backd` runs — it asks for a password and refuses when an administrator already exists:

```sh
docker compose exec backd /backd bootstrap --realm shelf --email operator@shelf.example
```

([Create the first administrator](../../auth/cli/) has the details.)

## Invite someone

Only admins manage invitations. Sign in as `operator@shelf.example` (it seeds the `admin` role) — the app's Admin view now shows **Invite a member**:

- An email makes the invitation work **only for that address**; leaving it empty makes a link anyone can use — once, either way.
- The answer carries `token` **once**: `backd` stores only its hash. The app builds `http://localhost:8080/?token=…` — copy it and hand it over.

```sh
# The same thing from the API (an admin API key or an admin session):
curl -X POST http://localhost:8080/v1/shelf/_admin/invitations \
  -H 'Content-Type: application/json' \
  -d '{"email":"newbie@example.com"}'
```

`GET /_admin/invitations` lists the pending ones (never their tokens); `DELETE` revokes. The Admin view's list shows both.

## Accept

The invitee opens the link — `?token=` prefills the sign-up form — picks a password, and `backd.auth.signup({email, password, invitation})` consumes the invitation. Used and expired invitations delete themselves.

## Where email fits

No email yet — the operator hands the link over out of band. Chapter 10 changes one argument (`send: true`, with `email`) and `backd` emails the invitation itself: the answer then has `sent: true` and **no token at all**, because nobody needs to hold one. The invitee's experience is what `email.links.invitation` decides — a hosted page or your own `acceptInvitation` flow.

## You should see

- Signing up without a token answers `403` — `signup: invite` is enforced.
- The operator's Admin view creates an invite and shows a one-use link; a member's view doesn't.
- The link prefills the sign-up form; accepting consumes it (the same link fails a second time).
- Revoking an unused invite makes its link stop working.
- `GET /v1/shelf/_admin/invitations` as the operator lists pending invites — no tokens.

Next: chapter 5 — the first function, `publish`, closes the hole the rules chapter left.
