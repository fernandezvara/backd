---
title: "Python client"
description: "backd for Python: a sync and an async client over httpx, for services, scripts and notebooks."
icon: "terminal"
weight: 640
toc: true
---

The Python client is the same client as [`backd-js`](../js/), the [Dart client](../dart/) and the [Go client](../go/), for services, scripts, notebooks and data work: the whole sign-in surface, collections that speak the [query language](../../api/querying/), files, functions and their jobs, organizations and teams, push devices, consents, the app configuration, an `ETag` revalidation cache, live views over one realtime stream and [generated types](../types/). It comes as a sync `Client` and an async `AsyncClient` with the same calls.

It has **one runtime dependency**, [`httpx`](https://www.python-httpx.org/), and needs Python 3.10 or later. The package is typed (`py.typed`, `mypy --strict` clean).

{{< hint style="warning" title="No admin API, by design" >}}
The Python client has **no admin API**. Administration is for the operator's own tooling: a program holding admin capability is a footgun, and the `bdk_…` key that unlocks it (the `admin` role, full realm access) must never ship in an application. A program that truly needs the admin API calls [the REST endpoints](../../../operate/administration/admin/) through `client.request`, knowingly.
{{< /hint >}}

## Install

The package is `backd`, on PyPI once the first release is made (see [Releasing](../../../contribute/#releasing-the-python-client)); until then install it from the repository:

```sh
pip install backd                                                                          # from PyPI
pip install "backd @ git+https://github.com/fernandezvara/backd#subdirectory=clients/python"   # from the repository
```

## Quickstart

```python
from backd import Client

with Client(url="http://localhost:8080", realm="shelf") as client:
    client.auth.signup("ada@example.com", "dev-p4ssw0rd!")
    notes = client.db("main").collection("assets")
    note = notes.create({"kind": "note", "title": "Hello"})
    for asset in notes.iterate(where={"kind": "note"}, order_by="-_meta.created_at"):
        print(asset["title"])
```

The same with `async`:

```python
import asyncio

from backd import AsyncClient


async def main() -> None:
    async with AsyncClient(url="http://localhost:8080", realm="shelf") as client:
        await client.auth.login("ada@example.com", "dev-p4ssw0rd!")
        notes = client.db("main").collection("assets")
        async for asset in notes.iterate(where={"kind": "note"}):
            print(asset["title"])


asyncio.run(main())
```

A complete script, with the generated types, is in [`clients/python/examples/shelf`](https://github.com/fernandezvara/backd/tree/main/clients/python/examples/shelf): it signs in to the [tutorial](../../../tutorial/) stack, lists the published assets, saves a note and follows the assets live.

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

## Files

A file field (`files:` in `collection.yaml`) is read and written through the document. `collection.files(id, field)` has the operations; `upload_file` and friends pick the right way to send.

```python
from pathlib import Path

posts = client.db("main").collection("posts")

# Upload: through backd (upload: proxy) or straight to the bucket (upload: direct), as the field says.
doc = posts.upload_file("p1", "cover", Path("cover.jpg"), on_progress=lambda sent, total: print(sent, total))

# Download: stream to a path (written in place only when it all arrived), or read it yourself.
posts.download_file("p1", "cover", doc["cover"]["id"], to=Path("out.jpg"), file=doc["cover"])
with posts.files("p1", "cover").get() as content:          # the one file of a single field
    for chunk in content.iter_bytes():
        ...

link = posts.file_url("p1", "cover", doc["cover"]["id"])    # {"url": …, "expires_at": …}, works without credentials
thumb = posts.file_url("p1", "cover", doc["cover"]["id"], version="thumb", file=doc["cover"])   # None until ready
```

- **What to send** — `Upload(source, name=..., type=...)`, or just the source: bytes, text (sent as UTF-8), a `pathlib.Path`, or a binary file object. Paths and seekable files are read in pieces, never loaded whole, and can be sent again after a `429` or `503`; a pipe can be sent once. `name` and `type` default to the file's own.
- **Progress** — `on_progress(sent, total)`; `total` is `None` for a stream of unknown size.
- **Direct uploads** hash the file first (SHA-256, off the event loop in the async client), declare it, send it to the signed link without backd's credentials, and complete. A file above the field's `multipart_above` is declared by the hash of each part and sent in parts, one after the other; a part that fails with a network error or a `5xx` is sent again (four tries), the others are not.
- **`prepare_upload`** sends a file for a document that doesn't exist yet and returns `ref`: name it in the field of the `create` (`{"cover": [up["ref"]]}`).
- **Downloads check the checksum** when the file's details are known (`get()` without an id looked them up, or you pass `file=`): a difference raises `BackdError` with code `checksum_mismatch`. A saved path is only replaced when the whole file arrived and matched.
- **Public files** — pass the file's details as `file=` to `file_url` and the permanent address comes back with no request; `public_url(file, version)` and `version_status(file, "thumb")` do the same from the details a document came with, to show a placeholder while a version is `pending`.
- **Versions** — `files(id, field).version("thumb").regenerate()`, `.generate(max_width=…)` or `.delete()` wait for a worker and return the version's state.

## Functions and jobs

Call a function of a database with `db.fn(name, input)` (or `collection.fn`, `client.functions.call("database/name", input)`). A `sync` function returns its output; an `async` one returns a `Job` to follow.

```python
refund = client.db("main").fn("refund", {"order": "o1"}, idempotency_key="refund-o1")

job = client.db("main").fn("report", {"month": "2026-09"})       # an async function: a Job
print(job.status())                                                # "queued", "running" or "done"
rows = job.wait(timeout=120, on_progress=lambda steps, job: print(steps[-1]["name"]))

rows = client.functions.run("main/report", {"month": "2026-09"})  # call, and wait when it is a job
```

- **Errors** — a function's own error (`ctx.error(409, "already_refunded", …)`) arrives as the `BackdError` its status stands for, with its `code` and `message`. For a job it arrives from `wait()`, the same as a `sync` call would have raised.
- **`wait`** polls every `poll_interval` seconds (0.5) and returns the output. With `timeout` it raises `JobTimeoutError` when the job has not finished; the job itself goes on, so wait again whenever you like. `job.steps` is what the function reported with `ctx.step()`, as of the last poll.
- **`respond_async=True`** queues a `sync` function as a job too, for calls that take long or that nobody waits for.
- **`idempotency_key`** makes a repeated request return the first one's answer, and lets the client repeat the call after a `429` or `503` when `retry` is on.
- **No input** is sent as JSON `null`. `webhook` functions can't be called here: their caller is whatever service sends the webhook.

## Organizations, consents, push and your own data

The rest of the account-level surface, with the structure of `backd-js`.

- **`client.orgs`** — `create(name, slug=…)` (the new organization comes with a session of its own, kept under `client.org(slug)`), `get`, `update` (a name or slug; `require_mfa`, `sso_required` and `session_max_age` ask for a fresh proof, see `with_proof`), `leave`, `delete`, and `signed_in()` for an organization switcher. Members, teams, templates, invitations and connections hang off one organization:

  ```python
  orgs = client.orgs
  orgs.members("o1").list(limit=50)                       # a page: items, has_more, next
  orgs.members("o1").update("u1", ["editor"])
  orgs.teams("o1").create("Ops", roles=["responder"])
  orgs.teams("o1").grant_role("t1", "u1", "lead", until="2026-12-31T00:00:00Z")
  orgs.invitations("o1").create("email", email="ada@example.com", roles=["editor"], send=True)
  orgs.templates("o1").put("invite", "es", {"subject": "…", "text": "…", "html": "…"})
  orgs.sso("o1").set("google", client_id="…", client_secret="…", hosted_domain="acme.example")
  ```

  A team of the session's organization is reached from a collection with `collection.in_team("t1")`.
- **`client.consents`** — `current()` and `text(name, version, locale)` need no session (a person reads the text before having an account); `state()`, `accept({"terms": "2026-10"})` and `withdraw(name)` are the signed-in user's. A required consent can't be withdrawn (`ConflictError`, code `consent_required_for_account`).
- **`client.push`** — `register("android", token, name="Pixel")` registers this device for the signed-in user's pushes (it needs a user session, not an API key); `devices()`, `unregister()`, and `register_on_launch(get_token)`, which keeps the device registered now and after every sign-in, in the background. Browser web push is not part of this client.
- **`client.account`** — a copy of everything the realm holds about the signed-in user. Needs a fresh proof and a realm with `compliance.export: true`:

  ```python
  client.auth.reauth({"method": "password", "password": password})
  export = client.account.request_export()
  ready = client.account.wait_for_export(export["id"])        # polls every 2 s, up to 30 minutes
  client.account.download_export(ready["id"]).save("export.zip")
  ```

## Realtime

A collection that declares `realtime: true` can be followed live. `client.realtime.subscribe` opens **one stream** for the whole client (`POST /v1/{realm}/_realtime`, parsed by hand over `httpx` streaming), loads each list with normal reads, and keeps it current from the events.

```python
live = client.db("main").collection("orders").subscribe(
    where={"status": "open"},
    load={"sort": "-_meta.created_at", "limit": 50},
)
live.on("change", lambda state: render(state.items))      # state.status, .items, .subscriptions, .references
live.wait_ready(timeout=10)                                  # blocks until the first list has loaded
...
live.close()
```

The async client's view runs as tasks of the running event loop (`await live.wait_ready()`); the sync client's runs in daemon threads, and its listeners are called from those threads, one at a time and outside any lock, so they can read `live.state`.

- **Several subscriptions** — `client.realtime.subscribe([spec, spec, …])` follows several collections or filters in one view; each spec is a dict with `db`, `collection`, and optionally `id`, `where`, `doc` (follow one document), `load` (`sort`, `limit`) and `references`. `state.subscriptions` has each one's items and status. Subscribe with an equality on a grouping field (`_meta.team`) to keep the server's work small; `collection.in_team(...)` adds it for you.
- **References** — `references={"author": {"field": "author_id", "collection": "users", "fields": ["name"]}}` keeps the referenced documents beside the list in `state.references["author"]`, read in one batched request.
- **How it stays right** — events that arrive while a list loads wait and are applied after it, ignoring versions that are not newer; a `reset` from the server reloads after a random wait of up to a second (`SubscribeOptions(jitter=…)`), so a reset of many clients doesn't make them all read at once; a dropped stream reconnects with a growing wait (`SubscribeOptions(max_backoff=…)`) and resumes from the last event it saw, starting over from a reload if the server no longer has it; the stream is also reopened when the credential's lifetime ends and when the set of subscriptions changes.
- **Events** — `on("status", …)` (`connecting`, `live`, `resyncing`, `closed`), `on("reset", …)`, `on("access_lost", …)` (a reload found the caller may no longer read the list; it is empty), and `on("error", …)`. An error ends the view: a refused subscription (`RealtimeNotEnabledError`, `ForbiddenError`, …) ends only the view that asked, and `SessionEndedError` ends them all; sign in again and subscribe again.
- **Closing** — `live.close()` stops one view; `client.close()` (or `await client.aclose()`) ends every stream and waits for its thread or task: nothing is left running.

`client.config.live(["maintenance"])` follows the [app configuration](#caches) on the same stream: `client.config.on("change", …)` hears of the keys that differ within the realtime delay instead of `cache_ttl`.

## Generated types

`backd types --lang python` turns a realm's config into `types.py` — a `TypedDict` for every document, write input, function input and output and the app configuration, `Literal` types for enums, classes of constants for the fields a `where` or an `order_by` may name, and typed wrappers of the client — and `realm.json` with the same fingerprint every language shares (`--check` and `--diff` work as for the others). See [Generated types](../types/#in-python) for what is generated.

```python
from shelf_api.types import MainAssetsFields, MainAssetsInput, ShelfRealm

shelf = ShelfRealm(Client(url=url, realm="shelf", api_key=key, types=FINGERPRINT))   # FINGERPRINT: realm.json's fingerprint
page = shelf.main.assets.list(order_by=MainAssetsFields.META_CREATED_AT_DESC)
new: MainAssetsInput = {"kind": "note", "title": "Hello"}
shelf.main.assets.create(new)
```

`types=` (the fingerprint in `realm.json`) is for development builds: the client sends it as `X-Backd-Types`, and when the config has moved on the server answers `X-Backd-Types-Stale` and the client logs one warning.

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

The async client is the source: the sync client is generated from it, so the two have the same calls and cannot drift. Realtime is the exception, written once for threads and once for tasks; the rules they share (the state of a live list, the wire format) are written once.
