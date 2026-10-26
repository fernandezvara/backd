package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/registry"
)

// Owner-editable email templates (design/team-access.md §11): a company writes its own version of
// the kinds email.customizable lists, per language. The realm's file stays the fallback.

// Audit actions of templates; the target is org:<id>, details never hold the text.
const (
	AuditOrgTemplateSave   = "org.template_save"   // details: kind, locale, version
	AuditOrgTemplateDelete = "org.template_delete" // details: kind, locale
	AuditOrgTemplateTest   = "org.template_test"   // details: kind, locale
)

// Limits of owner templates.
const (
	// OrgTemplateVersions is how many previous versions of a template are kept.
	OrgTemplateVersions = 20
	// OrgTemplateTestsPerHour is how many test sends an organization may make.
	OrgTemplateTestsPerHour = 10
)

// OrgTemplateVersion is a previous version of a template.
type OrgTemplateVersion struct {
	Version int
	email.Override
	By string // user id
	At time.Time
}

// OrgTemplate is an organization's version of one kind in one language.
type OrgTemplate struct {
	Org, Kind, Locale string
	email.Override
	Version   int
	UpdatedBy string
	UpdatedAt time.Time
	// Versions are the previous ones, newest first, at most OrgTemplateVersions.
	Versions []OrgTemplateVersion
}

var (
	// ErrTemplateNotCustomizable: the kind isn't listed in email.customizable.
	ErrTemplateNotCustomizable = errors.New("that kind of email can't be customized (email.customizable)")
	// ErrTemplateTestLimit: too many test sends.
	ErrTemplateTestLimit = errors.New("too many test emails for this organization; try again later")
)

// TemplateInvalidError is a template that doesn't pass the checks, with each problem.
type TemplateInvalidError struct{ Problems []email.OverrideProblem }

func (e *TemplateInvalidError) Error() string {
	if len(e.Problems) == 0 {
		return "the template is invalid"
	}
	return "the template is invalid: " + e.Problems[0].String()
}

// templateEditor returns the caller's membership, who must hold the capability
// email.overrides.permission names, and checks the kind and language.
func (s *Users) templateEditor(ctx context.Context, userID, orgID, kind, locale string) (Membership, error) {
	if _, err := s.orgs(); err != nil {
		return Membership{}, err
	}
	es := s.Settings.Email
	if es == nil {
		return Membership{}, ErrEmailNotConfigured
	}
	m, err := s.MembershipOf(ctx, userID, orgID)
	if err != nil {
		return Membership{}, err
	}
	if _, err := s.requireCaps(m, es.OverridePermission); err != nil {
		return Membership{}, err
	}
	if kind != "" && !slices.Contains(es.Customizable, kind) {
		return Membership{}, ErrTemplateNotCustomizable
	}
	if locale != "" {
		if _, locales := s.Settings.Languages(); !slices.Contains(locales, locale) {
			return Membership{}, &OrgInvalidError{"locale", fmt.Sprintf("%q is not a language of this realm", locale)}
		}
	}
	return m, nil
}

// CustomizableKinds are the kinds the realm lets companies customize, with the placeholders each needs.
func (s *Users) CustomizableKinds() map[string][]string {
	out := map[string][]string{}
	if es := s.Settings.Email; es != nil {
		for _, kind := range es.Customizable {
			out[kind] = email.RequiredPlaceholders(kind)
		}
	}
	return out
}

// OrgTemplates lists an organization's overrides to someone who may edit them.
func (s *Users) OrgTemplates(ctx context.Context, userID, orgID string) ([]OrgTemplate, error) {
	if _, err := s.templateEditor(ctx, userID, orgID, "", ""); err != nil {
		return nil, err
	}
	return s.Store.ListOrgTemplates(ctx, orgID)
}

// OrgTemplateOf returns one override; ErrNotFound when the organization has none.
func (s *Users) OrgTemplateOf(ctx context.Context, userID, orgID, kind, locale string) (OrgTemplate, error) {
	if _, err := s.templateEditor(ctx, userID, orgID, kind, locale); err != nil {
		return OrgTemplate{}, err
	}
	return s.Store.OrgTemplate(ctx, orgID, kind, locale)
}

