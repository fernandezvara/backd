# Design: Dart client

- **Issues:** features 28.1–28.13 (milestone `28-dart-client`)
- **Status:** decided
- **Related designs:** [types-openapi.md](./types-openapi.md) (typegen and OpenAPI), [conditional-get.md](./conditional-get.md) (the revalidation cache), [triggers-and-realtime.md](./triggers-and-realtime.md) (the realtime protocol), [app-config.md](./app-config.md) (the `_config` cache), [mfa-passwordless.md](./mfa-passwordless.md), [organizations.md](./organizations.md), [team-access.md](./team-access.md), [query-language.md](./query-language.md), [file-storage.md](./file-storage.md), [custom-ids.md](./custom-ids.md), [compliance.md](./compliance.md), [account-lifecycle.md](./account-lifecycle.md)

## 1. The idea

`backd-js` proves the shape of a good client: one entry point per realm, the whole sign-in surface, collections speaking the query language, files, function jobs, organizations and teams, push devices, a cached `_config`, live views over one multiplexed realtime stream, an ETag revalidation cache, retries, and a typed-error hierarchy — all in a dependency-free runtime package that generated types sit on top of.

The Dart client is the same client written by hand, not generated: pure Dart (no Flutter dependency), so it runs in Flutter apps **and** in standalone Dart programs — CLIs, daemons, server-side Dart — exactly like `backd-js` runs in browsers and Node alike. Per-realm Dart types come from the same `internal/typegen` generator that emits TypeScript today, so the coherence rule holds: what `backd types` produces for TypeScript, it produces for Dart.

Three deliveries ride on the client: its own page on the docs site, a Flutter desktop demo of Shelf, and pub.dev publishing on `dart-v*` tags.

