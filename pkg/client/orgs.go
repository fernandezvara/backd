package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// RoleOrigin says where a role of a member comes from: "direct", "default" (the realm's
// default_roles) or the names of the teams that carry it.
type RoleOrigin struct {
	Role   string   `json:"role"`
	Origin []string `json:"origin"`
}

// Organization is an organization as a member sees it.
type Organization struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Slug   *string `json:"slug"`
	Status string  `json:"status"` // "active", "orphaned" or "deleted"
	// RequireMFA: the organization requires a second factor of its members.
	RequireMFA bool `json:"require_mfa"`
	// SSORequired: its people can only sign in through its connection.
	SSORequired bool `json:"sso_required"`
	// MembersWithoutSSO and SessionsRevoked are only in the answer that turned SSORequired on.
	MembersWithoutSSO *int `json:"members_without_sso,omitempty"`
	SessionsRevoked   *int `json:"sessions_revoked,omitempty"`
	// SessionMaxAge is the seconds the sessions opened in the organization last at most; nil: the realm's limit.
	SessionMaxAge *int `json:"session_max_age"`
	// MFAMet is false when the organization requires a second factor and this session used none.
	MFAMet bool `json:"mfa_met"`
	// Claims are written by administrators and functions; read-only here.
	Claims    map[string]any `json:"claims"`
	CreatedAt string         `json:"created_at"`
	// Roles are the user's effective role names there; EffectiveRoles say where each came from.
	Roles          []string            `json:"roles"`
	EffectiveRoles []RoleOrigin        `json:"effective_roles"`
	Teams          []string            `json:"teams"`
	TeamRoles      map[string][]string `json:"team_roles"`
	// Session is only in the answer to creating the organization: a session scoped to it.
	Session *struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
		Org       string `json:"org"`
	} `json:"session,omitempty"`
}

// OrgPatch changes an organization; nil fields stay as they are. Turning on RequireMFA or
// SSORequired, or changing SessionMaxAge, needs a recent proof (Auth.Reauth).
type OrgPatch struct {
	Name        *string `json:"name,omitempty"`
	Slug        *string `json:"slug,omitempty"`
	RequireMFA  *bool   `json:"require_mfa,omitempty"`
	SSORequired *bool   `json:"sso_required,omitempty"`
	// SessionMaxAge is in seconds (at least 3600); 0 goes back to the realm's limit.
	SessionMaxAge *int `json:"session_max_age,omitempty"`
}

// TeamRoleGrant is a team role a member holds, in force or not.
type TeamRoleGrant struct {
	Team  string `json:"team"`
	Role  string `json:"role"`
	From  string `json:"from,omitempty"`  // counts from then; always when empty
	Until string `json:"until,omitempty"` // and until then; for ever when empty
	// GrantedBy is a user id; empty for administrators and functions.
	GrantedBy string `json:"granted_by,omitempty"`
	GrantedAt string `json:"granted_at"`
	// Active: whether it counts now.
	Active bool `json:"active"`
}

// OrgMember is a member of an organization.
type OrgMember struct {
	User string `json:"user"`
	// Email is present according to the realm's members_visible_to.
	Email string `json:"email,omitempty"`
	// Roles are the direct roles.
	Roles          []string        `json:"roles"`
	Teams          []string        `json:"teams"`
	TeamRoles      []TeamRoleGrant `json:"team_roles"`
	EffectiveRoles []RoleOrigin    `json:"effective_roles"`
	// Overlap lists roles held directly and also through a team or by default.
	Overlap []string `json:"overlap"`
	// MFA and MFARoles are only for members who manage the organization.
	MFA       *bool    `json:"mfa,omitempty"`
	MFARoles  []string `json:"mfa_roles,omitempty"`
	JoinedAt  string   `json:"joined_at"`
	InvitedBy string   `json:"invited_by,omitempty"`
}

