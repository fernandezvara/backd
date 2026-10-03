package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"

	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/registry"
)

var (
	// ErrEmailChangeDisabled means the realm doesn't let users change their
	// own address (account.allow_email_change).
	ErrEmailChangeDisabled = errors.New("this realm doesn't allow changing the email address")
	// ErrSameEmail means the new address is the one the user already has.
	ErrSameEmail = errors.New("that is already the address of this account")
)

// RequestEmailChange starts a change of the signed-in user's own address: it
// needs the current password, remembers the new address as pending and sends
// a confirmation link to it. Nothing changes until that link is used. An
// address someone else already has is answered like any other (the request
// is accepted and nothing is sent), so this can't be used to find out who is
// registered. A reached email limit is an *EmailLimitedError: the caller is
// signed in, so it may be told.
func (s *Users) RequestEmailChange(ctx context.Context, p Principal, password, newAddress, redirectTo, ip, requestID string) error {
	if s.Settings.Email == nil || !s.Settings.Account.AllowEmailChange {
		return ErrEmailChangeDisabled
	}
	if err := s.checkPassword(ctx, p.User, password); err != nil {
		return err
	}
	next, err := registry.NormalizeEmail(newAddress)
	if err != nil {
		return err
	}
	if next == p.User.Email {
		return ErrSameEmail
	}
	req := EmailRequest{Kind: email.ChangeEmail, Address: next, Locale: s.LocaleOf(p.User), RedirectTo: redirectTo, ClientIP: ip, RequestID: requestID, To: "pending"}
	if _, err := s.byEmail(ctx, next); err == nil {
		// Taken: the same work, minus the message.
		_, err := s.QueueEmail(ctx, req)
		return err
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	if err := s.Store.UpdateUser(ctx, p.User.ID, UserUpdate{PendingEmail: &next}, s.now()); err != nil {
		return err
	}
	// An earlier request that wasn't confirmed can't be anymore.
	if err := s.Store.InvalidateEmailTokens(ctx, p.User.ID, string(email.TokenPurpose(email.ChangeEmail)), s.now()); err != nil {
		return err
	}
	req.UserID = p.User.ID
	_, err = s.QueueEmail(ctx, req)
	return err
}

// ConfirmEmailChange uses the link sent to the new address: the address
// changes (and counts as verified: the owner just read that mailbox), every
// session of the user ends, and the old address is told, with a link to undo
// it. Every way the token can be wrong, or the address having been taken
// meanwhile, is ErrInvalidToken.
func (s *Users) ConfirmEmailChange(ctx context.Context, token string) (EmailToken, error) {
	t, err := s.RedeemEmailToken(ctx, token, string(email.TokenPurpose(email.ChangeEmail)))
	if err != nil {
		return EmailToken{}, err
	}
	u, err := s.activeUser(ctx, t.UserID)
	if err != nil {
		return EmailToken{}, err
	}
	if u.PendingEmail == "" || u.PendingEmail != t.Address {
		return EmailToken{}, ErrInvalidToken // superseded by a later request
	}
	if err := s.applyEmailChange(ctx, u, t.Address); errors.Is(err, ErrEmailTaken) {
		return EmailToken{}, ErrInvalidToken // someone registered it meanwhile
	} else if err != nil {
		return EmailToken{}, err
	}
	s.AuditAs(ctx, userTarget(u.ID), AuditEmailChanged, userTarget(u.ID), map[string]any{"by": "self"})
	s.queueEmailChanged(ctx, u, false)
	return t, nil
}

// RevertEmailChange uses the link sent to the old address: it restores that
// address, ends every session of the user and makes their password unusable,
// since whoever changed the address may know it, and sends a password reset
// link to the restored address. Every way the token can be wrong is
// ErrInvalidToken.
func (s *Users) RevertEmailChange(ctx context.Context, token string) (EmailToken, error) {
	t, err := s.RedeemEmailToken(ctx, token, string(email.TokenPurpose(email.EmailChanged)))
	if err != nil {
		return EmailToken{}, err
	}
	u, err := s.activeUser(ctx, t.UserID)
	if err != nil {
		return EmailToken{}, err
	}
	if u.PreviousEmail == "" || u.PreviousEmail != t.Address {
		return EmailToken{}, ErrInvalidToken // another change happened since
	}
	restored := u
	restored.Email = u.PreviousEmail
	empty := ""
	now := s.now()
	err = s.Store.UpdateUser(ctx, u.ID, UserUpdate{Email: &t.Address, PendingEmail: &empty, PreviousEmail: &empty, EmailVerified: ptr(true)}, now)
	if errors.Is(err, ErrEmailTaken) {
		return EmailToken{}, ErrInvalidToken
	}
	if err != nil {
		return EmailToken{}, err
	}
	if err := s.Store.InvalidateEmailTokens(ctx, u.ID, string(email.TokenPurpose(email.ChangeEmail)), now); err != nil {
		return EmailToken{}, err
	}
	// The password may be known to whoever changed the address.
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return EmailToken{}, err
	}
	hash, err := s.hash(ctx, base64.RawURLEncoding.EncodeToString(random))
	if err != nil {
		return EmailToken{}, err
	}
	if err := s.putPassword(ctx, u.ID, hash, now); err != nil {
		return EmailToken{}, err
	}
	if err := s.endSessions(ctx, u.ID, "email_reverted"); err != nil {
		return EmailToken{}, err
	}
	s.AuditAs(ctx, userTarget(u.ID), AuditEmailReverted, userTarget(u.ID), nil)
	s.queueQuietly(ctx, EmailRequest{Kind: email.ResetPassword, UserID: u.ID, Address: restored.Email, Locale: s.LocaleOf(u)})
	return t, nil
}

