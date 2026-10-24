# Design: compliance — personal data export and consent records

- **Issues:** features 19.1–19.11 (milestone `19-compliance`)
- **Status:** decided (missing consent texts warn at startup instead of failing). Issue 19.2 (the export) was deferred until export packages (17.1) existed; they do now and it is built (see the docs page "Personal data export")
- **Related designs:** [account-lifecycle.md](./account-lifecycle.md) (erasure and `on_owner_delete`), [export-import.md](./export-import.md) (package format), [organizations.md](./organizations.md), [mfa-passwordless.md](./mfa-passwordless.md) (sign-in challenges, reauth), [email-delivery.md](./email-delivery.md) and [push-notifications.md](./push-notifications.md) (gated sends), [field-encryption.md](./field-encryption.md) (decryption in personal exports), [file-storage.md](./file-storage.md)

## 1. The idea

Two features most apps under privacy law need, built once in backd:

- **Personal data export (access and portability):** a user downloads everything the realm holds about them, as a structured package with a readable summary. Support can produce the same for formal requests. What counts as "theirs" comes from the **same per-collection model as erasure**, and every exclusion is written down with its reason.
- **Consent records:** versioned texts (terms, privacy, marketing) kept with the config, accepted at sign-up and again when they change substantially, recorded append-only as proof, withdrawable, visible to rules, and enforced on email and push sends.

**Not legal advice:** backd provides the mechanisms. The operator decides legal bases, retention periods, response deadlines and texts, and the docs say so plainly. Age checks and parental consent belong to apps.

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Export scope | derived from erasure: owned documents, `pull` documents, owned files; organization documents the user created; `personal_data` override with a required reason to exclude |
| System data | account, identities, sessions, devices, MFA status, memberships, invitations, consents, audit records about them, invocations they made; never credentials, tokens, secrets or other people's data |
| Requesting | self-service with reauth (one per 24 h) or by an admin on behalf of the user; built as a job |
| Delivery | email to the app's download page; download with a session through short-lived signed links; deleted after 7 days; **file storage required** |
| Format | the export package format plus a localized `README.html`; encrypted fields decrypted; read and `reveal` rules don't limit scope; exclusions listed |
| Notification | the user is told about exports made on their behalf, unless `--no-notify` with an audited reason |
| Consent texts | `realm.yaml` declares consents; texts versioned in the config per locale, hashed and served |
| Records | append-only, with version, text hash, action, time, method, and IP and user agent (configurable) |
| Collecting | at sign-up in every method; re-acceptance through a challenge at sign-in; `reaccept_by` for open sessions |
| Using | withdrawal of optional consents; `user.consents` and `hasConsent`; `requires_consent` on email and push; admin recording with evidence |
| Erasure | records reduced (no IP or user agent, user id replaced by an email HMAC) and kept for `consent_retention` (3 years) |
| Minors | out of scope; documented |

## 3. Personal data scope (issue 19.1)

### 3.1 Documents

Included by default, following the erasure model ([account-lifecycle.md](./account-lifecycle.md) §5):

- documents whose `_meta.owner` is the user, whatever their `on_owner_delete` action;
- documents where the user's id appears in a `pull` field;
- **organization documents the user created** (still `_meta.owner`), included whole by default;
- files referenced by those documents.

### 3.2 Override

```yaml
# collection.yaml
personal_data:
  export: include            # include (default) | exclude
  fields: [title, notes]     # optional: only these fields, plus id and _meta
  reason: "fraud scores; exempt under …"   # required with exclude, and with fields when they leave data out
```

- **Startup checks:** `fields` exist in `schema.json`, and `reason` is present when required.
- **Organization collections** whose documents can hold colleagues' data (comments, reviews) use `fields` to export only the user's parts. The docs give a checklist ("does this collection hold other people's data?").
- **Scope report:** `GET /_admin/compliance/scope` and `backd compliance scope --realm <r>` list per collection what's included, which fields, and what's excluded with reasons. They **flag organization collections without a `fields` list**, so the decision is visible.

### 3.3 System data

