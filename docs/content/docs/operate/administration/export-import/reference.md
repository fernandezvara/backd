---
title: "Reference"
description: "The export package format and manifest, every flag of backd export and import, the admin API endpoints and what to do about each error."
icon: "book-open"
weight: 60
toc: true
---

Everything about the commands, in one place: the package format, every flag, the admin API and what to do about each error.

## The package

A package is a directory, or one `.tar.gz` of it (`--archive`). All paths are relative, with no `..`, and the whole is verified against `manifest.json` before an import uses any of it.

```
clinic-2026-10-09/
  manifest.json
  main/patients.jsonl              one file per collection
  main/visits.jsonl
  main/_files/<file id>            the object of every file the documents reference (private and public)
  _system/data_keys.jsonl          wrapped keys of encrypted fields (empty without any)
  _system/auth/users.jsonl         only with --include auth
  _system/auth/identities.jsonl    … orgs, org_members, org_teams, org_connections, mfa, devices
```

### manifest.json

```json
{
  "format": 1,
  "backd_version": "v0.10.0",
  "realm": "clinic",
  "created_at": "2026-10-09T10:15:00Z",
  "operator": "key:svc",
  "config_fingerprint": "9f2c…",
  "flags": {"partial": false, "anonymized": false, "auth": true, "no_files": false, "csv": false},
  "databases": [
    {"name": "main", "collections": [
      {"name": "patients", "documents": 1204, "part": "main/patients.jsonl"}
    ]}
  ],
  "encrypted_fields": [{"database": "main", "collection": "patients", "path": "notes", "key": "HEALTH", "fingerprint": "9f2c41d0", "form": "ciphertext"}],
  "auth": [{"name": "users", "documents": 31, "part": "_system/auth/users.jsonl"}],
  "auth_key_fingerprint": "6f42f0e47d8b8cc9",
  "files": 87,
  "checksums": {"main/patients.jsonl": "sha256:ba78…", "main/_files/fl_0123456789abcdefghij": "sha256:2c26…"}
}
```

| Field | Meaning |
|---|---|
| `format` | The package layout version. An import refuses a format it doesn't know |
| `backd_version`, `realm`, `created_at`, `operator` | Where and by whom it was made; `operator` is `cli:offline` for a [direct](../direct-mode/) export |
| `config_fingerprint` | Of the configuration it was made under; a different one on import is a warning |
| `flags.partial` | Made with `--fields`: an import refuses it |
| `flags.csv` | A [CSV extract](../reporting-extracts/): an import refuses it |
| `flags.anonymized` | A [staging copy](../seeding-staging/); an import compares it with `BACKD_ENV` |
| `flags.auth` | Holds authentication data (the `auth` parts) |
| `flags.no_files` | Made without files (`--no-files`, an anonymized or CSV export) |
| `databases[].collections[]` | Each collection's document count and the package path of its documents |
| `encrypted_fields[]` | Each [encrypted field](../encrypted-fields/): its `database`, `collection`, `path`, the field key's `key` name and `fingerprint`, and `form` (`ciphertext`, or `plaintext` once someone opened it with the key) |
| `auth`, `auth_key_fingerprint` | The authentication data parts, and the fingerprint of the one-time export key their sealed fields are sealed under |
| `checksums` | The SHA-256 of **every** other file, as `sha256:<hex>` |

### Documents

One document per line, in the API representation: `id`, the fields and `_meta`, soft-deleted documents included. Typed values use MongoDB's relaxed Extended JSON, so nothing is lost:

| Value | Written as |
|---|---|
| a date | `{"$date": "2026-10-09T10:15:00.000Z"}` |
| binary data | `{"$binary": {"base64": "…", "subType": "00"}}` |
| everything else | plain JSON |

`_expanded`, `_meta.redacted` and `_meta.score` never appear: an export is administrative, so read rules don't apply. Image versions of files are not exported; the target makes them again. Never exported at all: sessions, login codes and challenges, email and invitation tokens, API keys, counters, jobs and their history, function logs, push send records, secrets and the audit trail.

## Commands

Every value is a flag; there are no positional arguments. Exit code `0` is success, `1` a failure (the message says what), `2` a mistake in the flags.

### backd export

