// Package passkey is the WebAuthn side of passkeys (design/passkeys.md §14): the ceremonies of
// registering a credential and signing in with one, and the credential model with its store. It is
// a leaf: it knows nothing about sessions, strategies, throttling, email or organizations. It
// returns what a ceremony proved (the user, the credential, the flags, whether the counter went
// backwards) and the caller decides what that means.
//
// It imports the standard library and go-webauthn only; a test fails if that changes.
package passkey

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// The kinds of ceremony state kept between the two calls of a ceremony.
const (
	KindRegister = "passkey-register"
	KindLogin    = "passkey-login"
)

// CeremonyTTL is how long the state of a ceremony waits for its second call.
const CeremonyTTL = 5 * time.Minute

// Config is the relying party: the domain passkeys are bound to, its display name and the origins a
// ceremony may run from. The id is permanent in practice: a credential belongs to the id it was
// registered under.
type Config struct {
	RPID   string
	RPName string
	// Origins are web origins (https://app.example.com) and native app origins
	// (android:apk-key-hash:<hash>). A web origin's host is the RPID or a subdomain of it.
	Origins []string
	// AllowLoopbackHTTP lets http://localhost and other loopback origins stand (a development server).
	AllowLoopbackHTTP bool
}

// Validate says what is wrong with the configuration, or nil.
func (c Config) Validate() error {
	if strings.TrimSpace(c.RPID) == "" {
		return errors.New("passkey: rp_id is empty")
	}
	if len(c.Origins) == 0 {
		return errors.New("passkey: no origin is listed")
	}
	for _, o := range c.Origins {
		if strings.HasPrefix(o, "android:apk-key-hash:") && len(o) > len("android:apk-key-hash:") {
			continue
		}
		u, err := url.Parse(o)
		if err != nil || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("passkey: origin %q is not an origin", o)
		}
		if u.Scheme != "https" && (u.Scheme != "http" || !c.AllowLoopbackHTTP || !loopback(u.Hostname())) {
			return fmt.Errorf("passkey: origin %q is not https", o)
		}
		if h := u.Hostname(); h != c.RPID && !strings.HasSuffix(h, "."+c.RPID) {
			return fmt.Errorf("passkey: the host of origin %q is not %q or a subdomain of it", o, c.RPID)
		}
	}
	return nil
}

func loopback(h string) bool {
	return h == "localhost" || strings.HasSuffix(h, ".localhost") || h == "127.0.0.1" || h == "::1"
}

// Credential is a registered passkey. Everything in it is public.
type Credential struct {
	ID           string // backd's id
	UserID       string
	CredentialID []byte // the authenticator's id
	PublicKey    []byte // COSE key
	// RPID is the relying party id the credential was registered under.
	RPID       string
	SignCount  uint32
	Transports []string
	AAGUID     []byte
	// BackupEligible says the passkey syncs between devices; BackupState whether it has been synced.
	BackupEligible bool
	BackupState    bool
	// CloneSuspected is set when a counter went backwards.
	CloneSuspected bool
	Name           string
	CreatedAt      time.Time
	LastUsedAt     time.Time
}

// Update is a change to a stored credential; nil fields stay as they are.
type Update struct {
	SignCount      *uint32
	LastUsedAt     *time.Time
	BackupState    *bool
	CloneSuspected *bool
	Name           *string
}

// Store keeps credentials and the opaque handle each user has. Eight methods, implemented by the
// MongoDB store and an in-memory one.
type Store interface {
	Add(ctx context.Context, c Credential) error
	// ByCredentialID returns ErrUnknownCredential when there is none.
	ByCredentialID(ctx context.Context, id []byte) (Credential, error)
	ListByUser(ctx context.Context, userID string) ([]Credential, error)
	Update(ctx context.Context, credentialID []byte, u Update) error
	Delete(ctx context.Context, userID, id string) error
	CountByUser(ctx context.Context, userID string) (int, error)
	// UserHandle returns the user's handle (32 random bytes), making it the first time.
	UserHandle(ctx context.Context, userID string) ([]byte, error)
	// UserByHandle returns ErrUnknownCredential when no user has that handle.
	UserByHandle(ctx context.Context, handle []byte) (string, error)
}

