package auth

import (
	"context"
	"strings"

	"github.com/fernandezvara/backd/internal/registry"
)

// Company context (design/team-access.md §10): sign-in, sign-up, magic links and password
// resets may say which organization the person is coming from, so the emails and hosted pages
// of that flow can look like the company's. It is only a hint: it is used when it can't tell
// who is registered, and ignored without a word otherwise.

type orgContextKey struct{}

// WithOrgContext records on ctx the organization the emails queued by the request belong to.
func WithOrgContext(ctx context.Context, org string) context.Context {
	if org == "" {
		return ctx
	}
	return context.WithValue(ctx, orgContextKey{}, org)
}

// OrgContextOf returns the organization recorded on ctx, or "".
func OrgContextOf(ctx context.Context) string {
	org, _ := ctx.Value(orgContextKey{}).(string)
	return org
}

// ResolveOrgContext returns org when it can be the company context of a request about address:
// the organization exists and isn't deleted, and the address belongs to one of its members or
// has a pending invitation by email to it. Anything else, an unknown organization and a
// stranger's address alike, returns "": the caller treats the request as if it had no
// `org`, so the answer and the work are the same and nothing says who belongs where.
func (s *Users) ResolveOrgContext(ctx context.Context, org, address string) string {
	if org == "" || s.Settings.Organizations == nil {
		return ""
	}
	// Both lookups always run, so a known organization costs what an unknown one does.
	record, orgErr := s.Store.OrgByID(ctx, org)
	normalized, err := registry.NormalizeEmail(address)
	if err != nil {
		normalized = ""
	}
	belongs := false
	if u, uerr := s.Store.UserByEmail(ctx, normalized); uerr == nil && u.ErasedAt.IsZero() {
		_, merr := s.Store.OrgMember(ctx, org, u.ID)
		belongs = merr == nil
	}
	invited := false
	if list, lerr := s.Store.ListOrgInvitations(ctx, org); lerr == nil {
		now := s.now()
		for _, inv := range list {
			if inv.Binding == BindEmail && inv.Status(now) == "pending" && strings.EqualFold(inv.Email, normalized) {
				invited = true
				break
			}
		}
	}
	if orgErr != nil || record.Status == OrgDeleted || (!belongs && !invited) {
		return ""
	}
	return org
}
