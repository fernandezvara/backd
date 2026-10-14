package httpapi

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
)

const goodReset = `{"subject": "Reset your {{App.name}} password", "text": "Open {{Link}} (for {{Org.Name}})", "html": "<h1 style=\"color: #336699\">{{App.name}}</h1><p><a href=\"{{Link}}\">Reset</a></p><script>x()</script>"}`

func TestOrganizationTemplates(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Settings.Email.Customizable = []string{"reset-password", "org-invitation"}
	org := f.orgOf(t, f.ada, "Acme Corp")
	if _, err := f.svc.AdminAddMember(context.Background(), org, f.bobID, []string{"editor"}); err != nil {
		t.Fatal(err)
	}
	base := orgsPath + "/" + org + "/templates"
	one := base + "/reset-password/en"

	// Who may edit: the owner (manage: org); not an editor, not a stranger.
	for name, c := range map[string]struct {
		who  string
		code int
	}{"an editor": {f.bob, 403}, "a stranger": {f.carl, 403}} {
		if rec, out := f.doH(t, "PUT", one, goodReset, jsonHdr(c.who)); rec.Code != c.code {
			t.Errorf("%s: %d %v", name, rec.Code, out)
		}
	}
	// What can be customized, with what each must print.
	rec, out := f.doH(t, "GET", base, "", bearer(f.ada))
	kinds, _ := out["customizable"].(map[string]any)
	if rec.Code != 200 || len(kinds) != 2 || fmt.Sprint(kinds["org-invitation"]) != "map[required:[Link Data.org_name]]" || len(out["items"].([]any)) != 0 {
		t.Fatalf("list: %d %v", rec.Code, out)
	}
	// A kind that isn't listed, a language the realm lacks, and what is wrong inside.
	if rec, out := f.doH(t, "PUT", base+"/verify-email/en", goodReset, jsonHdr(f.ada)); rec.Code != 400 || errCode(out) != "template_not_customizable" {
		t.Errorf("not customizable: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "PUT", base+"/reset-password/fr", goodReset, jsonHdr(f.ada)); rec.Code != 400 {
		t.Errorf("unknown language: %d", rec.Code)
	}
	for name, c := range map[string]struct{ body, want string }{
		"a partial":       {`{"subject": "x", "text": "{{> a}} {{Link}}", "html": "{{Link}}"}`, "text"},
		"no link":         {`{"subject": "x", "text": "hello", "html": "{{Link}}"}`, "{{Link}}"},
		"an open section": {`{"subject": "x", "text": "{{Link}}", "html": "{{#a}}{{Link}}"}`, "line 1, column 1"},
		"an empty part":   {`{"subject": "", "text": "{{Link}}", "html": "{{Link}}"}`, "is required"},
		"too large":       {`{"subject": "x", "text": "{{Link}}", "html": "` + strings.Repeat("a", 65<<10) + `"}`, "larger than 64 KiB"},
	} {
		rec, out := f.doH(t, "PUT", one, c.body, jsonHdr(f.ada))
		if rec.Code != 400 || errCode(out) != "invalid_template" || !strings.Contains(jsonString(out), c.want) {
			t.Errorf("%s: %d %v", name, rec.Code, out)
		}
	}
	// Saving: versions grow, the last twenty are kept, and the sanitizer says what it dropped.
	rec, out = f.doH(t, "PUT", one, goodReset, jsonHdr(f.ada))
	if rec.Code != 200 || out["version"] != float64(1) || fmt.Sprint(out["removed"]) != "[<script>]" {
		t.Fatalf("save: %d %v", rec.Code, out)
	}
	for i := 2; i <= 23; i++ {
		body := fmt.Sprintf(`{"subject": "v%d {{App.name}}", "text": "{{Link}}", "html": "<a href=\"{{Link}}\">v%d</a>"}`, i, i)
		if rec, out := f.doH(t, "PUT", one, body, jsonHdr(f.ada)); rec.Code != 200 || out["version"] != float64(i) {
			t.Fatalf("save %d: %d %v", i, rec.Code, out)
		}
	}
	rec, out = f.doH(t, "GET", one, "", bearer(f.ada))
	versions := out["versions"].([]any)
	if rec.Code != 200 || out["version"] != float64(23) || len(versions) != 20 || versions[0].(map[string]any)["version"] != float64(22) || versions[19].(map[string]any)["version"] != float64(3) {
		t.Errorf("get: %d version %v with %d versions", rec.Code, out["version"], len(versions))
	}
	_, out = f.doH(t, "GET", base, "", bearer(f.ada))
	if items := out["items"].([]any); len(items) != 1 || strings.Contains(jsonString(items[0].(map[string]any)), "subject") {
		t.Errorf("the list holds no text: %v", out["items"])
	}
	// The audit trail records saving, never the text.
	trail, _, _ := f.svc.AuditTrail(context.Background(), auth.AuditFilter{Action: auth.AuditOrgTemplateSave})
	if len(trail) != 23 || strings.Contains(fmt.Sprint(trail[0].Details), "v23") {
		t.Errorf("audit: %d %+v", len(trail), trail[0])
	}
	// Deleting goes back to the realm's file.
	if rec, _ := f.doH(t, "DELETE", one, "", bearer(f.ada)); rec.Code != 204 {
		t.Errorf("delete: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", one, "", bearer(f.ada)); rec.Code != 404 {
		t.Errorf("get after delete: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "DELETE", one, "", bearer(f.ada)); rec.Code != 404 {
		t.Errorf("delete again: %d", rec.Code)
	}
}

func TestOrganizationTemplatesAreUsedInEmailsWithFallback(t *testing.T) {
	f, w := newVerifyFixture(t)
	f.svc.Settings.Email.Customizable = []string{"reset-password"}
	f.svc.Settings.Email.Limits.PerKindPerHour, f.svc.Settings.Email.Limits.PerDay, f.svc.Settings.Email.Limits.PerIPPerHour = 1000, 1000, 1000
	org := f.orgOf(t, f.ada, "Acme Corp")
	one := orgsPath + "/" + org + "/templates/reset-password/en"
	if rec, out := f.doH(t, "PUT", one, `{"subject": "Reset your Acme password", "text": "Open {{Link}} - Acme Corp", "html": "<a href=\"{{Link}}\">Acme reset</a><script>x()</script>"}`, jsonHdr(f.ada)); rec.Code != 200 {
		t.Fatalf("save: %d %v", rec.Code, out)
	}
	ask := func(org string) {
		t.Helper()
		body := `{"email": "ada@example.com"`
		if org != "" {
			body += `, "org": "` + org + `"`
		}
		if rec := f.page(t, "POST", "/v1/acme/_auth/reset-password/request", "application/json", body+"}", ""); rec.Code != 202 {
			t.Fatalf("request: %d", rec.Code)
		}
	}
	// With the company's context the company's text is used, sanitized; without it, the realm's.
	ask(org)
	in := deliver(t, f, w)
	if in.Subject != "Reset your Acme password" || !strings.Contains(in.Text, "Acme Corp") || strings.Contains(in.HTML, "script") || !strings.Contains(in.HTML, "Acme reset") {
		t.Errorf("company message: %+v", in)
	}
	if token := tokenOf(t, in); token == "" {
		t.Error("no token")
	}
	ask("")
	if in = deliver(t, f, w); strings.Contains(in.Subject, "Acme") {
		t.Errorf("realm message: %+v", in)
	}
	// A template that can't be used at send time falls back, and the managers are told, once.
	bad := auth.OrgTemplate{Org: org, Kind: "reset-password", Locale: "en", Version: 99, UpdatedAt: f.clock.Add(0)}
	bad.Subject, bad.Text, bad.HTML = "x", "{{#open}}", "y"
	if err := f.svc.Store.PutOrgTemplate(context.Background(), bad); err != nil {
		t.Fatal(err)
	}
	fellBack := func() {
		t.Helper()
		ask(org)
		in := deliver(t, f, w)
		if in.Kind != "reset-password" || strings.Contains(in.Text, "{{#open}}") || !strings.Contains(strings.ToLower(in.Subject), "password") {
			t.Fatalf("fallback: %+v", in)
		}
	}
	fellBack()
	notices, _, _ := f.svc.Jobs(context.Background(), auth.JobFilter{Origin: "backd:email." + "email-template-failed"})
	if len(notices) != 1 || notices[0].Email == nil || notices[0].Email.Event != "reset-password" {
		t.Fatalf("notices: %+v", notices)
	}
	got := deliver(t, f, w) // the notice
	if got.Kind != "email-template-failed" || got.To[0].Email != "ada@example.com" || !strings.Contains(got.Subject, "reset-password") || !strings.Contains(got.Text, "Acme Corp") {
		t.Errorf("notice: %+v", got)
	}
	// Told once a day: the same failure again sends the message and no second notice.
	fellBack()
	if notices, _, _ = f.svc.Jobs(context.Background(), auth.JobFilter{Origin: "backd:email." + "email-template-failed"}); len(notices) != 1 {
		t.Errorf("a second notice: %d", len(notices))
	}
}

func TestOrganizationTemplatePreviewAndTest(t *testing.T) {
	f, w := newVerifyFixture(t)
	f.svc.Settings.Email.Customizable = []string{"reset-password"}
	f.reg.Realms["acme"].Settings.Email.DataHook = "app/echo"
	hook := &hookCalls{answer: `{"name": "Acme Corp"}`}
	hook.install(f, "app/echo")
	org := f.orgOf(t, f.ada, "Acme Corp")
	base := orgsPath + "/" + org + "/templates/reset-password/en"
	draft := `{"subject": "Hi {{App.name}}", "text": "{{User.Email}}: {{Link}}", "html": "<p>{{App.name}} <a href=\"{{Link}}\">go</a></p><iframe src=\"https://evil.example\"></iframe>"`

	// Sample data: nothing calls the hook; real data does.
	rec, out := f.doH(t, "POST", base+"/preview", draft+`, "data": "sample"}`, jsonHdr(f.ada))
	if rec.Code != 200 || out["subject"] != "Hi" || !strings.Contains(out["text"].(string), "ada@example.com") || strings.Contains(out["html"].(string), "iframe") || fmt.Sprint(out["removed"]) != "[<iframe>]" || len(hook.inputs) != 0 {
		t.Fatalf("sample preview: %d %v (hook calls %d)", rec.Code, out, len(hook.inputs))
	}
	rec, out = f.doH(t, "POST", base+"/preview", draft+`, "data": "real"}`, jsonHdr(f.ada))
	if rec.Code != 200 || out["subject"] != "Hi Acme Corp" || len(hook.inputs) != 1 || hook.inputs[0]["org"] != org {
		t.Fatalf("real preview: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "POST", base+"/preview", `{"subject": "x"}`, jsonHdr(f.ada)); rec.Code != 400 {
		t.Errorf("a partial draft: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "POST", base+"/preview", `{}`, jsonHdr(f.ada)); rec.Code != 404 {
		t.Errorf("nothing saved: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "POST", base+"/preview", draft+`, "data": "other"}`, jsonHdr(f.ada)); rec.Code != 400 {
		t.Errorf("bad data: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "POST", base+"/preview", draft+"}", jsonHdr(f.bob)); rec.Code != 403 {
		t.Errorf("someone who can't edit: %d", rec.Code)
	}

	// A test goes to the requester's own address with [TEST] in the subject, through the delivery
	// function, and is limited per organization and hour.
	f.svc.Settings.Email.Limits.PerKindPerHour, f.svc.Settings.Email.Limits.PerDay, f.svc.Settings.Email.Limits.PerIPPerHour = 1000, 1000, 1000
	if rec, out := f.doH(t, "POST", base+"/test", draft+"}", jsonHdr(f.ada)); rec.Code != 202 {
		t.Fatalf("test: %d %v", rec.Code, out)
	}
	in := deliver(t, f, w)
	if in.Subject != "[TEST] Hi Acme Corp" || in.To[0].Email != "ada@example.com" || len(in.To) != 1 || !strings.Contains(in.HTML, "Acme Corp") {
		t.Errorf("test message: %+v", in)
	}
	for i := 1; i < auth.OrgTemplateTestsPerHour; i++ {
		if rec, _ := f.doH(t, "POST", base+"/test", draft+"}", jsonHdr(f.ada)); rec.Code != 202 {
			t.Fatalf("test %d: %d", i+1, rec.Code)
		}
	}
	rec, out = f.doH(t, "POST", base+"/test", draft+"}", jsonHdr(f.ada))
	if rec.Code != 429 || errCode(out) != "template_test_limit" {
		t.Errorf("over the limit: %d %v", rec.Code, out)
	}
	trail, _, _ := f.svc.AuditTrail(context.Background(), auth.AuditFilter{Action: auth.AuditOrgTemplateTest})
	if len(trail) != auth.OrgTemplateTestsPerHour {
		t.Errorf("audited tests: %d", len(trail))
	}
}

func TestRealPreviewsAreLimited(t *testing.T) {
	f, _ := newVerifyFixture(t)
	f.svc.Settings.Email.Customizable = []string{"reset-password"}
	f.reg.Realms["acme"].Settings.Email.DataHook = "app/echo"
	hook := &hookCalls{answer: `{"name": "Acme"}`}
	hook.install(f, "app/echo")
	org := f.orgOf(t, f.ada, "Acme")
	base := orgsPath + "/" + org + "/templates/reset-password/en/preview"
	draft := `{"subject": "s", "text": "{{Link}}", "html": "{{Link}}", "data": "real"}`
	for i := 0; i < auth.OrgTemplateRealPreviewsPerHour; i++ {
		if rec, out := f.doH(t, "POST", base, draft, jsonHdr(f.ada)); rec.Code != 200 {
			t.Fatalf("preview %d: %d %v", i+1, rec.Code, out)
		}
	}
	rec, out := f.doH(t, "POST", base, draft, jsonHdr(f.ada))
	if rec.Code != 429 {
		t.Errorf("over the limit: %d %v", rec.Code, out)
	}
	// Sample data costs nothing: the hook isn't called.
	if rec, _ := f.doH(t, "POST", base, strings.Replace(draft, `"real"`, `"sample"`, 1), jsonHdr(f.ada)); rec.Code != 200 {
		t.Errorf("a sample preview: %d", rec.Code)
	}
}
