---
title: "Email"
description: "Send transactional email through any provider: you write one function, backd renders the messages."
icon: "mail"
weight: 558
toc: true
---

`backd` **never talks to a mail server**. A realm names one **delivery function**: an [internal](../internal/), `async` function that receives a finished message and hands it to whatever provider you chose (Postmark, SES, Resend, Mailgun, SendGrid, …). `backd` does everything else: the templates, the tokens and links, the retries, the limits.

{{< hint style="note" >}}
`backd` sends no mail from its own servers or addresses, so **deliverability is your responsibility**, handled with your provider: a verified sending domain (SPF, DKIM, DMARC), a dedicated sender address, the provider's bounce and complaint handling and suppression lists. Watch your provider's bounce and complaint rates before sending real email, and try the flows with a test mailbox first.
{{< /hint >}}

## Configuration

Email is configured in [`realm.yaml`](../../configuration/realm/); `backd template realm` writes the section commented, with every key explained. A realm without `email` sends no email at all.

```yaml
email:
  function: notifications/deliver        # <database>/<function>
  from: "Acme <no-reply@acme.example>"
  reply_to: support@acme.example         # optional
  public_url: https://api.acme.example   # optional: backd's public address, for links
  default_locale: en
  locales: [en]                          # every listed language needs every template
  limits:                                # defaults shown
    per_recipient: { per_kind_per_hour: 3, per_day: 10 }
    per_ip: { per_hour: 20 }
```

- **`function`** is the delivery function. Startup fails if it doesn't exist, isn't `internal: true` or isn't `mode: async`, naming the file.
- **`public_url`** (or the `BACKD_URL` environment variable, which every `backd serve` and `backd worker` reads) is the address the links in emails point at. Startup fails when a realm sends email and neither is set.
- **`locales`** lists the languages the realm's emails exist in; `default_locale` must be one of them.
- Email needs `auth: enabled`: its users, tokens and counters live in the realm's system database.

## Templates

`backd template realm --realm <realm>` writes English templates for every kind of email under `<realm>/email/`:

```
<realm>/email/
  verify-email/     en.subject.txt  en.txt  en.html
  reset-password/   en.subject.txt  en.txt  en.html
  account-exists/   …
  password-changed/ …
  welcome/  change-email/  email-changed/  invitation/   …
```

- A folder per **kind**, and per language a subject, a plain-text body and an HTML body. Edit them freely.
- **Required kinds** (`verify-email`, `reset-password`, `account-exists`, `password-changed`) must exist whenever the realm configures `email`; **every listed locale needs every template**, or startup fails naming the missing files.
- Subjects and text bodies use Go's `text/template`; HTML bodies use `html/template`, which escapes what it prints. Available: `{{.Realm}}`, `{{.User.Email}}`, `{{.User.Locale}}`, `{{.Link}}`, `{{.ExpiresAt}}` and `{{.Data}}`. Templates never receive text written by whoever caused the email, so `backd` can't be used to send someone else's words.
- Templates are parsed and test-rendered at startup (a syntax error or an unknown field stops it), held in memory, and part of the [config fingerprint](../../operations/deploying/).

## The delivery function

The delivery function receives a finished message as `ctx.input` and hands it to your provider. It must be [`internal: true`](../internal/) and `mode: async`; it typically declares the provider's key as a [secret](../secrets/) and the provider's host in [`network`](../network/).

```ts
// ctx.input
{
  id: "d3c9ljp8hc2g00b6s1m0",           // the same on every retry of this email
  kind: "verify-email",                 // or any other kind
  from: "Acme <no-reply@acme.example>",
  reply_to: "support@acme.example",     // or null
  to:  [{ email: "ana@example.com", name: null }],
  cc:  [], bcc: [],
  subject: "Confirm your email address for blog",
  text: "…", html: "…",                 // rendered by backd, in the user's language
  locale: "en",
  data: { link: "https://…", expires_at: "2026-10-03T12:30:00Z" }   // for providers' own templates
}
```

