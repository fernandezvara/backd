---
title: "Outbound network"
description: "What a function may reach: the per-function allowlist, the egress proxy and network placement."
icon: "shield"
weight: 561
toc: true
---

## Calling an outside API

A function reaches the outside world only for hosts it declares. Say it needs to email a digest through a provider's HTTP API: declare the host, declare the key as a [secret](../secrets/), and use `fetch`:

```yaml
# function.yaml
mode: async
schedule: "@daily"
network: [api.mailer.example]     # host or host:port, lower case; no scheme, path or wildcard
secrets: [MAILER_KEY]
```

```ts
const res = await fetch("https://api.mailer.example/v1/send", {
  method: "POST",
  headers: { "content-type": "application/json", authorization: `Bearer ${ctx.secrets.MAILER_KEY}` },
  body: JSON.stringify({ to: "ops@example.com", subject: `Digest ${day}`, text: `${orders} orders, ${revenue / 100} in revenue` }),
});
if (!res.ok) throw ctx.error(424, "mailer_failed", `the mail provider answered ${res.status}`);
```

- A request to any host not in `network` fails, and so does one to a private, loopback or link-local address, even for a declared host name that resolves to one.
- Without a `network` list, a function can only call `backd` itself (through `ctx.db`).
- This is how the [cookbook's `daily_digest`](../cron/#another-example-a-daily-digest) would send its result by email instead of storing it.
- Send an idempotency key to the provider when the call has side effects and the function can run twice ([Async jobs](../jobs/#designing-jobs-that-are-safe-to-repeat)).

## Egress: the network allowlist, completed

**Why this exists.** A function runs code you may not have written yourself (an npm dependency, a library update). If its outbound network were left to a single hostname allowlist, a bug or a compromised dependency could still: connect to MongoDB or another private service directly; reach your cloud provider's metadata endpoint (`169.254.169.254`) and steal the credentials your infrastructure runs as (SSRF); or reach the executor's or `backd`'s own internal endpoints. A hostname allowlist alone doesn't stop any of this, because it checks **names**, not the addresses they resolve to — an allowed name can resolve to a private or loopback address (by mistake, by DNS rebinding, or because an attacker controls that DNS record) — and a raw TCP connection (`Deno.connect`, used directly instead of `fetch`) never goes through an HTTP proxy at all, so a proxy-based check alone can't see it either.

Because of that, the control is three layers, all required — each closes a gap the others leave open:

1. **Per-function allowlist.** `network:` in `function.yaml` becomes that process's `--allow-net`, plus `backd`'s own address; with no list, a function can only reach `backd`. Enforced by Deno itself, before any network call is attempted. **Doesn't stop:** an allowed name resolving somewhere it shouldn't, or raw TCP to an address that was never allowed by name at all.
2. **`backd egress`.** An HTTP forward/CONNECT proxy, set as `HTTP_PROXY`/`HTTPS_PROXY` for every function process, with a token scoped to that invocation's own allowed hosts (minted by the executor, verified by egress, so a compromised function can't widen its own allowlist). It resolves every target itself — not trusting the name — and refuses loopback, private, link-local (cloud metadata), CGNAT, multicast and unspecified addresses; `backd`'s internal listener (`EGRESS_ALLOW_PRIVATE`) is its only exception. Run it as `backd egress` (the main image; see [`backd egress help`](../running/#running-the-executor)). **Doesn't stop:** raw TCP, which never consults `HTTP_PROXY`.
3. **Network placement.** Deploy the executor on a network whose only reachable peers are `backd egress` and `backd`'s internal listener, so MongoDB and everything else stays unreachable at the network level — including from a raw TCP connection that bypasses the proxy entirely. **The [production reference](../../operations/production/#functions-executor-egress-and-worker) wires this up**: the executor's `func` network has no route to `data` (MongoDB) or the internet, only to `backd` and `backd egress`; its own test (`deploy/production/test.sh`) proves it from a real function call. `docker/egress-test/` in the repository is the same proof run in isolation, with every case from the table below (`make egress-test`). The [local stack](../../getting-started/#quick-start-with-docker-compose) also runs an executor and egress, but without this network isolation — it's a single-user, localhost-only convenience, not a hardened deployment. Assembling your own deployment from scratch: without this, a function can reach anything its container's network reaches over raw TCP, and neither layer 1 nor layer 2 can see it.

One case layer 3 can't close by itself: a raw TCP connection to the *executor's own* loopback address always succeeds, because it's the same container reaching itself — no network placement can prevent that. What stops it is that the executor's `/invoke` endpoint requires a separate shared credential (`BACKD_EXECUTOR_TOKEN`) a function never has, so reaching the socket gets an unauthenticated request nowhere.

See the [security model](../../auth/security/#server-side-functions-egress-and-network-placement) for the full threat model and what's proven versus what's still the operator's job.
