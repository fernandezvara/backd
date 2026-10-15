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
	// AuditTeamDeleted: the cleanup after a team was deleted finished; the target is org:<id>.
	AuditTeamDeleted = "org.team_deleted" // details: team, job_id, counts (never content)
)

// OrgDeleteOrigin is the origin of the job that applies the collections' on_org_delete.
const OrgDeleteOrigin = "backd:org.delete"

// ErrOrgNotDeleted: only a deleted organization can be reactivated.
var ErrOrgNotDeleted = errors.New("the organization is not deleted")

// OrgDeleteJob is what a deletion's cleanup carries: the organization, and the team when it is
// the cleanup after a team was deleted (on_team_delete). The job's Database is empty: it goes
// through every collection of the realm that has organizations (or teams).
type OrgDeleteJob struct {
	Org  string
	Team string
}

// TeamDeleteOrigin is the origin of the job that applies the collections' on_team_delete.
const TeamDeleteOrigin = "backd:team.delete"

// enqueueCleanup queues the cleanup job of a deletion; a second one for the same thing that is
// already queued or running is fine (the exclusive key makes it the same job).
func (s *Users) enqueueCleanup(ctx context.Context, origin, exclusive string, job OrgDeleteJob, requestID string) (Job, error) {
	now := s.now()
	j := Job{
		ID: xid.New().String(), CallerActor: ActorSystem, Origin: origin, Exclusive: exclusive,
		TimeoutMS: (10 * time.Minute).Milliseconds(), RequestID: requestID,
		OrgDelete: &job, Status: JobQueued, CreatedAt: now, ExpiresAt: now.Add(pendingJobTTL),
	}
	if err := s.Store.EnqueueJob(ctx, j); err != nil && !errors.Is(err, ErrJobExclusive) {
		return j, err
	}
	return j, nil
}

// DeleteOrg ends an organization for the caller, who needs manage: org (the HTTP layer asks for
// a fresh proof of identity before). Access ends at once; a job then applies the collections'
// policies to its documents.
func (s *Users) DeleteOrg(ctx context.Context, userID, orgID, requestID string) (Org, error) {
	m, err := s.MembershipOf(ctx, userID, orgID)
	if err != nil {
		return Org{}, err
	}
	if _, err := s.requireCaps(m, registry.CapOrg); err != nil {
		return Org{}, err
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
	org, err := s.Store.ArchiveOrg(ctx, orgID, s.now())
	if err != nil {
		return Org{}, err
	}
	j, err := s.enqueueCleanup(ctx, OrgDeleteOrigin, "org-delete:"+orgID, OrgDeleteJob{Org: orgID}, requestID)
	if err != nil {
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
	var orgs []string
	err := s.Store.Transact(ctx, func(ctx context.Context) error {
		var err error
		if orgs, err = s.Store.ForgetUserInOrgs(ctx, userID, address); err != nil {
			return err
		}
		for _, id := range orgs {
			if err := s.memberLeft(ctx, id, userID, LeftErased); err != nil {
				return err
			}
		}
		return nil
	})
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
