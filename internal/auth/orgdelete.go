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

// AuditOrgOrphaned is recorded when an organization loses its last manager to an erase.
const AuditOrgOrphaned = "org.orphaned" // details: user

// releaseOrgs takes an erased user out of the organizations (design/organizations.md §9): their
// memberships and team memberships go, invitations bound to them go, and the ones they created
// stay with created_by cleared. An organization left without anyone who can manage it becomes
// orphaned: its members keep access, and an administrator assigns a manager.
func (s *Users) releaseOrgs(ctx context.Context, userID, address string) error {
	if s.Settings.Organizations == nil {
		return nil
	}
	orgs, err := s.Store.ForgetUserInOrgs(ctx, userID, address)
	if err != nil {
		return err
	}
	for _, id := range orgs {
		org, err := s.Store.OrgByID(ctx, id)
		if err != nil || org.Status != OrgActive {
			continue
		}
		if err := s.keepManager(ctx, id, ""); errors.Is(err, ErrLastOrgManager) {
			if _, err := s.Store.UpdateOrg(ctx, id, OrgUpdate{Status: OrgOrphaned}, s.now()); err != nil {
				return err
			}
			s.Audit(ctx, AuditOrgOrphaned, orgTarget(id), map[string]any{"user": userID})
		} else if err != nil {
			return err
		}
	}
	return nil
}

// ManagedOrg is an organization a user can manage, for the report before an erase.
type ManagedOrg struct {
	ID   string
	Name string
	// LastManager says nobody else could manage it: erasing the user leaves it orphaned.
	LastManager bool
}

// OrgsManagedBy lists the organizations in which the user holds a role that manages the
// organization (manage: org), and whether they are the only one.
func (s *Users) OrgsManagedBy(ctx context.Context, userID string) ([]ManagedOrg, error) {
	o := s.Settings.Organizations
	if o == nil {
		return nil, nil
	}
	members, err := s.Store.OrgMembershipsOf(ctx, userID, 0)
	if err != nil {
		return nil, err
	}
	var out []ManagedOrg
	for _, m := range members {
		org, err := s.Store.OrgByID(ctx, m.Org)
		if err != nil || org.Status == OrgDeleted {
			continue
		}
		teamRoles, err := s.teamRolesOf(ctx, m.Org, m.Teams)
		if err != nil {
			return nil, err
		}
		var held []string
		for _, e := range s.effective(m, s.now(), teamRoles) {
			held = append(held, e.Role)
		}
		if !o.CapsOf(held).Has(registry.CapOrg) {
			continue
		}
		last := errors.Is(s.keepManagerWith(ctx, m.Org, userID, nil), ErrLastOrgManager)
		out = append(out, ManagedOrg{ID: org.ID, Name: org.Name, LastManager: last})
	}
	return out, nil
}
