package auth

import (
	"context"
	"errors"
	"strings"

	"github.com/fernandezvara/backd/internal/registry"
)

// Requiring the organization's connection (design/scoped-sessions.md §6, §7). A session records
// how it was opened as its first method; one opened through an organization's connection says so,
// with the organization, so a connection's session is never taken for another way in.

// AuditOrgSSORequired is recorded when sso_required is turned on or off; details: value, and when
// it is turned on without_sso and revoked, when it is released by backd the reason.
const AuditOrgSSORequired = "org.sso_required"

// ConnectionMethodPrefix starts the first method of every session an organization's connection
// opened.
func ConnectionMethodPrefix(org string) string { return "sso:" + org + ":" }

// ConnectionMethod is the first method of a session opened through the connection of an
// organization with a provider: not the provider's name alone, which the realm's own providers use
// too.
func ConnectionMethod(org, provider string) string { return ConnectionMethodPrefix(org) + provider }

// Connection says whether the session was opened through an organization's connection, and which.
// Such a session proves "the provider of the organization says this is the person", not that the
// person owns the account: it can't change what protects it.
func (s Session) Connection() (org, provider string, ok bool) {
	if len(s.Methods) == 0 {
		return "", "", false
	}
	rest, found := strings.CutPrefix(s.Methods[0], "sso:")
	if !found {
		return "", "", false
	}
	org, provider, ok = strings.Cut(rest, ":")
	return org, provider, ok && org != "" && provider != ""
}

// OrgSSORequired says whether the organization named (id or slug) requires its connection, and its
// id. An unknown, deleted or inactive organization is not required to anything.
func (s *Users) OrgSSORequired(ctx context.Context, ref string) (orgID string, required bool) {
	org, err := s.OrgByRef(ctx, ref)
	if err != nil {
		return "", false
	}
	return org.ID, org.SSORequired
}

// SSORequiredResult is the organization after sso_required changed, and what turning it on did.
type SSORequiredResult struct {
	Membership Membership
	// WithoutSSO is how many members had no identity on a connection when it was turned on: they
	// can't come in until they sign in through it. Revoked is how many sessions it ended.
	WithoutSSO int
	Revoked    int64
}

