package auth

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/fernandezvara/backd/internal/registry"
)

// ConsentRequiredError says which consents a person has to accept: at sign-up they
// were missing or outdated (400), or an open session is past the reaccept_by date of
// a version it hasn't accepted (403).
type ConsentRequiredError struct {
	Consents []registry.ConsentRef
	// Session is true when it is an authenticated request that is refused.
	Session bool
}

func (e *ConsentRequiredError) Error() string {
	return "required consents have to be accepted"
}

// finishSignIn ends a sign-in whose factors have been proved: with a session, or with a
// consent challenge when the user has required consents to accept first.
func (s *Users) finishSignIn(ctx context.Context, u User, methods []string, h handover) (Redeemed, error) {
	if todo := s.Settings.ConsentsToAccept(u.Consents); len(todo) > 0 {
		return s.issueConsentChallenge(ctx, u, methods, todo, h)
	}
	p, token, err := s.startSessionWith(ctx, u, methods, len(methods) > 1)
	if err != nil {
		return Redeemed{}, err
	}
	return Redeemed{Principal: p, Token: token, NewUser: h.newUser, Profile: h.profile}, nil
}

func (s *Users) issueConsentChallenge(ctx context.Context, u User, methods []string, todo []registry.ConsentRef, h handover) (Redeemed, error) {
	token, err := randomValue()
	if err != nil {
		return Redeemed{}, err
	}
	now := s.now()
	c := AuthChallenge{
		ID: HashToken(token), Kind: ChallengeConsent, UserID: u.ID, Factor: methods[0], Methods: methods,
		NewUser: h.newUser, CreatedAt: now, ExpiresAt: now.Add(ChallengeTTL),
	}
	if h.newUser && len(h.profile) > 0 {
		c.Profile = h.profile
	}
	if err := s.Store.PutChallenge(ctx, c); err != nil {
		return Redeemed{}, err
	}
	return Redeemed{Challenge: &IssuedChallenge{Token: token, Kind: ChallengeConsent, Consents: todo}}, nil
}

// ConsentContext is where a person gave consents from: for the proof.
type ConsentContext struct{ IP, UserAgent string }

// AcceptConsents trades a consent challenge and the consents accepted (name to version)
// for the session the sign-in was waiting for. Every consent the challenge named must be
// accepted at its current version; optional consents may come with them. A challenge
// that isn't satisfied stays valid, so the person can try again.
func (s *Users) AcceptConsents(ctx context.Context, challenge string, given map[string]string, from ConsentContext) (Verified, error) {
	if challenge == "" {
		return Verified{}, ErrInvalidChallenge
	}
	id := HashToken(challenge)
	c, err := s.Store.GetChallenge(ctx, id, s.now())
	if errors.Is(err, ErrNotFound) || (err == nil && c.Kind != ChallengeConsent) {
		return Verified{}, ErrInvalidChallenge
	}
	if err != nil {
		return Verified{}, err
	}
	u, err := s.Store.UserByID(ctx, c.UserID)
	if errors.Is(err, ErrNotFound) || (err == nil && (u.Disabled || !u.ErasedAt.IsZero())) {
		return Verified{}, ErrInvalidChallenge
	}
	if err != nil {
		return Verified{}, err
	}
	if err := s.refuseExpiredFactor(ctx, u, c.Factor); err != nil {
		return Verified{}, err
	}
	// What is still missing counts what the user holds plus what they accept now.
	if err := s.checkConsentsGiven(u.Consents, given); err != nil {
		return Verified{}, err
	}
	// One use: whoever deletes the challenge first gets the session.
	if _, err := s.Store.ClaimChallenge(ctx, id, s.now()); errors.Is(err, ErrNotFound) {
		return Verified{}, ErrInvalidChallenge
	} else if err != nil {
		return Verified{}, err
	}
	if err := s.recordGiven(ctx, u.ID, given, ConsentMethodPrompt, from, u.Locale); err != nil {
		return Verified{}, err
	}
	u.Consents = mergeConsents(u.Consents, given)
	p, token, err := s.startSessionWith(ctx, u, c.Methods, len(c.Methods) > 1)
	if err != nil {
		return Verified{}, err
	}
	return Verified{Redeemed: Redeemed{Principal: p, Token: token, NewUser: c.NewUser, Profile: c.Profile}, Factor: c.Factor}, nil
}

