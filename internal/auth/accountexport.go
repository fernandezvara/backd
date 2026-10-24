package auth

import (
	"context"
	"slices"
	"strings"
	"time"
)

// AccountPart is one file of the system data in a user's personal data export
// (`_account/<Name>.jsonl`): rows of plain values, with the times as RFC 3339 text.
type AccountPart struct {
	Name string
	// Rows are in a stable order. Fields are the API's snake_case names.
	Rows []map[string]any
	// Truncated says the part stopped at its cap, so the export is incomplete there.
	Truncated bool
}

// Caps on the parts that can grow without bound (audit records and function calls).
const (
	accountExportPage = 500
	accountExportCap  = 20000
)

func stamp(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// AccountExport gathers the system data the realm holds about a user for their personal data
// export (design/compliance.md §3.3): the account, the ways they sign in, sessions, push
// devices, the second factor, organizations, invitations addressed to them, consent history,
// the audit records where they are the actor or the subject, and the functions they called.
//
// It never returns credentials: password hashes, provider tokens, session and device tokens,
// second factor secrets and recovery codes, and invitation tokens stay out. Records that
// mention other people carry only the ids; nothing of theirs is copied.
func (s *Users) AccountExport(ctx context.Context, userID string) ([]AccountPart, error) {
	u, err := s.Store.UserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	var parts []AccountPart
	add := func(name string, rows []map[string]any, truncated bool) {
		parts = append(parts, AccountPart{Name: name, Rows: rows, Truncated: truncated})
	}

	account := map[string]any{
		"id": u.ID, "email": u.Email, "email_verified": u.EmailVerified, "roles": orEmpty(u.Roles),
		"locale": u.Locale, "disabled": u.Disabled, "created_at": stamp(u.CreatedAt), "updated_at": stamp(u.UpdatedAt),
	}
	if u.PendingEmail != "" {
		account["pending_email"] = u.PendingEmail
	}
	if u.PreviousEmail != "" {
		account["previous_email"] = u.PreviousEmail
	}
	add("account", []map[string]any{account}, false)

	idents, err := s.Store.ListIdentities(ctx, userID)
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	var lastSignIn time.Time
	for _, i := range idents {
		row := map[string]any{
			"provider": i.Provider, "created_at": stamp(i.CreatedAt), "last_used_at": stamp(i.LastUsedAt),
		}
		if i.Provider != ProviderPassword {
			row["provider_subject"], row["provider_email"], row["provider_email_verified"] = i.Subject, i.Email, i.EmailVerified
		} else {
			row["password_changed_at"] = stamp(i.PasswordChangedAt)
		}
		if i.LastUsedAt.After(lastSignIn) {
			lastSignIn = i.LastUsedAt
		}
		rows = append(rows, row)
	}
	parts[0].Rows[0]["last_sign_in_at"] = stamp(lastSignIn)
	add("identities", rows, false)

	sessions, err := s.Store.ListSessions(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows = nil
	for _, ss := range sessions {
		rows = append(rows, map[string]any{
			"id": ss.ID, "created_at": stamp(ss.CreatedAt), "last_used_at": stamp(ss.LastUsedAt), "expires_at": stamp(ss.ExpiresAt),
			"methods": orEmpty(ss.Methods), "second_factor": ss.MFA,
		})
	}
	add("sessions", rows, false)

	devices, err := s.Store.ListDevices(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows = nil
	for _, d := range devices {
		rows = append(rows, map[string]any{
			"id": d.ID, "platform": d.Platform, "name": d.Name, "created_at": stamp(d.CreatedAt), "last_seen_at": stamp(d.LastSeen),
		})
	}
	add("devices", rows, false)

	mfa := map[string]any{"enabled": false}
	if rec, err := s.Store.MFAOf(ctx, userID); err == nil {
		mfa = map[string]any{
			"enabled": true, "methods": []string{"totp"}, "enabled_at": stamp(rec.EnabledAt),
			"recovery_codes_remaining": len(rec.RecoveryCodes),
		}
		if rec.LastStep > 0 {
			mfa["last_used_at"] = stamp(time.Unix(rec.LastStep*30, 0))
		}
	} else if err != ErrNotFound {
		return nil, err
	}
	add("mfa", []map[string]any{mfa}, false)

	rows, err = s.accountOrgs(ctx, u)
	if err != nil {
		return nil, err
	}
	add("organizations", rows, false)

	rows, err = s.accountInvitations(ctx, u)
	if err != nil {
		return nil, err
	}
	add("invitations", rows, false)

	records, err := s.Store.ListConsentRecords(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows = nil
	for _, r := range records {
		row := map[string]any{
			"consent": r.Consent, "version": r.Version, "locale": r.Locale, "text_hash": r.TextHash,
			"action": r.Action, "method": r.Method, "at": stamp(r.At),
		}
		if r.Evidence != "" {
			row["evidence"] = r.Evidence
		}
		if r.IP != "" {
			row["ip"] = r.IP
		}
		if r.UserAgent != "" {
			row["user_agent"] = r.UserAgent
		}
		rows = append(rows, row)
	}
	add("consents", rows, false)

	rows, truncated, err := s.accountAudit(ctx, userID)
	if err != nil {
		return nil, err
	}
	add("audit", rows, truncated)

	rows, truncated, err = s.accountInvocations(ctx, userID)
	if err != nil {
		return nil, err
	}
	add("invocations", rows, truncated)
	return parts, nil
}

func orEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// accountOrgs lists the organizations the user belongs to: their roles, teams and when they joined.
func (s *Users) accountOrgs(ctx context.Context, u User) ([]map[string]any, error) {
	if _, err := s.orgs(); err != nil {
		return nil, nil // the realm has no organizations
	}
	mems, err := s.MyOrgs(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	for _, m := range mems {
		teams := orEmpty(m.Member.Teams)
		slices.Sort(teams)
		row := map[string]any{
			"organization": m.Org.ID, "name": m.Org.Name, "roles": orEmpty(m.RoleNames()), "teams": teams,
			"joined_at": stamp(m.Member.JoinedAt),
		}
		if m.Member.InvitedBy != "" {
			row["invited_by"] = m.Member.InvitedBy
		}
		if len(m.Member.TeamRoles) > 0 {
			grants := make([]map[string]any, 0, len(m.Member.TeamRoles))
			for _, g := range m.Member.TeamRoles {
				grants = append(grants, map[string]any{"team": g.Team, "role": g.Role})
			}
			row["team_roles"] = grants
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// accountInvitations lists the invitations addressed to the user, to the realm and to organizations,
// without their tokens.
func (s *Users) accountInvitations(ctx context.Context, u User) ([]map[string]any, error) {
	var rows []map[string]any
	all, err := s.Store.ListInvitations(ctx)
	if err != nil {
		return nil, err
	}
	for _, inv := range all {
		if inv.Email != "" && inv.Email == u.Email {
			rows = append(rows, map[string]any{
				"kind": "realm", "id": inv.ID, "created_at": stamp(inv.CreatedAt), "expires_at": stamp(inv.ExpiresAt),
				"created_by": inv.CreatedBy,
			})
		}
	}
	if _, err := s.orgs(); err == nil {
		orgInvs, err := s.Store.OrgInvitationsAddressedTo(ctx, u.ID, u.Email)
		if err != nil {
			return nil, err
		}
		now := s.now()
		for _, inv := range orgInvs {
			rows = append(rows, map[string]any{
				"kind": "organization", "id": inv.ID, "organization": inv.Org, "roles": orEmpty(inv.Roles), "teams": orEmpty(inv.Teams),
				"status": inv.Status(now), "created_at": stamp(inv.CreatedAt), "expires_at": stamp(inv.ExpiresAt), "created_by": inv.CreatedBy,
			})
		}
	}
	return rows, nil
}

// accountAudit lists the audit records where the user is the actor or the subject. Of each it
// keeps the event, the time and the ids, never its details, which may describe other people.
func (s *Users) accountAudit(ctx context.Context, userID string) ([]map[string]any, bool, error) {
	who := "user:" + userID
	seen := map[string]bool{}
	var recs []AuditRecord
	truncated := false
	for _, f := range []AuditFilter{{Actor: who}, {Target: who}} {
		for skip := 0; ; skip += accountExportPage {
			f.Limit, f.Skip = accountExportPage, skip
			page, more, err := s.Store.ListAudit(ctx, f)
			if err != nil {
				return nil, false, err
			}
			for _, r := range page {
				if !seen[r.ID] {
					seen[r.ID] = true
					recs = append(recs, r)
				}
			}
			if !more {
				break
			}
			if skip+accountExportPage >= accountExportCap {
				truncated = true
				break
			}
		}
	}
	slices.SortFunc(recs, func(a, b AuditRecord) int {
		if c := a.At.Compare(b.At); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	rows := make([]map[string]any, len(recs))
	for i, r := range recs {
		rows[i] = map[string]any{"id": r.ID, "at": stamp(r.At), "event": r.Action, "actor": r.Actor, "target": r.Target}
	}
	return rows, truncated, nil
}

// accountInvocations lists the function calls the user made: which function, when and how it ended.
// Inputs, outputs and logs are not kept by the realm and are not part of it.
func (s *Users) accountInvocations(ctx context.Context, userID string) ([]map[string]any, bool, error) {
	var rows []map[string]any
	truncated := false
	for skip := 0; ; skip += accountExportPage {
		page, more, err := s.Store.ListInvocations(ctx, InvocationFilter{Actor: "user:" + userID, Limit: accountExportPage, Skip: skip})
		if err != nil {
			return nil, false, err
		}
		for _, r := range page {
			rows = append(rows, map[string]any{"id": r.ID, "at": stamp(r.At), "function": r.Function, "status": r.Status, "mode": r.Mode, "origin": r.Origin})
		}
		if !more {
			break
		}
		if skip+accountExportPage >= accountExportCap {
			truncated = true
			break
		}
	}
	slices.Reverse(rows) // oldest first, like the other parts
	return rows, truncated, nil
}