// usableConnections are the organization's connections that sign people in now.
func (s *Users) usableConnections(ctx context.Context, orgID string) (map[string]bool, error) {
	sso := s.Settings.Organizations.SSO
	if sso == nil {
		return nil, nil
	}
	list, err := s.connectionViews(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, c := range list {
		if c.Enabled && c.SecretSet && sso.Allows(c.Provider) {
			out[c.Provider] = true
		}
	}
	return out, nil
}

// SetOrgSSORequired turns sso_required on or off for the organization of the caller's session. It
// needs manage: org (the HTTP layer has asked for a fresh proof). Turning it on needs a working
// connection and that the caller signed in through one, so nobody locks themselves out; it then ends
// the sessions that were not opened through a connection. Turning it off needs neither.
func (s *Users) SetOrgSSORequired(ctx context.Context, p Principal, orgID string, on bool) (SSORequiredResult, error) {
	if _, err := s.orgs(); err != nil {
		return SSORequiredResult{}, err
	}
	m, err := s.MembershipOf(ctx, p.User.ID, orgID)
	if err != nil {
		return SSORequiredResult{}, err
	}
	if _, err := s.requireCaps(m, registry.CapOrg); err != nil {
		return SSORequiredResult{}, err
	}
	if on {
		usable, err := s.usableConnections(ctx, orgID)
		if err != nil {
			return SSORequiredResult{}, err
		}
		org, provider, via := p.Session.Connection()
		if len(usable) == 0 || !via || org != orgID || !usable[provider] {
			return SSORequiredResult{}, ErrSSONotReady
		}
	}
	return s.setSSORequired(ctx, m, orgID, on)
}

// AdminSetOrgSSORequired is the administrators' way: they can turn it off whatever the state of the
// connections (the break glass for an organization that can't reach its provider), and on only
// when the organization has a working connection.
func (s *Users) AdminSetOrgSSORequired(ctx context.Context, orgID string, on bool) (SSORequiredResult, error) {
	if _, err := s.orgs(); err != nil {
		return SSORequiredResult{}, err
	}
	org, err := s.Store.OrgByID(ctx, orgID)
	if err != nil {
		return SSORequiredResult{}, err
	}
	if org.Status == OrgDeleted {
		return SSORequiredResult{}, ErrNotFound
	}
	if on {
		return SSORequiredResult{}, ErrSSONotReady // only a manager who signed in through the connection turns it on
	}
	return s.setSSORequired(ctx, Membership{Org: org}, orgID, false)
}

func (s *Users) setSSORequired(ctx context.Context, m Membership, orgID string, on bool) (SSORequiredResult, error) {
	res := SSORequiredResult{Membership: m}
	if m.Org.SSORequired == on {
		if on {
			n, err := s.membersWithoutSSO(ctx, orgID)
			res.WithoutSSO = n
			return res, err
		}
		return res, nil
	}
	if on {
		n, err := s.membersWithoutSSO(ctx, orgID)
		if err != nil {
			return res, err
		}
		res.WithoutSSO = n
	}
	org, err := s.Store.UpdateOrg(ctx, orgID, OrgUpdate{SSORequired: &on}, s.now())
	if err != nil {
		return res, err
	}
	res.Membership.Org = org
	details := map[string]any{"value": on}
	if on {
		// The sessions of the organization that did not come through a connection end now; the
		// caller's own was opened by one.
		n, err := s.Store.DeleteOrgSessionsNotOpenedBy(context.WithoutCancel(ctx), orgID)
		if err != nil && s.Log != nil {
			s.Log.Warn("end the sessions that did not come through the connection", "org", orgID, "error", err)
		}
		res.Revoked = n
		details["without_sso"], details["revoked"] = res.WithoutSSO, n
	}
	s.Audit(ctx, AuditOrgSSORequired, orgTarget(orgID), details)
	return res, nil
}

// releaseSSORequired turns the requirement off when the organization has no connection left that
// works: its people would have no way in at all. It runs after a connection is removed or disabled.
func (s *Users) releaseSSORequired(ctx context.Context, orgID, reason string) {
	org, err := s.Store.OrgByID(ctx, orgID)
	if err != nil || !org.SSORequired {
		return
	}
	usable, err := s.usableConnections(ctx, orgID)
	if err != nil || len(usable) > 0 {
		return
	}
	off := false
	if _, err := s.Store.UpdateOrg(ctx, orgID, OrgUpdate{SSORequired: &off}, s.now()); err != nil {
		if s.Log != nil {
			s.Log.Warn("release sso_required of an organization with no connection", "org", orgID, "error", err)
		}
		return
	}
	s.Audit(ctx, AuditOrgSSORequired, orgTarget(orgID), map[string]any{"value": false, "reason": reason})
}

// hasConnectionIdentity says whether the user signed in through one of the organization's
// connections: they hold an identity of the provider that belongs to it.
func (s *Users) hasConnectionIdentity(ctx context.Context, userID, orgID string, providers []string) (bool, error) {
	for _, pr := range providers {
		_, err := s.Store.IdentityOfIn(ctx, userID, pr, orgID)
		switch {
		case err == nil:
			return true, nil
		case !errors.Is(err, ErrNotFound):
			return false, err
		}
	}
	return false, nil
}

// connectionProviders are the providers the organization has connections for.
func (s *Users) connectionProviders(ctx context.Context, orgID string) ([]string, error) {
	list, err := s.Store.OrgConnections(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(list))
	for i, c := range list {
		out[i] = c.Provider
	}
	return out, nil
}

// membersWithoutSSO counts the members who have no identity on a connection of the organization.
func (s *Users) membersWithoutSSO(ctx context.Context, orgID string) (int, error) {
	providers, err := s.connectionProviders(ctx, orgID)
	if err != nil {
		return 0, err
	}
	n, after := 0, ""
	for {
		list, more, err := s.Store.ListOrgMembers(ctx, orgID, after, 500)
		if err != nil {
			return 0, err
		}
		for _, m := range list {
			has, err := s.hasConnectionIdentity(ctx, m.User, orgID, providers)
			if err != nil {
				return 0, err
			}
			if !has {
				n++
			}
		}
		if !more || len(list) == 0 {
			return n, nil
		}
		after = list[len(list)-1].User
	}
}

// MembersWithoutSSO lists the members who have no identity on a connection of the organization:
// the people who can't come in once it requires its connection. It is Members with that filter,
// and needs the same right.
func (s *Users) MembersWithoutSSO(ctx context.Context, userID, orgID, after string, limit int) ([]MemberView, bool, error) {
	viewer, err := s.MembershipOf(ctx, userID, orgID)
	if err != nil {
		return nil, false, err
	}
	providers, err := s.connectionProviders(ctx, orgID)
	if err != nil {
		return nil, false, err
	}
	var picked []OrgMember
	more := false
	for len(picked) <= limit {
		list, next, err := s.Store.ListOrgMembers(ctx, orgID, after, 500)
		if err != nil {
			return nil, false, err
		}
		for _, m := range list {
			has, err := s.hasConnectionIdentity(ctx, m.User, orgID, providers)
			if err != nil {
				return nil, false, err
			}
			if !has {
				picked = append(picked, m)
			}
		}
		if !next || len(list) == 0 {
			break
		}
		after = list[len(list)-1].User
	}
	if len(picked) > limit {
		picked, more = picked[:limit], true
	}
	views, err := s.viewsOf(ctx, viewer, orgID, picked)
	return views, more, err
}
