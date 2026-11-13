package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/auth/passkey"
)

// Managing a user's passkeys (design/passkeys.md §8). The routes act on the signed-in user; the
// admin removal acts on any user. Sign-in with a passkey comes with the next steps.

// MaxPasskeys is how many passkeys a user may register.
const MaxPasskeys = 10

// PasskeyNameError: a passkey name that cannot be used.
type PasskeyNameError struct{ Reason string }

func (e *PasskeyNameError) Error() string { return "name: " + e.Reason }

// ErrPasskeyLimit: the user has MaxPasskeys passkeys already.
var ErrPasskeyLimit = errors.New("a user can have at most 10 passkeys")

// Audit events of passkeys; the target is the user, details: passkey (backd's id), name.
const (
	AuditPasskeyRegistered = "passkey.registered"
	AuditPasskeyRemoved    = "passkey.removed" // details also: by (self or admin)
)

// PasskeyInfo is a passkey as the user sees it: never the key material.
type PasskeyInfo struct {
	ID             string
	Name           string
	CreatedAt      time.Time
	LastUsedAt     time.Time
	Transports     []string
	BackupEligible bool
	BackupState    bool
}

func infoOf(c passkey.Credential) PasskeyInfo {
	t := c.Transports
	if t == nil {
		t = []string{}
	}
	return PasskeyInfo{ID: c.ID, Name: c.Name, CreatedAt: c.CreatedAt, LastUsedAt: c.LastUsedAt, Transports: t, BackupEligible: c.BackupEligible, BackupState: c.BackupState}
}

// ListPasskeys lists the user's passkeys, oldest first.
func (s *Users) ListPasskeys(ctx context.Context, userID string) ([]PasskeyInfo, error) {
	list, err := s.Store.ListByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]PasskeyInfo, len(list))
	for i, c := range list {
		out[i] = infoOf(c)
	}
	return out, nil
}

// PasskeyCount is how many passkeys the user has (0 in a realm without passkeys).
func (s *Users) PasskeyCount(ctx context.Context, userID string) (int, error) {
	if !s.Settings.Passkeys.Enabled {
		return 0, nil
	}
	return s.Store.CountByUser(ctx, userID)
}

// BeginPasskeyRegistration starts adding a passkey for the user. The caller has checked that the
// session may (reauth, not through a connection). It returns the options for the browser and the
// id of the ceremony to send back.
func (s *Users) BeginPasskeyRegistration(ctx context.Context, u User) (options []byte, ceremony string, err error) {
	svc, err := s.PasskeyService()
	if err != nil {
		return nil, "", err
	}
	if n, err := s.Store.CountByUser(ctx, u.ID); err != nil {
		return nil, "", err
	} else if n >= MaxPasskeys {
		return nil, "", ErrPasskeyLimit
	}
	return svc.BeginRegistration(ctx, passkey.UserInfo{ID: u.ID, Name: u.Email, DisplayName: u.Email})
}

// FinishPasskeyRegistration checks the browser's answer and stores the passkey under name. It does not
// end other sessions and changes nothing about how the user signs in otherwise; it makes no recovery
// codes. The user is told by email, and it is audited.
func (s *Users) FinishPasskeyRegistration(ctx context.Context, u User, ceremony string, credential []byte, name string) (PasskeyInfo, error) {
	svc, err := s.PasskeyService()
	if err != nil {
		return PasskeyInfo{}, err
	}
	name, err = cleanPasskeyName(name)
	if err != nil {
		return PasskeyInfo{}, err
	}
	// Re-checked here: two ceremonies begun together must not go over the limit.
	if n, err := s.Store.CountByUser(ctx, u.ID); err != nil {
		return PasskeyInfo{}, err
	} else if n >= MaxPasskeys {
		return PasskeyInfo{}, ErrPasskeyLimit
	}
	c, err := svc.FinishRegistration(ctx, ceremony, u.ID, credential, name)
	if err != nil {
		return PasskeyInfo{}, err
	}
	s.Audit(ctx, AuditPasskeyRegistered, userTarget(u.ID), map[string]any{"passkey": c.ID, "name": c.Name, "synced": c.BackupEligible})
	s.Metrics.PasskeyEvent(s.Realm, "registered")
	s.securityNotice(ctx, u, "passkey-added", 0)
	return infoOf(c), nil
}

