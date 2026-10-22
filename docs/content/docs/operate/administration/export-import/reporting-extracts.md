---
title: "Reporting extracts and CSV"
description: "Export some fields of a collection, filtered, as a CSV table for a spreadsheet: what it keeps, what it flattens and why it can never be imported."
icon: "chart-line"
weight: 30
toc: true
---

To hand an analyst a table, not a package, narrow the export and ask for CSV:

```
backd export --realm clinic --db main --collection patients \
  --fields name,birth_year,address --where '{"status": "active"}' \
  --format csv --out ./active-patients
```

- **`--fields`** keeps only those paths of every document (`id` is always there; `_meta.owner` and the other `_meta` fields can be asked for, and are left out otherwise). A name that is an object, like `address`, keeps all of it. A path that exists in none of the exported collections is refused. A package made with `--fields` is marked **partial** in its manifest.
- **`--where`** takes the same JSON filter as the data API's [`where`](../../../../build/api/querying/#where) and needs `--db` and exactly one `--collection`. It is checked against the collection's schema before anything is read; documents in the trash match like any other.
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
