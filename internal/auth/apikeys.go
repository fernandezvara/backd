package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/registry"
)

// APIKeyPrefix starts every API key; see SessionTokenPrefix.
const APIKeyPrefix = "bdk_"

// displayLen is how many characters of a key are kept to recognize it.
const displayLen = len(APIKeyPrefix) + 6

var (
	ErrKeyNameTaken = errors.New("an API key with this name already exists")
	ErrKeyNotFound  = errors.New("API key not found")
)

// KeyRole is what an API key may do.
type KeyRole string

const (
	// KeyRoleData keys reach data routes, bypassing rules, and may act on
	// behalf of a user. The default: what services need.
	KeyRoleData KeyRole = "data"
	// KeyRoleAdmin keys also reach the admin API (/_admin): users, roles,
	// invitations and API keys. For management tooling only.
	KeyRoleAdmin KeyRole = "admin"
)

// ParseKeyRole returns the role named s ("" means data).
func ParseKeyRole(s string) (KeyRole, error) {
	switch KeyRole(s) {
	case "", KeyRoleData:
		return KeyRoleData, nil
	case KeyRoleAdmin:
		return KeyRoleAdmin, nil
	}
	return "", fmt.Errorf("invalid API key role %q: must be data or admin", s)
}

// KeyOptions are the settings of a new API key.
type KeyOptions struct {
	TTL      time.Duration     // 0: never expires
	Role     KeyRole           // "" means data
	Networks registry.Networks // where the key may be used from; empty: anywhere
}

// APIKey is a realm API key for server-side callers. Only a hash of the
// key is stored.
type APIKey struct {
	Name       string
	Role       KeyRole           // "" (stored before roles existed) counts as data
	Networks   registry.Networks // where the key may be used from; empty: anywhere
	Prefix     string            // first characters of the key, for recognizing it
	Hash       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time // nil: never expires
}

// Expired reports whether the key has expired at now.
func (k APIKey) Expired(now time.Time) bool { return k.ExpiresAt != nil && !now.Before(*k.ExpiresAt) }

// IsAdmin reports whether the key may use the admin API.
func (k APIKey) IsAdmin() bool { return k.Role == KeyRoleAdmin }

// CreateAPIKey creates a key named name with opts. The key itself is
// returned only here.
func (s *Users) CreateAPIKey(ctx context.Context, name string, opts KeyOptions) (APIKey, string, error) {
	ttl := opts.TTL
	role, err := ParseKeyRole(string(opts.Role))
	if err != nil {
		return APIKey{}, "", err
	}
	if !registry.ValidName(name) {
		return APIKey{}, "", fmt.Errorf("invalid API key name %q: must be lowercase letters and digits, optionally separated by single '-' or '_'", name)
	}
	if ttl < 0 {
		return APIKey{}, "", errors.New("expiry must be positive")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return APIKey{}, "", err
	}
	key := APIKeyPrefix + base64.RawURLEncoding.EncodeToString(b)
	now := s.now()
	k := APIKey{Name: name, Role: role, Networks: opts.Networks, Prefix: key[:displayLen], Hash: HashToken(key), CreatedAt: now}
	if ttl > 0 {
		exp := now.Add(ttl)
		k.ExpiresAt = &exp
	}
	if err := s.Store.CreateAPIKey(ctx, k); err != nil {
		return APIKey{}, "", err
	}
	details := map[string]any{"role": string(k.Role), "networks": k.Networks.Strings()}
	if k.ExpiresAt != nil {
		details["expires_at"] = k.ExpiresAt.Format(time.RFC3339)
	}
	s.Audit(ctx, AuditAPIKeyCreate, "key:"+k.Name, details)
	return k, key, nil
}

// ListAPIKeys returns the realm's API keys, sorted by name.
func (s *Users) ListAPIKeys(ctx context.Context) ([]APIKey, error) { return s.Store.ListAPIKeys(ctx) }

// RevokeAPIKey deletes a key; it stops working at once.
func (s *Users) RevokeAPIKey(ctx context.Context, name string) error {
	if err := s.Store.DeleteAPIKey(ctx, name); err != nil {
		return err
	}
	s.Audit(ctx, AuditAPIKeyRevoke, "key:"+name, nil)
	return nil
}