**Not in the client:** the admin API. Administration stays in the JavaScript client (functions, the CLI's own tooling and the admin UI). A shipped application must not be able to administer a realm, and every Dart doc says so — see §14.

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Language | Pure Dart (`dart:io` transport); Flutter is a consumer, not a dependency |
| Location / name | `clients/dart`, published as `backd` on pub.dev (`backd_dart` if the name is taken) |
| Parity | Everything `backd-js` does, except the admin API and browser-only modes (§14) |
| Admin API | JS-only by design; documented as a safety boundary |
| Types | `backd types --lang dart` from `internal/typegen`; same fingerprint, `--check` and `--diff` |
| Token storage | `TokenStorage` interface; memory and file implementations in the package; Flutter apps plug `flutter_secure_storage` or `shared_preferences` in |
| Realtime | SSE over `dart:io` `HttpClient`; one multiplexed `POST /_realtime`, `LiveView` semantics identical to JS |
| Cache | The same revalidation cache (ETag/`If-None-Match`, credential-scoped) and the same `_config` cache |
| OAuth on desktop | The app owns the redirect (deep link or loopback server); the client exposes `signInWith` + `completeSignIn`, no `completeOnLoad` |
| Flutter web | Out of scope for v1 (SSE needs streaming responses `dart:html`/`package:web` fetch doesn't give us cleanly); a follow-up |
| Demo | Flutter desktop app in `clients/dart/example/` (own pubspec, path dependency; fallback `examples/flutter-shelf`) |
| Versioning | `dart-v*` tags → `publish-dart.yml`; `0.0.x` until parity, then aligned with server/`backd-js` releases |

## 3. Feature recopilation: the `backd-js` surface

This inventory is the parity target. Every module of `clients/js/src` is listed with its public surface and its Dart counterpart. `admin.js` is excluded on purpose (§14); `xhr.js` has no counterpart — Dart's streamed request bodies give upload progress without it.

| JS module | Public surface | Dart surface |
|---|---|---|
| `client.js` | `createClient(options)`: `url`, `realm`, `storage`, `retry`, `cache`, `configStorage`, `headers`, `fetch`, `apiKey`, `types`, `onStaleTypes`; request pipeline, `cacheStats()` | `BackdClient(...)` + `BackdClientOptions`; `httpClient` injectable like `fetch`; `X-Backd-Types` stale-types warning |
| `storage.js` | `TokenStorage` (`get`/`set`/`remove`, may be async), `memoryStorage()`, `localStorageStorage()`, `COOKIE_SESSION` | `TokenStorage` (async by signature), `MemoryTokenStorage`, `FileTokenStorage` (dart:io file); `COOKIE_SESSION` kept for protocol parity, unused by Dart in v1 |
| `errors.js` | `BackdError` + one class per error `code` (`ValidationError`, `AuthenticationError`, `ForbiddenError`, `NotFoundError`, `ConflictError`, `IdTakenError`, `InvalidIdError`, `OrgRequiresMFAError`, `SecondStepRequiredError`, `ConsentRequiredError`, `VerificationRequiredError`, `SessionEndedError`, `InvalidResumeTokenError`, `RetryableError`, `NetworkError`, …) | `BackdException` hierarchy, same names and `code` mapping; thrown, not returned |
| `auth.js` | `signup`, `login`, `signIn` (returns the step: session, `mfaRequired`, `enrollmentRequired`), `verifyMfa`, `acceptConsents`, `mfa.*` (`status`, `setup`, `confirm`, `disable`, `regenerateRecoveryCodes`), `magicLink.*` (`request`, `verifyCode`), `password.set`, `reauth`, `requestReauthCode`, `withProof`, `me`, `updateMe`, `sessions`, `revokeSession`, `logout`, `logoutAll`, `deleteAccount`, `changePassword`, email flows (`resendVerification`, `verifyEmail`, `requestPasswordReset`, `resetPassword`, `requestEmailChange`, `confirmEmailChange`, `revertEmailChange`), `acceptInvitation`, `signInWithIdToken`, `linkProviderWithIdToken`, `token()`, `hasSession()`, auth events (`signedIn`, `signedOut`, `expired`), `auth.ready` | `client.auth`, same method names in lowerCamelCase; events as a Dart `Stream<AuthEvent>`; `ready` as a `Future` |
| `oauth.js` | `signInWith`, `linkProvider`, `completeSignIn`, `parseReturn`, `OAuthStorage` (PKCE verifier) | `auth.signInWith`, `auth.completeSignIn(returnUri)`; `OAuthStorage` interface; no `completeOnLoad` (the app supplies the URI) |
| `consents.js` | `current`, `text`, `state`, `accept`, `withdraw` | `client.consents`, same |
| `data.js` | `client.db(name).collection(name)`, `.inOrg()`, `.inTeam()`, `list` (`where`, `orderBy`, `limit`, `skip`, cursor, `expand`, `file_links`), `iterate`, `get`, `create`, `replace`, `patch`, `delete`, `restore`, `ifMatch`, `batch`, custom ids | `client.db('main').collection('posts')` — identical call chain; `where`/`orderBy` as typed builders or maps (typed mode uses generated types); `iterate` returns `Stream<Doc>` |
| `expand.js` | `expand()` builder for `expand` strings | `Expand` builder |
| `buckets.js` | `fillBuckets` for aggregation gaps | `fillBuckets` top-level function |
| `files.js` + `sha256.js` | `collection.files(id, field)`: `list`, `get`, `put`, `delete`, `link`; `version(name, fileId)`; `uploadFile`, `prepareUpload` (direct upload), `fileUrl`, `downloadFile`, `deleteFile`; `Sha256`/`sha256Blob` checksums; `versionStatus` | Same names under `collection.files(id, field)`; upload progress via a `Stream<double>`/callback on streamed bodies; `package:crypto` (Dart team) for SHA-256 |
| `functions.js` | `collection`/`db` `fn(name)` → `call`, `job(id)` → `Job`: `status`, `wait` (polls), `steps`/`data` snapshots, `JobTimeoutError` | `client.functions.call('db/name', input)`, `Job` with `status()`/`wait()` and the latest `steps`/`data`, `JobTimeoutException` |
| `orgs.js` | `client.orgs`: `create`, `list`, `get`, `update`, `leave`, `delete`; `members(org)` (`list`, `update`, `remove`); `teams(org)` (`list`, `create`, `update`, `delete`, `members`, `grantRole`, `setRoleWindow`, `revokeRole`, `addMember`, `removeMember`); `templates(org)` (`list`, `get`, `put`, `delete`, `preview`, `test`); invitations (`create`, `list`, `revoke`, `newLink`, `send`) | `client.orgs` — same structure and names |
| `push.js` | `register`, `devices`, `unregister`, `registerOnLaunch(getToken)`, `subscribeWeb` | `client.push`: `register`, `devices`, `unregister`; `registerOnLaunch` takes a token-provider callback. `subscribeWeb` excluded (browser-only) |
| `config.js` | `client.config`: `load`, `get`, `getSync`, `on` events, `view`, `fetch`, `revalidate`, persisted copy (`ConfigStorage`), realtime-driven `fresh` updates | `client.config` — same; `ConfigStorage` interface; changes as `Stream`s; in-memory default with optional file/`SharedPreferences` persistence |
| `realtime.js` | `client.realtime`, `LiveView`: multiplexed `POST /_realtime`, `SubscriptionSpec` (`where`, `doc`, `load`, `references`), `LiveState`, resume tokens, reset/reload with jitter, reconnect backoff, credential-lifetime reconnect | Identical protocol and `LiveView` semantics; SSE parsed by hand from the streamed response; state as `Stream<LiveState>` |
| `cache.js` | `RevalidationCache`: ETag + body per read, `If-None-Match` → `304`, credential-scoped (`scope`), LRU by entries and bytes, `cacheable()` rules, `stats()` | `RevalidationCache` ported 1:1, same defaults (200 entries, 5 MB) and same `cacheable` rules |
| `typed.js` | `createClient<Realm>()`: databases, collections, docs, `where`/`orderBy`, `expand`, aggregations, functions, config paths | `BackdClient<Realm>` with generated Dart types (§12); untyped `Map<String, Object?>` without it |

## 4. The package (issue 28.1)

```text
clients/dart/
├── pubspec.yaml            # name: backd
├── lib/
│   ├── backd.dart          # the barrel: BackdClient, all public types
│   └── src/                # client.dart, auth.dart, data.dart, files.dart,
│                           # functions.dart, orgs.dart, push.dart, consents.dart,
│                           # config.dart, realtime.dart, cache.dart, storage.dart,
│                           # oauth.dart, errors.dart, expand.dart, buckets.dart
├── test/                   # dart test, mocked HttpClient
└── example/                # the Shelf Flutter demo (§13), own pubspec, path dep
```

- **Dependencies:** kept to a minimum, mirroring `backd-js`'s "no dependencies" stance. `dart:io`, `dart:convert` and `dart:async` do the transport, JSON and streams. The only package dependencies allowed are Dart-team packages — `package:crypto` for file checksums; SSE and multipart are parsed by hand (both are small).
- **Transport seam:** `BackdClient` takes an optional `HttpClient`-compatible transport (like the JS `fetch` option) so tests mock it and unusual deployments plug their own. Upload progress comes from streaming the request body — no XHR equivalent needed.
- **Platforms:** `dart:io` covers Flutter mobile and desktop, and standalone Dart. Flutter web is declared unsupported in the README and docs (see follow-ups).
- **Linting:** `package:lints/recommended.yaml`, `dart analyze --fatal-warnings` in CI; `dart doc` kept warning-free for the pub.dev score.

### 4.1 API shape

JS names map to idiomatic Dart without redesigning semantics:

```dart
import 'package:backd/backd.dart';

final backd = BackdClient(
  url: 'https://localhost:8443',
  realm: 'shelf',
  storage: FileTokenStorage('${dir.path}/backd.session'),
);

final session = await backd.auth.login(email: 'ada@example.com', password: '…');
final page = await backd.db('main').collection('assets')
    .list(where: {'published_at': {'\$notNull': true}}, orderBy: '-published_at', limit: 20);

await for (final state in backd.realtime.liveView(subscriptions: […])) { … }
```

- `Future<T>` where JS returns promises; `Stream<T>` where JS emits events (auth events, `LiveView`, config changes, job progress, `iterate`).
- The same option names (`where`, `orderBy`, `limit`, `expand`, `fileLinks`) so `js.md` knowledge transfers.
- `close()` releases the realtime stream and the HTTP client — required for standalone apps.

### 4.2 A standalone script

The pure-Dart story in one file — this doubles as the README's quickstart (§15): `dart pub add backd`, then `dart run`, against the tutorial's Shelf stack:

```dart
// bin/shelf.dart — `SHELF_EMAIL=ada@example.com SHELF_PASSWORD=… dart run bin/shelf.dart`
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
      email: Platform.environment['SHELF_EMAIL']!,
      password: Platform.environment['SHELF_PASSWORD']!,
    );
  }

  final page = await backd.db('main').collection('assets').list(
        where: {'published_at': {'\$notNull': true}},
        orderBy: '-published_at',
        limit: 20,
      );

  for (final doc in page.items) {
    final tags = (doc['tags'] as List? ?? []).join(', ');
    stdout.writeln('• ${doc['title']} [${doc['kind']}] ${doc['url'] ?? ''} $tags');
  }
  stdout.writeln('--- ${page.items.length} published assets${page.hasMore ? ', more…' : ''}');

  await backd.close();
}
```

Output:

```text
• Backd's query language [link] https://backd.dev/docs/build/api/querying docs, backend
• Release checklist [note] 
--- 2 published assets
```

Every option shown is a `BackdClient` constructor argument — the script is what the README points at when it explains `storage`, realm setup and sessions in standalone programs.

## 5. Auth and sessions (issue 28.2)

Every flow of `auth.js`, `oauth.js` and `consents.js` is ported: the `signIn` step machine (session, `mfaRequired`, `enrollmentRequired`), TOTP setup and recovery codes, magic links, reauth and `withProof`, the account lifecycle (verification, reset, change, revert, invitations, erasure), consent acceptance during sign-in, sessions management, and `auth.ready` as a `Future` completing once stored credentials are checked.

Two platform differences:

- **OAuth:** there is no page URL to parse on load. `auth.signInWith(provider)` returns the provider URL for the app to open (system browser); the app receives the redirect (deep link or loopback listener) and calls `auth.completeSignIn(uri)`. `OAuthStorage` keeps the PKCE verifier through the round trip.
- **Token storage:** `TokenStorage` is the same three-method interface. The package ships `MemoryTokenStorage` (default) and `FileTokenStorage` for standalone apps; Flutter apps typically pass `flutter_secure_storage` — kept out of the package so `backd` stays pure Dart.

`signInWithIdToken` / `linkProviderWithIdToken` carry over directly: native SDKs (Google Sign-In, Sign in with Apple) hand the app an ID token, no redirect needed.

## 6. Collections and the query language (issues 28.3, 28.4)

The full `data.js` surface: `list` with `where`/`orderBy`/`limit`/`skip`/cursors (`iterate` as a `Stream`), `get`, `create` (with custom ids and the `IdTakenError`/`InvalidIdError` answers), `replace`, `patch`, `delete`, `restore`, `ifMatch` for concurrent edits, `batch`, `expand` with field selection and `_expanded`, `inOrg`/`inTeam` scoping.

Aggregations and search follow the realm's declarations exactly as in JS: `aggregate` (with `fillBuckets`), `search` (`$search`, language, relevance). The same typed errors (`AggregateTooExpensiveError`, `SearchNotEnabledError`, …) map to the Dart hierarchy.

## 7. The caches (issue 28.5)

- **Revalidation cache:** `RevalidationCache` ported verbatim — ETag and body per document/list read, `If-None-Match` on re-reads, `304` replay, credential-scoped (`scope`), LRU bounded by entries (200) and bytes (5 MB), identical `cacheable()` rules (no `_`-paths, no `file_links`, no `expand`), and `client.cacheStats()`. Every read still asks the server; nothing is served stale.
- **`_config` cache:** `client.config` keeps the realm's app configuration like `config.js` does: one cached document, `get`/`getSync`/`view`, change events as a `Stream`, realtime-driven invalidation, optional persistence through `ConfigStorage` (file for standalone, `SharedPreferences`-backed for Flutter — supplied by the app).

## 8. Files (issue 28.6)

The `files.js` surface under `collection.files(id, field)`: list, get, `put` (upload through backd), delete, `link` (signed download URLs), `version(name, fileId)` for image/document versions and `versionStatus`; plus `uploadFile`, `prepareUpload` + the direct-upload handshake to S3-compatible storage, `fileUrl`, `downloadFile`, `deleteFile`, and SHA-256 checksums (`package:crypto`). Progress reports ride on streamed request bodies, both through backd and direct — the `xhr.js` workaround has no Dart equivalent.

## 9. Functions and jobs (issue 28.7)

`client.functions.call('db/name', input)` returns the result or a `Job`: `job.status()` and `job.wait(timeout)` poll, each leaving the reported `steps` and `data` on the `Job`, with `JobTimeoutException` on a timeout. Typed mode binds input and output from the generated function types (§12).

## 10. Organizations, teams, push, consents (issue 28.8)

`client.orgs` with members, teams (role grants, windows, membership), templates (preview/test) and invitations; `client.push` device registration (`register`, `devices`, `unregister`, `registerOnLaunch` taking a platform-token callback — FCM/APNs wiring stays the app's job); `client.consents` (`current`, `text`, `state`, `accept`, `withdraw`).

## 11. Realtime (issue 28.9)

Same protocol, same semantics as `realtime.js`: one multiplexed `POST /v1/{realm}/_realtime` stream per client, `LiveView` per subscription set, stream-first list loading (events during load applied after, ignoring stale versions), sorted live lists with `limit`, referenced documents kept beside the list, resume tokens after disconnects, server `reset` → reload with jitter, exponential backoff capped (`maxBackoffMs`), reconnect at credential lifetime end, stream reopen on subscription-set change. Dart-specific notes:

- SSE is parsed by hand from the `HttpClient` streamed response (the protocol is `data:`/`id:`/`event:` lines — small).
- `LiveView` exposes `Stream<LiveState>` and `close()`; statuses (`connecting`, `live`, `resyncing`, `closed`) unchanged.
- A standalone app keeps the stream as long as the process lives; a Flutter app ties it to `WidgetsBinding` lifecycle if it wants — the client does not (pure Dart: no `WidgetsBinding`).

## 12. Dart types (issue 28.10)

`internal/typegen` grows a Dart emitter next to the TypeScript one; `backd types --lang dart --out <dir>` writes `types.dart` plus the same `realm.json` fingerprint. `--check` and `--diff` work identically, and the client's stale-types mechanism carries over: `types:` option sends `X-Backd-Types`, `X-Backd-Types-Stale` triggers one `onStaleTypes` warning. Debug-vs-production distinction uses `dart.vm.product` instead of a bundler's build mode.

Mapping table (extends the §4.1 table of `types-openapi.md`):

| JSON Schema | Dart |
|---|---|
| `object` with `properties`, `required` | `class` with required/nullable fields, `fromJson`/`toJson` |
| `additionalProperties: false` | closed class |
| `array` / `items` | `List<T>` |
| `enum`, `const` | `enum` (named) or `String` constants |
| `type: ["string", "null"]` | `String?` |
| `oneOf`, `anyOf` | sealed class hierarchy |
| local `$ref` / `$defs` | named classes |
| `format: date-time` | `DateTimeString` (Dart 3 extension type on `String`) |
| unsupported keywords | `Object?`, generator warning |

Same backd-specific types: `_meta`, file-field details, `_expanded` per reference with field-selection narrowing, per-collection `where`/`orderBy` types, aggregation results, function input/output, `_config` paths, template data, organization roles. Path-addressed names plus friendly aliases with the same collision fallback. Untyped usage (`BackdClient()` with no type argument) works with `Map<String, Object?>` documents, mirroring the untyped JS path.

## 13. Shelf Flutter demo (issue 28.11)

A Flutter app in `clients/dart/example/` (own `pubspec.yaml` with a path dependency on `package:backd`; `examples/flutter-shelf` as the fallback if packaging rules push it out). Desktop targets only — Linux, macOS, Windows — and it does what the JS Shelf app does: sign-in (including the second factor and magic link), the member/curator flows, publishing and public links, files and shared links, app configuration, and the live pins chapter's realtime view. It compiles its realm types with `backd types --lang dart`, demonstrating the generator end-to-end, and CI at least analyzes and builds one desktop target (`flutter build linux`) so it can't rot.

## 14. Exclusions and the admin boundary

- **No admin API.** `admin.js` is not ported. Administration is for the operator's own tooling — the CLI, the admin UI, and functions — all JavaScript, all server-side or operator-run. A client application holding admin capability is a footgun: the key that unlocks it (`bdk_…`, full realm access) must never ship in an app. `dart.md`, the package README and the clients index all state this; a Dart program that truly needs the admin API calls the REST endpoints directly, knowingly.
- **No `apiKey` footgun either:** `BackdClient` accepts `apiKey` for parity (standalone Dart tools are the server-side of this world) but there is no `dangerouslyAllowBrowser` — pure Dart never runs in a browser in v1.
- **No browser-only modes:** `cookies: true` (HttpOnly cookie sessions), `completeOnLoad` and `push.subscribeWeb` have no meaning outside browsers and are not ported.
- **No Flutter web** in v1 (streaming responses need `dart:io`; a `package:web` transport is the follow-up).
- **No generated-model dependency:** the client never requires `types.dart`; generated types are opt-in, exactly as `createClient<Realm>()` is.

## 15. Documentation (issue 28.12)

- **`docs/content/docs/build/clients/dart.md`** — mirrors `js.md`: install (`dart pub add backd`), create-a-client table, sign-up/login/sessions and the whole step machine, OAuth on desktop and `signInWithIdToken`, token storage choices, collections and the query language, caches, realtime, files, functions, orgs/teams/push/consents, errors, retries — plus a prominent note: *the Dart client has no admin API by design; administration is JS-only*.
- **`clients/_index.md`** — Dart moves out of the "generated clients" row into its own row.
- **`clients/types.md`** — the `--lang dart` output: what's generated, `BackdClient<Realm>`, drift checks.
- **`build/api/openapi.md`** — the "what generated clients don't do" list now points at `backd` for Dart instead of implying generators are the only path.
- **Tutorial chapter 19 hint** — the Dart mention updated to the real client.
- **`clients/dart/README.md`** — package readme for pub.dev (install, the §4.2 standalone script as the quickstart, links to the guide).

## 16. Testing, CI and publishing (issue 28.13)

- **Unit tests:** `dart test` against the injected transport — the same strategy as `backd-js`'s mocked `fetch`; the cache, the step machine, SSE parsing and `LiveView` get dedicated suites.
- **Integration:** `make dart-integration` mirrors `make js-integration`: backd + MongoDB in Docker, the client exercised end-to-end including realtime and files.
- **Package check:** `dart pub publish --dry-run` and a `scripts/check-dart-package.sh` analog to `check-js-package.sh` in CI.
- **Publishing:** `.github/workflows/publish-dart.yml` mirrors `publish-js.yml`: `dart-vX.Y.Z` tags, tag-must-match-pubspec check, tag must be on `main`, tests first, then `dart pub publish` using pub.dev's OIDC trusted publishing (no stored token). Suffix tags (`dart-v0.0.2-rc.1`) publish as pre-releases.
- **Versions:** `0.0.1`–`0.0.x` while the surface settles; the versioning policy aligns `backd` with server/`backd-js` releases once parity is declared — so a `backd` X.Y always matches backd X.Y, like `backd-js` does.

## 17. Issues

| id | Issue | Depends on |
|---|---|---|
| 28.1 | Package scaffold: transport seam, errors, retries, options | — |
| 28.2 | Token storage, auth, sessions and the sign-in step machine | 28.1 |
| 28.3 | Collections and the query language | 28.1 |
| 28.4 | Aggregations and search | 28.3 |
| 28.5 | Revalidation cache and `_config` cache | 28.3 |
| 28.6 | Files: uploads, versions, links, progress | 28.3 |
| 28.7 | Functions and jobs | 28.3 |
| 28.8 | Organizations, teams, push devices, consents | 28.3 |
| 28.9 | Realtime: multiplexed SSE and `LiveView` | 28.3 |
| 28.10 | Dart emitter in `internal/typegen`, stale-types header | 28.1 |
| 28.11 | Shelf Flutter desktop demo | 28.9, 28.10 |
| 28.12 | Client documentation | 28.9, 28.10 |
| 28.13 | CI (unit + `dart-integration`) and pub.dev publishing | 28.1 |

**When to build:** any time; nothing here touches the server except the typegen emitter (28.10), which extends the existing generator without changing its TypeScript output.

## 18. Alternatives considered

- **openapi-generator output as "the" Dart client:** rejected — generated clients have no sign-in flows, no token storage, no caches, no realtime, no jobs; they were always the stopgap (types-openapi §13 follow-ups).
- **A Flutter plugin instead of pure Dart:** rejected — it would lock out standalone Dart (the same reason `backd-js` isn't browser-only) and pull Flutter into a library that needs none of it.
- **Porting the admin API for completeness:** rejected — the safety boundary is the point (§14); revisit if a Dart server-side admin SDK is ever wanted, as a separate package.
- **Hand-written Dart models instead of a typegen emitter:** rejected — two generators would drift; extending `internal/typegen` keeps one source of truth.
- **Waiting for Flutter web to design the transport:** rejected — the `dart:io` transport is the right v1 and the seam already allows a `package:web` variant later.

## 19. Follow-ups

- Flutter web transport (`package:web` streaming fetch) behind the same transport seam.
- Kotlin and Swift clients following this blueprint; their typegen emitters share the 28.10 work.
- A separate server-side admin SDK for Dart, if real demand appears.
- Version alignment with the server release train once parity is declared (28.13).
