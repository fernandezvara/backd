---
title: "Command-line administration"
description: "Create a realm's first administrator with backd bootstrap, then manage users and API keys from the command line through the admin API."
icon: "terminal"
weight: 505
toc: true
---

The `backd` binary is also the command-line client for administering a realm. `backd user` and `backd apikey` don't touch MongoDB: they call the realm's [admin API](../admin/) on a running `backd`, as an administrator. They never need database credentials, and every change they make goes through the same checks as any other admin request, including [network restrictions](../../configuration/realm/#network-restrictions).

Two commands write to MongoDB directly instead: `backd bootstrap`, which creates a realm's first administrator, and `backd secret rotate-key`, which re-encrypts every realm's [secrets](../../functions/secrets/) under a new master key (see [Secrets](../admin/#secrets)).

## Conventions

- **Every value is a flag.** There are no positional arguments: `backd user create --realm blog --email ada@example.com`. `--realm` (and `--email`, `--name`, …) are required where the command needs them, and `--url` picks the server (below). `backd <command> --help`, or `backd <command> help`, lists a command's flags; `backd help` lists the commands.
- **Exit codes.** `0` on success (and after help); `1` when the command ran and failed (a server error, a wrong password, a missing `CONFIG_DIR`); `2` when it was invoked wrongly (an unknown command or flag, a missing or invalid flag value). Scripts can tell the two failures apart.
- **Output.** Help and results go to standard output; warnings, errors and prompts go to standard error, so `KEY=$(backd apikey create --realm blog --name ci)` captures only the key.

## Local and remote commands

| Command | Works on | Needs |
|---|---|---|
| `backd serve`, `backd provision` | MongoDB and `CONFIG_DIR` | `CONFIG_DIR`, `MONGO_URI` |
| `backd template`, `backd databases`, `backd rules test` | `CONFIG_DIR` | `CONFIG_DIR` |
| `backd functions build\|types` | `CONFIG_DIR` | `CONFIG_DIR` (`build` also needs Deno) |
| `backd bootstrap --realm <realm> --email <email>` | MongoDB, once per realm | `CONFIG_DIR`, `MONGO_URI`, a provisioned realm |
| `backd login`, `backd logout`, `backd whoami` | A running `backd` | Its URL |
| `backd user …`, `backd apikey …`, `backd audit …`, `backd secret set\|list\|delete`, `backd functions invoke\|history\|logs\|jobs`, `backd storage check` | A running `backd`, through the [admin API](../admin/) or the function's own route | Its URL, and a stored session or `BACKD_API_KEY` |
| `backd secret rotate-key` | MongoDB, directly (not the admin API) | `CONFIG_DIR`, `MONGO_URI`, `BACKD_SECRETS_KEY`, `BACKD_SECRETS_NEW_KEY` |
| `backd version` | Nothing | Nothing |

## The first administrator

Administrators are users holding an **admin role**: a role with `admin: true` in [`realm.yaml`](../../configuration/realm/#roles). The template (`backd template realm`) declares one, named `admin`:

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
- The password follows the realm's [password policy](../users/#password-policy). It is read from the terminal without echo, or as the first line of standard input.
- To keep the assignment if the realm is ever rebuilt from its config, also list the email under the role's `users` in `realm.yaml`. `bootstrap` reminds you when it isn't listed.

At startup, `backd` warns about each realm that declares no admin role, or whose admin roles nobody holds.

## Logging in

```sh
backd login --realm blog --url https://api.example.com
# Email: ops@example.com
# Password:
# logged in to realm blog on https://api.example.com as ops@example.com (session expires at … at the latest)

backd whoami --realm blog
# ops@example.com (id d3c9ljp8hc2g00b6s1m0) on realm blog at https://api.example.com; roles: admin
```

`backd login` signs in through [`/_auth/login`](../sessions/), like any user, and keeps the session for the following commands. It is an ordinary session: the realm's idle timeout, maximum lifetime, [login throttling](../sessions/#brute-force-protection) and the user's `login_networks` all apply, and it shows up in the user's session list. `backd logout --realm blog` ends it on the server and forgets it.

- **Server:** `--url`, else the `BACKD_URL` environment variable, else the server of the last login.
- **Email:** `--email`, or a prompt. When standard input isn't a terminal, `--email` is required and the password is the first line of standard input.
- **Sessions are stored** per server and realm in `~/.config/backd/credentials` (or `$XDG_CONFIG_HOME/backd/credentials`, or the file named by `BACKD_CREDENTIALS`). The file is created readable only by you, in a directory only you can open. Anyone who can read it can act as you until the sessions expire, so log out on shared machines.
- **TLS:** `backd` verifies the server's certificate against the system's CAs. For a private CA, such as the local stack's, point `BACKD_CA_CERT` at its PEM file. Logging in over plain HTTP to anything but `localhost`, a loopback address, or a host name without dots (a container on a private network, such as `http://backd:8080`) prints a warning: the password would travel unencrypted.

## Using an API key instead

When `BACKD_API_KEY` is set, `backd user` and `backd apikey` send that key instead of a stored session. It must be an [API key](../api-keys/) with the `admin` role. This suits automation that can't log in interactively:

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
