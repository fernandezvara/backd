package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/executor"
)

// deliveredEmail decodes what the delivery function was given.
func deliveredEmail(t *testing.T, req executor.InvokeRequest) emailInput {
	t.Helper()
	var in emailInput
	if err := json.Unmarshal(req.Envelope.Input, &in); err != nil {
		t.Fatalf("delivery input: %v (%s)", err, req.Envelope.Input)
	}
	return in
}

// tokenOf pulls the token out of the link in a message.
func tokenOf(t *testing.T, in emailInput) string {
	t.Helper()
	link, _ := in.Data["link"].(string)
	u, err := url.Parse(link)
	if err != nil || u.Query().Get("token") == "" {
		t.Fatalf("no token in the link %q", link)
	}
	return u.Query().Get("token")
}

// An email is queued as a small job, rendered by a worker and handed to the
// delivery function; the message and its token are never stored.
func TestEmailJob(t *testing.T) {
	f := newRulesFixture(t)
	w := newTestWorker(t, f)
	f.svc.Now = func() time.Time { return *f.clock }
	w.reg = f.reg
	ctx := context.Background()
	store := f.svc.Store.(*authtest.MemStore)
	f.runner.set(func(executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{Status: executor.StatusOK, Output: json.RawMessage(`{"message_id":"pm-1"}`)}, nil
	})

	job, err := f.svc.QueueEmail(ctx, auth.EmailRequest{Kind: email.VerifyEmail, UserID: f.adaID, Address: "Ada@Example.com", Locale: "es", RedirectTo: "https://app.acme.example/welcome", ClientIP: "203.0.113.5", RequestID: "req-1"})
	if err != nil {
		t.Fatal(err)
	}
	// What is queued: the kind, the user, the locale and the redirect.
	stored, _, _ := f.svc.GetJob(ctx, job.ID)
	if stored.Email == nil || *stored.Email != (auth.EmailJob{Kind: email.VerifyEmail, UserID: f.adaID, Locale: "es", RedirectTo: "https://app.acme.example/welcome"}) ||
		(len(stored.Input) != 0 && string(stored.Input) != "null") || stored.Function != "deliver" || stored.Origin != "backd:email.verify-email" || stored.TimeoutMS != 900000 {
		t.Fatalf("the email job: %+v", stored)
	}

	if !w.RunOnce(ctx) {
		t.Fatal("the worker found no job")
	}
	req := f.runner.last()
	if req.Function != "acme/app/deliver" || len(req.Envelope.User) > 0 && string(req.Envelope.User) != "null" {
		t.Errorf("request: %s, user %s", req.Function, req.Envelope.User)
	}
	in := deliveredEmail(t, req)
	token := tokenOf(t, in)
	if in.ID != job.ID || in.Kind != "verify-email" || in.From != "Acme <no-reply@acme.example>" || in.ReplyTo != nil || in.Locale != "es" ||
		len(in.To) != 1 || in.To[0].Email != "ada@example.com" || len(in.CC) != 0 || len(in.BCC) != 0 {
		t.Errorf("message: %+v", in)
	}
	if in.Subject != "Confirm your email address for acme" || !strings.Contains(in.Text, in.Data["link"].(string)) || !strings.Contains(in.HTML, `href="`+in.Data["link"].(string)+`"`) {
		t.Errorf("rendering: %q / %q", in.Subject, in.Text)
	}
	if !strings.HasPrefix(in.Data["link"].(string), "https://api.acme.example/v1/acme/_auth/verify-email?token=") || in.Data["expires_at"] != f.clock.Add(48*time.Hour).UTC().Format(time.RFC3339) {
		t.Errorf("link and expiry: %v", in.Data)
	}
	// The token is kept out of the function's logs, and isn't one of its secrets.
	if len(req.Envelope.Mask) != 1 || req.Envelope.Mask[0] != token || len(req.Envelope.Secrets) != 0 {
		t.Errorf("mask %v, secrets %v", req.Envelope.Mask, req.Envelope.Secrets)
	}

	// The job ended ok, with the provider's message id as its result.
	done, _, _ := f.svc.GetJob(ctx, job.ID)
	if done.Status != auth.JobDone || done.Result == nil || done.Result.Status != "ok" || !strings.Contains(string(done.Result.Output), "pm-1") {
		t.Errorf("job result: %+v", done)
	}
	recs, _, _ := f.svc.Invocations(ctx, auth.InvocationFilter{Function: "app/deliver"})
	if len(recs) != 1 || recs[0].Origin != "backd:email.verify-email" || recs[0].JobID != job.ID {
		t.Errorf("invocation: %+v", recs)
	}

	// Nothing stored contains the token, the link or the address (client
	// addresses are counted like login attempts are): only its hash.
	tokens := store.EmailTokens()
	if len(tokens) != 1 || tokens[0].Hash != auth.HashEmailToken(token) || tokens[0].Purpose != "verify-email" || tokens[0].UserID != f.adaID ||
		tokens[0].RedirectTo != "https://app.acme.example/welcome" || !tokens[0].ExpiresAt.Equal(f.clock.Add(48*time.Hour)) {
		t.Fatalf("stored token: %+v", tokens)
	}
	everything, _ := json.Marshal(map[string]any{"job": done, "tokens": tokens, "invocations": recs, "counters": store.CounterKeys()})
	for _, secret := range []string{token, in.Data["link"].(string), "ada@example.com", "Ada@Example.com"} {
		if strings.Contains(string(everything), secret) {
			t.Errorf("stored data contains %q: %s", secret, everything)
		}
	}
	for _, k := range store.CounterKeys() {
		if strings.Contains(k, "ada") {
			t.Errorf("counter key %q holds an address", k)
		}
	}

	// Redeeming: once, for its purpose, before it expires.
	if _, err := f.svc.RedeemEmailToken(ctx, token, "reset-password"); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("another purpose: %v", err)
	}
	got, err := f.svc.RedeemEmailToken(ctx, token, "verify-email")
	if err != nil || got.UserID != f.adaID || got.RedirectTo != "https://app.acme.example/welcome" {
		t.Fatalf("redeem: %+v %v", got, err)
	}
	if _, err := f.svc.RedeemEmailToken(ctx, token, "verify-email"); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("a used token: %v", err)
	}
	if _, err := f.svc.RedeemEmailToken(ctx, "not-a-token", "verify-email"); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("an unknown token: %v", err)
	}
	f.svc.NewEmailToken(ctx, "verify-email", f.adaID, "", time.Hour)
	tok2, _, _ := f.svc.NewEmailToken(ctx, "verify-email", f.adaID, "", time.Hour)
	*f.clock = f.clock.Add(2 * time.Hour)
	if _, err := f.svc.RedeemEmailToken(ctx, tok2, "verify-email"); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("an expired token: %v", err)
	}
}

