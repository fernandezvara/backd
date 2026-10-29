# Design: Python client

- **Issues:** features 38.1–38.11 (milestone `38-python-client`)
- **Status:** built (milestone 38); §8 records where the build differs from the plan
- **Related designs:** [dart-client.md](./dart-client.md) (the feature inventory this client follows), [go-client.md](./go-client.md), [types-openapi.md](./types-openapi.md), [conditional-get.md](./conditional-get.md), [triggers-and-realtime.md](./triggers-and-realtime.md)

## 1. The idea

The same client as `backd-js`, the Dart client and the Go client, for Python: services, scripts, notebooks and data work. The parity target is the table in [dart-client.md](./dart-client.md) §3, minus the admin API. It comes after the Go client (milestone 37), which settles the shared decisions.

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Location / name | `clients/python`, PyPI package `backd`, import `backd` (name checked before 38.1) |
| Python | 3.10 or later; `py.typed`; fully annotated, `mypy --strict` clean |
| Dependencies | One runtime dependency: `httpx` (sync, async and streaming). No other. |
| Sync and async | `Client` and `AsyncClient` with the same surface. The async code is the source and the sync code is **generated** from it (`scripts/unasync.py`, checked in CI), so the two cannot drift; pure logic (query building, cache rules, retry policy, size grammar, the state of a live list) is shared |
| Parity | Everything `backd-js` does except the admin API and browser-only modes |
| Admin API | Not in the client, same safety reason as the other clients |
| Types | `backd types --lang python` writes `types.py` with a `TypedDict` per document and per input; untyped use is plain dicts |
| Errors | `BackdError` hierarchy, one class per error `code`, same names as JS |
| Token storage | `TokenStorage` protocol with memory and file implementations; `api_key=` is first class |
| Realtime | SSE over `httpx` streaming; `LiveView` and `AsyncLiveView` with JS semantics |
| Iteration | `iterate()` yields documents following cursors (generator / async generator) |
| Sizes | `parse_size` and `format_bytes` run against `clients/testdata/size-vectors.json` |
| Tooling | `ruff`, `mypy --strict`, `pytest` with `httpx.MockTransport`; CI in `.github/workflows/python.yml` |
| Publishing | `py-v*` tags run `publish-python.yml` with PyPI trusted publishing; versions are `0.0.x` until parity, then aligned with the server |

## 3. Shape

```python
from backd import Client

with Client(url="http://localhost:8080", realm="shelf", api_key=key) as backd:
    page = backd.db("main").collection("assets").list(
        where={"published_at": {"$notNull": True}}, order_by="-published_at", limit=20)
    for doc in backd.db("main").collection("assets").iterate():
        ...
```

`AsyncClient` is the same with `async with`, `await` and `async for`.

## 4. Layout

```text
clients/python/
├── pyproject.toml
├── src/backd/   __init__.py client.py errors.py auth.py oauth.py data.py query.py
│                files.py functions.py orgs.py push.py consents.py config.py
│                cache.py realtime.py storage.py size.py _retry.py py.typed
└── tests/       # MockTransport; integration/ against a real backd
```

## 5. Generated types

The fourth emitter in `internal/typegen`, after TypeScript, Dart and Go (milestone 37): `backd types --lang python` writes `types.py` and `realm.json` with the same fingerprint, `--check` and `--diff`. `TypedDict` with `NotRequired` for optional fields; `Literal` for enums; the generic `Collection[T]` wrappers take them.

## 6. Issues

38.1 scaffold, transport, errors, retries, sizes · 38.2 auth and sessions · 38.3 collections and the query language · 38.4 caches · 38.5 files · 38.6 functions and jobs · 38.7 organizations, teams, consents, push · 38.8 realtime · 38.9 Python types · 38.10 docs, example, CI · 38.11 PyPI publishing.

## 7. Alternatives considered

- **Standard library only.** No async and no clean streaming for SSE and uploads; a hand-rolled client would be worse than `httpx`.
- **`requests` or `aiohttp`.** One covers sync only, the other async only; `httpx` covers both with one API.
- **Generating from `openapi.yaml`.** Gives models but none of the behaviour (sign-in steps, cache, retries, live views).

## 8. How the build differs from the plan

- **Generated sync code.** The plan said "shared pure logic underneath". Hand-writing each resource twice would have drifted, so the async files in `src/backd/_async` are the source and `_sync` is written by `scripts/unasync.py` (and a test and the CI fail when it is out of date). Realtime is the one exception: a thread and a task differ in more than syntax, so `_sync/realtime.py` and `_async/realtime.py` are written by hand over the shared `_live.py` (wire format, state, ordering, references, the rules for events).
- **Uploads in parts** are sent one after the other (`backd-js` sends three at a time); a part that fails with a network error or a `5xx` is sent again, four tries.
- **Types** use `TypedDict`s in two classes (required, optional) because Python 3.10 has no `NotRequired`, and wrappers for both clients (`ShopRealm`, `AsyncShopRealm`) instead of a generic `Collection[T]` wrapper: the client's own `collection(name, DocType)` already carries the document type.
- **Browser-only parts are out**: cookies, `completeOnLoad`, `subscribeWeb`, the XHR upload.
- **Closing** the client ends every live view and waits for its thread or task; a blocked stream read is woken by shutting the socket down, since the server's heartbeat comes only every 25 seconds.
