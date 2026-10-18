---
title: "Your realm's OpenAPI"
description: "backd openapi and /_openapi.json: an OpenAPI 3.1 document for one realm's client API, generated from its config, for typed clients in other languages."
icon: "brackets-curly"
weight: 340
toc: true
---

The [contract](../#openapi-specification) describes backd's generic API: documents are open objects there, because their fields come from your config. A realm's **generated document** is the same API with your config filled in: every collection with its schema, the sign-in methods the realm enables, its functions with their input and output. Generators of typed clients (openapi-generator, for Kotlin, Swift and many more) read it. Dart and Flutter apps use [package:backd](../../clients/dart/) instead — a hand-written client that doesn't come from a generator.

```sh
CONFIG_DIR=./config backd openapi --realm shop --out shop.openapi.json
curl -H "Authorization: Bearer $BACKD_API_KEY" https://api.example.com/v1/shop/_openapi.json
```

## What it covers

| In the document | Detail |
|---|---|
| Collections a client can reach | Auth `enabled`: at least one of `read`, `create`, `update` and `delete` isn't literally `false`; a collection with only `false` rules, and an operation whose rule is `false`, are left out. Auth `disabled`: every collection |
| Each collection | List, get, create, replace, patch and delete with the collection's schema as the document (plus `id` and `_meta`) and as the input (a collection with [`custom_id`](../../configuration/config-dir/#custom-ids) requires the `id`, with its pattern, in the create body and on the path), `where`, ordering, conditional reads, [soft delete](../../configuration/config-dir/#soft-delete) restore, and its [file fields](../../files/file-fields/) with their upload routes |
| Sign-in | Only the methods the realm enables: login and sessions always; sign-up when `signup: open`; email flows with `email:`; providers with `providers:`; MFA unless `mfa.enabled: false`; devices with `push:`; consents when declared |
| Functions | Every function that is not `internal`, with `input.schema.json` and `output.schema.json` as the call's request and response; async ones with their job route |
| Configuration | `_config`, with the values typed from `app-config/schema.json` |
| Batches, jobs, errors | The batch route of each database, the job route when a function is async, and the error codes |

Not in it: internal functions, the admin API (`/_admin/…`), rules, and system collections. The admin API has its own document, [below](#the-admin-document).

## Getting it

- **From the config:** `backd openapi --realm <realm>` prints it; `--out <file>` writes it. `--check` (with `--out`) exits non-zero when the file is missing or no longer matches what the config produces, for CI.
- **From a running instance:** `GET /v1/<realm>/_openapi.json`. Realm administrators (the `config` area) read it; `openapi.public: true` in [`realm.yaml`](../../configuration/realm/) makes it public, which is only the description of an API anyone can already read the error of, and a realm with `auth: disabled` serves it to everyone. It answers with an `ETag`, so `If-None-Match` costs a `304`.

{{< hint style="note" >}}
Public means anyone learns your collections' field names and your function names. Leave it admin-only unless a client library you publish needs it.
{{< /hint >}}

## Generating clients

**Typed clients for other platforms** come from the document with any generator that reads OpenAPI 3.1. For **Dart and Flutter**, generate nothing — [`package:backd`](../../clients/dart/) is a hand-written client (auth, sessions, files, jobs, realtime), typed with `backd types --lang dart`. For Kotlin, Swift and the rest, a generator:

```sh
docker run --rm -v "$PWD:/local" docker.io/openapitools/openapi-generator-cli generate \
  -i /local/shop.openapi.json -g kotlin -o /local/clients/kotlin     # or swift5
```

| Platform | Generator (`-g`) |
|---|---|
| Kotlin | `kotlin` |
| Swift | `swift5` |

Regenerate whenever the document changes: `backd openapi --realm <realm> --out <file> --check` in CI fails when the committed copy is stale, the same [drift check](../../clients/types/#keeping-the-types-current-drift) as for TypeScript types.

backd's own CI builds Dart, Kotlin and Swift clients from the Shelf and workshop realms' documents with openapi-generator, so the output stays usable by real generators (`make openapi-clients`).

### What generated clients don't do

A generated client calls the routes and types the documents; it doesn't do what the [JavaScript client](../../clients/js/) adds on top: the revalidation cache, retries, the sign-in flows that need PKCE and the browser's address bar, and file uploads in one call. Plan for sign-in with a provider and uploads to need a little hand-written code until hand-written clients exist for those platforms.

## The admin document

For internal tooling there is a second document, for the **admin API**: `backd openapi --realm <realm> --admin`, and `GET /v1/<realm>/_admin/openapi.json` for administrators with the `config` area (a read-only administrator reads it). It is never public, and a realm with `auth: disabled` has none.

| In the document | Detail |
|---|---|
| Administration | Users, invitations, API keys, secrets, audit, jobs, schedules, configuration and the routes of what the realm enables (push, storage, consents, app configuration) |
| Data past the rules | `/_admin/data/<database>/<collection>`, typed per collection with its schema, `_meta`, soft delete and files, whatever the collection's rules say |
| Functions | `/_admin/functions/<database>/<name>/invoke` for every function an administrator may run by hand, **internal ones included**, with `input.schema.json` and `output.schema.json`, and pause and resume for scheduled ones |

{{< hint style="warning" >}}
It describes routes that reach every document and every internal function: generate admin clients for your own tooling, never for an application your users run.
{{< /hint >}}
