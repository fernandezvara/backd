---
title: "Personal data export"
description: "Let a user download everything the realm holds about them, and let support produce the same for a formal request."
icon: "download-simple"
weight: 565
toc: true
---

A person can ask for a copy of the data you hold about them (their [public files](../../files/file-fields/#public-fields) included: public means anyone with the address can read them, not that they stop being theirs), and people who hold your support queue get formal requests for it. `backd` builds the **package** once: a zip with their documents, their files, the data about their account, a list of everything it leaves out and why, and a `README.html` in their language.

{{< hint style="note" >}}
`backd` provides the mechanism. How fast you must answer, who may ask for whom, and what is exempt from access are decisions for you and your lawyers; nothing here is legal advice. See [Compliance with backd](../../../operate/administration/compliance/).
{{< /hint >}}

## Turn it on

```yaml
# realm.yaml
compliance:
  export: true
  export_page: https://app.example.com/my-data     # your app's download page
  export_retention: 7d                             # how long a package is kept (default 7d)
```

`backd` refuses to start when the realm sets `export` without what it needs: the packages are kept in the realm's [file storage](../../files/) (`storage:`), the user is told by email (`email:`), and `export_page` must be a URL within the realm's `allowed_redirects`. Run `backd provision` once when you upgrade: exports add the `user_exports` system collection.

What a package holds comes from the [personal data scope](../../configuration/config-dir/#personal-data-scope) (`personal_data:` in each `collection.yaml`), the same model as erasure. Read it with `backd compliance scope --realm <r>` before you turn exports on.

## What the user does

Every call needs the user's session, and asking for an export also a fresh [re-authentication](../reauth/) (`X-Backd-Reauth`): a stolen session alone can't start taking someone's data.

```
POST /v1/{realm}/_auth/me/export                       → 202 {id, status: "queued", …}
GET  /v1/{realm}/_auth/me/exports                      → {items: [{id, status, expires_at, size, counts}]}
GET  /v1/{realm}/_auth/me/exports/{id}                 → one, with `step` while it runs
GET  /v1/{realm}/_auth/me/exports/{id}/download        → 302 to a link signed for five minutes
```

- **One a day.** A second request within 24 hours is `429` with `Retry-After`; an export that failed (or whose job an administrator cancelled) does not count.
- **It is built in the background** as a job and streamed to the storage as it is written, so a user with a lot of files costs the server no memory. `status` goes `queued`, `running`, `ready`, and later `expired` (or `failed`).
- **The email links your app, never the file.** When it is ready the user gets `data-export-ready` with a link to `export_page?export=<id>`. Your page signs the user in, calls `download` and sends the browser to the link. A page that fetches the API can't follow a redirect to another origin: send `Accept: application/json` and the answer is `200 {"url", "expires_at"}`. A realm whose files are proxied (their storage is not reachable by clients) gets the zip itself.
- **It expires.** The package is deleted after `export_retention` and the record says `expired`; the user asks again.

## What a package holds

```
manifest.json            what it holds, with SHA-256 checksums, and every exclusion with its reason
README.html              a readable summary in the user's language
app/orders.jsonl         one JSON record per line: the documents of each collection
app/_files/<id>          the files those documents hold
_account/account.jsonl   the account: address, language, roles, dates
_account/identities.jsonl, sessions.jsonl, devices.jsonl, mfa.jsonl, organizations.jsonl,
_account/invitations.jsonl, consents.jsonl, audit.jsonl, invocations.jsonl
```

- **The user's data, not what rules let them read.** Read and `reveal` rules decide who may *use* a document, not whose data it is, so they remove nothing from the package. Encrypted fields come out decrypted: the server opens them for this, and nothing else leaves it.
- **Documents:** the ones the user owns (whatever their `on_owner_delete`), and those that name them in a `pull` or `unset` field of an [erase policy](../../../operate/administration/erasure/), whole unless the collection narrows them with `personal_data.fields`.
- **Never in it:** password hashes, session, device and invitation tokens, second-factor secrets and recovery codes, function inputs, outputs and logs, and other people's details in the audit records (they carry the event, the time and the ids).
- **Exclusions are listed.** A collection that `personal_data` excludes, fields it leaves out, a file missing from the storage: each is in the manifest and the README with the reason you gave.
- **It is a zip, not a backup.** The layout is that of an [export package](../../../operate/administration/export-import/), but the manifest marks it `personal` and `partial`, so `backd import` refuses it.

## The README

`README.html` explains the package in plain words. backd ships it in English and Spanish; a realm writes its own per language in `_realm/compliance/readme/<locale>.html` (`backd template realm` writes the English one to start from). The template is logic-less, like the [email templates of an organization](../../organizations/email-templates/): values, sections and inverted sections, no code, every value escaped.

| Tag | Prints |
|---|---|
| `{{Realm}}`, `{{Email}}`, `{{Generated}}`, `{{Days}}` | the realm, the user's address, when it was made, how many days the copy is kept |
| `{{Count.sessions}}` | the records of each `_account` part (`account`, `identities`, `sessions`, `devices`, `mfa`, `organizations`, `invitations`, `consents`, `audit`, `invocations`) |
| `{{#Collections}}{{#Items}}{{Part}} {{Documents}}{{/Items}}{{Files}}{{/Collections}}` | the files with documents, and how many files |
| `{{#Exclusions}}{{#List}}{{What}} {{Reason}}{{/List}}{{/Exclusions}}` | what was left out, and why |
| `{{#Truncated}}{{#List}}{{Name}}{{/List}}{{/Truncated}}` | parts that stopped at a limit (audit records and function calls are capped at 20 000 each) |

A syntax error stops `backd` at start, with the file, line and column. The user's language is picked from their profile: your template for it, else the built-in one, else your default language.

## When support does it

For a formal request, an administrator produces the same package:

```
backd user export --realm shop --email ada@example.com --ticket REQ-1042 --out ada.zip
```

or `POST /_admin/users/{id}/export` with `{"ticket"}`. It needs **full admin access** and a fresh re-authentication (an admin API key needs no proof). The package goes **to the administrator**, through the API, never to the user: `GET /_admin/users/{id}/exports/{export}/download`, or the CLI, which waits for it and writes the file (never over one that exists). `backd user export --no-wait` prints the id and returns; `backd user export-download --email … --export <id>` fetches it later. A user's own export is theirs: an administrator does not download it.

- **The user is told.** `data-export-made` says an export was prepared, with the reference you gave, and that it went to the team that handles these requests, not to anyone else.
- **Unless you say why not.** Some inquiries must not warn the person. `--no-notify` needs `--reason`, which is kept in the audit record. Those exports are audited as `user.data_export_requested_silently`, apart from the ones the user was told about (`user.data_export_requested`), so they are easy to review.
- **An erased user** has nothing left to export (`409`).

## Audit

| Event | When | Details |
|---|---|---|
| `user.data_export_requested` | a user asked, or an administrator did and told them | `export`, `ticket` |
| `user.data_export_requested_silently` | an administrator did and did not | `export`, `ticket`, `reason` |
| `user.data_exported` | the package is ready | `export`, `size`, `by` |
| `user.data_export_failed` | it failed after its attempts | `export`, `error` |
| `user.data_export_downloaded` | a link was given, or the bytes were sent | `export`, `by` |
| `user.data_export_expired` | the package was deleted at the end of its retention | `export` |

## Emails

`data-export-ready` (`{{.Link}}`, `{{.ExpiresAt}}`) and `data-export-made` (`{{.Data.ticket}}`) are [built-in kinds](../../functions/email/): the English text is used for every language unless the realm's `email/` folder has the kind. `backd template realm` writes them to edit.

## What stays yours

- Who may ask for whom, and checking that it is them: the self-service route proves it is the account holder; a request from a third party is for you to verify before you press the button.
- The deadline to answer, and what you tell the person.
- That collections mixing several people's data are narrowed with `fields`: a document is exported whole to the user it belongs to, so read `backd compliance scope` and decide, collection by collection.
- The data `backd` can't see: what your functions sent to other services.
