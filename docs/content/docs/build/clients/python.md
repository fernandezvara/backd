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
