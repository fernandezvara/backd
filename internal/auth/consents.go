package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/registry"
)

// Consent actions and the methods that give them (design/compliance.md §5.2).
const (
	ConsentAccepted  = "accepted"
	ConsentWithdrawn = "withdrawn"

	ConsentMethodSignup = "signup"
	ConsentMethodPrompt = "prompt"
	ConsentMethodAPI    = "api"
	ConsentMethodAdmin  = "admin"
)

// AuditConsentRecorded is the audit action of an administrator recording a consent for a user.
const AuditConsentRecorded = "user.consent_recorded" // details: consent, version, action, evidence

// ConsentRecord is one entry of a user's append-only consent history: the proof
// that a text was accepted, or the withdrawal of that acceptance.
type ConsentRecord struct {
	ID       string
	UserID   string
	Consent  string
	Version  string
	Locale   string
	TextHash string // SHA-256 of the text shown
	Action   string // ConsentAccepted or ConsentWithdrawn
	Method   string // ConsentMethodSignup, …
	Evidence string // for the admin method: the reference given
	At       time.Time
	// IP and UserAgent are stored only when the realm lists them in compliance.consent_proof.
	IP        string
	UserAgent string
}

// ConsentRef is a consent and version a record refers to.
type ConsentRef struct{ Consent, Version string }

var (
	// ErrUnknownConsent: the realm declares no consent of that name.
	ErrUnknownConsent = errors.New("unknown consent")
	// ErrUnknownConsentVersion: the version isn't one the realm has a text for.
	ErrUnknownConsentVersion = errors.New("unknown consent version")
	// ErrConsentRequiredForAccount: a required consent can't be withdrawn without
	// deleting the account.
	ErrConsentRequiredForAccount = errors.New("a required consent can't be withdrawn: delete the account instead")
)

// ConsentInput is a consent given or withdrawn.
type ConsentInput struct {
	Consent  string
	Version  string // accepted: the version; withdrawn: ignored
	Action   string
	Method   string
	Evidence string
	// Locale is the language of the text the person saw; "" uses the user's, then the realm's.
	Locale    string
	IP        string
	UserAgent string
}

// RecordConsent appends a record for the user and updates their summary. Accepting
// needs a version the realm has a text for in the person's language; the record keeps
// that text's hash, so the proof names the exact words.
func (s *Users) RecordConsent(ctx context.Context, userID string, in ConsentInput) (ConsentRecord, error) {
	c := s.Settings.Consents[in.Consent]
	if c == nil {
		return ConsentRecord{}, fmt.Errorf("%w: %q", ErrUnknownConsent, in.Consent)
	}
	locale := s.Settings.BestLocale(in.Locale)
	rec := ConsentRecord{
		ID: xid.New().String(), UserID: userID, Consent: in.Consent, Action: in.Action, Method: in.Method,
		Evidence: strings.TrimSpace(in.Evidence), At: s.now().UTC(), Locale: locale,
	}
	switch in.Action {
	case ConsentAccepted:
		if _, ok := c.Texts[in.Version]; !ok {
			return ConsentRecord{}, fmt.Errorf("%w: %s has no version %q", ErrUnknownConsentVersion, in.Consent, in.Version)
		}
		text, ok := c.Texts[in.Version][locale]
		if !ok {
			// A version with texts only in some languages: fall back to the realm's default.
			def, _ := s.Settings.Languages()
			if text, ok = c.Texts[in.Version][def]; ok {
				rec.Locale = def
			}
		}
		if !ok {
			return ConsentRecord{}, fmt.Errorf("%w: %s %q has no text", ErrUnknownConsentVersion, in.Consent, in.Version)
		}
		rec.Version, rec.TextHash = in.Version, text.Hash
	case ConsentWithdrawn:
		// A withdrawal names the version it ends, the one the user holds.
		u, err := s.Store.UserByID(ctx, userID)
		if err != nil {
			return ConsentRecord{}, err
		}
		rec.Version = u.Consents[in.Consent]
		if rec.Version == "" {
			rec.Version = in.Version
		}
	default:
		return ConsentRecord{}, fmt.Errorf("consent action must be %s or %s", ConsentAccepted, ConsentWithdrawn)
	}
	if s.Settings.Compliance.Proof(registry.ProofIP) {
		rec.IP = in.IP
	}
	if s.Settings.Compliance.Proof(registry.ProofUserAgent) {
		rec.UserAgent = truncate(in.UserAgent, 300)
	}
	if err := s.Store.AppendConsentRecord(ctx, rec); err != nil {
		return ConsentRecord{}, err
	}
	version := rec.Version
	if rec.Action == ConsentWithdrawn {
		version = ""
	}
	if err := s.Store.SetUserConsent(ctx, userID, rec.Consent, version); err != nil {
		return ConsentRecord{}, err
	}
	return rec, nil
}

// ConsentHistory returns the user's consent records, oldest first.
func (s *Users) ConsentHistory(ctx context.Context, userID string) ([]ConsentRecord, error) {
	return s.Store.ListConsentRecords(ctx, userID)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
