package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	code, err := svc.IssueLoginCode(ctx, res.User.ID, challenge, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	red, err := svc.RedeemLoginCode(ctx, code, verifier)
	if err != nil || red.Principal.User.ID != res.User.ID || red.Token == "" || red.NewUser || red.Profile != nil {
		t.Fatalf("redeem: %+v, %v", red, err)
	}
	if _, err := svc.RedeemLoginCode(ctx, code, verifier); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("a code used twice: %v", err)
	}
	// A wrong verifier burns the code: a thief who has the code can't try verifiers.
	code, _ = svc.IssueLoginCode(ctx, res.User.ID, challenge, false, nil)
	if _, err := svc.RedeemLoginCode(ctx, code, "not-the-verifier"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong verifier: %v", err)
	}
	if _, err := svc.RedeemLoginCode(ctx, code, verifier); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("the code survived a wrong verifier: %v", err)
	}
	// Expiry, and a user disabled in between.
	code, _ = svc.IssueLoginCode(ctx, res.User.ID, challenge, false, nil)
	*clock = clock.Add(2 * time.Minute)
	if _, err := svc.RedeemLoginCode(ctx, code, verifier); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("an expired code: %v", err)
	}
	code, _ = svc.IssueLoginCode(ctx, res.User.ID, challenge, false, nil)
	_ = svc.SetDisabled(ctx, "ada@example.com", true)
	if _, err := svc.RedeemLoginCode(ctx, code, verifier); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("a disabled user: %v", err)
	}
	if _, err := svc.RedeemLoginCode(ctx, "", verifier); !errors.Is(err, ErrInvalidCredentials) {
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

func TestLoginCodesHandTheProfileOverOnce(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := providerFixture(t)
	res, _ := svc.ResolveProviderLogin(ctx, googleLogin("ada@example.com"))
	verifier, challenge, _ := NewPKCE()
	profile := map[string]string{"name": "Ada Lovelace", "given_name": "Ada"}
	code, _ := svc.IssueLoginCode(ctx, res.User.ID, challenge, true, profile)
	red, err := svc.RedeemLoginCode(ctx, code, verifier)
	if err != nil || !red.NewUser || red.Profile["name"] != "Ada Lovelace" || red.Profile["given_name"] != "Ada" {
		t.Fatalf("redeem: %+v, %v", red, err)
	}
	// The profile only goes with a code that made the user.
	code, _ = svc.IssueLoginCode(ctx, res.User.ID, challenge, false, profile)
	if red, _ = svc.RedeemLoginCode(ctx, code, verifier); red.NewUser || red.Profile != nil {
		t.Errorf("a returning user's code carried a profile: %+v", red)
	}
}

func TestProfilesAreCleaned(t *testing.T) {
	got := CleanProfile(map[string]string{
		"name": "  Ada\x00 Lovelace\n ", "given_name": "", "picture": "https://x/" + strings.Repeat("a", 3000), "email": "not a profile field", "other": "x",
	})
	if got["name"] != "Ada Lovelace" || len(got) != 2 || len([]rune(got["picture"])) != 2000 {
		t.Errorf("profile = %v", got)
	}
	long := CleanProfile(map[string]string{"name": strings.Repeat("é", 500)})
	if len([]rune(long["name"])) != 200 {
		t.Errorf("name length %d", len([]rune(long["name"])))
	}
}

func TestOnSignupIsQueuedForEveryNewUser(t *testing.T) {
	ctx := context.Background()
	svc, store, _ := providerFixture(t)
	svc.Settings.Account.OnSignup = "app/profile"
	svc.Settings.Account.OnSignupTimeout = 20 * time.Second
	jobs := func() []Job {
		js, _, _ := svc.Jobs(ctx, JobFilter{Origin: OnSignupOrigin})
		return js
	}
	// A password sign-up, and a provider sign-up with a profile.
	pw, _, err := svc.Signup(ctx, "pw@example.com", "dev-p4ssw0rd!", "")
	if err != nil {
		t.Fatal(err)
	}
	login := googleLogin("g@example.com")
	login.Profile = map[string]string{"name": "Gee", "picture": "https://x/p.png"}
	g, err := svc.ResolveProviderLogin(ctx, login)
	if err != nil {
		t.Fatal(err)
	}
	got := jobs()
	if len(got) != 2 {
		t.Fatalf("jobs: %d", len(got))
	}
	byUser := map[string]OnSignupInput{}
	for _, j := range got {
		var in OnSignupInput
		if err := json.Unmarshal(j.Input, &in); err != nil {
			t.Fatal(err)
		}
		byUser[in.UserID] = in
		if j.Database != "app" || j.Function != "profile" || !j.ActsAsFunction || j.TimeoutMS != 20000 || j.Origin != OnSignupOrigin {
			t.Errorf("job: %+v", j)
		}
	}
	if in := byUser[pw.User.ID]; in.Provider != "password" || in.Profile != nil {
		t.Errorf("password sign-up: %+v", in)
	}
	if in := byUser[g.User.ID]; in.Provider != "google" || in.Profile["name"] != "Gee" || in.Profile["picture"] != "https://x/p.png" {
		t.Errorf("provider sign-up: %+v", in)
	}
	// Signing in again, linking, or an administrator creating a user queue nothing.
	_, _ = svc.ResolveProviderLogin(ctx, googleLogin("g@example.com"))
	if _, err := svc.Create(ctx, "made@example.com", nil); err != nil {
		t.Fatal(err)
	}
	if n := len(jobs()); n != 2 {
		t.Errorf("jobs after more activity: %d", n)
	}
	// The profile values are what logs must hide.
	if vals := ProfileValues(byUserInput(t, got, g.User.ID)); len(vals) != 2 {
		t.Errorf("ProfileValues = %v", vals)
	}
	// A realm without the hook queues nothing, and a failing queue never fails the sign-up.
	svc.Settings.Account.OnSignup = ""
	if _, _, err := svc.Signup(ctx, "other@example.com", "dev-p4ssw0rd!", ""); err != nil || len(jobs()) != 2 {
		t.Errorf("no hook: %v, %d jobs", err, len(jobs()))
	}
	_ = store
}

func byUserInput(t *testing.T, jobs []Job, userID string) json.RawMessage {
	t.Helper()
	for _, j := range jobs {
		var in OnSignupInput
		_ = json.Unmarshal(j.Input, &in)
		if in.UserID == userID {
			return j.Input
		}
	}
	t.Fatal("no job for the user")
	return nil
}

func TestPurgingAnUnverifiedAccountRevokesItsAppleToken(t *testing.T) {
	ctx := context.Background()
	svc, store, clock := providerFixture(t)
	svc.Cipher, _ = NewSecretCipher([]byte("01234567890123456789012345678901"))
	svc.Settings.Providers = map[string]*registry.Provider{"apple": {Name: "apple", RevokeOnDelete: true}}
	svc.Settings.Account.PurgeUnverifiedAfter = time.Hour
	p, _, err := svc.Signup(ctx, "old@example.com", "dev-p4ssw0rd!", "")
	if err != nil {
		t.Fatal(err)
	}
	now := *clock
	if err := store.PutIdentity(ctx, Identity{ID: "a1", UserID: p.User.ID, Provider: "apple", Subject: "a-sub", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SaveAppleToken(ctx, p.User.ID, "com.acme.web", "refresh-old"); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(2 * time.Hour)
	if n, err := svc.PurgeUnverified(ctx); err != nil || n != 1 {
		t.Fatalf("purge = %d, %v", n, err)
	}
	jobs, _, _ := svc.Jobs(ctx, JobFilter{Origin: RevokeOrigin})
	if len(jobs) != 1 || jobs[0].Revoke.UserID != p.User.ID || jobs[0].Revoke.ClientID != "com.acme.web" {
		t.Fatalf("revoke jobs: %+v", jobs)
	}
	if plain, err := svc.OpenToken(jobs[0].Revoke.Token); err != nil || plain != "refresh-old" {
		t.Errorf("the job's token: %q, %v", plain, err)
	}
}
