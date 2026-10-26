package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/executor"
)

// hookCalls records what the realm's data hook was asked, and answers with answer (a JSON
// object, or "" for a failure); every other function answers {}.
type hookCalls struct {
	inputs []map[string]any
	answer string
	status string
}

func (h *hookCalls) install(f *rulesFixture, hook string) {
	f.runner.set(func(req executor.InvokeRequest) (executor.Result, error) {
		if req.Function != "acme/"+hook {
			return executor.Result{Status: executor.StatusOK, Output: json.RawMessage(`{}`)}, nil
		}
		var in map[string]any
		_ = json.Unmarshal(req.Envelope.Input, &in)
		h.inputs = append(h.inputs, in)
		if h.status != "" {
			return executor.Result{Status: h.status, Message: "no"}, nil
		}
		return executor.Result{Status: executor.StatusOK, Output: json.RawMessage(h.answer)}, nil
	})
}

func TestCompanyContextInEmailsAndPages(t *testing.T) {
	f, w := newVerifyFixture(t)
	f.reg.Realms["acme"].Settings.Email.DataHook = "app/echo"
	f.reg.Realms["acme"].Settings.PagesDataHook = "app/echo"
	hook := &hookCalls{answer: `{"name": "Acme Corp", "color": "#336699"}`}
	hook.install(f, "app/echo")
	org := f.orgOf(t, f.ada, "Acme Corp")
	other := f.orgOf(t, f.bob, "Elsewhere")

	// A member's address with its company: the hook is told, the delivery function gets App,
	// and the token keeps the organization for the page.
	rec := f.page(t, "POST", "/v1/acme/_auth/reset-password/request", "application/json", `{"email": "ada@example.com", "org": "`+org+`"}`, "")
	if rec.Code != 202 {
		t.Fatalf("request: %d %s", rec.Code, rec.Body)
	}
	in := deliver(t, f, w)
	if in.Kind != "reset-password" || in.Data["org"] != org || in.Data["app"].(map[string]any)["name"] != "Acme Corp" {
		t.Fatalf("message: %+v", in)
	}
	asked := hook.inputs[len(hook.inputs)-1]
	if asked["kind"] != "reset-password" || asked["org"] != org || asked["locale"] != "en" || asked["user"].(map[string]any)["email"] != "ada@example.com" {
		t.Errorf("the hook was asked: %v", asked)
	}
	before := len(hook.inputs)
	token := tokenOf(t, in)
	if rec := f.page(t, "GET", "/v1/acme/_auth/reset-password?token="+token, "", "", ""); rec.Code != 200 {
		t.Fatalf("page: %d", rec.Code)
	}
	if len(hook.inputs) != before+1 || hook.inputs[before]["org"] != org || hook.inputs[before]["kind"] != "reset-password" {
		t.Errorf("the page's hook call: %v", hook.inputs[before:])
	}

	// An organization the address doesn't belong to, and one that doesn't exist, are ignored
	// with the same answer, and the message carries no company.
	for name, body := range map[string]string{
		"a stranger's organization":  `{"email": "ada@example.com", "org": "` + other + `"}`,
		"an unknown organization":    `{"email": "ada@example.com", "org": "nosuchorg"}`,
		"an address with no account": `{"email": "nobody@example.com", "org": "` + org + `"}`,
	} {
		rec := f.page(t, "POST", "/v1/acme/_auth/reset-password/request", "application/json", body, "")
		if rec.Code != 202 || strings.TrimSpace(rec.Body.String()) != `{"status":"accepted"}` {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if !w.RunOnce(context.Background()) {
		t.Fatal("no job")
	}
	last := hook.inputs[len(hook.inputs)-1]
	if _, has := last["org"]; has {
		t.Errorf("a company the address has nothing to do with reached the hook: %v", last)
	}
}

func TestDataHookFailuresNeverStopAMessage(t *testing.T) {
	f, w := newVerifyFixture(t)
	f.reg.Realms["acme"].Settings.Email.DataHook = "app/echo"
	f.svc.Settings.Email.Limits.PerKindPerHour, f.svc.Settings.Email.Limits.PerDay, f.svc.Settings.Email.Limits.PerIPPerHour = 1000, 1000, 1000
	org := f.orgOf(t, f.ada, "Acme Corp")
	body := `{"email": "ada@example.com", "org": "` + org + `"}`
	for name, hook := range map[string]*hookCalls{
		"a function error":    {status: executor.StatusFunctionError},
		"a timeout":           {status: executor.StatusTimeout},
		"an answer too large": {answer: `{"x": "` + strings.Repeat("a", 17<<10) + `"}`},
		"not an object":       {answer: `[1, 2]`},
	} {
		hook.install(f, "app/echo")
		if rec := f.page(t, "POST", "/v1/acme/_auth/reset-password/request", "application/json", body, ""); rec.Code != 202 {
			t.Fatalf("%s: %d", name, rec.Code)
		}
		in := deliver(t, f, w)
		if in.Kind != "reset-password" || in.Data["app"] != nil || in.Data["org"] != org || len(hook.inputs) != 1 {
			t.Errorf("%s: %+v (hook calls %d)", name, in, len(hook.inputs))
		}
	}
}

func TestEmailSendKeepsTheCompany(t *testing.T) {
	f, w := newVerifyFixture(t)
	f.reg.Realms["acme"].Settings.Email.DataHook = "app/echo"
	hook := &hookCalls{answer: `{"name": "Acme Corp"}`}
	hook.install(f, "app/echo")
	org := f.orgOf(t, f.ada, "Acme Corp")
	if rec := f.emailAs(t, "app/notify", "inv-1", true, `{"kind": "order-shipped", "to": ["x@example.org"], "data": {"order_no": "A-1"}, "org": "nosuch"}`, ""); rec.Code != 400 {
		t.Errorf("an unknown organization: %d %s", rec.Code, rec.Body)
	}
	rec := f.emailAs(t, "app/notify", "inv-1", true, `{"kind": "order-shipped", "to": ["x@example.org"], "data": {"order_no": "A-1"}, "org": "`+org+`"}`, "")
	if rec.Code != 202 {
		t.Fatalf("send: %d %s", rec.Code, rec.Body)
	}
	in := deliver(t, f, w)
	if in.Data["org"] != org || in.Data["app"].(map[string]any)["name"] != "Acme Corp" || in.Data["order_no"] != "A-1" {
		t.Errorf("message: %+v", in)
	}
	if got := hook.inputs[0]; got["kind"] != "order-shipped" || got["org"] != org {
		t.Errorf("hook input: %v", got)
	}
}

// A page anyone can open (the one of a wrong token) never makes the app's function run: only a
// page that comes with a company context calls pages.data_hook.
func TestPagesDataHookNeedsACompanyContext(t *testing.T) {
	f, _ := newVerifyFixture(t)
	f.reg.Realms["acme"].Settings.PagesDataHook = "app/echo"
	hook := &hookCalls{answer: `{"name": "Acme"}`}
	hook.install(f, "app/echo")
	for range 3 {
		if rec := f.page(t, "GET", "/v1/acme/_auth/reset-password?token=not-a-token", "", "", ""); rec.Code != 400 {
			t.Fatalf("wrong token: %d", rec.Code)
		}
	}
	// A valid token without a company: no hook either.
	token := f.token(t, "reset-password", "")
	if rec := f.page(t, "GET", "/v1/acme/_auth/reset-password?token="+token, "", "", ""); rec.Code != 200 {
		t.Fatalf("page: %d", rec.Code)
	}
	if len(hook.inputs) != 0 {
		t.Errorf("the hook ran %d times for pages without a company", len(hook.inputs))
	}
}