// Ceremonies keeps the library's session data between the two calls of a ceremony.
type Ceremonies interface {
	Put(ctx context.Context, kind string, data []byte, userID string, ttl time.Duration) (id string, err error)
	// Claim returns the data once: ErrCeremony for an unknown, used, expired or other-kind id.
	Claim(ctx context.Context, id, kind string) (data []byte, userID string, err error)
}

// The errors of the package. A verification failure is an *Error whose Reason says why, for the
// audit log, and which is ErrInvalid: callers answer all of them alike.
var (
	ErrInvalid           = errors.New("passkey: the ceremony did not verify")
	ErrUnknownCredential = errors.New("passkey: unknown credential")
	ErrNoCredentials     = errors.New("passkey: the user has no passkeys")
	ErrCeremony          = errors.New("passkey: the ceremony is unknown, used or expired")
)

// Error is a ceremony that did not verify.
type Error struct{ Reason string }

func (e *Error) Error() string        { return "passkey: " + e.Reason }
func (e *Error) Is(target error) bool { return target == ErrInvalid }

// UserInfo is who a credential is registered for.
type UserInfo struct {
	ID          string
	Name        string // shown by the authenticator, such as the email
	DisplayName string
}

// Service runs the ceremonies.
type Service struct {
	cfg        Config
	store      Store
	ceremonies Ceremonies
	now        func() time.Time
	web        *webauthn.WebAuthn
}

// New builds the service from a valid configuration.
func New(cfg Config, store Store, ceremonies Ceremonies, now func() time.Time) (*Service, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	name := cfg.RPName
	if name == "" {
		name = cfg.RPID
	}
	web, err := webauthn.New(&webauthn.Config{
		RPID: cfg.RPID, RPDisplayName: name, RPOrigins: cfg.Origins,
		AttestationPreference: protocol.PreferNoAttestation,
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: CeremonyTTL, TimeoutUVD: CeremonyTTL},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: CeremonyTTL, TimeoutUVD: CeremonyTTL},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("passkey: %w", err)
	}
	return &Service{cfg: cfg, store: store, ceremonies: ceremonies, now: now, web: web}, nil
}

// user adapts a backd user to the library's interface.
type user struct {
	handle      []byte
	name        string
	displayName string
	creds       []webauthn.Credential
}

func (u user) WebAuthnID() []byte                         { return u.handle }
func (u user) WebAuthnName() string                       { return u.name }
func (u user) WebAuthnDisplayName() string                { return u.displayName }
func (u user) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func toLibrary(c Credential) webauthn.Credential {
	transports := make([]protocol.AuthenticatorTransport, len(c.Transports))
	for i, t := range c.Transports {
		transports[i] = protocol.AuthenticatorTransport(t)
	}
	return webauthn.Credential{
		ID: c.CredentialID, PublicKey: c.PublicKey, Transport: transports,
		Flags:         webauthn.CredentialFlags{UserPresent: true, UserVerified: true, BackupEligible: c.BackupEligible, BackupState: c.BackupState},
		Authenticator: webauthn.Authenticator{AAGUID: c.AAGUID, SignCount: c.SignCount},
	}
}

func (s *Service) userFor(ctx context.Context, info UserInfo) (user, []Credential, error) {
	handle, err := s.store.UserHandle(ctx, info.ID)
	if err != nil {
		return user{}, nil, err
	}
	stored, err := s.store.ListByUser(ctx, info.ID)
	if err != nil {
		return user{}, nil, err
	}
	u := user{handle: handle, name: info.Name, displayName: info.DisplayName}
	for _, c := range stored {
		u.creds = append(u.creds, toLibrary(c))
	}
	return u, stored, nil
}

