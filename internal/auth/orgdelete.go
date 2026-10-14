package auth

import (
	"context"
	"errors"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/registry"
)

// Deleting an organization (design/organizations.md §12).

// Audit actions of deletions; the target is org:<id>.
const (
	AuditOrgDelete     = "org.delete"     // details: by, job_id (access ended at once)
	AuditOrgDeleted    = "org.deleted"    // the cleanup finished; details: job_id, counts (never content)
	AuditOrgReactivate = "org.reactivate" // details: slug
)

// OrgDeleteOrigin is the origin of the job that applies the collections' on_org_delete.
const OrgDeleteOrigin = "backd:org.delete"

// ErrOrgNotDeleted: only a deleted organization can be reactivated.
var ErrOrgNotDeleted = errors.New("the organization is not deleted")

// OrgDeleteJob is what a deletion's cleanup carries: the organization. The job's Database is
// empty: it goes through every collection of the realm that has organizations.
type OrgDeleteJob struct {
	Org string
}

// DeleteOrg ends an organization for the caller, who needs manage: org (the HTTP layer asks for
// a fresh proof of identity before). Access ends at once; a job then applies the collections'
// policies to its documents.
func (s *Users) DeleteOrg(ctx context.Context, userID, orgID, requestID string) (Org, error) {
	m, err := s.MembershipOf(ctx, userID, orgID)
	if err != nil {
		return Org{}, err
	}
	if !s.caps(m).Has(registry.CapOrg) {
		return Org{}, ErrOrgForbidden
	}
	return s.deleteOrg(ctx, orgID, "member", requestID)
}

// AdminDeleteOrg ends an organization as a realm administrator.
func (s *Users) AdminDeleteOrg(ctx context.Context, orgID, requestID string) (Org, error) {
	if _, err := s.orgs(); err != nil {
		return Org{}, err
	}
	return s.deleteOrg(ctx, orgID, "admin", requestID)
}

func (s *Users) deleteOrg(ctx context.Context, orgID, by, requestID string) (Org, error) {
	now := s.now()
	org, err := s.Store.ArchiveOrg(ctx, orgID, now)
	if err != nil {
		return Org{}, err
	}
	j := Job{
		ID: xid.New().String(), CallerActor: ActorSystem, Origin: OrgDeleteOrigin, Exclusive: "org-delete:" + orgID,
		TimeoutMS: (10 * time.Minute).Milliseconds(), RequestID: requestID,
		OrgDelete: &OrgDeleteJob{Org: orgID}, Status: JobQueued, CreatedAt: now, ExpiresAt: now.Add(pendingJobTTL),
	}
	if err := s.Store.EnqueueJob(ctx, j); err != nil && !errors.Is(err, ErrJobExclusive) {
		// Access has ended; the cleanup can be started again by deleting again from the admin API.
		return org, err
	}
	s.Audit(ctx, AuditOrgDelete, orgTarget(orgID), map[string]any{"by": by, "job_id": j.ID})
	return org, nil
}

// AdminReactivateOrg brings a deleted organization back, with no members: an owner is assigned
// next. Documents kept under `keep` are visible to members again; those under `delete` are gone.
// slug, when not empty, is the slug the organization takes (its former one may be in use).
func (s *Users) AdminReactivateOrg(ctx context.Context, orgID, slug string) (Org, error) {
	if _, err := s.orgs(); err != nil {
		return Org{}, err
	}
	if err := validSlug(slug); err != nil {
		return Org{}, err
	}
	org, err := s.Store.OrgByID(ctx, orgID)
	if err != nil {
		return Org{}, err
	}
	if slug == "" {
		slug = org.FormerSlug
	}
	org, err = s.Store.ReactivateOrg(ctx, orgID, slug, s.now())
	if err != nil {
		return Org{}, err
	}
	s.Audit(ctx, AuditOrgReactivate, orgTarget(orgID), map[string]any{"slug": org.Slug})
	return org, nil
}
