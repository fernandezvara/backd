package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/executor"
)

func TestProfileIsHandedOverToTheNewUserOnce(t *testing.T) {
	f := newOAuthFixture(t)
	person := googleUser("g-new", "new@example.com", true)
	person["name"], person["given_name"], person["family_name"], person["picture"] = "Ada Lovelace", "Ada", "Lovelace", "https://img.example/ada.png"
	_, out := f.redeem(t, f.signIn(t, "google", person, nil))
	prof, _ := out["profile"].(map[string]any)
	if out["new_user"] != true || prof["name"] != "Ada Lovelace" || prof["given_name"] != "Ada" || prof["family_name"] != "Lovelace" || prof["picture"] != "https://img.example/ada.png" {
		t.Fatalf("first sign-in: %v", out)
	}
	// A returning user: no profile, new_user false. Nothing was stored for it to come from.
	person["name"] = "Ada Changed"
	_, again := f.redeem(t, f.signIn(t, "google", person, nil))
	if again["new_user"] != false || again["profile"] != nil {
		t.Errorf("second sign-in: %v", again)
	}
	// A new user whose provider gave no profile has no profile key at all.
	_, bare := f.redeem(t, f.signIn(t, "google", googleUser("g-bare", "bare@example.com", true), nil))
	if bare["new_user"] != true || bare["profile"] != nil {
		t.Errorf("no profile given: %v", bare)
	}
	// Linking to an existing account isn't a new user either.
	_, linked := f.redeem(t, f.signIn(t, "google", googleUser("g-ada", "ada@example.com", true), nil))
	if linked["new_user"] != false {
		t.Errorf("auto-link: %v", linked)
	}
	// Nothing about the profile stays in the stores that keep sign-in data.
	if recs, _, _ := f.svc.AuditTrail(context.Background(), auth.AuditFilter{}); strings.Contains(fmt.Sprint(recs), "Lovelace") {
		t.Errorf("the audit holds the profile")
	}
	ids, _ := f.svc.Identities(context.Background(), out["user"].(map[string]any)["id"].(string))
	if strings.Contains(fmt.Sprint(ids), "Lovelace") {
		t.Errorf("an identity holds the profile")
	}
}