| Included | Never included |
|---|---|
| account: id, email, verified, locale, realm roles, created, last sign-in | password hashes |
| identities: provider, provider email and subject, linked dates | provider tokens |
| sessions: created, last used, sign-in methods, IP and user agent where stored | session and refresh tokens |
| push devices: platform, name, created, last seen | device tokens |
| MFA: enabled, methods, recovery codes remaining, last used | TOTP secrets, recovery code hashes |
| organizations: memberships, roles, teams, joined; invitations addressed to them | invitation tokens |
| consents: full history | — |
| audit records where they are actor or subject: event, time, ids | other users' details in those records |
| function invocations they made: function, time, status | inputs, outputs, logs |

## 4. Personal data export (issue 19.2)

### 4.1 Requesting

- **Self-service:** `POST /_auth/me/export` with `X-Backd-Reauth` starts a job (progress as job steps). It's limited to one per user per 24 hours (`429` with `Retry-After`), and `GET /_auth/me/exports` lists exports with status and expiry.
- **On behalf of a user:** `backd user export --realm <r> --email <e> [--ticket <ref>] [--no-notify --reason "<text>"]` (the CLI's group is `user` and it finds users by `--email`, like its other commands) and `POST /_admin/users/{id}/export`, by a full admin with reauth.
  - The package goes to the admin.
  - The user is notified (`data-export-made`) unless `--no-notify`, which needs a reason stored in the audit record.
  - Silent exports are listed separately in the audit view.

### 4.2 Building the package

- **Format:** the [export package](./export-import.md) layout: `manifest.json`, `<db>/<collection>.jsonl`, `<db>/_files/`, plus `_account/*.jsonl` for system data, and a `README.html`. The container is a **`.zip`**, which people open without tools (the realm export's `.tar.gz` is for operators); the manifest marks it `personal` and `partial`, so it is never importable.
- **`README.html`:** localized in the user's language from a template (`<realm>/compliance/readme/<locale>.html`, written by `backd template realm`). It lists, in plain words, what each part is, counts, what was excluded and why, and how long the package is kept.
- **Scope comes from §3, not from rules:** read and `reveal` rules don't remove documents or fields. Access rights cover data *about* the person.
- **Encrypted fields are decrypted by the server.** This is a documented exception to "the server never decrypts for exports" (when field encryption, 16.x, exists).
- **The manifest and README** list every exclusion with its reason.

### 4.3 Delivery

- **File storage required:** personal exports need the realm's file storage. A realm with export enabled (`compliance.export: true`) and no `storage:` fails startup, saying storage is required.
- **Ready email:** when the job ends, `data-export-ready` is emailed with a link to the **app's** download page (`compliance.export_page`, within `allowed_redirects`), never a file link.
- **Downloading:** `GET /_auth/me/exports/{id}/download` with the user's session redirects to a short-lived signed link. Admin downloads go through the admin API or CLI.
- **Retention:** packages are deleted after `compliance.export_retention` (default 7 days).
- **Audit:** requests, completions and downloads are recorded (`user.data_exported`, `user.data_export_downloaded`).

## 5. Consent documents and records (issue 19.3)

### 5.1 Declaring consents

```yaml
# realm.yaml
consents:
  terms:
    required: true
    current: "2026-10"
    versions:
      "2026-10": { reaccept: true, reaccept_by: 2026-11-15 }
      "2026-03": { reaccept: false }
  privacy:
    required: true
    current: "2026-03"
  marketing:
    required: false
    current: "1"
compliance:
  consent_proof: [ip, user_agent]   # what each record stores besides time, method and text hash
  consent_retention: 3y             # after erasure; 0 deletes at erasure
```

- **Texts:** `<realm>/consents/<name>/<version>/<locale>.md`, for every listed locale. backd loads them at startup, computes SHA-256 per text, includes them in the config fingerprint, and serves them at `GET /v1/{realm}/_consents/<name>/<version>?locale=<l>`, with `GET /v1/{realm}/_consents` listing current versions.
- **Old versions stay in the folder** and in git: they are the proof.
- **Startup checks:** `current` exists in every locale; `reaccept_by` is a date.
- **Startup warning:** a query over `consent_records` lists the `consent`+`version` pairs referenced by records; a pair whose texts are missing produces a warning naming it — the realm still starts (the proof chain degrades, it doesn't break).

### 5.2 Records

`<realm>___system.consent_records`, **append-only**:

| Field | Content |
|---|---|
| `user` | user id (replaced at erasure, §8) |
| `consent`, `version` | name and version |
| `locale`, `text_hash` | the text shown |
| `action` | `accepted` or `withdrawn` |
| `method` | `signup`, `prompt`, `api`, `admin` |
| `evidence` | for `admin`: the reference given |
| `at` | time |
| `ip`, `user_agent` | when listed in `consent_proof` |

- **Current state:** the latest record per consent, kept as a small summary on the user for rules.
- **No updates or deletes,** except the reduction at erasure and deletion after retention.

## 6. Collecting consents (issue 19.4)

- **Sign-up:** every method takes the consents accepted:
  - password and magic-link sign-up: `consents: { terms: "2026-10", privacy: "2026-03", marketing: "1" }`;
  - provider sign-ups: passed to `start` and stored with the attempt.

  Missing or outdated required consents answer `400 consent_required` with the current versions. Optional ones are recorded only when sent. Records use method `signup`.
- **Re-acceptance:** when a required consent's current version has `reaccept: true` and the user hasn't accepted it, sign-in returns, after the first factor and MFA:
  ```json
  { "consent_required": true, "challenge": "…", "consents": [ { "name": "terms", "version": "2026-10" } ] }
  ```
  `POST /_auth/consents/accept { challenge, consents }` records them (method `prompt`) and issues the session. It's the same challenge mechanism and shape as MFA (5 minutes, hash-only).
- **Open sessions** continue until the version's `reaccept_by` date. After it, API calls by users missing the consent answer `403 consent_required`, until they accept through `POST /_auth/consents` with their session. Consent endpoints and sign-out stay available.
- **Corrections** (`reaccept: false`) just become current, with no prompt.
- **Notifying:** `backd consents notify --realm <r> --consent terms` sends `consent-updated` to affected users (those missing the current version), rate-limited by the email limits, never automatically — publishing a version never sends email on its own.

## 7. Withdrawal and use (issue 19.5)

- **`GET /_auth/consents`:** current state and history for the user.
- **`POST /_auth/consents`:** accepts optional consents, or newer versions.
- **`DELETE /_auth/consents/<name>`:** records `withdrawn` for **optional** consents. Required consents answer `409 consent_required_for_account`, pointing to account deletion. Withdrawing is as easy as giving.
- **Rules:** `user.consents` (name to accepted version) and `hasConsent("marketing")`. **Functions:** `ctx.user.consents`.
- **Gated sends:** `ctx.email.send({ …, requires_consent: "marketing" })` skips users without that consent and reports them as `no_consent` in the result. The same applies to `ctx.push.send` (19.11), whose audience filter can also select by consent.
- **Admin recording:** `backd consents record --realm <r> --user <u> --consent <name> --version <v> --action accepted|withdrawn --evidence "<reference>"` and the admin API, for consents given on paper or by phone. Method `admin`, evidence stored, audited.

## 8. Erasure and retention (issue 19.6)

- **At erasure:**
  - `ip` and `user_agent` are removed from the person's records;
  - `user` is replaced by an HMAC-SHA-256 of the normalized email, keyed by a realm secret, so a later dispute from that address can be matched without keeping it;
  - names, versions, text hashes, actions, methods and times stay.
- **After `consent_retention`** (default 3 years after erasure), a worker deletes the records. `0` deletes them at erasure.
- **The retention page** lists consent records, personal export packages and their periods, and explains that the periods are legal decisions for the operator.

## 9. JavaScript client (issue 19.7)

- **Data export:** `client.account.requestExport()` (runs the reauth helper), `exports()` and `downloadExport(id)`.
- **Consents:** `client.consents.current()`, `text(name, version, locale)`, `accept()` and `withdraw()`.
- **Sign-up and sign-in:** `signUp({ …, consents })`. Sign-in results include `{ consentRequired, challenge, consents }` next to the MFA shapes, and `acceptConsents(challenge, consents)`.
- **Errors:** `consent_required` from the API surfaces as a typed error, so apps can show the prompt.

## 10. Shelf (issue 19.8)

- **Sign-up** shows terms and privacy (texts from backd) and an optional marketing consent.
- **A "download my data" button** shows the README and package.
- **Marketing:** a newsletter function sends with `requires_consent: "marketing"`, and a user who withdraws stops receiving it.
- **Re-acceptance:** publishing a new terms version with `reaccept: true` shows the prompt at the next sign-in.
- **Tests:** Shelf code and tests are updated, and earlier chapters still pass.

## 11. Security considerations

- **Exports are complete copies of a person's data:** reauth, rate limits, session-only downloads, short-lived links, 7-day retention, audit, and notification when made on someone's behalf.
- **Never in exports:** credentials, tokens, secrets or other people's details. Colleagues' data in organization documents is controlled with `fields`, flagged by the scope report.
- **Consent proof:** exact texts are hashed and versioned, and records are append-only. Admin records require evidence.
- **Gated sends** enforce withdrawal even when app code forgets.
- **Erasure** keeps only what proves consent, for a period the operator controls.

## 12. Documentation (issue 19.9)

- **"Compliance with backd":** what backd provides and what the operator must still do (legal bases, texts, retention periods, answering requests within the legal deadline, minors), with a clear "not legal advice" note.
- **Personal data:**
  - declaring `personal_data`, the organization collections checklist, reading the scope report;
  - the export flow for users and support;
  - the package and README.
- **Consents:**
  - writing and versioning texts, `reaccept` and `reaccept_by`;
  - sign-up in each method, the challenge, withdrawal;
  - rules and gated sends;
  - admin records with evidence.
- **Retention page** updated. Recipes: a newsletter with marketing consent, a terms update, a support request answered with an export.
- **Minors:** age checks and parental consent belong to the app, recorded with admin records or functions.

## 13. Issues

| id | Issue | Depends on |
|---|---|---|
| 19.1 | Personal data scope and report | 4.6 |
| 19.2 | Personal data export | 19.1, 17.1, 13.4, 8.1, f6 |
| 19.3 | Consent documents and records | 4.2e |
| 19.4 | Consents at sign-up and re-acceptance | 19.3, 13.1, 7.12a |
| 19.5 | Withdrawal, rules and gated email | 19.4, 4.2g |
| 19.6 | Consent records at erasure and retention | 19.3, 4.6 |
| 19.7 | Compliance in the JavaScript client | 19.2, 19.5, 4.7 |
| 19.8 | Shelf compliance chapter | 19.7 |
| 19.9 | Compliance documentation | 19.8, 19.6 |
| 19.10 | Security review of compliance features | 19.9 |
| 19.11 | Consent gating for push | 19.5, 12.3 |

**When to build:** consents (19.3–19.6) right after MFA (13), since they share the sign-in challenge. Personal data export (19.1–19.2) after export packages (17.1). With field encryption (16), 16.6 adds decryption to personal exports.

## 14. Alternatives considered

- **A separate personal-data declaration, or owned documents only:** rejected for the erasure model with an override.
- **Account basics only, or including invocation inputs and logs:** rejected.
- **A synchronous download, or admin-only exports:** rejected for jobs with self-service.
- **Only what the user can see, or ciphertext for encrypted fields:** rejected because access covers data about the person.
- **Organization documents never included, or always whole:** rejected for whole by default with `fields` and a flag in the report.
- **Never or always notifying exports on behalf of a user:** rejected for notification by default with audited silence.
- **Packages in the system database without storage:** rejected; storage is required, since apps needing this are already big.
- **Texts hosted by the app, or consents modeled per app:** rejected for versioned, hashed texts in the config.
- **Apps enforcing consents themselves, or re-acceptance on every version:** rejected for challenges and `reaccept`.
- **Read-only consents, or gating by template:** rejected for explicit `requires_consent`.
- **Deleting consent records at erasure, or keeping them forever:** rejected for reduced records with retention.
- **Never or always storing IP and user agent:** rejected for stored by default with `consent_proof`.
- **Minimum age or guardian flows:** out of scope.

## 15. Follow-ups

- Self-service MFA recovery and other flows may need consent prompts in hosted pages (7.34).
- Admin UI: scope report, export requests, consent versions and adoption.
- An optional age gate if apps for younger audiences appear.
- Records of processing activities generated from `personal_data` declarations.
