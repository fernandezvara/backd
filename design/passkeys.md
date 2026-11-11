# Design: passkeys (WebAuthn)

- **Issues:** not created yet (§14 proposes the split)
- **Status:** proposed (for review; §15 lists the questions that need an answer)
- **Builds on:** [mfa-passwordless.md](./mfa-passwordless.md) (challenges, `methods`, reauth, recovery, limits). That design deferred passkeys to this one (its §17 and §18).
- **Related designs:** [scoped-sessions.md](./scoped-sessions.md) (sessions in realms with organizations), [account-lifecycle.md](./account-lifecycle.md), [email-delivery.md](./email-delivery.md) (hosted pages, `allowed_redirects`), [identity-providers.md](./identity-providers.md), [admin-ui.md](./admin-ui.md)

## 1. The idea

A passkey is a key pair kept by the user's device, password manager or security key. The private key never leaves it; backd stores only the public key. Signing in is a signature over a challenge backd issues, bound to the website's domain, so a phishing site cannot collect anything that works on the real one.

It fits what backd already has in three places, and this design adds only those three:

| Use | What it replaces or adds | Strength |
|---|---|---|
| **First factor** ("sign in with a passkey") | one more way to sign in, next to password, providers and magic links | proves possession of the device **and**, with user verification, the person (PIN or biometric): it counts as both factors |
| **Second factor** | one more method after a password, provider or magic link, next to TOTP and recovery codes | phishing-resistant, unlike TOTP |
| **Re-authentication proof** | one more `method` for `POST /_auth/reauth` | the same ceremony, bound to the current session |

Standing rules carried over from the MFA design:
- **No session in a URL.** Sessions travel only in `Authorization: Bearer`.
- **Nothing reveals who is registered** where it can be hidden.
- **Challenges are stored hash-only with a TTL** and used once.
- **Docs and tests are part of every issue.**