// OrgTeamMember is a member seen from one team.
type OrgTeamMember struct {
	OrgMember
	// InTeam: whether they belong to the team (a role holder needs not).
	InTeam      bool     `json:"in_team"`
	RolesInTeam []string `json:"roles_in_team"`
}

// OrgTeam is a team of an organization.
type OrgTeam struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Roles     []string `json:"roles"` // organization roles every member of the team holds
	CreatedAt string   `json:"created_at"`
}

// OrgMemberPage is a page of members.
type OrgMemberPage struct {
	Items   []OrgMember `json:"items"`
	HasMore bool        `json:"has_more"`
	// Next is the After of the next page.
	Next string `json:"next,omitempty"`
}

// OrgTeamMemberPage is a page of the members of a team.
type OrgTeamMemberPage struct {
	Items   []OrgTeamMember `json:"items"`
	HasMore bool            `json:"has_more"`
	Next    string          `json:"next,omitempty"`
}

// Orgs are the organizations of the signed-in user: c.Orgs().
type Orgs struct{ c *Client }

// Orgs returns the organizations of the signed-in user: members, teams, templates and invitations.
func (c *Client) Orgs() *Orgs { return &Orgs{c: c} }

// CreateOrgInput is the input of Orgs.Create.
type CreateOrgInput struct {
	Name string `json:"name"`
	// Slug is the short name people know the organization by: lowercase letters, digits and hyphens.
	Slug string `json:"slug"`
}

// Create makes an organization with the signed-in user as its manager. The session in the answer
// is scoped to it: with a storage that can keep a token per organization it is kept by the
// organization's client (c.Org(slug)).
func (o *Orgs) Create(ctx context.Context, in CreateOrgInput, opts ...RequestOption) (*Organization, error) {
	var org Organization
	if _, err := o.c.Do(ctx, http.MethodPost, []string{"_orgs"}, in, &org, opts...); err != nil {
		return nil, err
	}
	if s := org.Session; s != nil && s.Token != "" {
		if _, ok := o.c.rootClient().store.(SlotStorage); ok {
			ref := org.ID
			if org.Slug != nil {
				ref = *org.Slug
			}
			oc, err := o.c.Org(ref)
			if err != nil {
				return nil, err
			}
			if err := oc.auth.signedIn(ctx, &Session{Token: s.Token, ExpiresAt: s.ExpiresAt, Org: s.Org}); err != nil {
				return nil, err
			}
		}
	}
	return &org, nil
}

// SignedInOrg is an organization this program holds a session for.
type SignedInOrg struct {
	Ref       string // the id or slug the client of the organization is made with
	ID        string
	Name      string
	ExpiresAt string
}

// SignedIn lists the organizations the storage holds a session for, to show a switcher. It needs
// a storage with slots; with another it returns nothing.
func (o *Orgs) SignedIn(ctx context.Context) ([]SignedInOrg, error) {
	slots, ok := o.c.rootClient().store.(SlotStorage)
	if !ok {
		return nil, nil
	}
	names, err := slots.Slots(ctx)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []SignedInOrg
	for _, ref := range names {
		if strings.HasPrefix(ref, "meta:") {
			continue
		}
		var meta struct {
			Org       string `json:"org"`
			ExpiresAt string `json:"expires_at"`
			Name      string `json:"name"`
		}
		if raw, _ := slots.Slot("meta:" + ref).Get(ctx); raw != "" {
			_ = json.Unmarshal([]byte(raw), &meta)
		}
		out = append(out, SignedInOrg{Ref: ref, ID: meta.Org, Name: meta.Name, ExpiresAt: meta.ExpiresAt})
	}
	return out, nil
}

// Get returns an organization by id or slug.
func (o *Orgs) Get(ctx context.Context, id string, opts ...RequestOption) (*Organization, error) {
	var org Organization
	if _, err := o.c.Do(ctx, http.MethodGet, []string{"_orgs", id}, nil, &org, opts...); err != nil {
		return nil, err
	}
	return &org, nil
}

