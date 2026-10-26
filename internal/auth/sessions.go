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
	// ErrSessionUnscoped: in a realm with organizations every session belongs to one, and this one
	// does not (it was opened before the realm had them, or by another way): the person signs in
	// again, to an organization. Operators, who run the realm and belong to none, are the exception.
	ErrSessionUnscoped = errors.New("this session belongs to no organization: sign in to one")
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
	// MaxLifetime is the organization's own limit on the lifetime of its sessions, fixed when the
	// session opened (0: the realm's).
	MaxLifetime time.Duration
	// Methods are how the session was created: the first factor, plus "totp" or "recovery"
	// when a second factor was used; MFA says it was. Empty for sessions from before
	// they were recorded.
	Methods []string
	MFA     bool
	// Org is the organization the session is scoped to: it reaches that one and no other
	// (design/scoped-sessions.md). "" for an operator's session, and for one from before sessions
	// were scoped.
	Org string
}

// Principal is the authenticated caller of a request.
type Principal struct {
	User    User
	Session Session
}

// newOpaqueToken is prefix followed by 256 random bits, URL-safe. It is what a token the
// caller holds is made of; only its hash is stored.
func newOpaqueToken(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// NewSessionToken returns a new random session token (256 bits).
func NewSessionToken() (string, error) {
	return newOpaqueToken(SessionTokenPrefix)
}

// HashToken is the stored form of a token: hex SHA-256. Tokens are random
// and long, so a fast hash is enough.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// expiry is when a user's session expires: after the idle timeout since its
// last use, but never later than its maximum lifetime. Users who hold an
// admin role have their own (shorter) limits, judged by the roles they hold
// now: a promotion shortens their sessions from the next request on.
func (s *Users) expiry(u User, created, lastUsed time.Time, orgMax time.Duration) time.Time {
	idleTimeout, maxLifetime := s.Settings.IdleTimeout, s.Settings.MaxLifetime
	if s.Settings.IsAdmin(u.Roles) {
		if d := s.Settings.AdminIdleTimeout; d > 0 {
			idleTimeout = d
		}
		if d := s.Settings.AdminMaxLifetime; d > 0 {
			maxLifetime = d
		}
	}
	if orgMax > 0 && orgMax < maxLifetime {
		maxLifetime = orgMax
	}
	idle := lastUsed.Add(idleTimeout)
	if max := created.Add(maxLifetime); max.Before(idle) {
		return max
	}
	return idle
}

// MaxSessionExpiry is the latest a session started at created can last for
// this user: the absolute limit, whatever the idle timeout slides to.
func (s *Users) MaxSessionExpiry(u User, sess Session) time.Time {
	maxLifetime := s.Settings.MaxLifetime
	if d := s.Settings.AdminMaxLifetime; d > 0 && s.Settings.IsAdmin(u.Roles) {
		maxLifetime = d
	}
	if d := sess.MaxLifetime; d > 0 && d < maxLifetime {
		maxLifetime = d
	}
	return sess.CreatedAt.Add(maxLifetime)
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
	out, err := s.afterFirstFactor(ctx, u, ProviderPassword, handover{})
	if err != nil {
		return Principal{}, "", err
	}
	if out.Challenge != nil {
		return Principal{}, "", ErrSecondStepRequired
	}
	return out.Principal, out.Token, nil
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
	s.queueOnSignup(ctx, u, ProviderPassword, nil)
	return u, nil
}

// Login checks an email and password and starts a session. Every failure
// returns ErrInvalidCredentials after the same amount of hashing work, so
// neither the answer nor its timing reveals whether the email exists.
// Repeated failures for the email or from the client ip (may be empty)
// are throttled with a *ThrottledError.
func (s *Users) Login(ctx context.Context, email, password, ip string) (Principal, string, error) {
	out, err := s.SignIn(ctx, email, password, ip)
	if err != nil {
		return Principal{}, "", err
	}
	if out.Challenge != nil {
		return Principal{}, "", ErrSecondStepRequired
	}
	return out.Principal, out.Token, nil
}

// SignIn is Login for callers that can answer a second step: the outcome is a session,
// or a challenge the user trades for one with VerifyMFA. In a realm with organizations it is the
// operators' door alone.
func (s *Users) SignIn(ctx context.Context, email, password, ip string) (Redeemed, error) {
	var hooks signInHooks
	if s.Settings.Organizations != nil {
		// A realm with organizations has one more door than organizations: the realm's own, for the
		// people who run it. Anyone else signs in to an organization (SignInToOrg), and is refused
		// here like a wrong password (design/scoped-sessions.md §5.5).
		hooks.Gate = func(u User) error {
			if !s.Settings.IsAdmin(u.Roles) {
				return ErrInvalidCredentials
			}
			return nil
		}
	}
	return s.signIn(ctx, email, password, ip, hooks)
}

// signInHooks lets a sign-in add to the password's: Gate runs inside the throttled check as soon
// as the password is right (its error counts as a failed attempt, so a right password that
// leads nowhere looks like a wrong one), After runs when every check passed and before the
// session or the challenge, and Handover is what the sign-in carries to its end.
type signInHooks struct {
	Gate     func(User) error
	After    func(User) error
	Handover handover
}

func (s *Users) signIn(ctx context.Context, email, password, ip string, hooks signInHooks) (Redeemed, error) {
	var u User
	err := s.throttled(ctx, email, ip, func() error {
		var id Identity
		var err error
		if u, id, err = s.verifyLogin(ctx, email, password); err != nil {
			return err
		}
		if hooks.Gate != nil {
			if err := hooks.Gate(u); err != nil {
				return err
			}
		}
		// Outside the user's login networks, the right password fails
		// like a wrong one, and counts as a failure.
		if !u.LoginNetworks.Allows(ip) {
			return ErrInvalidCredentials
		}
		// A correct password that expired opens nothing; the answer is safe because only
		// the right password gets it.
		if expired, err := s.passwordExpired(ctx, u, id); err != nil {
			return err
		} else if expired {
			return ErrPasswordExpired
		}
		if s.Settings.Account.RequireVerifiedEmail && !u.EmailVerified {
			return ErrEmailNotVerified
		}
		return nil
	})
	if errors.Is(err, ErrPasswordExpired) {
		err = &PasswordExpiredError{EmailQueued: s.mailExpiredPassword(ctx, u, ip)}
	}
	if err != nil {
		return Redeemed{}, err
	}
	s.touchIdentity(ctx, u.ID, ProviderPassword)
	if hooks.After != nil {
		if err := hooks.After(u); err != nil {
			return Redeemed{}, err
		}
	}
	return s.afterFirstFactor(ctx, u, ProviderPassword, hooks.Handover)
}

// verifyLogin returns the user, and their password identity, if password is theirs, else
// ErrInvalidCredentials.
func (s *Users) verifyLogin(ctx context.Context, email, password string) (User, Identity, error) {
	hash := ""
	var ident Identity
	u, err := s.byEmail(ctx, email)
	switch {
	case err == nil:
		id, err := s.Store.Identity(ctx, ProviderPassword, u.ID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return User{}, Identity{}, err
		}
		hash, ident = id.PasswordHash, id
	case !errors.Is(err, ErrNotFound):
		return User{}, Identity{}, err
	}
	// Without a real hash, verify against a dummy one for equal timing;
	// the result is ignored.
	real := hash != ""
	if !real {
		if hash, err = s.dummyHash(ctx); err != nil {
			return User{}, Identity{}, err
		}
	}
	ok, err := s.Hasher.Verify(ctx, password, hash)
	if err != nil {
		return User{}, Identity{}, err
	}
	if !ok || !real || u.Disabled {
		return User{}, Identity{}, ErrInvalidCredentials
	}
	return u, ident, nil
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

// startSessionWith starts a session that records how it was created.
func (s *Users) startSessionWith(ctx context.Context, u User, methods []string, mfa bool, org string) (Principal, string, error) {
	token, err := NewSessionToken()
	if err != nil {
		return Principal{}, "", err
	}
	now := s.now()
	sess := Session{ID: xid.New().String(), UserID: u.ID, TokenHash: HashToken(token), CreatedAt: now, LastUsedAt: now, Methods: methods, MFA: mfa, Org: org}
	if org != "" {
		if o, err := s.Store.OrgByID(ctx, org); err == nil {
			// Every session of an organization that requires its connection comes through it: this is
			// the one place a session is made, so a code, a link or a challenge issued before the
			// requirement can't open one after it.
			if o.SSORequired && (len(methods) == 0 || !strings.HasPrefix(methods[0], ConnectionMethodPrefix(org))) {
				return Principal{}, "", ErrSSORequired
			}
			// The organization may shorten the lifetime; it is fixed now and never extended.
			sess.MaxLifetime = o.SessionMaxAge
		}
	}
	sess.ExpiresAt = s.expiry(u, now, now, sess.MaxLifetime)
	if err := s.Store.CreateSession(ctx, sess); err != nil {
		return Principal{}, "", err
	}
	s.Metrics.SessionStarted(s.Realm)
	if s.Settings.IsAdmin(u.Roles) && len(methods) > 0 {
		s.AuditAs(ctx, userTarget(u.ID), AuditAdminLogin, userTarget(u.ID), map[string]any{"session_id": sess.ID})
	}
	return Principal{User: u, Session: sess}, token, nil
}

// Authenticate resolves a session token to its user. It extends the
// session's idle timeout, writing at most once per minute.
func (s *Users) Authenticate(ctx context.Context, token string) (Principal, error) {
	p, err := s.AuthenticateIgnoringConsents(ctx, token)
	if err != nil {
		return Principal{}, err
	}
	if err := s.gateConsents(p.User); err != nil {
		return Principal{}, err
	}
	return p, nil
}

// AuthenticateIgnoringConsents is Authenticate without the consent deadline: for the few
// routes a person needs to accept what is missing, or to leave (see consents_signin.go).
func (s *Users) AuthenticateIgnoringConsents(ctx context.Context, token string) (Principal, error) {
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
	// The stored expiry was set at the last write; the user's roles may have
	// changed since, so the limits are applied again.
	if !now.Before(sess.ExpiresAt) || !now.Before(s.expiry(u, sess.CreatedAt, sess.LastUsedAt, sess.MaxLifetime)) || u.Disabled {
		return Principal{}, ErrUnauthenticated
	}
	if s.Settings.Organizations != nil && sess.Org == "" && !s.Settings.IsAdmin(u.Roles) {
		return Principal{}, ErrSessionUnscoped
	}
	if now.Sub(sess.LastUsedAt) >= touchInterval {
		sess.LastUsedAt = now
		sess.ExpiresAt = s.expiry(u, sess.CreatedAt, now, sess.MaxLifetime)
		if err := s.Store.TouchSession(ctx, sess.ID, sess.LastUsedAt, sess.ExpiresAt); err != nil {
			return Principal{}, err
		}
	}
	return Principal{User: u, Session: sess}, nil
}

// endSessions ends every session of a user and counts them under reason.
func (s *Users) endSessions(ctx context.Context, userID, reason string) error {
	n, err := s.Store.DeleteSessions(ctx, userID)
	s.Metrics.SessionsEnded(s.Realm, reason, n)
	return err
}

// endOtherSessions is endSessions without the session keepID.
func (s *Users) endOtherSessions(ctx context.Context, userID, keepID, reason string) error {
	n, err := s.Store.DeleteOtherSessions(ctx, userID, keepID)
	s.Metrics.SessionsEnded(s.Realm, reason, n)
	return err
}

// endSession ends one session and counts it under reason.
func (s *Users) endSession(ctx context.Context, userID, id, reason string) error {
	err := s.Store.DeleteSession(ctx, userID, id)
	if err == nil {
		s.Metrics.SessionsEnded(s.Realm, reason, 1)
	}
	return err
}

// countSessions counts a user's sessions before something that ends them
// without saying how many (deleting or erasing the user).
func (s *Users) countSessions(ctx context.Context, userID, reason string) {
	if s.Metrics == nil {
		return
	}
	if all, err := s.Store.ListSessions(ctx, userID); err == nil {
		s.Metrics.SessionsEnded(s.Realm, reason, int64(len(all)))
	}
}

// Logout ends the caller's session.
func (s *Users) Logout(ctx context.Context, p Principal) error {
	return s.endSession(ctx, p.User.ID, p.Session.ID, "logout")
}

// LogoutAll ends every session of the caller, including the current one.
func (s *Users) LogoutAll(ctx context.Context, p Principal) error {
	return s.endSessions(ctx, p.User.ID, "logout_all")
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

// UserSessions lists a user's unexpired sessions, newest first (an
// administrator's view of someone else's).
func (s *Users) UserSessions(ctx context.Context, userID string) ([]Session, error) {
	return s.Sessions(ctx, Principal{User: User{ID: userID}})
}

// RevokeUserSession ends one session of a user, on an administrator's say-so;
// ErrNotFound if the user has no session with that id.
func (s *Users) RevokeUserSession(ctx context.Context, userID, id string) error {
	if err := s.endSession(ctx, userID, id, "admin_revoked"); err != nil {
		return err
	}
	s.Audit(ctx, AuditSessionRevoke, userTarget(userID), map[string]any{"session": id})
	return nil
}

// RevokeSession ends one of the caller's sessions; ErrNotFound if the
// caller has no session with that id.
func (s *Users) RevokeSession(ctx context.Context, p Principal, id string) error {
	return s.endSession(ctx, p.User.ID, id, "revoked")
}

// ChangePassword replaces the caller's password and ends all of their other sessions.
// The caller has checked that the user proved who they are just now (CheckReauth).
func (s *Users) ChangePassword(ctx context.Context, p Principal, next string) error {
	hash, err := s.hash(ctx, next)
	if err != nil {
		return err
	}
	_, had := s.Store.IdentityOf(ctx, p.User.ID, ProviderPassword)
	if err := s.putPassword(ctx, p.User.ID, hash, s.now()); err != nil {
		return err
	}
	if errors.Is(had, ErrNotFound) {
		// An account that signed in without a password sets its first one.
		s.Audit(ctx, AuditPasswordSet, userTarget(p.User.ID), nil)
		if err := s.endOtherSessions(ctx, p.User.ID, p.Session.ID, "password_set"); err != nil {
			return err
		}
		s.securityNotice(ctx, p.User, "password-set", 0)
		return nil
	}
	s.Audit(ctx, AuditPasswordChange, userTarget(p.User.ID), nil)
	if err := s.endOtherSessions(ctx, p.User.ID, p.Session.ID, "password_changed"); err != nil {
		return err
	}
	s.notifyPasswordChanged(ctx, p.User)
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
	// The caller's user may be older than the last expiry; read the current one.
	if u, err = s.Store.UserByID(ctx, u.ID); err != nil {
		return err
	}
	if expired, err := s.passwordExpired(ctx, u, id); err != nil {
		return err
	} else if expired {
		return ErrPasswordExpired
	}
	return nil
}