// ChangeEmailAsAdmin changes a user's address at once, as an administrator
// vouching for it: the new address counts as verified, every session of the
// user ends, the old address gets the message with the link to undo it and
// the new one is told. It needs the realm to send email (the notifications).
func (s *Users) ChangeEmailAsAdmin(ctx context.Context, userID, newAddress string) (User, error) {
	if s.Settings.Email == nil {
		return User{}, ErrEmailNotConfigured
	}
	next, err := registry.NormalizeEmail(newAddress)
	if err != nil {
		return User{}, err
	}
	u, err := s.Store.UserByID(ctx, userID)
	if err != nil {
		return User{}, err
	}
	if next == u.Email {
		return User{}, ErrSameEmail
	}
	if err := s.applyEmailChange(ctx, u, next); err != nil {
		return User{}, err
	}
	s.Audit(ctx, AuditEmailChanged, userTarget(u.ID), map[string]any{"by": "admin"})
	s.queueEmailChanged(ctx, u, true)
	return s.Store.UserByID(ctx, userID)
}

// activeUser returns the user a token is for, or ErrInvalidToken when they
// are gone or disabled.
func (s *Users) activeUser(ctx context.Context, id string) (User, error) {
	u, err := s.Store.UserByID(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return User{}, ErrInvalidToken
	}
	if err != nil {
		return User{}, err
	}
	if u.Disabled {
		return User{}, ErrInvalidToken
	}
	return u, nil
}

// applyEmailChange stores the new address (verified), remembers the old one,
// ends the user's sessions and invalidates the links of earlier changes.
// ErrEmailTaken when another user has the address.
func (s *Users) applyEmailChange(ctx context.Context, u User, next string) error {
	empty := ""
	now := s.now()
	err := s.Store.UpdateUser(ctx, u.ID, UserUpdate{Email: &next, PendingEmail: &empty, PreviousEmail: &u.Email, EmailVerified: ptr(true)}, now)
	if err != nil {
		return err
	}
	for _, purpose := range []email.Purpose{email.TokenPurpose(email.ChangeEmail), email.TokenPurpose(email.EmailChanged)} {
		if err := s.Store.InvalidateEmailTokens(ctx, u.ID, string(purpose), now); err != nil {
			return err
		}
	}
	return s.endSessions(ctx, u.ID, "email_changed")
}

// queueEmailChanged tells the old address (with the link to undo the change)
// and, for an administrator's change, the new one too.
func (s *Users) queueEmailChanged(ctx context.Context, before User, tellNew bool) {
	after, err := s.Store.UserByID(ctx, before.ID)
	if err != nil {
		return
	}
	s.queueQuietly(ctx, EmailRequest{Kind: email.EmailChanged, UserID: before.ID, Address: before.Email, Locale: s.LocaleOf(after), To: "previous"})
	if tellNew {
		s.queueQuietly(ctx, EmailRequest{Kind: email.EmailChanged, UserID: before.ID, Address: after.Email, Locale: s.LocaleOf(after), Notice: true})
	}
}

// queueQuietly queues an email whose limits or failure must not change the
// outcome of what caused it.
func (s *Users) queueQuietly(ctx context.Context, r EmailRequest) {
	if s.Settings.Email == nil {
		return
	}
	_, err := s.QueueEmail(ctx, r)
	var limited *EmailLimitedError
	if err != nil && !errors.As(err, &limited) && s.Log != nil {
		s.Log.Error("queue an email", "kind", r.Kind, "error", err)
	}
}

func ptr[T any](v T) *T { return &v }
