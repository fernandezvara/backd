package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
)

func providerFixture(t *testing.T) (*Users, *authtest.MemStore, *time.Time) {
	t.Helper()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	return newUsers(store, &clock), store, &clock
}

func googleLogin(email string) ProviderLogin {
	return ProviderLogin{Provider: "google", Subject: "g-" + email, Email: email, EmailVerified: true, TrustsEmail: true, Intent: IntentSignIn}
}

func TestProviderLoginMakesAndFindsUsers(t *testing.T) {
	ctx := context.Background()
	svc, store, _ := providerFixture(t)
	res, err := svc.ResolveProviderLogin(ctx, ProviderLogin{Provider: "google", Subject: "g-1", Email: " Ada@Example.com ", EmailVerified: true, TrustsEmail: true, Intent: IntentSignIn, Locales: []string{"es"}})
	if err != nil || !res.NewUser || res.User.Email != "ada@example.com" || !res.User.EmailVerified {
		t.Fatalf("new user: %+v, %v", res, err)
	}
	ids, _ := svc.Identities(ctx, res.User.ID)
	if len(ids) != 1 || ids[0].Provider != "google" || ids[0].Email != "ada@example.com" || !ids[0].EmailVerified {
		t.Errorf("identities: %+v", ids)
	}
	// The same person again: the same user, no new account, and the identity follows the provider's address.
	again := ProviderLogin{Provider: "google", Subject: "g-1", Email: "new-address@example.com", EmailVerified: false, TrustsEmail: true, Intent: IntentSignIn}
	res2, err := svc.ResolveProviderLogin(ctx, again)
	if err != nil || res2.NewUser || res2.User.ID != res.User.ID || res2.User.Email != "ada@example.com" {
		t.Fatalf("known identity: %+v, %v", res2, err)
	}
	id, _ := store.IdentityOf(ctx, res.User.ID, "google")
	if id.Email != "new-address@example.com" || id.EmailVerified || id.LastUsedAt.IsZero() {
		t.Errorf("identity after a sign-in: %+v", id)
	}
	// A Microsoft or generic provider's address is stored, but the user is not verified by it.
	res3, err := svc.ResolveProviderLogin(ctx, ProviderLogin{Provider: "microsoft", Subject: "t:o", Email: "bob@corp.example", EmailVerified: true, TrustsEmail: false, Intent: IntentSignIn})
	if err != nil || !res3.NewUser || res3.User.EmailVerified {
		t.Errorf("untrusted provider: %+v, %v", res3, err)
	}
	// Without an address there is no account to make.
	if _, err := svc.ResolveProviderLogin(ctx, ProviderLogin{Provider: "google", Subject: "g-2", Intent: IntentSignIn}); !errors.Is(err, ErrEmailRequired) {
		t.Errorf("no address: %v", err)
	}
}

