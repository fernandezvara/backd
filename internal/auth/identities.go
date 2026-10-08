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
	switch {
	case !found:
		return ErrNotFound
	case len(ids) < 2:
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
