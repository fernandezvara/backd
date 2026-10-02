// Package auth holds backd's identity model: users, their sign-in
// identities and password handling. It is storage-neutral; MongoDB
// specifics live behind Store in internal/mongodb.
//
// A user carries no credentials. Each way of signing in is an Identity
// (provider + subject), so adding login providers later only adds
// identities: a password user has a "password" identity, and a user who
// signs in only through another provider needs none.
package auth

import (
	"context"
	"errors"
	"github.com/fernandezvara/backd/internal/registry"
	"time"
)

// ProviderPassword is the identity provider for email + password sign-in.
// Its subject is the user's id, so it doesn't depend on the email.
const ProviderPassword = "password"

// User is a realm user. It holds only identity data; profiles belong in
// application collections.
type User struct {
	ID            string
	Email         string // normalized: trimmed, lowercased
	EmailVerified bool
	// PendingEmail is an address the user asked to change to and hasn't
	// confirmed yet; PreviousEmail is the one before the last change, kept
	// so the old address can be told and can undo it.
	PendingEmail  string
	PreviousEmail string
	Roles         []string
	Disabled      bool
	// Locale is the user's language: one the realm lists (see
	// RealmSettings.Languages). Empty for users from before languages existed;
	// those use the realm's default.
	Locale string
	// AdminNetworks restrict the user's admin API requests; LoginNetworks
	// their login and every request with their session. Empty: no
	// restriction beyond the realm's.
	AdminNetworks registry.Networks
	LoginNetworks registry.Networks
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Identity is one way a user signs in.
type Identity struct {
	ID           string
	UserID       string
	Provider     string
	Subject      string // unique per provider
	PasswordHash string // ProviderPassword only: argon2id, PHC string format
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// UserUpdate changes the set fields of a user.
type UserUpdate struct {
	// Email changes the address (ErrEmailTaken when another user has it).
	Email         *string
	PendingEmail  *string // set; an empty string clears it
	PreviousEmail *string // set; an empty string clears it
	EmailVerified *bool
	Disabled      *bool
	Locale        *string
	AdminNetworks *registry.Networks // set (empty clears)
	LoginNetworks *registry.Networks // set (empty clears)
}

var (
	ErrNotFound   = errors.New("user not found")
	ErrEmailTaken = errors.New("email is already registered")
	// ErrBusy means no password-hashing slot freed up before the deadline.
	ErrBusy = errors.New("too many password operations in progress; try again later")
)

// Store persists users, identities and sessions of one realm.
type Store interface {
	// CreateUser inserts a user; ErrEmailTaken if the email exists.
	CreateUser(ctx context.Context, u User) error
	UserByEmail(ctx context.Context, email string) (User, error)
	// UserByID returns the user with that id; ErrNotFound if none.
	UserByID(ctx context.Context, id string) (User, error)
	ListUsers(ctx context.Context) ([]User, error)
	// AddRoles adds roles the user doesn't have yet, setting UpdatedAt.
	AddRoles(ctx context.Context, userID string, roles []string, now time.Time) error
	// RemoveRole removes a role from the user, setting UpdatedAt.
	RemoveRole(ctx context.Context, userID, role string, now time.Time) error
	// UpdateUser applies upd and sets UpdatedAt to now.
	UpdateUser(ctx context.Context, id string, upd UserUpdate, now time.Time) error
	// ListUnverifiedUsers returns up to limit users whose address is still
	// unverified and who were created before the given time, oldest first.
	ListUnverifiedUsers(ctx context.Context, createdBefore time.Time, limit int) ([]User, error)
	// DeleteUser removes the user with their identities and sessions.
	DeleteUser(ctx context.Context, id string) error
	// PutIdentity creates the identity, or, when one with the same provider
	// and subject exists, updates its credentials and UpdatedAt (keeping its
	// ID and CreatedAt).
	PutIdentity(ctx context.Context, id Identity) error
	Identity(ctx context.Context, provider, subject string) (Identity, error)
	// DeleteSessions revokes every session of the user.
	DeleteSessions(ctx context.Context, userID string) error

	CreateSession(ctx context.Context, s Session) error
	// SessionByTokenHash returns the session and its user; ErrNotFound if
	// either doesn't exist.
	SessionByTokenHash(ctx context.Context, hash string) (Session, User, error)
	// TouchSession records a use of the session and its new expiry.
	TouchSession(ctx context.Context, id string, lastUsed, expires time.Time) error
	// ListSessions returns the user's sessions, newest first.
	ListSessions(ctx context.Context, userID string) ([]Session, error)
	// DeleteSession revokes one session of the user; ErrNotFound if the
	// user has no session with that id.
	DeleteSession(ctx context.Context, userID, id string) error
	// DeleteOtherSessions revokes every session of the user except keepID.
	DeleteOtherSessions(ctx context.Context, userID, keepID string) error

	// CreateAPIKey stores a key; ErrKeyNameTaken if the name exists.
	CreateAPIKey(ctx context.Context, k APIKey) error
	// APIKeyByHash returns the key with that hash; ErrKeyNotFound if none.
	APIKeyByHash(ctx context.Context, hash string) (APIKey, error)
	ListAPIKeys(ctx context.Context) ([]APIKey, error)
	// DeleteAPIKey removes the named key; ErrKeyNotFound if none.
	DeleteAPIKey(ctx context.Context, name string) error
	// TouchAPIKey records a use of the key.
	TouchAPIKey(ctx context.Context, hash string, at time.Time) error

	// AppendAudit adds a record to the audit trail, which is never
	// modified: records are only removed when they expire.
	AppendAudit(ctx context.Context, rec AuditRecord) error
	// ListAudit returns records matching f, newest first, and whether
	// more follow.
	ListAudit(ctx context.Context, f AuditFilter) ([]AuditRecord, bool, error)

	CreateInvitation(ctx context.Context, inv Invitation) error
	// ClaimInvitation atomically deletes and returns the unexpired
	// invitation with that token hash; ErrNotFound if none.
	ClaimInvitation(ctx context.Context, hash string, now time.Time) (Invitation, error)
	// ClaimInvitationByID is ClaimInvitation for an invitation known by its
	// id (one sent by email: nobody holds its own token).
	ClaimInvitationByID(ctx context.Context, id string, now time.Time) (Invitation, error)
	// ListInvitations returns all invitations, newest first.
	ListInvitations(ctx context.Context) ([]Invitation, error)
	// DeleteInvitation removes an invitation by id; ErrNotFound if none.
	DeleteInvitation(ctx context.Context, id string) error

	// LoginAttempts returns the failure counter of key (zero if none).
	LoginAttempts(ctx context.Context, key string) (Attempts, error)
	// RecordLoginFailure increments key's counter atomically, setting its
	// last failure to at; the counter is deleted at expires.
	RecordLoginFailure(ctx context.Context, key string, at, expires time.Time) error
	// ClearLoginAttempts deletes key's counter.
	ClearLoginAttempts(ctx context.Context, key string) error

	// IncrementCounter atomically increments key's counter (creating it,
	// or restarting it at 1 if its window has already ended) and returns
	// the new count and when it resets. The same generic "N events per
	// window" primitive login throttling uses, reused for function rate
	// limits (roadmap F9), which is why it lives in the same collection.
	IncrementCounter(ctx context.Context, key string, at, expires time.Time) (count int, resetAt time.Time, err error)

	// RecordInvocation appends a function call record (roadmap F10).
	// backd never updates or deletes them; MongoDB removes them when
	// they expire.
	RecordInvocation(ctx context.Context, r InvocationRecord) error
	// ListInvocations returns records matching f, newest first, and
	// whether more follow.
	ListInvocations(ctx context.Context, f InvocationFilter) ([]InvocationRecord, bool, error)

	// EnqueueJob stores a new async job (roadmap F11), queued for a worker.
	EnqueueJob(ctx context.Context, j Job) error
	// ClaimJob atomically claims the oldest job that's never been claimed,
	// or whose lease has expired, leasing it to workerID until at plus
	// the job's own timeout plus margin. found is false when there is
	// none to claim.
	ClaimJob(ctx context.Context, workerID string, at time.Time, margin time.Duration) (j Job, found bool, err error)
	// RetryJob counts a failed attempt and queues the job again, not to be
	// claimed before notBefore (it is still the same job: same id, same input).
	RetryJob(ctx context.Context, id string, notBefore time.Time) error
	// CompleteJob records a job's result; expiresAt starts its retention
	// countdown (MongoDB removes it once past).
	CompleteJob(ctx context.Context, id string, result JobResult, completedAt, expiresAt time.Time) error
	// ListJobs returns the jobs matching f, newest first, and whether
	// more follow.
	ListJobs(ctx context.Context, f JobFilter) ([]Job, bool, error)
	// GetJob returns one job by id; found is false if there is none.
	GetJob(ctx context.Context, id string) (j Job, found bool, err error)

	// CreateEmailToken stores a token's hash with its purpose and user.
	CreateEmailToken(ctx context.Context, t EmailToken) error
	// GetEmailToken returns the token with this hash whatever its state, or
	// ErrInvalidToken when there is none.
	GetEmailToken(ctx context.Context, hash string) (EmailToken, error)
	// RedeemEmailToken atomically marks the token with this hash and purpose
	// used, if it is unused and unexpired at now, and returns it; otherwise
	// ErrInvalidToken.
	RedeemEmailToken(ctx context.Context, hash, purpose string, now time.Time) (EmailToken, error)
	// InvalidateEmailTokens marks used every unused token of the user and
	// purpose.
	InvalidateEmailTokens(ctx context.Context, userID, purpose string, now time.Time) error

	// ClaimIdempotency (roadmap F12) atomically inserts rec if its id
	// isn't already used, or returns the existing record (running or
	// done) if it is. claimed is false in the second case.
	ClaimIdempotency(ctx context.Context, rec IdempotencyRecord) (existing IdempotencyRecord, claimed bool, err error)
	// CompleteIdempotency stores a claimed key's outcome: result for a
	// sync call, jobID for async (mutually exclusive). expiresAt starts
	// its retention countdown.
	CompleteIdempotency(ctx context.Context, id string, result *JobResult, jobID string, completedAt, expiresAt time.Time) error
	// ReleaseIdempotency removes a claim that shouldn't be remembered
	// (an infrastructure failure, not a real outcome), so a retry can
	// go through cleanly instead of being stuck "running" forever.
	ReleaseIdempotency(ctx context.Context, id string) error

	// UpsertSecret creates or replaces the secret at (database, name),
	// keeping its original CreatedAt on an update.
	UpsertSecret(ctx context.Context, s Secret) error
	// SecretByScope returns one secret; ErrSecretNotFound if none.
	SecretByScope(ctx context.Context, database, name string) (Secret, error)
	// ListSecrets returns every secret's metadata, sorted by database
	// (realm scope, "", first) then name.
	ListSecrets(ctx context.Context) ([]Secret, error)
	// DeleteSecret removes one; ErrSecretNotFound if none.
	DeleteSecret(ctx context.Context, database, name string) error
}
