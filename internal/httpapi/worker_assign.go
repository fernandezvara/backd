package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/executor"
	"github.com/fernandezvara/backd/internal/query"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

const (
	assignBatch    = 100
	assignLease    = 10 * time.Minute
	assignAttempts = 3
	// assignedBy is what an assignment records in _meta.updated_by.
	assignedBy = "backd:org.assign"
)

// runAssignOrg gives every document of a collection that has no organization, and matches the
// job's filter, the job's organization (design/organizations.md §10). It reads the collection
// in id order, writes each document conditionally on the version it read, and keeps its lease
// as it goes; a worker that dies leaves a job another resumes, since the documents done
// already have an organization and are skipped.
func (w *Worker) runAssignOrg(ctx context.Context, log *slog.Logger, realm string, svc *auth.Users, job auth.Job) {
	a := job.Assign
	tr := &stepTracker{svc: svc, jobID: job.ID, attempt: job.Attempts}
	scope := job.Database + "/" + a.Collection
	fail := func(message string) {
		log.Error("assignment of an organization failed", "scope", scope, "error", message)
		tr.finish(ctx, auth.StepFailed)
		if err := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: executor.StatusError, Message: message}); err != nil {
			log.Error("complete the failed assignment", "error", err)
			return
		}
		w.fns.metrics.JobFinished(svc.Realm, "org", executor.StatusError)
	}
	c, ok := w.reg.Collection(realm, job.Database, a.Collection)
	if !ok || !c.InOrganizations() {
		fail("the collection no longer belongs to organizations")
		return
	}
	if !svc.OrgActive(ctx, a.Org) {
		fail("the organization doesn't exist or was deleted")
		return
	}
	where, err := query.ParseWhere(a.Where, c.Fields)
	if err != nil {
		fail("the filter is not valid any more: " + err.Error())
		return
	}
	repo := w.fns.docs.store.Repository(c)
	scanner, isScanner := repo.(storage.Scanner)
	if !isScanner {
		fail("the storage of " + scope + " can't be scanned")
		return
	}
	retry := func(err error) {
		if failed := job.Failures + 1; failed < assignAttempts {
			wait := 30 * time.Second << (failed - 1)
			log.Warn("assignment attempt failed; it will be tried again", "attempt", failed, "of", assignAttempts, "retry_in", wait.String(), "error", err)
			tr.finish(ctx, auth.StepFailed)
			if rerr := svc.RetryJob(ctx, job.ID, wait); rerr != nil {
				log.Error("queue the retry", "error", rerr)
			}
			return
		}
		fail(err.Error())
	}
	estimate, err := scanner.EstimatedCount(ctx)
	if err != nil {
		retry(err)
		return
	}
	tr.begin(ctx, scope, 100, fmt.Sprintf("scanning about %d documents", estimate))
	var scanned, assigned, skipped int64
	reported, after := 0, ""
	for {
		docs, err := scanner.ScanAfter(ctx, after, assignBatch)
		if err != nil {
			retry(err)
			return
		}
		if len(docs) == 0 {
			break
		}
		for _, doc := range docs {
			if ctx.Err() != nil {
				return // the worker is stopping: the lease lapses and the job resumes elsewhere
			}
			scanned++
			after, _ = doc["id"].(string)
			meta, _ := doc["_meta"].(map[string]any)
			if org, _ := meta["org"].(string); org != "" {
				continue // never moves a document that has one
			}
			if where != nil {
				match, err := storage.Match(doc, where)
				if err != nil || !match {
					continue
				}
			}
			done, err := w.assignOne(ctx, repo, doc, a.Org)
			switch {
			case err != nil:
				retry(fmt.Errorf("%s: %w", after, err))
				return
			case done:
				assigned++
			default:
				skipped++
			}
		}
		if held, err := svc.RenewJobLease(ctx, job.ID, job.Attempts, assignLease); err != nil {
			retry(err)
			return
		} else if !held {
			log.Info("the assignment was cancelled or taken over; stopping", "scope", scope)
			return
		}
		if pct, due := checkPercent(scanned, estimate, reported); due {
			reported = pct
			tr.progress(ctx, float64(pct), fmt.Sprintf("%d documents given an organization so far, in %s", assigned, progressMessage(scanned, estimate)))
		}
	}
	tr.progress(ctx, 100, fmt.Sprintf("%d documents looked at, %d given an organization, %d skipped", scanned, assigned, skipped))
	tr.finish(ctx, auth.StepDone)
	out, _ := json.Marshal(map[string]int64{"scanned": scanned, "assigned": assigned, "skipped": skipped})
	if err := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: executor.StatusOK, Output: out}); err != nil {
		log.Error("complete the assignment", "error", err)
		return
	}
	w.fns.metrics.JobFinished(svc.Realm, "org", executor.StatusOK)
	log.Info("organizations assigned", "scope", scope, "org", a.Org, "assigned", assigned, "skipped", skipped)
}

