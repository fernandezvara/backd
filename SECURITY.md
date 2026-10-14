# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub: open the repository's **Security** tab and choose **Report a vulnerability**. Don't open a public issue.

Include what you found, how to reproduce it, and what an attacker could do with it. You'll get an acknowledgement within a week, and updates until it's fixed. Once a fix is released, the advisory will credit you unless you prefer otherwise.

## Supported versions

Only the latest release gets security fixes.

| Version | Supported |
|---|---|
| Latest release | yes |
| Older releases and unreleased commits | no |

## Verifying releases

Release archives come with `checksums.txt` and an SPDX SBOM each. The container image `ghcr.io/fernandezvara/backd` is scanned before publishing, carries SBOM and provenance attestations, and is signed with cosign (keyless, GitHub OIDC):

```sh
cosign verify ghcr.io/fernandezvara/backd:v0.8.2 \
  --certificate-identity-regexp '^https://github.com/fernandezvara/backd/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Dependencies are watched continuously: `govulncheck` and an image scan run in CI, and Dependabot proposes updates weekly.

## Scope

In scope: authentication (`/v1/{realm}/_auth`), sessions and API keys, access rules (the `rules:` section of `collection.yaml`), the admin API (`/v1/{realm}/_admin`), the admin web interface served at `/_ui/` (its CSP and headers, how it holds the admin token, and any way to run script in it or read that token) and the CLI that uses it (`backd login`, `user`, `apikey`, `secret`, `bootstrap`, and the credentials file it keeps), the audit trail (`/v1/{realm}/_admin/audit`: records that are missing, alterable, or that contain secrets), erasing a user (`DELETE /v1/{realm}/_admin/users/{id}`: personal data it leaves behind beyond what the docs list, or an erase that skips silently), server-side functions (`/v1/{realm}/{database}/_func/…`, the executor, its sandbox and limits, callback credentials and the internal listener, `backd egress` and its per-invocation tokens, a function's ability to reach anything other than what it declares — including over raw TCP or to a private address a declared host name resolves to, and its secrets: `/v1/{realm}/_admin/secrets`, their encryption at rest and a function's ability to read another database's or realm's; async jobs (`/v1/{realm}/{database}/_jobs/{id}`: a job readable by anyone other than its own caller or an API key, or an `Idempotency-Key` claim crossing between two different callers), the email flows and the hosted pages (verification, password reset, address change, invitations: tokens, redirects, answers that tell whether an address is registered, and custom emails a function sends: a message sent to someone the caller shouldn't reach, or past its limits), files (`…/_files/…`, uploads in both modes and their tokens, downloads, signed links and the key that signs them, type detection and what a download can run in a browser, the storage credentials, `backd storage reconcile`, and a function's access to files: a file readable, writable or removable by a caller whose rules say otherwise, an object key or header a client can influence, or a secret of the storage in an answer or a log), sign-in with Google, Microsoft or Apple (`/v1/{realm}/_auth/oauth/…`: a session or login code that can be obtained or redeemed without the provider's proof or the app's PKCE verifier, a `state` or nonce that can be replayed, an ID token accepted that fails its signature, issuer, audience or expiry, an account linked or taken over through a provider address the rules say must not link, a `redirect_to` outside the allowed list, or a request backd makes to a host that is not the provider's), push notifications (`/v1/{realm}/_auth/devices`, `/v1/{realm}/_admin/push/*`, the built-in senders and the worker's egress: a device token or message content in a log or response, a push delivered to a device of another user or of a session that ended, a web subscription endpoint that makes backd call a host or port the realm doesn't allow, a provider credential outside the realm's secrets, or a limit that can be exceeded), two-step sign-in, re-authentication and magic links (`/v1/{realm}/_auth/mfa/…`, `…/_auth/reauth`, `…/_auth/magic-link`: a member of one organization who can read, count, change or move a document of another (`_meta.org`, `inOrg`, `hasOrgRole`, `orgClaims`, `/v1/{realm}/_orgs`), a role or capability obtained that the granting rules forbid, a managing role through an open invitation link, an invitation token that works for the wrong person or twice, access that survives a removal, or a session obtained without the second factor a user has, a code, recovery code or time step that can be used twice or tried beyond the limits, a re-authentication token that works for another session or after its time, a magic link or code redeemed without the asking app's verifier or twice, an answer that tells whether an address is registered, a second factor or secret in a log or response that doesn't need it, or an account taken over through a squatter's credentials), the metrics endpoint (`METRICS_ADDR`: anything it exposes that its docs say it doesn't, or a way to reach it from outside), the JavaScript client (`backd-js`: credentials it exposes or sends where it shouldn't), the query language (`where`, `order_by`), and anything that lets a caller read or change data they shouldn't.

Out of scope:

- Realms configured with `auth: disabled`: they are open by design.
- Denial of service through request volume. Rate limiting belongs in the reverse proxy in front of `backd` (see the operations docs).
- Weaknesses in how you deploy it, such as serving without TLS or leaking API keys.

Before exposing backd, go through the [hardening checklist](https://fernandezvara.github.io/backd/docs/operations/checklist/) (Operations → Hardening checklist in the documentation). The security model is under Authentication → Security model, and a tested production reference deployment is in [`deploy/production`](deploy/production) (Operations → Production deployment).

## Consents and personal data

- Consent records are append-only (no route updates or deletes them), keep the SHA-256 of the exact text shown, and hold an IP address and user agent only when `compliance.consent_proof` lists them. At erasure those two are removed and the user id becomes an HMAC of the address under the realm secret `BACKD_CONSENT_PSEUDONYM_KEY`; the records are removed after `compliance.consent_retention`.
- The consent challenge of a sign-in is random, single-use, expires in 5 minutes and is stored only as a hash. After a `reaccept_by` date, a session that has not accepted can only read its consent state, accept, sign out or delete the account.
- Administrators record consents with a required `evidence`, audited. `evidence` should be a reference, not personal data.
- `GET /_consents` serves public texts and nothing about people; the personal data scope report (`/_admin/compliance/scope`) needs the `config` area.
