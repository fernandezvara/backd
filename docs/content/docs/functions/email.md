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
  locales: [en]                          # every listed language needs every template and page
  redirect_delay: 3s                     # optional: how long the result page waits
  allowed_redirects: [https://app.acme.example, "acme://"]   # where users may be sent afterwards
  redirects:                             # optional: where, per flow, when the request didn't say
    verify_email: https://app.acme.example/verified
  limits:                                # defaults shown
    per_recipient: { per_kind_per_hour: 3, per_day: 10 }
    per_ip: { per_hour: 20 }
    per_function: { per_hour: 200, per_invocation: 50 }   # custom emails from functions
    recipients_per_message: 10                            # to + cc + bcc of one ctx.email.send()
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
- `email-changed` goes to the old address with the link to undo the change, and, after an administrator's change, to the new address with no link: write `{{if .Link}}…{{else}}…{{end}}` as the default template does.
- Templates are parsed and test-rendered at startup (a syntax error or an unknown field stops it), held in memory, and part of the [config fingerprint](../../operations/deploying/).

## Links and pages

The link in an email opens a **page served by backd**, so an app only chooses where users go afterwards. The link is `public_url` + `/v1/<realm>/_auth/<flow>?token=…`, and the flows are `verify-email`, `reset-password`, `confirm-email-change`, `revert-email-change` and `accept-invitation`.

- **`GET` shows the page, `POST` acts.** Opening the link only shows a form (a button, or a form with a new password) and never uses the token up, so mail scanners and link previewers that open every link can't spoil it. The token is used when the person submits the form. A session is never issued by these pages: after verifying or resetting, users sign in as usual.
- **One error page.** A token that is expired, already used, unknown or for another flow shows the same `invalid-token` page, so the page tells nobody which case it was.
- **Pages are templates** you own: `backd template realm` writes them to `<realm>/pages/<kind>/<locale>.html` (`verify-email`, `reset-password`, `confirm-email-change`, `revert-email-change`, `accept-invitation`, `result` and `invalid-token`). Every listed locale needs every page, and they are parsed and test-rendered at startup like the email templates. A page can use `{{.Realm}}`, `{{.Locale}}`, `{{.Action}}` (where the form posts), `{{.Token}}` (the hidden field), `{{.Error}}` and `{{.ErrorCode}}` (`password_mismatch` or `password_policy`, to say it in the page's own words), `{{.Kind}}`, `{{.Redirect}}`, `{{.DelaySeconds}}` and `{{.BackURL}}`. The page is in the language of the browser's `Accept-Language`, matched against `email.locales`.
- **Where users go next.** After a success the `result` page waits `redirect_delay` (3 seconds by default, at most 30) and sends the user to the `redirect_to` the request stored with the token, or else to `email.redirects.<flow>` (`verify_email`, `reset_password`, `change_email`, `invitation`). Both must be within `email.allowed_redirects`: origins such as `https://app.acme.example` or an app's own scheme such as `acme://`. The address is checked when the email is requested and again when the page uses it.
- **Your own pages instead.** `email.links.<flow>` replaces backd's address in the email with one of your app's pages, which must be within `allowed_redirects` and contain `{token}` once (`https://app.acme.example/reset?token={token}`). That page sends the token to the JSON endpoint of the same flow, `POST /v1/<realm>/_auth/<flow>` with `{"token"}` (and `{"password"}` for `reset-password` and `accept-invitation`), which answers `204`, or `400 invalid_token` for any token that doesn't work.

All the flows are there: [email verification](../../auth/sessions/#email-verification), [password reset](../../auth/sessions/#password-reset), [email change](../../auth/sessions/#changing-the-email-address) (confirmation and undo) and [emailed invitations](../../auth/sessions/#accepting-an-invitation). A page can also show `{{.Email}}`, the address its link is about: the new one for a change, the previous one for an undo, the invited one for an invitation.

Every page, in every state, sends `Cache-Control: no-store`, `Referrer-Policy: no-referrer`, a strict `Content-Security-Policy` (no scripts, no images, no frames, forms post only to backd) and `frame-ancestors 'none'`, loads nothing from other origins, and is served without the token in any log: the access log records the route, never the query string.

{{< hint style="warning" >}}
Your reverse proxy's own access log may record the full address, token included. Log the path without the query string; the production nginx configuration in `deploy/production` already does ([Production](../../operations/production/)).
{{< /hint >}}

## Languages

Every user has a `locale`, and every email to them is rendered in it. A realm lists the languages it supports in `email.locales` and picks the fallback with `email.default_locale` (English only when neither is set).

```yaml
email:
  default_locale: en
  locales: [en, es]     # every listed language needs every template
```

- **Allowed means translated.** Every listed language needs every required template (`es.subject.txt`, `es.txt`, `es.html` for each kind), or startup fails naming the missing files. To add a language, copy a kind's English files, translate them, and list it.
- **At sign-up** the user's language is the best listed match for the `locale` in the request or the browser's `Accept-Language`: exact (`es-MX`), then the language (`es`), then the default, never failing. **Changing it** later is an explicit request, `PATCH /_auth/me {"locale"}`, and must name a listed language. See [Sessions](../../auth/sessions/#language).
- **Which language an email uses:** the one the request asked for when there is one, else the user's own, else `default_locale`.

## The delivery function

The delivery function receives a finished message as `ctx.input` and hands it to your provider. It must be [`internal: true`](../internal/) and `mode: async`; it typically declares the provider's key as a [secret](../secrets/) and the provider's host in [`network`](../network/). The [cookbook](../cookbook/#deliver-email-with-postmark) has a complete, tested one for Postmark, step by step, and a table of what changes for other providers.

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
- **Lifetimes:** 48 hours to verify an address (`account.tokens.verify_email`), 1 hour to reset a password (`account.tokens.reset_password`), 24 hours to confirm an email change (`account.tokens.change_email`), 7 days to undo one (`account.tokens.revert_email_change`), and as long as the invitation itself to accept one.
- **In the job list** an email job appears with the delivery function as its `function` and `origin: backd:email.<kind>`, and its attempts, next attempt and result (the `message_id`); never a message, a link or an address.

## Sending email from functions

A function that declares `email: true` in its [`function.yaml`](../reference/) can send **custom emails** with `ctx.email.send()`, through the realm's templates, languages, delivery function, queue and retries:

```ts
await ctx.email.send({ kind: "order-shipped", to_user: order._meta.owner, data: { order_no: order.no } });   // a user of the realm
await ctx.email.send({ kind: "invoice", to: [order.customer_email], cc: [accounting], data: { invoice_no: 7 } }); // addresses
```

- **`kind`** is a folder of the realm's [`email/`](#templates) (`email/order-shipped/`), with the same files and languages as the others, checked at startup. A function can't send the kinds `backd` sends itself (`verify-email`, `reset-password`, …). A template prints the function's `data` as `{{.Data.order_no}}`; if the function leaves a field out, that send fails permanently (the delivery function is never called) and shows in the job list.
- **Recipients:** `to_user` (a realm user's id, in that user's language, which `backd` looks up) **or** `to` (addresses, in the realm's `default_locale` unless you pass a `locale`), plus optional `cc` and `bcc`. **Any recipient is allowed:** `backd` doesn't keep a list of who a function may write to. A function can't send a subject or a body of its own: the text comes from the template.
- **It returns** `{ id, status: "queued" }`, the id of the email's job. The email is sent by a worker, like the others: a slow provider never slows the function down.
- **Caps**, which bound what a function can do whoever the recipients are, under [`email.limits`](#configuration): messages per function per hour (`per_function.per_hour`, 200), per invocation (`per_invocation`, 50), recipients per message (`recipients_per_message`, 10), and the per-recipient limits that apply to every email. Refused attempts count, so a function that keeps trying is the one that gets stopped. Over a cap `ctx.email.send()` throws an error with `code: "email_limited"` and `retry_after` (seconds); nothing is queued, and the function decides what to answer.
- **What `backd` keeps.** An email job from a function stores the recipients' addresses and the `data` it was given, until [job retention](../jobs/) ends (the account emails store neither). Don't put secrets in `data`. In the job list such a job has `origin: function:<database>/<name>` and its `email_kind`; filter by `origin` to see (and count) what one function sent. Addresses never appear in logs or the audit trail. Startup logs each function that can send email.

{{< hint danger >}}
**A function that sends email sends from your domain.** If anyone can call it and it takes the recipient or the text from the request, it is a spam and phishing relay: blocklists, and a suspended provider account. The caps below only bound the damage.
{{< /hint >}}

Before you ship one:

- Require a verified user (`invoke: "user != nil && user.email_verified"`) and a `rate_limit`.
- Take recipients from your own data (an order's owner), never from the input, and fix the `kind` in the code.
- Don't print caller-written text in a template that goes to someone else, and don't put secrets in `data`.
- Watch your provider's bounce and complaint rates. At startup `backd` warns about a function that can send email and allows anonymous callers.

The safe shape, from the workshop realm: customers write to the shop's **own** mailbox. The recipient and the kind are fixed in the code, the input schema refuses any other field, only verified users may call it and only three times an hour:

{{< example-file path="workshop/main/_functions/contact/function.yaml" >}}

{{< example-file path="workshop/main/_functions/contact/index.ts" >}}

And the refund function there emails the customer, with the recipient taken from the order:

{{< example-file path="workshop/main/_functions/refund/index.ts" lines="42-50" >}}

[`@backd/functions-testing`](../testing/#unit-testing-a-functions-logic) gives you `ctx.email.send` in tests, with the same refusals and caps (`createContext({ email: true })`, `sentEmails()`); test that your function ignores recipients in its input.

## Developing without a provider

`backd template email-capture --realm <realm> --database <database>` adds a delivery function that **stores each email instead of sending it**: `email-capture` in the database's `_functions`, and an `outbox` collection it writes to (schema, rules and indexes included). Point the realm at it, and run `backd` in [dev mode](../testing/#dev-mode):

```yaml
email:
  function: notifications/email-capture
  from: "Dev <dev@example.com>"
  public_url: http://localhost:8080
```

Every message, with its working links, shows up in `outbox`: read it with the API (`GET /v1/<realm>/notifications/outbox`, readable by anyone, since it exists only on a development machine) or in the Mailbox.

**The Mailbox** is a small web app of the [local stack](../../getting-started/) (`make example`, then <https://localhost:8443/example/mailbox/>): it lists a realm's captured emails (`?realm=<realm>`, the `workshop` realm by default; the two expenses examples use it too, since they require verified addresses) and shows each one, text and HTML, with a button that opens its link, so you can click through verification and password reset by hand. The workshop realm is configured exactly like this, and its function is the tested one the template writes:

{{< example-file path="workshop/notifications/_functions/email-capture/function.yaml" >}}

{{< example-file path="workshop/notifications/_functions/email-capture/index.ts" >}}

The local stack runs `backd` in dev mode for this (`BACKD_DEV=true`, with `BACKD_DEV_ANY_ADDR=true` because its ports are published on `127.0.0.1` only).

`email-capture` is `dev_only: true`: `backd` refuses to start with it unless `BACKD_DEV=true`, because the outbox holds working links. Replace it with your provider's delivery function before you deploy anywhere real.

To unit-test your own delivery function or a function that sends email, [`@backd/functions-testing`](../testing/#unit-testing-a-functions-logic) provides `emailMessage()` (a message in the contract's shape, as `ctx.input`) and `fakeEmail()` (a fake `ctx.email.send` that records what a function sends).

## Limits

So the realm can't be used to flood an address or as a relay, `backd` limits how many emails it queues: per recipient, **3 emails of one kind per hour** and **10 in all per day**, and per client address, **20 email-sending requests per hour** (the keys of `email.limits` change them). The counters live in the realm's system database, shared by every instance, with the recipient only as a hash. A request made without a signed-in user (sign-up, a password-reset request) that goes over a limit is skipped silently, so the answer reveals nothing; a signed-in user's request answers `429` with `Retry-After`. Functions that [send email](#sending-email-from-functions) have caps of their own on top (per function, per invocation, per message).

## Upgrading

Email adds a system collection (`email_tokens`) to the realm's system database: run `backd provision` (or start with `PROVISION_MODE=apply`) after upgrading, as for any new system collection.
