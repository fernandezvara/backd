---
title: "JavaScript client"
description: "backd-js: auth, sessions, collections and the admin API from browsers, Node and edge runtimes."
icon: "file-js"
weight: 610
toc: true
---

`backd-js` is a small JavaScript library for `backd`. It is plain ES modules with no dependencies and no build step, and it runs wherever `fetch` does: browsers, Node 20+, Deno, Bun and edge runtimes. It ships TypeScript declarations, so editors autocomplete it in JavaScript and TypeScript projects alike.

## Install

```sh
npm install backd-js
```

It has no dependencies, needs no build step and works with any bundler. Without one, import it in a browser with an [import map](https://developer.mozilla.org/docs/Web/HTML/Element/script/type/importmap) pointing at the package's `src/index.js` (from a CDN such as `https://cdn.jsdelivr.net/npm/backd-js/src/index.js`, or from your own server), as the [example app](#example-app) does.

The client is released on the same version as the server (`backd-js` 0.9.4 goes with `backd` v0.9.4). Versions of `backd-js` before 0.2 were an unrelated, older library.

## Example app

The repository includes two example apps built with this client and [Alpine.js](https://alpinejs.dev/), with no build step:

- the [blog](../../examples/blog/): sign-up, public posts and private drafts, editing with `ifMatch`, and filters;
- the [expenses example](../../examples/expenses/): data shared between users, and the limits of access rules; it requires a verified email and offers "Forgot your password?";
- the [workshop tour](../../examples/workshop-tour/), which also walks the email flows (verification, reset, change of address, emailed invitations).

{{< live-example >}}

`make dev-examples` serves both at <https://localhost:8443/example/>.

## Create a client

```js
import { createClient } from 'backd-js'

const backd = createClient({
  url: 'https://localhost:8443',
  realm: 'blog',
})
```

| Option | Default | Meaning |
|---|---|---|
| `url` | required | Base URL of `backd` |
| `realm` | required | The realm to talk to |
| `storage` | memory | Where the session token is kept (see [Token storage](#token-storage)) |
| `cookies` | `false` | Browser apps: keep the session in an HttpOnly cookie, out of scripts' reach (see [Session cookies](#session-cookies)) |
| `retry` | off | `{ attempts, maxDelayMs }` for `429` and `503` answers (see [Retries](#retries)) |
| `headers` | none | Extra headers for every request |
| `fetch` | global `fetch` | A custom `fetch` implementation |
| `apiKey` | none | Server-side only: an [API key](../../auth/api-keys/). See [API keys](#api-keys-and-the-admin-api) |
| `dangerouslyAllowBrowser` | `false` | Allow `apiKey` in a browser, where every visitor can read it. Only for internal tools |

A web app on another origin needs that origin listed in the realm's [CORS settings](../../configuration/realm/#cors).

## Sign-up, login and sessions

```js
const session = await backd.auth.signup({ email: 'ada@example.com', password: 'dev-p4ssw0rd!', locale: navigator.language })
// or: await backd.auth.login({ email, password })

const me = await backd.auth.me()             // { id, email, email_verified, roles, locale, created_at, identities, reauth_methods }
await backd.auth.updateMe({ locale: 'es' })  // must be a language the realm lists, else a ValidationError (invalid_locale)
await backd.auth.reauth({ method: 'password', password })         // a fresh proof, before sensitive actions (below)
await backd.auth.changePassword({ newPassword })                   // other sessions end
const sessions = await backd.auth.sessions() // [{ id, created_at, last_used_at, expires_at, current }]
await backd.auth.revokeSession(sessions[1].id)
await backd.auth.logout()                    // this session
await backd.auth.logoutAll()                 // every session of the user
await backd.auth.deleteAccount()             // needs a fresh proof; deactivates the account, all data is kept
```

- `signup` and `login` store the session token; every later request sends it as `Authorization: Bearer …`.
- In realms with `signup: invite`, pass the invitation: `signup({ email, password, invitation })`.
- In a realm that [requires verified addresses](../../auth/sessions/#email-verification), `signup` creates the account and then rejects with a `VerificationRequiredError`: there is no session yet, and the user logs in after following the link in the email. A `login` before that rejects with a `ForbiddenError` whose `code` is `email_not_verified`. `redirectTo` names where the page after the link may send the user (it must be within the realm's `email.allowed_redirects`).
- `logout` always removes the stored token, even if the server can't be reached or already forgot the session.
- **Sensitive actions need a fresh proof.** `changePassword`, `requestEmailChange`, `deleteAccount`, `unlinkProvider` and (for an administrator signed in with a session) `admin.apiKeys.create` answer `401 reauth_required` without one. `auth.hasFreshProof()` tells whether the client holds one that is still good. `auth.reauth({ method, … })` takes the strongest proof the user has (`me.reauth_methods`: `totp` or `recovery_code` with a second factor, `password`, or `email_code` after `requestReauthCode()`), and the client sends the token it gave with those requests for the realm's `reauth.ttl` (5 minutes by default): one prompt covers a settings page. See [Re-authentication](../../auth/reauth/).

The endpoints behind these calls, and their rules, are described in [Sign-up, login and sessions](../../auth/sessions/).

### Two-step sign-in and magic links

A user with a [second factor](../../auth/mfa/) doesn't get a session from their password alone. `signIn()` returns the step as a value; `login()` and `signup()`, which promise a session, reject with a `SecondStepRequiredError` carrying the `challenge`.

```js
const result = await backd.auth.signIn({ email, password })
if (result.mfaRequired) {
  // result.methods: ['totp', 'recovery_code']
  const session = await backd.auth.verifyMfa(result.challenge, 'totp', code)   // stores the session
} else if (result.enrollmentRequired) {
  // the user's role requires a second factor and they have none: set one up, no session yet
  const setup = await backd.auth.mfa.setup({ challenge: result.challenge })    // { secret, otpauth_uri, qr (a PNG data URI) }
  const done = await backd.auth.mfa.confirm({ challenge: result.challenge, setupId: setup.setup_id, code })
  // done.recovery_codes (shown once) and done.session
} else {
  // result is the session
}
```

The same steps come back from `completeSignIn()` / `auth.ready` after a provider or a magic link, and from `signInWithIdToken()`.

```js
// A signed-in user's own second factor; changes need a fresh proof (see below).
await backd.auth.mfa.status()                    // { enabled, methods, recovery_codes_left, required }
const setup = await backd.auth.mfa.setup()       // show setup.qr, or the secret
const { recovery_codes } = await backd.auth.mfa.confirm({ setupId: setup.setup_id, code })
await backd.auth.mfa.regenerateRecoveryCodes()
await backd.auth.mfa.disable()                   // not for a role that requires it

// The proof, asked for only when the server wants it, then the action once more:
await backd.auth.withProof(async (methods) => ({ method: 'password', password: await ask() }),
  () => backd.auth.mfa.disable())

// A link and a code by email (the realm needs magic_link); no return route is needed.
await backd.auth.magicLink.request('ada@example.com')   // the link comes back to this page
const result = await backd.auth.magicLink.verifyCode('123456')   // or type the code here

await backd.auth.password.set({ newPassword })    // an account that signs in another way may have none
await backd.auth.password.remove()                // 409 last_sign_in_method when nothing else is left
```

Errors: `invalid_code` (a `ValidationError`, `details` has the attempts left), `invalid_challenge`, `mfa_locked` (a `RetryableError`, 429), `reauth_required` and, for a link or code, `invalid_magic_link`. A link opened on another device reports `wrong_device`, as for a provider. See [Multi-factor authentication](../../auth/mfa/), [Magic links](../../auth/magic-links/) and [Re-authentication](../../auth/reauth/).

### Account flows by email

In a realm with [`email`](../../functions/email/), the links in the emails open pages of `backd` that need no code. An app that wants **its own pages** points the links at them (`email.links` in `realm.yaml`) and posts the token from the link with these calls, none of which needs a session or starts one:

```js
await backd.auth.resendVerification({ email })                  // 202 whatever the address is
await backd.auth.verifyEmail(token)                             // the token of the verification link
await backd.auth.requestPasswordReset({ email, redirectTo })    // 202 whatever the address is
await backd.auth.resetPassword({ token, password })             // ends every session, verifies the address
await backd.auth.updatePassword({ email, password, newPassword, code })  // signed out, with the current one; ends every session
await backd.auth.requestEmailChange({ newEmail })               // signed in; needs a fresh proof (reauth)
await backd.auth.confirmEmailChange(token)                      // the link sent to the new address
await backd.auth.revertEmailChange(token)                       // the link sent to the old one
await backd.auth.acceptInvitation({ token, password, locale })  // an emailed invitation
```

When an administrator [expires passwords](../../auth/users/#expiring-every-password), signing in with the old one throws a `PasswordExpiredError`. Tell the person what happened and what to do next:

```js
try {
  await backd.auth.login({ email, password })
} catch (e) {
  if (e instanceof PasswordExpiredError) {
    // e.resetEmailQueued: backd already emailed the link to choose a new password
    if (!e.resetEmailQueued) await backd.auth.requestPasswordReset({ email })  // the same as "Forgot password"
    showMessage('Your password was reset by an administrator: check your email for the link to choose a new one.')
  } else throw e
}
```

- A token that is expired, used or unknown rejects with a `ValidationError` whose `code` is `invalid_token`; a password the policy refuses rejects with a `ValidationError` naming `password` and leaves the token usable. A reset request past the realm's limits rejects with a `RetryableError`.
- `redirectTo` (here and in `signup`) must be within the realm's `email.allowed_redirects`: it is where the page after the link may send the user.
- After `resetPassword`, `confirmEmailChange`, `revertEmailChange` and `acceptInvitation` the user logs in: none of them starts a session, and the first three end the sessions that exist.

The rules behind each flow are in [Sign-up, login and sessions](../../auth/sessions/).

### Sign in with Google, Microsoft or Apple

In a realm that lists [providers](../../auth/providers/), signing in is one call on the page with the buttons. backd sends the user back to a page of your app (the current one by default, so no return route is needed), and the client finishes the sign-in when it is created there.

```js
// A "Continue with Google" button: PKCE is handled, the browser goes to the provider.
await backd.auth.signInWith('google', { redirectTo: 'https://app.acme.example/signed-in' })

// On the page the user comes back to, whenever the app starts: `ready` is the outcome of the
// sign-in the address carried, or null on an ordinary visit.
try {
  const session = await backd.auth.ready
  if (session?.new_user) await createProfile(session.profile)   // { name, given_name, family_name, picture } as the provider gave them
} catch (err) {
  if (err.code === 'account_exists') showLinkHelp(err.details[0].reason)   // the provider
}
```

- The client reads the `backd_code` (or `backd_error`, `backd_linked`) that backd added to the address, removes it from the address bar and trades the one-time code for a session (stored like a login's) and resolves with it, plus `new_user` and, for a new user, the `profile`. Use [`account.on_signup`](../../auth/providers/#the-profile-and-the-sign-up-hook) to create the profile document on the server instead of depending on the page. `completeOnLoad: false` in `createClient` turns this off, and `completeSignIn()` does the same on demand.
- A page opened where the sign-in was not started (the link was opened on another device or in another tab) has no verifier to redeem the code with: `ready` rejects with a `ValidationError`, `code: "wrong_device"`; show "open this on the device where you asked".
- Failures reject with the error the code means: `account_exists` and `link_conflict` are a `ConflictError`; `signup_closed`, `email_not_verified` and `signin_refused` a `ForbiddenError`; `not_allowed` a `NotAllowedError`; `cancelled` an `AuthenticationError`; `invitation_invalid` and `email_required` a `ValidationError`; `provider_error` a `BackdError`. `code` is the code.
- The PKCE verifier waits in the tab's `sessionStorage` between the two pages (`oauthStorage` in `createClient` replaces it). `redirectTo` is the current page unless given; the realm's `sign_in.app_url` takes over only when the client sends none. `navigate(url)` in the options replaces `location.assign` for apps that open a browser session of their own (React Native, Capacitor); `redirectTo` may be an app scheme (`acme://signed-in`) there, and `completeSignIn(url)` takes the address the app was opened with.
- `signInWith(provider, { invitation, locale })` passes an invitation for realms with `signup: invite`.
- **Which buttons to show:** `await backd.auth.providers()` lists the providers the realm has configured *and* that work now (`['google', 'microsoft']`), the check a sign-in start makes. Nothing but names comes back.

#### Through an organization's own Google or Microsoft

A company can sign its people in with its own Google Workspace or Microsoft tenant (see [Sign-in connections](../../organizations/api/#sign-in-connections)). The same calls take `org`, the organization's id or slug:

```js
const names = await backd.auth.providers({ org: 'acme-corp' })     // ['google'], the connections usable now ([] for an unknown organization too)
await backd.auth.signInWith('google', { org: 'acme-corp' })        // the same flow, at the organization's address
await backd.auth.signInWithIdToken('google', { idToken, nonce, org: 'acme-corp' })   // a native app
await backd.auth.linkProvider('google', { org: 'acme-corp' })     // a person who already has an account links the connection, and joins
await backd.auth.unlinkProvider('google', { org: 'acme-corp' })   // identities through an organization are separate from the realm's
```

A person the connection does not admit (another Workspace domain or tenant, or an address outside its `email_domains`) gets a **`NotAllowedError`** (a `ForbiddenError`, `code: "not_allowed"`) from `ready`, `completeSignIn()` and `signInWithIdToken()`, and nothing is created. An organization sign-in never links to an existing account by address: that is `account_exists`, and the person links the connection from a session they already have.

Its managers keep the connections with `backd.orgs.sso(orgId)`:

```js
const sso = backd.orgs.sso(org.id)
const { items, providers, max_connections } = await sso.list()
await backd.auth.withProof(() => sso.set('google', { clientId, clientSecret, hostedDomain: 'acme.example', joinRoles: ['member'] }))
// items[n].redirect_uri is the address to register with the provider; the secret is never returned (client_secret_set says it is stored)
await backd.auth.withProof(() => sso.delete('google'))
```

`list()` and `get(provider)` need `manage: sso`; `set` and `delete` also a fresh proof. Leave `clientSecret` out of a `set` to keep the stored one. Administrators use `backd.admin.orgs.sso(id)`, `setSso(id, provider, input)` and `deleteSso(id, provider)`.

For a signed-in user:

```js
await backd.auth.linkProvider('apple', { redirectTo })   // the same flow; completeSignIn() resolves { linked: 'apple' }
const me = await backd.auth.me()                          // me.identities: [{ provider, email, email_verified, created_at, last_used_at }]
await backd.auth.unlinkProvider('apple')                  // never the last way to sign in: ConflictError, code 'last_sign_in_method'
```

An app that signs in with the platform (Sign in with Apple, Google Sign-In, MSAL) already holds the ID token and uses it directly:

```js
const session = await backd.auth.signInWithIdToken('apple', { idToken, nonce, authorizationCode })
await backd.auth.linkProviderWithIdToken('google', { idToken, nonce })   // for a signed-in user
```

`nonce` is the raw value the sign-in was started with. An invalid token rejects with an `AuthenticationError` (`invalid_token`). Apple's `authorizationCode` is required unless the provider sets `revoke_on_delete: false`.

### Working in an organization

An organization's people sign in to *it*, and the session they get is **scoped to it**: its token reaches that organization and no other. Bind the client to one, by its id or by the slug people know:

```js
const backd = createClient({ url, realm: 'shelf', org: 'acme-corp' })   // 'acme-corp' from the URL, or a "company" field
const { providers, methods } = await backd.auth.options()          // what Acme's sign-in page can offer
await backd.auth.login({ email, password })                        // POST /_auth/orgs/acme-corp/login
const session = backd.auth.current                                 // session.org: the organization's id
await backd.auth.org()                                             // { id, name, slug, roles, teams }
```

`login`, `signup`, `magicLink.request`, `requestPasswordReset`, `signInWith` and the other sign-in calls go to the organization, and every one takes `org` to name another. A wrong password, an address that is not a member and an organization that is not there all reject alike, with `invalid_credentials`. An invitation is used as part of the sign-in: `login({ email, password, invitation })`, `signup({ …, invitation })` (the invitation is required there) and `signInWith(provider, { invitation })`; `auth.previewInvitation(token)` says what it offers before the person signs in.

An app that works in several organizations keeps one client per organization, each with a token of its own:

```js
const acme = backd.org('acme-corp')       // the same client for the same organization
const beta = backd.org('beta')
await acme.auth.login({ email, password })
await beta.auth.login({ email, password })
await acme.auth.logout()                  // beta stays signed in
await backd.orgs.signedIn()               // [{ ref: 'beta', id, expiresAt, name }]: what a switcher lists
```

`client.org` needs a storage that can keep a token per organization: `memoryStorage()` and `localStorageStorage()` can (a custom one adds `slot(name)` and `slots()`). With `cookies: true` the browser holds one session at a time, so an app that works in several organizations uses tokens. The API never lists the organizations a person is in; the browser remembers the ones it signed in to.

A new person starts a company with `auth.signUpWithOrg({ email, password, name, slug })` (the account, the organization and a session in one call), `orgs.create(...)` opens a session for the organization it made, and `auth.findOrgs({ email })` emails an address the companies it belongs to for someone who does not remember the slug. See [Signing in to an organization](../../organizations/api/#signing-in-to-an-organization).

### Auth events

```js
const stop = backd.auth.onAuthChange((event, session) => {
  // 'SIGNED_IN' (session set), 'SIGNED_OUT' or 'SESSION_EXPIRED' (session null)
})
stop() // stop listening
```

`SESSION_EXPIRED` fires when the server refuses the stored token: it expired, was revoked (for example by a password change on another device), or the user was disabled. The client removes the token, so the app can send the user to the login screen. A failed login doesn't fire any event.

A listener that throws is reported with `console.error` and doesn't affect the client or other listeners.

## Collections

```js
const posts = backd.db('main').collection('posts')

const page = await posts.list({
  where: { published: true, title: { $ilike: 'go%' } },
  orderBy: '-_meta.created_at',   // or ['-_meta.created_at', 'title']
  limit: 20,
  skip: 0,
  count: true,
})
// { items: [...], limit: 20, skip: 0, has_more: true, next_cursor: '…', total: 42 }

// The next page: pass the cursor back, with the same where and orderBy
const next = await posts.list({ where: { published: true }, orderBy: '-_meta.created_at', limit: 20, after: page.next_cursor })

// Alternatives with $or (see Querying)
await posts.list({ where: { $or: [{ title: { $icontains: 'go' } }, { body: { $icontains: 'go' } }] } })

for await (const post of posts.iterate({ where: { published: true } })) {
  // every matching document; pages of 100 by default (set `limit` to change)
}

const post = await posts.get(id)
const created = await posts.create({ title: 'Hello', published: false })
await posts.patch(id, { title: 'Hello again', subtitle: null })  // null removes a field
await posts.replace(id, { title: 'Only this field now' })
await posts.delete(id)
```

- `where` and `orderBy` use `backd`'s [query language](../../api/querying/) as it is: the client only serializes them.
- Documents come back as the API returns them: your fields plus `id` and `_meta` (`created_at`, `updated_at`, `version`, and in realms with authentication `owner`, `created_by`, `updated_by`).
- What a user sees and may change is decided by the collection's [access rules](../../auth/rules/). A document they can't read answers `NotFoundError`; an operation they can't do answers `ForbiddenError`, or `AuthenticationError` when they aren't signed in.
- `iterate` follows each page's `next_cursor`, so documents created, changed or deleted while it runs never shift a page, and the end is as fast as the start. A cursor can't follow an `orderBy` on arrays, objects or fields of mixed types: `iterate` then fetches by offset (`skip`), where documents created or deleted meanwhile can be skipped or repeated. `after` can't be combined with `skip`; see [paging with a cursor](../../api/querying/#paging-with-a-cursor).

With TypeScript (or JSDoc), describe a collection's fields for typed documents:

```js
/** @typedef {{ title: string, published?: boolean }} Post */
/** @type {import('backd-js').Collection<Post>} */
const posts = backd.db('main').collection('posts')
```

### Revalidation cache

The client remembers the last body and `ETag` of each document and list it read, in memory, and sends them back as `If-None-Match` on the next read of the same URL. When nothing changed the server answers `304` without a body and the client returns the copy it holds: the query and the [rules](../../auth/rules/) still run for every read, so you never see more than the caller may read now, only less data on the wire.

```js
const post = await posts.get(id)       // 200, kept with its ETag
const again = await posts.get(id)      // sends If-None-Match; a 304 gives the same document
backd.cacheStats()                     // { hits: 1, misses: 1, entries: 1, bytes: … }

await posts.get(id, { cache: false })  // this read skips the cache
createClient({ url, realm, cache: false })                         // no cache at all
createClient({ url, realm, cache: { maxEntries: 50, maxBytes: 1e6 } })  // smaller bounds
```

- **Bounded:** 200 entries or 5 MB of bodies by default; the least recently used are dropped first.
- **Only document and list reads** are kept. Reads with `fileLinks` (their links expire) and answers marked `no-store` never are.
- **Per caller:** signing in, signing out or a different session or API key empties the cache, and so does `backd.clearCache()`. A successful write to a document drops its entry.
- **Functions** reach the API with the cache off.

For a client written by hand, or a mobile app, see [Revalidating a read](../../api/documents/#revalidating-a-read-if-none-match).

### Concurrent edits

Every write increases `_meta.version`. Pass the version you read as `ifMatch`, and the write fails with a `VersionMismatchError` if someone changed the document in between:

```js
import { VersionMismatchError } from 'backd-js'

const post = await posts.get(id)
try {
  await posts.patch(id, { title: 'Edited' }, { ifMatch: post._meta.version })
} catch (err) {
  if (err instanceof VersionMismatchError) reloadAndAskUser()
  else throw err
}
```

`ifMatch` also accepts `'*'` (the document must exist) or a raw header value. Writes with `ifMatch` are safe to [retry](#retries).

### The trash

In a collection that [soft-deletes](../../api/documents/#soft-delete), `delete` marks the document instead of removing it, and the trash has its own calls:

```js
await posts.delete(id)                                  // hidden from every read, restorable
const trash = await posts.list({ deleted: 'only' })     // 'include' for both; needs the restore rule
const gone = await posts.get(id, { deleted: 'only' })   // gone._meta.deleted_at, deleted_by, purge_at
const back = await posts.restore(id, { ifMatch: gone._meta.version })  // ConflictError if a live document took its unique value
await posts.delete(id, { purge: true })                 // for good, under the purge rule
```

### Expanding references

A collection that [declares references](../../configuration/config-dir/#references) can return the referenced documents next to the result. You ask for them with `expand`, on `get`, `list` and `iterate`; the client never adds it by itself:

```js
import { expand } from 'backd-js'

const page = await posts.list({
  expand: expand(['author', ['name', 'avatar']], 'tags'),   // "author(name,avatar),tags", or write the string
  limit: 20,
})
const post = page.items[0]
post._expanded?.author       // { id, name, avatar }, or null when missing or not readable by this user
post._expanded?.tags         // [{ id, ... }, null, ...] (positions kept), or { items, truncated: true }
```

- **`expand(...)`** takes names (`'author'`, `'author.company'` for a second level) and `[name, [fields]]` pairs, and checks their form; the server checks them against the config.
- **`null`** is the same for a document that doesn't exist, one the user can't read and one of another organization. In a typed client (below) `_expanded` is typed from the collection the reference leads to: `T | null`, the selected fields optional, and `truncated` for arrays.
- **Errors** are `InvalidExpandError` (`invalid_expand`: the `details` name the part) and `ExpandTooLargeError` (`expand_too_large`: ask for less). Both are `ValidationError`s. The [cost headers and limits](../../api/documents/#expanding-references) are the server's.
- **It isn't cached by the server**, and your [revalidation cache](#revalidation-cache) always asks again for an expanded read.
- **Functions** have it too: `ctx.db.collection('posts').get(id, { expand: 'author(name)' })`.

### Aggregations and search

`aggregate()` summarizes what the caller may read, with the fields the collection [declares](../../api/aggregations/); `search()` is a list sorted by relevance with the [text index](../../api/querying/#full-text-search):

```js
import { fillBuckets } from 'backd-js'

const { groups, total_groups } = await orders.aggregate({
  where: { status: { $in: ['paid', 'shipped'] } },
  groupBy: ['paid_at:month'], tz: 'Europe/Madrid',
  metrics: ['count', 'sum:total'], orderBy: 'paid_at:month',
})
groups[0].key['paid_at:month']      // '2026-09'
groups[0].sum_total                 // '18234.5' (an exact decimal string)

// A chart wants every month, including the ones nobody ordered in:
const points = fillBuckets(groups, { field: 'paid_at', unit: 'month', from: '2026-01', to: '2026-12', empty: { sum_total: '0' } })

const page = await articles.search('red "running shoes" -kids', { where: { status: 'published' }, limit: 10 })
page.items[0]._meta.score           // the relevance; `language: 'es'` picks the stemming
```

- **`aggregate(params)`**: `groupBy` and `metrics` take a string or a list, `orderBy` a key or result name (`'-count'`), `tz` an IANA zone, `limit` at most the collection's maximum. Without `groupBy` the answer is one group with an empty `key`, for a counter.
- **`fillBuckets(groups, { field, unit, from, to, empty })`** adds the empty buckets of one date field between `from` and `to` (bucket keys such as `'2026-09'`, or dates), as `count: 0` plus `empty`. It needs groups grouped by that bucket alone.
- **`search(text, params)`** takes a list's parameters except `orderBy` and `after`; pages with `skip` through the first 1,000 results.
- **Errors:** `AggregateNotEnabledError`, `InvalidAggregateError` and `SearchNotEnabledError` (400, `ValidationError`s), `AggregateTooExpensiveError` and `SearchTooExpensiveError` (422) and `IndexBuildingError` (503, a `RetryableError`: the text index is being rebuilt, and `retryAfter` says when to try again).
- **Typed:** in a client typed from `backd types`, `aggregate` only accepts the declared fields (a bucket only on dates), and a collection that declares no `aggregate:` has none.
- **Functions** have both on `ctx.db` (as the caller) and `ctx.admin.db`.

### Custom ids

A collection that declares [`custom_id`](../../configuration/config-dir/#custom-ids) takes the id from the client: pass it in the document. Nothing is overwritten, and a typed client requires it:

```js
const invoices = backd.db('billing').collection('invoices')
await invoices.create({ id: 'INV-2026-0042', customer: 'Ada', total: 120 })
await backd.db('main').collection('profiles').create({ id: user.id, bio: '…' })   // custom_id: user

try {
  await invoices.create({ id: 'INV-2026-0042', customer: 'Bob', total: 5 })
} catch (err) {
  if (err instanceof IdTakenError) pickAnotherNumber()          // 409 id_taken
  if (err instanceof InvalidIdError) console.log(err.code)      // id_required | invalid_id | id_not_allowed
}
```

A `429` after repeated `IdTakenError`s is a `RetryableError`: the [duplicate limit](../../api/documents/#custom-ids). `batch` creates take `id` on the operation. See [the API](../../api/documents/#custom-ids) for the order of the checks.

### Safe retries of creates

`create()` and `db.batch()` take an `idempotencyKey`. The server remembers the first answer for 24 hours and returns it for the same key and body, so a retry never makes a second document (see [Safe retries](../../api/documents/#safe-retries-with-idempotency-key)). A request with a key is also retried by the client itself after a network error, `429` or `503`, when `retry` is on:

```js
const order = await orders.create(draft, { idempotencyKey: 'checkout-' + cartId })
```

{{< hint style="tip" title="Best practice" >}}
Pass `ifMatch` for every update that depends on what the user saw: edit forms, counters, toggles. Without it, `replace` is last-write-wins and silently overwrites a change made after you read the document.
{{< /hint >}}

### Batch writes

`db.batch(operations)` creates, replaces, patches and deletes documents across the database's collections in one MongoDB transaction — either every operation applies, or none does:

```js
const [order, stock] = await backd.db('main').batch([
  { op: 'create', collection: 'orders', document: { item: 'widget', qty: 2 } },
  { op: 'patch', collection: 'stock', id: stockId, patch: { qty: 8 }, ifMatch: stockDoc._meta.version },
])
```

- Up to 100 operations. Each is validated and rule-checked exactly like a single write; `ifMatch` works the same as above.
- If any operation fails, the whole batch rejects (the usual error types — `ValidationError`, `ForbiddenError`, `VersionMismatchError`, ...) and nothing was written. A version mismatch means re-reading and retrying the whole batch, not just one operation.
- See [Batch writes](../../api/documents/#batch-writes) for the full request/response shape.

## Files

Files live in the file fields of a collection's documents ([File fields](../../files/file-fields/)). The collection handle has a method for each thing an app does with them, and each is a request to the field's [`_files` routes](../../files/transfers/), under the signed-in user's rules.

```js
const docs = backd.db('app').collection('claims')

// Upload into a document. Through backd or straight to the bucket: the client asks the field.
const doc = await docs.uploadFile(id, 'receipts', file, { onProgress: ({ loaded, total }) => bar.set(loaded / total) })

// Upload before the document exists, then name the upload in the create.
const up = await docs.prepareUpload('receipt', file)
await docs.create({ title: 'Taxi', receipt: up.ref })              // up.ref is { upload, token }

const { url, expiresAt } = await docs.fileUrl(id, 'receipts', fileId) // a link for an <img> or a button
await docs.downloadFile(id, 'receipts', fileId)                      // a fresh link, opened: the browser saves it
const one = await docs.get(id, { fileLinks: true })                  // every file carries url and expires_at
await docs.deleteFile(id, 'receipts', fileId)
const info = await docs.fileField('receipts')                       // { upload, max_size, types, multiple, max_files, … }
```

- **`uploadFile(id, field, file, opts)`** takes a `File` or `Blob` (or bytes, or a string) and resolves with the updated document. A field with `upload: direct` is sent straight to the bucket: the client reads the file once to compute its SHA-256 (incrementally, so a large file isn't held in memory), declares it, sends it with the signed link and completes it, and backd verifies the result. `opts`: `name` and `type` (default to the file's own), `ifMatch`, `signal` and `onProgress`.
- **`prepareUpload(field, file, opts)`** does the same for a document that doesn't exist yet and resolves with `{ id, token, ref, expiresAt, file }`. Put `ref` in the file field of a `create` (or a `put` or `patch`; a list of them for a `multiple` field). An upload is used once and expires after the realm's `pending_ttl`.
- **`onProgress({ loaded, total })`** reports the bytes sent as they go **in a browser** (the client uses `XMLHttpRequest`, the only way to know). Elsewhere it is called once, when the file has been sent.
- **`fileUrl(id, field, fileId)`** resolves with a link that works without credentials until `expiresAt`. Fetch it when it is needed: see [Download buttons](../../files/apps/#download-buttons).
- **`fileUrl(id, field, fileId, { version: 'thumb' })`** links a made [version](../../files/file-fields/#image-versions) of an image, and resolves with **`null`** while it isn't ready (`files(id, field).get()` and `.link()` take `{ version }` too and throw `version_unavailable`). **`versionStatus(file, 'thumb')`** reads a version's `{ status, ready, reason, width, height }` from a file's details, to show "processing…" and reserve room for the picture. With `fileLinks: true` each ready version also has its own `url`.
- **`files(id, field).version(name, fileId?)`** has `regenerate()`, `generate(params)` and `delete()` for a made version: they wait for a worker and resolve with the version's state (see [Versions of an image](../../files/functions/#versions-of-an-image)); they are what a function uses.
- **`downloadFile(id, field, fileId)`** is `fileUrl()` followed by `location.assign()`; the link forces a download, so the page stays. **Browsers only**: elsewhere it throws, and `fileUrl()` or `files(id, field).get()` are what to use.
- **`fileLinks: true`** on `get` and `list` adds `url` and `expires_at` to every file. Links expire; use it for what is shown at once.
- **`deleteFile(id, field, fileId, { ifMatch })`** removes one file. `files(id, field).delete()` clears the field.
- **`files(id, field)`** is the lower level: `put`, `get` (the bytes), `link`, `delete` and `list`, which is also what a [function](../../files/functions/) uses.
- **`fileField(field)`** says how the field takes files (asked once, then remembered): to show `max_size` and `types` and refuse a file before sending it.

Errors are the client's: `unsupported_file_type` (`415`), `payload_too_large` (`413`), `too_many_files` (`409`), a `NotFoundError` for a document the user can't read, and for a direct upload `upload_failed` when the bucket refuses the file. That one almost always means the bucket's CORS isn't set for the app's origin: see your [provider's page](../../files/).

## Functions

`db.fn(name, input, opts)` calls a [function](../../functions/). A `sync` function's output comes back directly:

```js
const receipt = await backd.db('shop').fn('checkout', { cart: 'c1' })
// { order: 'o7', total: 1250 }
```

An `async` function instead returns a `Job` handle, without waiting for it to finish:

```js
import { Job, JobTimeoutError } from 'backd-js'

const job = await backd.db('shop').fn('reconcile', {})
job.id            // "d3c9ljp8hc2g00b6s1m0"
job.function      // "shop/reconcile"

await job.status()                                   // 'queued', 'running' or 'done'
const output = await job.wait({ pollIntervalMs: 500, timeoutMs: 30_000 })
// polls GET .../_jobs/{id} until status is 'done', then resolves like a sync call would
```

- `wait` polls every `pollIntervalMs` (default 500) and resolves with the job's output once it's `done`. If the job itself fails — the function threw, timed out, or crashed — `wait` (and a plain `sync` call) throw the same [error](#errors) a direct call would: a `BackdError` subclass built from the job's own status/code/message, so callers don't need to branch on sync vs. async to handle failures.
- If `timeoutMs` is set and elapses before the job is done, `wait` throws `JobTimeoutError` (`.jobId`) instead — a plain `Error`, not a `BackdError`, since giving up on polling isn't something the server reported. The job itself keeps running; call `wait` again, or `status()`, to check on it later.
- With no `timeoutMs`, `wait` polls until the job finishes, however long that takes (up to the function's own `timeout`).
- `job.steps` are the [steps](../../functions/jobs/#reporting-progress-from-a-long-job) the function reported as of the last `status()` or `wait()` (the last is the current step), and `wait({ onProgress: (steps) => ... })` calls you after every poll.
- `job.raw` is the last `GET .../_jobs/{id}` response `status()`/`wait()` fetched, if you need fields beyond `id`, `function` and `status`.

An administrator can also run a function by hand, internal ones included, with `backd.admin.invokeFunction('<database>/<name>', { input, as, idempotencyKey })` (see [Internal functions](../../functions/internal/#running-a-function-by-hand)); it returns the output or a `Job`, like `db.fn()`.

A `sync` function can be started as a job by the caller, with `respondAsync: true` (sends `Prefer: respond-async`, see [Calling functions](../../functions/calling/#asking-for-a-job-prefer-respond-async)): the call resolves to a `Job`, and `job.wait()` gives the output or throws the function's error, exactly as above.

```js
const job = await backd.db('shop').fn('checkout', { cart: 'c1' }, { respondAsync: true })
const receipt = await job.wait()
```

An `Idempotency-Key` makes a retried call safe (see [Idempotency](../../functions/calling/#idempotency)):

```js
await backd.db('shop').fn('charge', { amount: 1250 }, { idempotencyKey: `order-${orderId}-charge` })
```

`fn`'s third argument also accepts `{ idempotencyKey, respondAsync, retry, signal, headers }`, same as [collections calls](#retries). `webhook` functions aren't called through `fn` — they're invoked by the sender, not the client.

### Triggers

A [trigger](../../configuration/config-dir/#triggers) runs an internal function when a collection's documents change, in the background and without a client in the loop, so **there is nothing to call**: the client has no `triggers` surface, by design. What the client does touch:

- `backd.admin.invokeFunction('main/on-paid', { input })` runs the trigger's *function* by hand, with the input you give it. It neither fires the trigger nor needs an event: the wiring lives in the collection's `collection.yaml`, not in the function.
- The function's input has a type, `generated` with the rest: the realm's `backd.d.ts` has `Realm['db']['main']['collections']['orders']['trigger']` for a collection with triggers (`before` and `after` are partial: only the paths the trigger's `fields` names are there).
- Handlers should be idempotent: the same change can be delivered again after a restart. Deduplicate on the input's `id`, which is the same on every delivery of one change.

```ts
export default async function (input: Realm["db"]["main"]["collections"]["orders"]["trigger"], ctx: Ctx) {
  const seen = ctx.admin.db("main").collection("seen_events");
  if ((await seen.list({ where: { event_id: input.id }, limit: 1 })).items.length) return;   // a replay
  await seen.create({ event_id: input.id });
  // …the effect
}
```

## Live lists

`client.realtime.subscribe()` follows collections that declare [`realtime: true`](../../api/realtime/): it loads each list, keeps it current from the stream and tells you when it changes.

```js
const live = client.realtime.subscribe([
  { db: 'main', collection: 'items', where: { '_meta.org': orgId }, load: { sort: '-_meta.created_at', limit: 50 } },
])
live.on('change', (state) => render(state.items))   // sorted, reconciled
live.on('status', (s) => showBanner(s))             // 'connecting' | 'live' | 'resyncing' | 'closed'
await live.ready                                    // the first load
live.close()

// The same, from a collection (a team scope becomes a `where` on `_meta.team`):
const mine = client.db('main').collection('items').inTeam(teamId).subscribe({ load: { sort: '-_meta.created_at' } })
```

What the client does for you:

- **One stream per client**, multiplexed over every view's subscriptions (the server allows 20 per stream). Adding or closing a subscription reopens it with the last resume token.
- **Stream first, then load:** the stream opens, each list is read with a normal list call, and the events that arrived meanwhile are applied after it, **ignoring any whose `_meta.version` is not newer**. New documents are inserted in the `load.sort` order, and the list is trimmed to `load.limit` (100 by default).
- **Resume:** after a dropped connection the stream continues after the last event, with the caller's current access; the list is not read again. A token the server refuses, or history it no longer holds, reloads the lists.
- **Resets:** when the server says `reset` (a slow client, a changed access, missing history) the affected list is reloaded after a random wait of up to `jitterMs` (1,000 by default), while the last list stays available and the status is `resyncing`.
- **Credentials:** at the end of a credential's lifetime (`reauth`) the client reconnects with the current one. When the session is revoked (`session_ended`) the view stops and `error` carries a `SessionEndedError`; sign in again and subscribe again.
- **References:** `references: { author: { field: 'author_id', collection: 'profiles', fields: ['name'] } }` keeps the referenced documents in `state.references.author` (by id), reading the missing ones in one batched call. They are read under the normal rules; subscribe to the referenced collection too if they must update live.
- **Errors** are typed: `RealtimeNotEnabledError` and `TooManySubscriptionsError` (`ValidationError`s), `InvalidResumeTokenError` (handled for you) and `SessionEndedError`.

A list bounded by `load.limit` can't refill itself when a document leaves it: a removal makes it shorter until the next reload, so subscribe with an equality on a grouping field and keep `limit` above what you show.

## Push notifications

`client.push` registers this device so that functions can push the signed-in user: `register({ platform, token })`, `devices()`, `unregister()`, `registerOnLaunch(getToken)` and, for browsers, `subscribeWeb({ registration })`, which asks for the permission and does nothing when it is denied.

```js
backd.push.registerOnLaunch(async () => ({ platform: 'android', token: await getFcmToken() }))
const { permission } = await backd.push.subscribeWeb({ registration: await navigator.serviceWorker.ready })
```

It needs a user session. The service worker, the mobile apps' raw endpoints and the rest are in [Push notifications → From apps](../../push/client/).

## Consents

A realm can ask people to accept [consents](../../auth/consents/): versioned texts such as terms, a privacy notice or marketing. `client.consents` reads them and records the answers.

```js
// Before an account exists: what the realm asks for, and the exact text
const list = await backd.consents.current()
// [{ name: 'terms', required: true, current: { version: '2026-10', reaccept: true }, locales: ['en', 'es'] }, …]
const terms = await backd.consents.text('terms', '2026-10', 'es')   // { text, hash, locale, … }

// At sign-up: the name and the version shown. Every required consent must be there
await backd.auth.signup({ email, password, consents: { terms: '2026-10', marketing: '1' } })

// Signed in: what the user holds, accept or withdraw
await backd.consents.state()                       // { consents, to_accept, history }
await backd.consents.accept({ marketing: '1' })
await backd.consents.withdraw('marketing')         // optional consents only
```

- **At sign-in** a user who has to accept something (a required consent they never accepted, or a new version that asks for re-acceptance) gets a `ConsentChallenge` from `auth.signIn()`: `{ consentRequired: true, challenge, consents }`. `auth.login()` and `auth.signup()` promise a session, so they throw a `SecondStepRequiredError` with `code: 'consent_required'` and the same `challenge` and `consents`. Show the texts and finish with `auth.acceptConsents(challenge, { terms: '2026-10' })`, which stores the session.
- **After a `reaccept_by` date**, a request of a user who hasn't accepted rejects with a `ConsentRequiredError` (`403`): show the prompt and send `client.consents.accept(...)`, which stays open for them.
- **Sign-ups through a magic link or a provider** take `consents` as an option (`auth.magicLink.request(email, { consents })`, `auth.signInWith(provider, { consents })`, `auth.signInWithIdToken(provider, { …, consents })`); a new account made without them gets the challenge at its first sign-in.

```js
import { ConsentRequiredError, SecondStepRequiredError } from 'backd-js'

try {
  await backd.auth.login({ email, password })
} catch (err) {
  if (err instanceof SecondStepRequiredError && err.code === 'consent_required') {
    const accepted = await showConsentDialog(err.consents)        // { terms: '2026-10' }
    await backd.auth.acceptConsents(err.challenge, accepted)
  } else throw err
}
```

## Your own data

`client.account` lets the signed-in user download a copy of everything the realm holds about them, when the realm sets [`compliance.export`](../../auth/personal-data/). Asking needs a fresh [proof](../../auth/reauth/); the rest only the session.

```js
await backd.auth.reauth({ method: 'password', password })
const job = await backd.account.requestExport()          // { id, status: 'queued', … }
const ready = await backd.account.waitForExport(job.id, { onProgress: (e) => show(e.step) })
const { url, bytes } = await backd.account.downloadExport(ready.id)
location.assign(url)                                      // a link signed for five minutes
// or, in Node or a service worker:
await fs.writeFile('my-data.zip', await bytes())
```

- **`requestExport()`** is limited to one every 24 hours: a second is a `RetryableError` (`429`) whose `retryAfter` says in milliseconds how long to wait. A realm that does not export answers `NotFoundError`; a missing proof, a `reauth_required` error.
- **`exports()`** lists the user's exports (newest first), **`export(id)`** reads one (`step` says what it is doing), and **`waitForExport(id)`** polls until it is `ready`, or throws for a `failed` or `expired` one (`pollIntervalMs`, `timeoutMs`, `onProgress`).
- **`downloadExport(id)`** asks for JSON so a page can use the link: `url` is signed for a few minutes (no credentials go to the storage), `bytes()` reads it. A realm whose storage clients cannot reach sends the zip through the API instead, and `bytes()` reads that. `409` until it is ready, `410` once expired.
- The user is also emailed a link to your app's page (`compliance.export_page?export=<id>`): that page is where you call `downloadExport(id)`.

## App configuration

`client.config` reads the realm's [app configuration](../../configuration/app-config/): feature flags, maintenance switches, limits. The server answers only what the signed-in user's rules allow, with defaults filled in.

```js
await client.config.load(["features", "maintenance"]);        // or load() for everything readable
client.config.get("maintenance.enabled");                      // from the copy held
await client.config.get("beta.new_editor", { refresh: true }); // force a fetch
client.config.on("change", (paths) => rerender(paths));
```

- **`load(paths?)`** fetches and keeps a copy, with no request while it is fresh. **`get(path)`** returns the value from the copy held; for a key whose group was never loaded it fetches that group first, and only then returns a promise. **`getSync(path, fallback)`** never asks: it returns the copy's value, or `fallback` (the client doesn't know the server's defaults).
- **Freshness:** a copy is fresh for the `cache_ttl` the server sent (5 minutes by default). After that, reads return the copy at once and revalidate it in the background with `If-None-Match`, which usually costs a `304`. `refresh: true` forces a fetch.
- **Where copies live:** per realm and per user, in `localStorage` in browsers and in memory elsewhere (`configStorage: false` for memory only, or any object with `getItem`, `setItem` and `removeItem`). Blocked storage falls back to memory. The copy of a page that has just reloaded is restored by the first `load()`.
- **Clearing:** sign-out and signing in as another user drop every copy of the realm, so the next person on the same browser never sees the previous user's keys.
- **`on("change")`** fires with the dot paths whose value changed when a read brings a different configuration.
- **`live(paths?)`** follows the configuration over the client's [realtime stream](../../api/realtime/#following-the-configuration), so `change` fires within the realtime delay instead of at the next expiry. It reads again only the branches you hold that the event touches, with `If-None-Match`, and whenever the stream opens or reopens it revalidates everything held, so a change made while it was down is not missed. It returns a function that stops following. It follows the signed-in user: signing in or out opens the stream again with the new credential. If the realm declares no configuration, or realtime is off, `on("error", fn)` hears of it.

```js
const stop = client.config.live(["maintenance"]);
client.config.on("change", (paths) => rerender(paths));
client.config.on("error", (err) => console.warn("no live updates:", err.message));
```

## Typed client

`backd types` generates a TypeScript `Realm` interface from the config ([Generated types](../types/)). Pass it to `createClient` and everything the client returns or accepts is typed, with no change at run time:

```ts
import { createClient } from "backd-js";
import type { Realm } from "./backd/types";

const client = createClient<Realm>({ url: "https://api.example.com", realm: "shop" });

const page = await client.db("main").collection("orders")
  .list({ where: { status: "paid", total: { $gte: 100 } }, orderBy: "-total" });
page.items[0].status;                                        // "new" | "paid" | "shipped"
const r = await client.db("main").fn("place", { items });    // typed input and result
client.config.get("shop.currency");                          // "eur" | "usd" | undefined
```

- **What's typed:** databases and collections by name, documents (with `_meta` and [file details](../../files/file-fields/), and `_expanded` for the [references](#expanding-references) a collection declares), what `create`, `replace` and `patch` accept, `where` (fields, and the operators each type allows) and `orderBy`, function calls (input, and result or [job](#functions) by mode; internal functions have no route and aren't offered) and configuration paths.
- **Without the type parameter** everything works untyped, as before. The runtime package is the same for every realm.
- **Stale-types warning (development).** Pass `types` the fingerprint from `realm.json`, and the client sends it as `X-Backd-Types`; when the server's config has moved on it answers `X-Backd-Types-Stale: true` and the client warns once on the console (or calls `onStaleTypes`). Nothing is refused. Send it only in development builds; without `types`, no header is sent. The fingerprint covers the whole realm, so generate without `--db`, or the warning never goes away.

```ts
import info from "./backd/realm.json";

const client = createClient<Realm>({
  url, realm: "shop",
  types: import.meta.env.DEV ? info.fingerprint : undefined,
});
```

## Token storage

By default the token is kept in memory: it is gone after a reload, and nothing on the page can read it from storage. To keep users signed in across reloads, choose a storage:

```js
import { createClient, localStorageStorage } from 'backd-js'

const backd = createClient({ url, realm, storage: localStorageStorage() })
```

{{< hint warning >}}
Any script running on the page can read `localStorage`, so an app that stores tokens there must protect itself against cross-site scripting (a strict Content-Security-Policy, no untrusted HTML). The default in-memory storage is safer; you trade it for staying signed in across reloads.
{{< /hint >}}

Or keep no token in the page at all, with [session cookies](#session-cookies).

Any object with `get()`, `set(token)` and `remove()` works as storage, and the methods may be async, for example to use a mobile app's secure storage:

```js
const storage = {
  get: () => SecureStore.getItemAsync('backd'),
  set: (token) => SecureStore.setItemAsync('backd', token),
  remove: () => SecureStore.deleteItemAsync('backd'),
}
```

`await backd.auth.token()` returns the stored token, or `null`.


## Session cookies

In a realm that enables [`sessions.cookie`](../../auth/sessions/#session-cookies), a browser app can keep the session in an HttpOnly cookie instead of a token, so even an injected script can't steal it:

```js
const backd = createClient({ url: 'https://api.example.com', realm: 'blog', cookies: true })

await backd.auth.login({ email, password })   // asks for a cookie: the answer has no token
await backd.auth.me()                         // the browser sends the cookie; the client sends no Authorization header
await backd.auth.hasSession()                 // what this page believes; after a reload, ask the server with me()
await backd.auth.logout()                     // also clears the cookie
```

- `cookies: true` makes `login` and `signup` ask for a cookie and every request send credentials (`fetch(..., { credentials: 'include' })`). `auth.token()` is `null`: there is no token for scripts to read.
- The realm must list your app's origin in `cors.origins` (no wildcard), and the API must be on a site that the cookie's `same_site` setting reaches: the same site as the app by default (`app.example.com` with `api.example.com`).
- The client keeps only a hint that a session exists (in the `storage` you choose, default memory). If the server stops accepting the cookie, the next request signs the client out (`SESSION_EXPIRED`), as with tokens.
- Not combined with `apiKey`, and not for the admin API, which needs an `Authorization` header (see [Sessions](../../auth/sessions/#session-cookies)).

## Organizations

`backd.orgs` is the [organizations API](../../organizations/api/): the groups of users a person belongs to, with their members, teams and invitations.

```js
const org = await backd.orgs.create({ name: 'Acme Corp', slug: 'acme' })   // also opens a session scoped to it, kept by the client
const mine = await backd.orgs.signedIn()              // [{ ref, id, name, expiresAt }]: the organizations this client holds a session in

await backd.orgs.members(org.id).list()
await backd.orgs.members(org.id).update(userId, ['editor'])   // direct roles
await backd.orgs.teams(org.id).create({ name: 'Editors', roles: ['editor'] })
await backd.orgs.teams(org.id).addMember(team.id, userId)
const { items } = await backd.orgs.teams(org.id).members(team.id, { role: 'manager' })   // members and holders of a team role, in force now
await backd.orgs.teams(org.id).grantRole(team.id, { user: userId, role: 'delegate', until: '2026-08-15T00:00:00Z' })
await backd.orgs.teams(org.id).setRoleWindow(team.id, { user: userId, role: 'delegate', until: '2026-08-31T00:00:00Z' })
await backd.orgs.teams(org.id).revokeRole(team.id, { user: userId, role: 'delegate' })

// An invitation answers the link once: show it, send it, or let the server email it.
const inv = await backd.orgs.invitations(org.id).create({ binding: 'email', email: 'ana@example.com', roles: ['editor'], send: true })
inv.link   // https://app.acme.example/join?token=…
const again = await backd.orgs.invitations(org.id).newLink(inv.id)   // a new token and link for a pending one; the old link stops working

// The person who got the link previews it, then signs in to the organization with it:
const offer = await backd.auth.previewInvitation(token, { org: 'acme' })   // { org, roles, teams }
await backd.org('acme').auth.login({ email, password, invitation: token })
```

The rest of the surface, by object: `orgs` has `create`, `signedIn`, `get`, `update` (`name`, `slug`, `require_mfa` and `session_max_age`), `leave` (allowed without a second factor; the last manager cannot) and `delete`; `members(id)` has `list`, `update` and `remove`; `teams(id)` has `list`, `create`, `update`, `delete`, `members`, `addMember`, `removeMember`, `grantRole`, `setRoleWindow` and `revokeRole`; `invitations(id)` has `create`, `list`, `revoke`, `send` and `newLink`; `templates(id)` has `list`, `get`, `put`, `delete`, `preview` and `test`; `sso(id)` has `list`, `get`, `set` and `delete` (the organization's [sign-in connections](#through-an-organizations-own-google-or-microsoft)). Each maps to a route of the [organizations API](../../organizations/api/).

**Documents belong to an organization** when their collection says so: the one the session was opened for. There is nothing to name: creates go there and lists show only that organization's documents (the read rule still decides what is seen). To work in another organization use its client, `backd.org('beta')`.

```js
const projects = backd.org('acme').db('main').collection('projects')
await projects.create({ title: 'Plan' })
const { items } = await projects.list({ where: { status: 'open' } })
```

In a collection with [teams](../../organizations/team-roles/#documents-that-belong-to-a-team), `inTeam(id)` narrows a collection of the organization further: creates go to that team (the caller must belong to it or hold a team role in it, or the create throws `NotTeamMemberError`) and lists show only its documents.

```js
const requests = backd.db('main').collection('requests').inTeam(sales.id)
await requests.create({ title: 'Leave' })
```

Moving documents between organizations or teams is an administrator's job: only through the [admin API](../../../operate/administration/admin/#data).

- **Deleting** an organization and **turning `require_mfa` on** ask for a fresh proof of identity: wrap them in [`backd.auth.withProof`](#sign-up-login-and-sessions), as other sensitive actions.
- **A second factor.** `mfa_met` is false for a session without one in an organization that requires it; the organization's calls then throw `OrgRequiresMFAError`. Leaving is still allowed.

## Errors

Every answer that isn't 2xx throws a `BackdError`, or one of its subclasses:

| Class | Status | Typical `code` |
|---|---|---|
| `ValidationError` | 400 | `validation_error`, `invalid_json`, `invalid_query`, `invalid_header` |
| `InvalidIdError` (a `ValidationError`) | 400 | `id_required`, `invalid_id`, `id_not_allowed`: the `id` of a [custom id](#custom-ids) create |
| `AuthenticationError` | 401 | `unauthenticated`, `invalid_credentials` |
| `PasswordExpiredError` (an `AuthenticationError`) | 401 | `password_expired`: the password is right but an administrator [expired it](../../auth/users/#expiring-every-password); `resetEmailQueued` says whether backd queued the email with the link to choose a new one. Only the right password gets this answer, from `auth.login()`, `auth.signIn()` and `auth.reauth()` |
| `ForbiddenError` | 403 | `forbidden` |
| `NotAllowedError` (a `ForbiddenError`) | 403 | `not_allowed`: the person is not of the organization the connection they signed in through admits; nothing was created |
| `SsoSessionError` (a `ForbiddenError`) | 403 | `sso_session`: a session opened through an organization's connection can't change the account's credentials or delete or export it |
| `SsoRequiredError` (a `ValidationError`) | 400 | `sso_required`: the organization requires its connection; `auth.options()` says so (`ssoRequired`) |
| `NotFoundError` | 404 | `not_found` |
| `ConflictError` | 409 | `email_taken`, `conflict`, `write_conflict` |
| `IdTakenError` (a `ConflictError`) | 409 | `id_taken`: a document with that [custom id](#custom-ids) exists, and nothing was overwritten |
| `NotOrgMemberError`, `OrgRequiresMFAError`, `OrgRoleRequiresMFAError` (`ForbiddenError`s) | 403 | `not_org_member` (also for an unknown organization), `org_requires_mfa`, `org_role_requires_mfa` (`details` names the role) |
| `NotTeamMemberError` (a `ForbiddenError`) | 403 | `not_team_member`: not a member of the team and no team role there (the same for an unknown team) |
| `TeamError` (a `ValidationError`) | 400 | `team_required`, `team_not_allowed` |
| `InvalidExpandError`, `ExpandTooLargeError` (`ValidationError`s) | 400 | `invalid_expand` (`details` name the part), `expand_too_large`: see [expanding references](#expanding-references) |
| `AggregateNotEnabledError`, `InvalidAggregateError`, `SearchNotEnabledError` (`ValidationError`s) | 400 | `aggregate_not_enabled`, `invalid_aggregate`, `search_not_enabled`: see [aggregations and search](#aggregations-and-search) |
| `AggregateTooExpensiveError`, `SearchTooExpensiveError` | 422 | `aggregate_too_expensive`, `search_too_expensive` |
| `IndexBuildingError` (a `RetryableError`) | 503 | `index_building`: the collection's text index is being rebuilt |
| `LastOrgManagerError` (a `ConflictError`) | 409 | `last_org_manager`: the organization needs someone who can manage it |
| `VersionMismatchError` | 412 | `version_mismatch` |
| `ConsentRequiredError` | 400, 403 | `consent_required`; `consents` lists what to accept, as `{ name, version }` |
| `RealtimeNotEnabledError`, `TooManySubscriptionsError`, `InvalidResumeTokenError` (`ValidationError`s) | 400 | `realtime_not_enabled`, `too_many_subscriptions`, `invalid_resume_token`: see [live lists](#live-lists) |
| `SessionEndedError` (an `AuthenticationError`) | — | `session_ended`: the stream's session, user or key is gone |
| `RetryableError` | 429, 503 | `too_many_requests`, `unavailable`; `retryAfter` in milliseconds |
| `NetworkError` | 0 | `network_error`, `aborted` |

Each error has `status`, `code`, `message`, `details` (`[{ path, reason }]`, for example the fields that failed validation) and `requestId`, which matches `backd`'s logs.

```js
import { AuthenticationError, ValidationError } from 'backd-js'

try {
  await backd.auth.login({ email, password })
} catch (err) {
  if (err instanceof AuthenticationError) showMessage('Wrong email or password')
  else if (err instanceof ValidationError) showFieldErrors(err.details)
  else throw err
}
```

## Retries

Retries are off by default. When enabled, the client retries `429` and `503` answers, waiting as long as the server's `Retry-After` asks (otherwise 0.5 s, doubling), and never longer than `maxDelayMs`:

```js
const backd = createClient({ url, realm, retry: { attempts: 2, maxDelayMs: 10_000 } })
```

{{< hint note >}}
Only safe requests are retried: reads, and writes sent with `If-Match`. Other writes might already have taken effect, so their errors are thrown as they come, and a `POST` that timed out may or may not have created its document.
{{< /hint >}}

Every call also accepts `{ retry, signal, headers }` as a last argument, for example an `AbortSignal` to cancel it.

## API keys and the admin API

For server-side code, `createClient({ url, realm, apiKey })` sends an [API key](../../auth/api-keys/) with every request. Access rules don't apply to it: collections calls see and change everything in the realm. In a browser, `createClient` refuses an `apiKey`, because every visitor could read it. Pass `dangerouslyAllowBrowser: true` only for tools that never reach real users.

{{< hint danger >}}
An API key in a browser or mobile app is readable by every visitor and bypasses all [access rules](../../auth/rules/). `dangerouslyAllowBrowser` exists for internal tools on a trusted network; anything public should sign users in with sessions instead.
{{< /hint >}}

A client with an API key with the `admin` role (`backd apikey create --realm <realm> --name <name> --role admin`) can use the [admin API](../../../operate/administration/admin/):

```js
const backd = createClient({ url, realm: 'blog', apiKey: process.env.BACKD_API_KEY })

const ada = await backd.admin.users.find('ada@example.com')      // or null
const page = await backd.admin.users.list({ limit: 50, skip: 0 })   // q: 'ada' searches emails
const user = await backd.admin.users.create({ email: 'bob@example.com', password })  // password optional
await backd.admin.users.get(user.id)
await backd.admin.users.update(user.id, { emailVerified: true, disabled: false })
await backd.admin.users.setPassword(user.id, newPassword)        // ends their sessions
await backd.admin.users.expirePassword(user.id, { reason })      // the password stops opening the account until they reset it
const { link } = await backd.admin.users.resetLink(user.id)     // a one-time link, shown once, nothing sent (fresh proof needed)
await backd.admin.passwords.state()                              // { accounts, epoch }: what expiring every password affects
await backd.admin.passwords.expireAll({ reason, notify: true })  // every password, every session; notify emails each user a link
await backd.admin.passwords.notify()                             // (re)starts that email job; users already told are skipped
await backd.admin.sessions.revokeAll()                           // every session of the realm, passwords untouched
await backd.admin.users.addRole(user.id, 'editor')               // roles declared in realm.yaml
await backd.admin.users.removeRole(user.id, 'editor')
await backd.admin.users.owned(user.id)                          // what erasing them would do, per collection with a policy
await backd.admin.users.identities(user.id)                      // the ways to sign in: password, providers
await backd.admin.users.unlinkIdentity(user.id, 'google')        // never the last one: ConflictError, 'last_sign_in_method'
await backd.admin.users.sessions(user.id)                        // unexpired sessions, never a token
await backd.admin.users.revokeSession(user.id, sessionId)
await backd.admin.users.delete(user.id)                         // ERASES them (irreversible): a tombstone, and the collections' policies; resolves with the erase job

const invitation = await backd.admin.invitations.create({ email: 'eve@example.com', expiresIn: '3d' })
sendInvitationEmail(invitation.token)                              // shown only here
await backd.admin.invitations.send({ email: 'eve@example.com', redirectTo, locale })  // backd emails it: no token
await backd.admin.users.changeEmail(user.id, 'new@example.com')   // at once; both addresses are told
await backd.admin.invitations.list()
await backd.admin.invitations.revoke(invitation.id)

const orgs = await backd.admin.orgs.list({ status: 'orphaned', q: 'acme' })   // each with its number of members
const report = await backd.admin.orgs.owned(orgs[0].id)         // what deleting it would do, per collection
await backd.admin.orgs.assignOwner(orgs[0].id, user.id)          // an orphaned organization gets a manager
await backd.admin.orgs.setClaims(orgs[0].id, { plan: 'pro' })    // read by rules with orgClaims()
await backd.admin.orgs.transfer('main', 'projects', docId, otherOrgId)   // moves a document; also invite, members, teams, delete, reactivate, assign

const access = await backd.admin.whoami()                         // level, areas it may read and change
const posts = backd.admin.data('blog', 'posts')                  // the admin data route: list/get/create/replace/patch/delete/restore, past the rules
const config = await backd.admin.config()                         // what this instance runs for the realm, read-only
const check = await backd.admin.checkConfig({ 'main/notes/collection.yaml': 'rules:\n  read: user != nil\n' })   // validate a change against the whole config, without applying it
const spec = await backd.admin.openapi()                       // the realm's admin OpenAPI document (needs the config area)
const appSpec = await backd.admin.clientOpenapi()                 // the one for client applications: no admin routes
const storage = await backd.admin.storage.check()                  // the realm's object storage: credentials, put/get, signed SHA-256, links
const started = await backd.admin.dataChecks.start({ database: 'main', collection: 'orders' }) // reads every document: see estimated_documents
const reports = await backd.admin.dataChecks.wait(started, { onProgress: (job) => console.log(job.progress?.current) })  // the documents that no longer match their schema
const detail = await backd.admin.jobs.get(jobId)                // the job with every step it reported (detail.steps)
await backd.admin.jobs.cancel(jobId)                              // a queued or running function job ends as "cancelled"
const again = await backd.admin.jobs.rerun(jobId)                 // a finished one is queued again as a new job (again.rerun_of)
await backd.admin.schedules.pause('main/nightly')                 // a schedule creates no runs until resume(); list() shows which are paused
const secrets = await backd.admin.secrets.list()                  // scope, name, who set it; never the values
await backd.admin.secrets.set('STRIPE_KEY', value, { database: 'main' })   // omit database for a realm secret
await backd.admin.secrets.delete('STRIPE_KEY', { database: 'main' })
const history = await backd.admin.invocations.list({ function: 'main/refund', limit: 20 })   // function calls, with origin and parent_id

const created = await backd.admin.apiKeys.create({ name: 'billing', role: 'data', expiresIn: '90d', networks: ['203.0.113.0/24'], scopes: ['read:main/posts'] })
storeSecret(created.key)                                           // shown only here
await backd.admin.apiKeys.list()                                   // never the keys themselves
await backd.admin.apiKeys.revoke('billing')
await backd.admin.users.setNetworks(user.id, { loginNetworks: ['10.20.0.0/16'] })   // [] removes

const trail = await backd.admin.audit.list({ target: `user:${user.id}`, since: new Date(Date.now() - 864e5) })
trail.items                                                        // newest first; read-only
const jobs = await backd.admin.jobs.list({ function: 'main/nightly_cleanup', status: 'done', limit: 10 })   // state and outcome, never input or output
```

A client signed in as a user holding one of the realm's [admin roles](../../configuration/realm/#roles) can use `client.admin` too, for example in a back-office app. Calling `client.admin` without an API key or a session throws at once; the server decides the rest. Acting on behalf of a user from the client is planned.
