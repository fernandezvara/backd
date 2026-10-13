package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
)

func consentUsers(t *testing.T) (*Users, *authtest.MemStore, *time.Time) {
	t.Helper()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)
	svc.Realm = "acme"
	svc.Settings.DefaultLocale, svc.Settings.Locales = "en", []string{"en", "es"}
	svc.Settings.Consents = map[string]*registry.Consent{
		"terms": {
			Name: "terms", Required: true, Current: "2",
			Versions: map[string]*registry.ConsentVersion{"1": {}, "2": {Reaccept: true}},
			Texts: map[string]map[string]registry.ConsentText{
				"1": {"en": {Body: "old", Hash: "h1"}},
				"2": {"en": {Body: "new", Hash: "h2en"}, "es": {Body: "nuevo", Hash: "h2es"}},
			},
		},
	}
	return svc, store, &clock
}

func mustCreate(t *testing.T, svc *Users, email string) User {
	t.Helper()
	u, err := svc.Create(context.Background(), email, ptr("dev-p4ssw0rd!"))
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestRecordConsent(t *testing.T) {
	ctx := context.Background()
	svc, store, clock := consentUsers(t)
	ada := mustCreate(t, svc, "ada@example.com")
	id := ada.ID

	rec, err := svc.RecordConsent(ctx, id, ConsentInput{Consent: "terms", Version: "2", Action: ConsentAccepted, Method: ConsentMethodAPI, Locale: "es-MX", IP: "203.0.113.9", UserAgent: "test/1"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Locale != "es" || rec.TextHash != "h2es" || rec.IP != "203.0.113.9" || rec.UserAgent != "test/1" || !rec.At.Equal(*clock) {
		t.Errorf("record = %+v", rec)
	}
	u, _ := store.UserByID(ctx, id)
	if u.Consents["terms"] != "2" {
		t.Errorf("summary = %v", u.Consents)
	}

	// The hash is of the language shown; a language without the version's text falls back to the default.
	svc.Settings.Locales = []string{"en", "es", "fr"}
	rec, err = svc.RecordConsent(ctx, id, ConsentInput{Consent: "terms", Version: "1", Action: ConsentAccepted, Method: ConsentMethodAPI, Locale: "fr"})
	if err != nil || rec.Locale != "en" || rec.TextHash != "h1" {
		t.Errorf("fallback = %+v %v", rec, err)
	}

	// Withdrawing records the version it ends and clears the summary.
	rec, err = svc.RecordConsent(ctx, id, ConsentInput{Consent: "terms", Action: ConsentWithdrawn, Method: ConsentMethodAPI})
	if err != nil || rec.Version != "1" || rec.TextHash != "" {
		t.Errorf("withdrawal = %+v %v", rec, err)
	}
	u, _ = store.UserByID(ctx, id)
	if _, ok := u.Consents["terms"]; ok {
		t.Errorf("summary after withdrawal = %v", u.Consents)
	}
	hist, _ := svc.ConsentHistory(ctx, id)
	if len(hist) != 3 || hist[0].Action != ConsentAccepted || hist[2].Action != ConsentWithdrawn {
		t.Errorf("history = %+v", hist)
	}

	if _, err := svc.RecordConsent(ctx, id, ConsentInput{Consent: "nope", Version: "1", Action: ConsentAccepted}); !errors.Is(err, ErrUnknownConsent) {
		t.Errorf("unknown consent: %v", err)
	}
	if _, err := svc.RecordConsent(ctx, id, ConsentInput{Consent: "terms", Version: "9", Action: ConsentAccepted}); !errors.Is(err, ErrUnknownConsentVersion) {
		t.Errorf("unknown version: %v", err)
	}
}

func TestConsentProofIsConfigurable(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := consentUsers(t)
	svc.Settings.Compliance = registry.ComplianceSettings{ConsentProof: []string{registry.ProofIP}}
	ada := mustCreate(t, svc, "ada@example.com")
	rec, err := svc.RecordConsent(ctx, ada.ID, ConsentInput{Consent: "terms", Version: "2", Action: ConsentAccepted, Method: ConsentMethodAPI, IP: "203.0.113.9", UserAgent: "test/1"})
	if err != nil || rec.IP != "203.0.113.9" || rec.UserAgent != "" {
		t.Errorf("record = %+v %v", rec, err)
	}
	svc.Settings.Compliance = registry.ComplianceSettings{ConsentProof: []string{}}
	rec, _ = svc.RecordConsent(ctx, ada.ID, ConsentInput{Consent: "terms", Version: "2", Action: ConsentAccepted, Method: ConsentMethodAPI, IP: "203.0.113.9", UserAgent: "test/1"})
	if rec.IP != "" || rec.UserAgent != "" {
		t.Errorf("no proof: %+v", rec)
	}
}

// A magic link request carries consents for the account a new address gets; without
// them the new user is asked at their first sign-in, like anyone who never accepted.
func TestMagicLinkSignupConsents(t *testing.T) {
	ctx := context.Background()
	f := newLinkFixture(t)
	f.svc.Settings.Signup = registry.SignupOpen
	f.svc.Settings.MagicLink.Signup = true
	f.svc.Settings.Consents = map[string]*registry.Consent{
		"terms": {
			Name: "terms", Required: true, Current: "1",
			Versions: map[string]*registry.ConsentVersion{"1": {}},
			Texts:    map[string]map[string]registry.ConsentText{"1": {"en": {Body: "terms", Hash: "h1"}}},
		},
	}
	verifier, challenge, _ := NewPKCE()

	// A request naming a consent that doesn't exist is refused before anything is sent.
	if _, err := f.svc.RequestMagicLink(ctx, MagicLinkInput{Email: "new@example.com", CodeChallenge: challenge, Consents: map[string]string{"nope": "1"}}); err == nil {
		t.Error("an unknown consent was accepted")
	}

	// With the consents: the account has them, and the link ends in a session.
	id, err := f.svc.RequestMagicLink(ctx, MagicLinkInput{Email: "new@example.com", CodeChallenge: challenge, IP: "203.0.113.5", Consents: map[string]string{"terms": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	var job Job
	for _, j := range f.jobs(t) {
		if j.Email != nil && j.Email.ChallengeID == HashToken(id) {
			job = j
		}
	}
	token, _, _, err := f.svc.MakeMagicLinkSecrets(ctx, job.Email.ChallengeID)
	if err != nil {
		t.Fatal(err)
	}
	login, _, err := f.svc.UseMagicLink(ctx, token, "203.0.113.5")
	if err != nil {
		t.Fatal(err)
	}
	red, err := f.svc.RedeemLoginCode(ctx, login, verifier)
	if err != nil || red.Token == "" || !red.NewUser || red.Challenge != nil {
		t.Fatalf("redeem: %+v %v", red, err)
	}
	hist, _ := f.svc.ConsentHistory(ctx, red.Principal.User.ID)
	if len(hist) != 1 || hist[0].Method != ConsentMethodSignup || hist[0].IP != "203.0.113.5" {
		t.Errorf("history = %+v", hist)
	}

	// Without them: a challenge to accept.
	_, token2, _ := f.ask(t, "other@example.com", challenge)
	login2, _, _ := f.svc.UseMagicLink(ctx, token2, "")
	red, err = f.svc.RedeemLoginCode(ctx, login2, verifier)
	if err != nil || red.Challenge == nil || red.Challenge.Kind != ChallengeConsent || len(red.Challenge.Consents) != 1 {
		t.Fatalf("redeem without consents: %+v %v", red, err)
	}
}

func TestNotifyConsentUpdated(t *testing.T) {
	ctx := context.Background()
	svc, store, _ := consentUsers(t)
	svc.Settings.Email = enrollEmail()
	svc.Settings.Consents["terms"].Required = true
	mk := func(email string, held map[string]string, mutate func(*User)) User {
		u := mustCreate(t, svc, email)
		for name, v := range held {
			if err := store.SetUserConsent(ctx, u.ID, name, v); err != nil {
				t.Fatal(err)
			}
		}
		if mutate != nil {
			mutate(&u)
		}
		return u
	}
	mk("current@example.com", map[string]string{"terms": "2"}, nil)
	mk("older@example.com", map[string]string{"terms": "1"}, nil)
	mk("never@example.com", nil, nil) // a required consent they never accepted
	off := mk("off@example.com", map[string]string{"terms": "1"}, nil)
	yes := true
	if err := store.UpdateUser(ctx, off.ID, UserUpdate{Disabled: &yes}, time.Now()); err != nil {
		t.Fatal(err)
	}

	res, err := svc.NotifyConsentUpdated(ctx, "terms")
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != "2" || res.Matched != 2 || res.Queued != 2 || res.Limited != 0 {
		t.Errorf("terms run = %+v", res)
	}
	n := 0
	jobs, _, _ := svc.Jobs(ctx, JobFilter{Limit: 100})
	for _, j := range jobs {
		if j.Email != nil && j.Email.Kind == "consent-updated" && j.Email.Event == "terms" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("consent-updated jobs = %d", n)
	}
	// An optional consent only reaches people who hold an older version of it.
	res, err = svc.NotifyConsentUpdated(ctx, "nope")
	if !errors.Is(err, ErrUnknownConsent) {
		t.Errorf("unknown consent: %+v %v", res, err)
	}
}

func TestAdminRecordsAConsentWithEvidence(t *testing.T) {
	ctx := context.Background()
	svc, store, _ := consentUsers(t)
	ada := mustCreate(t, svc, "ada@example.com")

	if _, err := svc.RecordConsentFor(ctx, ada.ID, ConsentInput{Consent: "terms", Version: "2", Action: ConsentAccepted, Evidence: "  "}); err == nil {
		t.Error("a record without evidence was accepted")
	}
	rec, err := svc.RecordConsentFor(ctx, ada.ID, ConsentInput{Consent: "terms", Version: "2", Action: ConsentAccepted, Method: ConsentMethodAPI, Evidence: "paper form 17"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Method != ConsentMethodAdmin || rec.Evidence != "paper form 17" {
		t.Errorf("record = %+v", rec)
	}
	u, _ := store.UserByID(ctx, ada.ID)
	if u.Consents["terms"] != "2" {
		t.Errorf("summary = %v", u.Consents)
	}
	trail, _, err := svc.AuditTrail(ctx, AuditFilter{Action: AuditConsentRecorded, Limit: 10})
	if err != nil || len(trail) != 1 || trail[0].Details["evidence"] != "paper form 17" {
		t.Errorf("audit = %+v %v", trail, err)
	}
	if _, err := svc.RecordConsentFor(ctx, "nobody", ConsentInput{Consent: "terms", Version: "2", Action: ConsentAccepted, Evidence: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
}

func TestConsentRecordsAtErasure(t *testing.T) {
	ctx := context.Background()
	svc, store, clock := consentUsers(t)
	svc.Cipher, _ = NewSecretCipher([]byte("01234567890123456789012345678901"))
	svc.Settings.Compliance = registry.ComplianceSettings{ConsentRetention: 90 * 24 * time.Hour, ConsentRetentionSet: true}
	ada := mustCreate(t, svc, "Ada@Example.com")
	other := mustCreate(t, svc, "bob@example.com")
	for _, id := range []string{ada.ID, other.ID} {
		if _, err := svc.RecordConsent(ctx, id, ConsentInput{Consent: "terms", Version: "2", Action: ConsentAccepted, Method: ConsentMethodSignup, IP: "203.0.113.9", UserAgent: "Agent/1"}); err != nil {
			t.Fatal(err)
		}
	}

	n, err := svc.ReduceConsentRecords(ctx, ada.ID, "ada@example.com")
	if err != nil || n != 1 {
		t.Fatalf("reduce = %d %v", n, err)
	}
	// Nothing is left under the id, and Bob's records are untouched.
	if recs, _ := svc.ConsentHistory(ctx, ada.ID); len(recs) != 0 {
		t.Errorf("records under the id: %+v", recs)
	}
	if recs, _ := svc.ConsentHistory(ctx, other.ID); len(recs) != 1 || recs[0].IP == "" {
		t.Errorf("another user's records changed: %+v", recs)
	}
	// They are under the pseudonym of the address (whatever its case), without the proof's IP and agent.
	p1, err := svc.ConsentPseudonym(ctx, "ADA@example.com ")
	if err != nil || !strings.HasPrefix(p1, "erased:") || len(p1) != len("erased:")+64 {
		t.Fatalf("pseudonym = %q %v", p1, err)
	}
	if strings.Contains(p1, "@") || strings.Contains(p1, "example.com") { // "ada" can be hex
		t.Error("the pseudonym holds the address")
	}
	recs, _ := svc.ConsentHistory(ctx, p1)
	if len(recs) != 1 || recs[0].IP != "" || recs[0].UserAgent != "" || recs[0].Consent != "terms" || recs[0].Version != "2" ||
		recs[0].TextHash == "" || recs[0].Method != ConsentMethodSignup || recs[0].Action != ConsentAccepted {
		t.Fatalf("reduced record = %+v", recs)
	}
	if want := clock.Add(90 * 24 * time.Hour); !recs[0].DeleteAt.Equal(want) {
		t.Errorf("deletes at %s, want %s", recs[0].DeleteAt, want)
	}
	// Repeating changes nothing, and another address has another pseudonym.
	if n, err := svc.ReduceConsentRecords(ctx, ada.ID, ""); err != nil || n != 0 {
		t.Errorf("repeat = %d %v", n, err)
	}
	if p2, _ := svc.ConsentPseudonym(ctx, "bob@example.com"); p2 == p1 {
		t.Error("two addresses share a pseudonym")
	}

	// After the retention nothing is left.
	if purged := store.PurgeConsentRecords(clock.Add(89 * 24 * time.Hour)); purged != 0 {
		t.Errorf("purged early: %d", purged)
	}
	if purged := store.PurgeConsentRecords(clock.Add(91 * 24 * time.Hour)); purged != 1 {
		t.Errorf("purged = %d", purged)
	}
	if recs, _ := svc.ConsentHistory(ctx, p1); len(recs) != 0 {
		t.Errorf("records after the retention: %+v", recs)
	}
}

func TestConsentRecordsAreDeletedAtErasureWithRetentionZero(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := consentUsers(t)
	svc.Settings.Compliance = registry.ComplianceSettings{ConsentRetentionSet: true} // 0
	ada := mustCreate(t, svc, "ada@example.com")
	if _, err := svc.RecordConsent(ctx, ada.ID, ConsentInput{Consent: "terms", Version: "2", Action: ConsentAccepted, Method: ConsentMethodAPI}); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.ReduceConsentRecords(ctx, ada.ID, "ada@example.com"); err != nil || n != 1 {
		t.Fatalf("reduce = %d %v", n, err)
	}
	if recs, _ := svc.ConsentHistory(ctx, ada.ID); len(recs) != 0 {
		t.Errorf("records = %+v", recs)
	}
	// Without a secrets key the id stays as the pseudonym instead of failing the erase.
	svc.Settings.Compliance = registry.ComplianceSettings{}
	bob := mustCreate(t, svc, "bob@example.com")
	if _, err := svc.RecordConsent(ctx, bob.ID, ConsentInput{Consent: "terms", Version: "2", Action: ConsentAccepted, Method: ConsentMethodAPI, IP: "203.0.113.9"}); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.ReduceConsentRecords(ctx, bob.ID, "bob@example.com"); err != nil || n != 1 {
		t.Fatalf("keyless = %d %v", n, err)
	}
	if recs, _ := svc.ConsentHistory(ctx, bob.ID); len(recs) != 1 || recs[0].IP != "" {
		t.Errorf("keyless records = %+v", recs)
	}
}

// A realm that doesn't let the sign-up in says so before it asks for consents.
func TestSignupModeIsAnsweredBeforeConsents(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := consentUsers(t)
	req := SignupRequest{Email: "ada@example.com", Password: "dev-p4ssw0rd!"}

	svc.Settings.Signup = registry.SignupClosed
	if _, err := svc.SignUp(ctx, req); !errors.Is(err, ErrSignupClosed) {
		t.Errorf("closed realm: %v", err)
	}
	svc.Settings.Signup = registry.SignupInvite
	if _, err := svc.SignUp(ctx, req); !errors.Is(err, ErrInvitationRequired) {
		t.Errorf("invite realm without an invitation: %v", err)
	}
	var need *ConsentRequiredError
	svc.Settings.Signup = registry.SignupOpen
	if _, err := svc.SignUp(ctx, req); !errors.As(err, &need) {
		t.Errorf("open realm without consents: %v", err)
	}
}
