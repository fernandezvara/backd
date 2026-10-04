# Design: admin UI

- **Issues:** to be created (proposed 9.1–9.12, backlog 7.37–7.39)
- **Status:** decided in the design interview of 2026-10-04 (replaces the preliminary version)
- **Related:** the admin API (`/v1/{realm}/_admin/*`), `@backd/client`, the secure-administration decisions (admin roles, network restrictions, audit), [file-storage.md](./file-storage.md), [internal-functions.md](./internal-functions.md)

Details marked *(proposed)* were filled in while writing and were not discussed in the interview.

## 1. Goal and principles

A web interface for **realm administrators** and **developers** to operate a realm: users, roles, invitations, API keys, secrets, audit, functions and jobs, data, files and configuration.

- **A client of the admin API and nothing more:** it gains no privilege the CLI doesn't have; every call is authenticated, network-checked and audited server-side.
- **The server enforces, the UI reflects:** every permission is checked by backd; the UI only hides what the signed-in level can't do.
- **Everything bundled and served by backd:** no CDN, font, icon or script from anywhere else.
- **What isn't served can't be attacked:** the admin API and the UI can be switched off per instance.
- **Configuration stays a reviewed artifact:** the UI shows it, never edits it.

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Users | realm admins (full) and developers (read-only level); one realm at a time; an instance operator view is backlog |
| Admin levels | `admin: true` (full) and `admin: read` (read-only) on roles in `realm.yaml` |
| Read-only visibility | operations by default (config, functions, jobs, logs, audit with ids, storage usage, warnings); users and data only when `admin.read_access` grants them |
| Data access | a dedicated `/_admin/data/...` route bypassing rules: full admins read/write, read-only admins read (if granted); writes audited |
| Hosting | assets **embedded** in the backd binary, served at `/_ui/`, `BACKD_ADMIN_UI=true` (default off); the same build also published as static files |
| Instance exposure | `BACKD_ADMIN_API=true\|false` (default true); public instances can omit `/_admin` entirely |
| Code | TypeScript, Vue 3, Vite, Vue Router, Pinia; API only through `@backd/client` |
| Components | in-house set on Tailwind CSS with accessible unstyled primitives (Headless UI or Radix Vue) |
| Forms | own JSON Schema renderer for backd's subset, JSON editor fallback |
| Sessions | token in memory; opt-in `sessionStorage` per tab; idle timeout revokes server-side |
| Configuration | read-only view through `GET /_admin/config` |
| Languages and look | English first, every string in `vue-i18n` files; light/dark; no per-realm branding; Spanish when stable (backlog) |

## 3. Admin levels and access

```yaml
# realm.yaml
roles:
  owner:
    admin: true                  # full administration
    users: [ana@example.com]
  developers:
    admin: read                  # read-only administration
    users: [{ email: dev@example.com, admin_networks: [10.0.0.0/8] }]
admin:
  allowed_networks: [10.0.0.0/8]
  read_access:
    users: false                 # true: read-only admins can see users (emails, identities, roles, sessions)
    data: false                  # true: read-only admins can read /_admin/data
```

| Area | Full admin | Read-only admin |
|---|---|---|
| Configuration view, startup warnings | read | read |
| Functions, history, logs, jobs | read, invoke internal, cancel/re-run (f1) | read |
| Audit trail | read | read (user ids) |
| Storage usage | read | read |
| Users, roles, invitations, sessions | read and manage | read only if `read_access.users` |
| API keys | read and manage | read (no creation, no revocation) *(proposed)* |
| Secrets (names only) | manage | read names *(proposed)* |
| Data (`/_admin/data`) | read and write | read only if `read_access.data` |

- Every `GET` under `/_admin` is open to read-only admins except users (unless granted) and data (unless granted); every change answers `403` for them.
- Network restrictions (`admin.allowed_networks`, per-user `admin_networks`/`login_networks`, per-key `networks`) apply to both levels.
- The docs warn that function logs contain whatever functions log; don't log personal data.

## 4. Admin data route

`/v1/{realm}/_admin/data/{db}/{collection}[/{id}]` (plus `_batch`, and `/{id}/_files/{field}` once Phase 7 exists):

- Bypasses collection rules; same schema validation, versions, `If-Match`, `where`, `order_by`, paging and errors as the normal data routes.
- Full admins read and write; read-only admins read only when `read_access.data` is true.
- **Writes audited** (`data.create`, `data.update`, `data.delete`): actor, database, collection, document id — never content. `updated_by`/`created_by` record the admin (`user:<id>`).
- Reads are logged with the actor in the access log.
- The normal data routes are unchanged: an admin using an app is bound by the rules there.

