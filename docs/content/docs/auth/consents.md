---
title: "Consents"
description: "Versioned consent texts kept with the config, accepted and recorded as proof, and served to apps."
icon: "fact_check"
weight: 566
toc: true
---

Apps that handle personal data usually have to show people a text (terms, a privacy notice, marketing) and keep proof of what they accepted and when. `backd` keeps the **texts** with the rest of the configuration, serves them to apps, and records acceptances in an **append-only** history.

{{< hint style="note" >}}
`backd` provides the mechanisms. Which texts you need, on which legal basis and for how long you keep the records are decisions for you and your lawyers; nothing here is legal advice.
{{< /hint >}}

## Declaring consents

In `realm.yaml`:

```yaml
consents:
  terms:
    required: true          # needed to have an account
    current: "2026-10"      # the version new accounts accept
    versions:
      "2026-10": { reaccept: true, reaccept_by: 2026-11-15 }
      "2026-03": { reaccept: false }
  marketing:
    required: false         # optional: given and withdrawn at will
    current: "1"
compliance:
  consent_proof: [ip, user_agent]   # what each record stores besides time, method and text hash
  consent_retention: 3y             # kept after erasure; 0 deletes records at erasure
```

- **Names** are lower-case letters, digits and underscores; **versions** are any label without slashes (`2026-10`, `3`).
- **`required`**: a required consent is needed for an account; an optional one is given and withdrawn at will.
- **`reaccept`** marks a version as a substantial change that people who accepted an earlier one must accept again, and `reaccept_by` is the date after which open sessions of people who haven't are refused (see [Re-acceptance](#re-acceptance)). A correction (`reaccept: false`) just becomes current: nobody is asked.
- **`compliance.consent_proof`** chooses whether the records store the IP address and the user agent (both by default).

## The texts

Texts are Markdown files, one per version and language of the realm:

```
<realm>/consents/<name>/<version>/<locale>.md
```

- `backd` loads them at startup, computes the **SHA-256** of each and includes them in the [config fingerprint](../../operations/deploying/): changing a text changes the config.
- **Startup fails** when the `current` version has no text in some language of the realm, or when a version listed under `versions` has none.
- **Keep old versions** in the folder (and in git): they are the proof of what people accepted. At startup `backd` warns, naming each consent and version, when records refer to a version whose text is no longer in the config; the realm still starts.

## Serving the texts

Apps show the text before an account exists, so these routes need no session:

```http
GET /v1/shelf/_consents
```

```json
{ "items": [
  { "name": "terms", "required": true, "current": { "version": "2026-10", "reaccept": true, "reaccept_by": "2026-11-15" }, "locales": ["en", "es"] },
  { "name": "marketing", "required": false, "current": { "version": "1", "reaccept": false }, "locales": ["en", "es"] }
] }
```

```http
GET /v1/shelf/_consents/terms/2026-10?locale=es
```

```json
{ "name": "terms", "version": "2026-10", "locale": "es", "text": "…", "hash": "9f2c…", "current": true }
```

`locale` is matched like the language of an email: the best of the realm's languages, else the default. `hash` is what a consent record keeps.

## Accepting at sign-up

Every sign-up method takes the consents accepted, as the consent's name and the version shown:

```json
POST /v1/shelf/_auth/signup
{ "email": "ada@example.com", "password": "…", "consents": { "terms": "2026-10", "privacy": "2026-03", "marketing": "1" } }
```

- **Password sign-up:** every **required** consent must be there at its **current** version, or the answer is `400 consent_required`, with a detail per consent saying the version to accept, and no account is made. Optional consents are recorded only when sent. A name or version the realm doesn't have is `400 validation_error`.
- **Magic links** and **provider sign-ins** take `consents` in the request that starts them (`POST /_auth/magic-link`, `POST /_auth/oauth/{provider}/start` and `/token`) and apply them if the sign-in makes a new account. They can't refuse an address up front, so a new account made without them gets the [prompt](#re-acceptance) at its first sign-in, like anyone who never accepted.
- **Records** of these have the method `signup`, with the IP address and user agent when [`compliance.consent_proof`](../../configuration/realm/) lists them.

## Re-acceptance

A user has consents to accept when a required consent was never accepted, or when they hold an older version and the **current version has `reaccept: true`**. (Only the current version counts: if you publish a correction after a version that asked for re-acceptance, keep `reaccept: true` on it so people who skipped the first one are still asked.)

**At sign-in**, after the first factor and any second factor, the answer is a challenge instead of a session:

```json
{ "consent_required": true, "challenge": "…", "consents": [ { "name": "terms", "version": "2026-10" } ] }
```

The app shows the texts and sends the acceptance. It is the same kind of step as a [second factor](../mfa/): the challenge lives 5 minutes, works once, and only the hash is stored.

```http
POST /v1/shelf/_auth/consents/accept
{ "challenge": "…", "consents": { "terms": "2026-10" } }
```

The answer is the session, as for a login. Leaving a consent out is `400 consent_required` and the challenge stays valid. Records get the method `prompt`.

**Open sessions** continue until the `reaccept_by` date of the version (UTC, the whole day included). After it, requests of a user who hasn't accepted answer `403 consent_required`, except these, which stay open so the person can accept or leave: `GET /_auth/me` (its `to_accept` lists what is missing), `POST /_auth/consents`, `POST /_auth/logout` and `logout-all`.

```http
POST /v1/shelf/_auth/consents
Authorization: Bearer …
{ "consents": { "terms": "2026-10" } }
```

It answers `{ "consents": { … }, "to_accept": [ … ] }`; the records get the method `api`. API keys are not affected: they act for your servers, not for a person.

{{< hint style="note" >}}
A realm that starts declaring a required consent asks **every** existing user to accept it at their next sign-in, because none has a record yet; set `reaccept_by` on that version to decide when open sessions are cut off.
{{< /hint >}}

### Telling people

Publishing a version never sends email by itself. To ask the users who hold an older version to review the new one:

```sh
backd consents notify --realm shelf --consent terms
```

It queues the `consent-updated` [email](../../functions/email/) (the consent's name is in `.Data.event`) for the users who accepted an older version, and for a required consent those who accepted none. The [email limits](../../functions/email/) apply, so a big realm may need to run it again later. Audited as `consents.notify`.

## Records

Each acceptance or withdrawal is a record in the realm's system database, never updated or deleted by the app:

| Field | Meaning |
|---|---|
| `user`, `consent`, `version` | who, which consent and which version |
| `locale`, `text_hash` | the language shown, and the SHA-256 of that exact text |
| `action` | `accepted` or `withdrawn` |
| `method` | how it was given: `signup`, `prompt`, `api` or `admin` |
| `evidence` | for `admin` records, the reference given (a ticket, a paper form) |
| `at` | when |
| `ip`, `user_agent` | when listed in `compliance.consent_proof` |

The latest accepted version of each consent is also kept as a small summary on the user, which is what rules and sign-in read.

{{< hint style="warning" >}}
A text must never be edited in place once people have accepted it: publish a new version and keep the old file. The hash in each record names the words that were shown, and an edited file would no longer match it.
{{< /hint >}}
