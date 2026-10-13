package auth

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/metrics"
	"github.com/fernandezvara/backd/internal/registry"
)

// Users manages the users of one realm.
type Users struct {
	Store    Store
	Hasher   *Hasher
	Settings registry.RealmSettings
	Now      func() time.Time // defaults to time.Now
	Realm    string           // the realm's name, for logs
	Log      *slog.Logger     // audit records are also logged here; nil: not logged

	// Cipher encrypts and decrypts secrets (roadmap F5); nil when
	// BACKD_SECRETS_KEY isn't configured, in which case every secret
	// resolves as missing. Cache holds decrypted values briefly, shared
	// across every realm's Users (NewSecretCache); nil disables caching.
	Cipher *SecretCipher
	Cache  *SecretCache

	// Metrics counts sessions and limits; nil turns it off.
	Metrics *metrics.Metrics

	// noLocalLock skips the in-process lock of password checks, so a test with
	// several Users on one store can show that the store's lock alone holds.
	noLocalLock bool
}

// Clock is the service's current time (its Now, or the real one).
func (s *Users) Clock() time.Time { return s.now() }

func (s *Users) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Create adds a user, as an administrator, with the address already verified. With password nil the user has
// no password identity and can't sign in with a password until one is set.
func (s *Users) Create(ctx context.Context, email string, password *string) (User, error) {
	// An administrator vouches for the address (bootstrap and the admin API).
	u, err := s.create(ctx, email, password, "", true)
	if err != nil {
		return User{}, err
	}
	s.Audit(ctx, AuditUserCreate, userTarget(u.ID), map[string]any{"password": password != nil, "roles": u.Roles})
	return u, nil
}

func userTarget(id string) string { return "user:" + id }

// create makes a user. locale is the language they asked for ("" for none): it
// is mapped to one the realm lists, or its default.
func (s *Users) create(ctx context.Context, email string, password *string, locale string, verified bool) (User, error) {
	email, err := registry.NormalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	var hash string
	if password != nil {
		if hash, err = s.hash(ctx, *password); err != nil {
			return User{}, err
		}
	}
	now := s.now()
	// Roles seeded in realm.yaml apply from the start.
	roles := s.seedRoles(email)
	if roles == nil {
		roles = []string{}
	}
	u := User{ID: xid.New().String(), Email: email, Roles: roles, Locale: s.Settings.BestLocale(locale), EmailVerified: verified, CreatedAt: now, UpdatedAt: now}
	if err := s.Store.CreateUser(ctx, u); err != nil {
		return User{}, err
	}
	if password != nil {
		if err := s.putPassword(ctx, u.ID, hash, now); err != nil {
			// Don't leave a user behind that the caller thinks wasn't created.
			_ = s.Store.DeleteUser(context.WithoutCancel(ctx), u.ID)
			return User{}, err
		}
	}
	return u, nil
}

// SetPassword sets or replaces the user's password and revokes all of
// their sessions.
func (s *Users) SetPassword(ctx context.Context, email, password string) error {
	u, err := s.byEmail(ctx, email)
	if err != nil {
		return err
	}
	hash, err := s.hash(ctx, password)
	if err != nil {
		return err
	}
	if err := s.putPassword(ctx, u.ID, hash, s.now()); err != nil {
		return err
	}
	s.Audit(ctx, AuditUserPassword, userTarget(u.ID), nil)
	if err := s.endSessions(ctx, u.ID, "password_set"); err != nil {
		return err
	}
	s.notifyPasswordChanged(ctx, u)
	return nil
}

// SetEmailVerified marks the user's email as verified or not.
func (s *Users) SetEmailVerified(ctx context.Context, email string, verified bool) error {
	u, err := s.byEmail(ctx, email)
	if err != nil {
		return err
	}
	if err := s.Store.UpdateUser(ctx, u.ID, UserUpdate{EmailVerified: &verified}, s.now()); err != nil {
		return err
	}
	s.Audit(ctx, AuditUserVerifyEmail, userTarget(u.ID), map[string]any{"verified": verified})
	return nil
}

// InvalidLocaleError means a language the realm doesn't list was asked for.
type InvalidLocaleError struct{ Allowed []string }