## 5. Instance exposure

- `BACKD_ADMIN_API=false` → every `/v1/{realm}/_admin/*` route (data included) answers `404`, like a route that doesn't exist.
- `BACKD_ADMIN_UI=true` requires the admin API on the same instance; otherwise startup fails.
- **Reference split** (production reference, documented and network-tested): public instances with the admin API off; one internal instance on a private network with the admin API and the UI, sharing MongoDB. The CLI's `BACKD_URL` points at the internal instance.
- Hardening checklist: turn the admin API off on internet-facing instances when an internal instance exists. `decisions.md` records the principle.

## 6. Hosting and build

```
ui/admin/                    TypeScript, Vue 3, Vite, Vue Router, Pinia, Tailwind
  src/router, src/stores     session, realm, preferences
  src/views/…                one view per area (§8)
  src/components/…           table, pagination, form fields, dialogs, menus, toasts, log viewer, JSON viewer, schema form
  src/i18n/en.json           every UI string
dist/  → embedded with go:embed → served at /_ui/
```

- Built in CI before the Go build; the release embeds `dist/`. A Go build without assets still works (`/_ui/` answers `404`), so backend contributors don't need Node.
- Serving: hashed assets with long caching; `index.html` with `no-store`; unknown `/_ui/` paths fall back to `index.html`; security headers on every response.
- The same build is published as static files for separate hosting; then the realm's CORS origins must list it.
- Deep links carry the realm: `/_ui/r/<realm>/…`.

## 7. Security

The admin token is the most valuable credential in a realm, so the UI is the prime target for injected scripts.

- **Strict CSP:** `default-src 'self'`; no `unsafe-inline`, no `unsafe-eval` (precompiled templates); `connect-src 'self'`; `img-src 'self' data:` *(proposed, for inline SVG icons)*; `frame-ancestors 'none'`; Trusted Types where supported *(proposed)*.
- **Nothing external:** system font stack or bundled fonts; bundled SVG icons; no analytics. An end-to-end test fails on any CSP violation or request to another origin.
- **Safe rendering:** documents, logs, file names, user agents and audit details always rendered as text; `v-html` forbidden by a lint rule; JSON shown with a text renderer.
- **Sessions:** token in memory; opt-in "keep me signed in in this tab" uses `sessionStorage` (never `localStorage`); idle timeout (default 30 min, `BACKD_ADMIN_UI_IDLE`) revokes the session server-side; sign-out always revokes. Admin sessions only — never API keys in the browser.
- **Destructive actions** (delete user, revoke key, delete secret, delete document) need typed confirmation.
- **Secrets** are write-only, as in the API.
- Dependencies pinned and scanned (`npm audit` in CI next to `govulncheck`).
- A security review of the UI and the new admin endpoints before production use.

## 8. Scope

**Operate the realm**
- Sign-in (email and password; providers after 7.12), realm in the URL, current realm always prominent, sign-out.
- Users: list and search (paged in MongoDB), detail (roles, identities, verification, networks, sessions), create, disable/enable, delete with the "owned" report (4.6), set password, change email (4.8), invitations (4.9), revoke sessions.
- Roles: assignments, with the "only in the database" warning.
- API keys: list (role, prefix, networks, expiry, last used), create (shown once), revoke.
- Secrets: names, scope, updated at/by; set and delete; values never shown.
- Audit: filter by actor, action, target and time.
- Functions: per database (mode, schedule, internal, limits, `calls`, `secrets`, `network`), invocation history and logs, jobs and attempts, manual invoke of internal functions with `as`, cancel/re-run when f1 exists.

**Data**
- Browser: databases → collections → documents; query builder from the schema (declared fields, allowed operators only); paging; JSON view.
- Schema-driven forms (§9) for create and edit; `If-Match` on save; `412` shows the newer version to reapply.
- Files per document field (download, upload, remove) once Phase 7 exists.

**Configuration (read-only)**
- `GET /_admin/config`: realm settings (secret references by name only), collections (schema, indexes, rules, `collection.yaml`), functions (`function.yaml`), email and page templates per language, startup warnings, config fingerprint; each item shows its source file path.
- Storage usage (Phase 7) and, once 5.1 exists, metrics.

## 9. Forms from JSON Schema

