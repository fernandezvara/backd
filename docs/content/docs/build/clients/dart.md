---
title: "Dart client"
description: "package:backd: auth, sessions, collections, files, jobs and realtime from Flutter apps and standalone Dart programs."
icon: "flutter"
weight: 620
toc: true
---

`backd` is a hand-written Dart client for `backd`, published on pub.dev as [`package:backd`](https://pub.dev/packages/backd). It is pure Dart — `dart:io` does the transport — so it runs in Flutter apps **and** in standalone Dart programs (CLIs, daemons, server-side Dart), exactly like `backd-js` runs in browsers and Node alike.

{{< hint style="warning" title="No admin API, by design" >}}
The Dart client has **no admin API**. Administration is for the operator's own tooling — the CLI, the admin UI and functions — all JavaScript, all server-side or operator-run. A client application holding admin capability is a footgun: the key that unlocks it (a `bdk_…` API key with the `admin` role, full realm access) must never ship in an app. A Dart program that truly needs the admin API calls [the REST endpoints](../../../operate/administration/admin/) directly, knowingly.
{{< /hint >}}

## Install

```sh
dart pub add backd        # or: flutter pub add backd
```

The package's only dependencies are Dart-team packages (`crypto` for file checksums); SSE and multipart are parsed by hand. It needs a `dart:io` platform — Flutter mobile and desktop, and standalone Dart. Flutter web is not supported yet (streaming responses need a `package:web` transport, which is a planned follow-up).

## Example app

The repository includes a Flutter desktop demo built with this client — [Shelf](https://github.com/fernandezvara/backd/tree/main/clients/dart-examples), the same app the [JavaScript tutorial](../../../tutorial/) builds: sign-in with the second-factor and magic-link steps, member and curator flows, publishing and public links, files, app configuration and the live pins view. It is also `backd types --lang dart` end-to-end: every call goes through the realm's generated types.

For a smaller start, the README's quickstart is [a standalone script](#a-standalone-script).

## Create a client

```dart
import 'package:backd/backd.dart';

final backd = BackdClient(
  url: 'http://localhost:8080',
  realm: 'shelf',
);
```

| Option | Default | Meaning |
|---|---|---|
| `url` | required | Base URL of `backd` |
| `realm` | required | The realm to talk to |
| `storage` | `MemoryTokenStorage` | Where the session token is kept (see [Token storage](#token-storage)) |
| `retry` | off | `RetryOptions(attempts:, maxDelay:)` for `429` and `503` answers (see [Retries](#retries)) |
| `cache` | on | `CacheOptions` bounds for the [revalidation cache](#revalidation-cache) |
| `configStorage` | memory | Where the [app configuration](#app-configuration) copy is kept |
| `headers` | none | Extra headers for every request |
| `httpClient`, `transport`, `streamTransport` | platform | Replace the transport — tests, or an exotic deployment |
| `apiKey` | none | An [API key](../../auth/api-keys/), for server-side tools (see [API keys](#api-keys)) |
| `types` | none | The realm's types fingerprint, for the dev-only [stale-types warning](#typed-client) |
| `onStaleTypes` | console | Who is called when the server says the types are stale |
| `oauthStorage` | memory | Where the PKCE verifier waits between [provider sign-in](#sign-in-with-a-provider-on-desktop) pages |

`close()` releases the realtime stream and the HTTP client — a standalone program calls it before exiting.

A web app on another origin needs that origin listed in the realm's [CORS settings](../../configuration/realm/#cors). A desktop app, which is not on an origin at all, is always let through.

## A standalone script

The pure-Dart story in one file — the package's README uses it as the quickstart:

```dart
// bin/shelf.dart — `SHELF_EMAIL=ada@example.com SHELF_PASSWORD=… dart run`
import 'dart:io';

import 'package:backd/backd.dart';

Future<void> main() async {
  final backd = BackdClient(
    url: 'http://localhost:8080',   // the tutorial stack: nginx in front of backd
    realm: 'shelf',
    storage: FileTokenStorage('${Platform.environment['HOME']}/.backd-shelf.session'),
  );

  // The token lives in the file above: only the first run logs in.
  if (!await backd.auth.hasSession()) {
    await backd.auth.login(
      Platform.environment['SHELF_EMAIL']!,
      Platform.environment['SHELF_PASSWORD']!,
    );
  }

  final page = await backd.db('main').collection('assets').list(
        where: {'published_at': {'$notNull': true}},
        orderBy: '-published_at',
        limit: 20,
      );

  for (final doc in page.items) {
    final tags = (doc['tags'] as List? ?? []).join(', ');
    stdout.writeln('• ${doc['title']} [${doc['kind']}] ${doc['url'] ?? ''} $tags');
  }

  await backd.close();
}
```

## Sign-up, login and sessions

```dart
final session = await backd.auth.signup('ada@example.com', 'dev-p4ssw0rd!', locale: 'en');
// or: await backd.auth.login(email, password)

final me = await backd.auth.me();              // id, email, emailVerified, roles, locale, identities, reauthMethods
await backd.auth.updateMe(locale: 'es');       // must be a language the realm lists
await backd.auth.reauth('password', password: password);  // a fresh proof, before sensitive actions
await backd.auth.changePassword(newPassword);                    // other sessions end
final sessions = await backd.auth.sessions();  // id, createdAt, lastUsedAt, expiresAt, current
await backd.auth.revokeSession(sessions[1].id);
await backd.auth.logout();                     // this session
await backd.auth.logoutAll();                  // every session of the user
await backd.auth.deleteAccount();              // needs a fresh proof; deactivates the account, all data is kept
```

- `signup` and `login` store the session token; every later request sends it as `Authorization: Bearer …`.
- In realms with `signup: invite`, pass the invitation: `signup(email, password, invitation: …)`.
- In a realm that [requires verified addresses](../../auth/sessions/#email-verification), `signup` creates the account and then throws a `VerificationRequiredError`: there is no session yet, and the user logs in after following the link in the email. A `login` before that throws a `ForbiddenError` whose `code` is `email_not_verified`. `redirectTo` names where the page after the link may send the user (it must be within the realm's `email.allowed_redirects`).
- `logout` always removes the stored token, even if the server can't be reached or already forgot the session.
- **Sensitive actions need a fresh proof.** `changePassword`, `requestEmailChange`, `deleteAccount`, `unlinkProvider` answer `401 reauth_required` without one. `auth.reauth(method: …)` takes the strongest proof the user has (`me.reauthMethods`: `totp` or `recovery_code` with a second factor, `password`, or `email_code` after `requestReauthCode()`), and the client sends the token it gave with those requests for the realm's `reauth.ttl` (5 minutes by default): one prompt covers a settings page. `withProof((methods) async => …, () => sensitive())` asks the proof only when the server wants it, then retries the action. See [Re-authentication](../../auth/reauth/).

The endpoints behind these calls, and their rules, are described in [Sign-up, login and sessions](../../auth/sessions/).

### Two-step sign-in and magic links

A user with a [second factor](../../auth/mfa/) doesn't get a session from their password alone. `signIn()` returns the step as a value; `login()` and `signup()`, which promise a session, throw a `SecondStepRequiredError` carrying the `challenge`.

```dart
final result = await backd.auth.signIn(email, password);
switch (result) {
  case MfaChallenge c:                       // c.methods: ['totp', 'recovery_code']
    await backd.auth.verifyMfa(c.challenge, 'totp', code);   // stores the session
  case EnrollmentChallenge c:
    // the user's role requires a second factor and they have none: set one up, no session yet
    final setup = await backd.auth.mfa.setup(challenge: c.challenge);   // secret, otpauthUri, qr (a PNG data URI)
    final done = await backd.auth.mfa.confirm(setupId: setup.setupId, code: code, challenge: c.challenge);
    // done.recoveryCodes (shown once) and done.session — or done.consent, the next step
  case ConsentChallenge c:
    await backd.auth.acceptConsents(c.challenge, {for (final p in c.consents) p.name: p.version});
  case Session s:
    break;                                   // signed in; s stores itself
}
```

The same steps come back from `completeSignIn()` after a provider or a magic link, and from `signInWithIdToken()`.

```dart
// A signed-in user's own second factor; changes need a fresh proof (see above).
await backd.auth.mfa.status();                    // enabled, methods, recoveryCodesLeft, required
final setup = await backd.auth.mfa.setup();       // show setup.qr, or the secret
final done = await backd.auth.mfa.confirm(setupId: setup.setupId, code: code);
await backd.auth.mfa.regenerateRecoveryCodes();
await backd.auth.mfa.disable();                   // not for a role that requires it

// A link and a code by email (the realm needs magic_link); no return route is needed.
final req = await backd.auth.magicLink.request('ada@example.com');
final result = await backd.auth.magicLink.verifyCode('123456');   // a SignInResult: the steps above

await backd.auth.password.set(newPassword);       // an account that signs in another way may have none
await backd.auth.password.remove();               // 409 last_sign_in_method when nothing else is left
```

Errors: `invalid_code` (a `ValidationError`, `details` has the attempts left), `invalid_challenge`, `mfa_locked` (a `RetryableError`, 429), `reauth_required` and, for a link or code, `invalid_magic_link`. A link opened on another device reports `wrong_device`, as for a provider. See [Multi-factor authentication](../../auth/mfa/), [Magic links](../../auth/magic-links/) and [Re-authentication](../../auth/reauth/).

### Account flows by email

In a realm with [`email`](../../functions/email/), the links in the emails open pages of `backd` that need no code. An app that wants **its own pages** points the links at them (`email.links` in `realm.yaml`) and posts the token from the link with these calls, none of which needs a session or starts one:

```dart
await backd.auth.resendVerification(email);                    // 202 whatever the address is
await backd.auth.verifyEmail(token);                           // the token of the verification link
await backd.auth.requestPasswordReset(email, redirectTo: …);   // 202 whatever the address is
await backd.auth.resetPassword(token, password);               // ends every session, verifies the address
await backd.auth.requestEmailChange(newEmail);                 // signed in; needs a fresh proof (reauth)
await backd.auth.confirmEmailChange(token);                    // the link sent to the new address
await backd.auth.revertEmailChange(token);                     // the link sent to the old one
await backd.auth.acceptInvitation(token, password);            // an emailed invitation
```

- A token that is expired, used or unknown throws a `ValidationError` whose `code` is `invalid_token`; a password the policy refuses throws a `ValidationError` naming `password` and leaves the token usable. A reset request past the realm's limits throws a `RetryableError`.
- `redirectTo` (here and in `signup`) must be within the realm's `email.allowed_redirects`.
- After `resetPassword`, `confirmEmailChange`, `revertEmailChange` and `acceptInvitation` the user logs in: none of them starts a session, and the first three end the sessions that exist.

The rules behind each flow are in [Sign-up, login and sessions](../../auth/sessions/).

### Sign in with a provider on desktop

In a realm that lists [providers](../../auth/providers/), a desktop app owns the whole round trip: `signInWith` hands back the provider's address for the system browser, the browser comes back to the app on a deep link (or a loopback listener), and the app gives the address to `completeSignIn`.

```dart
// A "Continue with Google" button:
final (:authorizeUrl) = await backd.auth.signInWith('google', redirectTo: 'acme://signed-in');
await launchUrl(Uri.parse(authorizeUrl));       // your browser-opening package

// …the app is opened on acme://signed-in?backd_code=… — hand the URI in:
final result = await backd.auth.completeSignIn(uri);   // a SignInResult, or null on an ordinary visit
```

- The client keeps the PKCE verifier in `oauthStorage` (memory by default — a Flutter app keeps its own between activations) between the two ends of the trip and trades the one-time `backd_code` for a session, stored like a login's. `backd_error` and `backd_linked` answers surface the same way.
- An address opened where the sign-in was not started (another device, another run) has no verifier to redeem the code with: `completeSignIn` throws a `ValidationError`, `code: 'wrong_device'`; show "open this on the device where you asked".
- `signInWith(provider, invitation: …, locale: …)` passes an invitation for realms with `signup: invite`. `linkProvider(provider, …)` is the same trip for a signed-in user; the answer carries `linked`. `unlinkProvider(provider)` is never the last way to sign in: `ConflictError`, code `last_sign_in_method`.
- Failures throw the error the code means: `account_exists` and `link_conflict` are a `ConflictError`; `signup_closed`, `email_not_verified` and `signin_refused` a `ForbiddenError`; `cancelled` an `AuthenticationError`; `invitation_invalid` and `email_required` a `ValidationError`; `provider_error` a `BackdException`. `code` is the code.

An app that signs in with the platform (Sign in with Apple, Google Sign-In) already holds the ID token and uses it directly:

```dart
final result = await backd.auth.signInWithIdToken('apple', idToken: token, nonce: nonce, authorizationCode: code);
final linked = await backd.auth.linkProviderWithIdToken('google', idToken: token, nonce: nonce);
```

`nonce` is the raw value the sign-in was started with. An invalid token throws an `AuthenticationError` (`invalid_token`). Apple's `authorizationCode` is required unless the provider sets `revoke_on_delete: false`.

### Auth events

```dart
final sub = backd.auth.events.listen((change) {
  // change.event: AuthEvent.signedIn | signedOut | sessionExpired; change.session
});
await sub.cancel();
```

`sessionExpired` fires when the server refuses the stored token: it expired, was revoked (for example by a password change on another device), or the user was disabled. The client removes the token, so the app can send the user to the login screen. A failed login doesn't fire any event.

## Collections

```dart
final posts = backd.db('main').collection('posts');

final page = await posts.list(
  where: {'published': true, 'title': {'$ilike': 'go%'}},
  orderBy: '-_meta.created_at',
  limit: 20,
  skip: 0,
  count: true,
);
// page.items, page.limit, page.skip, page.hasMore, page.nextCursor, page.total

// The next page: pass the cursor back, with the same where and orderBy
final next = await posts.list(
    where: {'published': true}, orderBy: '-_meta.created_at', limit: 20, after: page.nextCursor);

// Alternatives with $or (see Querying)
await posts.list(where: {
  '$or': [
    {'title': {'$icontains': 'go'}},
    {'body': {'$icontains': 'go'}}
  ]
});

await for (final post in posts.iterate(where: {'published': true})) {
  // every matching document; pages of 100 by default (set `limit` to change)
}

final post = await posts.get(id);
final created = await posts.create({'title': 'Hello', 'published': false});
await posts.patch(id, {'title': 'Hello again', 'subtitle': null});  // null removes a field
await posts.replace(id, {'title': 'Only this field now'});
await posts.delete(id);
```

- `where` and `orderBy` use `backd`'s [query language](../../api/querying/) as it is: the client only serializes them. Write the operators with a leading `\$` in Dart (`'\$ilike'`, `'published_at': {'\$notNull': true}`), and keep the map inside an `orderBy` — a bare string.
- Documents come back as `Map<String, Object?>`: your fields plus `id` and `_meta` (`created_at`, `updated_at`, `version`, and in realms with authentication `owner`, `created_by`, `updated_by`). `_meta.redacted` lists the [encrypted fields](../../encryption/) the caller may not see: they come back `null`, and that `null` means hidden, not empty. Generated realm types (see [Typed client](#typed-client)) turn them into classes.
- What a user sees and may change is decided by the collection's [access rules](../../auth/rules/). A document they can't read answers `NotFoundError`; an operation they can't do answers `ForbiddenError`, or `AuthenticationError` when they aren't signed in.
- `iterate` follows each page's `next_cursor`, so documents created, changed or deleted while it runs never shift a page, and the end is as fast as the start. A cursor can't follow an `orderBy` on arrays, objects or fields of mixed types: `iterate` then fetches by offset (`skip`), where documents created or deleted meanwhile can be skipped or repeated. `after` can't be combined with `skip`; see [paging with a cursor](../../api/querying/#paging-with-a-cursor).

### Revalidation cache

The client remembers the last body and `ETag` of each document and list it read, in memory, and sends them back as `If-None-Match` on the next read of the same URL. When nothing changed the server answers `304` without a body and the client returns the copy it holds: the query and the [rules](../../auth/rules/) still run for every read, so you never see more than the caller may read now, only less data on the wire.

```dart
final post = await posts.get(id);        // 200, kept with its ETag
final again = await posts.get(id);       // sends If-None-Match; a 304 gives the same document
backd.cacheStats();                      // hits, misses, entries, bytes

await posts.get(id, options: RequestOptions(cache: false));   // this read skips the cache
BackdClient(url: url, realm: realm, cache: CacheOptions(enabled: false));              // no cache at all
BackdClient(url: url, realm: realm, cache: CacheOptions(maxEntries: 50, maxBytes: 1024 * 1024));  // smaller bounds
```

- **Bounded:** 200 entries or 5 MB of bodies by default; the least recently used are dropped first.
- **Only document and list reads** are kept. Reads with `fileLinks` (their links expire) and answers marked `no-store` never are.
- **Per caller:** signing in, signing out or a different session or API key empties the cache, and so does `backd.clearCache()`. A successful write to a document drops its entry.
- **Functions** reach the API with the cache off.

For a client written by hand, or a mobile app, see [Revalidating a read](../../api/documents/#revalidating-a-read-if-none-match).

### Concurrent edits

Every write increases `_meta.version`. Pass the version you read as `ifMatch`, and the write throws a `VersionMismatchError` if someone changed the document in between:

```dart
final post = await posts.get(id);
try {
  await posts.patch(id, {'title': 'Edited'}, ifMatch: post['_meta']['version']);
} on VersionMismatchError {
  reloadAndAskUser();
}
```

`ifMatch` also accepts `'*'` (the document must exist) or a raw header value. Writes with `ifMatch` are safe to [retry](#retries).

### The trash

In a collection that [soft-deletes](../../api/documents/#soft-delete), `delete` marks the document instead of removing it, and the trash has its own calls:

```dart
await posts.delete(id);                                  // hidden from every read, restorable
final trash = await posts.list(deleted: DeletedDocs.only);     // DeletedDocs.include for both; needs the restore rule
final gone = await posts.get(id, deleted: DeletedDocs.only);   // _meta.deleted_at, deleted_by, purge_at
final back = await posts.restore(id, ifMatch: gone['_meta']['version']);  // ConflictError if a live document took its unique value
await posts.delete(id, purge: true);                     // for good, under the purge rule
```

### Expanding references

A collection that [declares references](../../configuration/config-dir/#references) can return the referenced documents next to the result. You ask for them with `expand`, on `get`, `list` and `iterate`; the client never adds it by itself:

```dart
final page = await posts.list(
  expand: expand([('author', ['name', 'avatar']), 'tags']),   // "author(name,avatar),tags", or write the string
  limit: 20,
);
final post = page.items[0];
post['_expanded']?['author'];    // {id, name, avatar}, or null when missing or not readable by this user
post['_expanded']?['tags'];      // [{id, ...}, null, ...] (positions kept), or {items, truncated: true}
```

- **`expand(...)`** takes names (`'author'`, `'author.company'` for a second level) and `(name, [fields])` records, and checks their form; the server checks them against the config.
- **`null`** is the same for a document that doesn't exist, one the user can't read and one of another organization. In a typed client (below) `_expanded` is typed from the collection the reference leads to.
- **Errors** are `InvalidExpandError` (`invalid_expand`: the `details` name the part) and `ExpandTooLargeError` (`expand_too_large`: ask for less). Both are `ValidationError`s. The [cost headers and limits](../../api/documents/#expanding-references) are the server's.
- **It isn't cached by the server**, and your [revalidation cache](#revalidation-cache) always asks again for an expanded read.

### Aggregations and search

`aggregate()` summarizes what the caller may read, with the fields the collection [declares](../../api/aggregations/); `search()` is a list sorted by relevance with the [text index](../../api/querying/#full-text-search):

```dart
final result = await orders.aggregate(
  where: {'status': {'\$in': ['paid', 'shipped']}},
  groupBy: ['paid_at:month'], tz: 'Europe/Madrid',
  metrics: ['count', 'sum:total'], orderBy: 'paid_at:month',
);
result.groups[0].key['paid_at:month'];    // '2026-09'
result.groups[0].metrics['sum_total'];    // '18234.5' (an exact decimal string)

final page = await articles.search('red "running shoes" -kids',
    where: {'status': 'published'}, limit: 10);
page.items[0]['_meta']['score'];          // the relevance; `language: 'es'` picks the stemming
```

- **`aggregate(...)`**: `groupBy` and `metrics` take a string or a list, `orderBy` a key or result name (`'-count'`), `tz` an IANA zone, `limit` at most the collection's maximum. Without `groupBy` the answer is one group with an empty `key`, for a counter.
- **`search(text, ...)`** takes a list's parameters except `orderBy` and `after`; pages with `skip` through the first 1,000 results.
- **Errors:** `AggregateNotEnabledError`, `InvalidAggregateError` and `SearchNotEnabledError` (400, `ValidationError`s), `AggregateTooExpensiveError` and `SearchTooExpensiveError` (422) and `IndexBuildingError` (503, a `RetryableError`: the text index is being rebuilt, and `retryAfter` says when to try again).
- **`fillBuckets(groups, field: 'paid_at', unit: BucketUnit.month, from: '2026-01', to: DateTime.now(), empty: {'sum_total': '0'})`** adds the buckets an aggregation left out, so a chart has a point for every day, week, month or year in the range. The groups must be grouped only by that one date field (`groupBy: 'paid_at:month'`); empty ones get `count: 0` and the metrics of `empty`; a `null` group stays first. `bucketKey(date, unit)` writes the key the server uses.
- **Typed:** in a client typed from `backd types`, `aggregate` only accepts the declared fields (a bucket only on dates), and a collection that declares no `aggregate:` has none.

### Custom ids

A collection that declares [`custom_id`](../../configuration/config-dir/#custom-ids) takes the id from the client: pass it in the document. Nothing is overwritten, and a typed client requires it:

```dart
final invoices = backd.db('billing').collection('invoices');
await invoices.create({'id': 'INV-2026-0042', 'customer': 'Ada', 'total': 120});
await backd.db('main').collection('profiles').create({'id': user.id, 'bio': '…'});   // custom_id: user

try {
  await invoices.create({'id': 'INV-2026-0042', 'customer': 'Bob', 'total': 5});
} on IdTakenError {
  pickAnotherNumber();                          // 409 id_taken
} on InvalidIdError catch (e) {
  print(e.code);                                // id_required | invalid_id | id_not_allowed
}
```

A `429` after repeated `IdTakenError`s is a `RetryableError`: the [duplicate limit](../../api/documents/#custom-ids). `batch` creates take `id` on the operation. See [the API](../../api/documents/#custom-ids) for the order of the checks.

### Safe retries of creates

`create()` and `db.batch()` take an `idempotencyKey`. The server remembers the first answer for 24 hours and returns it for the same key and body, so a retry never makes a second document (see [Safe retries](../../api/documents/#safe-retries-with-idempotency-key)). A request with a key is also retried by the client itself after a network error, `429` or `503`, when `retry` is on:

```dart
final order = await orders.create(draft, idempotencyKey: 'checkout-$cartId');
```

{{< hint style="tip" title="Best practice" >}}
Pass `ifMatch` for every update that depends on what the user saw: edit forms, counters, toggles. Without it, `replace` is last-write-wins and silently overwrites a change made after you read the document.
{{< /hint >}}

### Batch writes

`db.batch(operations)` creates, replaces, patches and deletes documents across the database's collections in one MongoDB transaction — either every operation applies, or none does:

```dart
final results = await backd.db('main').batch([
  BatchCreate(collection: 'orders', document: {'item': 'widget', 'qty': 2}),
  BatchPatch(collection: 'stock', id: stockId, patch: {'qty': 8},
      ifMatch: stockDoc['_meta']['version']),
]);
final order = results[0], stock = results[1];
```

- Up to 100 operations. Each is validated and rule-checked exactly like a single write; `ifMatch` works the same as above.
- If any operation fails, the whole batch throws (the usual error types — `ValidationError`, `ForbiddenError`, `VersionMismatchError`, ...) and nothing was written. A version mismatch means re-reading and retrying the whole batch, not just one operation.
- See [Batch writes](../../api/documents/#batch-writes) for the full request/response shape.

## Files

Files live in the file fields of a collection's documents ([File fields](../../files/file-fields/)). The collection handle has a method for each thing an app does with them, and each is a request to the field's [`_files` routes](../../files/transfers/), under the signed-in user's rules.

```dart
final docs = backd.db('app').collection('claims');

// Upload into a document. Through backd or straight to the bucket: the client asks the field.
final doc = await docs.uploadFile(id, 'receipts', bytes, name: 'receipt.pdf',
    onProgress: (p) => bar = p.loaded / p.total);

// Upload before the document exists, then name the upload in the create.
final up = await docs.prepareUpload('receipt', bytes, name: 'receipt.pdf');
await docs.create({'title': 'Taxi', 'receipt': up.ref});   // up.ref is {upload, token}

final link = await docs.fileUrl(id, 'receipts', fileId);   // {url, expiresAt} — a link for an Image.network or a button
final file = await docs.files(id, 'receipts').get(fileId: fileId);   // file.bytes: save them, show them
final one = await docs.get(id, fileLinks: true);           // every file carries url and expires_at
await docs.deleteFile(id, 'receipts', fileId);
final info = await docs.fileField('receipts');             // upload, maxSize, types, multiple, maxFiles, …
```

- **`uploadFile(id, field, file, ...)`** takes `List<int>` bytes or a `String` (UTF-8 text) and resolves with the updated document. A `name` and `type` can be given; they default to the data's own. A field with `upload: direct` is sent straight to the bucket: the client reads the file once to compute its SHA-256 (incrementally, so a large file isn't held in memory), declares it, sends it with the signed link and completes it, and backd verifies the result. Options: `name` and `type` (default to the data's own), `ifMatch` and `onProgress`.
- **`prepareUpload(field, file, ...)`** does the same for a document that doesn't exist yet and resolves with `token`, `ref`, `expiresAt` and `file`. Put `ref` in the file field of a `create` (or a `replace` or `patch`; a list of them for a `multiple` field). An upload is used once and expires after the realm's `pending_ttl`.
- **`onProgress(UploadProgress p)`** reports the bytes sent as they go — `p.loaded` and `p.total` — because Dart streams request bodies. (The JavaScript client needs XMLHttpRequest for the same.)
- **`fileUrl(id, field, fileId)`** resolves with a link that works without credentials until `expiresAt`. Fetch it when it is needed: see [Download buttons](../../files/apps/#download-buttons).
- **`fileUrl(id, field, fileId, version: 'thumb')`** links a made [version](../../files/file-fields/#image-versions) of an image, and resolves with **`null`** while it isn't ready (`files(id, field).get()` and `.link()` take `version` too and throw `version_unavailable`). **`versionStatus(file, 'thumb')`** reads a version's `{status, ready, reason, width, height}` from a file's details, to show "processing…" and reserve room for the picture. With `fileLinks: true` each ready version also has its own `url`.
- **`files(id, field).version(name)`** has `regenerate()`, `generate(params)` and `delete()` for a made version: they wait for a worker and resolve with the version's state (see [Versions of an image](../../files/functions/#versions-of-an-image)); they are what a function uses.
- **`files(id, field).get(fileId: …)`** fetches the file's bytes — save them to disk, or hand them to `Image.memory`. It throws `version_unavailable` for a file that isn't ready yet.
- **`fileLinks: true`** on `get` and `list` adds `url` and `expires_at` to every file. Links expire; use it for what is shown at once.
- **`downloadFile(id, field, fileId)`** resolves with a fresh link, like `fileUrl` but never `null`: open it with `url_launcher` or hand it to an HTTP client. (The JavaScript client navigates the browser; a Dart app has no page to navigate.)
- **`deleteFile(id, field, fileId, {ifMatch})`** removes one file. `files(id, field).delete()` clears the field.
- **`files(id, field)`** is the lower level: `put`, `get` (the bytes), `link`, `delete` and `list`, which is also what a [function](../../files/functions/) uses.
- **`fileField(field)`** says how the field takes files (asked once, then remembered): to show `max_size` and `types` and refuse a file before sending it.

Errors are the client's: `unsupported_file_type` (`415`), `payload_too_large` (`413`), `too_many_files` (`409`), a `NotFoundError` for a document the user can't read, and for a direct upload `upload_failed` when the bucket refuses the file.

## Functions

`db.fn(name, input, ...)` calls a [function](../../functions/). A `sync` function's output comes back directly:

```dart
final receipt = await backd.db('shop').fn('checkout', {'cart': 'c1'});
// {order: 'o7', total: 1250}
```

An `async` function instead returns a `Job` handle, without waiting for it to finish:

```dart
final job = await backd.db('shop').fn('reconcile', {});
// job is a Job — but typed as Object? like a sync answer, so check with `is Job`
job.id;          // "d3c9ljp8hc2g00b6s1m0"
job.function;    // "shop/reconcile"

await job.status();                     // 'queued', 'running' or 'done'
final output = await job.wait(pollInterval: Duration(milliseconds: 500), timeout: Duration(seconds: 30));
// polls GET .../_jobs/{id} until status is 'done', then resolves like a sync call would
```

- `wait` polls every `pollInterval` (default 500 ms) and resolves with the job's output once it's `done`. If the job itself fails — the function threw, timed out, or crashed — `wait` (and a plain `sync` call) throws the same [error](#errors) a direct call would: a `BackdException` subclass built from the job's own status/code/message, so callers don't need to branch on sync vs. async to handle failures.
- If `timeout` is set and elapses before the job is done, `wait` throws `JobTimeoutError` (`.jobId`) instead — a plain `Exception`, not a `BackdException`, since giving up on polling isn't something the server reported. The job itself keeps running; call `wait` again, or `status()`, to check on it later.
- With no `timeout`, `wait` polls until the job finishes, however long that takes (up to the function's own `timeout`).
- `job.steps` are the [steps](../../functions/jobs/#reporting-progress-from-a-long-job) the function reported as of the last `status()` or `wait()` (the last is the current step), and `wait(onProgress: (steps, job) => …)` calls you after every poll.
- `job.data` is the last `GET .../_jobs/{id}` response `status()`/`wait()` fetched, if you need fields beyond `id`, `function` and `status`.

A `sync` function can be started as a job by the caller, with `respondAsync: true` (sends `Prefer: respond-async`, see [Calling functions](../../functions/calling/#asking-for-a-job-prefer-respond-async)): the call resolves to a `Job`, and `job.wait()` gives the output or throws the function's error, exactly as above.

```dart
final job = await backd.db('shop').fn('checkout', {'cart': 'c1'}, respondAsync: true);
final receipt = await job.wait();
```

An `Idempotency-Key` makes a retried call safe (see [Idempotency](../../functions/calling/#idempotency)):

```dart
await backd.db('shop').fn('charge', {'amount': 1250}, idempotencyKey: 'order-$orderId-charge');
```

`fn`'s optional arguments also accept `options: RequestOptions(...)`, same as [collections calls](#retries). `webhook` functions aren't called through `fn` — they're invoked by the sender, not the client.

### Triggers

A [trigger](../../configuration/config-dir/#triggers) runs an internal function when a collection's documents change, in the background and without a client in the loop, so **there is nothing to call**: the client has no `triggers` surface, by design.

## Live lists

`client.realtime.subscribe()` follows collections that declare [`realtime: true`](../../api/realtime/): it loads each list, keeps it current from the stream and tells you when it changes.

```dart
final live = backd.realtime.subscribe([
  SubscriptionSpec(
    db: 'main', collection: 'items',
    where: {'_meta.org': orgId},
    sort: '-_meta.created_at', limit: 50,
  ),
]);
live.on('change', (state) => render(state.items));   // sorted, reconciled
live.on('status', (s) => showBanner(s));             // LiveStatus.connecting | live | resyncing | closed
await live.ready;                                     // the first load
live.close();
```

What the client does for you:

- **One stream per client**, multiplexed over every view's subscriptions (the server allows 20 per stream). Adding or closing a subscription reopens it with the last resume token.
- **Stream first, then load:** the stream opens, each list is read with a normal list call, and the events that arrived meanwhile are applied after it, **ignoring any whose `_meta.version` is not newer**. New documents are inserted in the `sort` order, and the list is trimmed to `limit` (100 by default).
- **Resume:** after a dropped connection the stream continues after the last event, with the caller's current access; the list is not read again. A token the server refuses, or history it no longer holds, reloads the lists.
- **Resets:** when the server says `reset` (a slow client, a changed access, missing history) the affected list is reloaded after a random wait, while the last list stays available and the status is `resyncing`.
- **Credentials:** at the end of a credential's lifetime (`reauth`) the client reconnects with the current one. When the session is revoked (`session_ended`) the view stops and `error` carries a `SessionEndedError`; sign in again and subscribe again.
- **References:** `references: {'author': ReferenceSpec(field: 'author_id', collection: 'profiles', fields: ['name'])}` keeps the referenced documents in `state.references['author']` (by id), reading the missing ones in one batched call. They are read under the normal rules; subscribe to the referenced collection too if they must update live.
- **`doc:`** follows one document by id instead of a filter.
- **Errors** are typed: `RealtimeNotEnabledError` and `TooManySubscriptionsError` (`ValidationError`s), `InvalidResumeTokenError` (handled for you) and `SessionEndedError`.

A list bounded by `limit` can't refill itself when a document leaves it: a removal makes it shorter until the next reload, so subscribe with an equality on a grouping field and keep `limit` above what you show.

## Push notifications

`client.push` registers this device so that functions can push the signed-in user: `register(DeviceInput(...))`, `devices()`, `unregister(id:)` and `registerOnLaunch(getToken)`, which calls your token provider on start and registers (and re-registers when the provider rotates the token).

```dart
final stop = backd.push.registerOnLaunch(() async =>
    DeviceInput(platform: 'android', token: await getFcmToken()));
stop();   // stops registering (also fires on each sign-in)
```

It needs a user session. The platform tokens (FCM, APNs) and the rest are the app's job, and are in [Push notifications → From apps](../../push/client/). `subscribeWeb` exists only in the JavaScript client — it is a browser flow.

## Consents

A realm can ask people to accept [consents](../../auth/consents/): versioned texts such as terms, a privacy notice or marketing. `client.consents` reads them and records the answers.

```dart
// Before an account exists: what the realm asks for, and the exact text
final list = await backd.consents.current();
// [{name: 'terms', required: true, current: {version: '2026-10', reaccept: true}, locales: ['en', 'es']}, …]
final terms = await backd.consents.text('terms', '2026-10', locale: 'es');   // {text, hash, locale, …}

// At sign-up: the name and the version shown. Every required consent must be there
await backd.auth.signup(email, password, consents: {'terms': '2026-10', 'marketing': '1'});

// Signed in: what the user holds, accept or withdraw
await backd.consents.state();                        // consents, to_accept, history
await backd.consents.accept({'marketing': '1'});
await backd.consents.withdraw('marketing');          // optional consents only
```

- **At sign-in** a user who has to accept something (a required consent they never accepted, or a new version that asks for re-acceptance) gets a `ConsentChallenge` from `auth.signIn()`. `auth.login()` and `auth.signup()` promise a session, so they throw a `SecondStepRequiredError` with `code: 'consent_required'` and the same `challenge` and `consents`. Show the texts and finish with `auth.acceptConsents(challenge, {'terms': '2026-10'})`, which stores the session.
- **After a `reaccept_by` date**, a request of a user who hasn't accepted throws a `ConsentRequiredError` (`403`): show the prompt and send `client.consents.accept(...)`, which stays open for them.
- **Sign-ups through a magic link or a provider** take `consents` as an option (`auth.magicLink.request(email, consents: …)`, `auth.signInWith(provider, consents: …)`, `auth.signInWithIdToken(provider, …, consents: …)`); a new account made without them gets the challenge at its first sign-in.

## App configuration

`client.config` reads the realm's [app configuration](../../configuration/app-config/): feature flags, maintenance switches, limits. The server answers only what the signed-in user's rules allow, with defaults filled in.

```dart
await backd.config.load(['features', 'maintenance']);          // or load() for everything readable
backd.config.getSync('maintenance.enabled');                    // from the copy held
await backd.config.get('beta.new_editor', refresh: true);       // force a fetch
backd.config.changes.listen((paths) => rerender(paths));
```

- **`load(paths)`** fetches and keeps a copy, with no request while it is fresh. **`get(path)`** returns the value from the copy held; for a key whose group was never loaded it fetches that group first, and only then returns a future. **`getSync(path, [fallback])`** never asks: it returns the copy's value, or `fallback` (the client doesn't know the server's defaults).
- **Freshness:** a copy is fresh for the `cache_ttl` the server sent (5 minutes by default). After that, reads return the copy at once and revalidate it in the background with `If-None-Match`, which usually costs a `304`. `refresh: true` forces a fetch.
- **Where copies live:** per realm and per user, in memory by default; `configStorage` takes any object with `getItem`, `setItem` and `removeItem` (a Flutter app passes a `SharedPreferences`-backed one). The copy of an app that has just restarted is restored by the first `load()`.
- **Clearing:** sign-out and signing in as another user drop every copy of the realm, so the next person on the same device never sees the previous user's keys. `client.config.clear()` drops them on demand.
- **`changes`** is a `Stream<List<String>>` of the dot paths whose value changed when a read brings a different configuration.
- **`live([paths])`** follows the configuration over the client's [realtime stream](../../api/realtime/#following-the-configuration), so `changes` fires within the realtime delay instead of at the next expiry. It reads again only the branches you hold that the event touches, with `If-None-Match`, and after a reconnection it revalidates everything held. It returns a function that stops following, and follows the signed-in user: signing in or out opens the stream again with the new credential. If the realm declares no configuration, or realtime is off, the error arrives on the `errors` stream.

```dart
final stop = backd.config.live(['maintenance']);
backd.config.changes.listen((paths) => rerender(paths));
backd.config.errors.listen((e) => print('no live updates: $e'));
```

## Typed client

`backd types --lang dart` generates Dart classes from the config ([Generated types](../types/)): one `ShelfRealm` (named for the realm) with typed databases, collections, functions and configuration, plus an extension on `BackdClient<ShelfRealm>`:

```dart
import 'package:backd/backd.dart';
import 'backd/types.dart';   // generated

final client = BackdClient<ShelfRealm>(url: url, realm: 'shelf');

final page = await client.typed.main.assets.list(
  where: {MainAssetsField.publishedAt: {'\$ne': null}},
  orderBy: '-${MainAssetsField.publishedAt}',
);
page.items[0].kind;                          // MainAssetsKind.link | note | file
final out = await client.typed.main.publish(FnMainPublishInput(assetId: id));
final enabled = await client.typed.config.maintenanceEnabled(refresh: true);
```

- **What's typed:** databases and collections by name, documents as classes (`fromJson`/`toJson`, `id`, `_meta`, file fields as `FileDetails`, `_expanded` for the [references](#expanding-references) a collection declares), what `create` and `replace` accept (`MainAssetsInput`), `where` (field constants, and the operators each type allows) and `orderBy`, function calls (typed `input`, and result or [job](#functions) by mode; internal functions have no route and aren't offered) and configuration paths as getters on `typed.config`.
- **Field constants** (`MainAssetsField.title`, `MainAssetsField.metaCreatedAt`, …) name every queryable field: they keep a `where` or an `orderBy` from drifting from the schema as the config changes — regenerate and a renamed field stops compiling.
- **Typed collections** are `TypedCollection<D, I, C>` wrappers over the plain collection: reads decode into `MainAssets`, `create`/`replace` encode a `MainAssetsInput`, and `.raw` reaches the untyped handle. `iterate` gives a `Stream<MainAssets>`.
- **Without the type parameter** everything works untyped, as above. The runtime package is the same for every realm; `types.dart` is opt-in.
- **Stale-types warning (development).** Pass `types:` the fingerprint from `realm.json`, and the client sends it as `X-Backd-Types`; when the server's config has moved on it answers `X-Backd-Types-Stale: true` and the client warns once (or calls `onStaleTypes`). Nothing is refused. Send it only in development builds; without `types`, no header is sent. The fingerprint covers the whole realm, so generate without `--db`, or the warning never goes away.

```dart
import 'dart:convert';
import 'dart:io' show Platform;

final fingerprint = jsonDecode(File('lib/backd/realm.json').readAsStringSync())['fingerprint'];
final client = BackdClient<ShelfRealm>(
  url: url, realm: 'shelf',
  types: const bool.fromEnvironment('dart.vm.product') ? null : fingerprint,
);
```

## Token storage

By default the token is kept in memory (`MemoryTokenStorage`): it is gone when the process ends, and nothing else on the device can read it from storage. To keep users signed in between runs, choose a storage:

```dart
import 'package:backd/backd.dart';

final backd = BackdClient(
    url: url, realm: realm,
    storage: FileTokenStorage('${dir.path}/backd.session'));   // a standalone program's session file
```

- **`MemoryTokenStorage`** (default) loses the token when the process ends — the safest, and the right choice for a Flutter app that also wants an OS-kept secret: pass a `TokenStorage` that wraps [`flutter_secure_storage`](https://pub.dev/packages/flutter_secure_storage) or `shared_preferences` instead of keeping the token in a file.
- **`FileTokenStorage(path)`** keeps it in a file, for standalone programs. Plain text: keep it out of version control and out of a directory other users read.

Any object with `get()`, `set(token)` and `remove()` (each may be `FutureOr`) works as storage — `TokenStorage` is an interface:

```dart
final class SecureStorage implements TokenStorage {
  SecureStorage(this.inner);
  final FlutterSecureStorage inner;               // package:flutter_secure_storage

  @override
  Future<String?> get() => inner.read(key: 'backd');
  @override
  Future<void> set(String token) => inner.write(key: 'backd', value: token);
  @override
  Future<void> remove() => inner.delete(key: 'backd');
}
```

`await backd.auth.token()` returns the stored token, or `null`; `backd.auth.hasSession()` is the same check.

## Organizations

`backd.orgs` is the [organizations API](../../organizations/api/): the groups of users a person belongs to, with their members, teams and invitations.

```dart
final org = await backd.orgs.create({'name': 'Acme Corp', 'slug': 'acme'});
final mine = await backd.orgs.list();              // each with roles, require_mfa and mfa_met

await backd.orgs.members(org['id']).list();
await backd.orgs.members(org['id']).update(userId, ['editor']);   // direct roles
await backd.orgs.teams(org['id']).create({'name': 'Editors', 'roles': ['editor']});
await backd.orgs.teams(org['id']).addMember(teamId, userId);
final members = await backd.orgs.teams(org['id']).members(teamId, role: 'manager');   // {members: …} — holders of a team role, in force now
await backd.orgs.teams(org['id']).grantRole(teamId, {'user': userId, 'role': 'delegate', 'until': '2026-08-15T00:00:00Z'});
await backd.orgs.teams(org['id']).setRoleWindow(teamId, {'user': userId, 'role': 'delegate', 'until': '2026-08-31T00:00:00Z'});
await backd.orgs.teams(org['id']).revokeRole(teamId, {'user': userId, 'role': 'delegate'});

// An invitation answers the link once: show it, send it, or let the server email it.
final inv = await backd.orgs.invitations(org['id']).create(
    binding: 'email', email: 'ana@example.com', roles: ['editor'], send: true);
inv['link'];   // https://app.acme.example/join?token=…

// The person who got the link:
final offer = await backd.orgs.previewInvitation(token);   // {org, roles, teams}
await backd.orgs.acceptInvitation(token);
```

**Documents belong to an organization** when their collection says so: `inOrg(id)` scopes a collection, so creates go there and lists show only that organization's documents (the read rule still decides what is seen).

```dart
final projects = backd.db('main').collection('projects').inOrg(orgId);
await projects.create({'title': 'Plan'});
final page = await projects.list(where: {'status': 'open'});
```

`org` in `create` and `batch` options does the same for one call.

In a collection with [teams](../../organizations/team-roles/#documents-that-belong-to-a-team), `inTeam(id)` narrows a collection of an organization further: creates go to that team (the caller must belong to it or hold a team role in it, or the create throws `NotTeamMemberError`) and lists show only its documents.

```dart
final requests = backd.db('main').collection('requests').inOrg(orgId).inTeam(salesId);
await requests.create({'title': 'Leave'});
```

- **Deleting** an organization and **turning `require_mfa` on** ask for a fresh proof of identity: wrap them in [`backd.auth.withProof`](#sign-up-login-and-sessions), as other sensitive actions.
- **A second factor.** `mfa_met` is false for a session without one in an organization that requires it; the organization's calls then throw `OrgRequiresMFAError`. Leaving is still allowed.

## Errors

Every answer that isn't 2xx throws a `BackdException`, or one of its subclasses:

| Class | Status | Typical `code` |
|---|---|---|
| `ValidationError` | 400 | `validation_error`, `invalid_json`, `invalid_query`, `invalid_header` |
| `InvalidIdError` (a `ValidationError`) | 400 | `id_required`, `invalid_id`, `id_not_allowed`: the `id` of a [custom id](#custom-ids) create |
| `AuthenticationError` | 401 | `unauthenticated`, `invalid_credentials` |
| `PasswordExpiredError` (an `AuthenticationError`) | 401 | `password_expired`: the password is right, but an administrator expired it. `resetEmailQueued` says that backd queued the email with the link to choose a new one; when it is `false`, offer `auth.requestPasswordReset(email: …)` or tell the user to ask their administrator |
| `ForbiddenError` | 403 | `forbidden` |
| `NotFoundError` | 404 | `not_found` |
| `ConflictError` | 409 | `email_taken`, `conflict`, `write_conflict` |
| `IdTakenError` (a `ConflictError`) | 409 | `id_taken`: a document with that [custom id](#custom-ids) exists, and nothing was overwritten |
| `NotOrgMemberError`, `OrgRequiresMFAError`, `OrgRoleRequiresMFAError` (`ForbiddenError`s) | 403 | `not_org_member` (also for an unknown organization), `org_requires_mfa`, `org_role_requires_mfa` (`details` names the role) |
| `NotTeamMemberError` (a `ForbiddenError`) | 403 | `not_team_member`: not a member of the team and no team role there (the same for an unknown team) |
| `TeamError` (a `ValidationError`) | 400 | `team_required`, `team_not_allowed` |
| `FieldEncryptedError` (a `ValidationError`) | 400 | `field_encrypted`: a `where` or `orderBy` names an [encrypted field](../../encryption/), or asks more of it than equality; `details` name the field |
| `InvalidExpandError`, `ExpandTooLargeError` (`ValidationError`s) | 400 | `invalid_expand` (`details` name the part), `expand_too_large`: see [expanding references](#expanding-references) |
| `AggregateNotEnabledError`, `InvalidAggregateError`, `SearchNotEnabledError` (`ValidationError`s) | 400 | `aggregate_not_enabled`, `invalid_aggregate`, `search_not_enabled`: see [aggregations and search](#aggregations-and-search) |
| `AggregateTooExpensiveError`, `SearchTooExpensiveError` | 422 | `aggregate_too_expensive`, `search_too_expensive` |
| `IndexBuildingError` (a `RetryableError`) | 503 | `index_building`: the collection's text index is being rebuilt |
| `LastOrgManagerError` (a `ConflictError`) | 409 | `last_org_manager`: the organization needs someone who can manage it |
| `VersionMismatchError` | 412 | `version_mismatch` |
| `ConsentRequiredError` | 400, 403 | `consent_required`; `consents` lists what to accept, as `{name, version}` |
| `RealtimeNotEnabledError`, `TooManySubscriptionsError`, `InvalidResumeTokenError` (`ValidationError`s) | 400 | `realtime_not_enabled`, `too_many_subscriptions`, `invalid_resume_token`: see [live lists](#live-lists) |
| `SessionEndedError` (an `AuthenticationError`) | — | `session_ended`: the stream's session, user or key is gone |
| `SecondStepRequiredError` | — | `mfa_required`, `enrollment_required`, `consent_required`: `login`/`signup` asking for [a second step](#two-step-sign-in-and-magic-links) |
| `VerificationRequiredError` | — | `verification_required`: `signup` in a realm that verifies emails |
| `RetryableError` | 429, 503 | `too_many_requests`, `unavailable`; `retryAfter` in milliseconds |
| `NetworkError` | 0 | `network_error`, `aborted` |

Each error has `status`, `code`, `message`, `details` (the fields that failed validation, for example) and `requestId`, which matches `backd`'s logs.

```dart
import 'package:backd/backd.dart';

try {
  await backd.auth.login(email, password);
} on AuthenticationError {
  showMessage('Wrong email or password');
} on ValidationError catch (e) {
  showFieldErrors(e.details);
}
```

## Retries

Retries are off by default. When enabled, the client retries `429` and `503` answers, waiting as long as the server's `Retry-After` asks (otherwise 0.5 s, doubling), and never longer than `maxDelay`:

```dart
final backd = BackdClient(
    url: url, realm: realm,
    retry: RetryOptions(attempts: 2, maxDelay: Duration(seconds: 10)));
```

{{< hint note >}}
Only safe requests are retried: reads, and writes sent with `If-Match`. Other writes might already have taken effect, so their errors are thrown as they come, and a `POST` that timed out may or may not have created its document.
{{< /hint >}}

Every call also accepts `options: RequestOptions(retry: …, headers: …, cache: …)` as a last argument.

## API keys

For server-side Dart tools, `BackdClient(url: url, realm: realm, apiKey: …)` sends an [API key](../../auth/api-keys/) with every request. Access rules don't apply to it: collections calls see and change everything in the realm.

{{< hint danger >}}
An API key shipped in an app is readable by every user and bypasses all [access rules](../../auth/rules/). `apiKey` exists for standalone Dart tools — a migration, an import, a cron job — never for anything your users run. There is no `dangerouslyAllowBrowser`: pure Dart doesn't run in a browser anyway.
{{< /hint >}}

There is no `admin` surface on this client — see the note at the top of this page. An API key with a `data` role already reads and writes every collection; administration itself stays in the [JavaScript client](../js/#api-keys-and-the-admin-api).
