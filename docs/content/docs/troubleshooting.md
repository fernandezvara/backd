---
title: "Troubleshooting"
description: "The messages you are most likely to meet when starting out with backd, what they mean, and what to do."
icon: "lifebuoy"
weight: 600
toc: true
---

The first place to look is always the log of the service that refused: `docker compose logs backd` (or `make dev-logs`). A bad configuration file stops startup and the log names the file; every request is logged with a `request_id`, which also appears in the body of every error.

## The configuration is refused

`backd config check` (and startup) print every problem at once, each starting with the path of the file or folder. Run it before you start the service: `CONFIG_DIR=./config backd config check`.

| Message | What it means |
|---|---|
| `shop/email: this folder is not a database: the realm's email folder now lives in _realm, so move it to shop/_realm/email` | The realm is laid out the old way. `email`, `pages`, `push`, `consents`, `compliance` and `app-config` (now `_realm/config`) belong in `_realm/`. See [Data model](../build/configuration/config-dir/). |
| `shop/main/orders: collection directory has no schema.json` | Every folder of a database is a collection and needs a `schema.json`. Either add one, or the folder is something else that should not be there. |
| `shop/_other: names starting with "_" are reserved for backd, and a realm has only _realm` | A realm has only `_realm`, a database only `_functions`. Rename the folder or remove it. |
| `…/collection.yaml: invalid YAML: … field rulez not found …` | A key backd does not know, usually a typo. The keys of `collection.yaml` are listed in [Data model](../build/configuration/config-dir/) and [Access rules](../build/auth/rules/). |
| `…/collection.yaml: rules only apply when the realm has auth enabled (realm.yaml has `auth: disabled`)` | Rules and `invoke:` mean nothing in a realm without authentication, so they are refused rather than ignored. Enable auth in `realm.yaml`, or remove them. |

{{< hint note >}}
These docs describe the current `main`. If a released image refuses a configuration that `backd config check` from this checkout accepts, the image is older than the documentation: use the matching version, or build the image from the checkout (see [Local development](../contribute/local-development/)).
{{< /hint >}}

## The stack does not start

- **`EXECUTOR_TOKEN required - see README.md (Develop locally)`** (or `CALLBACK_KEY`, `EGRESS_KEY`): Compose needs the three secrets in a `.env` file next to `compose.yaml`. The [Quickstart](../build/quickstart/#3-run-it) shows how to write it.
- **`/readyz` refuses the connection or answers `503`**: for a minute after `up`, MongoDB is still becoming a replica set. Repeat it every few seconds, and look at `docker compose ps` for the container that is down.
- **A port is taken**: change the left side of the port mapping in `compose.yaml` (`8080`, and `9000` for MinIO) and use that port in every URL.

## Commands ask for more than you gave

- **`this account must use two-step verification: set up an authenticator app`**: the template's `admin` role requires a second factor, so the first `backd login` enrolls an authenticator. To skip it while developing, empty `mfa.required_roles` in `realm.yaml`.
- **`no password on standard input`**: some commands (creating an API key, for one) ask for your password again as a fresh proof. In a terminal they prompt; in a script, pipe the password to the command's standard input.

## What an HTTP error means

An error body is `{"error": {"code", "message", "details"}}`, and the status says what kind:

| Status | Meaning |
|---|---|
| `400` | Invalid input; the failing fields are in `details`. |
| `401` | Not signed in, or the session or key ended. Sign in again. |
| `403` | Signed in, but a rule or an `invoke` expression refuses it. |
| `404` | The thing does not exist, **or you may not read it**: backd does not tell the two apart. |
| `409` | A unique index or an already-used value. |
| `412` | The document changed since you read it (`If-Match` did not match). |
| `503` | A function call while the executor is still starting. |

## A function change does nothing

Functions are bundled before they run. In dev mode saving the file rebuilds the bundle; otherwise run `backd functions build` (or `docker compose run --rm functions-build` in the tutorial stack) and restart `backd`. A TypeScript error stops the build and names the line. `console.log` lines and a failed call's error are in the log of `backd`.

## Start over

`docker compose down -v` deletes the stack's data and containers; `up` creates them again, empty. In the development stack of this repository, `make dev-reset` does the same and leaves your `config/` alone.