- Supported: objects and nested objects; arrays (add, remove, reorder); strings with `email`, `date-time`, `date`, `uri`, `pattern`, length limits; numbers and integers with limits; booleans; enums; required markers.
- Read-only: `id`, `_meta`, file metadata; file fields get upload/download/remove controls.
- Unsupported keywords (`oneOf`, `anyOf`, `if`/`then`, recursive `$ref`) → JSON editor for that field or the whole document.
- Server validation errors shown next to the matching field.

## 10. New admin API needs

| Endpoint | Purpose |
|---|---|
| `/_admin/data/...` | §4 |
| `GET /_admin/config` | §8 configuration view |
| `GET /_admin/users?q=&limit=&skip=` paged in MongoDB | user search (backlog 7.3 becomes a prerequisite) |
| `GET /_admin/users/{id}/sessions`, `DELETE …/sessions/{session_id}` | session management *(proposed paths)* |
| `GET /_admin/whoami` *(proposed)* | the signed-in admin's level and `read_access`, so the UI knows what to show |

## 11. Languages and look

- English first; every string in `vue-i18n` message files from day one; dates and numbers follow the browser's locale.
- Light and dark themes following the system, with a manual switch.
- No per-realm branding.
- Spanish once the UI is stable (backlog 7.37).

## 12. Testing and quality

- `vue-tsc` type checking, ESLint with the Vue plugin (including the `v-html` ban), Vitest for stores and components.
- Playwright end-to-end against `make example`: sign-in, every area, both admin levels and `read_access` combinations, network refusal, admin API off (`404`), idle timeout, CSP violations and external requests failing the test.
- Accessibility checks (axe) on main views.

## 13. Documentation

"Admin UI" guide (enabling it, levels and `read_access`, the public/internal split, each area), screenshots produced by the Playwright suite, configuration reference (`BACKD_ADMIN_API`, `BACKD_ADMIN_UI`, `BACKD_ADMIN_UI_IDLE`, `admin.read_access`, `admin: read`), hardening checklist entries, OpenAPI for the new endpoints.

## 14. Proposed issues

| id | Issue | Depends on |
|---|---|---|
| 9.1 | Read-only admin level and `admin.read_access`, enforced server-side; `whoami` | — |
| 9.2 | Instance exposure: `BACKD_ADMIN_API`, the public/internal split in the production reference, network test | — |
| 9.3 | Admin data route with audited writes | 9.1 |
| 9.4 | `GET /_admin/config`, paged user search, user session endpoints | 9.1, 7.3 |
| 9.5 | UI foundation: scaffolding, embedding and `/_ui/`, CSP and headers, sign-in, realm, sessions and idle timeout, i18n, themes, CI | 9.2 |
| 9.6 | UI: users, roles, invitations, sessions | 9.5, 9.4 |
| 9.7 | UI: API keys, secrets, audit | 9.5 |
| 9.8 | UI: functions, history, logs, jobs, manual invoke | 9.5, 4.2a |
| 9.9 | UI: configuration view | 9.5, 9.4 |
| 9.10 | UI: data browser and schema forms | 9.5, 9.3 |
| 9.11 | UI: files in the data browser | 9.10, 8.2 |
| 9.12 | Security review of the admin UI and new admin endpoints | 9.6, 9.7, 9.8, 9.9, 9.10 |
| 7.37 | Spanish translation of the admin UI (backlog) | 9.12 |
| 7.38 | Prepared configuration changes to download (backlog) | 9.9 |
| 7.39 | Instance operator view across realms (backlog) | 9.12 |

## 15. Alternatives considered

- **Admins only / an instance operator now:** chose admins plus a read-only developer level; operator view later.
- **Read-only admins see everything / never personal data:** chose operations by default with opt-in per realm.
- **Admin data through rules only / admin sessions bypassing rules everywhere:** chose one dedicated, audited route.
- **UI only built in / only separate:** chose embedded plus a static build.
- **Admin API always on:** chose a per-instance switch.
- **JavaScript with JSDoc:** chose TypeScript for an application this size.
- **A full component library:** chose a small in-house set for a strict CSP and few dependencies.
- **JSON Forms / JSON editor only:** chose an own renderer with fallback.
- **`sessionStorage` always / memory only:** chose memory with opt-in per tab.
- **Editable configuration:** rejected; configuration is a reviewed, versioned artifact.
- **Two languages from the start / hard-coded English:** chose English ready for translation.
