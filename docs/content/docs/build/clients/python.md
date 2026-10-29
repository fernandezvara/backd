---
title: "Python client"
description: "backd for Python: a sync and an async client over httpx, for services, scripts and notebooks."
icon: "terminal"
weight: 640
toc: true
---

The Python client is the same client as [`backd-js`](../js/), the [Dart client](../dart/) and the [Go client](../go/), for services, scripts, notebooks and data work. It is being built in steps (milestone 38); this page lists what exists today and grows with each step.

It has **one runtime dependency**, [`httpx`](https://www.python-httpx.org/), and needs Python 3.10 or later. The package is typed (`py.typed`, `mypy --strict` clean).

{{< hint style="warning" title="No admin API, by design" >}}
The Python client has **no admin API**. Administration is for the operator's own tooling: a program holding admin capability is a footgun, and the `bdk_…` key that unlocks it (the `admin` role, full realm access) must never ship in an application. A program that truly needs the admin API calls [the REST endpoints](../../../operate/administration/admin/) through `client.request`, knowingly.
{{< /hint >}}

## Install

The package is `backd` (published to PyPI when the client reaches parity; until then install from the repository):

```sh
pip install "backd @ git+https://github.com/fernandezvara/backd#subdirectory=clients/python"
```

## Create a client

```python
from backd import Client

with Client(url="http://localhost:8080", realm="shelf", api_key=key) as backd:
    ...
```

`AsyncClient` has the same calls: `async with AsyncClient(...)`, `await` on every call. Both close with `close()` / `await aclose()`, which ends what the client keeps running and releases its connections.

| Argument | |
|---|---|
| `url`, `realm` | Required: the server's base URL and the realm to talk to. |
| `api_key` | A server-side credential (`bdk_…`) with full access to the realm. Keep it out of anything a user's device can read. |
| `org` | Bind the client to an organization, by id or by the slug people know. |
| `storage` | Where the session token lives: `MemoryStorage()` (default) or `FileStorage(path)`. Any object with `get`, `set` and `remove` works. |
| `retry` | `Retry(attempts=3, max_delay=30)`: repeat calls the server answered `429` or `503` to. Off by default. Only safe calls are repeated: reads, and writes that carry `If-Match` or an `Idempotency-Key`. `Retry-After` is honoured. |
| `headers` | Sent with every call. |
| `transport`, `timeout` | An `httpx` transport (tests use `httpx.MockTransport`) and the seconds without progress before a call fails (30 by default; `None` waits for ever). |
| `types`, `on_stale_types` | Development only: the fingerprint of the [generated types](../types/) the program was written against. It is sent as `X-Backd-Types`; when the server's config has moved on it answers `X-Backd-Types-Stale` and the client logs one warning (or calls `on_stale_types`). |

## Making a call

`client.request(method, path, ...)` sends one call to `/v1/{realm}/{path…}`, the way every other part of the client does: it adds the credentials, repeats retryable answers, maps failures to errors and returns a `Response` with `status`, `headers` and the parsed JSON in `data`. Each path segment is encoded for you. It is the way to reach an endpoint the client has no method for yet.

```python
res = client.request("GET", ["main", "posts"], query={"limit": 5})
print(res.data)
```

## Sign up and sign in

`client.auth` has the whole sign-in surface of the other clients. A successful sign-in stores the session token in the client's `storage`; every later call sends it.

```python
session = client.auth.signup("ada@example.com", "dev-p4ssw0rd!")   # creates the account and signs in
session = client.auth.login("ada@example.com", "dev-p4ssw0rd!")
me = client.auth.me()                                               # the user, their identities and sign-in methods
client.auth.logout()
```

`signup` and `login` promise a session. When the account has a second step to take, they raise `SecondStepRequiredError` (`code` is `mfa_required`, `mfa_enrollment_required` or `consent_required`, with the `challenge`). `sign_in` returns the step as a value instead, so the code can branch on it:

```python
from backd import ConsentChallenge, EnrollmentChallenge, MfaChallenge

step = client.auth.sign_in(email, password)
if isinstance(step, MfaChallenge):                       # step.methods: "totp", "recovery_code"
    session = client.auth.verify_mfa(step.challenge, "totp", input("code: "))
elif isinstance(step, EnrollmentChallenge):              # set a second factor up first
    setup = client.auth.mfa.setup(challenge=step.challenge)
    done = client.auth.mfa.confirm(setup_id=setup["setup_id"], code=input("code: "), challenge=step.challenge)
elif isinstance(step, ConsentChallenge):                 # step.consents: [{"name", "version"}]
    session = client.auth.accept_consents(step.challenge, {c["name"]: c["version"] for c in step.consents})
else:
    session = step
```

A realm that verifies addresses answers a sign-up with `VerificationRequiredError`: the account exists, there is no session yet. A password an administrator expired raises `PasswordExpiredError` (`reset_email_queued` says whether the email with the link was sent).

An **API key** needs no sign-in: pass `api_key=` and every call sends it. Keep it to server-side programs.

### Sensitive actions

Changing the password or the address, deleting the account, unlinking a sign-in method, managing the second factor: the server wants a fresh proof. `client.auth.reauth({"method": "password", "password": ...})` (or `totp`, `recovery_code`, `email_code` after `request_reauth_code()`) gives it, and the client sends it with the sensitive calls while it is good. `with_proof(ask, action)` runs an action and, when the server asks for a proof, calls `ask(methods)` for it and tries the action once more.

### Providers, magic links, organizations

- **A provider by redirect:** `url = client.auth.sign_in_with("google", redirect_to="http://localhost:9000/cb", navigate=webbrowser.open)` starts it and returns the provider's address; the PKCE verifier waits in `oauth_storage` (memory by default; pass an `OAuthStorage` to survive a restart). When the person comes back, `client.auth.complete_sign_in(address)` trades the one-time code for a session, or raises the error the redirect carries (`ConflictError` for `account_exists`, `ForbiddenError` for `signup_closed`, …). `link_provider` adds a provider to the signed-in user. Mobile apps that hold the platform's ID token use `sign_in_with_id_token`.
- **A magic link:** `client.auth.magic_link.request(email, redirect_to=...)` then `verify_code(code)`, or `complete_sign_in` with the link's address.
- **Organizations:** `org="acme"` binds the client to an organization, so sign-in goes to `/_auth/orgs/acme/…` and the session is scoped to it. `client.org("acme")` gives the client of one organization for a program signed in to several; each keeps its own token in a slot of the storage (`MemoryStorage` and `FileStorage` have them). `sign_up_with_org` starts a company.
- **Also:** `mfa` (`status`, `setup`, `confirm`, `disable`, `regenerate_recovery_codes`), `password.set` / `password.remove`, the email flows (`resend_verification`, `verify_email`, `request_password_reset`, `reset_password`, `update_password`, `request_email_change`, `confirm_email_change`, `revert_email_change`, `accept_invitation`), `sessions` / `revoke_session`, `logout_all`, `delete_account`, `update_me`, `providers` / `options`, and `on_auth_change(listener)` for `SIGNED_IN`, `SIGNED_OUT` and `SESSION_EXPIRED`.

### Keeping the session between runs

```python
from pathlib import Path

from backd import Client, FileStorage

client = Client(url=url, realm="shelf", storage=FileStorage(Path.home() / ".config" / "app" / "session"))
```

`FileStorage(path)` writes the token to a file readable by its owner only, so a script or a tool stays signed in. Any object with `get`, `set` and `remove` works as a storage.

## Collections

`client.db("main").collection("posts")` reads and writes documents. Documents are plain `dict`s: the collection's fields plus `id` and `_meta` (the generated [types](../types/) describe them as `TypedDict`s when you want them).

```python
posts = client.db("main").collection("posts")

page = posts.list(
    where={"published_at": backd.not_null(), "tags": backd.in_("news", "go")},
    order_by="-published_at",
    limit=20,
    count=True,
)
for doc in page:                       # page.items, page.has_more, page.next_cursor, page.total
    print(doc["id"], doc["title"])

for doc in posts.iterate(where={"status": "published"}):   # every match, following cursors
    ...

doc = posts.create({"title": "Hello"}, idempotency_key="import-42")
doc = posts.patch(doc["id"], {"title": "Hello again", "draft": None}, if_match=doc["_meta"]["version"])
posts.delete(doc["id"])               # soft delete in a collection with soft_delete; purge=True removes it for good
posts.restore(doc["id"])
```

`where` takes a dict in the [query language](../../api/querying/) (or its JSON text). The helpers build the operators: `eq`, `ne`, `gt`, `gte`, `lt`, `lte`, `in_`, `nin`, `between`, `is_null`, `not_null`, `like`, `ilike`, `starts_with`, `ends_with`, `contains` (and the case-insensitive `i…` forms), `search`, and `or_` / `and_` for lists of filters. `expand(("author", ["name"]), "tags")` builds the `expand` option that returns referenced documents in `_expanded`.

- **Writes** — `create`, `replace`, `patch` (JSON Merge Patch: `None` removes a field), `delete`, `restore`. `if_match=3` (or `"*"`) makes the write fail with `VersionMismatchError` unless the document is still at that version, and a write with `if_match` or `idempotency_key` is safe to retry. A collection with `custom_id` takes the new document's `id` in the document; a taken one raises `IdTakenError`.
- **`iterate`** follows `next_cursor`, so documents created or deleted meanwhile never shift a page; a list ordered by something a cursor can't follow (arrays, objects, mixed types) is fetched by offset instead. `limit` is the page size (100 by default). The async client's `iterate` is an async generator: `async for doc in posts.iterate(): ...`.
- **`search("red \"running shoes\" -kids")`** is a list with the text as its `$search`, best match first (`_meta.score`).
- **`aggregate(group_by=["category", "paid_at:month"], metrics=["count", "sum:total"], tz="Europe/Madrid")`** returns `{"groups": [...], "total_groups": n}`; `fill_buckets(groups, field="paid_at", unit="month", from_="2026-01", to="2026-12")` adds the empty buckets a chart needs.
- **`client.db("main").batch([...])`** applies up to 100 writes across the database's collections in one transaction: all or none.
- **Teams and organizations** — `posts.in_team("t1")` scopes `list`, `iterate`, `aggregate` and `create` to a team of the session's organization; `in_org("o1")` names the organization of a create made with an admin key.

## Caches

Two caches keep reads cheap. Neither serves anything the server hasn't confirmed.

**The revalidation cache** keeps the last body and `ETag` of each document and list read, in memory. The next read of the same address sends `If-None-Match`, and an unchanged document costs a `304` instead of the body. Every read still goes to the server, which runs the rules, so a document the caller can no longer see is never served from the copy. Entries belong to the credentials they were read with, and a sign-in, a sign-out or an expired session empties the cache. A write to a document drops its entries; reads with `expand`, `file_links` or any `_` path are never kept.

```python
from backd import CacheOptions, Client

client = Client(url=url, realm="shelf", cache=CacheOptions(max_entries=500, max_bytes=20 * 1024 * 1024))
client.cache_stats()                    # CacheStats(hits=…, misses=…, entries=…, bytes=…)
posts.get("p1", cache=False)            # this read skips the cache
client.clear_cache()
```

On by default (200 entries, 5 MiB); `cache=False` turns it off. `client.request(...)` returns `from_cache=True` on a read answered from the copy.

**The app configuration** is the realm's flags, switches and limits (`_config`), as the signed-in user's rules allow. `client.config` keeps a copy per realm and user, fresh for the `cache_ttl` the server gave, with no request at all; after that a read returns the copy at once and revalidates it in the background (a task in the async client, a thread in the sync one).

```python
client.config.load(["features", "maintenance"])           # fetched once, then fresh for cache_ttl
client.config.get("features.export.enabled")                # from the copy; fetched first if never loaded
client.config.get_sync("maintenance.enabled", False)      # the copy only, never a request
stop = client.config.on("change", lambda paths: print("changed:", paths))
```

Pass `config_storage=FileConfigStorage(directory)` to start the next run with the last configuration it saw (`MemoryConfigStorage` is the default, and any object with `get`, `set`, `remove` and `all_keys` works). Copies are dropped on sign-out and when another user signs in.

## Errors

Every failure is a `BackdError` with `status`, `code`, `message`, `details`, `request_id` and `path`. The subclasses follow the server's error `code`s with the names `backd-js` uses, and a class matches its parents: `except ConflictError` also catches `IdTakenError`, and `except ValidationError` catches `InvalidIdError`.

| Class | When |
|---|---|
| `ValidationError` (400), `AuthenticationError` (401), `ForbiddenError` (403), `NotFoundError` (404), `ConflictError` (409), `VersionMismatchError` (412), `RetryableError` (429, 503) | The status, when the code has no class of its own. |
| `IdTakenError`, `InvalidIdError`, `TeamError`, `InvalidExpandError`, `FieldEncryptedError`, `OrgRequiresMFAError`, `NotOrgMemberError`, `SsoRequiredError`, … | One class for each of the server's [error codes](../../api/#errors). |
| `NetworkError` | The call got no answer: `code` is `network_error` or `timeout`, `status` is `0`, and `__cause__` has the reason. |

`RetryableError.retry_after` is in seconds when the server said.

## Sizes

`parse_size("128MB")` and `format_bytes(1536)` follow the one [size grammar](../../configuration/#sizes-and-units) of the server and the other clients (`parse_size("1.5MiB") == 1572864`; `format_bytes(1536) == "1.5 KiB"`), and run the same shared test vectors.

## Sync and async

The async client is the source: the sync client is generated from it, so the two have the same calls and cannot drift. Realtime is the exception, written once for threads and once for tasks.