func TestProviderLoginKnownIdentityRules(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := providerFixture(t)
	if _, err := svc.ResolveProviderLogin(ctx, googleLogin("ada@example.com")); err != nil {
		t.Fatal(err)
	}
	login := googleLogin("ada@example.com")
	if err := svc.SetDisabled(ctx, "ada@example.com", true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveProviderLogin(ctx, login); !errors.Is(err, ErrSignInRefused) {
		t.Errorf("disabled: %v", err)
	}
	if err := svc.SetDisabled(ctx, "ada@example.com", false); err != nil {
		t.Fatal(err)
	}
	// A login network the address is not in is refused like a disabled user.
	nets, _ := registry.ParseNetworks([]string{"10.0.0.0/8"})
	if _, err := svc.SetNetworks(ctx, "ada@example.com", nil, nets); err != nil {
		t.Fatal(err)
	}
	login.IP = "192.0.2.1"
	if _, err := svc.ResolveProviderLogin(ctx, login); !errors.Is(err, ErrSignInRefused) {
		t.Errorf("outside the login networks: %v", err)
	}
	login.IP = "10.1.2.3"
	if _, err := svc.ResolveProviderLogin(ctx, login); err != nil {
		t.Errorf("inside the login networks: %v", err)
	}
	// Verified addresses can be required.
	svc.Settings.Account.RequireVerifiedEmail = true
	if _, err := svc.ResolveProviderLogin(ctx, login); err != nil {
		t.Errorf("verified user with require_verified_email: %v", err)
	}
	unverified, err := svc.ResolveProviderLogin(ctx, ProviderLogin{Provider: "microsoft", Subject: "t:o", Email: "bob@corp.example", Intent: IntentSignIn})
	if !errors.Is(err, ErrEmailNotVerified) || !unverified.NewUser {
		t.Errorf("unverified new user: %+v, %v", unverified, err)
	}
	if _, err := svc.ResolveProviderLogin(ctx, ProviderLogin{Provider: "microsoft", Subject: "t:o", Email: "bob@corp.example", Intent: IntentSignIn}); !errors.Is(err, ErrEmailNotVerified) {
		t.Errorf("unverified user signing in again: %v", err)
	}
}

func TestProviderLoginLinkingByAddress(t *testing.T) {
	ctx := context.Background()
	svc, store, _ := providerFixture(t)
	// A user with a verified address and a password.
	u, err := svc.Create(ctx, "ada@example.com", ptr("dev-p4ssw0rd!"))
	if err != nil {
		t.Fatal(err)
	}
	verified := true
	if err := store.UpdateUser(ctx, u.ID, UserUpdate{EmailVerified: &verified}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// A trusted provider that asserts the verified address links to it.
	res, err := svc.ResolveProviderLogin(ctx, googleLogin("ada@example.com"))
	if err != nil || res.NewUser || !res.Linked || res.User.ID != u.ID {
		t.Fatalf("auto-link: %+v, %v", res, err)
	}
	ids, _ := svc.Identities(ctx, u.ID)
	if len(ids) != 2 {
		t.Errorf("identities: %+v", ids)
	}
	recs, _, _ := svc.AuditTrail(ctx, AuditFilter{Action: AuditIdentityLinked})
	if len(recs) != 1 {
		t.Errorf("audit: %+v", recs)
	}

	// Each condition on its own stops it.
	other, _ := svc.Create(ctx, "bob@example.com", ptr("dev-p4ssw0rd!"))
	if err := store.UpdateUser(ctx, other.ID, UserUpdate{EmailVerified: &verified}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*ProviderLogin){
		"the provider is not trusted":       func(l *ProviderLogin) { l.TrustsEmail = false },
		"the provider doesn't say verified": func(l *ProviderLogin) { l.EmailVerified = false },
	} {
		l := googleLogin("bob@example.com")
		mod(&l)
		if _, err := svc.ResolveProviderLogin(ctx, l); !errors.Is(err, ErrAccountExists) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The pre-hijacking case: an unverified password account made with the victim's address
	// never absorbs the victim's Google identity.
	if _, _, err := svc.Signup(ctx, "victim@example.com", "dev-p4ssw0rd!", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveProviderLogin(ctx, googleLogin("victim@example.com")); !errors.Is(err, ErrAccountExists) {
		t.Errorf("an unverified account: %v", err)
	}
	if _, err := store.Identity(ctx, "google", "g-victim@example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an identity was linked to the unverified account: %v", err)
	}
	// Linking is only automatic the first time; none of this created accounts.
	if us, _ := svc.List(ctx); len(us) != 3 {
		t.Errorf("users: %d", len(us))
	}
}

func TestProviderLoginExplicitLinking(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := providerFixture(t)
	ada, _ := svc.Create(ctx, "ada@example.com", ptr("dev-p4ssw0rd!"))
	bob, _ := svc.Create(ctx, "bob@example.com", ptr("dev-p4ssw0rd!"))
	link := func(user User, subject string) (ProviderResult, error) {
		return svc.ResolveProviderLogin(ctx, ProviderLogin{Provider: "google", Subject: subject, Email: "someone@gmail.example", EmailVerified: true, TrustsEmail: true, Intent: IntentLink, LinkUserID: user.ID})
	}
	res, err := link(ada, "g-ada")
	if err != nil || !res.Linked || res.User.ID != ada.ID {
		t.Fatalf("link: %+v, %v", res, err)
	}
	// The address of the provider account is not the user's: the user's address is unchanged.
	if u, _ := svc.Store.UserByID(ctx, ada.ID); u.Email != "ada@example.com" {
		t.Errorf("the user's address changed: %q", u.Email)
	}
	// Linking again what is already theirs is harmless; someone else's, or a second account of the provider, is a conflict.
	if _, err := link(ada, "g-ada"); err != nil {
		t.Errorf("relinking the same account: %v", err)
	}
	if _, err := link(bob, "g-ada"); !errors.Is(err, ErrLinkConflict) {
		t.Errorf("an account of another user: %v", err)
	}
	if _, err := link(ada, "g-other"); !errors.Is(err, ErrLinkConflict) {
		t.Errorf("a second google account: %v", err)
	}
	// A disabled user can't be linked to.
	if err := svc.SetDisabled(ctx, "bob@example.com", true); err != nil {
		t.Fatal(err)
	}
	if _, err := link(bob, "g-bob"); !errors.Is(err, ErrSignInRefused) {
		t.Errorf("a disabled user: %v", err)
	}
}

func TestProviderSignUpModes(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := providerFixture(t)
	svc.Settings.Signup = registry.SignupClosed
	if _, err := svc.ResolveProviderLogin(ctx, googleLogin("ada@example.com")); !errors.Is(err, ErrSignupClosed) {
		t.Errorf("closed: %v", err)
	}
	svc.Settings.Signup = registry.SignupInvite
	if _, err := svc.ResolveProviderLogin(ctx, googleLogin("ada@example.com")); !errors.Is(err, ErrInvitationRequired) {
		t.Errorf("invite without an invitation: %v", err)
	}
	l := googleLogin("ada@example.com")
	l.Invitation = HashToken("bdi_nothing")
	if _, err := svc.ResolveProviderLogin(ctx, l); !errors.Is(err, ErrInvalidInvitation) {
		t.Errorf("an unknown invitation: %v", err)
	}
	// An invitation bound to another address is refused and not used up.
	inv, token, err := svc.CreateInvitation(ctx, "someone-else@example.com", time.Hour, "admin")
	if err != nil {
		t.Fatal(err)
	}
	l.Invitation = HashToken(token)
	if _, err := svc.ResolveProviderLogin(ctx, l); !errors.Is(err, ErrInvalidInvitation) {
		t.Errorf("an invitation for another address: %v", err)
	}
	if invs, _ := svc.Store.ListInvitations(ctx); len(invs) != 1 {
		t.Errorf("the invitation was lost: %+v", invs)
	}
	// An open one is used up by the sign-up it admits.
	_, token, _ = svc.CreateInvitation(ctx, "", time.Hour, "admin")
	l.Invitation = HashToken(token)
	res, err := svc.ResolveProviderLogin(ctx, l)
	if err != nil || !res.NewUser {
		t.Fatalf("with an invitation: %+v, %v", res, err)
	}
	if _, err := svc.ResolveProviderLogin(ctx, googleLogin("late@example.com")); !errors.Is(err, ErrInvitationRequired) {
		t.Errorf("the invitation worked twice: %v", err)
	}
	_ = inv
}

func TestLoginCodesWorkOnceWithTheirVerifier(t *testing.T) {
	ctx := context.Background()
	svc, _, clock := providerFixture(t)
	res, _ := svc.ResolveProviderLogin(ctx, googleLogin("ada@example.com"))
	verifier, challenge, err := NewPKCE()
	if err != nil || !ValidPKCEChallenge(challenge) || ValidPKCEChallenge("short") {
		t.Fatal(err)
	}
	code, err := svc.IssueLoginCode(ctx, res.User.ID, challenge)
	if err != nil {
		t.Fatal(err)
	}
	p, token, err := svc.RedeemLoginCode(ctx, code, verifier)
	if err != nil || p.User.ID != res.User.ID || token == "" {
		t.Fatalf("redeem: %+v, %v", p, err)
	}
	if _, _, err := svc.RedeemLoginCode(ctx, code, verifier); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("a code used twice: %v", err)
	}
	// A wrong verifier burns the code: a thief who has the code can't try verifiers.
	code, _ = svc.IssueLoginCode(ctx, res.User.ID, challenge)
	if _, _, err := svc.RedeemLoginCode(ctx, code, "not-the-verifier"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong verifier: %v", err)
	}
	if _, _, err := svc.RedeemLoginCode(ctx, code, verifier); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("the code survived a wrong verifier: %v", err)
	}
	// Expiry, and a user disabled in between.
	code, _ = svc.IssueLoginCode(ctx, res.User.ID, challenge)
	*clock = clock.Add(2 * time.Minute)
	if _, _, err := svc.RedeemLoginCode(ctx, code, verifier); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("an expired code: %v", err)
	}
	code, _ = svc.IssueLoginCode(ctx, res.User.ID, challenge)
	_ = svc.SetDisabled(ctx, "ada@example.com", true)
	if _, _, err := svc.RedeemLoginCode(ctx, code, verifier); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("a disabled user: %v", err)
	}
	if _, _, err := svc.RedeemLoginCode(ctx, "", verifier); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("empty code: %v", err)
	}
}