// AcceptConsentsOf records consents a signed-in user gives (method api): an app showing
// the prompt after a version changed, or a user opting in to an optional consent.
func (s *Users) AcceptConsentsOf(ctx context.Context, u User, given map[string]string, from ConsentContext) (User, error) {
	if len(given) == 0 {
		return u, &ConsentRequiredError{Consents: s.Settings.ConsentsToAccept(u.Consents)}
	}
	if err := s.checkConsentsGiven(u.Consents, given); err != nil {
		// A signed-in user may accept some now and the rest later: only unknown consents
		// and versions are errors here.
		var req *ConsentRequiredError
		if !errors.As(err, &req) {
			return u, err
		}
	}
	if err := s.recordGiven(ctx, u.ID, given, ConsentMethodAPI, from, u.Locale); err != nil {
		return u, err
	}
	u.Consents = mergeConsents(u.Consents, given)
	return u, nil
}

// ValidateConsents checks that the consents a request names exist with a text, without
// asking for the required ones: for requests that carry them for later (a magic link, a
// provider sign-in), where whether an account is made isn't known yet.
func (s *Users) ValidateConsents(given map[string]string) error {
	if len(given) == 0 {
		return nil
	}
	var bad []string
	for name, version := range given {
		c := s.Settings.Consents[name]
		switch {
		case c == nil:
			bad = append(bad, fmt.Sprintf("consents.%s: is not a consent of this realm", name))
		case len(c.Texts[version]) == 0:
			bad = append(bad, fmt.Sprintf("consents.%s: version %q has no text", name, version))
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return &InvalidConsentsError{Reasons: bad}
	}
	return nil
}

// checkConsentsGiven validates the consents a person gives: every name and version must
// exist (UnknownConsentError), and, given what they already hold, no required consent may
// be left missing or outdated (ConsentRequiredError with what is).
func (s *Users) checkConsentsGiven(held, given map[string]string) error {
	if err := s.ValidateConsents(given); err != nil {
		return err
	}
	if todo := s.Settings.ConsentsToAccept(mergeConsents(held, given)); len(todo) > 0 {
		return &ConsentRequiredError{Consents: todo}
	}
	return nil
}

// InvalidConsentsError is a request that names consents or versions the realm doesn't have.
type InvalidConsentsError struct{ Reasons []string }

func (e *InvalidConsentsError) Error() string { return "invalid consents" }

func mergeConsents(held, given map[string]string) map[string]string {
	out := make(map[string]string, len(held)+len(given))
	for k, v := range held {
		out[k] = v
	}
	for k, v := range given {
		out[k] = v
	}
	return out
}

// recordGiven records each consent in given as accepted, in name order. An older
// version than the one held is not a step back: it is recorded all the same, as the
// person's own act, and the summary holds what they said last.
func (s *Users) recordGiven(ctx context.Context, userID string, given map[string]string, method string, from ConsentContext, locale string) error {
	names := make([]string, 0, len(given))
	for n := range given {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		_, err := s.RecordConsent(ctx, userID, ConsentInput{
			Consent: n, Version: given[n], Action: ConsentAccepted, Method: method,
			Locale: locale, IP: from.IP, UserAgent: from.UserAgent,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// checkSignupConsents validates the consents of a sign-up before the account exists: the
// required ones must be there at their current version.
func (s *Users) checkSignupConsents(given map[string]string) error {
	if len(s.Settings.Consents) == 0 {
		if len(given) > 0 {
			return &InvalidConsentsError{Reasons: []string{"consents: this realm asks for none"}}
		}
		return nil
	}
	return s.checkConsentsGiven(nil, given)
}

// gateConsents refuses an authenticated request of a user who hasn't accepted a
// consent whose reaccept_by date has passed.
func (s *Users) gateConsents(u User) error {
	if len(s.Settings.Consents) == 0 {
		return nil
	}
	if late := s.Settings.ConsentsPastDeadline(u.Consents, s.now()); len(late) > 0 {
		return &ConsentRequiredError{Consents: late, Session: true}
	}
	return nil
}