**What this is not:** a replacement for the password (nothing removes it), passkey-only sign-up (§15, question 3), attestation or hardware allow-lists, or the CLI signing in with a passkey (§11).

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Library | [`github.com/go-webauthn/webauthn`](https://github.com/go-webauthn/webauthn) (BSD-3, the maintained Go implementation). Its parsing and verification are not worth rewriting; a CBOR/COSE bug is a security bug. |
| Where it lives | `internal/auth/passkeys*.go` next to `mfa*.go`; storage in `internal/mongodb`; HTTP in `internal/httpapi/auth_passkeys.go`. |
| Configuration | a top-level `passkeys:` section in `realm.yaml`, **off by default**; relying party id and origins explicit or derived from `sign_in.app_url` (§3). |
| Credentials | stored per user in `<realm>___system.passkeys`; several per user, named, removable (§4). |
| Attestation | `none`. backd does not ask for or keep a device's make and model proof. |
| User verification | **required** for sign-in (first factor and second factor), so one ceremony proves possession and the person. |
| First factor | **discoverable credentials only**: `start` takes no email, so it cannot reveal who is registered; the browser offers the account (§6). |
| Counts as MFA | a passkey sign-in records `methods: ["passkey"]` and `mfa: true`; rules see `user.mfa` and `user.methods` as for TOTP (§7.3). |
| Second factor | `passkey` joins `methods` in an MFA challenge; any one second factor is enough (§7.1). |
| Role requirement | `mfa.required_roles` is satisfied by a passkey or by TOTP; forced enrollment offers both (§7.2). |
| Sign-up | passkeys are **added** to an account after any other sign-in; no account is created from a passkey alone (open: §15, question 3). |
| Last sign-in method | passkeys count as a sign-in method; the last method of any kind cannot be removed (`last_sign_in_method`). |
| Management | register, rename and remove need `X-Backd-Reauth`; at most 10 per user; each change sends a `security-notice` (§8). |
| Recovery | recovery codes and the admin reset work as before; the admin reset also removes passkeys. No new recovery path (§9). |
| Clients | JS does the whole ceremony through `navigator.credentials`; Dart delegates it to the platform; Go and Python manage and verify but cannot sign in with an authenticator (§11). |
| Tests | a software authenticator in Go tests; Playwright's virtual authenticator for browser tests (§12). |

## 3. Configuration (`realm.yaml`)

The template writes the section **commented**, with each key explained.

```yaml
passkeys:
  enabled: false                       # users may register passkeys and sign in with them
  rp_id: acme.example                  # the domain passkeys are bound to (default: the host of sign_in.app_url)
  rp_name: "Acme"                      # shown by the browser when registering (default: realm name)
  origins:                             # where the ceremony may run (default: the origin of sign_in.app_url)
    - https://app.acme.example
    - https://admin.acme.example
    - android:apk-key-hash:...         # native apps, see below
```

**The relying party id is permanent in practice.** A passkey belongs to its `rp_id` for life; change it and every registered passkey stops working, with no migration (the browser will not offer them). The docs say this next to the key. The id must be the app's domain, or a parent of it, never the API host unless the app is served from there too.

**Startup checks:**
- `passkeys.enabled` needs `rp_id` (explicit or derivable) and at least one origin;
- every origin is `https://`, except `http://localhost` and loopback addresses in dev mode (`BACKD_DEV`);
- every web origin's host equals `rp_id` or is a subdomain of it (the browser enforces the same, so a mismatch is a config error found at start rather than a failed sign-in);
- origins are within `sign_in.allowed_redirects` or `sign_in.app_url`, as in the magic-link design;
- `mfa.enabled: false` with `passkeys.enabled: true` is allowed: passkeys then work as a first factor only (§7.1).

**Native apps:** Android origins are `android:apk-key-hash:<base64url sha256 of the signing cert>`; iOS apps use the `rp_id` through *associated domains* and add no origin. backd serves nothing for either; the app owner publishes `/.well-known/assetlinks.json` and `apple-app-site-association` on the `rp_id` domain, and the docs say so.

**The hosted pages** (magic link, hosted sign-in #158) are served by backd, so they can use passkeys only when backd's host is within `rp_id`. When it is not (backd on a different registrable domain from the app), the page hides the passkey button and the docs explain why.

## 4. Storage

`<realm>___system.passkeys`, one document per credential:

| Field | Notes |
|---|---|
| `_id` | backd id |
| `user_id` | the owner |
| `credential_id` | binary, **unique index**; the authenticator's id |
| `public_key` | COSE key, as returned by the library; public, so not sealed |
| `sign_count` | last seen counter (§6.3) |
| `transports` | `usb`, `nfc`, `ble`, `internal`, `hybrid`; sent back as hints at sign-in |
| `aaguid` | authenticator model id; shown to the user as a name when known |
| `backup_eligible`, `backup_state` | whether the passkey syncs; informational, shown in the UI (§7.3) |
| `name` | what the user calls it ("Phone", "YubiKey"); defaults from the AAGUID or the user agent |
| `created_at`, `last_used_at` | |

**User handle.** WebAuthn needs a stable opaque id per user that reveals nothing. It is a random 32-byte value stored on the user document (`passkey_handle`), created at the first registration and never the user id or email. Discoverable sign-in finds the user from it.

**Ceremony state.** Challenges live in the existing `auth_challenges` collection, hash-only, 5 minutes, single use, with new kinds `passkey-register` and `passkey-login`. They keep the library's session data (the challenge, the user handle, the allowed credentials and the verification requirement).

## 5. The ceremonies

Every ceremony has two calls: `options` (the server issues a challenge) and the second call with the authenticator's answer (the server verifies it). The JSON follows the WebAuthn specification's `PublicKeyCredentialCreationOptions` and `…RequestOptions`, so a client passes the `options` to the browser unchanged and returns the result unchanged. Binary fields are base64url, as in the specification.

## 6. Passkeys as a first factor

### 6.1 Flow

1. `POST /_auth/passkey/login/options` (no body, no auth) → `{ challenge_id, options }` with `userVerification: "required"` and an empty `allowCredentials`. It answers the same whether or not anyone has a passkey, so it reveals nothing.
2. The browser asks the user to pick an account and verify (PIN, fingerprint, face), or offers it in the username field through *conditional UI* (`mediation: "conditional"`).
3. `POST /_auth/passkey/login/verify { challenge_id, credential }`.
   - The server finds the credential by `credential_id`, checks that its user handle matches, verifies the signature, the origin, the `rp_id` hash, the challenge and that user verification happened, then updates `sign_count`, `last_used_at` and the backup flags.
   - It continues exactly like another first factor: banned or erased users are refused, then a session with `methods: ["passkey"]` and `mfa: true` is issued. **No second step follows**, because user verification already was one. (A realm that wants TOTP on top of a passkey does not exist; see §15, question 2.)
4. **Any failure** is `401 invalid_passkey`, whatever the reason (unknown credential, bad signature, wrong origin, expired challenge, user banned). The reason goes to the audit log, not to the caller.

### 6.2 Realms with organizations

In realms with organizations a session is scoped to one of them, and sign-in happens through `/_auth/orgs/{org}/…` ([scoped-sessions.md §5](./scoped-sessions.md)). The passkey routes follow: `POST /_auth/orgs/{org}/passkey/login/options` and `…/verify`. The server verifies the passkey as above, then applies the same membership check as the password sign-in, with the same uniform failure for a non-member. A passkey belongs to the **user**, not to an organization, so one passkey signs in to each organization the person belongs to.

### 6.3 Sign counters and cloned keys

Synced passkeys report a counter of 0 forever; hardware keys count up. So:
- a counter of 0 stays 0 and is not an error;
- a counter that goes **down** or repeats when it was above 0 means a possibly cloned key: the sign-in **still fails** (`invalid_passkey`), the credential is flagged in the audit log, and the user gets a `security-notice` (`passkey-clone-suspected`). The user removes the passkey or keeps it; backd does not delete it on its own.

### 6.4 Limits

Verification failures count in the existing throttles: the per-IP login throttle covers `passkey/login/*`, and a failure for a known credential counts toward the per-user `mfa:user:<id>` counter (10 per hour, then `429 mfa_locked`). Failed attempts on unknown credentials count only per IP. Attempts per challenge: 1 (a ceremony that fails is started again; the browser makes that cheap).

## 7. Passkeys as a second factor

### 7.1 Within an MFA challenge

After a first factor that is not a passkey, a user with a passkey gets `methods: ["passkey", "totp", "recovery_code"]` (whichever they have). To use the passkey:

1. `POST /_auth/mfa/passkey/options { challenge }` → `{ options }`, with `allowCredentials` listing **this user's** credentials (the first factor already revealed the user, so there is nothing left to hide).
2. `POST /_auth/mfa/verify { challenge, method: "passkey", credential }`: `credential` is a new field next to `code`; exactly one of them is present, as the method requires.

A wrong ceremony is `400 invalid_code` like a wrong TOTP code and counts against the challenge's 5 attempts and the per-user limit. The session records `methods: [<first factor>, "passkey"]`.

A user whose only second factor is a passkey has no recovery code unless they create them (§9). Registering the **first** second factor, whether passkey or TOTP, generates the 10 recovery codes ([mfa-passwordless.md §5.2](./mfa-passwordless.md)); the second one does not.

### 7.2 Required by role

`mfa.required_roles` is satisfied by **any** enrolled second factor or a passkey registered as such. The forced-enrollment challenge ([mfa-passwordless.md §5.3](./mfa-passwordless.md)) additionally allows:
- `POST /_auth/mfa/passkey/register/options { challenge }`;
- `POST /_auth/mfa/passkey/register/verify { challenge, credential, name }`, which returns the recovery codes **and** the session, as TOTP confirm does.

A passkey registered for sign-in (a first factor) also satisfies the requirement, since the ceremony includes user verification: there is no separate "second factor passkey". It is one passkey with both uses.

### 7.3 What `user.mfa` means for a synced passkey

A passkey kept in iCloud Keychain or a Google account can be used from every device on that account; its strength is the strength of that account. backd treats a user-verified passkey as `mfa: true` anyway, as the specification's authenticator assurance levels do for this case, and exposes what it knows so rules can be stricter:

- `user.methods` contains `passkey`;
- `user.passkey_synced` is `true` when the passkey used is backup eligible (flag from the authenticator); absent otherwise;
- a rule for the most sensitive data can require `!user.passkey_synced`, forcing a hardware key. The docs show the example.

This is the biggest judgment call in the design (§15, question 1).

### 7.4 Re-authentication

`POST /_auth/reauth` gains `method: "passkey"`. It works in two calls: `POST /_auth/reauth/passkey/options` (session-bound, `allowCredentials` from the user's passkeys) and then `POST /_auth/reauth` with `{ method: "passkey", credential }`. The proof table ([mfa-passwordless.md §6.2](./mfa-passwordless.md)) becomes: MFA (TOTP, recovery code **or passkey**), else password, else email code. A user who has a passkey may use it even without TOTP.

## 8. Managing passkeys

All of these act on the signed-in user, and the registration and removal need `X-Backd-Reauth`. Sessions opened through an organization's connection answer `403 sso_session`, like the other account-level actions.

| Endpoint | Behavior |
|---|---|
| `GET /_auth/passkeys` | `[ { id, name, created_at, last_used_at, transports, backup_eligible, backup_state } ]`; never the key material |
| `POST /_auth/passkeys/register/options` | creates the registration challenge; `excludeCredentials` lists the user's existing credentials so one device is not registered twice; `authenticatorSelection` asks for a resident key and user verification |
| `POST /_auth/passkeys/register/verify { challenge_id, credential, name }` | stores the credential; sends `security-notice` (`passkey-added`); **does not** revoke other sessions (unlike enabling TOTP) because adding a sign-in method that the real user controls cannot lock them out |
| `PATCH /_auth/passkeys/{id} { name }` | rename; no reauth needed |
| `DELETE /_auth/passkeys/{id}` | removes it; refuses when it is the last sign-in method (`last_sign_in_method`) or the last second factor of a user in a required role (`mfa_required_by_role`); sends `security-notice` (`passkey-removed`) and revokes the other sessions that were opened **with that passkey** |

At most 10 passkeys per user (`passkey_limit`). `GET /_auth/mfa` gains `passkeys: <count>` and `methods` includes `passkey`.

**Why registration needs reauth.** Otherwise a stolen session could add the thief's own passkey and keep access after the password changes. Enabling TOTP needs it for the same reason ([mfa-passwordless.md §5.2](./mfa-passwordless.md)).

## 9. Recovery

- **Lost device, synced passkey:** nothing to do; the passkey is on the user's other devices.
- **Lost device, device-bound passkey:** the user signs in with another method (password, magic link, provider) and removes the lost passkey. Users with a passkey as their only way in use a recovery code, or an admin resets them.
- **Recovery codes** work as before and are the break-glass for a user whose second factor is a passkey.
- **Admin reset** (`POST /_admin/users/{id}/mfa/reset`, `backd user mfa reset`) removes **passkeys too**, revokes all sessions and sends `mfa-reset`. The `--local` break-glass form does the same.
- **No new recovery path.** A passkey adds no weaker route than the ones that exist.

## 10. Endpoints (new or changed)

| Method and path | Purpose |
|---|---|
| `POST /_auth/passkey/login/options`, `POST /_auth/passkey/login/verify` | first factor |
| `POST /_auth/orgs/{org}/passkey/login/options`, `…/verify` | the same in realms with organizations |
| `POST /_auth/mfa/passkey/options` | second factor options for a challenge |
| `POST /_auth/mfa/verify` | accepts `method: "passkey"` with `credential` |
| `POST /_auth/mfa/passkey/register/options`, `…/verify` | forced enrollment through a challenge |
| `GET /_auth/passkeys`, `PATCH /_auth/passkeys/{id}`, `DELETE /_auth/passkeys/{id}` | list, rename, remove |
| `POST /_auth/passkeys/register/options`, `…/verify` | register (reauth) |
| `POST /_auth/reauth/passkey/options`, `POST /_auth/reauth` | passkey as the proof |
| `GET /_auth/mfa` | gains the passkey count |
| `POST /_admin/users/{id}/mfa/reset` | also removes passkeys |
| `GET /_admin/users/{id}` | gains the passkey count, no key material |

**New codes:** `invalid_passkey`, `passkey_disabled`, `passkey_limit`. Existing ones reused: `invalid_code`, `invalid_challenge`, `mfa_locked`, `mfa_required_by_role`, `reauth_required`, `last_sign_in_method`, `sso_session`.

Every route is documented in `api/openapi.yaml` in the issue that adds it; request and response bodies for `credential` are described by reference to the WebAuthn JSON forms.

**Emails:** no new template. `security-notice` gains the events `passkey-added`, `passkey-removed` and `passkey-clone-suspected`.

**Personal data** ([account-lifecycle.md](./account-lifecycle.md)):
- **export** lists each passkey's `name`, `created_at`, `last_used_at` and flags, never the public key;
- **erase and account deletion** delete the credentials with the user;
- **audit events:** `passkey.registered`, `passkey.removed`, `passkey.login`, `passkey.login_failed` (with the internal reason), `passkey.clone_suspected`.

## 11. Clients, CLI and admin UI

**JavaScript** (`clients/js`): the only client that talks to an authenticator directly.
- `client.auth.passkeys.supported()` (feature check, including conditional UI);
- `client.auth.passkeys.signIn({ conditional?: boolean })` runs both calls with `navigator.credentials.get`; with `conditional: true` it waits for the user to pick from the browser's autofill and returns the same `{ session }` or `{ error }` as other sign-ins;
- `client.auth.verifyMfa(challenge, "passkey")` runs the second-factor ceremony (it detects `passkey` among the `methods` and reads `credential` itself); `verifyMfa(challenge, "totp", code)` is unchanged;
- `client.auth.passkeys.list()`, `register({ name })`, `rename(id, name)`, `remove(id)`;
- the base64url ↔ `ArrayBuffer` conversion and the JSON shape of `PublicKeyCredential` are done by the client, so apps never write it (the specification now has `PublicKeyCredential.parseCreationOptionsFromJSON` and `toJSON`; the client uses them when present and falls back to its own conversion, so older browsers work);
- no new runtime dependency.

**Dart:** the API matches the JS client, but the ceremony is platform code. The client exposes `passkeys.signInWithOptions(callback)` taking a function that receives the `options` JSON and returns the `credential` JSON; the docs show it with the [`passkeys`](https://pub.dev/packages/passkeys) package on iOS and Android. **Flutter web** uses the JS client's path.

**Go and Python:** there is no authenticator in a server or script. They get `Passkeys().List`, `Rename` and `Remove` and the options/verify calls as plain methods (so a test can drive the server with a software authenticator, §12), but no `SignIn`. Documented as such.

**CLI:** `backd login` does **not** support passkeys in this design: a terminal has no WebAuthn API and a browser hand-off is the "device flow" the MFA design rejected. `backd account passkeys list|remove` manage them. Users who sign in with a passkey in the browser use a password or magic link in the CLI, or an API key.

**Admin UI:** the account settings page lists passkeys with add, rename and remove; the sign-in screen offers a passkey button (conditional UI); the users page shows the count and the reset removes them. This follows the admin UI redesign ([admin-ui.md](./admin-ui.md)) and needs `rp_id` to cover the admin UI's origin.

## 12. Testing

- **Server tests:** a software authenticator in `internal/auth/authtest` that creates credentials and signs assertions with Go's `crypto/ecdsa` and the library's `protocol` types, so registration, sign-in, replay, counter regression, wrong origin, wrong `rp_id`, missing user verification and removed credentials are all tested without a browser. This runs in `go test ./...` and needs no services, except for the storage tests (`make test`).
- **Contract:** the new routes appear in `api/openapi.yaml` and `internal/httpapi/contract_test.go` covers them.
- **Browser tests:** Playwright's virtual authenticator (`CDP WebAuthn.addVirtualAuthenticator`) drives the JS client, the admin UI and the example app end to end. This is the one test that proves the options pass to the real browser API unchanged. It runs with the existing e2e jobs (`admin-ui-e2e`).
- **Clients:** JS tests use a mocked `navigator.credentials`; Dart tests use the callback; Go and Python tests use the software authenticator against a running server.
- **Manual check before release:** a real device (phone with a platform passkey, a hardware key) once, as the other release smoke tests.

## 13. Security considerations

- **Phishing resistance:** the origin and `rp_id` hash are verified on every ceremony; a look-alike site gets a signature that fails.
- **No enumeration:** discoverable sign-in takes no identifier, `options` answers identically for everyone, every failure is `invalid_passkey`. The second-factor and reauth options reveal only what the first factor or the session already revealed.
- **Replay:** challenges are 32 random bytes, single use, 5 minutes, hash-only; the counter rule catches cloned hardware keys.
- **User verification required:** a passkey without it is refused as a sign-in. Presence alone (touching a key without a PIN) is not enough because it would make a stolen laptop with an unlocked key a full sign-in.
- **Stolen sessions:** registering and removing need reauth; the attacker cannot add their own passkey.
- **Sync:** the strength of a synced passkey is the strength of its cloud account; §7.3 lets a rule demand a hardware key.
- **API keys** still bypass MFA by design, unchanged.
- **Public keys only:** a database leak reveals no secret that signs in; `credential_id` and `public_key` are not sensitive.
- **Library risk:** parsing is done by `go-webauthn`; backd pins it and watches its advisories like other dependencies (`govulncheck`, Dependabot).
- **Dev mode:** `localhost` over `http` is allowed as an origin only with `BACKD_DEV`, never in production. Realms with `auth: disabled` have no sign-in and are untouched.

## 14. Proposed issue split

Each is one step and one commit; docs and tests are in the same commit.

| # | Step | Contents |
|---|---|---|
| P1 | Foundation | `passkeys:` config and startup checks, `go-webauthn` dependency, storage and indexes, user handle, the software authenticator in `authtest` |
| P2 | Register and manage | the registration ceremony, `GET/PATCH/DELETE /_auth/passkeys`, limits, security notices, export and erase, audit events, OpenAPI |
| P3 | First factor | `passkey/login/*` and the org-scoped routes, sessions with `methods`/`mfa`, counter rule, `user.passkey_synced` in rules and functions |
| P4 | Second factor and reauth | `mfa/passkey/*`, `mfa/verify` with `credential`, forced enrollment, role requirement, reauth method, admin reset |
| P5 | JavaScript client and example app | `client.auth.passkeys`, conditional UI, the example app's account page, the Playwright virtual authenticator |
| P6 | Admin UI and CLI | settings page, sign-in button, users page, `backd account passkeys` |
| P7 | Dart, Go and Python clients | the callback API, list/rename/remove, docs |
| P8 | Documentation | `auth/passkeys.md`, `auth/mfa.md` and `reauth.md` updates, security model page, `SECURITY.md`, the "Is backd for you?" row moves from "does not do yet" to built, the glossary |

P1 to P4 are the server and can ship together as a release; P5 is the first thing a user can try.

## 15. Open questions (please answer; each changes the design)

1. **Does a synced passkey count as `mfa: true`?** Recommended: yes, with `user.passkey_synced` for stricter rules (§7.3). The alternative is `mfa: true` only for device-bound passkeys, which would make most users' passkeys *not* satisfy `required_roles`, so admins would still need TOTP and passkeys would not remove the enrollment friction.
2. **Should a realm be able to require a password or TOTP after a passkey?** Recommended: no; a user-verified passkey is the strongest factor backd has, and a knob that makes it weaker than a password is confusing. A `passkeys.require_second_factor` switch can be added later without breaking anything.
3. **Passkey-only sign-up** (create an account from a passkey and an email, no password and no link)? Recommended: not in v1; a passkey is added after the first sign-in (a magic link with `signup: true` already creates the account, and the app then prompts "add a passkey"). The reason: an email would be unverified, which the realm's other sign-ups never allow. Adding verification means the magic-link flow plus a passkey step, which is a later iteration.
4. **Email-first sign-in** (type the address, then the passkey) as well as the discoverable button? Recommended: no. It needs `allowCredentials`, which reveals whether an address has a passkey unless decoys are produced. Discoverable only.
5. **Attestation** for realms that need a particular hardware key (an enterprise requirement)? Recommended: not in v1 (`none` only). It can be added as `passkeys.attestation` plus an AAGUID allow-list later.
6. **The CLI** (§11): is "no passkey sign-in in the terminal" acceptable? The only alternative is a browser hand-off, which the MFA design rejected.
7. **Sequence:** the roadmap places passkeys after generic OIDC, atomic operators and rate limiting. Nothing here depends on them, so it can move; this is the user's call.

## 16. Alternatives considered

- **Writing the WebAuthn verification ourselves:** rejected; CBOR, COSE and the many authenticator quirks are exactly where subtle bugs live.
- **Passkeys as only a second factor:** rejected; the first-factor use is the reason people want passkeys, and it costs one route pair.
- **Presence-only ("tap the key") sign-in:** rejected (§13).
- **A `passkey` per-organization:** rejected; a person has one device and one set of passkeys, like one password, across organizations.
- **Storing attestation statements or certificates:** rejected; privacy cost and no use.
- **The PRF extension (keys derived from passkeys for field encryption):** out of scope; interesting for [field-encryption.md](./field-encryption.md) later, not for sign-in.
- **Removing the password when a passkey is added:** rejected; users choose, and `last_sign_in_method` protects them.

## 17. Follow-ups

- Passkey sign-up with a verified email (question 3).
- `passkeys.attestation` and an AAGUID allow-list.
- `passkeys.require_second_factor` (question 2).
- Prompt hints for apps: `GET /_auth/passkeys` is enough for an app to say "add a passkey" when the list is empty.
- Related-origin requests (`/.well-known/webauthn`) for apps on several unrelated domains, when browsers support them widely.
