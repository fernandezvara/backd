package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/registry"
)

// InvitationPrefix starts every invitation token.
const InvitationPrefix = "bdi_"

// Invitation lifetimes.
const (
	DefaultInvitationTTL = 7 * 24 * time.Hour
	MaxInvitationTTL     = 90 * 24 * time.Hour
)

var (
	// ErrInvitationRequired means the realm only accepts sign-ups with an invitation.
	ErrInvitationRequired = errors.New("sign-up requires an invitation")
	// ErrInvalidInvitation covers unknown, used, expired and mismatched
	// invitations alike.
	ErrInvalidInvitation = errors.New("invalid or expired invitation")
)

// Invitation lets one person sign up in a realm with `signup: invite`.
// Only a hash of its token is stored.
type Invitation struct {
	ID        string
	TokenHash string
	Email     string // "" if anyone holding the token may use it
	CreatedBy string // "key:<name>"
	CreatedAt time.Time
	ExpiresAt time.Time
}

// CreateInvitation creates an invitation valid for ttl (0: the default),
// optionally bound to email. The token is returned only here.
func (s *Users) CreateInvitation(ctx context.Context, email string, ttl time.Duration, createdBy string) (Invitation, string, error) {
	if email != "" {
		e, err := registry.NormalizeEmail(email)
		if err != nil {
			return Invitation{}, "", err
		}
		email = e
	}
	switch {
	case ttl == 0:
		ttl = DefaultInvitationTTL
	case ttl < time.Minute || ttl > MaxInvitationTTL:
		return Invitation{}, "", fmt.Errorf("invitation lifetime must be between 1m and %s", MaxInvitationTTL)
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return Invitation{}, "", err
	}
	token := InvitationPrefix + base64.RawURLEncoding.EncodeToString(b)
	now := s.now()
	inv := Invitation{
		ID: xid.New().String(), TokenHash: HashToken(token), Email: email,
		CreatedBy: createdBy, CreatedAt: now, ExpiresAt: now.Add(ttl),
	}
	if err := s.Store.CreateInvitation(ctx, inv); err != nil {
		return Invitation{}, "", err
	}
	s.Audit(ctx, AuditInviteCreate, "invitation:"+inv.ID, map[string]any{"bound_to_email": email != "", "expires_at": inv.ExpiresAt.Format(time.RFC3339)})
	return inv, token, nil
}

// Invitations lists the realm's unexpired invitations, newest first.
func (s *Users) Invitations(ctx context.Context) ([]Invitation, error) {
	all, err := s.Store.ListInvitations(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := all[:0]
	for _, inv := range all {
		if now.Before(inv.ExpiresAt) {
			out = append(out, inv)
		}
	}
	return out, nil
}

// RevokeInvitation deletes an invitation; ErrNotFound if none has that id.
func (s *Users) RevokeInvitation(ctx context.Context, id string) error {
	if err := s.Store.DeleteInvitation(ctx, id); err != nil {
		return err
	}
	s.Audit(ctx, AuditInviteRevoke, "invitation:"+id, nil)
	return nil
}

// signupWithInvitation creates the user if the invitation is valid for
// email. The invitation is used up only if the user is created.
func (s *Users) signupWithInvitation(ctx context.Context, email, password, token string) (User, error) {
	if token == "" {
		return User{}, ErrInvitationRequired
	}
	// Cheap checks first, so a typo doesn't use up the invitation.
	normalized, err := registry.NormalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	if _, err := CheckPassword(password, s.Settings.PasswordMinLength); err != nil {
		return User{}, err
	}
	if !strings.HasPrefix(token, InvitationPrefix) {
		return User{}, ErrInvalidInvitation
	}
	inv, err := s.Store.ClaimInvitation(ctx, HashToken(token), s.now())
	if errors.Is(err, ErrNotFound) {
		return User{}, ErrInvalidInvitation
	}
	if err != nil {
		return User{}, err
	}
	restore := func() { _ = s.Store.CreateInvitation(context.WithoutCancel(ctx), inv) }
	if inv.Email != "" && inv.Email != normalized {
		restore()
		return User{}, ErrInvalidInvitation
	}
	u, err := s.create(ctx, email, &password)
	if err != nil {
		restore()
		return User{}, err
	}
	return u, nil
}
