// Package adminclient is the /_admin/... side of backd's HTTP API, as an add-on to the Go client:
// adminclient.New takes a configured *client.Client and reuses its address, realm, credentials,
// transport, retries and error mapping through client.Do. It is internal on purpose: a shipped
// program must not be able to administer a realm, so the public client leaves administration out
// and only backd's own tools (the CLI) build on this. If the admin API is ever made public this
// package moves to pkg/admin with the same shape.
//
// Methods are named for what they do and take the pieces of the request (ids, a query, a body);
// answers are decoded into out (a pointer, or nil). Every method takes request options last
// (client.WithHeader for the proof of identity of a sensitive action, client.WithIfMatch, …).
package adminclient

import (
	"context"
	"io"
	"net/http"
	"net/url"

	"github.com/fernandezvara/backd/pkg/client"
)

// Admin calls the admin API through a client. Its fields group the endpoints by what they manage.
type Admin struct {
	c *client.Client

	AppConfig  *AppConfig
	APIKeys    *APIKeys
	Audit      *Audit
	Compliance *Compliance
	Consents   *Consents
	DataChecks *DataChecks
	Data       *Data
	Encryption *Encryption
	Export     *Export
	Files      *Files
	Functions  *Functions
	Import     *Import
	Jobs       *Jobs
	MFA        *MFA
	Orgs       *Orgs
	Passwords  *Passwords
	Push       *Push
	Secrets    *Secrets
	Sessions   *Sessions
	Storage    *Storage
	Triggers   *Triggers
	Types      *Types
	Users      *Users
}

// New returns the admin API of a configured client. It opens no connection of its own.
func New(c *client.Client) *Admin {
	a := &Admin{c: c}
	base := base{a}
	a.AppConfig, a.APIKeys, a.Audit, a.Compliance, a.Consents = &AppConfig{base}, &APIKeys{base}, &Audit{base}, &Compliance{base}, &Consents{base}
	a.DataChecks, a.Data, a.Encryption, a.Export, a.Files = &DataChecks{base}, &Data{base}, &Encryption{base}, &Export{base}, &Files{base}
	a.Functions, a.Import, a.Jobs, a.MFA, a.Orgs = &Functions{base}, &Import{base}, &Jobs{base}, &MFA{base}, &Orgs{base}
	a.Passwords, a.Push, a.Secrets, a.Sessions, a.Storage = &Passwords{base}, &Push{base}, &Secrets{base}, &Sessions{base}, &Storage{base}
	a.Triggers, a.Types, a.Users = &Triggers{base}, &Types{base}, &Users{base}
	return a
}

// Client returns the client the admin API is called through.
func (a *Admin) Client() *client.Client { return a.c }

type base struct{ a *Admin }

// call sends one request to /v1/{realm}/_admin/{path…} and decodes a JSON answer into out. It
// returns the response headers.
func (b base) call(ctx context.Context, method string, path []string, q url.Values, body, out any, opts []client.RequestOption) (http.Header, error) {
	res, err := b.a.c.Do(ctx, method, append([]string{"_admin"}, path...), body, out, append([]client.RequestOption{client.WithQuery(q)}, opts...)...)
	if err != nil {
		return nil, err
	}
	return res.Header, nil
}

// open sends a request and returns the answer unread: the caller closes Body. For answers too big
// to hold (an export's parts, a package).
func (b base) open(ctx context.Context, method string, path []string, q url.Values, opts []client.RequestOption) (*client.Response, error) {
	return b.a.c.Do(ctx, method, append([]string{"_admin"}, path...), nil, nil,
		append([]client.RequestOption{client.WithQuery(q), client.WithRaw()}, opts...)...)
}

// send sends a raw body (a chunk, a file) with its content type.
func (b base) send(ctx context.Context, method string, path []string, q url.Values, body io.Reader, contentType string, out any, opts []client.RequestOption) error {
	_, err := b.a.c.Do(ctx, method, append([]string{"_admin"}, path...), nil, out,
		append([]client.RequestOption{client.WithQuery(q), client.WithRawBody(body, contentType)}, opts...)...)
	return err
}

func s(parts ...string) []string { return parts }

// Stream is an answer read as it arrives. The caller closes Body.
type Stream = client.Response

func (b base) stream(ctx context.Context, path []string, q url.Values, opts []client.RequestOption) (*Stream, error) {
	return b.open(ctx, "GET", path, q, opts)
}

// Opt is a request option: a header, an If-Match, …
type Opt = client.RequestOption

// Opts is a list of request options.
type Opts = []client.RequestOption

// Proof makes the request options that carry the fresh proof of identity a sensitive action asks for.
func Proof(headers map[string]string) Opts {
	var o Opts
	for k, v := range headers {
		o = append(o, client.WithHeader(k, v))
	}
	return o
}

// ifMatch is the If-Match of a version of the app configuration.
func ifMatch(version string) client.RequestOption {
	return client.WithHeader("If-Match", `"`+version+`"`)
}
