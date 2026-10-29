package adminclient

import (
	"context"
	"io"
	"net/http"
	"net/url"
)

// AppConfig is the realm's app configuration (_admin/app-config).
type AppConfig struct{ base }

// Get reads the configuration; the headers carry its ETag.
func (x *AppConfig) Get(ctx context.Context, out any, opts ...Opt) (http.Header, error) {
	return x.call(ctx, "GET", s("app-config"), nil, nil, out, opts)
}

// Patch changes it, conditional on the version read.
func (x *AppConfig) Patch(ctx context.Context, version string, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "PATCH", s("app-config"), nil, body, out, append(Opts{ifMatch(version)}, opts...))
	return err
}

// History lists the past versions.
func (x *AppConfig) History(ctx context.Context, q url.Values, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("app-config", "history"), q, nil, out, opts)
	return err
}

// Rollback goes back to a past version, conditional on the current one.
func (x *AppConfig) Rollback(ctx context.Context, version string, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("app-config", "rollback"), nil, body, out, append(Opts{ifMatch(version)}, opts...))
	return err
}

// APIKeys are the realm's API keys (_admin/apikeys).
type APIKeys struct{ base }

func (x *APIKeys) Create(ctx context.Context, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("apikeys"), nil, body, out, opts)
	return err
}

func (x *APIKeys) List(ctx context.Context, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("apikeys"), nil, nil, out, opts)
	return err
}

func (x *APIKeys) Delete(ctx context.Context, name string, opts ...Opt) error {
	_, err := x.call(ctx, "DELETE", s("apikeys", name), nil, nil, nil, opts)
	return err
}

// Audit is the audit trail (_admin/audit).
type Audit struct{ base }

func (x *Audit) List(ctx context.Context, q url.Values, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("audit"), q, nil, out, opts)
	return err
}

// Compliance is the realm's compliance scope (_admin/compliance).
type Compliance struct{ base }

func (x *Compliance) Scope(ctx context.Context, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("compliance", "scope"), nil, nil, out, opts)
	return err
}

// Consents are the consents of the realm's users (_admin/consents).
type Consents struct{ base }

// Notify asks the users to accept a consent again.
func (x *Consents) Notify(ctx context.Context, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("consents", "notify"), nil, body, out, opts)
	return err
}

// DataChecks check the documents against their schemas (_admin/data-checks).
type DataChecks struct{ base }

func (x *DataChecks) Start(ctx context.Context, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("data-checks"), nil, body, out, opts)
	return err
}

// Get reads the result of a check: of a database's collection, or the check with a name.
func (x *DataChecks) Get(ctx context.Context, database, name string, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("data-checks", database, name), nil, nil, out, opts)
	return err
}

func (x *DataChecks) List(ctx context.Context, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("data-checks"), nil, nil, out, opts)
	return err
}

// Data acts on documents regardless of their rules (_admin/data).
type Data struct{ base }

// Transfer moves a document to another organization (and team).
func (x *Data) Transfer(ctx context.Context, database, collection, id string, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("data", database, collection, id, "_transfer"), nil, body, out, opts)
	return err
}

// AssignOrg gives the documents of a collection that have no organization one.
func (x *Data) AssignOrg(ctx context.Context, database, collection string, q url.Values, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("data", database, collection, "_assign_org"), q, body, out, opts)
	return err
}

// Encryption is field encryption (_admin/encryption).
type Encryption struct{ base }

func (x *Encryption) Status(ctx context.Context, q url.Values, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("encryption"), q, nil, out, opts)
	return err
}

func (x *Encryption) Rekey(ctx context.Context, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("encryption", "rekey"), nil, body, out, opts)
	return err
}

func (x *Encryption) Migrate(ctx context.Context, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("encryption", "migrate"), nil, body, out, opts)
	return err
}

// Export is the realm export (_admin/export): a plan, its parts, and the lease that keeps one
// export running at a time.
type Export struct{ base }

// Plan asks for the plan of an export (GET), or starts one with a body (POST).
func (x *Export) Plan(ctx context.Context, q url.Values, body, out any, opts ...Opt) error {
	method := "GET"
	if body != nil {
		method = "POST"
	}
	_, err := x.call(ctx, method, s("export"), q, body, out, opts)
	return err
}

// Complete ends the export and releases its lease.
func (x *Export) Complete(ctx context.Context, q url.Values, out any, opts ...Opt) error {
	_, err := x.call(ctx, "DELETE", s("export"), q, nil, out, opts)
	return err
}

// Part opens a part of the export: the caller reads and closes the body.
func (x *Export) Part(ctx context.Context, q url.Values, opts ...Opt) (*Stream, error) {
	return x.stream(ctx, s("export"), q, opts)
}

// Files regenerate and manage file versions (_admin/files).
type Files struct{ base }

func (x *Files) RegenerateVersions(ctx context.Context, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("files", "versions", "regenerate"), nil, body, out, opts)
	return err
}

// Functions invokes and controls functions (_admin/functions, _admin/schedules, _admin/invocations).
type Functions struct{ base }

// Invoke runs a function through the admin route (the way to run an internal one), and returns
// the response headers.
func (x *Functions) Invoke(ctx context.Context, database, name string, body, out any, opts ...Opt) (http.Header, error) {
	return x.call(ctx, "POST", s("functions", database, name, "invoke"), nil, body, out, opts)
}