// CheckOrgTemplate compiles a draft for kind and renders it with sample data, so a problem shows
// when it is written, with where it is. It returns what the sanitizer dropped from the sample.
func CheckOrgTemplate(kind string, o email.Override) (removed []string, err error) {
	c, problems := email.CompileOverride(kind, o)
	if len(problems) > 0 {
		return nil, &TemplateInvalidError{Problems: problems}
	}
	_, removed, rerr := c.Render(email.SampleData(kind), "Sample Organization")
	if rerr != nil {
		return nil, &TemplateInvalidError{Problems: []email.OverrideProblem{{Part: "template", Message: rerr.Error()}}}
	}
	return removed, nil
}

// SaveOrgTemplate stores the organization's version of a kind in a language after checking it.
// The previous one becomes a version; the last OrgTemplateVersions are kept.
func (s *Users) SaveOrgTemplate(ctx context.Context, userID, orgID, kind, locale string, o email.Override) (OrgTemplate, []string, error) {
	if _, err := s.templateEditor(ctx, userID, orgID, kind, locale); err != nil {
		return OrgTemplate{}, nil, err
	}
	removed, err := CheckOrgTemplate(kind, o)
	if err != nil {
		return OrgTemplate{}, nil, err
	}
	now := s.now()
	t := OrgTemplate{Org: orgID, Kind: kind, Locale: locale, Override: o, Version: 1, UpdatedBy: userID, UpdatedAt: now}
	switch prev, err := s.Store.OrgTemplate(ctx, orgID, kind, locale); {
	case err == nil:
		t.Version = prev.Version + 1
		t.Versions = append([]OrgTemplateVersion{{Version: prev.Version, Override: prev.Override, By: prev.UpdatedBy, At: prev.UpdatedAt}}, prev.Versions...)
		if len(t.Versions) > OrgTemplateVersions {
			t.Versions = t.Versions[:OrgTemplateVersions]
		}
	case !errors.Is(err, ErrNotFound):
		return OrgTemplate{}, nil, err
	}
	if err := s.Store.PutOrgTemplate(ctx, t); err != nil {
		return OrgTemplate{}, nil, err
	}
	s.Audit(ctx, AuditOrgTemplateSave, orgTarget(orgID), map[string]any{"kind": kind, "locale": locale, "version": t.Version})
	return t, removed, nil
}

// DeleteOrgTemplate returns a kind in a language to the realm's own file.
func (s *Users) DeleteOrgTemplate(ctx context.Context, userID, orgID, kind, locale string) error {
	if _, err := s.templateEditor(ctx, userID, orgID, kind, locale); err != nil {
		return err
	}
	if err := s.Store.DeleteOrgTemplate(ctx, orgID, kind, locale); err != nil {
		return err
	}
	s.Audit(ctx, AuditOrgTemplateDelete, orgTarget(orgID), map[string]any{"kind": kind, "locale": locale})
	return nil
}

// OrgTemplateFor returns the organization's override of a kind for the language, for rendering
// a message: nothing when there is none, the kind isn't customizable or the organization is gone.
func (s *Users) OrgTemplateFor(ctx context.Context, orgID, kind, locale string) (OrgTemplate, bool) {
	es := s.Settings.Email
	if es == nil || orgID == "" || !slices.Contains(es.Customizable, kind) {
		return OrgTemplate{}, false
	}
	t, err := s.Store.OrgTemplate(ctx, orgID, kind, locale)
	return t, err == nil
}

// CanEditTemplate checks that the caller may edit the organization's template of a kind in a
// language: the capability email.overrides.permission names, a customizable kind, a language of
// the realm.
func (s *Users) CanEditTemplate(ctx context.Context, userID, orgID, kind, locale string) error {
	_, err := s.templateEditor(ctx, userID, orgID, kind, locale)
	return err
}

// OrgName is the name of an active organization, or "".
func (s *Users) OrgName(ctx context.Context, orgID string) string {
	org, err := s.Store.OrgByID(ctx, orgID)
	if err != nil || org.Status == OrgDeleted {
		return ""
	}
	return org.Name
}

// ErrTemplatePreviewLimit: too many previews with real data.
var ErrTemplatePreviewLimit = errors.New("too many previews with real data for this organization; try again later")

