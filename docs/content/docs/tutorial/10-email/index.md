---
title: "10. Email for real"
description: "email-capture delivers in dev (the Mailbox), postmark for real — templates, hosted pages, ctx.email.send, emailed invitations."
weight: 300
toc: true
---

Invitations emailed themselves? Not yet — chapter 4 handed you a token to pass along. This chapter gives the realm an email pipeline: a delivery function, templates for every flow backd sends, hosted pages the links open, and one custom kind of your own.

## The realm side

```yaml
# realm.yaml
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

- **`function:`** — one function (in the new `mail` database) delivers every email `backd` renders. `email-capture` stores them in the `mail/outbox` collection instead of sending — `dev_only: true`, it only runs under `BACKD_DEV` ([Email](../../../functions/email/)).
- **Templates** — `email/<kind>/<locale>.{subject.txt,txt,html}`, one folder per kind `backd` sends (verify-email, reset-password, invitation, welcome, account-exists, change-email, email-changed, password-changed) plus custom kinds like `shelf-digest`. Every locale you list needs every kind — startup fails naming what's missing.
- **Pages** — `pages/<kind>/<locale>.html`: the hosted pages the email links open. `GET` shows a form, `POST` consumes the token — mail scanners can never spend your links.
- **`allowed_redirects` / `redirects`** — after a flow's page, the reader lands back at the app, and only at origins you list.

`backd template realm --realm shelf` writes all of `email/` and `pages/` for you, and `backd template email-capture --realm shelf --database mail` writes the delivery function and `outbox`. For real delivery, `mail/_functions/postmark/` is the swap — `email.function: mail/postmark`, plus its two secrets (`POSTMARK_TOKEN`, `POSTMARK_FROM`) and a verified sender.

## A kind of your own: shelf-digest

`digest` gained `email: true` — `ctx.email.send` queues one email per member through the same pipeline (render, delivery function, retries):

```ts
await ctx.email.send({
  kind: "shelf-digest",              // email/shelf-digest/
  to_user: member.user_id,           // their language; backd looks the user up
  data: { count: 3, since: "2026-10-01", assets: ["Handbook", "…"] },
})
```

The template prints `{{.Data.count}}`, `{{range .Data.assets}}` — a function supplies **data, never text**: nobody can make your realm send words you didn't write ([Sending email from functions](../../../functions/email/#sending-email-from-functions)).

## What changes in the app

- **Invite a member** gains *email the invitation* — `invitations.send` queues the `invitation` template; the mail lands in the **Mailbox** below (the dev outbox, readable by admins).
- **Forgot your password?** works — a reset email, a hosted page, and the link never spent by a scanner.
- The **account page** offers *Change email* (`account.allow_email_change`) — the new address confirms, the old one can undo.
- **Mailbox** (Admin view) lists captured emails — open the verify/reset/accept link right there.

## You should see

- Inviting by email lands `invitation` in the Mailbox; its link opens the hosted accept-invitation page, which sends you back to the app.
- **Run by hand → digest** produces a `shelf-digest` per member in the Mailbox, next to the in-app notifications.
- Requesting a password reset lands `reset-password`; the hosted page asks for the new password, then redirects to the app.
- `deno test` covers the digest's `ctx.email.send` (`sentEmails()`) and email-capture's outbox write.

Next: chapter 11 — the internet calls you: the `import` webhook and verifying the sender.
