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
- **`reaccept`** marks a version as a substantial change that people who accepted an earlier one must accept again, and `reaccept_by` is the date after which sessions of people who haven't are refused. They only take effect once sign-up and sign-in enforce consents (the next step of this feature); a correction (`reaccept: false`) just becomes current.
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
