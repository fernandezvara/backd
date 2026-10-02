package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

// emailAs sends POST /v1/acme/_email/send on the internal listener, as the
// function app/notify (which declared `email: true`) in invocation inv.
func (f *rulesFixture) emailAs(t *testing.T, fn, inv string, declared bool, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	tok := auth.SignCallback(f.callbackKey, auth.CallbackClaims{Realm: "acme", Function: fn, Inv: inv, Email: declared, Expires: f.clock.Add(time.Minute)})
	req := httptest.NewRequest("POST", "/v1/acme/_email/send", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	if contentType == "" {
		contentType = "application/json"
	}
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	f.internal.ServeHTTP(rec, req)
	return rec
}

// A function mails a realm user in their language, and addresses (with cc and
// bcc) in the one it asked for, through the realm's template for the kind.
func TestCustomEmailFromAFunction(t *testing.T) {
	f, w := newVerifyFixture(t)
	ctx := context.Background()
	if _, err := f.svc.SetLocale(ctx, f.adaID, "es"); err != nil {
		t.Fatal(err)
	}

	rec := f.emailAs(t, "app/notify", "inv-1", true, `{"kind": "order-shipped", "to_user": "`+f.adaID+`", "data": {"order_no": "A-17"}}`, "")
	if rec.Code != 202 || !strings.Contains(rec.Body.String(), `"queued"`) {
		t.Fatalf("to_user: %d %s", rec.Code, rec.Body)
	}
	in := deliver(t, f, w)
	if in.Kind != "order-shipped" || in.Locale != "es" || in.To[0].Email != "ada@example.com" || in.Subject != "Pedido A-17 enviado" || !strings.Contains(in.Text, "ada@example.com") || in.Data["order_no"] != "A-17" {
		t.Errorf("message to a user: %+v", in)
	}

	rec = f.emailAs(t, "app/notify", "inv-1", true, `{"kind": "order-shipped", "to": ["Buyer@Example.org"], "cc": ["ops@example.org"], "bcc": ["audit@example.org"], "data": {"order_no": "B-2"}, "locale": "es-MX"}`, "")
	if rec.Code != 202 {
		t.Fatalf("addresses: %d %s", rec.Code, rec.Body)
	}
	in = deliver(t, f, w)
	if in.Locale != "es" || len(in.To) != 1 || in.To[0].Email != "Buyer@Example.org" && in.To[0].Email != "buyer@example.org" || len(in.CC) != 1 || in.CC[0].Email != "ops@example.org" || len(in.BCC) != 1 || in.Subject != "Pedido B-2 enviado" {
		t.Errorf("message to addresses: %+v", in)
	}
	// Without a language: the realm's default.
	f.emailAs(t, "app/notify", "inv-1", true, `{"kind": "order-shipped", "to": ["x@example.org"], "data": {"order_no": "C-3"}}`, "")
	if in = deliver(t, f, w); in.Locale != "en" || in.Subject != "Order C-3 shipped" {
		t.Errorf("default language: %+v", in)
	}

	// The jobs say who asked and what kind, never to whom.
	jobs := f.svc
	list, _, _ := jobs.Jobs(ctx, auth.JobFilter{Origin: "function:app/notify"})
	if len(list) != 3 || list[0].Email == nil || list[0].Email.Kind != "order-shipped" {
		t.Fatalf("jobs of the function: %+v", list)
	}
	code, out := f.as(t, f.key, "GET", "/v1/acme/_admin/jobs?origin=function:app/notify", "")
	items, _ := out["items"].([]any)
	if code != 200 || len(items) != 3 || items[0].(map[string]any)["email_kind"] != "order-shipped" || items[0].(map[string]any)["origin"] != "function:app/notify" || strings.Contains(strings.ToLower(rec.Body.String()+jsonString(out)), "buyer@example.org") {
		t.Errorf("admin listing: %d %v", code, out)
	}
	if _, out = f.as(t, f.key, "GET", "/v1/acme/_admin/jobs?origin=function:app/other", ""); len(out["items"].([]any)) != 0 {
		t.Errorf("another origin: %v", out)
	}
}

func jsonString(v map[string]any) string {
	var b strings.Builder
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case map[string]any:
			for k, v := range t {
				b.WriteString(k + "=")
				walk(v)
			}
		case []any:
			for _, v := range t {
				walk(v)
			}
		case string:
			b.WriteString(t + ";")
		}
	}
	walk(v)
	return b.String()
}