// AuthenticateKey resolves an API key. It records the use at most once a
// minute.
func (s *Users) AuthenticateKey(ctx context.Context, key string) (APIKey, error) {
	if !strings.HasPrefix(key, APIKeyPrefix) {
		return APIKey{}, ErrUnauthenticated
	}
	k, err := s.Store.APIKeyByHash(ctx, HashToken(key))
	if errors.Is(err, ErrKeyNotFound) {
		return APIKey{}, ErrUnauthenticated
	}
	if err != nil {
		return APIKey{}, err
	}
	now := s.now()
	if k.Expired(now) {
		return APIKey{}, ErrUnauthenticated
	}
	if k.LastUsedAt == nil || now.Sub(*k.LastUsedAt) >= touchInterval {
		if err := s.Store.TouchAPIKey(ctx, k.Hash, now); err != nil {
			return APIKey{}, err
		}
		k.LastUsedAt = &now
	}
	return k, nil
}

// Caller is whoever makes a request to a realm with auth enabled: a user
// with a session, a server-side caller with an API key, an API key acting
// on behalf of a user (both set), or anonymous (neither). A function
// calling back (Func set) acts as the caller that invoked it, or, with
// admin access, as itself.
type Caller struct {
	User *Principal  // the user whose rules apply
	Key  *APIKey     // set for API key callers
	Func *FuncCaller // set for a function's callbacks
}

// BypassesRules reports whether the caller has full access: an API key
// that isn't acting on behalf of a user, or a function's admin access.
func (c Caller) BypassesRules() bool {
	return (c.Key != nil && c.User == nil) || (c.Func != nil && c.Func.Admin)
}

// Subject is who the caller acts as, for ownership fields: "user:<id>",
// "key:<name>" or "anonymous".
func (c Caller) Subject() string {
	switch {
	case c.Func != nil && c.Func.Admin:
		return "func:" + c.Func.Name
	case c.User != nil:
		return "user:" + c.User.User.ID
	case c.Key != nil:
		return "key:" + c.Key.Name
	}
	return "anonymous"
}

// Actor names the caller for logs: the subject, and for an API key acting
// on behalf of a user, "key:<name> as user:<id>".
func (c Caller) Actor() string {
	actor := c.Subject()
	if c.Key != nil && c.User != nil {
		actor = "key:" + c.Key.Name + " as " + actor
	}
	if c.Func != nil && !c.Func.Admin {
		actor = "func:" + c.Func.Name + " as " + actor
	}
	return actor
}

// ErrNotAllowedOnBehalf means a caller other than an API key tried to act
// on behalf of a user.
var ErrNotAllowedOnBehalf = errors.New("only API keys can act on behalf of a user")

// ErrUserDisabled means the user an API key acts for is disabled.
var ErrUserDisabled = errors.New("user is disabled")

// OnBehalfOf makes an API key caller act as the user with id: that user's
// rules and ownership apply. It returns ErrNotFound for unknown users.
func (s *Users) OnBehalfOf(ctx context.Context, c Caller, userID string) (Caller, error) {
	if c.Key == nil || c.User != nil {
		return Caller{}, ErrNotAllowedOnBehalf
	}
	u, err := s.Store.UserByID(ctx, userID)
	if err != nil {
		return Caller{}, err
	}
	if u.Disabled {
		return Caller{}, ErrUserDisabled
	}
	c.User = &Principal{User: u}
	return c, nil
}

// Identify resolves a bearer credential (a session token or an API key)
// to its caller. An empty credential is the anonymous caller.
func (s *Users) Identify(ctx context.Context, credential string) (Caller, error) {
	switch {
	case credential == "":
		return Caller{}, nil
	case strings.HasPrefix(credential, APIKeyPrefix):
		k, err := s.AuthenticateKey(ctx, credential)
		if err != nil {
			return Caller{}, err
		}
		return Caller{Key: &k}, nil
	default:
		p, err := s.Authenticate(ctx, credential)
		if err != nil {
			return Caller{}, err
		}
		return Caller{User: &p}, nil
	}
}
