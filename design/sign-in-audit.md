# Design: what the audit trail records, and the sign-in events

- **Issues:** #966–#970 (milestone 44-sign-in-audit)
- **Status:** decided
- **Related designs:** [passkeys.md](./passkeys.md) (`passkey.login`, `passkey.login_failed`), [auth-strategies.md](./auth-strategies.md), [compliance.md](./compliance.md)

## 1. The problem

The audit trail records security-sensitive *changes*. Of the *sign-ins* it records only those of administrators (`admin.login`), of passkeys and of organization connections. A password, email-code, magic-link or provider sign-in leaves nothing, and neither does any failure other than a passkey's. Compliance questionnaires ask for both ("who signed in, who failed to"), and an operator investigating an account takeover has nothing to read.

## 2. What is recorded

**Every audit action is an entry the realm can switch on or off**: the sign-in events below (new here), and every action the trail records today (`role.add`, `apikey.create`, `data.update`, `export`, `org.create`…). The list is known in advance, so the setting is built once for all of them and keeps working when an action is added later (§6). Only the sign-in events below have defaults other than on.

| Entry | Records | Default |
|---|---|---|
| `admin.login` | a sign-in by a user who holds an admin role (existing). **A special case:** it records admins only, never ordinary users | on |
| `user.login` | a sign-in by a user who holds **no** admin role, by any method except a passkey or an organization connection (they have their own entries) | off |
| `user.login_failed` | a first step that failed: wrong password, unknown address, disabled account, unverified address, network refused, expired password, bad or used email code or magic link, a provider sign-in refused | on |
| `user.step_failed` | a later step that failed: a TOTP, recovery or email code, or a link | on |
| `passkey.login` | a passkey sign-in (existing) | on |
| `passkey.login_failed` | a failed passkey sign-in or step (existing) | on |
| `org.sso.login` | a sign-in through an organization's connection (existing) | on |

An administrator who signs in produces `admin.login` and never `user.login`, whatever the method: the two entries do not overlap. A failure is `user.login_failed` for everyone, administrators included, because the trail cannot know who the person is yet.

Every other action defaults to **on**, as it is recorded today. The defaults keep what is recorded today and add the failures: **errors on, successes off, except the ones already recorded.**

## 3. Configuration

```yaml
# realm.yaml
audit:
  retention: 365d
  entries:                       # true records the action, false does not
    # sign-in
    admin.login: true            # administrators only
    user.login: false            # everyone else
    user.login_failed: true
    user.step_failed: true
    passkey.login: true
    passkey.login_failed: true
    org.sso.login: true
    # users
    user.create: true
    user.signup: true
    # … one line for every action, in groups
```

- **One key per action**, named as the trail names it (the "Action" column of `audit.md`). Every key is optional and takes its default (§2: on, except `user.login`) when missing.
- **The template writes them all.** `backd template realm` and `backd template project` generate the block from the same list the server uses, grouped and commented, so the choice is visible and one edit away. A test fails when the template lacks an action, so it cannot go stale.
- A name that is not an action, or a value that is not `true` or `false`, stops the load with the file, the key and the closest valid names (a typo in `role.ad` must not silently record nothing).
- **Turning an entry off is logged.** `backd-server serve` logs at start which entries are off, and the audit trail records `audit.entries_changed` (always on, not configurable: it is how a change to the trail is itself visible) with the names whose value changed since the last start. The docs warn that turning off `role.add`, `apikey.create` or the like weakens the trail.
- Read at load like the rest of `realm.yaml`: a change needs a restart (dev mode reloads).

## 4. Failures cannot flood the trail

The trail is append-only and kept a year, and a failure is something an attacker causes. Two rules:

- **One record per target and address in a window.** The first failure of a (target, client address) pair in 10 minutes is recorded; later ones in the window are counted, not recorded. The failure that makes the existing sign-in throttle refuse the address is recorded too, with `throttled: true`. The count of the window goes in the next record as `suppressed`. The counter reuses `IncrementCounter` (the `login_attempts` store) under its own key namespace.
- **Successes are not capped:** one record per sign-in is the point of turning `user.login` on, and it needs a session first.

## 5. What a record holds

- **Target:** the user's id when the account exists. When it does not, `anonymous`, and the details carry `address_hash`: a keyed hash (HMAC with a key derived from the realm's secret) of the normalized address, truncated. It lets an operator see that two failures were about the same address without the trail ever holding an address that may belong to no one. This is the rule the passkey failures already follow for an unknown credential.
- **Details:** `strategy`, `methods` (the steps taken), `session_id` for a success; `reason` for a failure; `step` for `user.step_failed`. Never a password, a code, a link or a token.
- **Reasons** are a closed list: `invalid_credentials` (wrong password and unknown address look the same), `disabled`, `unverified`, `network`, `throttled`, `password_expired`, `invalid_code`, `invalid_link`, `provider_refused`. The app is told what it is told today; the trail may be more precise than the response because only administrators read it.
- The client address and the request id are already part of every record.

## 6. One list, one gate

- **One registry.** Every audit action is declared once, in a table in `internal/auth` (name, group, default, one-line meaning). The constants, the template, the validation of `audit.entries`, the docs check and the admin UI filter all read it. An action used without being in the table fails a unit test, which is what makes a later event work with no change to the setting: add it to the table and it is configurable, templated and checked.
- **One gate.** Everything is recorded through the same write function, which looks the action up in the realm's `entries` and records nothing when it is off. The 90-odd call sites do not change. The sign-in paths call it like any other, and `admin.login`, the passkey entries and `org.sso.login` go through it too, so the setting governs them.
- The sign-in paths already converge on a few functions in `internal/auth` (password, email code, magic link, provider/ID-token, the step verifiers); each records its success or failure once.

## 7. Out of scope

Sign-out and session revocation by the user, token refresh, scoped-session switches and sign-up (`user.signup` exists). They can be entries later; the map is open to new names without changing the shape.

## 8. Docs and checks

`audit.md` gets a row per new action, a "Default" mark and the sentence that says failed logins are not recorded removed; `realm.md` documents `audit.entries`; the template and the security model page say what is recorded; `scripts/check-docs-audit.py` keeps code, table and docs in step (and now also the table and the template). The admin UI audit filter lists the new actions.
