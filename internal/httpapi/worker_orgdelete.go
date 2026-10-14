package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/executor"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

const (
	orgDeleteBatch    = 200
	orgDeleteAttempts = 5
)

// runOrgDelete applies on_org_delete to the documents of a deleted organization (and
// on_team_delete to those of a deleted team): `delete` removes them, soft-deleted ones included,
// and queues their files for deletion; `keep` leaves them. Access ended when the organization
// or team was deleted, so this only cleans up. It works in batches of documents that still
// match, so a job that was interrupted, retried or taken over converges to the same result.
func (w *Worker) runOrgDelete(ctx context.Context, log *slog.Logger, realm string, svc *auth.Users, job auth.Job) {
	org, team := job.OrgDelete.Org, job.OrgDelete.Team
	scope := purgeScope{target: "org:" + org, audit: auth.AuditOrgDeleted, reason: "org-deleted", what: "organization",
		applies: func(c *registry.Collection) bool { return c.InOrganizations() && c.OnOrgDelete == registry.OrgDelete },
		remove: func(p storage.OrgPurger, fileFields []string) (int64, []storage.ErasedFile, error) {
			return p.DeleteInOrg(ctx, org, fileFields, orgDeleteBatch)
		}}
	if team != "" {
		scope = purgeScope{target: "org:" + org, audit: auth.AuditTeamDeleted, reason: "team-deleted", what: "team",
			applies: func(c *registry.Collection) bool { return c.InTeams() && c.OnTeamDelete == registry.OrgDelete },
			remove: func(p storage.OrgPurger, fileFields []string) (int64, []storage.ErasedFile, error) {
				return p.DeleteInTeam(ctx, team, fileFields, orgDeleteBatch)
			}}
	}
	counts, err := w.purge(ctx, realm, svc, scope)
	if err != nil {
		if failed := job.Failures + 1; failed < orgDeleteAttempts {
			wait := 30 * time.Second << (failed - 1)
			log.Warn(scope.what+" cleanup attempt failed; it will be tried again", "attempt", failed, "of", orgDeleteAttempts, "retry_in", wait.String(), "error", err)
			if rerr := svc.RetryJob(ctx, job.ID, wait); rerr != nil {
				log.Error("queue the retry", "error", rerr)
			}
			return
		}
		log.Error(scope.what+" cleanup attempts used up; the job fails", "org", org, "team", team, "error", err)
		if cerr := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: executor.StatusError, Message: err.Error()}); cerr != nil {
			log.Error("complete the failed cleanup", "error", cerr)
			return
		}
		w.fns.metrics.JobFinished(svc.Realm, "org", executor.StatusError)
		return
	}
	output, _ := json.Marshal(counts)
	details := map[string]any{"job_id": job.ID, "counts": counts}
	if team != "" {
		details["team"] = team
	}
	svc.AuditAs(ctx, auth.ActorSystem, scope.audit, scope.target, details)
	if err := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: executor.StatusOK, Output: output}); err != nil {
		log.Error("complete the "+scope.what+" cleanup", "error", err)
		return
	}
	w.fns.metrics.JobFinished(svc.Realm, "org", executor.StatusOK)
	log.Info(scope.what+" cleaned up", "org", org, "team", team)
}

// purgeScope says what a cleanup removes: the documents of an organization or of a team.
type purgeScope struct {
	target string // of the audit event
	audit  string // the event recorded when it finishes
	reason string // for the deletion of their files
	what   string // for messages
	// applies says whether the collection's policy removes the documents.
	applies func(c *registry.Collection) bool
	// remove deletes one batch.
	remove func(p storage.OrgPurger, fileFields []string) (int64, []storage.ErasedFile, error)
}

// purge runs the `delete` policies of a scope and returns what was removed, by
// <database>/<collection>.
func (w *Worker) purge(ctx context.Context, realm string, svc *auth.Users, scope purgeScope) (map[string]int64, error) {
	rl := w.reg.Realms[realm]
	if rl == nil {
		return nil, errors.New("the realm no longer exists")
	}
	counts := map[string]int64{}
	for _, dbName := range slices.Sorted(maps.Keys(rl.Databases)) {
		for _, c := range rl.Databases[dbName].SortedCollections() {
			if !scope.applies(c) {
				continue
			}
			purger, ok := w.fns.docs.store.Repository(c).(storage.OrgPurger)
			if !ok {
				return counts, fmt.Errorf("the storage of %s.%s can't delete by %s", dbName, c.Name, scope.what)
			}
			fileFields := sortedKeys(c.Files)
			for {
				n, files, err := scope.remove(purger, fileFields)
				w.orgFiles(ctx, svc, c, files, scope.reason)
				if err != nil {
					return counts, fmt.Errorf("%s.%s: %w", dbName, c.Name, err)
				}
				if n == 0 {
					break
				}
				counts[dbName+"/"+c.Name] += n
				if ctx.Err() != nil {
					return counts, ctx.Err()
				}
			}
		}
	}
	return counts, nil
}

// orgFiles queues the objects of the files of deleted documents and takes them out of the
// storage usage of the users who held them.
func (w *Worker) orgFiles(ctx context.Context, svc *auth.Users, c *registry.Collection, files []storage.ErasedFile, reason string) {
	d := w.fns.docs
	for _, f := range files {
		if err := svc.QueueFileDeletion(ctx, d.objectKey(c, f.ID), reason); err != nil {
			w.log.Error("queue a deleted file's object for deletion", "realm", svc.Realm, "file", f.ID, "error", err)
			continue
		}
		if f.Owner != "" {
			if err := svc.FileRemoved(ctx, f.Owner, f.Size); err != nil {
				w.log.Error("count a deleted file out of the storage usage", "realm", svc.Realm, "error", err)
			}
		}
		for _, name := range slices.Sorted(maps.Keys(f.Versions)) {
			if err := svc.QueueFileDeletion(ctx, d.objectKey(c, f.ID)+"/"+name, reason); err != nil {
				w.log.Error("queue a deleted file's version for deletion", "realm", svc.Realm, "file", f.ID, "version", name, "error", err)
				continue
			}
			if f.Owner != "" {
				if err := svc.FileRemoved(ctx, f.Owner, f.Versions[name]); err != nil {
					w.log.Error("count a deleted version out of the storage usage", "realm", svc.Realm, "error", err)
				}
			}
		}
	}
}
