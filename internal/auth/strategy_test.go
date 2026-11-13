package auth_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
)

// The strategies of these tests: web is the password alone, admin adds the authenticator,
// google is a provider, deep asks for the authenticator twice.
func strategies() []registry.Strategy {
	return []registry.Strategy{
		{Name: "web", Methods: []string{"password"}},
		{Name: "admin", Methods: []string{"password", "totp"}},
		{Name: "deep", Methods: []string{"google", "totp", "totp"}},
		{Name: "google", Methods: []string{"google"}},
	}
}

func TestSignInNeedsAStrategyWhenSeveralStartWithThePassword(t *testing.T) {
	ctx := context.Background()
	f := newMFAFixture(t)
	f.svc.Settings.Strategies = strategies()
	if _, err := f.svc.SignInWith(ctx, "", "ada@example.com", mfaPassword, ""); !errors.Is(err, ErrStrategyRequired) {
		t.Errorf("no strategy named: %v", err)
	}
	for name, strategy := range map[string]string{"unlisted": "ghost", "another first method": "google"} {
		if _, err := f.svc.SignInWith(ctx, strategy, "ada@example.com", mfaPassword, ""); !errors.Is(err, ErrUnknownStrategy) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Before anything about the person is looked at: a wrong password is no different.
	if _, err := f.svc.SignInWith(ctx, "ghost", "nobody@example.com", "x", ""); !errors.Is(err, ErrUnknownStrategy) {
		t.Errorf("unknown strategy and unknown person: %v", err)
	}
}

func TestTheStrategyDecidesTheStepsAndIsRecorded(t *testing.T) {
	ctx := context.Background()
	f := newMFAFixture(t) // ada has an authenticator
	f.svc.Settings.Strategies = strategies()
	// web has no second step: her own authenticator adds none.
	out, err := f.svc.SignInWith(ctx, "web", "ada@example.com", mfaPassword, "")
	if err != nil || out.Challenge != nil || out.Principal.Session.Strategy != "web" || out.Principal.Session.MFA {
		t.Fatalf("web: %+v %v", out, err)
	}
	// admin asks for the code, and the session records both methods and the strategy.
	out, err = f.svc.SignInWith(ctx, "admin", "ada@example.com", mfaPassword, "")
	if err != nil || out.Challenge == nil || out.Challenge.Strategy != "admin" || !slices.Equal(out.Challenge.Next, []string{"totp"}) {
		t.Fatalf("admin: %+v %v", out, err)
	}
	v, err := f.svc.VerifyMFA(ctx, out.Challenge.Token, MethodTOTP, f.code(t, 0), "")
	s := v.Principal.Session
	if err != nil || s.Strategy != "admin" || !s.MFA || !slices.Equal(s.Methods, []string{"password", "totp"}) {
		t.Fatalf("verified: %+v %v", s, err)
	}
	// A recovery code stands in for the code wherever totp is next.
	out, _ = f.svc.SignInWith(ctx, "admin", "ada@example.com", mfaPassword, "")
	if v, err = f.svc.VerifyMFA(ctx, out.Challenge.Token, MethodRecovery, f.codes[0], ""); err != nil || v.Principal.Session.Strategy != "admin" {
		t.Errorf("recovery code: %+v %v", v.Principal.Session, err)
	}
}

func TestAStrategyOfThreeStepsIssuesTheChallengeAgain(t *testing.T) {
	ctx := context.Background()
	f := newMFAFixture(t)
	f.svc.Settings.Strategies = strategies()
	out, err := f.svc.StartProviderSessionFor(ctx, f.user, "google", "deep", false, nil, "")
	if err != nil || out.Challenge == nil {
		t.Fatalf("first step: %+v %v", out, err)
	}
	v, err := f.svc.VerifyMFA(ctx, out.Challenge.Token, MethodTOTP, f.code(t, 0), "")
	if err != nil || v.Challenge == nil || v.Token != "" || v.Challenge.Token == out.Challenge.Token {
		t.Fatalf("second step: %+v %v", v, err)
	}
	// The first code is spent: the next step asks for the next time step's.
	*f.clock = f.clock.Add(time.Minute)
	v, err = f.svc.VerifyMFA(ctx, v.Challenge.Token, MethodTOTP, f.code(t, 0), "")
	if err != nil || v.Challenge != nil || v.Token == "" || len(v.Principal.Session.Methods) != 3 {
		t.Fatalf("last step: %+v %v", v, err)
	}
}

func TestAPersonWithoutAnAuthenticatorEnrolsWhereTheStrategyAsksForOne(t *testing.T) {
	ctx := context.Background()
	f := newMFAFixture(t)
	f.svc.Settings.Strategies = strategies()
	bob, err := f.svc.Create(ctx, "bob@example.com", new(mfaPassword))
	if err != nil {
		t.Fatal(err)
	}
	_ = bob
	if out, err := f.svc.SignInWith(ctx, "web", "bob@example.com", mfaPassword, ""); err != nil || out.Challenge != nil {
		t.Fatalf("web: %+v %v", out, err)
	}
	out, err := f.svc.SignInWith(ctx, "admin", "bob@example.com", mfaPassword, "")
	if err != nil || out.Challenge == nil || out.Challenge.Kind != ChallengeEnroll || out.Challenge.Strategy != "admin" {
		t.Fatalf("admin: %+v %v", out, err)
	}
	setup, err := f.svc.BeginEnrollment(ctx, out.Challenge.Token)
	if err != nil {
		t.Fatal(err)
	}
	v, _, err := f.svc.CompleteEnrollment(ctx, out.Challenge.Token, setup.SetupID, totpAt(t, setup.Secret, *f.clock))
	if err != nil || v.Principal.Session.Strategy != "admin" || !v.Principal.Session.MFA {
		t.Errorf("enrolled: %+v %v", v.Principal.Session, err)
	}
}

func TestRolesFollowTheStrategyOfTheSession(t *testing.T) {
	ctx := context.Background()
	f := newMFAFixture(t)
	f.svc.Settings.Strategies = strategies()
	f.svc.Settings.Roles = map[string]registry.Role{
		"owner":  {Strategies: []string{"admin"}},
		"editor": {},
	}
	if err := f.store.AddRoles(ctx, f.user.ID, []string{"owner", "editor"}, *f.clock); err != nil {
		t.Fatal(err)
	}
	// With the weak strategy the session holds editor only, and says what stayed inactive.
	out, err := f.svc.SignInWith(ctx, "web", "ada@example.com", mfaPassword, "")
	p := out.Principal
	if err != nil || !slices.Equal(p.User.Roles, []string{"editor"}) || !slices.Equal(p.RolesInactive, []string{"owner"}) {
		t.Fatalf("web: roles %v inactive %v %v", p.User.Roles, p.RolesInactive, err)
	}
	// Every later request sees the same filter, from the stored session.
	got, err := f.svc.Authenticate(ctx, out.Token)
	if err != nil || !slices.Equal(got.User.Roles, []string{"editor"}) || !slices.Equal(got.RolesInactive, []string{"owner"}) {
		t.Fatalf("authenticate: %v %v %v", got.User.Roles, got.RolesInactive, err)
	}
	// The stronger strategy holds both.
	ch, _ := f.svc.SignInWith(ctx, "admin", "ada@example.com", mfaPassword, "")
	v, err := f.svc.VerifyMFA(ctx, ch.Challenge.Token, MethodTOTP, f.code(t, 0), "")
	if err != nil || !slices.Equal(v.Principal.User.Roles, []string{"owner", "editor"}) || len(v.Principal.RolesInactive) != 0 {
		t.Errorf("admin: %+v %v", v.Principal, err)
	}
	// A session from before strategies keeps every role.
	if p, _ := f.svc.Settings.ActiveRoles([]string{"owner"}, ""); len(p) != 1 {
		t.Errorf("no strategy: %v", p)
	}
}

// Every way of signing in ends in the strategy engine: a session starts only from finishSignIn
// (and the organization switch), and finishSignIn is reached only from the engine and the consent
// step. A new entry point that starts a session by itself would skip the strategy's steps and
// the roles' filter, so this test lists where each call may stand.
func TestEverySignInEntryPointEndsInTheEngine(t *testing.T) {
	calls := func(name string) map[string]int {
		files, _ := filepath.Glob("*.go")
		out := map[string]int{}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, _ := os.ReadFile(f)
			if n := strings.Count(string(src), name+"("); n > 0 {
				out[f] = n
			}
		}
		return out
	}
	want := map[string]map[string]int{
		// the entry points: password (sign-in, sign-up, verify email), magic link, providers, organizations
		"s.afterFirstFactor": {"sessions.go": 2, "verify.go": 1, "orgsignin.go": 1, "magiclink.go": 1, "oauth.go": 2},
		// the engine finishes the strategy; the consent step finishes what it paused
		"s.finishSignIn": {"mfa.go": 1},
		// a session is started in these places only
		"s.startSessionWith": {"consents_signin.go": 2, "orgsignin.go": 1},
	}
	for name, files := range want {
		got := calls(name)
		if len(got) != len(files) {
			t.Errorf("%s is called from %v, want %v", name, got, files)
			continue
		}
		for f, n := range files {
			if got[f] != n {
				t.Errorf("%s in %s: %d calls, want %d (a sign-in that skips the engine skips the strategy's steps)", name, f, got[f], n)
			}
		}
	}
}

