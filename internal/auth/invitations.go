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

	"github.com/fernandezvara/backd/internal/email"

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
func (s *Users) signupWithInvitation(ctx context.Context, email, password, token, locale string) (User, error) {
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
	// An invitation bound to an address was sent to it: its owner vouches for it.
	u, err := s.create(ctx, email, &password, locale, inv.Email != "")
	if err != nil {
		restore()
		return User{}, err
	}
	return u, nil
}

// ErrInvitationNeedsAddress means an invitation to be sent by email has no
// address to send it to.
var ErrInvitationNeedsAddress = errors.New("an invitation sent by email needs an email address")

// SendInvitation is CreateInvitation for an invitation that backd emails to
// the address it is bound to: the person gets a link to a page where they
// choose a password. Nobody holds the invitation's own token, so no token
// is returned; the link's token is made by a worker when it sends the
// message, and only its hash is stored. A reached email limit removes the
// invitation again and is an *EmailLimitedError.
func (s *Users) SendInvitation(ctx context.Context, address string, ttl time.Duration, createdBy, redirectTo, locale, ip, requestID string) (Invitation, error) {
	if s.Settings.Email == nil {
		return Invitation{}, ErrEmailNotConfigured
	}
	if address == "" {
		return Invitation{}, ErrInvitationNeedsAddress
	}
	inv, _, err := s.CreateInvitation(ctx, address, ttl, createdBy)
	if err != nil {
		return Invitation{}, err
	}
	_, err = s.QueueEmail(ctx, EmailRequest{
		Kind: email.Invitation, InvitationID: inv.ID, Address: inv.Email, Locale: s.Settings.BestLocale(locale),
		RedirectTo: redirectTo, ClientIP: ip, RequestID: requestID,
	})
	if err != nil {
		_ = s.Store.DeleteInvitation(context.WithoutCancel(ctx), inv.ID)
		return Invitation{}, err
	}
	s.Audit(ctx, AuditInviteSent, "invitation:"+inv.ID, map[string]any{"expires_at": inv.ExpiresAt.Format(time.RFC3339)})
	return inv, nil
}

// AcceptInvitation uses the link of an emailed invitation: it creates the
// account with the chosen password and the invited address, already
// verified (only its owner received the link). It starts no session. The
// password is checked first, so a refused one leaves the link usable. Every
// way the link can be wrong, or the address being registered meanwhile, is
// ErrInvalidToken.
func (s *Users) AcceptInvitation(ctx context.Context, token, password, locale string) (EmailToken, error) {
	if _, err := CheckPassword(password, s.Settings.PasswordMinLength); err != nil {
		return EmailToken{}, err
	}
	t, err := s.RedeemEmailToken(ctx, token, string(email.TokenPurpose(email.Invitation)))
	if err != nil {
		return EmailToken{}, err
	}
	inv, err := s.Store.ClaimInvitationByID(ctx, t.InvitationID, s.now())
	if errors.Is(err, ErrNotFound) {
		return EmailToken{}, ErrInvalidToken // revoked or expired
	}
	if err != nil {
		return EmailToken{}, err
	}
	restore := func() { _ = s.Store.CreateInvitation(context.WithoutCancel(ctx), inv) }
	if inv.Email == "" || inv.Email != t.Address {
		restore()
		return EmailToken{}, ErrInvalidToken
	}
	u, err := s.create(ctx, inv.Email, &password, locale, true)
	if errors.Is(err, ErrEmailTaken) {
		return EmailToken{}, ErrInvalidToken
	}
	if err != nil {
		restore()
		return EmailToken{}, err
	}
	s.AuditAs(ctx, userTarget(u.ID), AuditUserSignup, userTarget(u.ID), map[string]any{"invited": true, "emailed": true, "roles": u.Roles})
	s.queueOnSignup(ctx, u, ProviderPassword, nil)
	return t, nil
}

// InvitationByID returns an unexpired invitation by id.
func (s *Users) InvitationByID(ctx context.Context, id string) (Invitation, bool, error) {
	all, err := s.Invitations(ctx)
	if err != nil {
		return Invitation{}, false, err
	}
	for _, inv := range all {
		if inv.ID == id {
			return inv, true, nil
		}
	}
	return Invitation{}, false, nil
}