// BeginRegistration starts adding a passkey for a user: options are the JSON the browser's
// navigator.credentials.create takes (the "publicKey" member included), and ceremony the id to send
// back with FinishRegistration. Credentials the user already has are excluded; the credential must be
// discoverable (a resident key) and the user verified.
func (s *Service) BeginRegistration(ctx context.Context, info UserInfo) (options []byte, ceremony string, err error) {
	u, stored, err := s.userFor(ctx, info)
	if err != nil {
		return nil, "", err
	}
	exclude := make([]protocol.CredentialDescriptor, len(stored))
	for i, c := range stored {
		exclude[i] = protocol.CredentialDescriptor{Type: protocol.PublicKeyCredentialType, CredentialID: c.CredentialID}
	}
	required := true
	creation, session, err := s.web.BeginRegistration(u,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			RequireResidentKey: &required, ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationRequired,
		}),
		webauthn.WithExclusions(exclude),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
	)
	if err != nil {
		return nil, "", fmt.Errorf("passkey: %w", err)
	}
	return s.put(ctx, KindRegister, info.ID, creation, session)
}

func (s *Service) put(ctx context.Context, kind, userID string, options any, session *webauthn.SessionData) ([]byte, string, error) {
	data, err := json.Marshal(session)
	if err != nil {
		return nil, "", err
	}
	id, err := s.ceremonies.Put(ctx, kind, data, userID, CeremonyTTL)
	if err != nil {
		return nil, "", err
	}
	out, err := json.Marshal(options)
	if err != nil {
		return nil, "", err
	}
	return out, id, nil
}