// An organization's own connection is a first method (sso) like any other: the strategy that
// starts with it decides the steps, and the session records the strategy.
func TestAConnectionSignInFollowsAnSSOStrategy(t *testing.T) {
	ctx := context.Background()
	f := newMFAFixture(t)
	f.svc.Settings.Strategies = []registry.Strategy{
		{Name: "sso", Methods: []string{"sso"}},
		{Name: "sso_totp", Methods: []string{"sso", "totp"}},
	}
	method := ConnectionMethod("org-1", "google")
	if _, err := f.svc.StartProviderSessionFor(ctx, f.user, method, "", false, nil, "org-1"); !errors.Is(err, ErrStrategyRequired) {
		t.Fatalf("two strategies start with sso and none is named: %v", err)
	}
	out, err := f.svc.StartProviderSessionFor(ctx, f.user, method, "sso", false, nil, "org-1")
	if err != nil || out.Challenge != nil || out.Principal.Session.Strategy != "sso" || out.Principal.Session.Org != "org-1" {
		t.Fatalf("sso: %+v %v", out, err)
	}
	out, err = f.svc.StartProviderSessionFor(ctx, f.user, method, "sso_totp", false, nil, "org-1")
	if err != nil || out.Challenge == nil || out.Challenge.Strategy != "sso_totp" {
		t.Fatalf("sso_totp: %+v %v", out, err)
	}
	v, err := f.svc.VerifyMFA(ctx, out.Challenge.Token, MethodTOTP, f.code(t, 0), "")
	if err != nil || v.Principal.Session.Strategy != "sso_totp" || v.Principal.Session.Org != "org-1" || !v.Principal.Session.MFA {
		t.Fatalf("verified: %+v %v", v.Principal.Session, err)
	}
	// A realm with no sso strategy offers no way in through a connection.
	f.svc.Settings.Strategies = strategies()
	if _, err := f.svc.StartProviderSessionFor(ctx, f.user, method, "", false, nil, "org-1"); !errors.Is(err, ErrUnknownStrategy) {
		t.Errorf("no sso strategy: %v", err)
	}
}

