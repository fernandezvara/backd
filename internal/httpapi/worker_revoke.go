package httpapi

import (
	"context"
	"log/slog"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/executor"
	"github.com/fernandezvara/backd/internal/registry"
)

// revokeAttempts is how many times a revocation is tried before its job ends as failed.
const revokeAttempts = 8

// runRevoke tells Apple to revoke a refresh token that belonged to a user whose Apple identity
// is gone. It is not a function: a worker does it itself. A failure is tried again after a
// growing wait; once Apple has revoked the token the job forgets it.
func (w *Worker) runRevoke(ctx context.Context, log *slog.Logger, realm string, svc *auth.Users, job auth.Job) {
	r := job.Revoke
	p := svc.Settings.Providers[registry.ProviderApple]
	if r.Token == "" || p == nil || !p.RevokeOnDelete {
		// Nothing to revoke, or the realm no longer revokes (revoke_on_delete: false).
		w.finishRevoke(ctx, log, svc, job, false)
		return
	}
	err := w.revokeApple(ctx, svc, p, r)
	if err != nil {
		w.revokeFailed(ctx, log, svc, job, err)
		return
	}
	w.finishRevoke(ctx, log, svc, job, true)
}

func (w *Worker) revokeApple(ctx context.Context, svc *auth.Users, p *registry.Provider, r *auth.RevokeJob) error {
	token, err := svc.OpenToken(r.Token)
	if err != nil {
		return err
	}
	refs := []registry.SecretRef{{Realm: true, Name: p.PrivateKey}}
	values, missing, err := svc.ResolveSecrets(ctx, refs, "")
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return errSecretMissing(missing)
	}
	clientID := r.ClientID
	if clientID == "" {
		clientID = p.ClientID
	}
	secret, err := w.oauth.AppleClientSecret(p, clientID, values["realm."+p.PrivateKey])
	if err != nil {
		return err
	}
	return w.oauth.Revoke(ctx, p, clientID, secret, token)
}

type errSecretMissing []string

func (e errSecretMissing) Error() string {
	return "the secret of Apple's private key is not set (backd secret set)"
}

// finishRevoke drops the token from the job and completes it; revoked says whether Apple was told.
func (w *Worker) finishRevoke(ctx context.Context, log *slog.Logger, svc *auth.Users, job auth.Job, revoked bool) {
	if err := svc.Store.ClearRevokeToken(ctx, job.ID); err != nil {
		w.revokeFailed(ctx, log, svc, job, err)
		return
	}
	if revoked {
		svc.AuditAs(ctx, auth.ActorSystem, auth.AuditAppleRevoked, "user:"+job.Revoke.UserID, nil)
	}
	status := "ok"
	if !revoked {
		status = "skipped"
	}
	if err := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: status}); err != nil {
		log.Error("complete the revoke job", "error", err)
		return
	}
	w.fns.metrics.JobFinished(svc.Realm, "revoke", status)
	log.Info("apple token handled", "user_id", job.Revoke.UserID, "revoked", revoked)
}

// revokeFailed tries again after a growing wait while attempts are left; then the job ends as
// failed, visibly, and the sealed token stays in it until the job expires.
func (w *Worker) revokeFailed(ctx context.Context, log *slog.Logger, svc *auth.Users, job auth.Job, err error) {
	if failed := job.Failures + 1; failed < revokeAttempts {
		wait := min(time.Minute<<(failed-1), 6*time.Hour)
		log.Warn("revoking an Apple token failed; it will be tried again", "attempt", failed, "of", revokeAttempts, "retry_in", wait.String(), "error", err)
		if rerr := svc.RetryJob(ctx, job.ID, wait); rerr != nil {
			log.Error("queue the retry", "error", rerr)
		} else {
			w.fns.metrics.JobRetried(svc.Realm, "revoke")
		}
		return
	}
	log.Error("revoking an Apple token failed for good", "error", err)
	if cerr := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: executor.StatusError, Message: err.Error()}); cerr != nil {
		log.Error("complete the failed revoke job", "error", cerr)
		return
	}
	w.fns.metrics.JobFinished(svc.Realm, "revoke", executor.StatusError)
}
