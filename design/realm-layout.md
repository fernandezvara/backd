# Design: one place for what a realm shows and configures (`_realm/`)

- **Issues:** 32.1–32.5 (milestone `32-realm-layout`)
- **Status:** decided
- **Related:** [app-config.md](./app-config.md), [email-delivery.md](./email-delivery.md), [push-notifications.md](./push-notifications.md), [compliance.md](./compliance.md)

## 1. The problem

A realm is a directory. Inside it, every folder is a database, except the ones backd reserves by name. The list grew with each feature and is now six names, written in Go (`registry.ReservedDirs`):

```
<realm>/email  pages  push  consents  compliance  app-config   ← not databases
<realm>/<database>/                                           ← databases
```

Consequences:

- **Easy to get wrong.** A new feature that adds a folder must remember to add it to the list. `compliance/` was missing from it in the first dev stack run against an older binary, and the error was "collection directory has no schema.json", which says nothing about what happened. The docs page listing the reserved folders was missing it too.
- **A reserved name can never be a database**, in any realm, with no way to see that from the tree.
- **The tree does not say which folders are data and which are configuration.** `app-config` is a schema and rules, `email` is text, `main` is data, and they all sit side by side.
- **Nothing is left for the next feature.** Each one claims another top-level name.

The convention that already works is the underscore: `_functions` inside a database means "backd's, not a collection", and a name that starts with `_` is not a valid database, collection or realm name.

## 2. The layout

```
<realm>/
├── realm.yaml                  required, stays here: it marks the folder as a realm
├── _realm/                     everything else the realm shows and configures
│   ├── email/<kind>/<locale>.html | .txt | .subject.txt
│   ├── pages/<page>/<locale>.html
│   ├── push/<kind>/<locale>.yaml
│   ├── consents/<kind>/<version>/<locale>.md
│   ├── compliance/readme/<locale>.html
│   └── config/                 the app configuration: schema.json, rules.yaml
└── <database>/
    ├── _functions/
    └── <collection>/           schema.json, indexes.json, collection.yaml, rules.test.yaml
```

The inner layout of each folder does not change; only where it lives does. `app-config` becomes `config` inside `_realm/`, which is also the name of its API (`GET /v1/{realm}/_config`).

## 3. Decisions

| # | Decision |
|---|---|
| 1 | **`_realm/`** holds the realm's own content and configuration, other than `realm.yaml`. `realm.yaml` stays at the realm root: it is what makes a folder a realm, and every tool and habit looks for it there. |
| 2 | **Any other name starting with `_` is an error**, at every level. At a realm: only `_realm` is defined. At a database: only `_functions`. `_functions` at a realm and `_realm` in a database are errors too. A reserved name that is not used wastes the namespace, and an unknown one is most likely a typo or a leftover. The message names the folder and what is allowed there. |
| 3 | **Everything without a leading `_` is a database.** The reserved list in Go goes away; a database name is only checked by the name rule. |
| 4 | **Unknown folders inside `_realm/` are an error** that lists the valid ones. `_realm/emial/` must not silently drop templates. Files at that level are ignored, as elsewhere. |
| 5 | **The six old top-level names get their own error.** A realm that still has `email/`, `pages/`, `push/`, `consents/`, `compliance/` or `app-config/` at the top is told "move `email/` to `_realm/email/`" (`app-config` to `_realm/config`), instead of being read as a database and failing with "no schema.json". The check is only there to explain, and it is the only trace of the old layout. |
| 6 | **No compatibility, no migration command.** There are no deployments to migrate: the old layout stops loading, with the error of decision 5. This is a breaking change and the changelog says so. |
| 7 | **The loaders are given `_realm/` as their base**, so each feature keeps its own subfolder constant and its own file naming. Nothing else about templates, locales, consents versions or app configuration changes. |
| 8 | **Dev mode follows `_realm/`** the same way it follows the rest of the configuration (the fingerprint covers it), and so does the stack's live reload. |

## 4. What changes in the code

- `internal/registry`: `Load` reads `_realm/`; `ReservedDirs` is replaced by the underscore rule (decisions 2, 3, 4, 5); constants for the subfolders are relative to `_realm`; `fingerprint.go` walks the new paths; error messages that mention a path (consents, app config, push, export readme) use the new one.
- `internal/email`, `internal/push`, `internal/exportreadme`: their `DirName`-style constants stay names *inside* `_realm`; callers pass the new base.
- `internal/templates` (`backd template ...`): writes into `_realm/...`; the database-name check uses the underscore rule instead of the reserved list. `backd template project` writes the new layout.
- `cmd/backd`: messages and help that name the folders; `backd config check` shows the new errors.
- `internal/typegen`, `internal/openapigen`, the admin API and the admin UI do not read these folders, but are checked.

## 5. What changes around the code

- Every example realm in `examples/config` that has these folders (`shelf`, `expenses`, `expenses-with-functions`, `workshop`) is moved with `git mv`, and so is anything that fixes their paths: the tutorial test that compares the chapters with `examples/config/shelf`, scripts, the compose files and the JS and Dart examples that quote them.
- The Shelf tutorial's chapters (email, consents, app configuration, your data, and the ones that mention the folders in passing), the configuration pages (`config-dir.md`, `realm.md`, `app-config.md`), the email, push, consents, compliance and erasure pages, the CLI reference, the quickstart, and the contribution pages are updated in the same step as the code that makes them true.
- The starter `backd template project` and its README.

## 6. Not in this design

- Moving `realm.yaml` into `_realm/`.
- A per-database `_realm`-like folder.
- Changing what any of the folders contain, or the app configuration API.
- Renaming the `backd app-config` command (it names the feature, not the folder).

## 7. Steps (milestone `32-realm-layout`)

Every step leaves the repository building with its tests passing, so what must change together changes together: the loader cannot move without the example realms, the fixtures and the generators that the tests load.

| Id | Step |
|---|---|
| 32.1 | This design, committed with the first change. |
| 32.2 | The core: the loader reads `_realm/`, the underscore rule and the errors for unknown and old names, the fingerprint; `backd template` (every subcommand, including `project`), `backd config check` and the messages; every Go test and fixture that builds a realm directory; the example realms moved with `git mv`. Docs of the features that own a folder (`config-dir.md`, `realm.md`, `app-config.md`, email, push, consents, compliance, erasure) in the same commit. |
| 32.3 | The Shelf tutorial: the chapters that show the folders, the test that compares them with `examples/config/shelf`, and the tour scripts and clients that name the paths. |
| 32.4 | Everything that runs the examples: scripts, compose files, the dev and example stacks, CI workflows, the starter project's README, the CLI reference and the changelog. |
| 32.5 | Review: each stack (`make dev-examples`, `make example`, the tutorial stack) runs from the new layout; the error of the old layout is read once as a user would; `SECURITY.md` and the security page checked for folder names. |