// Two parallel redemptions of one token: exactly one succeeds. A success also
// invalidates the user's other tokens of that purpose.
func TestEmailTokenRedeemedOnce(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	tok1, _, _ := f.svc.NewEmailToken(ctx, "reset-password", f.adaID, "", time.Hour)
	tok2, _, _ := f.svc.NewEmailToken(ctx, "reset-password", f.adaID, "", time.Hour)
	other, _, _ := f.svc.NewEmailToken(ctx, "verify-email", f.adaID, "", time.Hour)

	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() { _, err := f.svc.RedeemEmailToken(ctx, tok1, "reset-password"); results <- err }()
	}
	wins := 0
	for i := 0; i < 8; i++ {
		if err := <-results; err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Errorf("%d parallel redemptions succeeded, want 1", wins)
	}
	if _, err := f.svc.RedeemEmailToken(ctx, tok2, "reset-password"); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("a sibling token after a success: %v", err)
	}
	if _, err := f.svc.RedeemEmailToken(ctx, other, "verify-email"); err != nil {
		t.Errorf("a token of another purpose was invalidated: %v", err)
	}
}

// A failure that may pass is retried (each attempt with its own token); one
// the function chose is final.
func TestEmailJobRetries(t *testing.T) {
	f := newRulesFixture(t)
	w := newTestWorker(t, f)
	f.svc.Now = func() time.Time { return *f.clock }
	ctx := context.Background()
	store := f.svc.Store.(*authtest.MemStore)
	queue := func(kind string) auth.Job {
		t.Helper()
		j, err := f.svc.QueueEmail(ctx, auth.EmailRequest{Kind: kind, UserID: f.adaID, Address: "ada@example.com"})
		if err != nil {
			t.Fatal(err)
		}
		return j
	}

	f.runner.set(func(executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{Status: executor.StatusError, Message: "provider unreachable"}, nil
	})
	job := queue(email.ResetPassword)
	w.RunOnce(ctx)
	got, _, _ := f.svc.GetJob(ctx, job.ID)
	if got.Status != auth.JobQueued || got.Failures != 1 || got.NextAttemptAt.IsZero() {
		t.Fatalf("after a failure: %+v", got)
	}
	first := tokenOf(t, deliveredEmail(t, f.runner.last()))
	*f.clock = f.clock.Add(time.Minute)
	w.RunOnce(ctx)
	if second := tokenOf(t, deliveredEmail(t, f.runner.last())); second == first {
		t.Error("a retry reused the token")
	}
	if n := len(store.EmailTokens()); n != 2 {
		t.Errorf("tokens after two attempts: %d", n)
	}
	if deliveredEmail(t, f.runner.last()).ID != job.ID {
		t.Error("the message id changed between attempts")
	}

	// The function refuses the message (a 4xx it chose): no retry.
	f.runner.set(func(executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{Status: executor.StatusFunctionError, FunctionError: &executor.FunctionError{Status: 422, Code: "rejected", Message: "bad address"}}, nil
	})
	job = queue(email.PasswordChanged)
	w.RunOnce(ctx)
	if got, _, _ = f.svc.GetJob(ctx, job.ID); got.Status != auth.JobDone || got.Result.Status != "function_error" || got.Result.Code != "rejected" {
		t.Errorf("a rejected message: %+v", got)
	}
	// A kind without a token has no link at all.
	if in := deliveredEmail(t, f.runner.last()); in.Data["link"] != nil || in.Kind != "password-changed" {
		t.Errorf("password-changed: %+v", in)
	}
}

