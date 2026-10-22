---
title: "Exporting a realm"
description: "backd export: write a realm, a database or some collections, with their files, as a package you can keep, inspect or move."
icon: "export"
weight: 568
toc: true
---

`backd export` writes the documents and files of a realm to a **package**: a plain directory (or one `.tar.gz`) you can read with ordinary tools. It is the supported way to take data out of `backd` without `mongodump`: it goes through the [admin API](../admin/), so it needs no access to MongoDB, and it writes documents the way the API shows them.

{{< hint style="note" title="Planned" >}}
Importing a package, authentication data, direct mode against MongoDB and anonymized exports are planned for the same feature. This page covers what exists now: the export.
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

## Big realms and interruptions

The server reads each collection in pages and streams it, so memory does not grow with the size of the data. Only **one export of a realm runs at a time** (`409 export_running`); an export that goes silent for ten minutes loses its place.

Every part is checked as it arrives and listed in `<out>/.export-progress.json`. If the command stops, **run the same command again**: parts already complete are kept (they are checked for their size) and the export continues from the next. A part that was in flight starts again from its beginning.

{{< hint style="warning" title="A package holds your data" >}}
Treat a package like the database it came from: keep it where only the people who may read the realm can, do not commit it, and delete it when you are done. It is written with owner-only permissions.
{{< /hint >}}

## Not a backup

Collections are read one after another while the app keeps working, so a package is not a snapshot of one instant, and it leaves out the system data a restore needs. For backups see [Backup and restore](../../backup/).

## The admin API

`backd export` is a client of `GET /v1/{realm}/_admin/export`:

| Request | Answer |
|---|---|
| `GET /export?database=&collection=&files=false&fields=&where=&format=` | `200` with the plan: a `lease`, the manifest as far as it can be known (counts are estimates), and the file fields of each collection. `409 export_running` while another export holds the realm. A previous `lease` in `X-Backd-Export` takes over an interrupted export of the same administrator |
| `GET /export?part=<database>/<collection>.jsonl` | The collection's documents as `application/x-ndjson`, with the lease in `X-Backd-Export` |
| `GET /export?file=<database>/<collection>/<file id>` | The file's object, through `backd`'s storage client |
| `DELETE /export?complete=true` | `204`: the export ended; releases the realm and audits what was streamed |

All of them need full admin access; the plan needs the re-authentication header for sessions. `409 export_lease` means the lease is missing, expired or another administrator's.
