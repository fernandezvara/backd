package auth

import (
	"context"
	"log/slog"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/registry"
)

// Audit actions: security-sensitive changes and events, recorded in the
// realm's append-only audit trail.
const (
	AuditUserCreate      = "user.create"   // by an administrator (or bootstrap)
	AuditUserSignup      = "user.signup"   // self-service sign-up
	AuditUserPassword    = "user.password" // set by an administrator
	AuditPasswordChange  = "user.password_change"
	AuditUserVerifyEmail = "user.verify_email"      // details: verified
	AuditPasswordReset   = "user.password_reset"    // by the link in an email; details: verified_address
	AuditUserPurged      = "user.purged_unverified" // details: count; never addresses
	AuditUserDisable     = "user.disable"
	AuditUserEnable      = "user.enable"
	AuditUserDelete      = "user.delete"
	AuditAccountDelete   = "user.delete_account" // self-service: the account is deactivated
	AuditUserErased      = "user.erased"         // details: job_id and counts, or needs_attention
	AuditUserNetworks    = "user.networks"       // details: admin_networks, login_networks
	AuditSessionRevoke   = "session.revoke"      // by an administrator; details: session
	AuditRoleAdd         = "role.add"            // details: role
	AuditRoleRemove      = "role.remove"         // details: role
	AuditAPIKeyCreate    = "apikey.create"       // details: role, expires_at, networks
	AuditAPIKeyRevoke    = "apikey.revoke"
	AuditEmailChanged    = "user.email_changed"         // details: by (self or admin)
	AuditEmailReverted   = "user.email_change_reverted" // the old address undid a change
	AuditInviteSent      = "invitation.sent"            // details: expires_at
	AuditInviteCreate    = "invitation.create"          // details: bound_to_email, expires_at
	AuditInviteRevoke    = "invitation.revoke"
	AuditAdminLogin      = "admin.login"   // login of a user holding an admin role
	AuditAdminRefused    = "admin.refused" // details: reason
	AuditBootstrap       = "realm.bootstrap"
	// Sign-in methods; the target is the user, details: provider, and by
	// (self or admin) for an unlink.
	AuditIdentityLinked   = "identity.linked"
	AuditIdentityUnlinked = "identity.unlinked"
	// Apple revoked a user's tokens when their account went away or Apple was unlinked;
	// never the token itself.
	AuditAppleRevoked = "identity.apple_revoked"
	// A provider sign-in the account's state refused; details: provider, reason
	// (disabled, network, erased, …).
	AuditSignInRefused = "identity.signin_refused"
	// The admin data route (documents written past the collections' rules);
	// the target is database/collection/id, never content.
	AuditDataCreate  = "data.create"
	AuditDataUpdate  = "data.update"
	AuditDataDelete  = "data.delete"
	AuditDataRestore = "data.restore"
	AuditDataPurge   = "data.purge"
	// A schema check started by an administrator; the target is the scope
	// (<database>/<collection>, <database> or the realm), details: collections, limit.
	AuditDataCheck = "data.check"
	// A job cancelled or re-run by an administrator; the target is job:<id>,
	// details: function (and, for a re-run, the new job's id).
	AuditJobCancel = "job.cancel"
	AuditJobRerun  = "job.rerun"
	// A scheduled function paused or resumed by an administrator; the target is
	// schedule:<database>/<function>.
	AuditSchedulePause  = "schedule.pause"
	AuditScheduleResume = "schedule.resume"
	// An administrator ran `backd storage check`; details: ok, provider, bucket.
	AuditStorageCheck = "storage.check"
	// An administrator ran `backd storage reconcile`; details: delete, orphans, deleted.
	AuditStorageReconcile = "storage.reconcile"
	// An administrator ran `backd files regenerate`; details: version, missing_only, job_id.
	AuditVersionsRegenerate = "files.versions.regenerate"
	AuditSecretSet          = "secret.set"        // details: database ("": realm scope); never the value
	AuditSecretDelete       = "secret.delete"     // details: database
	AuditSecretsRotated     = "secret.rotate_key" // by backd secret rotate-key; details: count
)

