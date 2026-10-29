package adminclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/pkg/client"
)

var bg = context.Background()

type seen struct {
	line, body, ctype string
	hdr               http.Header
}

// fake answers every request with an empty JSON object and keeps what it was asked.
func fake(t *testing.T) (*Admin, *[]seen) {
	var log []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		log = append(log, seen{r.Method + " " + strings.TrimPrefix(r.URL.EscapedPath(), "/v1/r/") + queryOf(r), string(b), r.Header.Get("Content-Type"), r.Header.Clone()})
		w.Header().Set("ETag", `"v7"`)
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(client.Options{URL: srv.URL, Realm: "r", APIKey: "bdk_admin"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return New(c), &log
}

func queryOf(r *http.Request) string {
	if r.URL.RawQuery == "" {
		return ""
	}
	return "?" + r.URL.RawQuery
}

func TestEveryEndpointIsTheRightRequest(t *testing.T) {
	a, log := fake(t)
	q := url.Values{"limit": {"5"}}
	body := map[string]any{"x": 1}
	var out map[string]any
	cases := []struct {
		name string
		do   func() error
		want string
		body string
	}{
		{"app-config get", func() error { _, e := a.AppConfig.Get(bg, &out); return e }, "GET _admin/app-config", ""},
		{"app-config patch", func() error { return a.AppConfig.Patch(bg, "7", body, &out) }, "PATCH _admin/app-config", `{"x":1}`},
		{"app-config history", func() error { return a.AppConfig.History(bg, q, &out) }, "GET _admin/app-config/history?limit=5", ""},
		{"app-config rollback", func() error { return a.AppConfig.Rollback(bg, "7", body, &out) }, "POST _admin/app-config/rollback", `{"x":1}`},
		{"apikeys create", func() error { return a.APIKeys.Create(bg, body, &out) }, "POST _admin/apikeys", `{"x":1}`},
		{"apikeys list", func() error { return a.APIKeys.List(bg, &out) }, "GET _admin/apikeys", ""},
		{"apikeys delete", func() error { return a.APIKeys.Delete(bg, "ci key") }, "DELETE _admin/apikeys/ci%20key", ""},
		{"audit", func() error { return a.Audit.List(bg, q, &out) }, "GET _admin/audit?limit=5", ""},
		{"compliance", func() error { return a.Compliance.Scope(bg, &out) }, "GET _admin/compliance/scope", ""},
		{"consents notify", func() error { return a.Consents.Notify(bg, body, &out) }, "POST _admin/consents/notify", `{"x":1}`},
		{"data-checks start", func() error { return a.DataChecks.Start(bg, body, &out) }, "POST _admin/data-checks", `{"x":1}`},
		{"data-checks get", func() error { return a.DataChecks.Get(bg, "main", "notes", &out) }, "GET _admin/data-checks/main/notes", ""},
		{"data-checks list", func() error { return a.DataChecks.List(bg, &out) }, "GET _admin/data-checks", ""},
		{"data transfer", func() error { return a.Data.Transfer(bg, "main", "notes", "n1", body, nil) }, "POST _admin/data/main/notes/n1/_transfer", `{"x":1}`},
		{"data assign-org", func() error { return a.Data.AssignOrg(bg, "main", "notes", q, body, &out) }, "POST _admin/data/main/notes/_assign_org?limit=5", `{"x":1}`},
		{"encryption status", func() error { return a.Encryption.Status(bg, q, &out) }, "GET _admin/encryption?limit=5", ""},
		{"encryption rekey", func() error { return a.Encryption.Rekey(bg, body, &out) }, "POST _admin/encryption/rekey", `{"x":1}`},
		{"encryption migrate", func() error { return a.Encryption.Migrate(bg, body, &out) }, "POST _admin/encryption/migrate", `{"x":1}`},
		{"export plan get", func() error { return a.Export.Plan(bg, q, nil, &out) }, "GET _admin/export?limit=5", ""},
		{"export plan post", func() error { return a.Export.Plan(bg, nil, body, &out) }, "POST _admin/export", `{"x":1}`},
		{"export complete", func() error { return a.Export.Complete(bg, url.Values{"complete": {"true"}}, &out) }, "DELETE _admin/export?complete=true", ""},
		{"files regenerate", func() error { return a.Files.RegenerateVersions(bg, body, &out) }, "POST _admin/files/versions/regenerate", `{"x":1}`},
		{"functions invoke", func() error { _, e := a.Functions.Invoke(bg, "main", "job", body, &out); return e }, "POST _admin/functions/main/job/invoke", `{"x":1}`},
		{"functions control", func() error { return a.Functions.Control(bg, "main", "job", "pause", &out) }, "POST _admin/functions/main/job/pause", ""},
		{"schedules", func() error { return a.Functions.Schedules(bg, &out) }, "GET _admin/schedules", ""},
		{"invocations", func() error { return a.Functions.Invocations(bg, q, &out) }, "GET _admin/invocations?limit=5", ""},
		{"import begin", func() error { return a.Import.Begin(bg, body, &out) }, "POST _admin/import", `{"x":1}`},
		{"import abort", func() error { return a.Import.Abort(bg, q) }, "DELETE _admin/import?limit=5", ""},
		{"jobs list", func() error { return a.Jobs.List(bg, q, &out) }, "GET _admin/jobs?limit=5", ""},
		{"jobs get", func() error { return a.Jobs.Get(bg, "j1", &out) }, "GET _admin/jobs/j1", ""},
		{"jobs control", func() error { return a.Jobs.Control(bg, "j1", "cancel", &out) }, "POST _admin/jobs/j1/cancel", ""},
		{"mfa", func() error { return a.MFA.Status(bg, &out) }, "GET _admin/mfa", ""},
		{"passwords expire", func() error { return a.Passwords.Expire(bg, body, &out) }, "POST _admin/passwords/expire", `{"x":1}`},
		{"passwords notify", func() error { return a.Passwords.Notify(bg, &out) }, "POST _admin/passwords/notify", ""},
		{"push sends", func() error { return a.Push.Sends(bg, q, &out) }, "GET _admin/push/sends?limit=5", ""},
		{"push send", func() error { return a.Push.Send(bg, "s1", &out) }, "GET _admin/push/sends/s1", ""},
		{"push test", func() error { return a.Push.Test(bg, body, &out) }, "POST _admin/push/test", `{"x":1}`},
		{"push delete devices", func() error { return a.Push.DeleteDevices(bg, url.Values{"platform": {"web"}}, &out) }, "DELETE _admin/push/devices?platform=web", ""},
		{"secrets list", func() error { return a.Secrets.List(bg, &out) }, "GET _admin/secrets", ""},
		{"secrets put", func() error { return a.Secrets.Put(bg, "k", body) }, "PUT _admin/secrets/k", `{"x":1}`},
		{"secrets create", func() error { return a.Secrets.Create(bg, body, &out) }, "POST _admin/secrets", `{"x":1}`},
		{"secrets rotate", func() error { return a.Secrets.Rotate(bg, "k", body, &out) }, "POST _admin/secrets/k/rotate", `{"x":1}`},
		{"secrets delete", func() error { return a.Secrets.Delete(bg, "k", q) }, "DELETE _admin/secrets/k?limit=5", ""},
		{"sessions", func() error { return a.Sessions.RevokeAll(bg, &out) }, "DELETE _admin/sessions", ""},
		{"storage usage", func() error { return a.Storage.Usage(bg, &out) }, "GET _admin/storage", ""},
		{"storage reconcile", func() error { return a.Storage.Reconcile(bg, body, &out) }, "POST _admin/storage/reconcile", `{"x":1}`},
		{"storage check", func() error { return a.Storage.Check(bg, q, &out) }, "POST _admin/storage/check?limit=5", ""},
		{"triggers status", func() error { return a.Triggers.Status(bg, &out) }, "GET _admin/triggers/status", ""},
		{"triggers gaps", func() error { return a.Triggers.Gaps(bg, q, &out) }, "GET _admin/triggers/gaps?limit=5", ""},
		{"triggers resolve", func() error { return a.Triggers.ResolveGap(bg, "g1", &out) }, "POST _admin/triggers/gaps/g1/resolve", ""},
		{"types", func() error { return a.Types.Get(bg, q, &out) }, "GET _admin/types?limit=5", ""},

		{"users list", func() error { return a.Users.List(bg, q, &out) }, "GET _admin/users?limit=5", ""},
		{"users create", func() error { return a.Users.Create(bg, body, &out) }, "POST _admin/users", `{"x":1}`},
		{"users patch", func() error { return a.Users.Patch(bg, "u1", body, &out) }, "PATCH _admin/users/u1", `{"x":1}`},
		{"users erase", func() error { return a.Users.Erase(bg, "u1", &out) }, "DELETE _admin/users/u1", ""},
		{"users password", func() error { return a.Users.SetPassword(bg, "u1", body, &out) }, "POST _admin/users/u1/password", `{"x":1}`},
		{"users expire", func() error { return a.Users.ExpirePassword(bg, "u1", body, &out) }, "POST _admin/users/u1/expire-password", `{"x":1}`},
		{"users email", func() error { return a.Users.ChangeEmail(bg, "u1", body, &out) }, "POST _admin/users/u1/email", `{"x":1}`},
		{"users grant", func() error { return a.Users.Role(bg, "u1", "editor/x", true, &out) }, "PUT _admin/users/u1/roles/editor%2Fx", ""},
		{"users revoke", func() error { return a.Users.Role(bg, "u1", "editor", false, &out) }, "DELETE _admin/users/u1/roles/editor", ""},
		{"users owned", func() error { return a.Users.Owned(bg, "u1", &out) }, "GET _admin/users/u1/owned", ""},
		{"users networks", func() error { return a.Users.SetNetworks(bg, "u1", body, &out) }, "PUT _admin/users/u1/networks", `{"x":1}`},
		{"users identities", func() error { return a.Users.Identities(bg, "u1", &out) }, "GET _admin/users/u1/identities", ""},
		{"users unlink", func() error { return a.Users.UnlinkIdentity(bg, "u1", "google") }, "DELETE _admin/users/u1/identities/google", ""},
		{"users mfa", func() error { return a.Users.MFA(bg, "u1", &out) }, "GET _admin/users/u1/mfa", ""},
		{"users mfa reset", func() error { return a.Users.ResetMFA(bg, "u1") }, "POST _admin/users/u1/mfa/reset", ""},
		{"users reset link", func() error { return a.Users.ResetLink(bg, "u1", body, &out) }, "POST _admin/users/u1/reset-link", `{"x":1}`},
		{"users consents", func() error { return a.Users.AcceptConsents(bg, "u1", body, &out) }, "POST _admin/users/u1/consents", `{"x":1}`},
		{"users devices", func() error { return a.Users.Devices(bg, "u1", &out) }, "GET _admin/users/u1/devices", ""},
		{"users device delete", func() error { return a.Users.DeleteDevice(bg, "u1", "d1") }, "DELETE _admin/users/u1/devices/d1", ""},
		{"users export start", func() error { return a.Users.StartExport(bg, "u1", body, &out) }, "POST _admin/users/u1/export", `{"x":1}`},
		{"users export", func() error { return a.Users.Export(bg, "u1", "e1", &out) }, "GET _admin/users/u1/exports/e1", ""},

		{"orgs list", func() error { return a.Orgs.List(bg, q, &out) }, "GET _admin/orgs?limit=5", ""},
		{"orgs get", func() error { return a.Orgs.Get(bg, "acme", &out) }, "GET _admin/orgs/acme", ""},
		{"orgs create", func() error { return a.Orgs.Create(bg, body, &out) }, "POST _admin/orgs", `{"x":1}`},
		{"orgs patch", func() error { return a.Orgs.Patch(bg, "acme", body, nil) }, "PATCH _admin/orgs/acme", `{"x":1}`},
		{"orgs delete", func() error { return a.Orgs.Delete(bg, "acme") }, "DELETE _admin/orgs/acme", ""},
		{"orgs members", func() error { return a.Orgs.Members(bg, "acme", q, &out) }, "GET _admin/orgs/acme/members?limit=5", ""},
		{"orgs add member", func() error { return a.Orgs.AddMember(bg, "acme", body) }, "POST _admin/orgs/acme/members", `{"x":1}`},
		{"orgs owner", func() error { return a.Orgs.SetOwner(bg, "acme", body) }, "POST _admin/orgs/acme/owner", `{"x":1}`},
		{"orgs claims", func() error { return a.Orgs.SetClaims(bg, "acme", body) }, "PUT _admin/orgs/acme/claims", `{"x":1}`},
		{"orgs slug", func() error { return a.Orgs.SetSlug(bg, "acme", body, &out) }, "PUT _admin/orgs/acme/slug", `{"x":1}`},
		{"orgs reactivate", func() error { return a.Orgs.Reactivate(bg, "acme", body) }, "POST _admin/orgs/acme/reactivate", `{"x":1}`},
		{"orgs owned", func() error { return a.Orgs.Owned(bg, "acme", &out) }, "GET _admin/orgs/acme/owned", ""},
		{"orgs invite", func() error { return a.Orgs.Invite(bg, "acme", body, &out) }, "POST _admin/orgs/acme/invitations", `{"x":1}`},
		{"orgs invitations", func() error { return a.Orgs.Invitations(bg, "acme", &out) }, "GET _admin/orgs/acme/invitations", ""},
		{"orgs revoke invitation", func() error { return a.Orgs.RevokeInvitation(bg, "acme", "i1") }, "DELETE _admin/orgs/acme/invitations/i1", ""},
		{"orgs sso list", func() error { return a.Orgs.SSOList(bg, "acme", &out) }, "GET _admin/orgs/acme/sso", ""},
		{"orgs sso set", func() error { return a.Orgs.SSOSet(bg, "acme", "google", body, &out) }, "PUT _admin/orgs/acme/sso/google", `{"x":1}`},
		{"orgs sso delete", func() error { return a.Orgs.SSODelete(bg, "acme", "google") }, "DELETE _admin/orgs/acme/sso/google", ""},
	}
	for _, c := range cases {
		*log = nil
		if err := c.do(); err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if len(*log) != 1 || (*log)[0].line != c.want || (*log)[0].body != c.body {
			t.Errorf("%s:\n got %+v\nwant %s %q", c.name, *log, c.want, c.body)
		}
		if (*log)[0].hdr.Get("Authorization") != "Bearer bdk_admin" {
			t.Errorf("%s: the client's credentials were not used: %q", c.name, (*log)[0].hdr.Get("Authorization"))
		}
	}
}

func TestIfMatchAndProofHeaders(t *testing.T) {
	a, log := fake(t)
	a.AppConfig.Patch(bg, "7", map[string]any{}, nil)
	a.AppConfig.Rollback(bg, "8", map[string]any{}, nil)
	a.Secrets.Delete(bg, "k", nil, Proof(map[string]string{"X-Backd-Reauth": "proof-1"})...)
	a.Users.ResetLink(bg, "u1", nil, nil, Proof(map[string]string{"X-Backd-Reauth": "proof-2"})...)
	got := []string{(*log)[0].hdr.Get("If-Match"), (*log)[1].hdr.Get("If-Match"), (*log)[2].hdr.Get("X-Backd-Reauth"), (*log)[3].hdr.Get("X-Backd-Reauth")}
	if strings.Join(got, "|") != `"7"|"8"|proof-1|proof-2` {
		t.Errorf("headers %q", got)
	}
}

func TestAppConfigGetReturnsTheETag(t *testing.T) {
	a, _ := fake(t)
	hdr, err := a.AppConfig.Get(bg, nil)
	if err != nil || hdr.Get("ETag") != `"v7"` {
		t.Errorf("%v %v", err, hdr)
	}
}

func TestErrorsAreTheClientsErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":{"code":"forbidden","message":"admins only","request_id":"rq1"}}`)
	}))
	defer srv.Close()
	c, _ := client.New(client.Options{URL: srv.URL, Realm: "r", APIKey: "k"})
	err := New(c).Jobs.Get(bg, "j1", nil)
	var e *client.Error
	if !errors.Is(err, client.ErrForbidden) || !errors.As(err, &e) || e.RequestID != "rq1" || e.Message != "admins only" {
		t.Errorf("%#v", err)
	}
}

