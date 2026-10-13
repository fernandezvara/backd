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

func TestRecordConsent(t *testing.T) {
	ctx := context.Background()
	svc, store, clock := consentUsers(t)
	ada := signedIn(t, svc, "ada@example.com")
	id := ada.User.ID

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
	ada := signedIn(t, svc, "ada@example.com")
	rec, err := svc.RecordConsent(ctx, ada.User.ID, ConsentInput{Consent: "terms", Version: "2", Action: ConsentAccepted, Method: ConsentMethodAPI, IP: "203.0.113.9", UserAgent: "test/1"})
	if err != nil || rec.IP != "203.0.113.9" || rec.UserAgent != "" {
		t.Errorf("record = %+v %v", rec, err)
	}
	svc.Settings.Compliance = registry.ComplianceSettings{ConsentProof: []string{}}
	rec, _ = svc.RecordConsent(ctx, ada.User.ID, ConsentInput{Consent: "terms", Version: "2", Action: ConsentAccepted, Method: ConsentMethodAPI, IP: "203.0.113.9", UserAgent: "test/1"})
	if rec.IP != "" || rec.UserAgent != "" {
		t.Errorf("no proof: %+v", rec)
	}
}
