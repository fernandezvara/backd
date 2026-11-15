# Design: `as_user` in the clients

- **Issues:** #971–#975 (milestone 45-as-user); closes #33 (widened from the JavaScript client to the four)
- **Status:** decided
- **Related designs:** [scoped-sessions.md](./scoped-sessions.md), [organizations.md](./organizations.md) (API keys on behalf of a user)

## 1. The problem

The server lets an API key act as one of the realm's users with `X-Backd-On-Behalf-Of`: the user's rules apply, documents are owned by them, the access log shows `key:<name> as user:<id>`. No client wraps it, so a backend writes the header by hand and, worse, shares one client (and its cache) between users.

## 2. The API

One call on a client made with an API key. It returns another client of the same kind that sends the header on every request:

| Client | Call |
|---|---|
| JavaScript | `client.as_user(id)` |
| Python | `client.as_user(id)` (sync and async clients) |
| Dart | `client.asUser(id)` |
| Go | `client.AsUser(id)` |

The names are the maintainer's choice. JavaScript uses `as_user` to match Python, and the docs say it is spelled that way on purpose.

- **A new client, not a mode.** It shares the transport, the API key, the base URL and the options (retry, schema versions, timeouts) with the client it came from, and nothing mutable.
- **Its own cache.** A client with a revalidation cache gives each `as_user` client a cache of its own, with the same limits. Sharing it would serve one user a body another user's request stored. The plain client's cache is untouched.
- **Only on a key client.** On a client made with a session, or without credentials, it throws at once (`ConfigError` / the language's equivalent) with a message that says why. Chaining (`as_user(a).as_user(b)`) throws too: a service names the user once.
- **An empty or malformed id** throws before any request. An id that is not a user of the realm is the server's `400 invalid_header`, surfaced as the client's usual error.
- **Typed clients** return the same typed client, so the generated types and `schemaVersions` carry over.
- **Allowed through it:** data, files, functions, realtime, organizations. These are the routes a user could call.

## 3. Administration routes refuse it

No administration endpoint accepts `X-Backd-On-Behalf-Of`, and the client enforces it too:

- Server: every route under `/_admin` answers `400 invalid_header` when the header is present. The `/_auth` routes of a signed-in user (me, sessions, devices, passkeys, MFA) refuse it too, because they act on the account, not on data: a service that could change another person's password or second factor "as them" would defeat the audit. The existing refusal with a callback token stays.
- Client: `client.admin` (and the auth, passkey and account helpers) on an `as_user` client throws locally.

- **An administrator cannot be faked.** A user who holds a role with admin rights answers `403` to the header, whatever the key (data or admin) and on every route that takes it: acting as a user must never bring more privileges than the key has, and an administrator's rights come only from the administrator's own credentials. A role without admin rights is not an administrator.

This is a security-relevant change: `SECURITY.md` and the security model page say so.

## 4. Order of work

Server first (the refusal, with tests for each route family and for the header with a session, a key, a callback token), then the four clients, then docs: `api-keys.md`, each client page, the OpenAPI description of the header.
