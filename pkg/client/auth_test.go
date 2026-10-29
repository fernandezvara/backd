package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const sessionJSON = `{"token":"bds_1","token_type":"Bearer","session_id":"s1","expires_at":"2030-01-01T00:00:00Z","user":{"id":"u1","email":"ada@example.com","email_verified":true,"roles":["user"],"locale":"en","created_at":"2026-01-01T00:00:00Z"}}`

// routes is a fake backd: a handler per "METHOD /path"; the request bodies are kept in sent.
type routes struct {
	mu   sync.Mutex
	h    map[string]func(w http.ResponseWriter, r *http.Request, body map[string]any)
	sent map[string]map[string]any
	auth map[string]string
}

func newRoutes() *routes {
	return &routes{h: map[string]func(http.ResponseWriter, *http.Request, map[string]any){}, sent: map[string]map[string]any{}, auth: map[string]string{}}
}

func (rt *routes) json(key, status string, out string) {
	rt.h[key] = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		if status != "" {
			n := 0
			for _, c := range status {
				n = n*10 + int(c-'0')
			}
			w.WriteHeader(n)
		}
		io.WriteString(w, out)
	}
}

func (rt *routes) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var b map[string]any
	_ = json.Unmarshal(raw, &b)
	key := r.Method + " " + strings.TrimPrefix(r.URL.Path, "/v1/r")
	rt.mu.Lock()
	rt.sent[key], rt.auth[key] = b, r.Header.Get("Authorization")
	h := rt.h[key]
	rt.mu.Unlock()
	if h == nil {
		http.Error(w, `{"error":{"code":"not_found","message":"no route `+key+`"}}`, http.StatusNotFound)
		return
	}
	h(w, r, b)
}

func authClient(t *testing.T, rt *routes, mod ...func(*Options)) *Client {
	return newTestClient(t, rt.ServeHTTP, mod...)
}

func TestLoginStoresTheSessionAndEmitsEvents(t *testing.T) {
	rt := newRoutes()
	rt.json("POST /_auth/login", "", sessionJSON)
	rt.json("POST /_auth/logout", "", `{}`)
	c := authClient(t, rt)
	var events []AuthEvent
	stop := c.Auth().OnChange(func(ch AuthChange) { events = append(events, ch.Event) })

	s, err := c.Auth().Login(bg, SignInInput{Email: "ada@example.com", Password: "dev-p4ssw0rd!"})
	if err != nil || s.Token != "bds_1" || s.User.Email != "ada@example.com" {
		t.Fatalf("%v %+v", err, s)
	}
	if rt.sent["POST /_auth/login"]["email"] != "ada@example.com" || rt.sent["POST /_auth/login"]["password"] != "dev-p4ssw0rd!" {
		t.Errorf("sent %v", rt.sent["POST /_auth/login"])
	}
	if ok, _ := c.Auth().HasSession(bg); !ok {
		t.Error("HasSession")
	}
	if tok, _ := c.Auth().Token(bg); tok != "bds_1" || c.Auth().Current() != s {
		t.Errorf("token %q", tok)
	}
	if err := c.Auth().Logout(bg); err != nil {
		t.Fatal(err)
	}
	if rt.auth["POST /_auth/logout"] != "Bearer bds_1" {
		t.Errorf("logout sent %q", rt.auth["POST /_auth/logout"])
	}
	if ok, _ := c.Auth().HasSession(bg); ok || c.Auth().Current() != nil {
		t.Error("still signed in")
	}
	stop()
	c.Auth().emit(AuthChange{Event: SignedOut})
	if len(events) != 2 || events[0] != SignedIn || events[1] != SignedOut {
		t.Errorf("events = %v", events)
	}
}