// FinishRegistration checks the browser's answer (the JSON of the PublicKeyCredential) against the
// ceremony, which must have been begun for userID, and stores the credential under name. Origin, relying party id, challenge and user
// verification are checked by the library; a mismatch is an *Error.
func (s *Service) FinishRegistration(ctx context.Context, ceremony, userID string, body []byte, name string) (Credential, error) {
	data, owner, err := s.ceremonies.Claim(ctx, ceremony, KindRegister)
	if err != nil {
		return Credential{}, err
	}
	// The ceremony was begun for someone: only they can finish it.
	if owner != userID {
		return Credential{}, ErrCeremony
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(data, &session); err != nil {
		return Credential{}, ErrCeremony
	}
	handle, err := s.store.UserHandle(ctx, userID)
	if err != nil {
		return Credential{}, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(body)
	if err != nil {
		return Credential{}, &Error{Reason: "the registration is not a credential: " + err.Error()}
	}
	cred, err := s.web.CreateCredential(user{handle: handle}, session, parsed)
	if err != nil {
		return Credential{}, &Error{Reason: err.Error()}
	}
	if !cred.Flags.UserVerified {
		return Credential{}, &Error{Reason: "the user was not verified"}
	}
	id, err := newID()
	if err != nil {
		return Credential{}, err
	}
	transports := make([]string, len(cred.Transport))
	for i, t := range cred.Transport {
		transports[i] = string(t)
	}
	c := Credential{
		ID: id, UserID: userID, CredentialID: cred.ID, PublicKey: cred.PublicKey, RPID: s.cfg.RPID,
		SignCount: cred.Authenticator.SignCount, Transports: transports, AAGUID: cred.Authenticator.AAGUID,
		BackupEligible: cred.Flags.BackupEligible, BackupState: cred.Flags.BackupState,
		Name: name, CreatedAt: s.now(),
	}
	if err := s.store.Add(ctx, c); err != nil {
		return Credential{}, err
	}
	return c, nil
}

// BeginLogin starts a sign-in. With userID empty it is discoverable: the browser offers the passkeys
// it holds for the relying party and the answer says who the user is. With a userID only that
// user's credentials are allowed (ErrNoCredentials when they have none).
func (s *Service) BeginLogin(ctx context.Context, userID string) (options []byte, ceremony string, err error) {
	uv := webauthn.WithUserVerification(protocol.VerificationRequired)
	if userID == "" {
		assertion, session, err := s.web.BeginDiscoverableLogin(uv)
		if err != nil {
			return nil, "", fmt.Errorf("passkey: %w", err)
		}
		return s.put(ctx, KindLogin, "", assertion, session)
	}
	u, stored, err := s.userFor(ctx, UserInfo{ID: userID})
	if err != nil {
		return nil, "", err
	}
	if len(stored) == 0 {
		return nil, "", ErrNoCredentials
	}
	assertion, session, err := s.web.BeginLogin(u, uv)
	if err != nil {
		return nil, "", fmt.Errorf("passkey: %w", err)
	}
	return s.put(ctx, KindLogin, userID, assertion, session)
}

// LoginResult is what a sign-in proved.
type LoginResult struct {
	UserID     string
	Credential Credential
	// UserVerified, BackupEligible and BackupState are the flags of this assertion.
	UserVerified   bool
	BackupEligible bool
	BackupState    bool
	// CounterRegressed says the authenticator's counter did not go forward though it had counted
	// before: the credential may be cloned. The sign-in still verified; the stored counter is left
	// and the credential is flagged (CloneSuspected), so the caller can decide. A counter that
	// stays 0 (authenticators that do not count) is never a regression.
	CounterRegressed bool
}

// FinishLogin checks the browser's answer (the JSON of the assertion). Origin, relying party id,
// challenge, user verification and the signature are checked; anything that does not verify is an
// *Error, and a credential nobody registered is ErrUnknownCredential.
func (s *Service) FinishLogin(ctx context.Context, ceremony string, body []byte) (LoginResult, error) {
	data, userID, err := s.ceremonies.Claim(ctx, ceremony, KindLogin)
	if err != nil {
		return LoginResult{}, err
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(data, &session); err != nil {
		return LoginResult{}, ErrCeremony
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(body)
	if err != nil {
		return LoginResult{}, &Error{Reason: "the assertion is not a credential: " + err.Error()}
	}
	// The credential the answer names must be one we hold, and its user the one the ceremony is for.
	stored, err := s.store.ByCredentialID(ctx, parsed.RawID)
	if err != nil {
		return LoginResult{}, err
	}
	if userID != "" && stored.UserID != userID {
		return LoginResult{}, &Error{Reason: "the credential is another user's"}
	}
	handle, err := s.store.UserHandle(ctx, stored.UserID)
	if err != nil {
		return LoginResult{}, err
	}
	if len(parsed.Response.UserHandle) > 0 && string(parsed.Response.UserHandle) != string(handle) {
		return LoginResult{}, &Error{Reason: "the user handle is not the credential's user"}
	}
	list, err := s.store.ListByUser(ctx, stored.UserID)
	if err != nil {
		return LoginResult{}, err
	}
	u := user{handle: handle}
	for _, c := range list {
		u.creds = append(u.creds, toLibrary(c))
	}
	var cred *webauthn.Credential
	if userID == "" {
		_, cred, err = s.web.ValidatePasskeyLogin(func(rawID, userHandle []byte) (webauthn.User, error) { return u, nil }, session, parsed)
	} else {
		cred, err = s.web.ValidateLogin(u, session, parsed)
	}
	if err != nil {
		return LoginResult{}, &Error{Reason: err.Error()}
	}
	flags := parsed.Response.AuthenticatorData.Flags
	res := LoginResult{
		UserID: stored.UserID, UserVerified: flags.HasUserVerified(), BackupEligible: flags.HasBackupEligible(), BackupState: flags.HasBackupState(),
		CounterRegressed: cred.Authenticator.CloneWarning,
	}
	now := s.now()
	up := Update{LastUsedAt: &now, BackupState: &res.BackupState}
	if res.CounterRegressed {
		yes := true
		up.CloneSuspected = &yes
		stored.CloneSuspected = true
	} else {
		count := cred.Authenticator.SignCount
		up.SignCount = &count
		stored.SignCount = count
	}
	if err := s.store.Update(ctx, stored.CredentialID, up); err != nil {
		return LoginResult{}, err
	}
	stored.LastUsedAt, stored.BackupState = now, res.BackupState
	res.Credential = stored
	return res, nil
}

func newID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)), nil
}
