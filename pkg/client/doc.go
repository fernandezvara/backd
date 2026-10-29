// Package client is the Go client for backd: one entry point per realm, with the sign-in surface,
// collections that speak the query language, files, functions and their jobs, organizations, push
// devices, the app configuration and live views, the way backd-js and the Dart client do.
//
// It uses the standard library only and never imports backd's internal packages, so it stays
// small to depend on. The admin API is not part of it: administration is a server-side concern and
// a shipped application must not be able to administer a realm. Tools that need it build on [Client.Do].
//
//	c := client.New(client.Options{URL: "http://localhost:8080", Realm: "shelf", APIKey: key})
//	defer c.Close()
//
// Every call that does I/O takes a [context.Context] first. Errors are [*Error] values: use
// [errors.Is] with the Err… sentinels (ErrNotFound, ErrConflict, …) and [errors.As] with [*Error]
// or the typed errors that carry more, such as [*ConsentRequiredError].
//
// The design is in design/go-client.md.
package client
