# Design: Go client

- **Issues:** features 37.1–37.11 (milestone `37-go-client`)
- **Status:** decided
- **Related designs:** [dart-client.md](./dart-client.md) (the feature inventory this client follows), [python-client.md](./python-client.md), [types-openapi.md](./types-openapi.md), [conditional-get.md](./conditional-get.md), [triggers-and-realtime.md](./triggers-and-realtime.md)

## 1. The idea

`backd-js` and the Dart client define what a good backd client does. The Go client is the same client for Go programs: services, jobs, tools and tests that talk to a realm. Its feature inventory is the table in [dart-client.md](./dart-client.md) §3; every row has a Go counterpart except the admin API.

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Location | `pkg/client`, in the main module: `github.com/fernandezvara/backd/pkg/client`. Versions follow the server tags. |
| Dependencies | Standard library only. A CI check (`go list -deps`) fails when the package imports `internal/` or a third-party module. |
| Parity | Everything `backd-js` does except the admin API and browser-only modes. |
| Admin API | Not in this package, for the same safety reason as the other clients. The CLI keeps its own admin calls (§6). |
| Context | Every call that does I/O takes a `context.Context` first. |
| Types | `backd types --lang go` from `internal/typegen`, with generic helpers for typed documents. Untyped use is `map[string]any`. |
| Errors | `*client.Error` (`Code`, `Status`, `Message`, `Details`); sentinel errors for `errors.Is` and typed errors for `errors.As`, one per server error `code`. |
| Transport | An injectable `Doer` (`Do(*http.Request) (*http.Response, error)`), the `fetch` analogue; tests use `httptest`. |
| Token storage | `TokenStorage` interface with memory and file implementations. |
| Realtime | SSE parsed by hand over one multiplexed `POST /_realtime`; `LiveView` semantics identical to JS. |
| Iteration | `iter.Seq2[Doc, error]` for `Iterate` and lists that follow cursors. |
| Sizes | `ParseSize` and `FormatSize` in the package, run against `clients/testdata/size-vectors.json`. |
| Versioning | No separate tag: `pkg/client` is released with the server (`v0.x.y`). Its changes are listed in the main `CHANGELOG.md` under "Go client". |

## 3. Shape

```go
c := client.New(client.Options{URL: "http://localhost:8080", Realm: "shelf", APIKey: key})
page, err := c.DB("main").Collection("assets").List(ctx, client.Query{
    Where:   client.Where{"published_at": client.NotNull()},
    OrderBy: "-published_at",
    Limit:   20,
})
for doc, err := range c.DB("main").Collection("assets").Iterate(ctx, client.Query{}) { ... }
```

Typed use: `client.Collection[Asset]` from the generated `types.go`. Option names match `backd-js` (`where`, `orderBy`, `limit`, `expand`, `fileLinks`) so its docs transfer.

## 4. Layout

```text
pkg/client/
├── doc.go  client.go  errors.go  retry.go  storage.go  size.go
├── auth.go  oauth.go  consents.go  account.go
├── data.go  query.go  expand.go  buckets.go
├── files.go  functions.go  orgs.go  push.go  config.go
├── cache.go  realtime.go  typed.go
└── *_test.go        # httptest server; no MongoDB
```

An integration test against a real backd runs in CI with the others (same compose file as the Dart integration).

## 5. Generated types

`internal/typegen` gets a third emitter: `backd types --lang go` writes `types.go` (package name from `--package`, default `backdtypes`) and `realm.json` as for the other languages: same fingerprint, `--check` and `--diff`. It emits one struct per document and per where/order input, `Collection[T]` constructors per database, and function input and output structs. Things Go can't express are `any` and listed as warnings.

## 6. The CLI uses it

`cmd/backd/remote.go` has its own HTTP, credentials and error handling. The client replaces that for every call the public surface covers: login and MFA, account, consents, organizations and teams, and data and file transfers. The admin calls (`/_admin/...`: jobs, invocations, users, secrets, audit, config) stay in the CLI; a `pkg/admin` is not planned. The credentials file format stays; the client's `TokenStorage` reads and writes it. This is its own issue (37.10) after the client is complete, so the client isn't shaped around the CLI.

## 7. Issues

37.1 scaffold, transport, errors, retries, sizes · 37.2 auth and sessions · 37.3 collections and the query language · 37.4 caches · 37.5 files · 37.6 functions and jobs · 37.7 organizations, teams, consents, push · 37.8 realtime · 37.9 Go types · 37.10 the CLI adopts the client · 37.11 docs, example, CI.

## 8. Alternatives considered

- **A nested `go.mod` in `pkg/client`.** Gives the client its own tags and a dependency graph of its own, at the cost of two release flows and a replace directive in development. The package has no dependencies, so the single module costs nothing.
- **Generating the client from `openapi.yaml`.** Fine for models, but gives none of the behaviour that matters: sign-in steps, revalidation cache, retries, live views.
- **An admin package.** Possible later; not needed for v1.
