---
title: "Passkeys"
description: "Sign in with a fingerprint, a face, a PIN or a security key instead of a password: how passkeys work in plain words, how to turn them on, and what to keep for the day a device is lost."
icon: "fingerprint"
weight: 507
toc: true
---

A **passkey** is a pair of keys made on a person's device (or in a password manager, or on a security key). The private key never leaves it; backd keeps only the public key and a name. To sign in, backd sends a random challenge, the device asks the person for their fingerprint, face or PIN, and signs the challenge. The signature is made for the website's domain only, so a look-alike site collects nothing that works on the real one. There is no password to type, to reuse or to leak.

In backd a passkey is a **method** that [strategies](../strategies/) list, like `password` or `totp`. Where it sits in the list is what it does:

- **First** (`users_passkey: [passkey]`): the person signs in with the passkey alone. They type nothing, not even an email: the device offers the passkeys it holds for the realm.
- **After another method** (`admin_passkey: [password, passkey]`): a second step, stronger than an authenticator code because it cannot be phished.
- **As a proof** to [re-authenticate](../reauth/) before a sensitive action, with or without a TOTP.

## Turning them on

```yaml
auth:
  strategies:
    password: [password]
    passkey: [passkey]
  sign_in:
    app_url: https://app.example.com/
    allowed_redirects: [https://app.example.com]
  passkeys:
    enabled: true
    # rp_id: example.com                      # the domain passkeys are bound to (from app_url when left out)
    # origins: [https://app.example.com]      # where the ceremony may run (from app_url when left out)
    # rp_name: Example                        # what the device shows as the site's name
```

Two settings decide whether passkeys work at all, and both are easy to get wrong:

- **`rp_id` is the domain the passkeys are bound to.** It must be the host of the app, or a parent of it (`example.com` serves `app.example.com` and `admin.example.com`). **It cannot change later without losing every registered passkey**: a browser will not offer a passkey made for another domain. Choose it once, as the domain you will keep.
- **`origins` are the addresses the ceremony may run from**: `https://` addresses within the `rp_id`, which are also within `auth.sign_in` (the app's address or its `allowed_redirects`). `http://localhost` is allowed only while `BACKD_DEV` is set. The admin interface is served by backd, so its origin must be listed too when you want passkeys there.

backd refuses to start with an `rp_id` and origins that disagree, and says which line is wrong.

{{< hint style="warning" title="Choose the rp_id once" >}}
Changing `rp_id` (or moving the app to another registrable domain) makes every registered passkey unusable. Keep a [second strategy](#when-a-device-is-lost) until people have added their passkey again.
{{< /hint >}}

### Native apps

An Android app lists its origin as `android:apk-key-hash:<base64url of the SHA-256 of the signing certificate>`. An iOS app uses the `rp_id` through *associated domains* and adds no origin. backd serves nothing for either: the owner of the domain publishes `/.well-known/assetlinks.json` (Android) and `apple-app-site-association` (iOS) on the `rp_id` domain, which is how the phone checks that the app may use passkeys for it. The [Dart client](../../clients/dart/#passkeys) shows the callback that talks to the platform.

### The hosted pages

A passkey can be used only from a page within the `rp_id`. The pages served by backd itself (the magic-link page, for one) have no passkey button, and backd's own host is often not within the app's `rp_id` (backd on `api.example.net`, the app on `example.com`): put the passkey sign-in in the app, which is where the JavaScript and Dart clients run it.

## What people do

- **Add one** on the account page of the app (or in the [admin interface](../../../operate/administration/admin-ui/#your-account-and-passkeys)): the app asks for a fresh [proof](../reauth/), the browser asks for a fingerprint or PIN, and the passkey is stored with a name. At most 10 per person.
- **Sign in** with the button, or from the sign-in field's autofill (conditional UI: the input has `autocomplete="username webauthn"` and one tap on the saved passkey signs in).
- **Remove or rename** one. Removing needs a fresh proof, ends the sessions that passkey opened and tells the person by email.
- A person whose strategy needs a passkey and who has none is led to add one when they sign in.

The HTTP calls are in [Sessions](../sessions/#passkeys); the client calls are in the [JavaScript](../../clients/js/#passkeys), [Dart](../../clients/dart/#passkeys), [Go](../../clients/go/#passkeys) and [Python](../../clients/python/#passkeys) pages. Only the JavaScript client (and Flutter on the web, through it) and a platform callback in the Dart client talk to an authenticator; the Go and Python clients and the `backd` command line list and remove passkeys but cannot sign in with one.

## Example strategies

```yaml
auth:
  strategies:
    password: [password]                     # everyone's fallback
    passkey: [passkey]                       # a passkey alone
    admin_passkey: [password, passkey]       # the password, then a passkey
    admin_hardware: [passkey, totp]          # a passkey, then a code, for the most sensitive role
roles:
  admin:
    admin: true
    strategies: [admin_passkey, admin_hardware]   # an administrator signs in only these ways
```

Who may use which strategy is decided by roles ([Strategies](../strategies/#roles-and-strategies)): a role that lists only passkey strategies is active only in a session made with one.

## Rules that care how a person signed in

A rule or function sees `user.methods` (a list that holds `"passkey"` for a passkey sign-in) and `user.passkey_synced`. A passkey that **syncs** between devices is as strong as the cloud account that syncs it; one that stays on a **hardware key** is stronger. To ask for a hardware key:

```yaml
rules:
  delete: user != nil && "passkey" in user.methods && !user.passkey_synced
```

## When a device is lost

- **Keep a second strategy for administrators.** If a role accepts only passkey strategies and its holder loses every device, nothing signs them in. Keep a strategy such as `admin_password: [password, totp]` on the role, or another administrator who can help.
- **A lost device is removed by an administrator** with `DELETE /_admin/users/{id}/passkeys/{passkey_id}` (the [user page](../../../operate/administration/admin-ui/#your-account-and-passkeys) of the admin interface lists them): the person is told by email, the sessions that passkey opened end, and it is audited.
- **A full reset** (`POST /_admin/users/{id}/mfa/reset`, `backd-server user mfa-reset`, also when no administrator can sign in) removes the authenticator app, the recovery codes and every passkey, ends every session and tells the person. Their next sign-in enrols what the strategy needs.
- **backd will not remove the last way in.** Removing a passkey answers `409 credential_required` when it is what lets a holder of an administrator role complete any strategy that role allows, and `409 last_sign_in_method` when it is the person's only way to sign in. Add another first.
- **A password is not enough to prove who you are once a passkey exists**: the fresh proof for sensitive actions asks for the passkey or an authenticator code, so a stolen session cannot add its own passkey. In a terminal, where a passkey cannot be used, that means `backd account passkeys-remove` also needs an authenticator code.

## What backd checks

On every ceremony: the origin and the `rp_id` hash, a one-time challenge (32 random bytes, valid 5 minutes, stored by hash), that the person was **verified** (fingerprint, face or PIN: a bare touch of a key is not accepted) and the signature. A counter that goes backwards (a cloned hardware key) fails the sign-in, flags the passkey, is audited as `passkey.clone_suspected` and tells the person by email. Every failure answers the same `invalid_passkey`, whatever the cause, so nobody can learn which accounts exist. See the [security model](../../../operate/administration/security/).