Common flags are `--realm` (required), `--url` (the server; default `BACKD_URL`, then the last one logged in to) and the [direct-mode flags](#direct-mode-flags).

| Flag | Effect |
|---|---|
| `--out` | The package directory to write (required). It must be new, empty, or hold an interrupted export of the same command |
| `--archive` | Write `<out>.tar.gz`, readable only by you, and remove the directory. Refuses to overwrite an archive |
| `--db`, `--collection` | Only this database, or these collections of it (comma-separated) |
| `--no-files` | The documents only |
| `--fields` | Keep only these field paths (`id` is always kept); marks the package partial |
| `--where` | Only the documents matching this JSON filter, for one `--collection` |
| `--format` | `jsonl` (default) or `csv` |
| `--include auth` | Add the authentication data (whole realm only) |
| `--auth-key-out` | Write the one-time export key to this file (mode `0600`) instead of printing it |
| `--anonymize` | Make a staging copy |
| `--anonymize-config` | YAML of per-collection `remove` and `replace` rules |
| `--staging-password` | One password for every user: `env:<VAR>`, `file:<path>` or `key:<value>` |
| `--anonymize-files keep` | Export files in an anonymized copy |
| `--field` | Keys to open [encrypted fields](../encrypted-fields/): `<path>=<key:\|env:\|file:…>` entries separated by `;` |

### backd import

| Flag | Effect |
|---|---|
| `--from` | The package: its directory or the `.tar.gz` (required) |
| `--mode` | `create` (default), `skip` or `replace` |
| `--dry-run` | Verify and validate; write nothing |
| `--skip-invalid` | Leave out the documents that are invalid for the target |
| `--report` | Write the problems found to this JSON file (default with `--skip-invalid`: `import-report.json`) |
| `--auth-key` | The export key of a package with authentication data: `env:<VAR>`, `file:<path>` or `key:<value>` |
| `--field` | Source keys for encrypted fields the target can't open itself, as in `export` |

### backd export-decrypt

Opens [encrypted fields](../encrypted-fields/) of a package, offline.

| Flag | Effect |
|---|---|
| `--from` | The package: its directory or `.tar.gz` (required) |
| `--out` | A new directory for the opened copy (required) |
| `--field` | The keys, as in `export` (required) |

### Direct-mode flags

On both commands; see [direct mode](../direct-mode/).

| Flag | Effect |
|---|---|
| `--direct` | Work on MongoDB and `CONFIG_DIR` |
| `--mongo-uri` | A URI, or `env:` / `file:`; default `BACKD_MONGO_URI`, then `MONGO_URI` |
| `--master-key` | `env:` / `file:`; default `BACKD_SECRETS_KEY` |
| `--storage-key`, `--storage-secret` | Storage credentials instead of the realm's secrets |
| `--force` | Import while an instance serves the realm |

## The admin API

`backd export` is a client of `/v1/{realm}/_admin/export`:

| Request | Answer |
|---|---|
| `GET /export?database=&collection=&files=false&fields=&where=&format=&include=`, or `POST /export` with the same query and a JSON body `{anonymize?, anonymize_files?, anonymize_config?, staging_password?}` | `200` with the plan: a `lease`, the manifest as far as it can be known (counts are estimates), and the file fields of each collection. `409 export_running` while another export holds the realm. With `include=auth` the answer also has the one-time `export_key`. A previous `lease` in `X-Backd-Export` takes over an interrupted export of the same administrator |
| `GET /export?part=<database>/<collection>.jsonl` | The collection's documents as `application/x-ndjson`, with the lease in `X-Backd-Export` |
| `GET /export?file=<database>/<collection>/<file id>` | The file's object, through `backd`'s storage client |
| `DELETE /export?complete=true` | `200`: the export ended; releases the realm, audits what was streamed and answers those totals, with how many second factors and devices could not be carried (`auth_unavailable`) |

All of them need full admin access; the plan needs the re-authentication header for sessions. `409 export_lease` means the lease is missing, expired or another administrator's.

`backd import` is a client of `/v1/{realm}/_admin/import`; the [OpenAPI document](../../../../build/api/openapi/) has the details:

| Request | Answer |
|---|---|
| `POST /import` `{manifest, mode?, skip_invalid?, dry_run?}` | `200` with a `lease`, the size of the chunks to send and warnings. `422 import_refused` when the package can't be read or names collections the realm lacks. One export or import of a realm runs at a time (`409 export_running`) |
| `POST /import?phase=validate\|write&part=<database>/<collection>.jsonl` | The body is whole lines of that part. `200` with the counts by outcome (`created`, `replaced`, `skipped`, `unchanged`, `conflict`, `invalid`), the first problems with their line, and how many documents belong to users or organizations the target lacks. `write` is refused on a dry run |
| `PUT /import?file=<database>/<collection>/<file id>` | The file's bytes, checked against `X-Backd-Sha256`; a file that arrives different is deleted |
| `DELETE /import?complete=true` | `204`: releases the realm and audits the counts |

## Errors, and what to do

| You see | It means | Do |
|---|---|---|
| `401 reauth_required` | A session needs a fresh proof of identity | The command asks for it; answer, or use an admin API key |
| `403` naming an admin right | The credential doesn't hold full admin access | Use an admin API key or a user whose roles open every area |
| `409 export_running` | An export or import of this realm is running (on this instance) | Wait for it; a silent one is released after ten minutes |
| `409 export_lease` | The lease is missing, expired, another administrator's, or the other kind (export or import) | Start again; an interrupted export resumes from its progress file |
| `422 import_refused` | The package can't be imported here: another format, partial, a CSV extract, collections the realm doesn't have, authentication data this store can't take, or [encrypted fields](../encrypted-fields/#importing) nobody here can open | Read the details; add the collections to the configuration, or export again without `--fields` / as a package |
| `400` on `fields`, `where`, `include`, `format`, `anonymize_config`, `staging_password` | An option makes no sense, or a rule is refused by a schema | The details name each problem |
| `400 checksum_mismatch` | A file arrived different from its checksum and was deleted | Run the import again |
| `404` for a collection or file | The part isn't in the realm, or the realm has no storage | Check the configuration |
| `503 storage_unavailable` | The realm's storage keys can't be read | Set the storage secrets, or in [direct mode](../direct-mode/) pass `--master-key` or `--storage-key` and `--storage-secret` |
| `the package is damaged or was changed after it was made` | A file is missing or its checksum differs | Take the package again from its source |
| `documents reference files the package doesn't hold` | The package was made without files, or lost some | Export again with files |
| `nothing was written: fix the problems above` | The validation found invalid documents or conflicts | Fix the configuration or data, use `--mode`, or `--skip-invalid` for invalid ones |
| `realm … is served by … running backd instance(s)` | A direct import while an instance serves the realm | Stop it, or `--force` if you know it isn't writing |
| `… is not the key this package was exported with` | The wrong `--auth-key` | Use the key printed by that export |
| `the key given for … has the fingerprint …, but … is encrypted with the key …` | A `--field` key is not the one the field was encrypted with | Use the key whose fingerprint the manifest records |
| `--field … matches no encrypted field of the package` | A typo, or the field is already plaintext | Check `encrypted_fields` in the manifest |