- **It returns** an optional `{ message_id }`, stored on the email job.
- **`id` is stable**: a provider with idempotency keys (Resend's `Idempotency-Key`, for example) can use it to drop a repeat when the first attempt reached the provider but its answer was lost. Providers without that feature ignore it.
- **Failing.** Throw `ctx.error(4xx, code, message)` when the provider refuses the message (a bad address, for example): that is permanent and the email is not retried. Anything else (a thrown error, a timeout, the provider unreachable) is retried according to the function's [`retry`](../jobs/#retrying-a-failed-job); give it `retry: {attempts: 5, backoff: 1m}`.
- **No waiting.** The function runs on a worker; nothing in a request waits for it, so a failing provider never breaks sign-up.

{{< hint warning >}}
Links and tokens in a message are credentials. `backd` hides the token from the function's captured logs (replacing it with `***`, exactly), but only the function can keep it out of anything else it writes: don't log the message, don't store it, and don't forward it anywhere but your provider.
{{< /hint >}}

## How an email is sent

```
   something asks for an email           a worker picks it up
 ──────────────────────────────▶ email job ─────────────────────▶ creates the token (stores its hash),
   (kind, user, locale, redirect)    queued                        renders the message in memory,
                                                                    calls the delivery function
```

- **The email job holds no message.** What is queued is a small record: the kind, the user's id, the language and where the page after the link may send the user. No address, no text, no token. The worker builds the message when it runs, so the message exists only in memory and at your provider.
- **Tokens are created at send time.** The worker creates a token of 256 random bits for the links that need one (verifying an address, resetting a password, …) and stores **only its SHA-256 hash**, with its purpose, user, expiry and use. A token is redeemed once, in one atomic step: of two parallel attempts exactly one succeeds, and a success invalidates the user's other outstanding tokens of the same purpose. Unknown, expired, used and wrong-purpose tokens are indistinguishable. A retry of the email makes a new token; an earlier one that was never delivered simply expires.
- **Lifetimes:** 48 hours to verify an address, 1 hour to reset a password, 24 hours to confirm an email change, 7 days to undo one and to accept an invitation.
- **In the job list** an email job appears with the delivery function as its `function` and `origin: backd:email.<kind>`, and its attempts, next attempt and result (the `message_id`); never a message, a link or an address.

## Developing without a provider

`backd template email-capture --realm <realm> --database <database>` adds a delivery function that **stores each email instead of sending it**: `email-capture` in the database's `_functions`, and an `outbox` collection it writes to (schema, rules and indexes included). Point the realm at it, and run `backd` in [dev mode](../testing/#dev-mode):

```yaml
email:
  function: notifications/email-capture
  from: "Dev <dev@example.com>"
  public_url: http://localhost:8080
```

Every message, with its working links, shows up in `outbox`: read it with the API (`GET /v1/<realm>/notifications/outbox`, readable by anyone, since it exists only on a development machine) or click through verification and reset by hand.

`email-capture` is `dev_only: true`: `backd` refuses to start with it unless `BACKD_DEV=true`, because the outbox holds working links. Replace it with your provider's delivery function before you deploy anywhere real.

## Limits

So the realm can't be used to flood an address or as a relay, `backd` limits how many emails it queues: per recipient, **3 emails of one kind per hour** and **10 in all per day**, and per client address, **20 email-sending requests per hour** (the keys of `email.limits` change them). The counters live in the realm's system database, shared by every instance, with the recipient only as a hash. A request made without a signed-in user (sign-up, a password-reset request) that goes over a limit is skipped silently, so the answer reveals nothing; a signed-in user's request answers `429` with `Retry-After`.

## Upgrading

Email adds a system collection (`email_tokens`) to the realm's system database: run `backd provision` (or start with `PROVISION_MODE=apply`) after upgrading, as for any new system collection.