// Update changes an organization.
func (o *Orgs) Update(ctx context.Context, id string, patch OrgPatch, opts ...RequestOption) (*Organization, error) {
	if patch.RequireMFA != nil || patch.SSORequired != nil || patch.SessionMaxAge != nil {
		opts = append(opts, WithReauth())
	}
	var org Organization
	if _, err := o.c.Do(ctx, http.MethodPatch, []string{"_orgs", id}, patch, &org, opts...); err != nil {
		return nil, err
	}
	return &org, nil
}

// Leave removes the signed-in user from an organization.
func (o *Orgs) Leave(ctx context.Context, id string, opts ...RequestOption) error {
	_, err := o.c.Do(ctx, http.MethodPost, []string{"_orgs", id, "leave"}, nil, nil, opts...)
	return err
}

// Delete deletes an organization; it needs a recent proof.
func (o *Orgs) Delete(ctx context.Context, id string, opts ...RequestOption) error {
	_, err := o.c.Do(ctx, http.MethodDelete, []string{"_orgs", id}, nil, nil, append(opts, WithReauth())...)
	return err
}

// Members returns the members of an organization.
func (o *Orgs) Members(org string) *OrgMembers { return &OrgMembers{c: o.c, org: org} }

// Teams returns the teams of an organization.
func (o *Orgs) Teams(org string) *OrgTeams { return &OrgTeams{c: o.c, org: org} }

// Templates returns the email templates an organization customizes.
func (o *Orgs) Templates(org string) *OrgTemplates { return &OrgTemplates{c: o.c, org: org} }

// Invitations returns the invitations of an organization.
func (o *Orgs) Invitations(org string) *OrgInvitations { return &OrgInvitations{c: o.c, org: org} }

// SSO returns the sign-in connections of an organization.
func (o *Orgs) SSO(org string) *OrgSSO { return &OrgSSO{c: o.c, org: org} }

// PageParams page the lists of members.
type PageParams struct {
	Limit int
	// After is the Next of the previous page.
	After string
}

