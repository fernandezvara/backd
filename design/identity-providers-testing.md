# Testing provider sign-in with real providers

- **Issue:** #216
- **Design:** [identity-providers.md](./identity-providers.md)
- **Status:** a guide to run by hand before each release that touches sign-in with providers

CI tests the whole flow against a fake provider (`internal/oauth/oauthtest`): every rule of the design, every
error code, the admin side and the client. What it cannot prove is that **Google, Microsoft and Apple**
behave as the fake does: their consoles, their redirect rules, their token contents, Apple's form post and
first-authorization name, Microsoft's tenants, Apple's revocation. This guide is the checklist for that, part
by part. Run it against a staging stack with real accounts, never against production data.

Nothing here goes in the repository except this guide: **credentials stay in your shell**, test results go in
`support_docs/release-smoke-tests.md` (local, never committed).

## 1. What you need

| For | You need |
|---|---|
| Google | A Google account that owns a Google Cloud project; two or three Google accounts to sign in with (one of them with the same address as a verified backd user) |
| Microsoft | A Microsoft Entra ID tenant you can register apps in (a free developer tenant is enough); a **work or school** account in it, a **personal** Microsoft account (outlook.com), and, for the multi-tenant checks, a guest or second tenant |
| Apple | An Apple Developer Program membership; a Mac or iPhone with an Apple ID signed in; for the native checks, an iOS app (or Apple's sample) that can request a Sign in with Apple credential |
| Backd | A staging stack reachable from the browser **over HTTPS on a public host name** (Apple refuses `localhost` and plain `http`; a tunnel such as `cloudflared tunnel` or `ngrok http` to `make example`'s nginx or to a staging host works), with MongoDB, `BACKD_SECRETS_KEY` set, **a worker running**, and `BACKD_URL` set to the public address |
| Tools | `curl`, `jq`, `mongosh` (or the stack's MongoDB shell), the `backd` binary as the administrator CLI (`backd login`), a browser with a private window per account |

A single realm for the whole run, named `idtest` here:

```yaml
# <CONFIG_DIR>/idtest/realm.yaml
auth: enabled
signup: open
sessions: { cookie: { enabled: true, same_site: lax } }   # only for checks 5.8 and 12.4
cors: { origins: [https://app.idtest.example] }
roles: { admin: { admin: true } }
sign_in:
  allowed_redirects: [https://app.idtest.example, "idtest://"]
providers:
  google:
    client_id: <google client id>
    client_secret: secret:GOOGLE_CLIENT_SECRET
    native_client_ids: [<google ios/android client id>]
  microsoft:
    client_id: <application (client) id>
    client_secret: secret:MICROSOFT_CLIENT_SECRET
    tenant: common
  apple:
    services_id: <com.example.idtest.web>
    bundle_ids: [<com.example.idtest.ios>]
    team_id: <TEAMID>
    key_id: <KEYID>
    private_key: secret:APPLE_SIGN_IN_KEY
account:
  on_signup: main/create-profile       # section 11; needs the function below
```

`app.idtest.example` stands for a page that can receive the redirect. The simplest is the blog example
(`clients/js/examples/blog`) served from the same origin as the API with `PROVIDERS` set in its `app.js`;
or any static page that calls `completeSignIn()`. Where this guide says "the app", it means that page.

Set the secrets from your shell (the values never touch a file):

```sh
export BACKD_URL=https://staging.example     # the public address
backd login --realm idtest --url "$BACKD_URL" --email admin@idtest.example
printf '%s\n' "$GOOGLE_SECRET"    | backd secret set --realm idtest --name GOOGLE_CLIENT_SECRET
printf '%s\n' "$MICROSOFT_SECRET" | backd secret set --realm idtest --name MICROSOFT_CLIENT_SECRET
backd secret set --realm idtest --name APPLE_SIGN_IN_KEY < AuthKey_<KEYID>.p8     # the whole file
```

(`backd secret set` reads a single line from standard input. For Apple's multi-line `.p8` use the admin
UI's Secrets page, or the admin API: `PUT /v1/idtest/_admin/secrets/APPLE_SIGN_IN_KEY` with `{"value": "<the file's text>"}`.)

The hook function of section 11 (`main/create-profile`, `internal: true`, `mode: async`, `retry: {attempts: 3, backoff: 1m}`)
can be as small as: log nothing, write `{user_id, provider, profile}` to a `profiles` collection with
`ctx.admin.db`. Keep the profile collection to see what arrived.

**Useful windows while you test**

```sh
backd audit --realm idtest --limit 20                      # newest first
backd audit --realm idtest --action identity.linked
backd user identities --realm idtest --email <address>
docker compose logs -f backd | jq -c 'select(.msg|test("provider|sign-in|ID token|Apple"))'
mongosh "$MONGO_URI" --eval 'db.getSiblingDB("idtest___system").oauth_states.countDocuments()'
```

## 2. Registering the applications

Do these once; write down every id, key id and secret in your password manager.

### Google

1. [Google Cloud console](https://console.cloud.google.com) → *APIs & Services* → **OAuth consent screen**: user type *External*, status *Testing*, add your test Google accounts as **Test users**. Scopes: the defaults (`openid`, `email`, `profile`) are all backd asks for.
2. **Credentials → Create credentials → OAuth client ID → Web application.** Authorised redirect URI: `https://<public host>/v1/idtest/_auth/oauth/google/callback` (exactly what `backd` logs at startup as `callback_url`). Copy the client id and secret.
3. For the native checks: create an **iOS** (bundle id) or **Android** client, or a second *Web application* client to play the part of a native app. Its id goes in `native_client_ids`.

### Microsoft

1. [Entra admin center](https://entra.microsoft.com) → *App registrations* → **New registration**. *Supported account types*: "Accounts in any organizational directory and personal Microsoft accounts" (needed to test `common`/`consumers`; use a narrower one only for the tenant checks of section 7). Redirect URI: platform **Web**, `https://<public host>/v1/idtest/_auth/oauth/microsoft/callback`.
2. *Certificates & secrets* → new client secret (note its **value** now). *Token configuration*: add the optional claim `email` to the **ID** token.
3. Note the **Application (client) ID** and the **Directory (tenant) ID**.
4. For the native checks: add a second platform (*Mobile and desktop applications*) or a second registration; its id goes in `native_client_ids`.

### Apple

1. [Developer account](https://developer.apple.com/account) → *Certificates, Identifiers & Profiles* → **Identifiers** → an **App ID** with the *Sign in with Apple* capability (this is the iOS app's bundle id).
2. A **Services ID** (this is the web client id, `services_id`): enable *Sign in with Apple* → *Configure*: primary App ID as above, **Domains** `<public host>` and **Return URLs** `https://<public host>/v1/idtest/_auth/oauth/apple/callback`.
3. **Keys** → a new key with *Sign in with Apple* enabled for the App ID → download `AuthKey_<KEYID>.p8` **once**. Note the **Key ID** and your **Team ID**.
4. Apple's private email relay needs *Sign in with Apple for Email Communication*: register the domain/address you send mail from, or relay addresses will not receive mail (the check in 3.3 does not need mail).

Start backd with the realm and check the log: one `sign-in provider` line per provider with its `callback_url`;
a `provider's secrets are not set` warning for any secret you have not stored.

## 3. The redirect flow, provider by provider

Run section 3 once per provider. In the app, click "Continue with <provider>" (or call `backd.auth.signInWith`).
Use a **new** account each time unless the step says otherwise. After each step look at the app, the audit and `backd user identities`.

| # | Do | Expect |
|---|---|---|
| 3.1 | Sign in with an account backd has never seen | The provider's own sign-in and consent page; back at the app with a session. `completeSignIn()` resolved with `new_user: true`. `backd audit` shows `user.signup` with `provider`, and `identity.linked`. `GET /_auth/me`: one identity (the provider), its `email` the one you used |
| 3.2 | Look at the user | **Google:** `email_verified: true` on the user. **Apple:** `email_verified: true` (also for a relay address `…@privaterelay.appleid.com` when you chose *Hide my email*). **Microsoft:** `email_verified: false` on the user and on the identity |
| 3.3 | Apple only: choose **Hide My Email** on the consent sheet | The user's address is the relay address; sign-in works; the name is empty or as you typed it (see 11.1) |
| 3.4 | Sign out of backd, sign in again with the same account | The same user (same `id`), `new_user: false`, no `profile`, the identity's `last_used_at` moved |
| 3.5 | Change the display name or picture at the provider, sign in again | Nothing changes in backd; `new_user: false` |
| 3.6 | Press **Cancel** / **Deny** on the provider's consent page | Back at the app with `?error=cancelled`; `completeSignIn()` rejects with an `AuthenticationError`, `code: "cancelled"`; no user was created |
| 3.7 | Start a sign-in, then wait more than 10 minutes on the provider's page, then finish it | The browser lands on the **oauth-error page** (HTTP 400, "Sign-in didn't complete") because the `state` expired; nothing was created |
| 3.8 | Finish a sign-in, then press **Back** in the browser and open the callback URL again (or replay it with `curl`) | The oauth-error page again: a `state` works once |
| 3.9 | Open the callback with a made-up `state` | The oauth-error page; HTTP 400 |
| 3.10 | In `realm.yaml` use a wrong client secret (or stop the secret with `backd secret delete`), then try | With a wrong secret: back at the app with `?error=provider_error`, the log has "the provider didn't give a token". With the secret deleted: the **start** answers `503 provider_unavailable` before the user leaves the app (secrets are cached for up to a minute per instance) |
| 3.11 | Register a wrong redirect URI at the provider, then try | The **provider** shows its own error page (`redirect_uri_mismatch`, `AADSTS50011`, `invalid_request`); backd never sees the callback. This is the most common setup mistake: compare with the `callback_url` in the log |
| 3.12 | `curl -i "$BACKD_URL/v1/idtest/_auth/oauth/google/start?redirect_to=https://evil.example/&code_challenge=$(printf x | openssl dgst -sha256 -binary | basenc --base64url | tr -d =)"` | `400 invalid_redirect`; nothing is stored (`oauth_states` count unchanged) |
| 3.13 | Same, without `code_challenge` | `400 validation_error` naming `code_challenge` |

**Apple specifics.** Apple answers with an HTTP **POST** (a form) to the callback and redirects the browser
with `303`: if the browser ends on the app with `?code=`, that works; if it ends on a blank page or an HTTP 405,
the callback route is not reachable for POST (check the reverse proxy). Apple also only sends the user's
**name** the first time a person authorizes the app (see 11.1).

**Microsoft specifics.** If the sign-in fails with `provider_error` for a work account, read the log line
"the provider's ID token is not valid": the reason is one of `issuer` (a tenant outside `tenant:`), `audience`,
`nonce`, `expired`, `no tenant or object id` (add the `email`/`oid` claims in token configuration).

## 4. Who the person is: linking and sign-up

Prepare two backd users with passwords (`backd user create`), `ada@…` whose address **is** a Google account you control and is **verified** (`backd user verify-email`), and `vic@…` whose address is another Google account you control but is **unverified** (sign up with the API, do not verify).

| # | Do | Expect |
|---|---|---|
| 4.1 | Sign in with **Google** as the account whose address is `ada@…` | Signed in as the **existing** ada (`user.id` unchanged); `new_user: false`; `identity.linked` in the audit; `/me` lists password **and** google. This is the automatic linking |
| 4.2 | Sign in with **Google** as the account of `vic@…` (the unverified backd user) | `?error=account_exists&provider=google`; **no identity was added** to `vic` (`backd user identities`). This is the pre-hijacking guarantee. Then verify `vic`'s address (`backd user verify-email`) and try again: it links |
| 4.3 | Sign in with **Microsoft** using an account whose address equals an existing verified backd user's | `?error=account_exists&provider=microsoft` (Microsoft never links by address) |
| 4.4 | Same with **Apple** (a real address, not the relay) | Links, like Google, when the backd user is verified |
| 4.5 | While signed in as a password user, link a provider (`backd.auth.linkProvider('google')`) | After the round trip `completeSignIn()` resolves `{ linked: 'google' }`; `/me` has the identity; the identity's address may differ from the user's; **the user's address is unchanged** |
| 4.6 | Sign in with the linked Google account | The same user as in 4.5 |
| 4.7 | While signed in as **another** backd user, try to link the Google account of 4.5 | `?error=link_conflict` |
| 4.8 | While signed in as the user of 4.5, link a **second** Google account | `?error=link_conflict` (one identity per provider) |
| 4.9 | `DELETE /_auth/identities/google` (`backd.auth.unlinkProvider`) as that user | `204`; `/me` no longer lists it; audit `identity.unlinked` with `by: self`. Signing in with that Google account now finds no identity: it is a new sign-in under the rules (it links again by address if they allow it, else `account_exists`, else a new user) |
| 4.10 | For a user with a password and Google: remove Google (`204`), then try to remove the password | `409 last_sign_in_method`; the user can always sign in. Repeat the other way round with a user who signed up with Google and later got a password (password reset): remove the password, then try Google: the same `409` |
| 4.11 | `signup: closed`, then sign in with a never-seen account | `?error=signup_closed`; no user |
| 4.12 | `signup: invite`: sign in with a new account (a) without an invitation, (b) with an invitation (`backd` admin API / `signInWith('google', {invitation})`) bound to **another** address, (c) with an open invitation, (d) the same invitation again | (a) `invitation_invalid`; (b) `invitation_invalid` and the invitation **still works** for its own address; (c) signed in, the invitation is used up; (d) `invitation_invalid` |
| 4.13 | `account.require_verified_email: true` (needs `email:`), sign in with Microsoft (unverified) | `?error=email_not_verified`; the user was created. Verify the address by the email flow, sign in again: it works |
| 4.14 | Create a Microsoft/Google account with **no email** (a Microsoft account with only a phone number, or an Entra user with no mail attribute) | `?error=email_required`; no user. (Known limitation: accounts need an address) |
| 4.15 | `backd user disable` a linked user, then sign in with their provider | `?error=signin_refused`; the audit has `identity.signin_refused` with `reason: disabled`. Enable them again: it works |
| 4.16 | Give the user `login_networks` that excludes your address, sign in | `?error=signin_refused`, `reason: network` |

## 5. The one-time code and the PKCE verifier

Use the browser's network tab or `curl` to hold a code (the app's `?code=` in the address bar before `completeSignIn()` removes it; stop the app or block the request).

| # | Do | Expect |
|---|---|---|
| 5.1 | `POST /v1/idtest/_auth/oauth/token` with the code and a **wrong** verifier | `401 invalid_credentials` |
| 5.2 | Then with the right verifier | `401` again: a wrong verifier **burns** the code |
| 5.3 | A fresh code, redeemed twice with the right verifier | The second is `401` |
| 5.4 | A fresh code, wait 70 seconds, redeem | `401` |
| 5.5 | Tamper with the `redirect_to` in the callback URL | Ignored: backd uses the one stored with the attempt; the redirect goes where `start` said |
| 5.6 | Start with an `intent=link` in a `GET` | `400` (linking needs `POST` with the session) |
| 5.7 | `POST …/start` with `"intent": "link"` and no session | `401` |
| 5.8 | `{"cookie": true}` on `POST /oauth/token` from the app's origin, in a realm with `sessions.cookie.enabled` | The answer has no `token`; an `HttpOnly` cookie is set; `GET /_auth/me` works with the cookie. From another origin: `403` |

## 6. Rate limits and the multi-instance case

| # | Do | Expect |
|---|---|---|
| 6.1 | 31 `GET …/start` calls from one address within a minute (`for i in $(seq 31); do curl -s -o /dev/null -w '%{http_code}\n' …; done`) | `302` ×30 then `429` with `Retry-After`. Same for `POST /oauth/token` and `…/id-token`, each with its own count |
| 6.2 | Two backd instances behind one load balancer with **no** stickiness: start a sign-in (it lands on A), let the callback land on B | The sign-in completes: the `state` lives in MongoDB. Repeat 10 times |

## 7. Microsoft tenants

Change `tenant:` in `realm.yaml` (restart) and try with each account kind. "Accepted" means the flow completes;
"rejected" means `?error=provider_error` and the log says `issuer`.

| `tenant:` | Work/school account of your tenant | Work account of another tenant | Personal account |
|---|---|---|---|
| `common` | accepted | accepted | accepted |
| `organizations` | accepted | accepted | rejected |
| `consumers` | rejected | rejected | accepted |
| `<your tenant id>` | accepted | rejected | rejected |

Registering the app as *single tenant* in Entra rules out `common`, `organizations` and `consumers` at Microsoft's side (`AADSTS…` errors), whatever `tenant:` says: register it as multi-tenant plus personal accounts to run this table.

Also: the same person (same work account) in **two** tenants (a guest) is two identities with different
subjects, and the second one cannot be linked to a user that has a Microsoft identity already (`link_conflict`). The
identity `subject` in MongoDB is `<tid>:<oid>` (`db.identities.find({provider: "microsoft"})`).

## 8. The error page

| # | Do | Expect |
|---|---|---|
| 8.1 | Trigger 3.7 with `Accept-Language: es` and a realm that lists `es` in `email.locales` and has `pages/oauth-error/es.html` | The Spanish page. Without that file: the built-in English page |
| 8.2 | Look at the headers (`curl -i`) | `Cache-Control: no-store`, `Referrer-Policy: no-referrer`, a strict `Content-Security-Policy` with `frame-ancestors 'none'`, `X-Content-Type-Options: nosniff` |

## 9. Native sign-in with ID tokens

You need an ID token and the nonce it was made with. Real ones come from the platform SDKs of an app; for a
check without an app use a page with the provider's JavaScript SDK (Google Identity Services with a `nonce`,
Apple's `AppleID.auth.signIn({nonce, usePopup: true})`, MSAL.js) and print the token. The nonce is any random string you
choose; keep it.

```sh
curl -s -X POST "$BACKD_URL/v1/idtest/_auth/oauth/google/id-token" -H 'Content-Type: application/json' \
  -d "{\"id_token\": \"$ID_TOKEN\", \"nonce\": \"$NONCE\"}" | jq
```

| # | Do | Expect |
|---|---|---|
| 9.1 | A valid Google token whose audience is a `native_client_ids` entry | `200` with a session, `new_user`, `profile` (Google puts name and picture in the token) |
| 9.2 | The same call again | `200`, the same user, `new_user: false` |
| 9.3 | The SDK put the nonce **hashed** in the token (Apple's and Google's native SDKs do; the web ones may not) and you send the raw nonce | `200`. Both forms are accepted |
| 9.4 | Send another `nonce` | `401 invalid_token` |
| 9.5 | A token for another application (another client id) | `401 invalid_token` |
| 9.6 | An expired token (wait an hour) | `401 invalid_token` |
| 9.7 | Change one character of the token | `401 invalid_token` |
| 9.8 | Apple with the iOS app: send `authorization_code` too | `200`. Without it: `400` (unless `revoke_on_delete: false`). With another person's code: `401` |
| 9.9 | Microsoft with MSAL (a personal or work account) | `200`; the user is unverified |
| 9.10 | `"intent": "link"` with the session of a signed-in user | `200 {"linked": "<provider>"}`; the same account for another user: `409 link_conflict` |

## 10. Apple: the refresh token and revocation

Prerequisite: `revoke_on_delete` is not set (default `true`), a worker runs.

| # | Do | Expect |
|---|---|---|
| 10.1 | Sign in with Apple (web, then native with the authorization code) | `db.getSiblingDB("idtest___system").identities.findOne({provider: "apple"})` has `apple_refresh_token` as an opaque `<keyid>.<nonce>.<ciphertext>` string and `apple_client_id`. `GET /_auth/me` and the admin identities route **never** show it |
| 10.2 | On an iPhone: *Settings → [your name] → Sign-In & Security → Sign in with Apple*: the app is listed | It is listed |
| 10.3 | `backd user delete --email … --yes` for that user (an erase) | A job with `origin: backd:apple.revoke` appears (`backd functions jobs --realm idtest`) and ends `ok` within a minute; `backd audit --action identity.apple_revoked` has one record for the user (no token in it). **The app has disappeared** from the Apple ID list of 10.2 |
| 10.4 | Repeat with **unlinking**: link Apple to a password user, then `DELETE /_auth/identities/apple` | The same revoke job and audit; the app disappears from the list |
| 10.5 | Break the Apple key secret (`backd secret set … APPLE_SIGN_IN_KEY` with a wrong key), erase a second Apple user | The job fails and is **retried** (see `backd functions jobs --realm idtest`; the log says "it will be tried again"). Put the right key back: the next attempt revokes it. Within eight attempts (the waits double from one minute, so about two hours in all) the job fails visibly and keeps the sealed token until it expires |
| 10.6 | `revoke_on_delete: false` and restart; sign in with Apple; erase | `apple_refresh_token` is **not** stored; no revoke job; the native call does not need `authorization_code` |
| 10.7 | Rotate `BACKD_SECRETS_KEY` (`backd secret rotate-key`) after 10.1 | The stored Apple token cannot be opened any more: the revoke job fails with a message about the key (documented limitation: re-sign-in refreshes the stored token) |

## 11. The profile and `account.on_signup`

| # | Do | Expect |
|---|---|---|
| 11.1 | Apple, first authorization (a person who never used the app): in the Apple sheet type a first and last name | The redirect-flow answer's `profile` has `name`, `given_name`, `family_name` (the name came with the callback form, not the token); `account.on_signup` received it |
| 11.2 | Sign out, sign in again | No name now (`new_user: false`). To get the first-authorization behaviour again: iPhone → Sign in with Apple → your app → *Stop using Sign in with Apple*, then use a **new** backd user (the old one stays) |
| 11.3 | Google/Microsoft first sign-in | `profile` has `name`, `given_name`, `family_name`, `picture` as the provider's token has them (Microsoft: depends on the claims you configured) |
| 11.4 | The hook ran for each sign-up kind | A `profiles` row (or whatever your function writes) exists for: a password sign-up, an emailed-invitation sign-up, and each provider sign-up. Not for `backd user create` |
| 11.5 | Read the function's **logs** (`backd functions logs`, or the admin UI) | The profile's values appear as `***`, not in clear |
| 11.6 | Read the hook's job after it finished (`db.getSiblingDB("idtest___system").jobs.find({origin: "backd:account.on_signup"})`) | The input is empty (`{}`): the profile is not kept |
| 11.7 | Make the hook function throw | The sign-up still succeeded; the job is retried per its `retry:`; after the attempts it is listed as failed; the user exists and can sign in |
| 11.8 | `mongosh`: search the realm's system database for the user's name | In `jobs` while the hook waits for a retry, and nowhere else (`users`, `identities`, `audit`, `oauth_codes`, `oauth_states` do not hold it) |

## 12. The JavaScript client and the example app

Serve the blog example with `PROVIDERS = ['google', 'microsoft', 'apple']` in its `app.js` and the redirect URL
in `sign_in.allowed_redirects`.

| # | Do | Expect |
|---|---|---|
| 12.1 | Click each "Continue with …" | The flow of section 3; the page returns signed in without a reload loop; the address bar shows no `?code=` |
| 12.2 | Cause 4.2 (`account_exists`) from the page | The friendly message of the example appears; the console shows a `ConflictError` with `code: "account_exists"` and `details[0].reason: "google"` |
| 12.3 | Reload the page in the middle of the provider's page (before returning), then continue | The sign-in completes (the verifier is in `sessionStorage`). In a **new tab** it cannot: `completeSignIn()` rejects with `code: "sign_in_not_started"` |
| 12.4 | `createClient({ cookies: true })` against a realm with cookie sessions | The sign-in works and `localStorage` holds no token |
| 12.5 | `unlinkProvider('password')` on a user with only a password | `ConflictError`, `code: "last_sign_in_method"` |
| 12.6 | A React Native / Capacitor app, if you have one: `signInWith(provider, { redirectTo: 'idtest://signed-in', navigate: openBrowser })`, then `completeSignIn(url)` with the URL the app was opened with | Works; `idtest://` is in `allowed_redirects` |

## 13. The administrator's side

| # | Do | Expect |
|---|---|---|
| 13.1 | `backd user identities --email …` for a user with a password, Google and Apple | Three rows: provider, address, verified, linked, last used (`-` before a use is recorded) |
| 13.2 | `backd user unlink-identity --email … --provider google` | `…can no longer sign in with google`; audit `identity.unlinked` with `by: admin` and the administrator as the actor |
| 13.3 | The same in the admin UI (*Users → the user → Ways to sign in → Remove*) | Asks to type the method's name; the table refreshes; removing the last is refused with the server's message |
| 13.4 | A read-only administrator (`admin: read` with `admin.read_access.users`) | Sees the table, has no **Remove** button, and `DELETE` answers `403` |
| 13.5 | `backd audit --action identity.signin_refused` after 4.15 and 4.16 | The records with their `reason` |

## 14. Clean up

- **Users:** erase every test user (`backd user delete --email … --yes`), which also revokes the Apple tokens; check `identities` and `jobs` for leftovers.
- **Secrets:** `backd secret delete --realm idtest --name …` for the three secrets, then **rotate** them at the providers (or delete the client secrets/keys), since they passed through your shell.
- **Providers:** remove the test accounts' grants (Google: [myaccount.google.com/permissions](https://myaccount.google.com/permissions); Microsoft: [myapps.microsoft.com](https://myapps.microsoft.com); Apple: *Sign in with Apple* list), delete the test OAuth clients, Services ID and key if you will not reuse them.
- **Tunnel/host:** shut it down; remove the redirect URIs from the provider consoles.

## 15. Recording the result

Add a section to `support_docs/release-smoke-tests.md` (local, never committed) per run, in this shape, and
remember to fix the design or the code if anything differed from the "Expect" column:

```
## Provider sign-in (design/identity-providers-testing.md), run YYYY-MM-DD, backd vX.Y.Z

Providers: Google ✓ / Microsoft ✓ / Apple ✓   Native: Google ✓ / Apple ✓ / Microsoft –
Sections: 3 ✓  4 ✓  5 ✓  6 ✓  7 ✓ (tenants: common, organizations, consumers, specific)  8 ✓  9 ✓  10 ✓  11 ✓  12 ✓  13 ✓
Differences found: <step> – <what happened> – <issue/commit>
Not run: <step> – <why>
```

A release that changes sign-in with providers is not tagged until sections 3, 4 and 10 pass for the provider
the change touches, and the whole guide has passed once since the last such release.