func (e *InvalidLocaleError) Error() string {
	return "locale must be one of: " + strings.Join(e.Allowed, ", ")
}

// SetLocale changes a user's language to one the realm lists (ignoring case;
// es-mx sets es-MX). Any other value is an *InvalidLocaleError naming the
// allowed ones: unlike sign-up, an explicit change isn't silently mapped.
func (s *Users) SetLocale(ctx context.Context, userID, locale string) (User, error) {
	listed := s.Settings.ListedLocale(locale)
	if listed == "" {
		_, allowed := s.Settings.Languages()
		return User{}, &InvalidLocaleError{Allowed: allowed}
	}
	if err := s.Store.UpdateUser(ctx, userID, UserUpdate{Locale: &listed}, s.now()); err != nil {
		return User{}, err
	}
	return s.Store.UserByID(ctx, userID)
}

// LocaleOf is the language of a user: theirs, or the realm's default for
// users from before languages existed.
func (s *Users) LocaleOf(u User) string {
	if u.Locale != "" {
		return u.Locale
	}
	def, _ := s.Settings.Languages()
	return def
}

// SetDisabled disables or re-enables the user. Disabling revokes all of
// their sessions.
func (s *Users) SetDisabled(ctx context.Context, email string, disabled bool) error {
	u, err := s.byEmail(ctx, email)
	if err != nil {
		return err
	}
	if !u.ErasedAt.IsZero() {
		return ErrUserErased // all that is left is a tombstone
	}
	if err := s.Store.UpdateUser(ctx, u.ID, UserUpdate{Disabled: &disabled}, s.now()); err != nil {
		return err
	}
	if disabled {
		s.Audit(ctx, AuditUserDisable, userTarget(u.ID), nil)
	} else {
		s.Audit(ctx, AuditUserEnable, userTarget(u.ID), nil)
	}
	if disabled {
		return s.endSessions(ctx, u.ID, "disabled")
	}
	return nil
}

// Delete removes the user, their identities and sessions. Documents they
// own are kept.
func (s *Users) Delete(ctx context.Context, email string) error {
	u, err := s.byEmail(ctx, email)
	if err != nil {
		return err
	}
	s.countSessions(ctx, u.ID, "deleted")
	if err := s.queueAppleRevocation(ctx, u.ID); err != nil {
		return err
	}
	if err := s.Store.DeleteUser(ctx, u.ID); err != nil {
		return err
	}
	s.Audit(ctx, AuditUserDelete, userTarget(u.ID), nil)
	return nil
}

// UserByID returns the user with that id, or ErrNotFound.
func (s *Users) UserByID(ctx context.Context, id string) (User, error) {
	return s.Store.UserByID(ctx, id)
}

// Find returns the user with that email, or ErrNotFound.
func (s *Users) Find(ctx context.Context, email string) (User, error) { return s.byEmail(ctx, email) }

// List returns every user of the realm.
func (s *Users) List(ctx context.Context) ([]User, error) { return s.Store.ListUsers(ctx) }

// ListPage is List for one page, sorted by email; see Store.ListUsersPage.
func (s *Users) ListPage(ctx context.Context, contains, after string, skip, limit int) ([]User, bool, error) {
	return s.Store.ListUsersPage(ctx, contains, after, skip, limit)
}

func (s *Users) byEmail(ctx context.Context, email string) (User, error) {
	e, err := registry.NormalizeEmail(email)
	if err != nil {
		return User{}, errors.Join(ErrNotFound, err)
	}
	return s.Store.UserByEmail(ctx, e)
}

func (s *Users) hash(ctx context.Context, password string) (string, error) {
	p, err := CheckPassword(password, s.Settings.PasswordMinLength)
	if err != nil {
		return "", err
	}
	return s.Hasher.Hash(ctx, p)
}

func (s *Users) putPassword(ctx context.Context, userID, hash string, now time.Time) error {
	return s.Store.PutIdentity(ctx, Identity{
		ID:           xid.New().String(),
		UserID:       userID,
		Provider:     ProviderPassword,
		Subject:      userID,
		PasswordHash: hash,
		CreatedAt:    now,
		UpdatedAt:    now,
	})
}
