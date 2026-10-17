---
title: "Re-authentication"
description: "A fresh proof of identity before sensitive actions: which proof applies to which user, the endpoints that ask for it and how a client sends it."
icon: "fingerprint"
weight: 535
toc: true
---

A session proves who a request comes from **when it started**, not that the person is still at the keyboard. Before an action that is hard to undo or that could lock the real user out, `backd` asks for one **fresh proof** of identity. It is the same single step for every kind of user, and it replaces the `password` field that these endpoints used to take.

## The proof

`POST /_auth/reauth` takes the strongest proof the user has:

| The user has | The proof (`method`) |
|---|---|
| a [second factor](../mfa/) | `{"method": "totp" or "recovery_code", "code": "…"}`; a password alone is **not** enough |
| a password, no second factor | `{"method": "password", "password": "…"}` |
| neither (a provider-only user) | `{"method": "email_code", "code": "…"}`, after `POST /_auth/reauth/email-code` emails a 6-digit code (the `reauth-code` message; 10 minutes, 5 attempts) |

`GET /_auth/me` lists the proofs a user can give in `reauth_methods`, so an app asks for the right one. The answer is `{"reauth_token": "…", "expires_at": "…"}`.

- The token is **bound to the session** that asked, stored only as a hash, and works for `reauth.ttl` (5 minutes by default; `1m` to `1h`) and for as many actions as that covers, so a settings page asks once.
- Wrong proofs count against the same limits as signing in: the password is [throttled](../../../operate/administration/security/) per account and address, and second-factor codes lock the user after 10 in an hour.
- A realm that sends no email and a user with neither a password nor a second factor have no way to prove anything beyond their session: sensitive actions don't ask them.

```yaml
# realm.yaml
reauth:
  ttl: 5m          # 1m to 1h
```

## Which actions ask

Send the token in the **`X-Backd-Reauth`** header. Without a good one these answer `401 reauth_required` (with `WWW-Authenticate: Bearer error="insufficient_user_authentication"`):

| Action | Endpoint |
|---|---|
| Turn the second factor on, off or replace its recovery codes | `POST /_auth/mfa/totp/setup`, `POST /_auth/mfa/totp/confirm`, `DELETE /_auth/mfa/totp`, `POST /_auth/mfa/recovery-codes` (not the enrollment a sign-in asks for) |
| Change, set or remove the password | `POST /_auth/password` with `{"new_password"}`, `DELETE /_auth/password` |
| Change the address | `POST /_auth/email` with `{"new_email"}` |
| Deactivate the account | `DELETE /_auth/me`, no body |
| Remove a sign-in method | `DELETE /_auth/identities/{provider}` |
| Create an API key, as an administrator signed in with a session | `POST /_admin/apikeys` |

The `password` and `current_password` fields of these requests no longer exist: the proof comes first. An administrator acting through the admin API with an **admin API key** needs no proof (a key has none to give), nor does an administrator unlinking someone else's provider.

Each of these is audited, and several tell the user by email ([security notices](../mfa/#notices)); creating an API key as a signed-in administrator sends one too.

{{< hint style="note" >}}
API keys are **never** asked for a second factor, by design: they are for servers. That is why creating one needs a proof of a person, and why keys should be scoped and rotated.
{{< /hint >}}

## From a client

The [JavaScript client](../../clients/js/#sign-in-with-google-microsoft-or-apple) keeps the proof for you:

```js
const me = await backd.auth.me()                 // me.reauth_methods: ['totp', 'recovery_code']
await backd.auth.reauth({ method: 'totp', code }) // the client holds the proof until it expires
await backd.auth.changePassword({ newPassword })  // sent with X-Backd-Reauth
```

Without the client, send the header yourself:

```sh
TOKEN=$(curl -s -X POST "$API/v1/acme/_auth/reauth" -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"method": "password", "password": "…"}' | jq -r .reauth_token)
curl -X POST "$API/v1/acme/_auth/password" -H "Authorization: Bearer $SESSION" -H "X-Backd-Reauth: $TOKEN" \
  -H 'Content-Type: application/json' -d '{"new_password": "…"}'
```