// Actors that aren't a credential.
const (
	// ActorConfig applies realm.yaml's role and network seeds.
	ActorConfig = "config:realm.yaml"
	// ActorBootstrap is `backd bootstrap`.
	ActorBootstrap = "cli:bootstrap"
	// ActorSystem is backd itself, in the background (purging unverified accounts).
	ActorSystem = "system"
	// ActorAnonymous is a caller without credentials.
	ActorAnonymous = "anonymous"
)

// AuditRecord is one entry of the audit trail. It names users by id, keys
// by name and invitations by id, and never holds secrets: no passwords or
// hashes, tokens, keys, emails or request bodies.
type AuditRecord struct {
	ID        string
	At        time.Time
	ExpiresAt time.Time // removed after this (the realm's retention)
	Action    string
	Actor     string // "user:<id>", "key:<name>", "anonymous", or an Actor* constant
	Target    string // "user:<id>", "key:<name>", "invitation:<id>", or ""
	Details   map[string]any
	RequestID string
	ClientIP  string
}

// AuditFilter selects audit records. Zero fields match everything.
type AuditFilter struct {
	Action, Actor, Target string
	Since, Until          time.Time // At >= Since, At < Until
	Limit, Skip           int
}

// AuditSource says who is acting and from where. The HTTP layer puts it in
// the request context; commands set their own.
type AuditSource struct {
	Actor, RequestID, ClientIP string
}

type auditSourceKey struct{}

// WithAuditSource returns a context whose audit records take their actor,
// request id and client address from source, called when a record is
// written (the actor may be known only after authentication).
func WithAuditSource(ctx context.Context, source func() AuditSource) context.Context {
	return context.WithValue(ctx, auditSourceKey{}, source)
}

func auditSource(ctx context.Context) AuditSource {
	var src AuditSource
	if fn, ok := ctx.Value(auditSourceKey{}).(func() AuditSource); ok {
		src = fn()
	}
	if src.Actor == "" {
		src.Actor = ActorAnonymous
	}
	return src
}

func (s *Users) auditRetention() time.Duration {
	if s.Settings.AuditRetention > 0 {
		return s.Settings.AuditRetention
	}
	return registry.DefaultAuditRetention
}

// Audit appends a record to the realm's audit trail and writes it to the
// log. A record that can't be stored is logged as an error; the action it
// describes has already happened, so it isn't undone.
func (s *Users) Audit(ctx context.Context, action, target string, details map[string]any) {
	s.AuditAs(ctx, "", action, target, details)
}

// AuditAs is Audit with an explicit actor (when not empty), for events
// whose actor isn't the caller yet, such as a login.
func (s *Users) AuditAs(ctx context.Context, actor, action, target string, details map[string]any) {
	src := auditSource(ctx)
	if actor != "" {
		src.Actor = actor
	}
	now := s.now()
	rec := AuditRecord{
		ID: xid.New().String(), At: now, ExpiresAt: now.Add(s.auditRetention()),
		Action: action, Actor: src.Actor, Target: target, Details: details,
		RequestID: src.RequestID, ClientIP: src.ClientIP,
	}
	log := s.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	attrs := []any{"action", action, "actor", rec.Actor, "target", target, "request_id", rec.RequestID, "client", rec.ClientIP}
	if s.Realm != "" {
		attrs = append([]any{"realm", s.Realm}, attrs...)
	}
	if len(details) > 0 {
		attrs = append(attrs, "details", details)
	}
	// The trail must outlive the request that caused it.
	if err := s.Store.AppendAudit(context.WithoutCancel(ctx), rec); err != nil {
		log.Error("audit record not stored", append(attrs, "error", err)...)
		return
	}
	log.Info("audit", attrs...)
}

// AuditTrail returns records matching f, newest first, and whether more
// follow.
func (s *Users) AuditTrail(ctx context.Context, f AuditFilter) ([]AuditRecord, bool, error) {
	return s.Store.ListAudit(ctx, f)
}