func TestStreamsAreReturnedUnread(t *testing.T) {
	var lines []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lines = append(lines, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery+" "+r.Header.Get("X-Backd-Export"))
		io.WriteString(w, "PART-BYTES")
	}))
	defer srv.Close()
	c, _ := client.New(client.Options{URL: srv.URL, Realm: "r", APIKey: "k"})
	a := New(c)
	for _, open := range []func() (*Stream, error){
		func() (*Stream, error) {
			return a.Export.Part(bg, url.Values{"part": {"data/main.ndjson"}}, client.WithHeader("X-Backd-Export", "lease1"))
		},
		func() (*Stream, error) { return a.Users.OpenExport(bg, "u1", "e1") },
	} {
		st, err := open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(st.Body)
		st.Body.Close()
		if string(b) != "PART-BYTES" {
			t.Errorf("%q", b)
		}
	}
	if lines[0] != "GET /v1/r/_admin/export?part=data%2Fmain.ndjson lease1" || lines[1] != "GET /v1/r/_admin/users/u1/exports/e1/download? " {
		t.Errorf("%q", lines)
	}
}

func TestImportSendsARawBody(t *testing.T) {
	a, log := fake(t)
	if err := a.Import.Send(bg, "POST", url.Values{"phase": {"data"}}, strings.NewReader("{\"a\":1}\n"), "application/x-ndjson", nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Import.Send(bg, "PUT", url.Values{"part": {"f"}}, strings.NewReader("BYTES"), "application/octet-stream", nil, client.WithHeader("X-Backd-Import", "lease")); err != nil {
		t.Fatal(err)
	}
	l := *log
	if l[0].line != "POST _admin/import?phase=data" || l[0].ctype != "application/x-ndjson" || l[0].body != "{\"a\":1}\n" ||
		l[1].line != "PUT _admin/import?part=f" || l[1].ctype != "application/octet-stream" || l[1].hdr.Get("X-Backd-Import") != "lease" {
		t.Errorf("%+v", l)
	}
}

func TestOnlyTheStandardLibraryAndTheClient(t *testing.T) {
	// the add-on builds on pkg/client alone: no server packages
	for _, f := range []string{"admin.go", "areas.go", "users.go"} {
		src, err := readFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(src, "\n") {
			if strings.Contains(line, `"github.com/fernandezvara/backd/`) && !strings.Contains(line, "pkg/client") {
				t.Errorf("%s imports %s", f, strings.TrimSpace(line))
			}
		}
	}
}

func readFile(name string) (string, error) {
	b, err := os.ReadFile(name)
	return string(b), err
}