func TestCustomEmailValidation(t *testing.T) {
	f, w := newVerifyFixture(t)
	ctx := context.Background()
	for name, tc := range map[string]struct {
		declared bool
		fn       string
		body     string
		status   int
	}{
		"not declared":            {false, "app/echo", `{"kind": "order-shipped", "to": ["a@example.org"]}`, 403},
		"a system kind":           {true, "app/notify", `{"kind": "reset-password", "to_user": "x"}`, 400},
		"an unknown kind":         {true, "app/notify", `{"kind": "nothing", "to": ["a@example.org"]}`, 400},
		"no kind":                 {true, "app/notify", `{"to": ["a@example.org"]}`, 400},
		"no recipient":            {true, "app/notify", `{"kind": "order-shipped"}`, 400},
		"both kinds of recipient": {true, "app/notify", `{"kind": "order-shipped", "to_user": "x", "to": ["a@example.org"]}`, 400},
		"an invalid address":      {true, "app/notify", `{"kind": "order-shipped", "to": ["nope"]}`, 400},
		"an unknown field":        {true, "app/notify", `{"kind": "order-shipped", "to": ["a@example.org"], "subject": "free text"}`, 400},
		"not an object":           {true, "app/notify", `[]`, 400},
		"too many recipients":     {true, "app/notify", `{"kind": "order-shipped", "to": ["a@example.org", "b@example.org", "c@example.org", "d@example.org", "e@example.org", "f@example.org"], "cc": ["g@example.org", "h@example.org", "i@example.org", "j@example.org", "k@example.org"]}`, 400},
		"an unknown user":         {true, "app/notify", `{"kind": "order-shipped", "to_user": "nobody"}`, 404},
	} {
		if rec := f.emailAs(t, tc.fn, "inv", tc.declared, tc.body, ""); rec.Code != tc.status {
			t.Errorf("%s: %d, want %d: %s", name, rec.Code, tc.status, rec.Body)
		}
	}
	if rec := f.emailAs(t, "app/notify", "inv", true, `{}`, "text/plain"); rec.Code != 415 {
		t.Errorf("content type: %d", rec.Code)
	}
	// A disabled user gets nothing, and nothing is queued by any refusal.
	if err := f.svc.SetDisabled(ctx, "bob@example.com", true); err != nil {
		t.Fatal(err)
	}
	if rec := f.emailAs(t, "app/notify", "inv", true, `{"kind": "order-shipped", "to_user": "`+f.bobID+`"}`, ""); rec.Code != 404 {
		t.Errorf("a disabled user: %d", rec.Code)
	}
	if w.RunOnce(ctx) {
		t.Error("a refused email was queued")
	}
	// The public listener has no such route, and callback tokens aren't accepted there.
	tok := auth.SignCallback(f.callbackKey, auth.CallbackClaims{Realm: "acme", Function: "app/notify", Email: true, Expires: f.clock.Add(time.Minute)})
	req := httptest.NewRequest("POST", "/v1/acme/_email/send", strings.NewReader(`{"kind": "order-shipped", "to": ["a@example.org"]}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code == http.StatusAccepted {
		t.Errorf("the public listener accepted it: %d", rec.Code)
	}
}

// The caps bound a badly guarded function, whoever the recipients are: per
// invocation, per function per hour, and the per-recipient limits.
func TestCustomEmailCaps(t *testing.T) {
	f, w := newVerifyFixture(t)
	ctx := context.Background()
	limits := &f.svc.Settings.Email.Limits
	limits.PerInvocation, limits.PerFunctionPerHour = 2, 6 // refused attempts count too: a function that keeps trying is the one to stop
	send := func(inv, to string) *httptest.ResponseRecorder {
		return f.emailAs(t, "app/notify", inv, true, `{"kind": "order-shipped", "to": ["`+to+`"], "data": {"order_no": "1"}}`, "")
	}

	for i, to := range []string{"a@example.org", "b@example.org"} {
		if rec := send("inv-A", to); rec.Code != 202 {
			t.Fatalf("send %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	rec := send("inv-A", "c@example.org")
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), `"email_limited"`) || !strings.Contains(rec.Body.String(), "invocation") || rec.Header().Get("Retry-After") == "" {
		t.Errorf("per invocation: %d %s", rec.Code, rec.Body)
	}
	// Another invocation of the same function has its own budget, until the function's hour is used up.
	for _, to := range []string{"d@example.org", "e@example.org"} {
		if rec := send("inv-B", to); rec.Code != 202 {
			t.Fatalf("second invocation: %d %s", rec.Code, rec.Body)
		}
	}
	if rec := send("inv-C", "f@example.org"); rec.Code != 202 { // the sixth attempt of the function
		t.Fatalf("fifth: %d %s", rec.Code, rec.Body)
	}
	if rec := send("inv-D", "g@example.org"); rec.Code != 429 || !strings.Contains(rec.Body.String(), "function") {
		t.Errorf("per function: %d %s", rec.Code, rec.Body)
	}
	// And the hour passes.
	*f.clock = f.clock.Add(61 * time.Minute)
	f.svc.Now = func() time.Time { return *f.clock }
	if rec := send("inv-E", "h@example.org"); rec.Code != 202 {
		t.Errorf("after the hour: %d %s", rec.Code, rec.Body)
	}
	for w.RunOnce(ctx) {
	}

	// The per-recipient limit applies to the same address however many functions ask.
	limits.PerFunctionPerHour, limits.PerInvocation = 1000, 1000
	for i := 0; i < 3; i++ {
		if rec := send("inv-F", "same@example.org"); rec.Code != 202 {
			t.Fatalf("same address %d: %d", i, rec.Code)
		}
	}
	if rec := send("inv-F", "same@example.org"); rec.Code != 429 || !strings.Contains(rec.Body.String(), "recipient") {
		t.Errorf("per recipient: %d %s", rec.Code, rec.Body)
	}
}