func TestAppleFirstAuthorizationProfile(t *testing.T) {
	f := newOAuthFixture(t)
	verifier, challenge, _ := auth.NewPKCE()
	f.idp.SetUser(appleUser("a-first"))
	rec, _ := f.doH(t, "GET", oauthBase+"/apple/start?redirect_to="+url.QueryEscape(appRedirect)+"&code_challenge="+challenge, "", nil)
	back, _ := url.Parse(f.idp.Authorize(rec.Header().Get("Location")))
	form := back.Query()
	form.Set("user", `{"name": {"firstName": "Ada", "lastName": "Lovelace"}}`)
	cb, _ := f.doH(t, "POST", back.Path, form.Encode(), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	to, _ := url.Parse(cb.Header().Get("Location"))
	_, out := f.redeem(t, attempt{verifier: verifier, result: to, status: cb.Code})
	prof, _ := out["profile"].(map[string]any)
	if out["new_user"] != true || prof["name"] != "Ada Lovelace" || prof["given_name"] != "Ada" || prof["family_name"] != "Lovelace" {
		t.Errorf("profile: %v", out)
	}
	// A "user" field that is not what Apple sends is ignored, not an error.
	verifier, challenge, _ = auth.NewPKCE()
	f.idp.SetUser(appleUser("a-second"))
	rec, _ = f.doH(t, "GET", oauthBase+"/apple/start?redirect_to="+url.QueryEscape(appRedirect)+"&code_challenge="+challenge, "", nil)
	back, _ = url.Parse(f.idp.Authorize(rec.Header().Get("Location")))
	form = back.Query()
	form.Set("user", "{not json")
	cb, _ = f.doH(t, "POST", back.Path, form.Encode(), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	to, _ = url.Parse(cb.Header().Get("Location"))
	_, out = f.redeem(t, attempt{verifier: verifier, result: to, status: cb.Code})
	if out["new_user"] != true || out["profile"] != nil {
		t.Errorf("a bad user field: %v", out)
	}
}

func TestNativeSignInCarriesTheProfileToo(t *testing.T) {
	f := newOAuthFixture(t)
	claims := nativeGoogle("g-n1", "native@example.com", true, "raw-nonce")
	claims["name"], claims["picture"] = "Nat Ive", "https://img.example/n.png"
	code, out := f.idToken(t, "google", claims, nil, nil)
	prof, _ := out["profile"].(map[string]any)
	if code != 200 || out["new_user"] != true || prof["name"] != "Nat Ive" || prof["picture"] != "https://img.example/n.png" {
		t.Fatalf("first: %d %v", code, out)
	}
	if _, out := f.idToken(t, "google", claims, nil, nil); out["new_user"] != false || out["profile"] != nil {
		t.Errorf("second: %v", out)
	}
}

func TestOnSignupHookRunsForEveryNewUserWithoutLeakingTheProfile(t *testing.T) {
	f := newOAuthFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	f.svc.Settings.Account.OnSignup = "app/cleanup" // an internal, async function of the fixture
	// A password sign-up and a provider sign-up with a profile.
	if code, _ := f.do(t, "POST", "/v1/acme/_auth/signup", `{"email": "pw@example.com", "password": "dev-p4ssw0rd!"}`); code.Code != 201 {
		t.Fatalf("signup: %d", code.Code)
	}
	person := googleUser("g-new", "new@example.com", true)
	person["name"], person["given_name"] = "Ada Lovelace", "Ada"
	a := f.signIn(t, "google", person, nil)
	if a.errorCode() != "" {
		t.Fatal(a.result)
	}
	w := newTestWorker(t, f.rulesFixture)
	for w.RunOnce(context.Background()) {
	}
	var inputs []auth.OnSignupInput
	var masks [][]string
	f.runner.mu.Lock()
	for _, r := range f.runner.reqs {
		if !strings.HasSuffix(r.Function, "/cleanup") {
			continue
		}
		var in auth.OnSignupInput
		if err := json.Unmarshal(r.Envelope.Input, &in); err == nil && in.UserID != "" {
			inputs = append(inputs, in)
			masks = append(masks, r.Envelope.Mask)
		}
	}
	f.runner.mu.Unlock()
	if len(inputs) != 2 {
		t.Fatalf("the hook ran %d times: %+v", len(inputs), inputs)
	}
	for i, in := range inputs {
		switch in.Provider {
		case "password":
			if in.Profile != nil {
				t.Errorf("password sign-up carried a profile: %+v", in)
			}
		case "google":
			if in.Profile["name"] != "Ada Lovelace" || in.Profile["given_name"] != "Ada" {
				t.Errorf("provider sign-up: %+v", in)
			}
			m := strings.Join(masks[i], "|")
			if !strings.Contains(m, "Ada Lovelace") || !strings.Contains(m, "Ada") {
				t.Errorf("the profile is not masked in the logs: %v", masks[i])
			}
		default:
			t.Errorf("provider %q", in.Provider)
		}
	}
	// The jobs are done, and no longer hold the profile.
	jobs, _, _ := f.svc.Jobs(context.Background(), auth.JobFilter{Origin: auth.OnSignupOrigin})
	if len(jobs) != 2 {
		t.Fatalf("jobs: %d", len(jobs))
	}
	for _, j := range jobs {
		if j.Status != auth.JobDone || strings.Contains(string(j.Input), "Lovelace") || strings.Contains(string(j.Input), "user_id") {
			t.Errorf("job %s: %s %s", j.ID, j.Status, j.Input)
		}
	}
}

func TestOnSignupHookFailureNeverFailsTheSignUp(t *testing.T) {
	f := newOAuthFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	f.svc.Settings.Account.OnSignup = "app/cleanup"
	f.runner.set(func(executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{Status: executor.StatusFunctionError, FunctionError: &executor.FunctionError{Code: "boom", Message: "no"}}, nil
	})
	a := f.signIn(t, "google", googleUser("g-new", "new@example.com", true), nil)
	if code, _ := f.redeem(t, a); code != 200 {
		t.Fatalf("the sign-up failed with the hook: %d", code)
	}
	w := newTestWorker(t, f.rulesFixture)
	for w.RunOnce(context.Background()) {
	}
	// The user exists whatever the hook did.
	if _, err := f.svc.Find(context.Background(), "new@example.com"); err != nil {
		t.Errorf("the user is gone: %v", err)
	}
}
