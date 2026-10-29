---
title: "Go client"
description: "pkg/client: sign-in and sessions, collections and the query language, files, functions and jobs, organizations, live views and generated types, for Go programs."
icon: "terminal"
weight: 630
toc: true
---

`pkg/client` is the Go client for `backd`: one entry point per realm, with the whole sign-in surface, collections that speak the [query language](../../api/querying/), files, functions and their jobs, organizations and teams, push devices, consents, the app configuration, an ETag revalidation cache and live views over one realtime stream. It is the same client as [`backd-js`](../js/) and the [Dart client](../dart/), written for services, jobs, tools and tests. The `backd` command line is built on it.

It uses the **standard library only** and never imports backd's internal packages (a test fails on any other import), so depending on it adds nothing to your build. It is part of backd's module and versioned with it: it is released with the server (`v0.x.y`), and its changes are listed in the [changelog](https://github.com/fernandezvara/backd/blob/main/CHANGELOG.md).

{{< hint style="warning" title="No admin API, by design" >}}
The Go client has **no admin API**. Administration is for the operator's own tooling: a program holding admin capability is a footgun, and the `bdk_…` key that unlocks it (the `admin` role, full realm access) must never ship in an application. A program that truly needs the admin API calls [the REST endpoints](../../../operate/administration/admin/) through [`Client.Do`](#building-on-the-client), knowingly.
{{< /hint >}}

## Install

```sh
go get github.com/fernandezvara/backd/pkg/client
```

The module declares the Go version it needs (`go.mod`; the `go` command fetches that toolchain when yours is older). Iterating over cursors and live views use `iter.Seq2`.

## Create a client

```go
import "github.com/fernandezvara/backd/pkg/client"

c, err := client.New(client.Options{
    URL:   "http://localhost:8080",
    Realm: "shelf",
    // APIKey: os.Getenv("BACKD_API_KEY"),            // a server-side program may use a key
    // Storage: client.NewFileStorage("/var/lib/app/session"),   // or keep a session between runs
})
if err != nil {
    return err
}
defer c.Close()   // ends live views and releases idle connections
```

| Option | |
|---|---|
| `URL`, `Realm` | The server and the realm. Both are required. |
| `APIKey` | A server-side credential with the key's role. Use it for jobs and services; use sessions for people. |
| `Storage` | Where the session token lives: memory by default; `NewFileStorage(path)` keeps it between runs (mode `0600`). |
| `Org` | Binds the client to an organization (see [Working in an organization](#working-in-an-organization)). |
| `Retry` | `{Attempts: 2, MaxDelay: 10 * time.Second}` repeats `429` and `503` answers of **safe** calls: reads, and writes that carry `If-Match` or an idempotency key. Off by default. |
| `Cache` | The [revalidation cache](#revalidation-cache): on by default, `Cache.Disable` turns it off. |
| `ConfigStorage` | Keeps the [app configuration](#app-configuration) between runs (`NewFileConfigStorage(dir)`). |
| `Doer` | An `interface{ Do(*http.Request) (*http.Response, error) }`: an `*http.Client` with your timeouts, proxy or TLS, or a fake in tests. |
| `Headers` | Extra headers for every call. |
| `Types`, `OnStaleTypes` | The fingerprint of the [generated types](#generated-types) the program was written against: when the server's config has moved on, `OnStaleTypes` is called once. |
| `MapError` | Shapes every error the client returns (the `backd` CLI turns them into the messages it prints). |

Every call that does I/O takes a `context.Context` first, and ends with it. Every method takes `...client.RequestOption` last (`client.WithHeader`, `WithQuery`, `WithRetry`, …).

## Sign-up, login and sessions

```go
auth := c.Auth()

session, err := auth.Signup(ctx, client.SignUpInput{Email: "ada@example.com", Password: "dev-p4ssw0rd!"})
session, err = auth.Login(ctx, client.SignInInput{Email: "ada@example.com", Password: "dev-p4ssw0rd!"})
// the token is stored: every later call carries it

me, err := auth.Me(ctx)
err = auth.Logout(ctx)
```

`Signup` and `Login` return the session. When the realm needs an extra step they return a `*client.SecondStepRequiredError`; `SignIn` gives the step as a value instead:

```go
step, err := auth.SignIn(ctx, client.SignInInput{Email: email, Password: password})
switch step.Kind {
case client.StepSession:      // step.Session holds the session; it is stored
case client.StepMFA:          // a code of the second factor
    session, err = auth.VerifyMFA(ctx, step.Challenge, "totp", code)
case client.StepEnrollment:   // a role requires a second factor and the user has none
    setup, err := auth.MFA.Setup(ctx, step.Challenge)      // show setup.OtpauthURI as a QR code
    res, err := auth.MFA.Confirm(ctx, client.MFAConfirmInput{Challenge: step.Challenge, SetupID: setup.SetupID, Code: code})
case client.StepConsent:      // consents to accept first
    session, err = auth.AcceptConsents(ctx, step.Challenge, map[string]string{"terms": "3"})
}
```

Sessions end: when the server refuses the stored token the client forgets it, so `HasSession` turns false. Listen for it:

```go
stop := auth.OnChange(func(ch client.AuthChange) {
    // ch.Event is client.SignedIn, client.SignedOut or client.SessionExpired
})
defer stop()
```

### Magic links, providers and passwords

```go
// A link (and a code) sent by email; finish with the code, or with the address the link opens.
_, err := auth.MagicLink.Request(ctx, "ada@example.com", client.MagicLinkOptions{})
step, err := auth.MagicLink.VerifyCode(ctx, code)

// A provider by redirect: open the address, then give back the address the person returns to.
url, err := auth.SignInWith(ctx, "google", client.OAuthOptions{RedirectTo: "http://localhost:9999/cb"})
step, err = auth.CompleteSignIn(ctx, "http://localhost:9999/cb?backd_code=…")
// …or with an identity token a native flow gave the program
step, err = auth.SignInWithIDToken(ctx, "apple", client.IDTokenInput{IDToken: jwt, Nonce: nonce})
```

The PKCE verifier of a redirect sign-in is kept in an `OAuthStorage` (memory by default, `Options.OAuthStorage`) between the start and the end. The email flows (`VerifyEmail`, `RequestPasswordReset`, `ResetPassword`, `RequestEmailChange`, …) send no credentials and answer alike whether or not the address is known.

### Sensitive actions

Changing a password, an address, the second factor, deleting the account or linking a provider asks for a **fresh proof of identity**. `Reauth` gets one and the client sends it while it is good; `WithProof` does the asking for you:

```go
err := auth.WithProof(ctx,
    func(ctx context.Context, methods []string) (client.ReauthInput, error) {
        return client.ReauthInput{Method: "password", Password: askPassword()}, nil   // methods lists what the user can use
    },
    func(ctx context.Context) error { return auth.ChangePassword(ctx, newPassword) },
)
```

## Collections

```go
posts := c.DB("main").Collection("posts")

doc, err := posts.Create(ctx, map[string]any{"title": "Hello", "status": "draft"})
page, err := posts.List(ctx, client.Query{
    Where:   client.Where{"status": "published", "views": client.Gte(100)},
    OrderBy: []string{"-_meta.created_at"},
    Limit:   20,
    Count:   true,    // also page.Total
})
for _, d := range page.Items {
    fmt.Println(d.ID(), d["title"], d.Meta().Version)
}
doc, err = posts.Get(ctx, doc.ID(), client.ReadOptions{})
doc, err = posts.Patch(ctx, doc.ID(), map[string]any{"status": "published"})   // JSON Merge Patch; null removes a field
err = posts.Delete(ctx, doc.ID())
```

`where` and `OrderBy` use the [query language](../../api/querying/) as it is; the client only serializes them. `client.Where` is a map of fields to values (equality) or operators: `Eq`, `Ne`, `Gt`, `Gte`, `Lt`, `Lte`, `In`, `Nin`, `Between`, `IsNull`, `NotNull`, `Like`, `ILike`, `StartsWith`, `Contains`, … and `Or(…)` and `And(…)` of other `Where`s. A `Where` can also be the JSON text of one.

### Pages and cursors

`List` reads one page. `Iterate` reads everything the query matches, following the cursors, as an `iter.Seq2`; the loop ends at the first error:

```go
for doc, err := range posts.Iterate(ctx, client.Query{Where: client.Where{"status": "published"}, OrderBy: []string{"title"}}) {
    if err != nil {
        return err
    }
    fmt.Println(doc["title"])
}
```

### Typed documents

`client.As[T]` reads into your struct, which embeds `client.Base` (`ID`, `Meta`, `Expanded`):

```go
type Post struct {
    client.Base
    Title  string `json:"title"`
    Status string `json:"status"`
}
typed := client.As[Post](posts)
page, err := typed.List(ctx, client.Query{Limit: 10})   // *client.Page[Post]
post, err := typed.Get(ctx, id, client.ReadOptions{})
```

[Generated types](#generated-types) give you these structs, and the typed collections, from the realm's config.

### Revalidation cache

The client keeps the last body and `ETag` of document and list reads in memory and asks the server `If-None-Match`, so a read of something unchanged costs a `304`. It is on by default (200 entries, 5 MiB; `Options.Cache` changes that), scoped to the signed-in caller (signing in or out forgets it), dropped for a document by a write to it, and never used for reads with `FileLinks` or `Expand`. `c.CacheStats()` says how it is doing; `client.WithoutCache()` skips it for one read.

### Concurrent edits

`WithIfMatch` makes a write conditional on the document's version. A document that has moved on is an error matching `client.ErrVersionMismatch`; read it again and decide:

```go
doc, _ := posts.Get(ctx, id, client.ReadOptions{})
_, err := posts.Replace(ctx, id, map[string]any{"title": "New", "status": "draft"}, client.WithIfMatch(doc.Meta().Version))
if errors.Is(err, client.ErrVersionMismatch) { /* someone else changed it */ }
```

A write with `If-Match` is safe to retry, so [`Options.Retry`](#create-a-client) applies to it.

### The trash, references, aggregations and search

```go
page, _ := posts.List(ctx, client.Query{Deleted: "only"})            // soft-deleted documents
doc, _ = posts.Restore(ctx, id)
err = posts.Delete(ctx, id, client.WithPurge())                       // for good

q := client.Query{Expand: client.MustExpand(client.Ref("author", "name", "avatar"), client.Ref("tags"))}
page, _ = posts.List(ctx, q)                                          // d["_expanded"]["author"]

res, _ := posts.Aggregate(ctx, client.AggregateParams{GroupBy: []string{"status"}, Metrics: []string{"count", "sum:views"}})
groups, _ := client.FillBuckets(res.Groups, client.FillBucketsOptions{Field: "paid_at", Unit: client.BucketMonth, From: "2026-01", To: "2026-12"})

hits, _ := posts.Search(ctx, `blue "running shoes" -red`, client.Query{Limit: 10})   // best first
```

### Safe retries of creates, batches, teams and organizations

```go
doc, err := posts.Create(ctx, body, client.WithIdempotencyKey(orderID))   // the same key returns the first answer

results, err := c.DB("main").Batch(ctx, []client.BatchOperation{          // all or nothing, one database
    {Op: "create", Collection: "orders", Document: map[string]any{"total": 10}},
    {Op: "patch", Collection: "stock", ID: sku, Patch: map[string]any{"left": 4}, IfMatch: 7},
})

team := posts.InTeam("docs")                  // lists and creates reach that team's documents
doc, err = posts.Create(ctx, body, client.WithOrg("acme"))
```

## Files

```go
src, err := client.OpenFile("photo.jpg")      // or client.FileFromBytes(data, "photo.jpg", "image/jpeg")
defer src.Close()
doc, err := posts.UploadFile(ctx, id, "photo", src, client.UploadOptions{
    OnProgress: func(p client.UploadProgress) { fmt.Printf("%d/%d\n", p.Loaded, p.Total) },
})
```

`UploadFile` sends the file the way the field takes it: through backd, or straight to the storage with a signed request (the SHA-256 is declared up front, files above the field's `multipart_above` go in parts of 64 MiB or more, three at a time, a failed part tried again). `PrepareUpload` stores a file before its document exists and gives the `Ref` to put in the field. Then:

```go
files := posts.Files(id, "photo")
list, err := files.List(ctx)
content, err := files.Get(ctx, "", client.GetFileOptions{Version: "thumb"})   // "" is the only file of the field
data, err := content.Bytes()
link, err := posts.FileURL(ctx, id, "photo", fileID, client.FileURLOptions{File: &list[0]})   // a public file's address needs no request
_, err = posts.DownloadFile(ctx, id, "photo", fileID, w, client.GetFileOptions{})
```

`Files.Version(name, fileID)` regenerates, generates and drops a version of an image; `client.PublicURL` and `client.VersionStatus` read what a document says about a file.

## Functions and jobs

```go
var out struct{ Refunded float64 }
job, err := c.DB("main").Call(ctx, "refund", map[string]any{"order": id}, &out, client.WithIdempotencyKey("refund-"+id))
// job == nil: the function is synchronous and out is filled in

// A call can also be made by the function's full name:
job, err = c.Functions().Call(ctx, "main/report", input, nil, client.WithRespondAsync())
if job != nil {                                   // async: poll, or wait
    err = job.WaitInto(ctx, &out, client.WaitOptions{
        PollInterval: time.Second,
        Timeout:      5 * time.Minute,
        OnProgress:   func(steps []client.JobStep, j *client.Job) { /* ctx.step() and ctx.progress() of the function */ },
    })
}
```

`Run` is `Call` followed by the wait. A function that fails gives the `*client.Error` a synchronous call would have given, with the function's own `Code`. A `*client.JobTimeoutError` means the wait gave up; the job is still running (wait again, or `Status`).

## Organizations, consents and push

`c.Orgs()` has organizations (create, update, leave, delete), `Members`, `Teams` (with role grants and time windows), `Templates` (the organization's own emails), `Invitations` and `SSO` connections; `c.Consents()` reads texts and keeps what the user accepted; `c.Account()` asks for and downloads a copy of the user's data; `c.Push()` registers the device.

```go
org, err := c.Orgs().Create(ctx, client.CreateOrgInput{Name: "Acme", Slug: "acme"})
inv, err := c.Orgs().Invitations("acme").Create(ctx, client.InviteInput{Binding: "email", Email: "bob@example.com", Roles: []string{"editor"}, Send: true})

stop := c.Push().RegisterOnLaunch(func(ctx context.Context) (*client.DeviceInput, error) {
    return &client.DeviceInput{Platform: "android", Token: fcmToken}, nil
}, func(err error) { log.Print(err) })
```

### Working in an organization

A session is scoped to one organization. A program that works in several holds one session each: `c.Org("acme")` is the client of that organization, with its own token (the storage must keep one per organization; `MemoryStorage` and `FileStorage` do), and `c.Orgs().SignedIn(ctx)` lists the ones it holds a session for. `Options.Org` makes a client the organization's from the start, so the sign-in calls go to its own routes.

## App configuration

```go
cfg := c.Config()
all, err := cfg.Load(ctx, nil)                         // or Load(ctx, []string{"features"})
enabled, err := cfg.Get(ctx, "features.reports.enabled")
v := cfg.GetSync("features.reports.max_range", "365d") // from what is held; never waits
cfg.OnChange(func(paths []string) { /* the dotted paths that changed */ })
stop := cfg.Live()                                     // follow it over the realtime stream
```

Copies are kept per user, revalidated with `If-None-Match` when their `cache_ttl` ends (a stale copy is served at once and refreshed in the background; failures go to `OnError`), and forgotten when the caller changes.

## Live views

`Collection.Subscribe` follows a collection over **one** realtime stream, shared by all the views of the client, and keeps the documents current:

```go
view, err := posts.Subscribe(client.SubscriptionSpec{
    Where: client.Where{"status": "published"},
    Load:  client.LoadSpec{Sort: []string{"-_meta.created_at"}, Limit: 50},
    References: map[string]client.ReferenceSpec{"author": {Field: "author_id", Collection: "profiles", Fields: []string{"name"}}},
}, client.SubscribeOptions{})
defer view.Close()

state, err := view.Ready(ctx)                // waits until the view is live
view.OnChange(func(s client.LiveState) { /* s.Items, s.Status, s.References */ })
for s := range view.Updates() { … }          // or read the latest state from a channel
```

The view loads from normal reads when the stream opens, applies the events that arrive meanwhile, drops older versions, resumes after a drop from the last event it saw (and starts over when the server no longer has it), reloads after a `reset` with a random delay so many clients don't all reload at once, and reconnects with a growing wait. `LiveState.Status` is `connecting`, `live`, `resyncing` or `closed`. A view that fails for good (the server refuses the subscription, the session ended) closes and reports `view.Err()` to `OnError`. [`client.Close`](#create-a-client) closes every view. Callbacks run one at a time, outside any lock, so they can read the view.

## Errors

Every failure is a `*client.Error`: the HTTP `Status`, the API's `Code` and `Message`, `Details` (which field and why), the `RequestID` to give support, `RetryAfter`, the `Path` of the call and the response `Header`. Test for a class with `errors.Is` and the `Err…` values; a class matches its subclasses (`ErrConflict` also matches `ErrIDTaken`):

```go
_, err := posts.Get(ctx, id, client.ReadOptions{})
switch {
case errors.Is(err, client.ErrNotFound):
case errors.Is(err, client.ErrForbidden):
case errors.Is(err, client.ErrNetwork):      // no answer; errors.Is(err, context.DeadlineExceeded) also works
}
var e *client.Error
if errors.As(err, &e) { log.Printf("%s: %s (request %s)", e.Code, e.Message, e.RequestID) }
```

| Value | When |
|---|---|
| `ErrValidation` (400), `ErrAuthentication` (401), `ErrForbidden` (403), `ErrNotFound` (404), `ErrConflict` (409), `ErrVersionMismatch` (412), `ErrRetryable` (429, 503) | The status classes |
| `ErrIDTaken`, `ErrInvalidID`, `ErrOrgRequiresMFA`, `ErrNotAllowed`, `ErrPasswordExpired`, `ErrSessionEnded`, `ErrIndexBuilding`, … | One per error `code`, below its class |
| `ErrNetwork` | There was no answer, or the context ended first (`Code` is `network_error` or `aborted`) |
| `ErrVerificationRequired`, `ErrSecondStepRequired` | A sign-in that needs more (`*client.SecondStepRequiredError` carries the challenge) |
| `*client.ConsentRequiredError`, `*client.PasswordExpiredError` | Typed errors with the consents to accept, or whether a reset email was queued |

## Generated types

`backd types --lang go` writes a `types.go` with a struct per document and per write, constants for the fields of `where` and `order_by`, typed collections and function calls. See [Generated types](../types/#in-go).

```go
shelf := shelfapi.NewShelfRealm(c)
page, err := shelf.Main().Assets().List(ctx, client.Query{
    Where:   client.Where{shelfapi.MainAssetsFieldPublishedAt: client.NotNull()},
    OrderBy: []string{shelfapi.MainAssetsFieldPublishedAtDesc},
})
```

## Building on the client

`Client.Do(ctx, method, path, body, out, opts...)` is the primitive every call is made with: it adds the credentials, applies retries and the cache, maps failures to `*client.Error` and reports stale types. Use it for endpoints the client has no method for (`path` is the segments after the realm; each is escaped):

```go
var out struct{ Items []json.RawMessage }
_, err := c.Do(ctx, "GET", []string{"main", "posts", id, "comments"}, nil, &out, client.WithQuery(url.Values{"limit": {"5"}}))
```

Options include `WithHeader`, `WithQuery`, `WithRawBody` (a file's bytes), `WithRaw` (the answer unread), `WithProgress`, `WithoutAuth`, `WithRetry` and `WithContentType`.

## Sizes

`client.ParseSize("1.5MiB", client.SizeBoth, false)` and `client.FormatSize(n, client.SizeBinary)` read and print sizes with the server's [grammar](../../configuration/#sizes-and-units): a number and a case-sensitive unit with no space, in decimal (`KB`…`TB`) or binary (`KiB`…`TiB`) units. They pass the same test vectors as the server, `backd-js` and the Dart client.

## An example

[`clients/go-examples/shelf`](https://github.com/fernandezvara/backd/tree/main/clients/go-examples/shelf) signs in to the tutorial's Shelf realm, lists the published assets with the generated types, saves a note and follows the assets live.