// assignOne writes the organization on one document, conditional on the version read. A
// document changed meanwhile is read again once; one that keeps changing is skipped, and so is
// one that was deleted.
func (w *Worker) assignOne(ctx context.Context, repo storage.Repository, doc storage.Document, org string) (bool, error) {
	for range 2 {
		meta, _ := doc["_meta"].(map[string]any)
		read := version(doc)
		next := map[string]any{}
		maps.Copy(next, meta)
		next["org"], next["version"], next["updated_at"], next["updated_by"] = org, read+1, w.fns.docs.timestamp(), assignedBy
		delete(next, "chain") // no trigger caused this write: it carries no chain
		fields := map[string]any{}
		maps.Copy(fields, doc)
		fields["_meta"] = next
		err := repo.Replace(ctx, fields, read)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, storage.ErrVersionMismatch), errors.Is(err, storage.ErrNotFound):
			fresh, ferr := storage.Fetch(ctx, repo, doc["id"].(string), nil)
			if ferr != nil {
				return false, nil // gone
			}
			if m, _ := fresh["_meta"].(map[string]any); m["org"] != nil {
				return false, nil // someone gave it one
			}
			doc = fresh
		default:
			return false, err
		}
	}
	return false, nil
}

// ReportOrganizationless looks, once at startup, for organizations left without a manager and for collections that belong to organizations
// and hold documents without one (a collection that adopted organizations later): they are
// invisible to members until an administrator assigns them. It changes nothing.
func (w *Worker) ReportOrganizationless(ctx context.Context) {
	without := storage.Condition{Field: registry.MetaOrg, Op: storage.OpIsNull, Value: true}
	for _, realm := range w.reg.RealmNames() {
		rl := w.reg.Realms[realm]
		if rl == nil || rl.Settings.Organizations == nil {
			continue
		}
		// Organizations that lost their last manager to an erase.
		if svc := w.fns.docs.users(realm); svc != nil {
			if orphaned, err := svc.AdminOrgs(ctx, auth.OrgOrphaned); err != nil {
				w.log.Warn("could not list the orphaned organizations", "realm", realm, "error", err)
			} else if len(orphaned) > 0 {
				w.log.Warn("organizations without a manager: assign one (backd orgs assign-owner --realm "+realm+" --id <id> --email <email>)",
					"realm", realm, "organizations", len(orphaned))
			}
		}
		for _, db := range sortedKeys(rl.Databases) {
			for _, c := range rl.Databases[db].SortedCollections() {
				if !c.InOrganizations() || ctx.Err() != nil {
					continue
				}
				page, err := w.fns.docs.store.Repository(c).List(ctx, storage.Query{Filter: without, Limit: 1, Count: true})
				if err != nil {
					w.log.Warn("could not count the documents without an organization", "realm", realm, "collection", db+"/"+c.Name, "error", err)
					continue
				}
				if page.Total != nil && *page.Total > 0 {
					w.log.Warn("documents without an organization: members can't see them until they are assigned (backd data assign-org --realm "+realm+" --database "+db+" --collection "+c.Name+" --org <id>)",
						"realm", realm, "collection", db+"/"+c.Name, "documents", *page.Total, "organization", c.Organization)
				}
			}
		}
	}
}
