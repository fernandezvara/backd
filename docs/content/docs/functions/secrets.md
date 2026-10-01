---
title: "Secrets"
description: "Encrypted values a function may read, managed by realm administrators."
icon: "vpn_key"
weight: 560
toc: true
---

A function that needs a third-party API key, or any value it shouldn't hold in its own code, declares it in `function.yaml`'s `secrets` list and reads it from `ctx.secrets`, decrypted, at call time — never from an environment variable, which the function's process doesn't have.

{{< hint style="tip" title="Best practice" >}}
Declare only what a function needs, and prefer database-scope secrets (`NAME`) to realm-scope ones (`realm.NAME`): a function can then never read another database's secrets. Never log a secret; the value is masked in captured logs, but only when it appears exactly.
{{< /hint >}}

```yaml
secrets: [STRIPE_KEY, realm.SHARED_WEBHOOK_SECRET]
```

- `NAME` (upper-case letters, digits and `_`, starting with a letter) is scoped to the function's **own database**; `realm.NAME` is scoped to the **realm**, shared by every database's functions. There's no fallback between them, and a database's functions can never read another database's secrets.
- `ctx.secrets` is keyed exactly as declared: `ctx.secrets.STRIPE_KEY` and `ctx.secrets["realm.SHARED_WEBHOOK_SECRET"]`.
- A secret with no stored value makes `backd` warn at startup (naming the function) and the function answer `500 secret_missing` until one is set. Declaring a secret in a realm with `auth: disabled` is refused at startup: there's nowhere to store it.

### Setting values

Realm administrators manage values through the [admin API](../../auth/admin/#secrets) or its CLI client:

```sh
backd secret set --realm blog --database shop --name STRIPE_KEY --url https://api.example.com
# Password: (the value, read without echo — or piped in, e.g. printf '%s' "$KEY" | backd secret set …)
backd secret set --realm blog --name SHARED_WEBHOOK_SECRET --url https://api.example.com   # realm scope: no --database
backd secret list --realm blog
backd secret delete --realm blog --database shop --name STRIPE_KEY
```

Values are write-only: `backd secret list` and `GET /_admin/secrets` show only the scope, name and who last changed a secret, never its value. A changed or deleted value reaches running functions within about a minute (decrypted values are cached briefly, so `backd` doesn't decrypt on every call).

### Storage and the master key

Secrets are stored in `<realm>___system.secrets`, encrypted with AES-256-GCM under a master key `backd` never persists: the environment variable `BACKD_SECRETS_KEY` (32 characters or more), or `BACKD_SECRETS_KEY_FILE` naming a file that holds it. Without one configured, `backd` refuses to start if any function declares secrets.

{{< hint warning >}}
**Trust model:** a realm's administrators can set and delete that realm's secrets (through the admin API, like any other admin action — audited); the operator holding `BACKD_SECRETS_KEY` can decrypt every secret of every realm on the instance, same as anyone with `CONFIG_DIR` and `MONGO_URI` access already could reach everything else. Keep it like any other production secret: outside version control, in a secret store, readable only by `backd`.
{{< /hint >}}

To rotate the master key (for example after a suspected leak, or on a schedule), generate a new one and run, where `backd` runs:

```sh
BACKD_SECRETS_KEY=$OLD_KEY BACKD_SECRETS_NEW_KEY=$NEW_KEY \
  docker compose exec backd /backd secret rotate-key
```

This re-encrypts every realm's secrets in place — unlike `set`/`list`/`delete`, it writes to MongoDB directly, so it needs `CONFIG_DIR` and `MONGO_URI`, not a session or API key. Afterward, restart every `backd`, `worker` and CLI use with `BACKD_SECRETS_KEY` set to the new key; the old one is no longer needed.