func TestSignInReturnsTheSecondStepAndLoginFailsWithIt(t *testing.T) {
	rt := newRoutes()
	rt.json("POST /_auth/login", "", `{"mfa_required":true,"challenge":"ch1","methods":["totp","recovery_code"]}`)
	rt.json("POST /_auth/mfa/verify", "", sessionJSON)
	c := authClient(t, rt)

	step, err := c.Auth().SignIn(bg, SignInInput{Email: "a@b.c", Password: "p"})
	if err != nil || step.Kind != StepMFA || step.Challenge != "ch1" || len(step.Methods) != 2 {
		t.Fatalf("%v %+v", err, step)
	}
	if ok, _ := c.Auth().HasSession(bg); ok {
		t.Error("a second step stores nothing")
	}
	_, err = c.Auth().Login(bg, SignInInput{Email: "a@b.c", Password: "p"})
	var ss *SecondStepRequiredError
	if !errors.As(err, &ss) || ss.Code != "mfa_required" || ss.Challenge != "ch1" || !errors.Is(err, ErrSecondStepRequired) {
		t.Fatalf("%#v", err)
	}
	s, err := c.Auth().VerifyMFA(bg, "ch1", "totp", "123456")
	if err != nil || s.Token != "bds_1" {
		t.Fatalf("%v %+v", err, s)
	}
	if got := rt.sent["POST /_auth/mfa/verify"]; got["challenge"] != "ch1" || got["method"] != "totp" || got["code"] != "123456" {
		t.Errorf("sent %v", got)
	}
}

func TestEnrollmentAndConsentSteps(t *testing.T) {
	rt := newRoutes()
	rt.json("POST /_auth/login", "", `{"mfa_enrollment_required":true,"challenge":"e1"}`)
	rt.json("POST /_auth/signup", "", `{"consent_required":true,"challenge":"c1","consents":[{"name":"terms","version":"3"}]}`)
	rt.json("POST /_auth/consents/accept", "", sessionJSON)
	c := authClient(t, rt)

	step, _ := c.Auth().SignIn(bg, SignInInput{})
	if step.Kind != StepEnrollment || step.Challenge != "e1" {
		t.Errorf("%+v", step)
	}
	_, err := c.Auth().Signup(bg, SignUpInput{Email: "a@b.c", Password: "p"})
	var ss *SecondStepRequiredError
	if !errors.As(err, &ss) || ss.Code != "consent_required" || ss.Consents[0] != (ConsentRef{"terms", "3"}) {
		t.Fatalf("%#v", err)
	}
	if _, err := c.Auth().AcceptConsents(bg, "c1", map[string]string{"terms": "3"}); err != nil {
		t.Fatal(err)
	}
	if got := rt.sent["POST /_auth/consents/accept"]; got["challenge"] != "c1" || got["consents"].(map[string]any)["terms"] != "3" {
		t.Errorf("sent %v", got)
	}
}

func TestSignupNeedingVerificationStoresNothing(t *testing.T) {
	rt := newRoutes()
	rt.json("POST /_auth/signup", "202", `{}`)
	c := authClient(t, rt)
	_, err := c.Auth().Signup(bg, SignUpInput{Email: "a@b.c", Password: "p", Locale: "es", Consents: map[string]string{"terms": "3"}})
	if !errors.Is(err, ErrVerificationRequired) {
		t.Fatalf("%v", err)
	}
	if got := rt.sent["POST /_auth/signup"]; got["locale"] != "es" || got["invitation"] != nil || got["redirect_to"] != nil {
		t.Errorf("empty fields must be left out: %v", got)
	}
	if ok, _ := c.Auth().HasSession(bg); ok {
		t.Error("stored a session")
	}
}

