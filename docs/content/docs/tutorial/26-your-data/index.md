---
title: "26. Your data"
description: "A privacy notice members accept, and a button that downloads a copy of everything Shelf holds about them: a zip built by the worker, a README that explains it, and an email that links the app."
icon: "download-simple"
weight: 460
toc: true
---

Shelf now holds a lot about its members: their assets and pins, their files, private notes, and the records of what they do. A member can ask **what you hold about them** and take a copy elsewhere. This chapter adds the two halves of that promise: a **privacy notice** they accept, and a **Download my data** button that gives them everything.

{{< hint style="note" >}}
`backd` provides the mechanisms. Which notice you need, how fast you must answer a request and what is exempt are decisions for you and your lawyers; nothing here is legal advice. Read [Compliance with backd](../../operate/administration/compliance/).
{{< /hint >}}

## A second required consent

[Chapter 17](../17-consents/) made the terms a consent. The privacy notice is another one, with its text in a file:

```yaml
# config/shelf/realm.yaml
consents:
  terms:
    required: true
    current: "2026-10"
  privacy:
    required: true
    current: "2026-10"
  marketing:
    required: false
    current: "1"
```

```markdown
<!-- config/shelf/consents/privacy/2026-10/en.md -->
# Shelf privacy notice

Shelf keeps what you give it and what you do in it:

- your email address and how you sign in (a password, a link or an authenticator app);
- the assets, pins, notes and files you create, and the shares you make;
- records of what happens to your account: when you sign in, what you change, which functions you call.

We use it to run the library for your team and to keep it secure. We do not sell it. You can ask Shelf for a copy of everything it holds about you from *Account → Your data*, and you can delete your account there too.
```

Restart `backd`. A sign-up that ticks the terms but not the privacy notice is `400 consent_required`, and **members who already have an account** are asked for the new consent at their next sign-in, with the same step chapter 17 showed. The app's sign-up form and its consent step list every consent the realm asks for, so nothing in the app changes.

## Let members download their data

Turn the export on in `realm.yaml`:

```yaml
compliance:
  export: true
  export_page: http://localhost:8080/    # the app's page; within email.allowed_redirects
  export_retention: 7d
```

`backd` refuses to start unless the realm has a `storage:` (the packages are kept there; chapter 13 added it), an `email:` (the member is told when theirs is ready; chapter 10) and an `export_page` within the allowed redirects. The package holds what the erasure policies already say is a member's: the documents they own, those that name them, their files, and the account data. Read exactly what with:

```sh
curl -s localhost:8080/v1/shelf/_admin/compliance/scope -H "Authorization: Bearer $OP"
# or: backd compliance scope --realm shelf
```

It lists, per collection, what is included, which fields and every exclusion with its reason. The pins' private notes of [chapter 25](../25-private-notes/) are part of it: the server opens them for the member whose data they are, and for nobody else.

Run `docker compose restart backd`. If you upgraded an existing stack, `docker compose run --rm backd provision` first: exports add one system collection.

## The button

The account page gets a **Your data** card. Asking is a [sensitive action](../16-second-factor/), so it sits behind the same fresh-proof helper the password change uses:

```js
const job = await backd.auth.withProof((methods) => this.askProof(methods), () => backd.account.requestExport())
await backd.account.waitForExport(job.id, { onProgress: (e) => (this.message = e.step) })
```

`requestExport()` answers at once (`202`): the zip is built by the [worker](../08-async/), in the background, one step per collection. `waitForExport` follows it and the page shows what it is doing. A member may ask **once every 24 hours** (a second request is `429` with `Retry-After`); one that failed doesn't count.

When it is ready the card lists it, and **Save the zip** downloads it and shows its README:

```js
const download = await backd.account.downloadExport(id)   // a link signed for five minutes, or the zip itself
const bytes = await download.bytes()
this.readme = await readZip(bytes).text('README.html')    // read from the package you just saved
```

`lib/zip.js` (fetched with the app's other files) is a 60-line reader for the central directory of the zip, using the browser's own `DecompressionStream`: it is how the app shows the README without a library. Any zip tool opens the file too.

## What is in the zip

```
manifest.json            what it holds, with SHA-256 checksums, and every exclusion with its reason
README.html              a readable summary in the member's language
main/pins.jsonl          their pins, one JSON record per line (the private notes in the clear)
main/assets.jsonl, main/_files/…   their assets and the files those hold
_account/account.jsonl, sessions.jsonl, consents.jsonl, audit.jsonl, …   the account data
```

Never in it: the password hash, session and device tokens, second-factor secrets and recovery codes, and other people's details in the audit records. The `README.html` is a [template](../../build/auth/personal-data/#the-readme) in `config/shelf/compliance/readme/en.html`; Shelf's adds a paragraph about where its own data is.

## The email

When the zip is ready the member gets `data-export-ready`, which links **the app**, not the file: `http://localhost:8080/?export=<id>`. The app opens the account page with that export highlighted; downloading goes through the member's session, so a forwarded email gives nobody anything. In the tutorial's stack the mail lands in the Mailbox of the Admin view.

## When support has to do it

A formal request from a member, answered by an administrator, is the same package made on their behalf:

```sh
backd user export --realm shelf --email ana@shelf.example --ticket REQ-1042 --out ana.zip
```

The member is told an export was prepared (`data-export-made`); an inquiry that must not warn them takes `--no-notify --reason "…"`, kept in the audit record and listed apart from the others. The package goes to the administrator, never to the member. See [Personal data export](../../build/auth/personal-data/#when-support-does-it).

## You should see

- Signing up without ticking the privacy notice is refused; an account from before this chapter meets the consent step once.
- *Account → Your data → Download my data* asks for your password (or your second factor), shows what the worker is doing, and ends with a ready export to save. The README appears in the page, from the zip.
- A second click within a day says to try again in a few hours.
- The Mailbox holds a `data-export-ready` mail whose link ends in `?export=<id>`; opening it lands on the account page.
- `backd user export` produces a zip for any member, and `backd` audit shows `user.data_export_requested` or, with `--no-notify`, `user.data_export_requested_silently` with the reason.

The tutorial's [tour](https://github.com/fernandezvara/backd/blob/main/clients/js/examples/shelf/tour.js) checks all of it against a running stack: the required notice, the proof, the limit, the package with its README and the pin's note in the clear, that another member can't see it, the email's link, and a silent export by an administrator.
