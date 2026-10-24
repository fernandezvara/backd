---
title: "Compliance with backd"
description: "What backd provides for privacy compliance (personal data scope, consents, erasure, retention) and what stays your job."
icon: "shield-check"
weight: 567
toc: true
---

Apps that handle personal data usually have to answer four questions: *what do you hold about me*, *did I agree to this*, *stop using my data for that* and *delete me*. `backd` builds the mechanisms once; the decisions stay yours.

{{< hint style="warning" title="Not legal advice" >}}
`backd` gives you tools, not conclusions. Which law applies to you, the legal basis of each use of data, the texts people accept, how long you keep each thing and how fast you must answer a request are decisions for you and your lawyers. Nothing here, and nothing `backd` does by default, is a statement that you comply.
{{< /hint >}}

## What backd provides, and what stays yours

| Need | What backd gives you | What is still yours |
|---|---|---|
| **Knowing what is held about a person** | A personal data scope per collection (`personal_data:`), a report of it (`backd compliance scope`), and the system data it adds; the same model as erasure | Declaring what exempts a collection, and checking that collections mixing several people's data are narrowed with `fields` |
| **A copy of the data** | A [personal data export](../../../build/auth/personal-data/): the user asks for their own (with a fresh proof), or support produces it for a formal request; a zip with their documents, files, account data, a list of exclusions and a README in their language | Answering the request within your legal deadline, and verifying who asks when it is not the account holder |
| **Consent** | Versioned texts, records that name the exact words (a hash), withdrawal, re-acceptance, rules and email/push gating | The texts, which consents you need and why, and what each is used for |
| **Erasure** | A tombstone, per-collection policies, reduced consent records, [retention](../erasure/) | The policies you declare, backups, and data held outside `backd` (your function's calls to other services) |
| **Retention** | `audit.retention`, `functions.log_retention`, `compliance.consent_retention`, soft-delete retention | The periods themselves: they are legal decisions |
| **Minors** | Nothing: age checks and parental consent belong to the app | Whether you need them; they can be recorded as [consents](../../../build/auth/consents/#recording-what-the-app-cant-see) or by functions |

## What counts as a person's data

A user's data is, by default, **the documents whose `_meta.owner` is that user** (whatever their `on_owner_delete` action), the documents where their id or email is listed in a `pull` or `unset` field of an [erase policy](../erasure/), and the files those documents hold, plus the account itself: identities, sessions, devices, second-factor status, consents, audit records about them and the function invocations they made. Credentials, tokens, secrets and other people's details are never part of it.

Override it per collection in its `collection.yaml`:

```yaml
personal_data:
  export: exclude
  reason: "fraud scores: exempt from access requests under …"
```

or keep a collection but only the person's own part (`fields: [title, notes]`, with a `reason`). `backd` refuses to start without the reason. See [Personal data scope](../../../build/configuration/config-dir/#personal-data-scope).

### A checklist per collection

Ask of each collection:

1. **Does it hold other people's data next to the owner's?** (comments, reviews, shared notes.) Then narrow it with `fields`, or the whole document goes to whoever owns it.
2. **Is there a legal reason not to give it?** Declare `export: exclude` with that reason; a request can ask why.
3. **Is the person named elsewhere** (an array of members, a `paid_by` field)? That is what `pull` and `unset` in [`on_owner_delete`](../erasure/) are for, and they also bring those documents into the scope.
4. **Does it hold files?** They follow their documents.

Then read `backd compliance scope --realm <r>` (or `GET /_admin/compliance/scope`): every collection with what is included, which fields and every exclusion with its reason.

## Consents

Texts live with the config, `backd` records each acceptance and keeps what proves it, and apps, rules and functions use it. Read [Consents](../../../build/auth/consents/) for the whole mechanism. In short:

- **Sign-up** takes the consents accepted (`consents: { terms: "2026-10" }`); a required one that is missing or outdated is `400 consent_required`.
- **A new version** with `reaccept: true` asks people again at their next sign-in, and refuses open sessions after `reaccept_by`.
- **Withdrawal** is `DELETE /_auth/consents/<name>` for optional consents; rules see `user.consents` and `hasConsent`; `ctx.email.send` and `ctx.push.send` take `requires_consent`.
- **Consents given outside the app** are recorded by an administrator with the evidence.

## Retention

Where `backd` keeps personal data, for how long, and who decides:

| Store | Holds | Kept | Decided by |
|---|---|---|---|
| Documents | whatever you store | until deleted, or by the collection's policy at erasure | you: `collection.yaml` |
| Soft-deleted documents | the same, marked | until purged, or `soft_delete.retention` | you: `collection.yaml` |
| Audit trail | actor and target ids, actions, times | `audit.retention` (365 days by default) | you: `realm.yaml` |
| Function invocation records | caller, status, duration; the function's own console output | `functions.log_retention` (7 days) | you: `realm.yaml` |
| Async job results | input and output of the job | `functions.job_retention` (24 hours) | you: `realm.yaml` |
| Consent records | consent, version, text hash, action, method, time; IP and user agent if `consent_proof` lists them | with the account; after an erase, reduced and kept `compliance.consent_retention` (3 years by default) | you: `realm.yaml` |
| Push send records | counts, never content | `push.send_retention` | you: `realm.yaml` |
| Backups | everything at that moment | your backup retention | you |

The periods are legal decisions for you. `backd`'s defaults are conveniences, not advice.

## Recipes

### A newsletter that respects a withdrawal

1. Declare an optional consent (`marketing`) and its text; put the checkbox in the sign-up form and a switch on the account page (`backd.consents.accept` / `withdraw`).
2. In the function that sends, say whose consent it needs: `ctx.email.send({ kind: "newsletter", to_user: id, requires_consent: "marketing" })`. A member who withdrew is skipped and the call answers `no_consent`.
3. The [tutorial's chapter 17](../../../tutorial/17-consents/) builds exactly this.

### Updating the terms

1. Add the folder `consents/terms/<new version>/<locale>.md` for every language, move `current`, and add `reaccept: true` (and a `reaccept_by` date) if people have to agree again.
2. Deploy. Members are asked at their next sign-in; open sessions are refused after the date until they accept.
3. Tell them if you want: `backd consents notify --realm <r> --consent terms`.
4. Keep the old folder: it is the proof of what earlier members accepted.

### Answering a request about a person

1. Check who asks (that is on you).
2. Read their records: `GET /_admin/users/{id}` and `/identities`, `/sessions`, `/devices`, `/consents` and `/owned` (the documents they own, by collection).
3. For consents given by phone or paper, record them with their evidence: `backd consents record`.
4. For erasure, `backd user delete --realm <r> --email <e> --yes` applies the policies and reduces the consent records; the retention table above says what remains and for how long.

{{< hint style="note" >}}
Planned: a single downloadable package (the personal data export, with a localized summary), requested by the person or by an administrator on their behalf. Until then the reads above are the way to assemble an answer.
{{< /hint >}}

## Minors

Age checks and parental consent belong to the app: `backd` has no notion of a person's age. If you need them, collect the date or the guardian's consent in your own collections or with a function, and record guardian consents with `backd consents record` and their evidence.