// Jobs for users or realms that are gone end instead of retrying forever.
func TestEmailJobEndsWhenItCannotBeSent(t *testing.T) {
	f := newRulesFixture(t)
	w := newTestWorker(t, f)
	ctx := context.Background()
	job, err := f.svc.QueueEmail(ctx, auth.EmailRequest{Kind: email.Welcome, UserID: "nobody", Address: "x@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	w.RunOnce(ctx)
	if got, _, _ := f.svc.GetJob(ctx, job.ID); got.Status != auth.JobDone || got.Result.Status != "error" {
		t.Errorf("job for a missing user: %+v", got)
	}
	if _, err := f.svc.QueueEmail(ctx, auth.EmailRequest{Kind: "welcome", UserID: f.adaID, Address: "not an email"}); err == nil {
		t.Error("a bad address was accepted")
	}
	// A realm without email queues nothing.
	other := &auth.Users{Store: authtest.NewMemStore(), Settings: f.reg.Realms["acme"].Settings}
	other.Settings.Email = nil
	if _, err := other.QueueEmail(ctx, auth.EmailRequest{Kind: "welcome", UserID: "u", Address: "a@example.com"}); !errors.Is(err, auth.ErrEmailNotConfigured) {
		t.Errorf("without email: %v", err)
	}
}

// Limits per recipient (per kind per hour, and per day) and per client address.
func TestEmailLimits(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	ctx := context.Background()
	queue := func(kind, address, ip string) error {
		_, err := f.svc.QueueEmail(ctx, auth.EmailRequest{Kind: kind, UserID: f.adaID, Address: address, ClientIP: ip})
		return err
	}
	var limited *auth.EmailLimitedError
	// 3 emails of one kind per hour to one recipient; the 4th is limited, others aren't.
	for i := 0; i < 3; i++ {
		if err := queue("reset-password", "ada@example.com", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := queue("reset-password", "ADA@example.com", ""); !errors.As(err, &limited) || limited.RetryAfter <= 0 || limited.RetryAfter > time.Hour {
		t.Errorf("4th email of a kind: %v", err)
	}
	if err := queue("verify-email", "ada@example.com", ""); err != nil {
		t.Errorf("another kind: %v", err)
	}
	if err := queue("reset-password", "bob@example.com", ""); err != nil {
		t.Errorf("another recipient: %v", err)
	}
	// The hour passes: the kind is free again, but the day's total counts.
	*f.clock = f.clock.Add(61 * time.Minute)
	for i := 0; i < 6; i++ { // 4 + these 6: 10 in all today
		if err := queue([]string{"welcome", "password-changed", "email-changed", "change-email", "invitation", "account-exists"}[i], "ada@example.com", ""); err != nil {
			t.Fatalf("email %d of the day: %v", i, err)
		}
	}
	if err := queue("reset-password", "ada@example.com", ""); !errors.As(err, &limited) {
		t.Errorf("11th email of the day: %v", err)
	}
	// 20 requests per hour from one address.
	for i := 0; i < 20; i++ {
		if err := queue("welcome", "user"+string(rune('a'+i))+"@example.com", "198.51.100.7"); err != nil {
			t.Fatalf("request %d from one address: %v", i, err)
		}
	}
	if err := queue("welcome", "usern@example.com", "198.51.100.7"); !errors.As(err, &limited) {
		t.Errorf("21st request from one address: %v", err)
	}
	if err := queue("welcome", "usern@example.com", "198.51.100.8"); err != nil {
		t.Errorf("another address: %v", err)
	}
}

// With no language in the request, an email is in the user's own language.
func TestEmailUsesTheUsersLanguage(t *testing.T) {
	f := newRulesFixture(t)
	w := newTestWorker(t, f)
	ctx := context.Background()
	if _, err := f.svc.SetLocale(ctx, f.adaID, "es"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.QueueEmail(ctx, auth.EmailRequest{Kind: email.PasswordChanged, UserID: f.adaID, Address: "ada@example.com"}); err != nil {
		t.Fatal(err)
	}
	w.RunOnce(ctx)
	if in := deliveredEmail(t, f.runner.last()); in.Locale != "es" {
		t.Errorf("locale = %q, want the user's es", in.Locale)
	}
	// A language the request names wins; one with no template falls back to the default.
	if _, err := f.svc.QueueEmail(ctx, auth.EmailRequest{Kind: email.Welcome, UserID: f.adaID, Address: "ada@example.com", Locale: "en"}); err != nil {
		t.Fatal(err)
	}
	w.RunOnce(ctx)
	if in := deliveredEmail(t, f.runner.last()); in.Locale != "en" {
		t.Errorf("locale = %q, want en", in.Locale)
	}
}
