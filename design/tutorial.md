# Design: tutorial — the team asset library

- **Issues:** tutorial design → tutorial chapters (the file chapters 13–15: #165)
- **Status:** decided
- **Related designs:** [internal-functions.md](./internal-functions.md) (chapters 7–8), [email-delivery.md](./email-delivery.md) (chapters 8–10), [account-lifecycle.md](./account-lifecycle.md) (chapter 2), [file-storage.md](./file-storage.md) (chapters 13–15)

## 1. Why

`backd`'s docs explain features one at a time, and the examples show finished apps. What is missing is a **path**: a single application a new developer builds chapter by chapter, where each feature is added because the app genuinely needs it. That is how someone learns the product — and how the team finds out which features are hard to explain.

The tutorial builds a **team asset library**: members of a workspace save assets (links, notes and, from chapter 13, files), tag them, share them with expiring public links, and get a weekly digest. The domain is familiar, every current feature earns its place, and file storage lands later as a natural extension instead of a bolt-on.

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| App | "Shelf" — a team asset library; realm `shelf`, database `main` |
| Form | one page per chapter under a new **Tutorial** section of the docs site; each chapter ends in a working app |
| Code | one realm `examples/config/shelf` at its final state, plus the finished app under `examples/`; chapters are narrative diffs on top of it |
| Reader's setup | **no `backd` checkout:** the tutorial serves a starter (a `docker-compose.yaml` on the released image + the static app snapshot) from the docs site; readers write every config file themselves, chapter by chapter |
| Frontend | [Alpine.js](https://alpinejs.dev) + [PicoCSS](https://picocss.com): reactive pages with no build step, a classless stylesheet that fits a small CRUD app |
| Order | data → auth → rules → invitations → functions → internals/`ctx.call` → jobs → cron → email → webhooks → operate/harden |
| File management | chapters 13–15, written with file storage (roadmap 8.9); earlier chapters keep `assets.file` as a reserved schema property, which chapter 13 moves to `files:` |
| Testing story | `@backd/functions-testing` fakes for function chapters; `email-capture`/Mailbox for email; demo accounts like the workshop tour |

## 3. The application

A workspace ("shelf") holds **assets**: a URL or a text note, a title, free-form tags, and publication metadata. Members add and edit their own assets, curators publish them, everyone in the workspace reads, and a published asset can be shared publicly through an expiring link. A nightly digest emails each member what was published.

### 3.1 Data model (final state)

```text
shelf/main/assets
  id, _meta.owner, _meta.created_at, _meta.updated_at
  title        string, required
  body         string            # note text or URL description
  url          string            # for link assets
  kind         "link" | "note" | "file"   # "file" from chapter 13
  tags         string[]
  published_at string date-time, x-backd-store: date   # null while a draft
  downloads    integer           # chapter 15, written only by the download function (rules refuse clients)
  # file fields: declared in collection.yaml `files:`, not in schema.json (chapter 13)
  file         single, proxy upload, ≤ 25 MiB, documents/images; `versions: thumb` (256 px cover), made by a worker   # ch 13, 14
  attachments  multiple, max_files 5, upload: direct, ≤ 1 GiB          # ch 14

shelf/main/shares
  id, _meta.owner
  asset_id     string            # → assets.id
  token        string            # public slug in the share URL
  expires_at   string date-time, x-backd-store: date
```

Indexes: `assets` on `published_at` (gallery order) and `_meta.owner`; `shares` on `token` (unique) and `expires_at` (cleanup).

### 3.2 Functions (final state)

| Function | Mode | Shows |
|---|---|---|
| `publish` | sync, `admin: true`, `idempotency: required` | a privileged transition: validate, set `published_at`, call `notify` |
| `preview` | sync, `secrets:`, `network:` | fetch link metadata through the egress proxy |
| `notify` | internal sync | `ctx.call` callee: create a notification document |
| `digest` | async, `retry:`, `schedule: "@daily"` | job + cron: per-member digest via `ctx.email.send()` |
| `cleanup` | internal async, `schedule:` nightly | delete expired shares |
| `import` | `mode: webhook`, `invoke: "true"`, `rate_limit:` | receive assets pushed by an outside service |
| `deliver` | internal async, `retry:` | the realm's email delivery function |
| `download` | sync, `admin: true` | download function: increment `downloads` through `ctx.admin.db`, return `link()` (ch 15) |
| `share-open` | sync, `admin: true` (existing) | also returns a file link for a shared file asset via `ctx.admin.db … link()` (ch 15) |

## 4. Chapters

Each chapter: goal in one line, the features it exercises, a working state, and an acceptance ("you should see…"). Chapters reuse the workshop tour's inspector pattern where it helps (show the request, the answer, the function log, a `curl`).

### Ch 1 — An empty shelf: realm, collection, first document

- Write `realm.yaml`, `assets/schema.json`, `indexes.json`; `format: date-time` + `x-backd-store: date`; why dates are stored as BSON.
- Read/write through the JS client; anonymous reads of published assets only.
- Filters, `order_by`, `skip`, cursors in a paginated gallery.
- *See:* a page listing assets you typed by hand.

### Ch 2 — Members: sign-up to account lifecycle

- `auth:` on; sign-up, login, sessions, password change.
- Email verification and password reset land later in Ch 10; for now the Mailbox.
- Roles seeded from `realm.yaml`; first admin via `backd bootstrap`.
- *See:* sign up, sign in, a profile page, "delete my account" with the `on_owner_delete` erasure policy explained (it works, is exercised at the end).

### Ch 3 — Who may touch what: access rules

- `rules:` in `collection.yaml`: owner writes own assets; members read published + own; curators extra rights.
- The limits of rules: what they can express, what leaks (invite the reader to find the hole — sets up Ch 5).
- *See:* two demo users, one blocked where the other isn't.

### Ch 4 — Bringing the team in: invitations

- Invitation emails, `bound_to_email`, accepting an invite, roles on invite.
- *See:* invite a second account from inside the app.

### Ch 5 — The first function: publish

- `publish` (sync): `admin: true` for the privileged write, `idempotency: required` for the double-clicked button, input schema, `ctx.error`.
- What changed vs the Ch 3 hole; `functions history` to see the run.
- *See:* a "Publish" button that can't double-publish.

### Ch 6 — Reaching the outside: secrets and network

- `preview` fetches a page title/OG image for link assets: `secrets:`, `network:` allowlist, egress proxy, private-address refusal.
- *See:* paste a URL, get a fetched title.

### Ch 7 — Functions calling functions: internals and `ctx.call`

- `notify` as an internal function; `calls:` in `publish`; identity inheritance; why `invoke`/`rate_limit` don't apply inside; the 404 behavior.
- *See:* publish → notification appears; `_func/notify` answers 404; startup rejects a made-up `calls` entry.

### Ch 8 — Work that takes time: async jobs

- `digest` as `mode: async`: job handle, `status()`/`wait()`, `retry:` with a forced failure, history shows attempts.
- *See:* run a digest, watch the job page, see a retried attempt.

### Ch 9 — On a schedule: cron

- `schedule:` on `digest` and `cleanup`; `backd functions invoke` to run them by hand as admin.
- *See:* expired shares disappear after a manual cleanup run.

### Ch 10 — Email for real: delivery, templates, Mailbox

- `email:` in `realm.yaml`, the `deliver` function contract, `backd template realm`, locales, `ctx.email.send()` inside `digest`.
- Two delivery alternatives, both shown: `deliver` follows the Postmark recipe for a real provider (`secrets:`, `network:`); `email-capture` is the `dev_only` default that stores messages in an outbox for the Mailbox. Verify-email and reset-password flows now work end to end (revisits Ch 2).
- *See:* click a verify link out of the Mailbox; receive a digest.

### Ch 11 — Being called by the internet: webhooks

- `import` webhook: raw request, signature check, dedup by event id, anonymous `invoke`, `rate_limit`, why a webhook can't be internal.
- *See:* a `curl` (or a tiny fake provider script) posts an asset in.

### Ch 12 — Running it: operators and hardening

- API keys, admin API from the app (activity feed = audit trail rendered), `functions invoke`, `admin_networks`.
- The hardening checklist applied to Shelf: CORS, rate limits on anonymous endpoints, secrets backup, `dev_only` where needed.
- *See:* a `/admin` page showing audit entries for the actions you just did.

### Ch 13 — Files on assets

- Starter stack gains MinIO; `storage:` in `realm.yaml` (`provider: minio`, `endpoint: http://minio:9000`, `public_endpoint: http://localhost:9000`, `prefix`, keys as `secret:`); `backd storage check`.
- `file` leaves `schema.json` (startup refuses file fields there) and is declared under `files:` in `assets/collection.yaml`; `kind` gains `"file"`.
- New asset with a file: `prepareUpload()` then create with `{ upload, token }`; replace and remove with `If-Match`; a too-large file (`413`) and a disguised one (`415`, type detected from content).
- Rules for files: reading a file is reading the asset, every upload or removal is an update checked before any byte is stored; `data` and `changed()` show the file change, so the `published_at` lesson of chapter 5 carries over (the reader inspects a refused upload's `403`).
- Gallery previews with `fileLinks: true`; download buttons with `downloadFile()` (the Download buttons pattern: links fetched on click).
- Erasure: deleting a member's account removes their files (the existing `on_owner_delete: delete`).
- *See:* upload a PDF and an image, preview the image, download the PDF, get refused for a renamed `.exe`.

### Ch 14 — Big files and thumbnails

- `attachments`: `multiple`, `max_files`, `upload: direct` with a progress bar; bucket CORS; `409 too_many_files`.
- `versions: thumb` on `file` (declared image versions, `design/image-versions.md`): a worker makes the thumbnail after the upload; the app looks again until `versionStatus()` says it is no longer `pending`; `fileLinks` links the ready version; a text file's version is `skipped`. (This replaced a thumbnail function; the functions files API is still taught by the `download` and `share-open` functions in ch 15.)
- *See:* a large attachment uploads straight to MinIO with progress; the card shows the thumbnail within seconds; a text file answers `404 version_unavailable` for its thumbnail.

### Ch 15 — Sharing and counting downloads

- `download` function: takes `{ document_id, field, file_id }`, checks the caller can read the asset (`ctx.db … link()`), increments `downloads` through `ctx.admin.db`, returns the link; the app opens the `url`.
- Securing it: `create` gains `data.downloads == nil`, `update` gains `!('downloads' in changed())`, so an owner can't `PATCH` their own count.
- `share-open` returns a file link for shared file assets (`ctx.admin.db … link()` after the token and expiry checks), so public share pages can download.
- Operations: storage usage (`GET /_admin/storage`) in the admin page, `backd storage usage`, `reconcile` after a restore.
- *See:* the download count rises; a `PATCH` of `downloads` answers `403`; an anonymous share link downloads the file until it expires.

### Step 0 — the static app snapshot

Before chapter 1 the tutorial hands the reader the **finished static app**: `index.html`, the stylesheet and the vendored Alpine.js/PicoCSS, with every view hard-coded — gallery, asset detail and share dialog, notifications, admin feed, auth screens. No JavaScript yet. Readers download it from the tutorial pages so layout is never the lesson; each chapter then replaces a static section with live Alpine bindings. The same files, evolved to their live state, are what `examples/` keeps.

## 5. How the tutorial ships

- **Docs:** a "Tutorial" section between Getting started and Configuration in the sidebar; `tutorial/index.md` is the overview, `tutorial/01-empty-shelf/` … `tutorial/12-running-it/` are the chapters, and `tutorial/13-files/` … `tutorial/15-sharing-files/` the file chapters. Each chapter page ends with a checklist of what now works.
- **Code:** `examples/config/shelf/` holds the *final* configuration (every function, rule, index, template); the app under `examples/` is served by `make example` and linked from `docs/examples/`. Chapters describe the diffs; they do not maintain 12 copies of the realm.
- **Reader setup (standalone):** readers never clone `backd`. The tutorial serves its own starter from the docs site: a `docker-compose.yaml` (released `backd` image, MongoDB, executor, egress, and MinIO from chapter 13 with its port published on localhost) plus the step-0 snapshot; everything else they write following the chapters. Repo files double as the downloads through Hugo mounts, so the docs quote and serve the same tested bytes.
- **Frontend stack:** Alpine.js for reactivity (sprinkled on plain HTML, one `<script>` tag) and PicoCSS for layout (classless, dark/light for free). Both are vendored under the app's assets — like the other examples there is **no build step**, no npm, no bundler: what the reader writes is what the browser runs. The JS client is the only real dependency.
- **Checkpoints:** one commit per chapter on the feature branch (tracked by issue #91); each commit is a working state.
- **Demo accounts:** `shelf` seeds a member, a curator and an operator like the workshop tour does.

## 6. Testing

- The chapter apps' functions have unit tests with `@backd/functions-testing` (`createContext`, `fakeCall`, `fakeEmail`, `fakeJob`) — the tutorial *teaches* the testing helpers in the chapters that need them; `preview`'s fetch is stubbed on `globalThis.fetch`.
- Integration tests for `examples/config/shelf` mirror the workshop's: rules enforced, publish idempotent, webhook signature required, digest retried; files uploaded in both modes, refused by size and type, thumbnailed by a worker, counted and downloaded through a share link; client writes to `downloads` refused (`403`) while the functions succeed.
- The CI replay (`scripts/shelf-tour-ci.sh`, `tour.js`) covers chapters 13–15 on the starter stack with MinIO.
- Each chapter's acceptance list doubles as a manual smoke script.

## 7. Security considerations

The tutorial should teach secure defaults, not just features:

- Rules chapter explicitly asks the reader to find the rule hole a function must close — the expenses example's lesson, learned once.
- Anonymous surfaces (`import` webhook, public shares) get `rate_limit` and are revisited in Ch 12's checklist.
- Fields only functions write (`published_at`, `downloads`) are refused to clients in the `rules:` of `collection.yaml` (`== nil` on create, `changed()` on update) and written through `ctx.admin.db`; chapter 15 shows the hole first, then close it, like chapter 5.
- Secrets only ever live in `secrets:`; the egress allowlist is minimal (`api.example.com`-style placeholder, or a self-hosted target).
- Audit chapter reads ids, not emails; demo accounts are disposable.

## 8. Documentation

- New Tutorial section (15 chapter pages + overview) as in §5; the overview's chapter table and "what runs" table gain the file chapters and MinIO.
- `docs/examples/` gains a Shelf row: "the tutorial's finished app".
- Existing pages link forward where a reader naturally arrives (e.g. querying → "build it: chapter 1").
- No OpenAPI changes: the tutorial uses what ships.

## 9. Acceptance criteria

- A reader following chapters 1–15 ends with the `shelf` realm and app exactly as committed under `examples/`.
- Every current user-facing feature appears in at least one chapter: schemas and dates, indexes, queries and cursors, rules, auth lifecycle (signup→verify→reset→email change→delete), invitations, sync functions, `admin`, idempotency, secrets, network, internal functions, `ctx.call`, async jobs, `retry`, `schedule`, email (`ctx.email.send`, templates, locales, Mailbox), webhooks, API keys, admin invoke, audit, rate limits, hardening checklist, `dev_only`, `@backd/functions-testing`; files: `storage:` with `public_endpoint`, `storage check`, file fields, proxy and direct uploads, pending uploads, type and size refusals, `If-Match`, `file_links`, `downloadFile()`, the functions files API with `link()`, erasure of files, storage usage, `reconcile`.
- Each chapter ends at a working state with an explicit "you should see" list.
- `make example` serves the finished app.
- A reader who never clones `backd` — starter compose + downloaded snapshot only — completes chapters 1–15 against a released `backd` image.

## 10. Alternatives considered

- **Waiting for file management first:** the feature is unscheduled; reserving `assets.file` lets the tutorial ship now and absorb it later.

## 11. Follow-ups

- Metrics/observability chapter after [metrics.md](./metrics.md) ships (instrumenting Shelf).
- A "build vs compare" appendix reusing the expenses side-by-side technique for rules vs functions (Ch 3 → Ch 5).