// Control sends an operation to a function's schedule: "pause", "resume", "skip", …
func (x *Functions) Control(ctx context.Context, database, name, op string, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("functions", database, name, op), nil, nil, out, opts)
	return err
}

func (x *Functions) Schedules(ctx context.Context, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("schedules"), nil, nil, out, opts)
	return err
}

func (x *Functions) Invocations(ctx context.Context, q url.Values, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("invocations"), q, nil, out, opts)
	return err
}

// Import loads an export into the realm (_admin/import).
type Import struct{ base }

func (x *Import) Begin(ctx context.Context, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("import"), nil, body, out, opts)
	return err
}

// Abort cancels the import and releases its lease.
func (x *Import) Abort(ctx context.Context, q url.Values, opts ...Opt) error {
	_, err := x.call(ctx, "DELETE", s("import"), q, nil, nil, opts)
	return err
}

// Send sends a chunk of documents (POST, application/x-ndjson) or a file (PUT, application/octet-stream).
func (x *Import) Send(ctx context.Context, method string, q url.Values, body io.Reader, contentType string, out any, opts ...Opt) error {
	return x.send(ctx, method, s("import"), q, body, contentType, out, opts)
}

// Jobs are the background jobs (_admin/jobs).
type Jobs struct{ base }

func (x *Jobs) List(ctx context.Context, q url.Values, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("jobs"), q, nil, out, opts)
	return err
}

func (x *Jobs) Get(ctx context.Context, id string, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("jobs", id), nil, nil, out, opts)
	return err
}

// Control sends an operation to a job: "cancel", "rerun", …
func (x *Jobs) Control(ctx context.Context, id, op string, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("jobs", id, op), nil, nil, out, opts)
	return err
}

// MFA is the realm's second-factor policy (_admin/mfa).
type MFA struct{ base }

func (x *MFA) Status(ctx context.Context, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("mfa"), nil, nil, out, opts)
	return err
}

// Passwords expire and notify (_admin/passwords).
type Passwords struct{ base }

func (x *Passwords) Expire(ctx context.Context, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("passwords", "expire"), nil, body, out, opts)
	return err
}

func (x *Passwords) Notify(ctx context.Context, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("passwords", "notify"), nil, nil, out, opts)
	return err
}

// Push is the push notification side (_admin/push).
type Push struct{ base }

func (x *Push) Sends(ctx context.Context, q url.Values, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("push", "sends"), q, nil, out, opts)
	return err
}

func (x *Push) Send(ctx context.Context, id string, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("push", "sends", id), nil, nil, out, opts)
	return err
}

func (x *Push) Test(ctx context.Context, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("push", "test"), nil, body, out, opts)
	return err
}

// DeleteDevices removes devices of a platform ("web", after the keys were rotated).
func (x *Push) DeleteDevices(ctx context.Context, q url.Values, out any, opts ...Opt) error {
	_, err := x.call(ctx, "DELETE", s("push", "devices"), q, nil, out, opts)
	return err
}

// Secrets are the realm's secrets (_admin/secrets).
type Secrets struct{ base }

func (x *Secrets) List(ctx context.Context, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("secrets"), nil, nil, out, opts)
	return err
}

// Put sets a secret's value.
func (x *Secrets) Put(ctx context.Context, name string, body any, opts ...Opt) error {
	_, err := x.call(ctx, "PUT", s("secrets", name), nil, body, nil, opts)
	return err
}

// Create makes a generated secret key.
func (x *Secrets) Create(ctx context.Context, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("secrets"), nil, body, out, opts)
	return err
}

func (x *Secrets) Rotate(ctx context.Context, name string, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("secrets", name, "rotate"), nil, body, out, opts)
	return err
}

func (x *Secrets) Delete(ctx context.Context, name string, q url.Values, opts ...Opt) error {
	_, err := x.call(ctx, "DELETE", s("secrets", name), q, nil, nil, opts)
	return err
}

// Sessions ends the realm's sessions (_admin/sessions).
type Sessions struct{ base }

// RevokeAll ends every session of the realm.
func (x *Sessions) RevokeAll(ctx context.Context, out any, opts ...Opt) error {
	_, err := x.call(ctx, "DELETE", s("sessions"), nil, nil, out, opts)
	return err
}

// Storage is file storage (_admin/storage).
type Storage struct{ base }

func (x *Storage) Usage(ctx context.Context, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("storage"), nil, nil, out, opts)
	return err
}

func (x *Storage) Reconcile(ctx context.Context, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("storage", "reconcile"), nil, body, out, opts)
	return err
}

func (x *Storage) Check(ctx context.Context, q url.Values, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("storage", "check"), q, nil, out, opts)
	return err
}

// Triggers are the function triggers (_admin/triggers).
type Triggers struct{ base }

func (x *Triggers) Status(ctx context.Context, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("triggers", "status"), nil, nil, out, opts)
	return err
}

func (x *Triggers) Gaps(ctx context.Context, q url.Values, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("triggers", "gaps"), q, nil, out, opts)
	return err
}

func (x *Triggers) ResolveGap(ctx context.Context, id string, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("triggers", "gaps", id, "resolve"), nil, nil, out, opts)
	return err
}

// Types are the generated types of the realm (_admin/types).
type Types struct{ base }

func (x *Types) Get(ctx context.Context, q url.Values, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("types"), q, nil, out, opts)
	return err
}
