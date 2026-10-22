---
title: "Command-line administration"
description: "Create a realm's first administrator with backd bootstrap, then manage users and API keys from the command line through the admin API."
icon: "terminal"
weight: 505
toc: true
---

The `backd` binary is also the command-line client for administering a realm. `backd user` and `backd apikey` don't touch MongoDB: they call the realm's [admin API](../admin/) on a running `backd`, as an administrator. They never need database credentials, and every change they make goes through the same checks as any other admin request, including [network restrictions](../../../build/configuration/realm/#network-restrictions).

Two commands write to MongoDB directly instead: `backd bootstrap`, which creates a realm's first administrator, and `backd secret rotate-key`, which re-encrypts every realm's [secrets](../../../build/functions/secrets/) under a new master key (see [Secrets](../admin/#secrets)).

## Conventions

- **Every value is a flag.** There are no positional arguments: `backd user create --realm blog --email ada@example.com`. `--realm` (and `--email`, `--name`, …) are required where the command needs them, and `--url` picks the server (below). `backd <command> --help`, or `backd <command> help`, lists a command's flags; `backd help` lists the commands.
- **Exit codes.** `0` on success (and after help); `1` when the command ran and failed (a server error, a wrong password, a missing `CONFIG_DIR`); `2` when it was invoked wrongly (an unknown command or flag, a missing or invalid flag value). Scripts can tell the two failures apart.
- **Output.** Help and results go to standard output; warnings, errors and prompts go to standard error, so `KEY=$(backd apikey create --realm blog --name ci)` captures only the key.

## Local and remote commands

| Command | Works on | Needs |
|---|---|---|
| `backd serve`, `backd provision` | MongoDB and `CONFIG_DIR` | `CONFIG_DIR`, `MONGO_URI` |
| `backd template`, `backd databases`, `backd rules test` | `CONFIG_DIR` | `CONFIG_DIR` |
| `backd types` | `CONFIG_DIR`, or `--from <url>` | `CONFIG_DIR`, or the running instance |
| `backd functions build\|types` | `CONFIG_DIR` | `CONFIG_DIR` (`build` also needs Deno) |
| `backd bootstrap --realm <realm> --email <email>` | MongoDB, once per realm | `CONFIG_DIR`, `MONGO_URI`, a provisioned realm |
| `backd login`, `backd logout`, `backd whoami`, `backd account …` | A running `backd` | Its URL |
| `backd user …`, `backd apikey …`, `backd data transfer\|assign-org`, `backd orgs list\|show\|create\|owned\|delete\|reactivate\|members\|add-member\|invite\|invitations\|revoke-invitation\|assign-owner\|claims-set`, `backd push devices\|remove-device\|sends\|test`, `backd audit …`, `backd secret set\|create\|list\|delete`, `backd functions invoke\|history\|logs\|jobs`, `backd triggers status\|gaps\|resolve`, `backd storage check\|usage\|reconcile`, `backd export`, `backd compliance scope`, `backd consents notify\|record`, `backd app-config get\|set\|reset\|history\|rollback` | A running `backd`, through the [admin API](../admin/) or the function's own route | Its URL, and a stored session or `BACKD_API_KEY` |
| `backd triggers rescan` | MongoDB, directly, like `backd provision`: it may read a whole collection | `CONFIG_DIR`, `MONGO_URI` |
| `backd secret rotate-key` | MongoDB, directly (not the admin API) | `CONFIG_DIR`, `MONGO_URI`, `BACKD_SECRETS_KEY`, `BACKD_SECRETS_NEW_KEY` |
| `backd version` | Nothing | Nothing |

## The first administrator

Administrators are users holding an **admin role**: a role with `admin: true` in [`realm.yaml`](../../../build/configuration/realm/#roles). The template (`backd template realm`) declares one, named `admin`:

```yaml
roles:
  admin:
    description: Manages users, invitations and API keys
    admin: true
```

Once the realm is provisioned, create its first administrator where `backd` runs, with the same `CONFIG_DIR` and `MONGO_URI`:

```sh
docker compose exec backd /backd bootstrap --realm blog --email ops@example.com
# Password:
# Repeat password:
# created ops@example.com (id d3c9ljp8hc2g00b6s1m0) with the admin role "admin" in realm blog
```

- If `realm.yaml` declares several roles with `admin: true`, choose one with `--role`. A role that opens only some [areas](../admin/#admin-rights) can't be the first administrator's: it can't manage the rest.
- After the first administrator, others are created by an administrator, through the API.

{{< hint note >}}
`bootstrap` writes to MongoDB directly, so it needs `CONFIG_DIR`, `MONGO_URI` and a provisioned realm, and it runs where `backd` runs. It refuses when a user already holds an admin role or the email is already registered, so it can't be used to take over an existing realm.
{{< /hint >}}
- The password follows the realm's [password policy](../../../build/auth/users/#password-policy). It is read from the terminal without echo, or as the first line of standard input.
- To keep the assignment if the realm is ever rebuilt from its config, also list the email under the role's `users` in `realm.yaml`. `bootstrap` reminds you when it isn't listed.

At startup, `backd` warns about each realm that declares no admin role, or whose admin roles nobody holds.

## Your own sign-in security

`backd account` manages the signed-in user's own [second factor](../../../build/auth/mfa/) with the session `backd login` stored (an API key is not a person). Each change asks for a fresh [proof](../../../build/auth/reauth/): the authenticator code if the user has one, else the password.

| Command | What it does |
|---|---|
| `backd account mfa-status` | Whether two-step verification is on, and the recovery codes left |
| `backd account mfa-enable` | Sets an authenticator app up (QR code, first code) and shows the recovery codes once |
| `backd account mfa-disable --yes` | Turns it off (refused for a role that requires it) |
| `backd account recovery-codes` | Replaces the recovery codes, shown once |

`backd apikey create` asks for the same proof when it runs with a session: an administrator's key outlives the session and bypasses MFA, so a person confirms it. With `BACKD_API_KEY` set there is nobody to ask.

## Logging in

```sh
backd login --realm blog --url https://api.example.com
# Email: ops@example.com
# Password:
# logged in to realm blog on https://api.example.com as ops@example.com (session expires at … at the latest)

backd whoami --realm blog
# ops@example.com (id d3c9ljp8hc2g00b6s1m0) on realm blog at https://api.example.com; roles: admin
```

`backd login` signs in through [`/_auth/login`](../../../build/auth/sessions/), like any user, and keeps the session for the following commands. It is an ordinary session: the realm's idle timeout, maximum lifetime, [login throttling](../../../build/auth/sessions/#brute-force-protection) and the user's `login_networks` all apply, and it shows up in the user's session list. `backd logout --realm blog` ends it on the server and forgets it.

- **The second step.** A user with a [second factor](../../../build/auth/mfa/) is asked for its code after the password (`Code from your authenticator app (or a recovery code)`); a user in a [required role](../../../build/auth/mfa/#required-by-role) who has none sets one up in the terminal: the command shows a QR code made of block characters, the `otpauth://` address and the secret to type in, asks for the first code, and shows the **recovery codes once** (on a terminal it waits until you have saved them). The code is read from the terminal or from the next line of standard input, after the password.
- **`--magic-link`.** `backd login --realm blog --email ops@example.com --magic-link` asks for a message instead of a password (the realm needs [`magic_link`](../../../build/auth/magic-links/)) and then for the 6-digit code in it.
- **Server:** `--url`, else the `BACKD_URL` environment variable, else the server of the last login.
- **Email:** `--email`, or a prompt. When standard input isn't a terminal, `--email` is required and the password is the first line of standard input.
- **Sessions are stored** per server and realm in `~/.config/backd/credentials` (or `$XDG_CONFIG_HOME/backd/credentials`, or the file named by `BACKD_CREDENTIALS`). The file is created readable only by you, in a directory only you can open. Anyone who can read it can act as you until the sessions expire, so log out on shared machines.
- **TLS:** `backd` verifies the server's certificate against the system's CAs. For a private CA, such as the local stack's, point `BACKD_CA_CERT` at its PEM file. Logging in over plain HTTP to anything but `localhost`, a loopback address, or a host name without dots (a container on a private network, such as `http://backd:8080`) prints a warning: the password would travel unencrypted.

## Using an API key instead

When `BACKD_API_KEY` is set, `backd user` and `backd apikey` send that key instead of a stored session. It must be an [API key](../../../build/auth/api-keys/) with the `admin` role. This suits automation that can't log in interactively:

```sh
BACKD_URL=https://api.example.com BACKD_API_KEY=$ADMIN_KEY backd user list --realm blog
```

## Errors

Commands print the API's error code and message, with a hint when there's something to do:

| Answer | Usual cause |
|---|---|
| `401` with a stored session | The session expired, was revoked (a password change revokes the others), or isn't accepted from this network: log in again |
| `403 forbidden` | The user has no admin role, or the key has the `data` role |
| `404 not_found` on admin commands | The realm doesn't exist or has auth disabled, or the admin API isn't reachable from this network (`admin.allowed_networks`, or a proxy rule such as the production reference's `ADMIN_ALLOW_FROM`) |

{{< hint warning >}}
Be careful when taking an admin role away from yourself: once no one holds an admin role, only `backd bootstrap` (on a realm without administrators) or an admin API key can manage the realm again.
{{< /hint >}}

## Every command

`backd help` lists the same commands in the same groups; `backd <command> --help` shows one command's flags.

**Develop** — for building with `backd` (see also the [quickstart](../../../build/quickstart/)):

| Command | Purpose |
|---|---|
| `backd template project\|realm\|database\|function\|email-capture` | Create config files under `CONFIG_DIR`: a whole project, or a realm, database, [function](../../../build/functions/) or dev email function inside it (see [Starting from a template](../../../build/configuration/config-dir/#starting-from-a-template)) |
| `backd config check`, `backd config fingerprint` | Validate `CONFIG_DIR` as startup would, or print its fingerprint (see [Deploying config](../../deploying/)) |
| `backd rules test` | Run each collection's `rules.test.yaml` against its `rules:`, without a database (see [Testing rules](../../../build/auth/rules/#testing-rules)) |
| `backd databases [--collections]` | Print the MongoDB databases (or collections) the config uses, one per line (needs only `CONFIG_DIR`; see [least-privilege deployments](../../#least-privilege-deployments)) |
| `backd types` | Generate a realm's TypeScript types (see [Typing and OpenAPI](../../../build/clients/types/)) |
| `backd openapi` | Generate a realm's OpenAPI 3.1 document (see [OpenAPI](../../../build/api/openapi/)) |
| `backd functions build\|invoke\|history\|logs\|jobs` | Bundle the functions with Deno, call one through a running `backd`, and see its recent calls, console output and jobs (see [Calling functions](../../../build/functions/calling/), [Async jobs](../../../build/functions/jobs/)) |

**Session:**

| Command | Purpose |
|---|---|
| `backd login --realm <realm>` | Sign in to a running `backd` as an administrator and keep the session (see [Logging in](#logging-in)) |
| `backd logout`, `backd whoami` | End the stored session, or show who it belongs to |
| `backd account <command>` | Manage your own [second factor](../../../build/auth/mfa/) and recovery codes (see [Your own sign-in security](#your-own-sign-in-security)) |

**Administer a realm:**

| Command | Purpose |
|---|---|
| `backd bootstrap --realm <realm> --email <email>` | Create a realm's first administrator (see [The first administrator](#the-first-administrator)) |
| `backd user <command>` | Manage a realm's users through the admin API (see [Users and passwords](../../../build/auth/users/)) |
| `backd orgs <command>` | Manage a realm's organizations, members, teams and claims (see [Organizations](../../../build/organizations/)) |
| `backd apikey <command>` | Manage a realm's API keys (see [API keys](../../../build/auth/api-keys/)) |
| `backd push devices\|sends\|test` | See a realm's push devices and send records, and send an audited test push (see [Records and test send](../../../build/push/records/)) |
| `backd secret set\|create\|list\|delete`, `backd secret rotate-key` | Manage [function secrets](../../../build/functions/secrets/) and [field keys](../../../build/configuration/field-keys/) (`create --type key --generate`, or `--value key:\|env:\|file:`); `rotate-key` re-encrypts them under a new master key, on MongoDB directly |
| `backd audit` | Print the realm's [audit trail](../audit/), newest first |
| `backd data check\|transfer\|assign-org` | Check stored documents against their schemas, move documents between collections, or assign them an organization |
| `backd storage check\|usage\|reconcile` | Check a realm's [object storage](../../providers/storage/), report its usage, or clean up orphans (see [Keeping storage tidy](../../storage-maintenance/)) |
| `backd export`, `backd import` | Write a realm, database or collections, with their files, as a [package](../export-import/), or read one into a realm |
| `backd files regenerate` | Rebuild [image versions](../../../build/files/file-fields/#image-versions) the documents already hold |
| `backd compliance scope` | What a user's [personal data export](../compliance/) covers |
| `backd consents notify\|record` | Manage the [consents](../../../build/auth/consents/) users accept |
| `backd app-config get\|set\|reset\|history\|rollback` | The realm's [app configuration](../../../build/configuration/app-config/): values, history, rollback |
| `backd triggers status\|gaps\|resolve`, `backd triggers rescan` | See and repair what the [trigger dispatcher](../../change-feed/) did; `rescan` writes to MongoDB directly |

**Server:**

| Command | Purpose |
|---|---|
| `backd serve [--with-worker]` | Load config, provision or verify MongoDB, serve HTTP; `--with-worker` also runs the worker role (see [Deploying config](../../deploying/)) |
| `backd provision` | Load config, apply provisioning once and exit |
| `backd worker` | Claim and run [async jobs](../../../build/functions/jobs/); `WORKER_CONCURRENCY` caps how many run at once |
| `backd executor` | Run server-side functions for `backd` (needs Deno; see [Running in production](../../functions-runtime/)) |
| `backd egress` | Run the egress role, the executor's only way out (see [Outbound network](../../../build/functions/network/)) |

**General:** `backd help`, `backd version`.
