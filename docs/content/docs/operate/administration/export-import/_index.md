---
title: "Exporting and importing"
description: "Move a realm, a database or some collections between instances as a package, seed staging with anonymized data, or extract a report."
icon: "export"
weight: 568
toc: true
---

`backd export` writes the documents and files of a realm to a **package**: a plain directory (or one `.tar.gz`) you can read with ordinary tools. `backd import` reads one into a realm, after checking all of it against the target's own configuration. Together they are the supported way to take data out of `backd`, move it between instances, seed a staging copy or hand someone a report, without `mongodump`. They go through the [admin API](../admin/), so they need no access to MongoDB, or run [directly on MongoDB](direct-mode/) when no server is available.

| I want to… | Read |
|---|---|
| Move an app, with its users, from one instance to another | [Moving an app between instances](moving-an-app/) |
| Fill a staging instance with realistic data that holds no real person's details | [Seeding staging](seeding-staging/) |
| Give an analyst a table of some fields | [Reporting extracts and CSV](reporting-extracts/) |
| Export or import with the server stopped, or read a backup | [Direct mode](direct-mode/) |
| Look up the package format, every flag, the admin API or an error | [Reference](reference/) |
| Keep packages and keys safe | [Keeping packages safe](keeping-packages-safe/) |

## Export or backup?

An export is for moving data, not for recovering a deployment: it leaves out the system data a restore needs (sessions, API keys, secrets, the audit trail) and reads the collections one after another, not as one snapshot. For disaster recovery use [Backup and restore](../../backup/); to read a backup as a package, restore it into a throwaway MongoDB and use [direct mode](direct-mode/#reading-an-old-backup).

## At a glance

```
backd export --realm clinic --include auth --out ./clinic-2026-10-09 --archive
backd import --realm clinic --from ./clinic-2026-10-09.tar.gz --dry-run
backd import --realm clinic --from ./clinic-2026-10-09.tar.gz
```

- **Lossless.** A package keeps ids, `_meta` (creation and update times, version, owner, trash state) and files, so references and file links stay valid after an import.
- **Validated before anything is written.** An import checks the package's checksums, then every document against the target realm's schemas, file limits, indexes and existing documents; `--dry-run` stops there.
- **Resumable.** Running a command again after an interruption finishes the job.
- **Audited.** Every export and import is recorded (`export`, `import` and their ends), with counts and never content.
- **Authentication data only when asked.** Users, password hashes, second factors and push devices travel only with `--include auth`; secrets, sessions and tokens never do.
- **Needs full admin access** and, for a signed-in session, a fresh proof of identity. With an admin API key nothing more is asked.

Encrypted fields in packages are planned together with field encryption and are not available yet.
