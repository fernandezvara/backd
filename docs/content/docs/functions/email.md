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
