# Design: Go client

- **Issues:** features 37.1–37.12 (milestone `37-go-client`; #751–#761 and #774)
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
| Admin API | Not in this package, for the same safety reason as the other clients. It lives in `internal/adminclient`, an add-on that wraps a configured `*client.Client` (§6). |
| Extension point | `pkg/client` exports one request primitive (`Do`) that applies auth, `If-Match`, retries, error mapping and the cache rules; add-ons and library users build on it. |
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

## 6. The admin add-on and the CLI

Go has no dynamic fields, so a client can't gain a `c.Admin` after construction without `pkg/client` importing the admin code. The add-on wraps the client instead:

```go
c := client.New(opts)               // the public client, configured as usual
a := adminclient.New(c)             // internal/adminclient; takes the configured client
jobs, err := a.Jobs.List(ctx)       // a.Users, a.Secrets, a.Audit, a.Config, a.Export, ...
```

- `adminclient.New(c)` reuses the client's URL, realm, credentials (session or API key), `Doer`, retries and error mapping through the exported `c.Do(ctx, method, path, body, out, opts...)`. It adds no transport of its own.
- It lives in `internal/`, so only this module (the CLI) can import it; the public package never contains admin code. If the admin API is ever made public it moves to `pkg/admin` with the same `New(c)` shape.
- It covers every `/_admin/...` call the CLI makes today (jobs, invocations, schedules, users, API keys, secrets, audit, app config and its history, encryption, export and import, data checks, push, compliance, consents and password notices, file version regeneration, OpenAPI).

`cmd/backd/remote.go` has its own HTTP, credentials and error handling. Once the client and the add-on exist, the CLI is built on both and `remote.go` goes away, keeping only the credentials file (the client's `TokenStorage` reads and writes it, format unchanged) and the TLS options. This is its own issue (37.10) after the client and the add-on (37.12), so the client isn't shaped around the CLI.

## 7. Issues

37.1 scaffold, transport, errors, retries, sizes · 37.2 auth and sessions · 37.3 collections and the query language · 37.4 caches · 37.5 files · 37.6 functions and jobs · 37.7 organizations, teams, consents, push · 37.8 realtime · 37.9 Go types · 37.10 the CLI adopts the client · 37.11 docs, example, CI · 37.12 the admin add-on.

## 8. Alternatives considered

- **A nested `go.mod` in `pkg/client`.** Gives the client its own tags and a dependency graph of its own, at the cost of two release flows and a replace directive in development. The package has no dependencies, so the single module costs nothing.
- **Generating the client from `openapi.yaml`.** Fine for models, but gives none of the behaviour that matters: sign-in steps, revalidation cache, retries, live views.
- **A public `pkg/admin`.** Possible later with the same shape; for now admin stays internal.
- **`c.Add(admin)` and `c.Admin`.** Needs a field of a fixed type in `pkg/client` (so it would import the admin code) or an untyped registry; wrapping a configured client is simpler and fully typed.