func TestOrgRoutes(t *testing.T) {
	rt := newRoutes()
	rt.json("POST /_auth/orgs/acme/login", "", strings.Replace(sessionJSON, `"user"`, `"org":"o1","user"`, 1))
	rt.json("POST /_auth/orgs/other/login", "", sessionJSON)
	rt.json("GET /_auth/orgs/acme/providers", "", `{"items":["google"],"methods":["password"],"sso_required":true}`)
	c := authClient(t, rt, func(o *Options) { o.Org = " ACME" })
	if _, err := c.Auth().Login(bg, SignInInput{Email: "a@b.c", Password: "p"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Auth().Login(bg, SignInInput{Email: "a@b.c", Password: "p", Org: "other"}); err != nil {
		t.Fatal(err)
	}
	o, err := c.Auth().Options(bg, "")
	if err != nil || !o.SSORequired || o.Providers[0] != "google" || o.Methods[0] != "password" {
		t.Errorf("%v %+v", err, o)
	}
	// the organization's session is noted for the list of organizations
	slots, _ := c.rootClient().store.(SlotStorage).Slots(bg)
	if len(slots) != 1 || slots[0] != "meta:acme" {
		t.Errorf("slots = %v", slots)
	}
}

func TestReauthAndWithProof(t *testing.T) {
	rt := newRoutes()
	var proofs []string
	rt.h["POST /_auth/password"] = func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		proofs = append(proofs, r.Header.Get("X-Backd-Reauth"))
		if r.Header.Get("X-Backd-Reauth") == "" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"code":"reauth_required","message":"prove it"}}`)
			return
		}
		w.WriteHeader(204)
	}
	rt.json("GET /_auth/me", "", `{"id":"u1","email":"a@b.c","reauth_methods":["password","totp"],"identities":[]}`)
	exp := time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339Nano)
	rt.json("POST /_auth/reauth", "", `{"reauth_token":"rt1","expires_at":"`+exp+`"}`)
	c := authClient(t, rt)
	c.store.Set(bg, "tok")

	var offered []string
	err := c.Auth().WithProof(bg, func(_ context.Context, methods []string) (ReauthInput, error) {
		offered = methods
		return ReauthInput{Method: "password", Password: "dev-p4ssw0rd!"}, nil
	}, func(ctx context.Context) error { return c.Auth().ChangePassword(ctx, "dev-p4ssw0rd!2") })
	if err != nil {
		t.Fatal(err)
	}
	if len(offered) != 2 || len(proofs) != 2 || proofs[0] != "" || proofs[1] != "rt1" || !c.Auth().HasFreshProof() {
		t.Errorf("offered %v proofs %q", offered, proofs)
	}
	if got := rt.sent["POST /_auth/reauth"]; got["method"] != "password" || got["password"] != "dev-p4ssw0rd!" || got["code"] != nil {
		t.Errorf("sent %v", got)
	}
	// signing in again drops the proof
	rt.json("POST /_auth/login", "", sessionJSON)
	c.Auth().Login(bg, SignInInput{})
	if c.Auth().HasFreshProof() {
		t.Error("a new session keeps the old proof")
	}
}

func TestExpiryEndsTheSession(t *testing.T) {
	rt := newRoutes()
	rt.json("GET /_auth/me", "401", `{"error":{"code":"unauthenticated","message":"expired"}}`)
	c := authClient(t, rt)
	c.store.Set(bg, "tok")
	var ev AuthChange
	c.Auth().OnChange(func(ch AuthChange) { ev = ch })
	if _, err := c.Auth().Me(bg); !errors.Is(err, ErrAuthentication) {
		t.Fatal(err)
	}
	if ev.Event != SessionExpired || ev.Session != nil {
		t.Errorf("event %+v", ev)
	}
	if ok, _ := c.Auth().HasSession(bg); ok {
		t.Error("the refused token is still stored")
	}
}

func TestFileStorageSurvivesANewClient(t *testing.T) {
	rt := newRoutes()
	rt.json("POST /_auth/login", "", sessionJSON)
	path := filepath.Join(t.TempDir(), "session")
	c := authClient(t, rt, func(o *Options) { o.Storage = NewFileStorage(path) })
	if _, err := c.Auth().Login(bg, SignInInput{}); err != nil {
		t.Fatal(err)
	}
	c2 := authClient(t, rt, func(o *Options) { o.Storage = NewFileStorage(path) })
	if tok, _ := c2.Auth().Token(bg); tok != "bds_1" {
		t.Errorf("token = %q", tok)
	}
}

func TestSessionsAndMe(t *testing.T) {
	rt := newRoutes()
	rt.json("GET /_auth/sessions", "", `{"items":[{"id":"s1","current":true,"expires_at":"x"},{"id":"s2","org":"o1","connection":"google"}]}`)
	rt.json("DELETE /_auth/sessions/s2", "204", ``)
	rt.json("GET /_auth/me", "", `{"id":"u1","email":"a@b.c","identities":[{"provider":"password","last_used_at":null}],"to_accept":[{"name":"terms","version":"3"}],"org":{"id":"o1","name":"Acme","slug":"acme"}}`)
	c := authClient(t, rt)
	ss, err := c.Auth().Sessions(bg)
	if err != nil || len(ss) != 2 || !ss[0].Current || ss[1].Connection != "google" {
		t.Fatalf("%v %+v", err, ss)
	}
	if err := c.Auth().RevokeSession(bg, "s2"); err != nil {
		t.Error(err)
	}
	me, err := c.Auth().Me(bg)
	if err != nil || me.Email != "a@b.c" || me.Identities[0].LastUsedAt != nil || me.ToAccept[0].Name != "terms" || *me.Org.Slug != "acme" {
		t.Fatalf("%v %+v", err, me)
	}
}

func TestEmailFlowsSendNoCredentialsAndOmitEmptyFields(t *testing.T) {
	rt := newRoutes()
	for _, k := range []string{"POST /_auth/verify-email", "POST /_auth/verify-email/resend", "POST /_auth/reset-password/request", "POST /_auth/reset-password",
		"POST /_auth/password/update", "POST /_auth/confirm-email-change", "POST /_auth/revert-email-change", "POST /_auth/accept-invitation", "POST /_auth/orgs/find"} {
		rt.json(k, "204", ``)
	}
	c := authClient(t, rt)
	c.store.Set(bg, "tok")
	a := c.Auth()
	for _, err := range []error{
		a.VerifyEmail(bg, "t"), a.ResendVerification(bg, "a@b.c", ""), a.RequestPasswordReset(bg, "a@b.c", "", ""), a.ResetPassword(bg, "t", "p"),
		a.UpdatePassword(bg, UpdatePasswordInput{Email: "a@b.c", Password: "p", NewPassword: "n"}), a.ConfirmEmailChange(bg, "t"),
		a.RevertEmailChange(bg, "t"), a.AcceptInvitation(bg, "t", "", ""), a.FindOrgs(bg, "a@b.c", ""),
	} {
		if err != nil {
			t.Error(err)
		}
	}
	for k, h := range rt.auth {
		if h != "" {
			t.Errorf("%s sent credentials %q", k, h)
		}
	}
	if got := rt.sent["POST /_auth/accept-invitation"]; len(got) != 1 || got["token"] != "t" {
		t.Errorf("sent %v", got)
	}
	if got := rt.sent["POST /_auth/password/update"]; got["new_password"] != "n" || got["code"] != nil {
		t.Errorf("sent %v", got)
	}
}

func TestPreviewInvitationNeedsAnOrganization(t *testing.T) {
	rt := newRoutes()
	rt.json("POST /_auth/orgs/acme/invitations/preview", "", `{"org":{"id":"o1","name":"Acme"},"roles":["editor"],"teams":[{"id":"t1","name":"Docs"}],"binding":"email","expires_at":"x"}`)
	c := authClient(t, rt)
	if _, err := c.Auth().PreviewInvitation(bg, "tok", ""); err == nil {
		t.Error("an organization is required")
	}
	p, err := c.Auth().PreviewInvitation(bg, "tok", "acme")
	if err != nil || p.Org.Name != "Acme" || p.Roles[0] != "editor" || p.Teams[0].Name != "Docs" {
		t.Errorf("%v %+v", err, p)
	}
}
