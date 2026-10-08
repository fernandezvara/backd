package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

// worker is a worker whose provider calls go to the fake provider.
func (f *oauthFixture) worker(t *testing.T) *Worker {
	t.Helper()
	return NewWorker(Config{
		Registry: f.reg, Store: f.store, OAuth: f.idp.Service(),
		Users: func(realm string) *auth.Users {
			if realm == "acme" {
				return f.svc
			}
			return nil
		},
		Now: func() time.Time { return *f.clock },
		Log: slog.New(slog.DiscardHandler),
	}, "test-worker")
}

func appleUser(sub string) map[string]any {
	return map[string]any{"iss": "https://appleid.apple.com", "aud": "com.acme.web", "sub": sub, "email": sub + "@privaterelay.appleid.com", "email_verified": "true"}
}

// appleIdentity signs a new Apple user in over the redirect flow (the fake gives a refresh token)
// and returns the user's id.
func (f *oauthFixture) appleIdentity(t *testing.T, sub string) string {
	t.Helper()
	f.idp.GiveRefreshToken("refresh-" + sub)
	a := f.signIn(t, "apple", appleUser(sub), nil)
	_, out := f.redeem(t, a)
	return out["user"].(map[string]any)["id"].(string)
}

func sealedAppleToken(t *testing.T, f *oauthFixture, userID string) auth.Identity {
	t.Helper()
	id, err := f.svc.Store.IdentityOf(context.Background(), userID, "apple")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAppleRefreshTokenIsKeptSealed(t *testing.T) {
	f := newOAuthFixture(t)
	userID := f.appleIdentity(t, "a-1")
	id := sealedAppleToken(t, f, userID)
	if id.AppleRefreshToken == "" || strings.Contains(id.AppleRefreshToken, "refresh-a-1") || id.AppleClientID != "com.acme.web" {
		t.Fatalf("identity: %+v", id)
	}
	if plain, err := f.svc.OpenToken(id.AppleRefreshToken); err != nil || plain != "refresh-a-1" {
		t.Errorf("OpenToken = %q, %v", plain, err)
	}
	// It never comes back through the API.
	_, me := f.doH(t, "GET", "/v1/acme/_auth/me", "", bearer(f.loginOf(t, userID)))
	if b, _ := json.Marshal(me); strings.Contains(string(b), "refresh") || strings.Contains(string(b), id.AppleRefreshToken) {
		t.Errorf("me shows the token: %s", b)
	}
	// With revoke_on_delete: false nothing is kept.
	f.svc.Settings.Providers["apple"].RevokeOnDelete = false
	f.idp.GiveRefreshToken("refresh-a-2")
	a := f.signIn(t, "apple", appleUser("a-2"), nil)
	_, out := f.redeem(t, a)
	if id := sealedAppleToken(t, f, out["user"].(map[string]any)["id"].(string)); id.AppleRefreshToken != "" {
		t.Errorf("a token was kept with revoke_on_delete: false: %+v", id)
	}
}

// loginOf gives a session for a user (the tests have no password for provider users).
func (f *oauthFixture) loginOf(t *testing.T, userID string) string {
	t.Helper()
	u, err := f.svc.Store.UserByID(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	p, token, err := f.svc.StartProviderSession(context.Background(), u)
	if err != nil || p.User.ID != userID {
		t.Fatal(err)
	}
	return token
}

func TestErasingAUserRevokesTheirAppleToken(t *testing.T) {
	f := newOAuthFixture(t)
	userID := f.appleIdentity(t, "a-1")
	code, out := f.as(t, f.key, "DELETE", "/v1/acme/_admin/users/"+userID, "")
	if code != 202 {
		t.Fatalf("erase: %d %v", code, out)
	}
	w := f.worker(t)
	for w.RunOnce(context.Background()) {
	}
	revs := f.idp.Revocations()
	if len(revs) != 1 || revs[0].Get("token") != "refresh-a-1" || revs[0].Get("client_id") != "com.acme.web" || revs[0].Get("token_type_hint") != "refresh_token" || strings.Count(revs[0].Get("client_secret"), ".") != 2 {
		t.Fatalf("revocations: %v", revs)
	}
	recs, _, _ := f.svc.AuditTrail(context.Background(), auth.AuditFilter{Action: auth.AuditAppleRevoked})
	if len(recs) != 1 || recs[0].Target != "user:"+userID {
		t.Errorf("audit: %+v", recs)
	}
	if b, _ := json.Marshal(recs); strings.Contains(string(b), "refresh-a-1") {
		t.Errorf("the audit holds the token: %s", b)
	}
	// The job forgot the token and is done.
	jobs, _, _ := f.svc.Jobs(context.Background(), auth.JobFilter{Origin: auth.RevokeOrigin})
	if len(jobs) != 1 || jobs[0].Status != auth.JobDone || jobs[0].Revoke.Token != "" || jobs[0].Result.Status != "ok" {
		t.Errorf("jobs: %+v", jobs)
	}
	// The user is gone from Apple's side only once: another run revokes nothing more.
	for w.RunOnce(context.Background()) {
	}
	if n := len(f.idp.Revocations()); n != 1 {
		t.Errorf("revoked %d times", n)
	}
}

func TestUnlinkingAppleRevokesItsToken(t *testing.T) {
	f := newOAuthFixture(t)
	// ada has a password; she links Apple, then removes it.
	f.idp.GiveRefreshToken("refresh-ada")
	f.idp.SetUser(appleUser("a-ada"))
	body := `{"redirect_to": "` + appRedirect + `", "code_challenge": "` + func() string { _, c, _ := auth.NewPKCE(); return c }() + `", "intent": "link"}`
	_, out := f.doH(t, "POST", oauthBase+"/apple/start", body, bearer(f.ada))
	if a := f.finish(t, "apple", out["authorize_url"].(string), "", false); a.result.Query().Get("linked") != "apple" {
		t.Fatalf("link: %v", a.result)
	}
	if rec, _ := f.doH(t, "DELETE", "/v1/acme/_auth/identities/apple", "", bearer(f.ada)); rec.Code != 204 {
		t.Fatalf("unlink: %d", rec.Code)
	}
	for f.worker(t).RunOnce(context.Background()) {
	}
	if revs := f.idp.Revocations(); len(revs) != 1 || revs[0].Get("token") != "refresh-ada" {
		t.Errorf("revocations: %v", revs)
	}
	// Unlinking Google, which holds no Apple token, queues nothing.
	f.idp.GiveRefreshToken("")
	if jobs, _, _ := f.svc.Jobs(context.Background(), auth.JobFilter{Origin: auth.RevokeOrigin}); len(jobs) != 1 {
		t.Errorf("revoke jobs: %d", len(jobs))
	}
}

func TestRevocationIsTriedAgain(t *testing.T) {
	f := newOAuthFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	userID := f.appleIdentity(t, "a-1")
	f.idp.FailRevoke(500)
	if code, _ := f.as(t, f.key, "DELETE", "/v1/acme/_admin/users/"+userID, ""); code != 202 {
		t.Fatal(code)
	}
	w := f.worker(t)
	for w.RunOnce(context.Background()) {
	}
	jobs, _, _ := f.svc.Jobs(context.Background(), auth.JobFilter{Origin: auth.RevokeOrigin})
	if len(jobs) != 1 || jobs[0].Status == auth.JobDone || jobs[0].Revoke.Token == "" || jobs[0].Failures != 1 {
		t.Fatalf("after a failure: %+v", jobs)
	}
	// Apple comes back; the job runs again when its wait is over.
	f.idp.FailRevoke(0)
	*f.clock = f.clock.Add(time.Hour)
	for w.RunOnce(context.Background()) {
	}
	jobs, _, _ = f.svc.Jobs(context.Background(), auth.JobFilter{Origin: auth.RevokeOrigin})
	if len(jobs) != 1 || jobs[0].Status != auth.JobDone || jobs[0].Revoke.Token != "" || jobs[0].Result.Status != "ok" {
		t.Errorf("after the retry: %+v", jobs)
	}
	if revs := f.idp.Revocations(); len(revs) != 2 || revs[1].Get("token") != "refresh-a-1" {
		t.Errorf("revocations: %v", revs)
	}
}

func TestRevocationIsSkippedWhenTheRealmNoLongerRevokes(t *testing.T) {
	f := newOAuthFixture(t)
	userID := f.appleIdentity(t, "a-1")
	if code, _ := f.as(t, f.key, "DELETE", "/v1/acme/_admin/users/"+userID, ""); code != 202 {
		t.Fatal(code)
	}
	f.svc.Settings.Providers["apple"].RevokeOnDelete = false
	for f.worker(t).RunOnce(context.Background()) {
	}
	jobs, _, _ := f.svc.Jobs(context.Background(), auth.JobFilter{Origin: auth.RevokeOrigin})
	if len(f.idp.Revocations()) != 0 || len(jobs) != 1 || jobs[0].Revoke.Token != "" || jobs[0].Result.Status != "skipped" {
		t.Errorf("revocations %v, jobs %+v", f.idp.Revocations(), jobs)
	}
}

func TestNativeAppleNeedsTheAuthorizationCode(t *testing.T) {
	f := newOAuthFixture(t)
	ios := func(sub string) map[string]any {
		c := appleUser(sub)
		c["aud"] = "com.acme.ios"
		c["nonce"] = "raw-nonce"
		return c
	}
	// Without the code the realm can't revoke later: refused.
	if code, out := f.idToken(t, "apple", ios("a-n1"), nil, nil); code != 400 || len(out["error"].(map[string]any)["details"].([]any)) == 0 {
		t.Errorf("no code: %d %v", code, out)
	}
	// A code that is someone else's (another subject) is refused, and nothing is kept.
	f.idp.GrantCode("code-other", map[string]any{"iss": "https://appleid.apple.com", "aud": "com.acme.ios", "sub": "somebody-else"})
	if code, out := f.idToken(t, "apple", ios("a-n1"), map[string]any{"authorization_code": "code-other"}, nil); code != 401 {
		t.Errorf("another person's code: %d %v", code, out)
	}
	if code, _ := f.idToken(t, "apple", ios("a-n1"), map[string]any{"authorization_code": "unknown"}, nil); code != 401 {
		t.Errorf("an unknown code: %d", code)
	}
	if users, _ := f.svc.List(context.Background()); len(users) != 3 {
		t.Errorf("a refused sign-in made a user: %d", len(users))
	}
	// The right one: the exchange acts as the app (its bundle id), and the token is kept.
	f.idp.GrantCode("code-ok", map[string]any{"iss": "https://appleid.apple.com", "aud": "com.acme.ios", "sub": "a-n1"})
	f.idp.GiveRefreshToken("refresh-ios")
	code, out := f.idToken(t, "apple", ios("a-n1"), map[string]any{"authorization_code": "code-ok"}, nil)
	if code != 200 {
		t.Fatalf("sign-in: %d %v", code, out)
	}
	forms := f.idp.TokenRequests()
	form := forms[len(forms)-1]
	payload, _ := base64.RawURLEncoding.DecodeString(strings.Split(form.Get("client_secret"), ".")[1])
	if form.Get("client_id") != "com.acme.ios" || form.Has("redirect_uri") || !strings.Contains(string(payload), `"sub":"com.acme.ios"`) {
		t.Errorf("exchange: %v payload %s", form, payload)
	}
	id := sealedAppleToken(t, f, out["user"].(map[string]any)["id"].(string))
	if plain, _ := f.svc.OpenToken(id.AppleRefreshToken); plain != "refresh-ios" || id.AppleClientID != "com.acme.ios" {
		t.Errorf("kept: %+v", id)
	}
	// A realm that doesn't revoke asks for no code.
	f.svc.Settings.Providers["apple"].RevokeOnDelete = false
	if code, _ := f.idToken(t, "apple", ios("a-n2"), nil, nil); code != 200 {
		t.Errorf("revoke_on_delete: false without a code: %d", code)
	}
}

func TestRevocationFailsVisiblyWhenItNeverWorks(t *testing.T) {
	f := newOAuthFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	userID := f.appleIdentity(t, "a-1")
	f.idp.FailRevoke(500)
	if code, _ := f.as(t, f.key, "DELETE", "/v1/acme/_admin/users/"+userID, ""); code != 202 {
		t.Fatal(code)
	}
	w := f.worker(t)
	for range 10 {
		for w.RunOnce(context.Background()) {
		}
		*f.clock = f.clock.Add(7 * time.Hour)
	}
	jobs, _, _ := f.svc.Jobs(context.Background(), auth.JobFilter{Origin: auth.RevokeOrigin})
	if len(jobs) != 1 || jobs[0].Status != auth.JobDone || jobs[0].Result.Status == "ok" || jobs[0].Failures != revokeAttempts-1 {
		t.Fatalf("job: %+v", jobs)
	}
	// The sealed token stays in the failed job (until it expires), and Apple was asked every time.
	if jobs[0].Revoke.Token == "" || len(f.idp.Revocations()) != revokeAttempts {
		t.Errorf("token kept %v, revocations %d", jobs[0].Revoke.Token != "", len(f.idp.Revocations()))
	}
}