func (p PageParams) values() url.Values {
	v := url.Values{}
	if p.Limit != 0 {
		v.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.After != "" {
		v.Set("after", p.After)
	}
	return v
}

// OrgMembers are the members of an organization.
type OrgMembers struct {
	c   *Client
	org string
}

// MemberListParams page the members of an organization.
type MemberListParams struct {
	PageParams
	// WithoutSSO lists only the members with no identity on the organization's connection.
	WithoutSSO bool
}

// List returns a page of members.
func (m *OrgMembers) List(ctx context.Context, p MemberListParams, opts ...RequestOption) (*OrgMemberPage, error) {
	var page OrgMemberPage
	q := p.PageParams.values()
	if p.WithoutSSO {
		q.Set("sso", "false")
	}
	opts = append([]RequestOption{WithQuery(q)}, opts...)
	if _, err := m.c.Do(ctx, http.MethodGet, []string{"_orgs", m.org, "members"}, nil, &page, opts...); err != nil {
		return nil, err
	}
	return &page, nil
}

// Update sets a member's direct roles.
func (m *OrgMembers) Update(ctx context.Context, user string, roles []string, opts ...RequestOption) (*OrgMember, error) {
	var out OrgMember
	if _, err := m.c.Do(ctx, http.MethodPatch, []string{"_orgs", m.org, "members", user}, map[string][]string{"roles": roles}, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Remove removes a member from the organization.
func (m *OrgMembers) Remove(ctx context.Context, user string, opts ...RequestOption) error {
	_, err := m.c.Do(ctx, http.MethodDelete, []string{"_orgs", m.org, "members", user}, nil, nil, opts...)
	return err
}

// OrgTeams are the teams of an organization.
type OrgTeams struct {
	c   *Client
	org string
}

// TeamInput is the input of Create.
type TeamInput struct {
	Name  string   `json:"name"`
	Roles []string `json:"roles,omitempty"`
}

// TeamPatch changes a team; nil fields stay as they are.
type TeamPatch struct {
	Name  *string   `json:"name,omitempty"`
	Roles *[]string `json:"roles,omitempty"`
}

// TeamRoleInput names a team role to grant, change the window of, or revoke.
type TeamRoleInput struct {
	// User is a member of the organization (they need not belong to the team).
	User string `json:"user"`
	// Role is a team role the realm declares.
	Role  string `json:"role"`
	From  string `json:"from,omitempty"`
	Until string `json:"until,omitempty"`
}

// TeamMembersParams filter and page the members of a team.
type TeamMembersParams struct {
	// Role keeps the holders of a team role.
	Role string
	PageParams
}

func (t *OrgTeams) path(rest ...string) []string {
	return append([]string{"_orgs", t.org, "teams"}, rest...)
}

// List returns the teams.
func (t *OrgTeams) List(ctx context.Context, opts ...RequestOption) ([]OrgTeam, error) {
	var out struct {
		Items []OrgTeam `json:"items"`
	}
	if _, err := t.c.Do(ctx, http.MethodGet, t.path(), nil, &out, opts...); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// Create makes a team.
func (t *OrgTeams) Create(ctx context.Context, in TeamInput, opts ...RequestOption) (*OrgTeam, error) {
	var out OrgTeam
	if _, err := t.c.Do(ctx, http.MethodPost, t.path(), in, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Update changes a team.
func (t *OrgTeams) Update(ctx context.Context, team string, patch TeamPatch, opts ...RequestOption) (*OrgTeam, error) {
	var out OrgTeam
	if _, err := t.c.Do(ctx, http.MethodPatch, t.path(team), patch, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Delete deletes a team.
func (t *OrgTeams) Delete(ctx context.Context, team string, opts ...RequestOption) error {
	_, err := t.c.Do(ctx, http.MethodDelete, t.path(team), nil, nil, opts...)
	return err
}

// Members returns a page of the members of a team, or of the holders of one of its roles.
func (t *OrgTeams) Members(ctx context.Context, team string, p TeamMembersParams, opts ...RequestOption) (*OrgTeamMemberPage, error) {
	q := p.PageParams.values()
	if p.Role != "" {
		q.Set("role", p.Role)
	}
	var page OrgTeamMemberPage
	if _, err := t.c.Do(ctx, http.MethodGet, t.path(team, "members"), nil, &page, append([]RequestOption{WithQuery(q)}, opts...)...); err != nil {
		return nil, err
	}
	return &page, nil
}

func (t *OrgTeams) role(ctx context.Context, method, team string, in TeamRoleInput, opts []RequestOption) (*OrgTeamMember, error) {
	var out OrgTeamMember
	if _, err := t.c.Do(ctx, method, t.path(team, "roles"), in, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// GrantRole gives a member a team role, for a window of time if From and Until say so.
func (t *OrgTeams) GrantRole(ctx context.Context, team string, in TeamRoleInput, opts ...RequestOption) (*OrgTeamMember, error) {
	return t.role(ctx, http.MethodPost, team, in, opts)
}

// SetRoleWindow changes the window of a granted team role.
func (t *OrgTeams) SetRoleWindow(ctx context.Context, team string, in TeamRoleInput, opts ...RequestOption) (*OrgTeamMember, error) {
	return t.role(ctx, http.MethodPatch, team, in, opts)
}

// RevokeRole takes a team role away from a member.
func (t *OrgTeams) RevokeRole(ctx context.Context, team string, in TeamRoleInput, opts ...RequestOption) (*OrgTeamMember, error) {
	return t.role(ctx, http.MethodDelete, team, in, opts)
}

// AddMember puts a member of the organization in a team.
func (t *OrgTeams) AddMember(ctx context.Context, team, user string, opts ...RequestOption) (*OrgMember, error) {
	var out OrgMember
	if _, err := t.c.Do(ctx, http.MethodPut, t.path(team, "members", user), nil, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// RemoveMember takes a member out of a team.
func (t *OrgTeams) RemoveMember(ctx context.Context, team, user string, opts ...RequestOption) (*OrgMember, error) {
	var out OrgMember
	if _, err := t.c.Do(ctx, http.MethodDelete, t.path(team, "members", user), nil, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// OrgTemplates are the email templates an organization customizes.
type OrgTemplates struct {
	c   *Client
	org string
}

// TemplateText is the text of a template.
type TemplateText struct {
	Subject string `json:"subject"`
	Text    string `json:"text"`
	HTML    string `json:"html"`
}

// OrgTemplate is a customized template, with its older versions (newest first, at most 20).
type OrgTemplate struct {
	Kind      string `json:"kind"`
	Locale    string `json:"locale"`
	Version   int    `json:"version"`
	UpdatedAt string `json:"updated_at"`
	UpdatedBy string `json:"updated_by,omitempty"`
	// Subject, Text and HTML are only set when one template is read.
	Subject  string `json:"subject,omitempty"`
	Text     string `json:"text,omitempty"`
	HTML     string `json:"html,omitempty"`
	Versions []struct {
		TemplateText
		Version int    `json:"version"`
		At      string `json:"at"`
		By      string `json:"by,omitempty"`
	} `json:"versions"`
	// Removed is only in the answer to a save: what the HTML sanitizer drops from the sample.
	Removed []string `json:"removed,omitempty"`
}

// OrgTemplateList is the answer to listing templates.
type OrgTemplateList struct {
	// Customizable maps the kinds an organization may customize to the placeholders each must print.
	Customizable map[string]struct {
		Required []string `json:"required"`
	} `json:"customizable"`
	Items []OrgTemplate `json:"items"`
}

// TemplatePreview is a rendered template, already sanitized.
type TemplatePreview struct {
	TemplateText
	// Removed is what the sanitizer dropped.
	Removed []string `json:"removed"`
}

func (t *OrgTemplates) path(rest ...string) []string {
	return append([]string{"_orgs", t.org, "templates"}, rest...)
}

// List returns the templates the organization has customized, and the kinds it may.
func (t *OrgTemplates) List(ctx context.Context, opts ...RequestOption) (*OrgTemplateList, error) {
	var out OrgTemplateList
	if _, err := t.c.Do(ctx, http.MethodGet, t.path(), nil, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Get returns one template.
func (t *OrgTemplates) Get(ctx context.Context, kind, locale string, opts ...RequestOption) (*OrgTemplate, error) {
	var out OrgTemplate
	if _, err := t.c.Do(ctx, http.MethodGet, t.path(kind, locale), nil, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Put saves a template.
func (t *OrgTemplates) Put(ctx context.Context, kind, locale string, text TemplateText, opts ...RequestOption) (*OrgTemplate, error) {
	var out OrgTemplate
	if _, err := t.c.Do(ctx, http.MethodPut, t.path(kind, locale), text, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Delete goes back to the default template.
func (t *OrgTemplates) Delete(ctx context.Context, kind, locale string, opts ...RequestOption) error {
	_, err := t.c.Do(ctx, http.MethodDelete, t.path(kind, locale), nil, nil, opts...)
	return err
}

// PreviewInput is the input of Preview: a draft to render (the saved template when nil) with sample data.
type PreviewInput struct {
	Draft *TemplateText
	Data  map[string]any
}

// Preview renders a template with sample data.
func (t *OrgTemplates) Preview(ctx context.Context, kind, locale string, in PreviewInput, opts ...RequestOption) (*TemplatePreview, error) {
	body := map[string]any{}
	if in.Draft != nil {
		body["subject"], body["text"], body["html"] = in.Draft.Subject, in.Draft.Text, in.Draft.HTML
	}
	if in.Data != nil {
		body["data"] = in.Data
	}
	var out TemplatePreview
	if _, err := t.c.Do(ctx, http.MethodPost, t.path(kind, locale, "preview"), body, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Test sends a draft (or the saved template) to the signed-in user's address.
func (t *OrgTemplates) Test(ctx context.Context, kind, locale string, draft *TemplateText, opts ...RequestOption) error {
	var body any = map[string]any{}
	if draft != nil {
		body = draft
	}
	_, err := t.c.Do(ctx, http.MethodPost, t.path(kind, locale, "test"), body, nil, opts...)
	return err
}

// OrgInvitations are the invitations of an organization.
type OrgInvitations struct {
	c   *Client
	org string
}

// OrgInvitation is an invitation to join an organization.
type OrgInvitation struct {
	ID string `json:"id"`
	// Binding says who may accept: "user" (one user), "email" (whoever holds a verified address) or
	// "none" (anyone signed in with the link).
	Binding string `json:"binding"`
	// Status is "pending", "used", "revoked" or "expired".
	Status    string   `json:"status"`
	User      string   `json:"user,omitempty"`
	Email     string   `json:"email,omitempty"`
	Roles     []string `json:"roles"`
	Teams     []string `json:"teams"`
	MaxUses   int      `json:"max_uses"`
	Uses      int      `json:"uses"`
	CreatedAt string   `json:"created_at"`
	ExpiresAt string   `json:"expires_at"`
}

// NewOrgInvitation is an invitation with its token and link, shown only when it is made or renewed.
type NewOrgInvitation struct {
	OrgInvitation
	Token string  `json:"token"`
	Link  *string `json:"link"`
}

// InviteInput is the input of Create.
type InviteInput struct {
	Binding string   `json:"binding"`
	User    string   `json:"user,omitempty"`  // for "user"
	Email   string   `json:"email,omitempty"` // for "email"
	Roles   []string `json:"roles,omitempty"`
	Teams   []string `json:"teams,omitempty"` // team ids
	// Send also emails it (only "email").
	Send bool `json:"send,omitempty"`
	// ExpiresIn is a duration such as "2d" (default 7d, at most 30d).
	ExpiresIn string `json:"expires_in,omitempty"`
	// MaxUses is for "none"; 1 by default.
	MaxUses int `json:"max_uses,omitempty"`
}

func (i *OrgInvitations) path(rest ...string) []string {
	return append([]string{"_orgs", i.org, "invitations"}, rest...)
}

// Create makes an invitation.
func (i *OrgInvitations) Create(ctx context.Context, in InviteInput, opts ...RequestOption) (*NewOrgInvitation, error) {
	var out NewOrgInvitation
	if _, err := i.c.Do(ctx, http.MethodPost, i.path(), in, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// List returns the invitations.
func (i *OrgInvitations) List(ctx context.Context, opts ...RequestOption) ([]OrgInvitation, error) {
	var out struct {
		Items []OrgInvitation `json:"items"`
	}
	if _, err := i.c.Do(ctx, http.MethodGet, i.path(), nil, &out, opts...); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// Revoke cancels an invitation.
func (i *OrgInvitations) Revoke(ctx context.Context, id string, opts ...RequestOption) error {
	_, err := i.c.Do(ctx, http.MethodDelete, i.path(id), nil, nil, opts...)
	return err
}

// NewLink renews an invitation's token and link.
func (i *OrgInvitations) NewLink(ctx context.Context, id string, opts ...RequestOption) (*NewOrgInvitation, error) {
	var out NewOrgInvitation
	if _, err := i.c.Do(ctx, http.MethodPost, i.path(id, "link"), nil, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Send emails an invitation to its address.
func (i *OrgInvitations) Send(ctx context.Context, id string, opts ...RequestOption) error {
	_, err := i.c.Do(ctx, http.MethodPost, i.path(id, "send"), nil, nil, opts...)
	return err
}

// OrgSSO are the sign-in connections of an organization (its own Google Workspace or Microsoft tenant).
type OrgSSO struct {
	c   *Client
	org string
}

// OrgConnection is an organization's sign-in provider.
type OrgConnection struct {
	Provider string `json:"provider"` // "google" or "microsoft"
	ClientID string `json:"client_id"`
	// Tenant is the Microsoft tenant id; HostedDomain the Google Workspace domain.
	Tenant       string `json:"tenant,omitempty"`
	HostedDomain string `json:"hosted_domain,omitempty"`
	// EmailDomains: when set, the verified address must also be in one of them.
	EmailDomains []string `json:"email_domains"`
	// NativeClientIDs are extra client ids the id-token sign-in accepts (mobile apps).
	NativeClientIDs []string `json:"native_client_ids"`
	// JoinRoles are the organization roles given to people who join through it.
	JoinRoles []string `json:"join_roles"`
	Enabled   bool     `json:"enabled"`
	// ClientSecretSet: without a secret the connection is unavailable.
	ClientSecretSet bool `json:"client_secret_set"`
	// RedirectURI is the address to register with the provider (when backd knows its public address).
	RedirectURI  string `json:"redirect_uri,omitempty"`
	RedirectPath string `json:"redirect_path"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// OrgConnections are the connections of an organization.
type OrgConnections struct {
	Items []OrgConnection `json:"items"`
	// Providers are the providers the realm lets organizations connect.
	Providers      []string `json:"providers,omitempty"`
	MaxConnections int      `json:"max_connections,omitempty"`
}

// ConnectionInput is the input of Set. Nil and empty fields are left out.
type ConnectionInput struct {
	ClientID string
	// ClientSecret is required the first time; left out, the stored one is kept.
	ClientSecret string
	// Tenant: Microsoft, required, a tenant id (never common, organizations or consumers).
	Tenant string
	// HostedDomain: Google, required, the Workspace domain.
	HostedDomain    string
	EmailDomains    []string
	NativeClientIDs []string
	JoinRoles       []string
	// Enabled is true for a new connection; unchanged on a replace.
	Enabled *bool
}

func (in ConnectionInput) body() map[string]any {
	b := map[string]any{"client_id": in.ClientID}
	if in.ClientSecret != "" {
		b["client_secret"] = in.ClientSecret
	}
	if in.Tenant != "" {
		b["tenant"] = in.Tenant
	}
	if in.HostedDomain != "" {
		b["hosted_domain"] = in.HostedDomain
	}
	if in.EmailDomains != nil {
		b["email_domains"] = in.EmailDomains
	}
	if in.NativeClientIDs != nil {
		b["native_client_ids"] = in.NativeClientIDs
	}
	if in.JoinRoles != nil {
		b["join_roles"] = in.JoinRoles
	}
	if in.Enabled != nil {
		b["enabled"] = *in.Enabled
	}
	return b
}

// List returns the organization's connections.
func (s *OrgSSO) List(ctx context.Context, opts ...RequestOption) (*OrgConnections, error) {
	var out OrgConnections
	if _, err := s.c.Do(ctx, http.MethodGet, []string{"_orgs", s.org, "sso"}, nil, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Get returns one connection.
func (s *OrgSSO) Get(ctx context.Context, provider string, opts ...RequestOption) (*OrgConnection, error) {
	var out OrgConnection
	if _, err := s.c.Do(ctx, http.MethodGet, []string{"_orgs", s.org, "sso", provider}, nil, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Set creates or replaces a connection; it needs a recent proof.
func (s *OrgSSO) Set(ctx context.Context, provider string, in ConnectionInput, opts ...RequestOption) (*OrgConnection, error) {
	var out OrgConnection
	if _, err := s.c.Do(ctx, http.MethodPut, []string{"_orgs", s.org, "sso", provider}, in.body(), &out, append(opts, WithReauth())...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Delete removes a connection; it needs a recent proof.
func (s *OrgSSO) Delete(ctx context.Context, provider string, opts ...RequestOption) error {
	_, err := s.c.Do(ctx, http.MethodDelete, []string{"_orgs", s.org, "sso", provider}, nil, nil, append(opts, WithReauth())...)
	return err
}
