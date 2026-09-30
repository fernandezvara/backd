package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/executor"
)

// TestWebhookCall proves a webhook function receives the raw body and
// headers (roadmap F13) and that its own chosen status, headers and
// body are answered as-is, without any JSON interpretation.
func TestWebhookCall(t *testing.T) {
	f := newRulesFixture(t)
	const hook = "/v1/acme/app/_func/hook"

	f.runner.set(func(req executor.InvokeRequest) (executor.Result, error) {
		if req.Envelope.Mode != "webhook" || req.Envelope.Webhook == nil {
			t.Fatalf("envelope: %+v", req.Envelope)
		}
		if req.Envelope.Webhook.Body != "raw, not JSON at all" {
			t.Errorf("body: %q", req.Envelope.Webhook.Body)
		}
		if got := req.Envelope.Webhook.Headers.Get("Stripe-Signature"); got != "t=1,v1=abc" {
			t.Errorf("header not forwarded: %q", got)
		}
		return executor.Result{Status: executor.StatusOK, Webhook: &executor.WebhookResponse{
			Status: 201, Body: `{"received":true}`, Headers: map[string]string{"Content-Type": "application/json"},
		}}, nil
	})

	req := httptest.NewRequest("POST", hook, strings.NewReader("raw, not JSON at all"))
	req.Header.Set("Content-Type", "text/plain") // never allowed for sync/async, must be fine here
	req.Header.Set("Stripe-Signature", "t=1,v1=abc")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)

	if rec.Code != 201 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if rec.Body.String() != `{"received":true}` {
		t.Errorf("body = %q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want the function's own choice", ct)
	}
}

// TestWebhookInvalidResponse proves a webhook handler that doesn't set
// a valid response answers 500, instead of silently succeeding with an
// empty body.
func TestWebhookInvalidResponse(t *testing.T) {
	f := newRulesFixture(t)
	f.runner.set(func(executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{Status: executor.StatusOK}, nil // no Webhook set
	})
	rec, out := f.doH(t, "POST", "/v1/acme/app/_func/hook", "anything", map[string]string{"Content-Type": "text/plain"})
	if rec.Code != 500 || out["error"].(map[string]any)["code"] != "function_failed" {
		t.Errorf("invalid webhook response: %d %v", rec.Code, out)
	}
}

// TestWebhookRequiresAnonymousInvokeAtRuntime is a light sanity check
// that a webhook call with no credential at all still runs (the
// "hook" fixture's invoke rule is "true"): webhook senders never
// authenticate.
func TestWebhookAnonymousCallRuns(t *testing.T) {
	f := newRulesFixture(t)
	f.runner.set(func(executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{Status: executor.StatusOK, Webhook: &executor.WebhookResponse{Status: 200, Body: "ok"}}, nil
	})
	req := httptest.NewRequest("POST", "/v1/acme/app/_func/hook", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("anonymous webhook call: %d %s", rec.Code, rec.Body)
	}
}
