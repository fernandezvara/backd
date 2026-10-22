---
title: "Exporting and importing a realm"
description: "backd export and import: move a realm, a database or some collections, with their files, between instances as a package, or extract a report."
icon: "export"
weight: 568
toc: true
---

`backd export` writes the documents and files of a realm to a **package**: a plain directory (or one `.tar.gz`) you can read with ordinary tools. It is the supported way to take data out of `backd` without `mongodump`: it goes through the [admin API](../admin/), so it needs no access to MongoDB, and it writes documents the way the API shows them.

{{< hint style="note" title="Planned" >}}
Direct mode against MongoDB, anonymized exports and encrypted fields are planned for the same feature. This page covers what exists now: exporting a package or a report, importing a package, and moving users with `--include auth`.
{{< /hint >}}

## Exporting

```
backd export --realm clinic --out ./clinic-2026-10-09
backd export --realm clinic --out ./clinic-2026-10-09 --archive     # ./clinic-2026-10-09.tar.gz
backd export --realm clinic --db main --collection patients,visits --out ./clinic-main --no-files
```

| Flag | Effect |
|---|---|
| `--out` | The package directory to write (required). With `--archive` it ends as `<out>.tar.gz`, readable only by you, and the directory is removed |
| `--db` | Only this database of the realm |
| `--collection` | Only these collections of `--db`, comma-separated |
| `--no-files` | The documents only: leave the files out |
| `--fields` | A [reporting extract](#reporting-extracts-and-csv): only these fields |
| `--where` | Only the documents matching a filter, for one `--collection` |
| `--format` | `jsonl` (a package, the default) or `csv` |
| `--include auth` | Also export the realm's [authentication data](#moving-users) |
| `--auth-key-out` | With `--include auth`: write the export key to this file instead of showing it |

It needs **full admin access** and, when you are signed in with a session, a fresh proof that it is you (the same one [creating an API key](../admin/) asks for). With an admin API key in `BACKD_API_KEY` nothing more is asked. Both start and end of every export are [audited](../audit/) (`export`, `export.done`) with the scope and the counts, never the content.

## What is in a package

```
clinic-2026-10-09/
  manifest.json
  main/patients.jsonl
  main/visits.jsonl
  main/_files/<file id>
  _system/data_keys.jsonl
```

- **`manifest.json`**: the package format version and the `backd` version, the realm, when it was made and by whom, the config fingerprint of the instance, the databases and collections with their document counts, the number of files, and a **SHA-256 checksum of every other file**.
- **`<database>/<collection>.jsonl`**: one document per line, in the API representation (`id`, the fields and `_meta`), documents in the trash included. Dates are written as `{"$date": "…"}` and binary values as `{"$binary": {"base64": "…", "subType": "00"}}` (MongoDB's relaxed Extended JSON), so nothing is lost.
- **`<database>/_files/<file id>`**: the object of every file the documents reference. Image versions are not exported: they can be made again.
- **`_system/data_keys.jsonl`**: the wrapped keys of encrypted fields. It is empty until a realm has encrypted fields.

Never exported: sessions, tokens and codes, counters, jobs and their history, function logs, push send records, secrets and the audit trail.

## Reporting extracts and CSV

To hand an analyst a table, not a package, narrow the export and ask for CSV:

```
backd export --realm clinic --db main --collection patients \
  --fields name,birth_year,address --where '{"status": "active"}' \
  --format csv --out ./active-patients
```

- **`--fields`** keeps only those paths of every document (`id` is always there; `_meta.owner` and the other `_meta` fields can be asked for, and are left out otherwise). A name that is an object, like `address`, keeps all of it. A path that exists in none of the exported collections is refused. A package made with `--fields` is marked **partial** in its manifest.
- **`--where`** takes the same JSON filter as the data API's [`where`](../../../build/api/querying/#where) and needs `--db` and exactly one `--collection`. It is checked against the collection's schema before anything is read; documents in the trash match like any other.
- **`--format csv`** writes `<database>/<collection>.csv`, one RFC 4180 file per collection, and a manifest with their checksums. No system data and no files go in it.

The CSV is a **flat view and loses information**, so it can never be imported:

| In the document | In the CSV |
|---|---|
| nested object | one `a.b` column per leaf; an empty object is `{}` |
| array | JSON text in one cell |
| `_meta` | `_meta.owner`, `_meta.created_at`… columns, last |
| date | RFC 3339 text |
| binary, ciphertext | base64 text |
| `null` or missing | an empty cell |
| text starting with `=`, `+`, `-` or `@` | an apostrophe is put in front, so a spreadsheet shows it instead of running it as a formula |

Columns are the union over all documents: `id` first, then the fields in the order you gave (or alphabetically), then `_meta`. The documents are held in a temporary file next to the output while the columns are worked out.

## Moving users

A package holds documents only: the users who own them stay behind unless you ask. `--include auth` adds, for the **whole realm** (not with `--db`, `--collection`, `--fields` or CSV), `_system/auth/<collection>.jsonl` for:

| Collection | What it holds |
|---|---|
| `users`, `identities` | Accounts and their sign-in methods, **password hashes included** |
| `orgs`, `org_members`, `org_teams` | Organizations, memberships (with their roles) and teams |
| `mfa` | Second-factor enrollments (authenticator apps) |
| `devices` | Push notification devices |

Never included: sessions, login codes, challenges, email and invitation tokens, API keys, secrets and the audit trail. Users are signed out of the old instance's sessions, not carried: they sign in again.

Some of those fields are sealed with the **realm's secrets key** on the instance they come from: the authenticator secrets, the device tokens and Apple refresh tokens. That key never leaves an instance, so the server opens them and seals them again under a **one-time export key** it makes for this export and shows to you **once**:

```
backd export --realm clinic --include auth --out ./clinic-auth --auth-key-out ./clinic.key
backd import --realm clinic2 --from ./clinic-auth --auth-key file:./clinic.key
```

- Without `--auth-key-out` the key is printed on the terminal, once; `backd` keeps no copy. The manifest records only its fingerprint, which `import` checks the key against.
- On import, `--auth-key` takes `key:<value>`, `env:<VAR>` or `file:<path>` (a file readable by others draws a warning; the value form ends up in shell history and process lists, so prefer the others). The key is used **only on the machine running the import**: it opens the sealed fields there and sends them over the authenticated connection to be sealed under the target realm's own key.
- **Without the key**, users, identities, organizations, memberships and teams are imported, but without second factors, push devices and Apple tokens. The command says so; users with required second factors enroll again at their next sign-in.
- **Recovery codes don't travel.** They are keyed hashes under the realm's master key and can't be re-keyed: users with an authenticator keep it and generate new recovery codes.
- Authentication data is fetched whole on every run, even when resuming, because the export key belongs to one run. Import it first: it is sent before the documents, so the owners they name exist when they are checked; documents owned by users that are in the package are not reported as missing.
- Importing uses the same modes as documents (`create`, `skip`, `replace`) and the same idempotence. The store checks each document as it is — the id, unique values such as emails and provider identities, the shape its collection keeps — and a repeated email is a conflict.

{{< hint style="warning" title="The package then holds credentials" >}}
Password hashes let anyone with the package try passwords offline, and the export key opens the authenticator secrets. Keep the package and the key apart, in different places, and delete both when the move is done. Both actions are audited (`export.auth`, `import`).
{{< /hint >}}

## Big realms and interruptions

The server reads each collection in pages and streams it, so memory does not grow with the size of the data. Only **one export of a realm runs at a time** (`409 export_running`); an export that goes silent for ten minutes loses its place.

Every part is checked as it arrives and listed in `<out>/.export-progress.json`. If the command stops, **run the same command again**: parts already complete are kept (they are checked for their size) and the export continues from the next. A part that was in flight starts again from its beginning.

{{< hint style="warning" title="A package holds your data" >}}
Treat a package like the database it came from: keep it where only the people who may read the realm can, do not commit it, and delete it when you are done. It is written with owner-only permissions.
{{< /hint >}}

## Importing

```
backd import --realm clinic --from ./clinic-2026-10-09 --dry-run
backd import --realm clinic --from ./clinic-2026-10-09
backd import --realm clinic --from ./clinic-2026-10-09.tar.gz --mode replace
```

`--from` is a package directory or the `.tar.gz` that `--archive` wrote. Like the export, it needs full admin access and a fresh proof of identity for sessions, and it is [audited](../audit/) (`import`, `import.done`). The import goes in this order, and **nothing is written until all of it is known**:

1. **The package is verified.** The manifest's format must be one this `backd` reads, and the SHA-256 of every file it lists must match. A package made with `--fields` (partial) and a CSV extract are refused.
2. **Everything is validated against the target realm's own configuration**, which may differ from the source's: each collection must exist there, and every document is checked as the API would check one a client sent: its id (the collection's `custom_id` rules, or a generated id), its fields against `schema.json`, its files against the file fields (and their size limits), and its unique indexes against what the target holds. The result is a report: counts per outcome, and the first invalid or conflicting documents with their line and reasons. **`--dry-run` stops here.**
3. **The files are uploaded**, through the target's storage, before the documents that reference them.
4. **The documents are written** with their **ids and `_meta` kept** (creation and update times, version, owner, trash state), so references between documents, file links and ownership stay valid. Files are counted in the realm's storage usage and the image versions their fields declare are queued for the workers: versions are not in the package.

| `--mode` | A document whose id exists in the target |
|---|---|
| `create` (the default) | The same document is left alone; a **different** one is a conflict and nothing is written |
| `skip` | The existing document is kept |
| `replace` | The existing document is overwritten, and files it no longer holds are queued for deletion |

Invalid documents stop the import. `--skip-invalid` leaves them out instead and writes them, with their reasons, to `--report` (`import-report.json` by default). A conflict always stops it.

**An interrupted import is finished by running the same command again**: a document already written is found identical and left alone, whatever the mode, and files are uploaded again with the same bytes. Documents are sent in chunks, so memory doesn't grow with the package.

{{< hint style="warning" title="The import writes like any other writer" >}}
Triggers and realtime subscriptions see imported documents as ordinary creations and updates. Documents of users and organizations the target doesn't have are imported and counted in the report: their owners can't sign in there until you bring the users across. The config fingerprint of the package is compared with the instance's and a difference is a warning, never a refusal: validation decides what is accepted.
{{< /hint >}}

## Not a backup

Collections are read one after another while the app keeps working, so a package is not a snapshot of one instant, and it leaves out the system data a restore needs. For backups see [Backup and restore](../../backup/).

## The admin API

`backd export` is a client of `/v1/{realm}/_admin/export`:

| Request | Answer |
|---|---|
| `GET /export?database=&collection=&files=false&fields=&where=&format=&include=` | `200` with the plan: a `lease`, the manifest as far as it can be known (counts are estimates), and the file fields of each collection. `409 export_running` while another export holds the realm. With `include=auth` the answer also has the one-time `export_key`. A previous `lease` in `X-Backd-Export` takes over an interrupted export of the same administrator |
| `GET /export?part=<database>/<collection>.jsonl` | The collection's documents as `application/x-ndjson`, with the lease in `X-Backd-Export` |
| `GET /export?file=<database>/<collection>/<file id>` | The file's object, through `backd`'s storage client |
| `DELETE /export?complete=true` | `204`: the export ended; releases the realm and audits what was streamed |

All of them need full admin access; the plan needs the re-authentication header for sessions. `409 export_lease` means the lease is missing, expired or another administrator's.

`backd import` is a client of `/v1/{realm}/_admin/import`; the [OpenAPI document](../../../build/api/openapi/) has the details:

| Request | Answer |
|---|---|
| `POST /import` `{manifest, mode?, skip_invalid?, dry_run?}` | `200` with a `lease`, the size of the chunks to send and warnings. `422 import_refused` when the package can't be read or names collections the realm lacks. One export or import of a realm runs at a time (`409 export_running`) |
| `POST /import?phase=validate\|write&part=<database>/<collection>.jsonl` | The body is whole lines of that part. `200` with the counts by outcome (`created`, `replaced`, `skipped`, `unchanged`, `conflict`, `invalid`), the first problems with their line, and how many documents belong to users or organizations the target lacks. `write` is refused on a dry run |
| `PUT /import?file=<database>/<collection>/<file id>` | The file's bytes, checked against `X-Backd-Sha256`; a file that arrives different is deleted |
| `DELETE /import?complete=true` | `204`: releases the realm and audits the counts |
