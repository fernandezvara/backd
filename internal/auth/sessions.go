package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/registry"
)

// SessionTokenPrefix starts every session token, so tokens are easy to
// recognize (e.g. by secret scanners) and to tell apart from API keys.
const SessionTokenPrefix = "bds_"

// touchInterval limits how often a session's last use is written.
const touchInterval = time.Minute

var (
	// ErrInvalidCredentials is the one answer to a failed login, whatever
	// the reason (unknown email, wrong password, no password, disabled).
	ErrInvalidCredentials = errors.New("invalid email or password")
	// ErrUnauthenticated means the token is unknown, expired, revoked or
	// belongs to a disabled user.
	ErrUnauthenticated = errors.New("invalid or expired token")
	// ErrSignupClosed means the realm doesn't allow self sign-up.
	ErrSignupClosed = errors.New("sign-up is not open in this realm")
	// ErrEmailNotVerified answers a login with the right password for an
	// address that isn't verified yet, in a realm that requires it. It is
	// only returned after the password was checked, so it reveals nothing
	// to someone who doesn't have it.
	ErrEmailNotVerified = errors.New("verify your email address before signing in")
)

// Session is a signed-in session. Only a hash of its token is stored.
type Session struct {
	ID         string // public id, used to list and revoke sessions
	UserID     string
	TokenHash  string
	CreatedAt  time.Time
	LastUsedAt time.Time
	ExpiresAt  time.Time
}

// Principal is the authenticated caller of a request.
type Principal struct {
	User    User
	Session Session
}

// NewSessionToken returns a new random session token (256 bits).
func NewSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return SessionTokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken is the stored form of a token: hex SHA-256. Tokens are random
// and long, so a fast hash is enough.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// expiry is when a session expires: after the idle timeout since its last
// use, but never later than its maximum lifetime.
func (s *Users) expiry(created, lastUsed time.Time) time.Time {
	idle := lastUsed.Add(s.Settings.IdleTimeout)
	if max := created.Add(s.Settings.MaxLifetime); max.Before(idle) {
		return max
	}
	return idle
}

// Signup creates a user with a password and signs them in. Realms with
// `signup: open` accept anyone; `signup: invite` needs a valid invitation
// token (used up by a successful sign-up); `signup: closed` refuses.
//
// locales are the languages the person asked for, most wanted first (the
// request's locale, then Accept-Language); the user gets the best one the
// realm lists, or its default, silently.
func (s *Users) Signup(ctx context.Context, email, password, invitation string, locales ...string) (Principal, string, error) {
	u, err := s.signup(ctx, email, password, invitation, locales)
	if err != nil {
		return Principal{}, "", err
	}
	return s.startSession(ctx, u)
}

// signup creates the user and records it; Signup and SignUp differ in what
// follows.
func (s *Users) signup(ctx context.Context, email, password, invitation string, locales []string) (User, error) {
	var u User
	var err error
	locale := s.Settings.BestLocale(locales...)
	switch s.Settings.Signup {
	case registry.SignupOpen:
		u, err = s.create(ctx, email, &password, locale, false)
	case registry.SignupInvite:
		u, err = s.signupWithInvitation(ctx, email, password, invitation, locale)
	default:
		err = ErrSignupClosed
	}
	if err != nil {
		return User{}, err
	}
	s.AuditAs(ctx, userTarget(u.ID), AuditUserSignup, userTarget(u.ID), map[string]any{"invited": invitation != "", "roles": u.Roles})
	return u, nil
}

// Login checks an email and password and starts a session. Every failure
// returns ErrInvalidCredentials after the same amount of hashing work, so
// neither the answer nor its timing reveals whether the email exists.
// Repeated failures for the email or from the client ip (may be empty)
// are throttled with a *ThrottledError.
func (s *Users) Login(ctx context.Context, email, password, ip string) (Principal, string, error) {
	var u User
	err := s.throttled(ctx, email, ip, func() error {
		var err error
		if u, err = s.verifyLogin(ctx, email, password); err != nil {
			return err
		}
		// Outside the user's login networks, the right password fails
		// like a wrong one, and counts as a failure.
		if !u.LoginNetworks.Allows(ip) {
			return ErrInvalidCredentials
		}
		if s.Settings.Account.RequireVerifiedEmail && !u.EmailVerified {
			return ErrEmailNotVerified
		}
		return nil
	})
	if err != nil {
		return Principal{}, "", err
	}
	p, token, err := s.startSession(ctx, u)
	if err == nil && s.Settings.IsAdmin(u.Roles) {
		s.AuditAs(ctx, userTarget(u.ID), AuditAdminLogin, userTarget(u.ID), map[string]any{"session_id": p.Session.ID})
	}
	return p, token, err
}

// verifyLogin returns the user if password is theirs, else
// ErrInvalidCredentials.
func (s *Users) verifyLogin(ctx context.Context, email, password string) (User, error) {
	hash := ""
	u, err := s.byEmail(ctx, email)
	switch {
	case err == nil:
		id, err := s.Store.Identity(ctx, ProviderPassword, u.ID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return User{}, err
		}
		hash = id.PasswordHash
	case !errors.Is(err, ErrNotFound):
		return User{}, err
	}
	// Without a real hash, verify against a dummy one for equal timing;
	// the result is ignored.
	real := hash != ""
	if !real {
		if hash, err = s.dummyHash(ctx); err != nil {
			return User{}, err
		}
	}
	ok, err := s.Hasher.Verify(ctx, password, hash)
	if err != nil {
		return User{}, err
	}
	if !ok || !real || u.Disabled {
		return User{}, ErrInvalidCredentials
	}
	return u, nil
}