// OrgTemplateRealPreviewsPerHour is how many previews with real data (which call the data hook)
// an organization may make.
const OrgTemplateRealPreviewsPerHour = 30

// AllowRealPreview counts a preview with real data against the organization's hourly limit.
func (s *Users) AllowRealPreview(ctx context.Context, orgID string) error {
	if err := s.RateLimit(ctx, "tplpreview:"+orgID, OrgTemplateRealPreviewsPerHour, time.Hour); err != nil {
		if _, ok := errors.AsType[*ThrottledError](err); ok {
			return ErrTemplatePreviewLimit
		}
		return err
	}
	return nil
}

// QueueTemplateTest sends a draft (or the saved version, when draft is nil) of a template to
// the requesting user's own address, with "[TEST]" in the subject. At most
// OrgTemplateTestsPerHour per organization.
func (s *Users) QueueTemplateTest(ctx context.Context, userID, orgID, kind, locale string, draft *email.Override, requestID string) error {
	if _, err := s.templateEditor(ctx, userID, orgID, kind, locale); err != nil {
		return err
	}
	var o email.Override
	if draft != nil {
		o = *draft
	} else {
		t, err := s.Store.OrgTemplate(ctx, orgID, kind, locale)
		if err != nil {
			return err
		}
		o = t.Override
	}
	if _, err := CheckOrgTemplate(kind, o); err != nil {
		return err
	}
	if err := s.RateLimit(ctx, "tpltest:"+orgID, OrgTemplateTestsPerHour, time.Hour); err != nil {
		if _, ok := errors.AsType[*ThrottledError](err); ok {
			return ErrTemplateTestLimit
		}
		return err
	}
	u, err := s.Store.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	job := EmailRequest{Kind: kind, UserID: userID, Address: u.Email, Locale: locale, Org: orgID, Notice: true, RequestID: requestID, Draft: &o}
	if _, err := s.QueueEmail(ctx, job); err != nil {
		return err
	}
	s.Audit(ctx, AuditOrgTemplateTest, orgTarget(orgID), map[string]any{"kind": kind, "locale": locale})
	return nil
}

// NotifyTemplateFailed tells the managers of an organization that a template of theirs couldn't
// be used when a message was sent: by email, at most once a day for each kind. It logs what
// fails and never returns an error: the message that failed goes out in the realm's text.
func (s *Users) NotifyTemplateFailed(ctx context.Context, orgID, kind string) {
	if s.Settings.Organizations == nil || s.Settings.Email == nil {
		return
	}
	if err := s.RateLimit(ctx, "tplfail:"+orgID+":"+kind, 1, 24*time.Hour); err != nil {
		return // told already today
	}
	managers, err := s.orgManagers(ctx, orgID)
	if err != nil {
		if s.Log != nil {
			s.Log.Error("could not find the managers to tell about a template", "org", orgID, "error", err)
		}
		return
	}
	for _, m := range managers {
		u, err := s.Store.UserByID(ctx, m.User)
		if err != nil || u.Disabled || !u.ErasedAt.IsZero() {
			continue
		}
		s.queueNotice(ctx, EmailRequest{Kind: email.TemplateFailed, UserID: u.ID, Address: u.Email, Locale: s.LocaleOf(u), Org: orgID, Notice: true, Event: kind})
	}
}

// orgManagers are the members of an organization who can manage it (manage: org), directly or
// through a team.
func (s *Users) orgManagers(ctx context.Context, orgID string) ([]OrgMember, error) {
	o := s.Settings.Organizations
	var teamIDs []string
	teams, err := s.Store.ListOrgTeams(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for _, t := range teams {
		if o.CapsOf(t.Roles).Has(registry.CapOrg) {
			teamIDs = append(teamIDs, t.ID)
		}
	}
	direct, err := s.Store.OrgMembersWithRoles(ctx, orgID, o.RolesWith(registry.CapOrg))
	if err != nil {
		return nil, err
	}
	viaTeam, err := s.Store.OrgMembersInTeams(ctx, orgID, teamIDs)
	if err != nil {
		return nil, err
	}
	var out []OrgMember
	seen := map[string]bool{}
	for _, m := range append(direct, viaTeam...) {
		if !seen[m.User] {
			seen[m.User] = true
			out = append(out, m)
		}
	}
	return out, nil
}
