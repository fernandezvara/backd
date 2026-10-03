---
title: "JavaScript client"
description: "backd-js: auth, sessions, collections and the admin API from browsers, Node and edge runtimes."
icon: "javascript"
weight: 610
toc: true
---

`backd-js` is a small JavaScript library for `backd`. It is plain ES modules with no dependencies and no build step, and it runs wherever `fetch` does: browsers, Node 20+, Deno, Bun and edge runtimes. It ships TypeScript declarations, so editors autocomplete it in JavaScript and TypeScript projects alike.

## Install

```sh
npm install backd-js
```

It has no dependencies, needs no build step and works with any bundler. Without one, import it in a browser with an [import map](https://developer.mozilla.org/docs/Web/HTML/Element/script/type/importmap) pointing at the package's `src/index.js` (from a CDN such as `https://cdn.jsdelivr.net/npm/backd-js/src/index.js`, or from your own server), as the [example app](#example-app) does.

The client is versioned on its own, apart from the server. Versions of `backd-js` before 0.2 were an unrelated, older library.

## Example app

The repository includes two example apps built with this client and [Alpine.js](https://alpinejs.dev/), with no build step:

- the [blog](../../examples/blog/): sign-up, public posts and private drafts, editing with `ifMatch`, and filters;
- the [expenses example](../../examples/expenses/): data shared between users, and the limits of access rules; it requires a verified email and offers "Forgot your password?";
- the [workshop tour](../../examples/workshop-tour/), which also walks the email flows (verification, reset, change of address, emailed invitations).

{{< live-example >}}

`make example` serves both at <https://localhost:8443/example/>.

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

const me = await backd.auth.me()             // { id, email, email_verified, roles, locale, created_at }
await backd.auth.updateMe({ locale: 'es' })  // must be a language the realm lists, else a ValidationError (invalid_locale)
await backd.auth.changePassword({ currentPassword, newPassword })   // other sessions end
const sessions = await backd.auth.sessions() // [{ id, created_at, last_used_at, expires_at, current }]
await backd.auth.revokeSession(sessions[1].id)
await backd.auth.logout()                    // this session
await backd.auth.logoutAll()                 // every session of the user
await backd.auth.deleteAccount({ password }) // deactivates the account; all data is kept
```

- `signup` and `login` store the session token; every later request sends it as `Authorization: Bearer …`.
- In realms with `signup: invite`, pass the invitation: `signup({ email, password, invitation })`.
- In a realm that [requires verified addresses](../../auth/sessions/#email-verification), `signup` creates the account and then rejects with a `VerificationRequiredError`: there is no session yet, and the user logs in after following the link in the email. A `login` before that rejects with a `ForbiddenError` whose `code` is `email_not_verified`. `redirectTo` names where the page after the link may send the user (it must be within the realm's `email.allowed_redirects`).
- `logout` always removes the stored token, even if the server can't be reached or already forgot the session.

The endpoints behind these calls, and their rules, are described in [Sign-up, login and sessions](../../auth/sessions/).

### Account flows by email

In a realm with [`email`](../../functions/email/), the links in the emails open pages of `backd` that need no code. An app that wants **its own pages** points the links at them (`email.links` in `realm.yaml`) and posts the token from the link with these calls, none of which needs a session or starts one:

```js
await backd.auth.resendVerification({ email })                  // 202 whatever the address is
await backd.auth.verifyEmail(token)                             // the token of the verification link
await backd.auth.requestPasswordReset({ email, redirectTo })    // 202 whatever the address is
await backd.auth.resetPassword({ token, password })             // ends every session, verifies the address
await backd.auth.requestEmailChange({ newEmail, password })     // signed in; needs the current password
await backd.auth.confirmEmailChange(token)                      // the link sent to the new address
await backd.auth.revertEmailChange(token)                       // the link sent to the old one
await backd.auth.acceptInvitation({ token, password, locale })  // an emailed invitation
```

- A token that is expired, used or unknown rejects with a `ValidationError` whose `code` is `invalid_token`; a password the policy refuses rejects with a `ValidationError` naming `password` and leaves the token usable. A reset request past the realm's limits rejects with a `RetryableError`.
- `redirectTo` (here and in `signup`) must be within the realm's `email.allowed_redirects`: it is where the page after the link may send the user.
- After `resetPassword`, `confirmEmailChange`, `revertEmailChange` and `acceptInvitation` the user logs in: none of them starts a session, and the first three end the sessions that exist.

The rules behind each flow are in [Sign-up, login and sessions](../../auth/sessions/).

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
// { items: [...], limit: 20, skip: 0, has_more: true, total: 42 }

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
- `iterate` fetches pages by offset. Documents created or deleted while it runs can be skipped or repeated.

With TypeScript (or JSDoc), describe a collection's fields for typed documents:

```js
/** @typedef {{ title: string, published?: boolean }} Post */
/** @type {import('backd-js').Collection<Post>} */
const posts = backd.db('main').collection('posts')
```

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
- `job.raw` is the last `GET .../_jobs/{id}` response `status()`/`wait()` fetched, if you need fields beyond `id`, `function` and `status`.

An administrator can also run a function by hand, internal ones included, with `backd.admin.invokeFunction('<database>/<name>', { input, as, idempotencyKey })` (see [Internal functions](../../functions/internal/#running-a-function-by-hand)); it returns the output or a `Job`, like `db.fn()`.

An `Idempotency-Key` makes a retried call safe (see [Idempotency](../../functions/calling/#idempotency)):

```js
await backd.db('shop').fn('charge', { amount: 1250 }, { idempotencyKey: `order-${orderId}-charge` })
```

`fn`'s third argument also accepts `{ retry, signal, headers }`, same as [collections calls](#retries). `webhook` functions aren't called through `fn` — they're invoked by the sender, not the client.

## Token storage

By default the token is kept in memory: it is gone after a reload, and nothing on the page can read it from storage. To keep users signed in across reloads, choose a storage:

```js
import { createClient, localStorageStorage } from 'backd-js'

const backd = createClient({ url, realm, storage: localStorageStorage() })
```

{{< hint warning >}}
Any script running on the page can read `localStorage`, so an app that stores tokens there must protect itself against cross-site scripting (a strict Content-Security-Policy, no untrusted HTML). The default in-memory storage is safer; you trade it for staying signed in across reloads.
{{< /hint >}}

Any object with `get()`, `set(token)` and `remove()` works as storage, and the methods may be async, for example to use a mobile app's secure storage:

```js
const storage = {
  get: () => SecureStore.getItemAsync('backd'),
  set: (token) => SecureStore.setItemAsync('backd', token),
  remove: () => SecureStore.deleteItemAsync('backd'),
}
```

`await backd.auth.token()` returns the stored token, or `null`.

## Errors

Every answer that isn't 2xx throws a `BackdError`, or one of its subclasses:

| Class | Status | Typical `code` |
|---|---|---|
| `ValidationError` | 400 | `validation_error`, `invalid_json`, `invalid_query`, `invalid_header` |
| `AuthenticationError` | 401 | `unauthenticated`, `invalid_credentials` |
| `ForbiddenError` | 403 | `forbidden` |
| `NotFoundError` | 404 | `not_found` |
| `ConflictError` | 409 | `email_taken`, `conflict`, `write_conflict` |
| `VersionMismatchError` | 412 | `version_mismatch` |
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

A client with an API key with the `admin` role (`backd apikey create --realm <realm> --name <name> --role admin`) can use the [admin API](../../auth/admin/):

```js
const backd = createClient({ url, realm: 'blog', apiKey: process.env.BACKD_API_KEY })

const ada = await backd.admin.users.find('ada@example.com')      // or null
const page = await backd.admin.users.list({ limit: 50, skip: 0 })
const user = await backd.admin.users.create({ email: 'bob@example.com', password })  // password optional
await backd.admin.users.get(user.id)
await backd.admin.users.update(user.id, { emailVerified: true, disabled: false })
await backd.admin.users.setPassword(user.id, newPassword)        // ends their sessions
await backd.admin.users.addRole(user.id, 'editor')               // roles declared in realm.yaml
await backd.admin.users.removeRole(user.id, 'editor')
await backd.admin.users.owned(user.id)                          // what erasing them would do, per collection with a policy
await backd.admin.users.delete(user.id)                         // ERASES them (irreversible): a tombstone, and the collections' policies; resolves with the erase job

const invitation = await backd.admin.invitations.create({ email: 'eve@example.com', expiresIn: '3d' })
sendInvitationEmail(invitation.token)                              // shown only here
await backd.admin.invitations.send({ email: 'eve@example.com', redirectTo, locale })  // backd emails it: no token
await backd.admin.users.changeEmail(user.id, 'new@example.com')   // at once; both addresses are told
await backd.admin.invitations.list()
await backd.admin.invitations.revoke(invitation.id)

const secrets = await backd.admin.secrets.list()                  // scope, name, who set it; never the values
await backd.admin.secrets.set('STRIPE_KEY', value, { database: 'main' })   // omit database for a realm secret
await backd.admin.secrets.delete('STRIPE_KEY', { database: 'main' })
const history = await backd.admin.invocations.list({ function: 'main/refund', limit: 20 })   // function calls, with origin and parent_id

const created = await backd.admin.apiKeys.create({ name: 'billing', role: 'data', expiresIn: '90d', networks: ['203.0.113.0/24'] })
storeSecret(created.key)                                           // shown only here
await backd.admin.apiKeys.list()                                   // never the keys themselves
await backd.admin.apiKeys.revoke('billing')
await backd.admin.users.setNetworks(user.id, { loginNetworks: ['10.20.0.0/16'] })   // [] removes

const trail = await backd.admin.audit.list({ target: `user:${user.id}`, since: new Date(Date.now() - 864e5) })
trail.items                                                        // newest first; read-only
const jobs = await backd.admin.jobs.list({ function: 'main/nightly_cleanup', status: 'done', limit: 10 })   // state and outcome, never input or output
```

A client signed in as a user holding one of the realm's [admin roles](../../configuration/realm/#roles) can use `client.admin` too, for example in a back-office app. Calling `client.admin` without an API key or a session throws at once; the server decides the rest. Acting on behalf of a user from the client is planned.