// dummyHashes holds, per set of hashing parameters, a hash to verify
// against when there is no real one.
var dummyHashes sync.Map

func (s *Users) dummyHash(ctx context.Context) (string, error) {
	if h, ok := dummyHashes.Load(s.Hasher.Params); ok {
		return h.(string), nil
	}
	h, err := s.Hasher.Hash(ctx, "backd-dummy-password")
	if err != nil {
		return "", err
	}
	dummyHashes.Store(s.Hasher.Params, h)
	return h, nil
}

func (s *Users) startSession(ctx context.Context, u User) (Principal, string, error) {
	token, err := NewSessionToken()
	if err != nil {
		return Principal{}, "", err
	}
	now := s.now()
	sess := Session{ID: xid.New().String(), UserID: u.ID, TokenHash: HashToken(token), CreatedAt: now, LastUsedAt: now}
	sess.ExpiresAt = s.expiry(now, now)
	if err := s.Store.CreateSession(ctx, sess); err != nil {
		return Principal{}, "", err
	}
	return Principal{User: u, Session: sess}, token, nil
}

// Authenticate resolves a session token to its user. It extends the
// session's idle timeout, writing at most once per minute.
func (s *Users) Authenticate(ctx context.Context, token string) (Principal, error) {
	if !strings.HasPrefix(token, SessionTokenPrefix) {
		return Principal{}, ErrUnauthenticated
	}
	sess, u, err := s.Store.SessionByTokenHash(ctx, HashToken(token))
	if errors.Is(err, ErrNotFound) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, err
	}
	now := s.now()
	if !now.Before(sess.ExpiresAt) || u.Disabled {
		return Principal{}, ErrUnauthenticated
	}
	if now.Sub(sess.LastUsedAt) >= touchInterval {
		sess.LastUsedAt = now
		sess.ExpiresAt = s.expiry(sess.CreatedAt, now)
		if err := s.Store.TouchSession(ctx, sess.ID, sess.LastUsedAt, sess.ExpiresAt); err != nil {
			return Principal{}, err
		}
	}
	return Principal{User: u, Session: sess}, nil
}

// Logout ends the caller's session.
func (s *Users) Logout(ctx context.Context, p Principal) error {
	return s.Store.DeleteSession(ctx, p.User.ID, p.Session.ID)
}

// LogoutAll ends every session of the caller, including the current one.
func (s *Users) LogoutAll(ctx context.Context, p Principal) error {
	return s.Store.DeleteSessions(ctx, p.User.ID)
}

// Sessions lists the caller's unexpired sessions, newest first.
func (s *Users) Sessions(ctx context.Context, p Principal) ([]Session, error) {
	all, err := s.Store.ListSessions(ctx, p.User.ID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := all[:0]
	for _, x := range all {
		if now.Before(x.ExpiresAt) {
			out = append(out, x)
		}
	}
	return out, nil
}

// RevokeSession ends one of the caller's sessions; ErrNotFound if the
// caller has no session with that id.
func (s *Users) RevokeSession(ctx context.Context, p Principal, id string) error {
	return s.Store.DeleteSession(ctx, p.User.ID, id)
}

// ChangePassword replaces the caller's password after checking the
// current one, and ends all of their other sessions.
func (s *Users) ChangePassword(ctx context.Context, p Principal, current, next string) error {
	if err := s.checkPassword(ctx, p.User, current); err != nil {
		return err
	}
	hash, err := s.hash(ctx, next)
	if err != nil {
		return err
	}
	if err := s.putPassword(ctx, p.User.ID, hash, s.now()); err != nil {
		return err
	}
	s.Audit(ctx, AuditPasswordChange, userTarget(p.User.ID), nil)
	return s.Store.DeleteOtherSessions(ctx, p.User.ID, p.Session.ID)
}

// DeleteAccount deletes the caller after checking their password.
func (s *Users) DeleteAccount(ctx context.Context, p Principal, password string) error {
	if err := s.checkPassword(ctx, p.User, password); err != nil {
		return err
	}
	if err := s.Store.DeleteUser(ctx, p.User.ID); err != nil {
		return err
	}
	s.Audit(ctx, AuditAccountDelete, userTarget(p.User.ID), nil)
	return nil
}

// checkPassword returns ErrInvalidCredentials unless password is the
// user's current password. It is throttled like logins for the account,
// so a stolen session can't be used to guess the password.
func (s *Users) checkPassword(ctx context.Context, u User, password string) error {
	return s.throttled(ctx, u.Email, "", func() error { return s.comparePassword(ctx, u, password) })
}

func (s *Users) comparePassword(ctx context.Context, u User, password string) error {
	id, err := s.Store.Identity(ctx, ProviderPassword, u.ID)
	if errors.Is(err, ErrNotFound) {
		return ErrInvalidCredentials
	}
	if err != nil {
		return err
	}
	ok, err := s.Hasher.Verify(ctx, password, id.PasswordHash)
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidCredentials
	}
	return nil
}
