---
title: "Hardening checklist"
description: "One page to go through before a backd deployment faces the internet: realms, credentials, the edge, MongoDB and operations."
icon: "checklist"
weight: 430
toc: true
---

Go through this page before any realm faces the internet, and again after changing the configuration or the deployment. The [production reference deployment](../production/) implements the deployment items, and its test checks many of them.

## Realms

- [ ] **No realm has `auth: disabled`.** Such a realm is open to anyone who can reach `backd`. `backd` logs a `WARN` line for each one at startup.
- [ ] **The sample realms aren't deployed as they are.** `blog`, `shop` and `expenses` in `examples/config`, and the realms `backd template realm --sample` creates, are examples: `shop` has `auth: disabled`, `blog` and `expenses` let anyone sign up, and `expenses` has [known holes](../../examples/expenses/#known-holes). Start from them, then review every setting.
- [ ] **Each realm's `signup` mode is the intended one.** `open` lets anyone create an account (and needs the sign-up rate limit below), `invite` needs an [invitation](../../auth/admin/#invitations), `closed` leaves it to operators.
- [ ] **Every collection that users or anonymous callers need has a reviewed `rules.yaml`, with a `rules.test.yaml`** that `backd rules test --strict` runs in CI: see [Testing rules](../../auth/rules/#testing-rules). A missing rule denies; a too-broad one (`read: "true"`) publishes a collection. See [access rules](../../auth/rules/).
- [ ] **`cors.origins` lists exactly your web apps' origins**, and no `*` for realms with users. Browser apps can keep sessions in an HttpOnly [cookie](../../auth/sessions/#session-cookies) (`sessions.cookie`), which needs this list.
- [ ] **Someone can administer each realm:** an [admin role](../../configuration/realm/#roles) (`admin: true`) held by the right people, the first created with [`backd bootstrap`](../../auth/cli/#the-first-administrator). Startup warns when nobody holds an admin role.
- [ ] **If you run [functions](../../functions/):** the executor and `backd`'s internal listener (`BACKD_INTERNAL_ADDR`) are reachable only from each other, never published; `BACKD_EXECUTOR_TOKEN`, `BACKD_CALLBACK_KEY`, `BACKD_EGRESS_KEY` and, if any function declares secrets, `BACKD_SECRETS_KEY` are random, 32+ characters, kept as secrets (never in `CONFIG_DIR`) — the [production reference](../production/#functions-executor-egress-and-worker)'s `setup.sh` generates all four; `admin: true` functions and `network` lists are reviewed.
- [ ] **The executor can reach only `backd egress` and `backd`'s internal listener — nothing else, including MongoDB, is on its network.** This is [layer 3 of the egress design](../../functions/network/#egress-the-network-allowlist-completed): without it, a function can reach anything its container's network reaches over raw TCP, bypassing both the per-function allowlist and the egress proxy. The [production reference](../production/#functions-executor-egress-and-worker) wires this up (its own test proves it from a real function call); if you assemble your own deployment instead, `docker/egress-test/` in the repository proves the isolation and is the reference to reproduce.
- [ ] **Every function callable anonymously has a [`rate_limit`](../../functions/calling/#rate-limits).** Startup warns, once, for any that don't — a public function with no per-caller limit can be abused as a relay (e.g. one that sends email).
- [ ] **Every [`webhook`](../../functions/webhooks/) function verifies its sender's signature itself**, before trusting the body. `backd` only enforces that its `invoke` rule allows anonymous callers (it has to — a webhook sender has no session or key); it can't tell a genuine request from a forged one.
- [ ] **`BACKD_SECRETS_KEY` (the master key) is backed up separately from database backups**, never alongside them: losing it loses every stored function secret, and storing it with a backup defeats that backup's own encryption. See [Backup and restore](../backup/#what-a-backup-must-include).
- [ ] **`functions.log_retention` and `functions.job_retention`** ([function logs](../../functions/logs/), [async jobs](../../functions/jobs/)) match your privacy policy: both keep a function's own console output, and a job additionally keeps its input and output, in the realm's system database until they expire.
- [ ] **Someone reviews the [audit trail](../../auth/audit/)** (`backd audit --realm <realm> --since 7d`, or the `"msg":"audit"` log lines in your log pipeline), and `audit.retention` matches your privacy policy.
- [ ] **Operators' machines protect the CLI's sessions** (`~/.config/backd/credentials`): log out with `backd logout` on shared machines, and prefer short session lifetimes for realms with administrators.

## Credentials

- [ ] **Every API key has an expiry** (`backd apikey create … --expires 90d`), is used by one service, lives in a secret store, and is rotated before it expires. See the [expiry and rotation policy](../../auth/api-keys/#expiry-and-rotation-policy). Startup warns about keys without an expiry.
- [ ] **API keys never reach browsers or mobile apps.**
- [ ] **Admin sessions are short:** `sessions.admin_idle_timeout` and `sessions.admin_max_lifetime` in each realm's `realm.yaml` (for example `1h` and `12h`), so a stolen administrator's session is worth little. See [Sessions](../../auth/sessions/#sessions-and-tokens).
- [ ] **Keys are scoped to what their service does** (`--scopes read:main/posts,call:main/contact`): a leaked key then exposes that and nothing more. See [Scopes](../../auth/api-keys/#scopes).
- [ ] **Services use `data` keys**; `admin` keys (`--role admin`) exist only for management tooling.
- [ ] **Secrets** (MongoDB passwords, TLS keys, the CA key, `.env`) are outside version control and readable only by the services that need them.

## The edge

- [ ] **TLS everywhere:** the edge terminates TLS with a trusted certificate that renews automatically, and plain HTTP only redirects. MongoDB connections use TLS too (below).
- [ ] **Rate limits:** sign-up, login and the account flows (emailed links, password and address changes) have a strict per-address limit, and the rest of `/v1/` a general one. Requests over a limit get `429`.
- [ ] **Body size:** the edge caps request bodies at `MAX_BODY_BYTES`.
- [ ] **Client addresses:** the edge overwrites `X-Forwarded-For`, and `TRUSTED_PROXIES` names exactly the edge's addresses, never whole private ranges. `backd`'s access log shows real client addresses.
- [ ] **Only the edge is reachable:** `backd` isn't published directly, and `/healthz` and `/readyz` aren't public.
- [ ] **The admin API** is only reachable from your operators' network: `admin.allowed_networks` in each `realm.yaml` (and `admin_networks` per admin where useful), plus `ADMIN_ALLOW_FROM` at the edge in the reference.
- [ ] **The [metrics](../metrics/) port is private** (`METRICS_ADDR` is never published or proxied, and `METRICS_TOKEN` is set if anything but Prometheus can reach it).
- [ ] **API keys of services with fixed addresses are pinned** to them with `--networks`.

## MongoDB

- [ ] **MongoDB requires TLS and authentication**, and isn't reachable from the internet.
- [ ] **`backd` runs with `PROVISION_MODE=verify`** as a user limited to the configured collections; admin credentials are only available to the provisioning step. See [least-privilege provisioning](../production/#least-privilege-provisioning).
- [ ] **Encrypted backups** of every data and `<realm>___system` database run on a schedule, are copied off the server, and a restore has been practised. The decryption key isn't on the server. See [Backup and restore](../backup/).

## Operations

- [ ] **`LOG_LEVEL` is `info`** (the default). `debug` logs every access denial with its reason, which is useful while writing rules but noisy and revealing in production.
- [ ] **Logs are collected and kept**, and startup `WARN` lines are reviewed: realms with `auth: disabled`, realms without rules, API keys without an expiry or about to expire, anonymous-callable functions with no `rate_limit`, and (if you run functions) an executor that isn't configured or doesn't answer.
- [ ] **The container runs hardened:** read-only filesystem, no Linux capabilities, CPU and memory limits (they also bound [password hashing](../../auth/users/#password-storage)), and a stop grace period longer than `SHUTDOWN_TIMEOUT`.
- [ ] **You know how to report and receive security fixes:** see [`SECURITY.md`](https://github.com/fernandezvara/backd/blob/main/SECURITY.md). Only the latest release gets them.
