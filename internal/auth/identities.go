package auth

import (
	"context"
	"errors"
	"sort"
)

// Identities returns the user's sign-in methods, oldest first.
func (s *Users) Identities(ctx context.Context, userID string) ([]Identity, error) {
	ids, err := s.Store.ListIdentities(ctx, userID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(ids, func(i, j int) bool { return ids[i].CreatedAt.Before(ids[j].CreatedAt) })
	return ids, nil
}

// Unlink removes the user's sign-in method of a provider, never the last one:
// ErrLastSignInMethod. ErrNotFound when the user has none of that provider.
// by says who did it for the audit ("self" or "admin").
func (s *Users) Unlink(ctx context.Context, userID, provider, by string) error {
	ids, err := s.Store.ListIdentities(ctx, userID)
	if err != nil {
		return err
	}
	found := false
	for _, i := range ids {
		found = found || i.Provider == provider
	}
	// The email method is the mailbox itself (magic links), which the user can't give up one by
	// one: it is there while the realm has magic links and the address is verified.
	if !found || provider == ProviderEmail {
		return ErrNotFound
	}
	u, err := s.Store.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	if s.signInMethodsLeft(u, ids, provider) == 0 {
		return ErrLastSignInMethod
	}
	if provider == ProviderApple {
		if err := s.queueAppleRevocation(ctx, userID); err != nil {
			return err
		}
	}
	if err := s.Store.DeleteIdentity(ctx, userID, provider); err != nil {
		return err
	}
	s.Audit(ctx, AuditIdentityUnlinked, userTarget(userID), map[string]any{"provider": provider, "by": by})
	return nil
}

// magicLinkAvailable says the user can sign in with a link: the realm has them and the address
// is one only they could have verified.
func (s *Users) magicLinkAvailable(u User) bool {
	return s.Settings.MagicLink.Enabled && s.Settings.Email != nil && u.EmailVerified
}

// signInMethodsLeft counts the ways the user could still sign in without the identity of
// without ("" for none removed): their password, each provider, and, when the realm has magic
// links and the address is verified, the mailbox. The `email` identity is not a method of its own: it
// only records that the mailbox was used.
func (s *Users) signInMethodsLeft(u User, ids []Identity, without string) int {
	n := 0
	for _, i := range ids {
		if i.Provider != without && i.Provider != ProviderEmail {
			n++
		}
	}
	if s.magicLinkAvailable(u) {
		n++
	}
	return n
}

// SignInMethods lists the ways the user can sign in now: "password", "magic_link" and the
// provider names, in that order.
func (s *Users) SignInMethods(ctx context.Context, u User) ([]string, error) {
	ids, err := s.Identities(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	out := []string{}
	hasPassword := false
	var providers []string
	for _, i := range ids {
		switch i.Provider {
		case ProviderPassword:
			hasPassword = true
		case ProviderEmail:
		default:
			providers = append(providers, i.Provider)
		}
	}
	if hasPassword {
		out = append(out, ProviderPassword)
	}
	if s.magicLinkAvailable(u) {
		out = append(out, "magic_link")
	}
	return append(out, providers...), nil
}

// RemovePassword removes the user's password, so that they sign in with a link or a provider
// only. It refuses when nothing else would be left (ErrLastSignInMethod), and ends every
// other session: whoever knew the password may have a session. The caller has checked a fresh
// proof of identity.
func (s *Users) RemovePassword(ctx context.Context, p Principal) error {
	ids, err := s.Store.ListIdentities(ctx, p.User.ID)
	if err != nil {
		return err
	}
	has := false
	for _, i := range ids {
		has = has || i.Provider == ProviderPassword
	}
	if !has {
		return ErrNotFound
	}
	u, err := s.Store.UserByID(ctx, p.User.ID)
	if err != nil {
		return err
	}
	if s.signInMethodsLeft(u, ids, ProviderPassword) == 0 {
		return ErrLastSignInMethod
	}
	if err := s.Store.DeleteIdentity(ctx, u.ID, ProviderPassword); err != nil {
		return err
	}
	s.Audit(ctx, AuditPasswordRemoved, userTarget(u.ID), nil)
	if err := s.endOtherSessions(ctx, u.ID, p.Session.ID, "password_removed"); err != nil {
		return err
	}
	s.securityNotice(ctx, u, "password-removed", 0)
	return nil
}

// touchIdentity records a successful sign-in with the user's identity of a
// provider. A failure is not the sign-in's: it is only logged.
func (s *Users) touchIdentity(ctx context.Context, userID, provider string) {
	id, err := s.Store.IdentityOf(ctx, userID, provider)
	if err == nil {
		now := s.now()
		err = s.Store.UpdateIdentity(ctx, id.ID, IdentityUpdate{LastUsedAt: &now}, now)
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		if s.Log != nil {
			s.Log.Warn("record the use of a sign-in method", "provider", provider, "error", err)
		}
	}
}