// A function's data calls on behalf of a user see what the request saw: the session's
// strategy, and only the roles that strategy keeps active.
func TestCallbackCallerKeepsTheStrategyAndItsRoles(t *testing.T) {
	ctx := context.Background()
	f := newMFAFixture(t)
	f.svc.Settings.Strategies = strategies()
	f.svc.Settings.Roles = map[string]registry.Role{"owner": {Strategies: []string{"admin"}}, "editor": {}}
	if err := f.store.AddRoles(ctx, f.user.ID, []string{"owner", "editor"}, *f.clock); err != nil {
		t.Fatal(err)
	}
	for strategy, want := range map[string][]string{"web": {"editor"}, "admin": {"owner", "editor"}} {
		c, err := f.svc.CallbackCaller(ctx, CallbackClaims{UserID: f.user.ID, Strategy: strategy, Methods: []string{"password"}})
		if err != nil {
			t.Fatal(err)
		}
		if c.SignInStrategy() != strategy || !slices.Equal(c.User.User.Roles, want) {
			t.Errorf("%s: strategy %q roles %v, want %v", strategy, c.SignInStrategy(), c.User.User.Roles, want)
		}
	}
	if c := (Caller{}); c.SignInStrategy() != "" {
		t.Error("an anonymous caller has no strategy")
	}
}
