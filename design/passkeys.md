# Design: passkeys (WebAuthn)

- **Issues:** not created yet (§15 proposes the split)
- **Status:** proposed (for review; §16 lists the questions that need an answer)
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

**What this is not:** a replacement for the password (nothing removes it), passkey-only sign-up (§16, question 3), attestation or hardware allow-lists, or the CLI signing in with a passkey (§11).

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Library | [`github.com/go-webauthn/webauthn`](https://github.com/go-webauthn/webauthn) (BSD-3, the maintained Go implementation). Its parsing and verification are not worth rewriting; a CBOR/COSE bug is a security bug. |
| Where it lives | a **new package `internal/auth/passkey`** (the WebAuthn protocol and credential storage, a leaf that does not import `auth`), with thin glue in `internal/auth/passkeys.go` for sessions, limits and notices; storage in `internal/mongodb`; HTTP in `internal/httpapi/auth_passkeys.go`. It is also the pilot for splitting `auth` (§14.4). |
| Configuration | a top-level `passkeys:` section in `realm.yaml`, **off by default**; relying party id and origins explicit or derived from `sign_in.app_url` (§3). |
| Credentials | stored per user in `<realm>___system.passkeys`; several per user, named, removable (§4). |
| Attestation | `none`. backd does not ask for or keep a device's make and model proof. |
| User verification | **required** for sign-in (first factor and second factor), so one ceremony proves possession and the person. |
| First factor | **discoverable credentials only**: `start` takes no email, so it cannot reveal who is registered; the browser offers the account (§6). |
| Counts as MFA | a passkey sign-in records `methods: ["passkey"]` and `mfa: true`; rules see `user.mfa` and `user.methods` as for TOTP (§7.3). |
| Second factor | `passkey` joins `methods` in an MFA challenge; any one second factor is enough (§7.1). |
| Role requirement | `mfa.required_roles` is satisfied by a passkey or by TOTP; forced enrollment offers both (§7.2). |
| Sign-up | passkeys are **added** to an account after any other sign-in; no account is created from a passkey alone (open: §16, question 3). |
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

**The three actors.** The *app* (the JS client, or the platform code in Dart) only relays data. The *authenticator* (device, password manager or security key) holds the private key and signs only after the user verifies. *backd* holds the public key and issues one-time challenges. Nothing secret travels to backd: it receives a signature, and checks it with a public key.

### 5.1 Registration (signed in, with reauth)

```mermaid
sequenceDiagram
    autonumber
    participant U as User
    participant A as App (client)
    participant B as Browser / platform
    participant K as Authenticator
    participant S as backd

    A->>S: POST /_auth/passkeys/register/options (X-Backd-Reauth)
    S->>S: new challenge, stored hash-only (5 min, single use)
    S-->>A: { challenge_id, options: challenge, rp_id, user handle, exclude existing, UV required }
    A->>B: navigator.credentials.create(options)
    B->>U: "Create a passkey?"
    U->>K: verify (PIN, fingerprint, face)
    K->>K: new key pair for this domain
    K-->>B: public key, credential id, signed statement (challenge + origin)
    B-->>A: credential
    A->>S: POST /_auth/passkeys/register/verify { challenge_id, credential, name }
    S->>S: challenge is mine, unused? origin and rp_id match? user verified?
    S->>S: store public key and credential id
    S-->>A: 201 passkey (never key material)
    S-)U: security-notice "passkey-added"
```

### 5.2 Sign-in as a first factor

```mermaid
sequenceDiagram
    autonumber
    participant U as User
    participant A as App (client)
    participant B as Browser / platform
    participant K as Authenticator
    participant S as backd

    A->>S: POST /_auth/passkey/login/options (no email, no auth)
    S->>S: new challenge, stored hash-only (5 min, single use)
    S-->>A: { challenge_id, options: challenge, rp_id, UV required, no allowCredentials }
    A->>B: navigator.credentials.get(options)
    B->>U: pick an account
    U->>K: verify (PIN, fingerprint, face)
    K->>K: sign (challenge + origin + counter) with the private key
    K-->>B: assertion (credential id, user handle, signature)
    B-->>A: credential
    A->>S: POST /_auth/passkey/login/verify { challenge_id, credential }
    S->>S: find public key by credential id; user handle matches?
    S->>S: signature valid? challenge mine and unused? origin and rp_id? user verified? counter?
    S->>S: user active? consents to accept? (as every sign-in)
    alt all good
        S-->>A: session (methods: [passkey], mfa: true) or a consent challenge
    else anything wrong
        S-->>A: 401 invalid_passkey (the same for every cause)
    end
```

### 5.3 Sign-in with a passkey as the second factor

```mermaid
sequenceDiagram
    autonumber
    participant A as App (client)
    participant B as Browser / platform
    participant S as backd

    A->>S: POST /_auth/login (password)
    S-->>A: { mfa_required, challenge, methods: [passkey, totp, recovery_code] }
    A->>S: POST /_auth/mfa/passkey/options { challenge }
    S-->>A: { options } (allowCredentials: this user's passkeys)
    A->>B: navigator.credentials.get(options)
    Note over B: the user verifies; the authenticator signs
    B-->>A: credential
    A->>S: POST /_auth/mfa/verify { challenge, method: passkey, credential }
    S->>S: same checks as 5.2, plus the MFA challenge's attempts and the per-user limit
    S-->>A: session (methods: [password, passkey], mfa: true)
```

Re-authentication (§7.4) is the same shape as 5.3: options bound to the current session, then the credential in `POST /_auth/reauth`.

## 6. Passkeys as a first factor

### 6.1 Flow

1. `POST /_auth/passkey/login/options` (no body, no auth) → `{ challenge_id, options }` with `userVerification: "required"` and an empty `allowCredentials`. It answers the same whether or not anyone has a passkey, so it reveals nothing.
2. The browser asks the user to pick an account and verify (PIN, fingerprint, face), or offers it in the username field through *conditional UI* (`mediation: "conditional"`).
3. `POST /_auth/passkey/login/verify { challenge_id, credential }`.
   - The server finds the credential by `credential_id`, checks that its user handle matches, verifies the signature, the origin, the `rp_id` hash, the challenge and that user verification happened, then updates `sign_count`, `last_used_at` and the backup flags.
   - Banned or erased users are refused, then the sign-in ends through `finishSignIn` with `methods: ["passkey"]` and `mfa: true`: a session, or the **consent challenge** when the realm's consents are pending, exactly as for every other sign-in. It does **not** go through `afterFirstFactor`, so **no MFA step follows**: user verification already was the second factor. (A realm that wants TOTP on top of a passkey does not exist; see §16, question 2.)
   - Today `finishSignIn` sets `mfa` from `len(methods) > 1`, which would record this session as `mfa: false`. §14.2 replaces that with a rule on the method names.
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

This is the biggest judgment call in the design (§16, question 1).

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

- **Server tests:** a software authenticator in `internal/auth/passkey/passkeytest` that creates credentials and signs assertions with Go's `crypto/ecdsa` and the library's `protocol` types, so registration, sign-in, replay, counter regression, wrong origin, wrong `rp_id`, missing user verification and removed credentials are all tested without a browser. This runs in `go test ./...` and needs no services, except for the storage tests (`make test`).
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

## 14. Fit with the existing auth code

Passkeys are the first feature since MFA that adds a *kind* of factor rather than a provider, so this is where the auth code can drift into special cases. This section records what the code does today, where passkeys would strain it, and the small preparatory change that keeps the result consistent. (Checked against `main` at v0.9.8.)

### 14.1 The shape today

- **First factors converge.** Password (five call sites), magic links, OAuth (two) and organization sign-in all end in `afterFirstFactor`, which decides between a session, an MFA challenge and an enrollment challenge, and then `finishSignIn`, which handles consents and starts the session.
- **Second factors are one switch.** `VerifyMFA` takes `(challenge, method, code string)` and switches on `totp` or `recovery`; `Reauthenticate` has the same shape (`method, proof string`).
- **"Has a second factor" is a TOTP lookup.** `Store.MFAOf` (the TOTP record) is called from eleven places, with three meanings:
  - *"Is MFA on for this user?"*: `afterFirstFactor`, `ReauthMethodsOf`, `memberHasMFA` (the managers' view of who lacks a second factor) and password update. These must also see passkeys.
  - *TOTP-specific*: setup (`ErrMFAAlreadyEnabled`), `DisableMFA`, recovery-code regeneration, the account export of the TOTP state. These stay as they are.
  - *Remove everything*: the magic-link path that makes an account passwordless deletes the MFA record, and the admin reset does the same. These must also remove passkeys.
- **Password update restates the reauth methods** (`ReauthTOTP` or `ReauthRecovery`) instead of asking `ReauthMethodsOf`, so it is a twelfth place that needs the passkey.
- **`mfa` is derived from the method count.** `finishSignIn` calls `startSessionWith(..., len(methods) > 1, ...)`.
- **Method names are string constants** (17 uses of `MethodTOTP` and `MethodRecovery`), and the MFA challenge's method list is written out in `issueChallenge`.

### 14.2 Where passkeys would strain it

| Strain | If done naively | Result |
|---|---|---|
| "Has MFA" means a TOTP record | a user with only a passkey looks like a user without MFA at the "is MFA on?" call sites | `afterFirstFactor` skips the challenge, reauth falls back to password, role requirements fail |
| `mfa` from `len(methods) > 1` | a passkey session has one method (`["passkey"]`) | recorded as `mfa: false` although it is the strongest sign-in; rules that require `user.mfa` refuse it |
| `code string` | the credential is a JSON object | a second parameter next to `code` in two signatures, each with a "one of the two" rule |
| Hard-coded method list in the challenge | add `passkey` in one place and forget another | the challenge says one thing and `VerifyMFA` accepts another |
| Three nearly identical challenge writers (`issueChallenge`, `issueConsentChallenge`, now the ceremony) | a third copy with its own TTL and hashing | drift in expiry or single-use rules |

### 14.3 The preparatory step (P0, no behavior change)

One commit before any passkey code, with the existing tests unchanged and passing:

1. **One question, one answer:** `SecondFactorsOf(ctx, user) ([]string, error)` returns the second factors the user has (today: `totp` or nothing). The "is MFA on?" call sites (`afterFirstFactor`, `ReauthMethodsOf`, `memberHasMFA`, password update) use it, and password update stops restating the method list; the TOTP-specific ones keep `MFAOf`. The two "remove everything" paths (passwordless conversion, admin reset) go through one `RemoveSecondFactors(ctx, user)`. Adding `passkey` later changes these functions and nothing else.
2. **`mfa` from the names, not the count:** `IsSecondFactor(method)` classifies every method name as a first factor, a second factor or both. `startSessionWith` sets `mfa` when the methods contain a second factor, or a first factor that includes user verification (`passkey`).
3. **A `Proof` type:** `Proof{Code string; Credential []byte}` for `VerifyMFA` and `Reauthenticate`, parsed once in the HTTP layer; each method reads the field it needs and rejects the other.
4. **One list of methods:** the challenge's `methods` and the reauth `methods` are built from `SecondFactorsOf`, not written out.
5. **One challenge writer:** `issueChallenge` and `issueConsentChallenge` share a helper that owns the TTL, hashing and single-use rules; the passkey ceremony uses it with an extra `Ceremony []byte` field for the library's session data.

This is a refactor of code that already has tests; it does not need passkeys to be justified, and it removes the duplication the next factor would otherwise copy. Item 5 is also the seam the passkey package uses for its ceremony state (§14.4).

### 14.4 The pilot: `internal/auth/passkey` as its own package

`internal/auth` is one package with one type (`Users`, 484 methods) and one 209-method `Store`; Go does not allow the methods of one type in two packages, so a real split means separate services with narrow interfaces, not moved files. Doing that for the whole package is a large change to security-critical code ([§17](#17-alternatives-considered)). Passkeys are new, so they can be written that way from the start at no migration cost, and the result tells us whether to split more.

**Split of responsibilities**

| In `internal/auth/passkey` (the leaf) | In `internal/auth` (the glue) |
|---|---|
| `Config` (`rp_id`, `rp_name`, origins) and its validation | reading `realm.yaml` into `passkey.Config`, the startup checks that involve other settings (`sign_in`, `mfa`) |
| the `go-webauthn` wrapper: `BeginRegistration`, `FinishRegistration`, `BeginLogin`, `FinishLogin` (with and without a known user), user verification, origin and `rp_id` checks, counter rule (§6.3) | the three exits: `finishSignIn` (first factor), the `passkey` case in `VerifyMFA`, the `passkey` case in `Reauthenticate` |
| credential model and the `Store` for it (add, list, by credential id, update counter and flags, delete, count, user handle, user by handle) | the throttle and per-user lock, audit events, `security-notice` emails, revocation, `last_sign_in_method` and role guards, export and erase |
| the in-memory store and the software authenticator (`passkey/passkeytest`) | organization membership checks (§6.2) |

The leaf knows nothing about sessions, MFA, throttling, email or organizations. It returns a result (user, credential, flags, whether the counter regressed) and the glue decides what that means. That keeps the interface small and the security decisions in one place.

**Dependencies of the leaf** (all passed in, none imported from `auth`):

```go
package passkey

type Store interface {                  // eight methods, implemented by mongodb and the in-memory store
    Add(ctx context.Context, c Credential) error
    ByCredentialID(ctx context.Context, id []byte) (Credential, error)
    ListByUser(ctx context.Context, userID string) ([]Credential, error)
    Update(ctx context.Context, id []byte, u Update) error      // counter, last used, flags
    Delete(ctx context.Context, userID, id string) error
    CountByUser(ctx context.Context, userID string) (int, error)
    UserHandle(ctx context.Context, userID string) ([]byte, error) // get or create
    UserByHandle(ctx context.Context, handle []byte) (string, error)
}

type Ceremonies interface {             // where the library's session data waits between the two calls
    Put(ctx context.Context, kind string, data []byte, userID string, ttl time.Duration) (id string, err error)
    Claim(ctx context.Context, id, kind string) (data []byte, userID string, err error) // single use
}
```

`Service` is built from `Config`, `Store`, `Ceremonies` and a clock. `auth.Store` embeds `passkey.Store`, so there is still one concrete implementation per backend, but the leaf sees eight methods instead of 209. `Ceremonies` is backed by the shared challenge writer from P0 (§14.3, item 5), which is why that step comes first.

**Direction of imports:** `auth` imports `passkey`; `passkey` imports only the standard library and `go-webauthn`. The `registry` package parses `passkeys:` into `passkey.Config` (a leaf import, no cycle). `httpapi` imports `auth`, and `passkey` only for the credential shapes in responses.

**Boundary test:** a test in `passkey` (the same idea as `authcore/deps_test.go`) fails if the package imports `internal/auth`, `internal/mongodb`, `internal/httpapi` or the MongoDB driver.

**Tests get smaller.** The leaf is tested alone with its in-memory store and the software authenticator: registration, assertion, replay, wrong origin, wrong `rp_id`, missing user verification, counter regression and unknown credentials, with no `auth` fixture, no users and no sessions. The `auth` tests then cover only the glue (the three exits, limits, notices, organizations) against a stub of the leaf's results.

**Other rules of this layout**
- **One verification, three exits.** `Service.FinishLogin` is the only place that checks origin, `rp_id`, user verification, challenge, signature and counter; the glue's `verifyAssertion` wraps it with the throttle and then hands the result to the first factor (§6.1), `VerifyMFA` or reauth.
- **No new sign-in flow.** There is no parallel session-creation path: every session still starts in `startSessionWith`, so revocation, `methods`, scoping by organization and consents stay in one place.
- **HTTP:** `internal/httpapi/auth_passkeys.go` next to `auth_mfa.go`, parsing and error mapping only.
- **No plug-in framework.** A `Factor` interface with registration would be speculative for three methods. If a fourth second factor appears, it is introduced then, with the call sites already funneled through the functions in §14.3.

**How we judge the pilot** (after P4, before splitting anything else):
- `passkey` imports nothing from `auth`, and the boundary test stays green;
- the leaf's tests run without any `auth` fixture;
- the glue in `auth` is small and is mostly the three exits, not copies of logic;
- no change in the leaf was needed to fix a bug that came from the core.

If it holds, the next candidate is organizations (`orgs.go`, 1,204 lines and 44 methods, not a sign-in method) as `internal/orgs`, planned in its own design. If it creates more friction than it removes, we keep the rest as one package and have lost little. Password, magic links and OAuth stay where they are for now: they are small and tied to `afterFirstFactor`.

### 14.5 Keeping it consistent

- **Classification test:** a test lists every `Method*` constant and fails when one is not classified in `IsSecondFactor`, so a new method cannot be added without deciding what it counts as.
- **Entry-point test:** a table test lists the sign-in entry points (password, magic link, OAuth, organization sign-in, passkey) and asserts each ends in `finishSignIn`, so none creates a session on its own.
- **Boundary test:** `passkey` may not import `auth`, the MongoDB driver or `httpapi` (§14.4).
- **Invariant tests for the matrix:** passkey as first factor, second factor and reauth, each with and without organizations, with consents pending, with a banned user and with `required_roles`, run on the software authenticator (§12).
- **A recipe in the code:** a comment at `afterFirstFactor` says how to add a first factor (prove it, call `afterFirstFactor`, or `finishSignIn` when it already includes user verification) and a second factor (classify it, add it to `SecondFactorsOf`, add a case to `VerifyMFA` and `Reauthenticate`).
- **The older design points here:** a note at the top of [mfa-passwordless.md](./mfa-passwordless.md) says it is extended by this design, as [scoped-sessions.md](./scoped-sessions.md) does.
- **Other planned work** (generic OIDC #157, SCIM) adds first factors only: it calls `afterFirstFactor` and needs none of the above, but it benefits from P0.

### 14.6 The honest cost

The server code is comparable to MFA: a ceremony file, a management file and a few cases in `mfa.go` and `reauth.go`, plus the HTTP file. The complexity that stays is the **matrix** (three uses, organization-scoped or not, five client stories), which tests and the single verification function contain rather than remove. The risk is not size but a missed call site, which is why P0 and the classification test come first.

## 15. Proposed issue split

Each is one step and one commit; docs and tests are in the same commit.

| # | Step | Contents |
|---|---|---|
| P0 | Prepare the auth code | §14.3: `SecondFactorsOf`, `IsSecondFactor`, `Proof`, one list of methods, one challenge writer; no behavior change |
| P1 | Foundation | the `internal/auth/passkey` package (§14.4): `Config` and startup checks, `go-webauthn` dependency, `Store` and `Ceremonies`, the in-memory store and the software authenticator in `passkeytest`, the boundary test; the Mongo store and indexes, the user handle |
| P2 | Register and manage | the registration ceremony, `GET/PATCH/DELETE /_auth/passkeys`, limits, security notices, export and erase, audit events, OpenAPI |
| P3 | First factor | `passkey/login/*` and the org-scoped routes, sessions with `methods`/`mfa`, counter rule, `user.passkey_synced` in rules and functions |
| P4 | Second factor and reauth | `mfa/passkey/*`, `mfa/verify` with `credential`, forced enrollment, role requirement, reauth method, admin reset |
| P5 | JavaScript client and example app | `client.auth.passkeys`, conditional UI, the example app's account page, the Playwright virtual authenticator |
| P6 | Admin UI and CLI | settings page, sign-in button, users page, `backd account passkeys` |
| P7 | Dart, Go and Python clients | the callback API, list/rename/remove, docs |
| P8 | Documentation | `auth/passkeys.md`, `auth/mfa.md` and `reauth.md` updates, security model page, `SECURITY.md`, the "Is backd for you?" row moves from "does not do yet" to built, the glossary |

P1 to P4 are the server and can ship together as a release; P5 is the first thing a user can try.

## 16. Open questions (please answer; each changes the design)

1. **Does a synced passkey count as `mfa: true`?** Recommended: yes, with `user.passkey_synced` for stricter rules (§7.3). The alternative is `mfa: true` only for device-bound passkeys, which would make most users' passkeys *not* satisfy `required_roles`, so admins would still need TOTP and passkeys would not remove the enrollment friction.
2. **Should a realm be able to require a password or TOTP after a passkey?** Recommended: no; a user-verified passkey is the strongest factor backd has, and a knob that makes it weaker than a password is confusing. A `passkeys.require_second_factor` switch can be added later without breaking anything.
3. **Passkey-only sign-up** (create an account from a passkey and an email, no password and no link)? Recommended: not in v1; a passkey is added after the first sign-in (a magic link with `signup: true` already creates the account, and the app then prompts "add a passkey"). The reason: an email would be unverified, which the realm's other sign-ups never allow. Adding verification means the magic-link flow plus a passkey step, which is a later iteration.
4. **Email-first sign-in** (type the address, then the passkey) as well as the discoverable button? Recommended: no. It needs `allowCredentials`, which reveals whether an address has a passkey unless decoys are produced. Discoverable only.
5. **Attestation** for realms that need a particular hardware key (an enterprise requirement)? Recommended: not in v1 (`none` only). It can be added as `passkeys.attestation` plus an AAGUID allow-list later.
6. **The CLI** (§11): is "no passkey sign-in in the terminal" acceptable? The only alternative is a browser hand-off, which the MFA design rejected.
7. **Sequence:** the roadmap places passkeys after generic OIDC, atomic operators and rate limiting. Nothing here depends on them, so it can move; this is the user's call.

## 17. Alternatives considered

- **Writing the WebAuthn verification ourselves:** rejected; CBOR, COSE and the many authenticator quirks are exactly where subtle bugs live.
- **Passkeys as only a second factor:** rejected; the first-factor use is the reason people want passkeys, and it costs one route pair.
- **Presence-only ("tap the key") sign-in:** rejected (§13).
- **A `passkey` per-organization:** rejected; a person has one device and one set of passkeys, like one password, across organizations.
- **Storing attestation statements or certificates:** rejected; privacy cost and no use.
- **The PRF extension (keys derived from passkeys for field encryption):** out of scope; interesting for [field-encryption.md](./field-encryption.md) later, not for sign-in.
- **Removing the password when a passkey is added:** rejected; users choose, and `last_sign_in_method` protects them.
- **Splitting the whole `auth` package now** (password, magic link, OAuth, organizations, passkeys as packages): rejected for this milestone. One type (`Users`, 484 methods) and one 209-method `Store` are shared by every feature, so a real split needs per-feature services, split store interfaces and a lower shared layer for sessions, audit and throttle, across 255 importing files. That is a project of its own. The pilot (§14.4) tests the pattern on new code first.
- **Passkey code inside `auth` as more methods on `Users`:** rejected; it would be the fifth feature to reach into the same internals, and the protocol part has no need to.

## 18. Follow-ups

- Decide on further splits after the pilot (§14.4): organizations first, as `internal/orgs`, in its own design.
- Passkey sign-up with a verified email (question 3).
- `passkeys.attestation` and an AAGUID allow-list.
- `passkeys.require_second_factor` (question 2).
- Prompt hints for apps: `GET /_auth/passkeys` is enough for an app to say "add a passkey" when the list is empty.
- Related-origin requests (`/.well-known/webauthn`) for apps on several unrelated domains, when browsers support them widely.