func TestOAuthStatesWorkOnce(t *testing.T) {
	ctx := context.Background()
	svc, _, clock := providerFixture(t)
	state, nonce, verifier, err := svc.BeginOAuth(ctx, OAuthState{Provider: "google", Intent: IntentSignIn, CodeChallenge: "c", RedirectTo: "https://app.example/x"})
	if err != nil || state == "" || nonce == "" || verifier == "" || state == nonce {
		t.Fatal(state, nonce, verifier, err)
	}
	st, err := svc.TakeOAuthState(ctx, state)
	if err != nil || st.Provider != "google" || st.Nonce != nonce || st.Verifier != verifier || st.RedirectTo != "https://app.example/x" {
		t.Fatalf("take: %+v, %v", st, err)
	}
	if _, err := svc.TakeOAuthState(ctx, state); !errors.Is(err, ErrNotFound) {
		t.Errorf("a state used twice: %v", err)
	}
	state, _, _, _ = svc.BeginOAuth(ctx, OAuthState{Provider: "google", Intent: IntentSignIn})
	*clock = clock.Add(11 * time.Minute)
	if _, err := svc.TakeOAuthState(ctx, state); !errors.Is(err, ErrNotFound) {
		t.Errorf("an expired state: %v", err)
	}
	if _, err := svc.TakeOAuthState(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("an empty state: %v", err)
	}
}