// cleanPasskeyName trims a name and gives the default for none.
func cleanPasskeyName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "Passkey", nil
	}
	if len([]rune(name)) > 64 {
		return "", &PasskeyNameError{Reason: "must be at most 64 characters"}
	}
	return name, nil
}

// RenamePasskey renames one of the user's passkeys.
func (s *Users) RenamePasskey(ctx context.Context, userID, id, name string) (PasskeyInfo, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return PasskeyInfo{}, &PasskeyNameError{Reason: "is required"}
	}
	if len([]rune(name)) > 64 {
		return PasskeyInfo{}, &PasskeyNameError{Reason: "must be at most 64 characters"}
	}
	c, err := s.passkeyOf(ctx, userID, id)
	if err != nil {
		return PasskeyInfo{}, err
	}
	if err := s.Store.Update(ctx, c.CredentialID, passkey.Update{Name: &name}); err != nil {
		return PasskeyInfo{}, err
	}
	c.Name = name
	return infoOf(c), nil
}

func (s *Users) passkeyOf(ctx context.Context, userID, id string) (passkey.Credential, error) {
	list, err := s.Store.ListByUser(ctx, userID)
	if err != nil {
		return passkey.Credential{}, err
	}
	for _, c := range list {
		if c.ID == id {
			return c, nil
		}
	}
	return passkey.Credential{}, ErrNotFound
}

// passkeysAreSignIn says a passkey is a way to sign in at all in this realm: some strategy starts
// with it. Until one does, passkeys are credentials a user holds, not a way in, and removing one
// can never take the user's last way to sign in.
func (s *Users) passkeysAreSignIn() bool {
	for _, st := range s.Settings.Strategies {
		if len(st.Methods) > 0 && st.Methods[0] == "passkey" {
			return true
		}
	}
	return false
}

// RemovePasskey removes one of the user's passkeys. It refuses (ErrLastSignInMethod) the user's last way
// to sign in, ends the sessions that passkey opened, tells the user and audits it.
func (s *Users) RemovePasskey(ctx context.Context, u User, id string) error {
	c, err := s.passkeyOf(ctx, u.ID, id)
	if err != nil {
		return err
	}
	if s.passkeysAreSignIn() {
		ids, err := s.Identities(ctx, u.ID)
		if err != nil {
			return err
		}
		others, err := s.Store.CountByUser(ctx, u.ID)
		if err != nil {
			return err
		}
		if s.signInMethodsLeft(u, ids, "", "")+others-1 == 0 {
			return ErrLastSignInMethod
		}
	}
	return s.removePasskey(ctx, u, c, "self")
}

// AdminRemovePasskey removes a passkey of any user (a lost device the user cannot remove themselves).
func (s *Users) AdminRemovePasskey(ctx context.Context, userID, id string) error {
	u, err := s.Store.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	c, err := s.passkeyOf(ctx, userID, id)
	if err != nil {
		return err
	}
	return s.removePasskey(ctx, u, c, "admin")
}

func (s *Users) removePasskey(ctx context.Context, u User, c passkey.Credential, by string) error {
	if err := s.Store.Delete(ctx, u.ID, c.ID); err != nil {
		return err
	}
	n, err := s.Store.DeleteSessionsOfPasskey(ctx, u.ID, c.ID)
	if err != nil {
		return err
	}
	s.Metrics.SessionsEnded(s.Realm, "passkey_removed", n)
	s.Audit(ctx, AuditPasskeyRemoved, userTarget(u.ID), map[string]any{"passkey": c.ID, "name": c.Name, "by": by, "sessions_ended": n})
	event := "removed"
	if by == "admin" {
		event = "removed_by_admin"
	}
	s.Metrics.PasskeyEvent(s.Realm, event)
	s.securityNotice(ctx, u, "passkey-removed", 0)
	return nil
}
