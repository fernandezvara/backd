---
title: "10. Email for real"
description: "email-capture delivers in dev (the Mailbox), postmark for real — templates, hosted pages, ctx.email.send, emailed invitations."
weight: 300
toc: true
---

Invitations emailed themselves? Not yet — chapter 4 handed you a token to pass along. This chapter gives the realm an email pipeline: a delivery function, templates for every flow backd sends, hosted pages the links open, and one custom kind of your own.

## The realm side

Add to `config/shelf/realm.yaml` (keeping `auth`, `signup` and `roles` as they are):

```yaml
email:
  function: mail/email-capture
  from: "Shelf <no-reply@shelf.example>"
  allowed_redirects: [http://localhost:8080]
  redirects:
    verify_email: http://localhost:8080/
    reset_password: http://localhost:8080/
    change_email: http://localhost:8080/
    invitation: http://localhost:8080/

account:
  allow_email_change: true
```

- **`function:`** — one function (in the new `mail` database) delivers every email `backd` renders. `email-capture` stores them in the `mail/outbox` collection instead of sending — `dev_only: true`, it only runs under `BACKD_DEV`, which the tutorial's `compose.yaml` sets (a real deployment doesn't, and `backd` then refuses to start with it configured) ([Email](../../functions/email/)).
- **Templates** — `email/<kind>/<locale>.{subject.txt,txt,html}`, one folder per kind `backd` sends (verify-email, reset-password, invitation, welcome, account-exists, change-email, email-changed, password-changed) plus custom kinds like `shelf-digest`. Every locale you list needs every kind — startup fails naming what's missing.
- **Pages** — `pages/<kind>/<locale>.html`: the hosted pages the email links open. `GET` shows a form, `POST` consumes the token — mail scanners can never spend your links.
- **`allowed_redirects` / `redirects`** — after a flow's page, the reader lands back at the app, and only at origins you list.

On a checkout, `backd template realm --realm shelf` writes all of `email/` and `pages/` for you, and `backd template email-capture --realm shelf --database mail` the delivery function and `outbox`; the tutorial's `config/` is mounted read-only into the container, so fetch the finished files instead:

{{< tutorial-files "email/account-exists/en.html email/account-exists/en.subject.txt email/account-exists/en.txt email/change-email/en.html email/change-email/en.subject.txt email/change-email/en.txt email/email-changed/en.html email/email-changed/en.subject.txt email/email-changed/en.txt email/invitation/en.html email/invitation/en.subject.txt email/invitation/en.txt email/password-changed/en.html email/password-changed/en.subject.txt email/password-changed/en.txt email/reset-password/en.html email/reset-password/en.subject.txt email/reset-password/en.txt email/shelf-digest/en.html email/shelf-digest/en.subject.txt email/shelf-digest/en.txt email/verify-email/en.html email/verify-email/en.subject.txt email/verify-email/en.txt email/welcome/en.html email/welcome/en.subject.txt email/welcome/en.txt pages/accept-invitation/en.html pages/confirm-email-change/en.html pages/invalid-token/en.html pages/reset-password/en.html pages/result/en.html pages/revert-email-change/en.html pages/verify-email/en.html mail/_functions/deno.json mail/_functions/email-capture/function.yaml mail/_functions/email-capture/index.ts mail/_functions/email-capture/index.test.ts mail/outbox/schema.json mail/outbox/indexes.json mail/outbox/collection.yaml main/_functions/digest/function.yaml main/_functions/digest/index.ts main/_functions/digest/index.test.ts" >}}

(That last group replaces `digest/` with its finished version: chapter 9's `schedule:` plus `email: true`, and the `ctx.email.send` call shown below.) Rebuild and restart: `docker compose run --rm functions-build && docker compose restart backd`. For real delivery, `mail/_functions/postmark/` is the swap — `email.function: mail/postmark`, plus its two secrets (`POSTMARK_TOKEN`, `POSTMARK_FROM`) and a verified sender.

## A kind of your own: shelf-digest

`digest` gained `email: true` — `ctx.email.send` queues one email per member through the same pipeline (render, delivery function, retries):

```ts
await ctx.email.send({
  kind: "shelf-digest",              // email/shelf-digest/
  to_user: member.user_id,           // their language; backd looks the user up
  data: { count: 3, since: "2026-10-01", assets: ["Handbook", "…"] },
})
```

The template prints `{{.Data.count}}`, `{{range .Data.assets}}` — a function supplies **data, never text**: nobody can make your realm send words you didn't write ([Sending email from functions](../../functions/email/#sending-email-from-functions)).

## What changes in the app

- **Invite a member** gains *email the invitation* — `invitations.send` queues the `invitation` template; the mail lands in the **Mailbox** below (the dev outbox: its rule is `read: "true"`, so anyone can read it — which is why `email-capture` only runs in dev).
- **Forgot your password?** works — a reset email, a hosted page, and the link never spent by a scanner.
- The **account page** offers *Change email* (`account.allow_email_change`) — the new address confirms, the old one can undo.
- **Mailbox** (Admin view) lists captured emails — open the verify/reset/accept link right there.

## You should see

- Inviting by email lands `invitation` in the Mailbox; its link opens the hosted accept-invitation page, which sends you back to the app.
- **Run by hand → digest** produces a `shelf-digest` per member in the Mailbox, next to the in-app notifications.
- Requesting a password reset lands `reset-password`; the hosted page asks for the new password, then redirects to the app.
- `deno test` covers the digest's `ctx.email.send` (`sentEmails()`) and email-capture's outbox write.

Next: chapter 11 — the internet calls you: the `import` webhook and verifying the sender.
