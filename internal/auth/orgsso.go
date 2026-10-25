package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/registry"
)

// Organization sign-in connections (design/org-sso.md §4): a provider configured for one
// organization by its managers, with the client secret kept as an organization secret.

// Audit actions of connections; the target is org:<id>, details never hold the secret.
const (
	AuditOrgSSOSet      = "org.sso.set"      // details: provider, enabled, secret_changed
	AuditOrgSSODeleted  = "org.sso.deleted"  // details: provider
	AuditOrgSSOLogin    = "org.sso.login"    // a person signed in through a connection; details: user, provider, joined
	AuditOrgSSODisabled = "org.sso.disabled" // backd disabled a connection; details: provider, reason
)

var (
	// ErrSSODisabled: the realm doesn't let organizations set up sign-in (organizations.sso).
	ErrSSODisabled = errors.New("organization sign-in is not enabled in this realm")
	// ErrSSOProviderUnknown: the realm doesn't offer that provider to organizations.
	ErrSSOProviderUnknown = errors.New("that provider is not available to organizations")
	// ErrConnectionNotFound: the organization has no connection with that provider.
	ErrConnectionNotFound = errors.New("the organization has no sign-in connection with that provider")
	// ErrConnectionUnusable: the connection was disabled because the manager who last set it no
	// longer holds the roles it gives on joining.
	ErrConnectionUnusable = errors.New("the sign-in connection can't be used")
	// ErrSSOLimit: the organization has the most connections the realm allows.
	ErrSSOLimit = errors.New("the organization has the most sign-in connections it may")
)

// Limits of a connection's lists.
const (
	MaxConnectionEmailDomains = 20
	MaxConnectionNativeIDs    = 10
	MaxConnectionBlocked      = 1000
	// MaxConnectionSecretBytes is the longest client secret a connection takes.
	MaxConnectionSecretBytes = 1024
)

// OrgConnection is an organization's sign-in provider. The client secret is not here: it is an
// organization secret (SSOSecretName) nobody can read.
type OrgConnection struct {
	ID       string // org:provider, which is also what makes the pair unique
	Org      string
	Provider string // google or microsoft
	ClientID string
	// Tenant is Microsoft's tenant id (a GUID); HostedDomain is Google's Workspace domain.
	// The one that applies is required: they are what limits who may sign in.
	Tenant       string
	HostedDomain string
	// EmailDomains optionally narrows further: the verified email's domain must be one of them.
	EmailDomains []string
	// NativeClientIDs are extra audiences the id-token endpoint accepts (mobile apps).
	NativeClientIDs []string
	// JoinRoles are given to a person who joins the organization through the connection.
	JoinRoles []string
	Enabled   bool
	// Blocked are the ids of members a manager removed: they sign in but are not joined again
	// until invited. Kept by the sign-in code, not set through the API.
	Blocked   []string
	CreatedBy string
	UpdatedBy string // who last set it: the join roles were checked against their capabilities
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SSOSecretName is the organization secret that holds a provider's client secret.
func SSOSecretName(provider string) string {
	return "SSO_" + strings.ToUpper(provider) + "_CLIENT_SECRET"
}

// ConnectionInput is what a manager sends for a connection; PUT replaces every field.
type ConnectionInput struct {
	ClientID        string
	Tenant          string
	HostedDomain    string
	EmailDomains    []string
	NativeClientIDs []string
	JoinRoles       []string
	// Enabled nil means true for a new connection and unchanged for an existing one.
	Enabled *bool
	// ClientSecret is required when the connection has none yet and kept when empty.
	ClientSecret string
}

// ConnectionView is a connection with what the secret's state allows to say.
type ConnectionView struct {
	OrgConnection
	// SecretSet says whether the client secret is stored; the value is never returned.
	SecretSet bool
}

// ssoSettings returns the realm's organization sign-in section, ErrSSODisabled when absent.
func (s *Users) ssoSettings() (*registry.OrgSSOSettings, error) {
	o, err := s.orgs()
	if err != nil {
		return nil, ErrSSODisabled
	}
	if o.SSO == nil {
		return nil, ErrSSODisabled
	}
	return o.SSO, nil
}

// OrgConnections lists an organization's connections for a member who holds manage: sso.
func (s *Users) OrgConnections(ctx context.Context, userID, orgID string) ([]ConnectionView, error) {
	if _, err := s.ssoSettings(); err != nil {
		return nil, err
	}
	m, err := s.MembershipOf(ctx, userID, orgID)
	if err != nil {
		return nil, err
	}
	if _, err := s.requireCaps(m, registry.CapSSO); err != nil {
		return nil, err
	}
	return s.connectionViews(ctx, orgID)
}

// AdminOrgConnections lists an organization's connections for a realm administrator.
func (s *Users) AdminOrgConnections(ctx context.Context, orgID string) ([]ConnectionView, error) {
	if _, err := s.ssoSettings(); err != nil {
		return nil, err
	}
	if _, err := s.Store.OrgByID(ctx, orgID); err != nil {
		return nil, err
	}
	return s.connectionViews(ctx, orgID)
}

func (s *Users) connectionViews(ctx context.Context, orgID string) ([]ConnectionView, error) {
	list, err := s.Store.OrgConnections(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]ConnectionView, len(list))
	for i, c := range list {
		out[i] = s.connectionView(ctx, c)
	}
	return out, nil
}

func (s *Users) connectionView(ctx context.Context, c OrgConnection) ConnectionView {
	_, err := s.Store.SecretByScope(ctx, OrgScope(c.Org), SSOSecretName(c.Provider))
	return ConnectionView{OrgConnection: c, SecretSet: err == nil}
}

// OrgConnection returns one connection for a member who holds manage: sso; ErrNotFound if none.
func (s *Users) OrgConnection(ctx context.Context, userID, orgID, provider string) (ConnectionView, error) {
	if _, err := s.ssoProvider(provider); err != nil {
		return ConnectionView{}, err
	}
	m, err := s.MembershipOf(ctx, userID, orgID)
	if err != nil {
		return ConnectionView{}, err
	}
	if _, err := s.requireCaps(m, registry.CapSSO); err != nil {
		return ConnectionView{}, err
	}
	return s.oneConnection(ctx, orgID, provider)
}

// AdminOrgConnection is OrgConnection for a realm administrator.
func (s *Users) AdminOrgConnection(ctx context.Context, orgID, provider string) (ConnectionView, error) {
	if _, err := s.ssoProvider(provider); err != nil {
		return ConnectionView{}, err
	}
	if _, err := s.Store.OrgByID(ctx, orgID); err != nil {
		return ConnectionView{}, err
	}
	return s.oneConnection(ctx, orgID, provider)
}

func (s *Users) oneConnection(ctx context.Context, orgID, provider string) (ConnectionView, error) {
	c, err := s.Store.OrgConnection(ctx, orgID, provider)
	if errors.Is(err, ErrNotFound) {
		return ConnectionView{}, ErrConnectionNotFound
	}
	if err != nil {
		return ConnectionView{}, err
	}
	return s.connectionView(ctx, c), nil
}

// ssoProvider checks that the realm offers provider to organizations.
func (s *Users) ssoProvider(provider string) (*registry.OrgSSOSettings, error) {
	sso, err := s.ssoSettings()
	if err != nil {
		return nil, err
	}
	if !sso.Allows(provider) {
		return nil, ErrSSOProviderUnknown
	}
	return sso, nil
}

// OrgConnectionAllowed says whether the caller may manage the organization's connection with
// provider: the realm offers it, they are a member, and their roles hold manage: sso. The HTTP
// layer asks before it asks for a fresh proof of identity.
func (s *Users) OrgConnectionAllowed(ctx context.Context, userID, orgID, provider string) (registry.OrgCaps, error) {
	if _, err := s.ssoProvider(provider); err != nil {
		return 0, err
	}
	m, err := s.MembershipOf(ctx, userID, orgID)
	if err != nil {
		return 0, err
	}
	return s.requireCaps(m, registry.CapSSO)
}

// SetOrgConnection creates or replaces a connection for a member who holds manage: sso (the HTTP
// layer asks for a fresh proof of identity before). The roles it gives on joining follow the
// granting rules: nothing the caller couldn't give themselves.
func (s *Users) SetOrgConnection(ctx context.Context, userID, orgID, provider string, in ConnectionInput) (ConnectionView, error) {
	if _, err := s.ssoProvider(provider); err != nil {
		return ConnectionView{}, err
	}
	m, err := s.MembershipOf(ctx, userID, orgID)
	if err != nil {
		return ConnectionView{}, err
	}
	cc, err := s.requireCaps(m, registry.CapSSO)
	if err != nil {
		return ConnectionView{}, err
	}
	return s.setConnection(ctx, m.Org, cc, "user:"+userID, provider, in)
}

// AdminSetOrgConnection is SetOrgConnection for a realm administrator, who may give any role.
func (s *Users) AdminSetOrgConnection(ctx context.Context, orgID, provider string, in ConnectionInput) (ConnectionView, error) {
	if _, err := s.ssoProvider(provider); err != nil {
		return ConnectionView{}, err
	}
	org, err := s.Store.OrgByID(ctx, orgID)
	if err != nil {
		return ConnectionView{}, err
	}
	if org.Status == OrgDeleted {
		return ConnectionView{}, ErrOrgNotActive
	}
	return s.setConnection(ctx, org, registry.AllCaps, "admin", provider, in)
}

func (s *Users) setConnection(ctx context.Context, org Org, callerCaps registry.OrgCaps, by, provider string, in ConnectionInput) (ConnectionView, error) {
	sso := s.Settings.Organizations.SSO
	have, err := s.Store.OrgConnection(ctx, org.ID, provider)
	exists := err == nil
	if err != nil && !errors.Is(err, ErrNotFound) {
		return ConnectionView{}, err
	}
	c := OrgConnection{
		ID: org.ID + ":" + provider, Org: org.ID, Provider: provider, ClientID: in.ClientID,
		Tenant: in.Tenant, HostedDomain: in.HostedDomain,
		EmailDomains: in.EmailDomains, NativeClientIDs: in.NativeClientIDs, JoinRoles: uniqueStrings(in.JoinRoles),
		Enabled: true, CreatedBy: by, UpdatedBy: by,
	}
	if exists {
		c.Blocked, c.CreatedBy, c.CreatedAt, c.Enabled = have.Blocked, have.CreatedBy, have.CreatedAt, have.Enabled
	}
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
	if err := s.checkConnection(&c, callerCaps); err != nil {
		return ConnectionView{}, err
	}
	secretSet := false
	if _, err := s.Store.SecretByScope(ctx, OrgScope(org.ID), SSOSecretName(provider)); err == nil {
		secretSet = true
	}
	switch {
	case len(in.ClientSecret) > MaxConnectionSecretBytes:
		return ConnectionView{}, &OrgInvalidError{"client_secret", fmt.Sprintf("must be at most %d bytes", MaxConnectionSecretBytes)}
	case in.ClientSecret == "" && !secretSet:
		return ConnectionView{}, &OrgInvalidError{"client_secret", "is required: the connection has none yet"}
	}
	if !exists {
		list, err := s.Store.OrgConnections(ctx, org.ID)
		if err != nil {
			return ConnectionView{}, err
		}
		if len(list) >= sso.MaxPerOrg {
			return ConnectionView{}, ErrSSOLimit
		}
	}
	if in.ClientSecret != "" {
		if err := s.SetOrgSecret(ctx, org.ID, SSOSecretName(provider), in.ClientSecret, by); err != nil {
			return ConnectionView{}, err
		}
		secretSet = true
	}
	now := s.now()
	if !exists {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	if err := s.Store.PutOrgConnection(ctx, c); err != nil {
		return ConnectionView{}, err
	}
	s.Audit(ctx, AuditOrgSSOSet, orgTarget(org.ID), map[string]any{"provider": provider, "enabled": c.Enabled, "secret_changed": in.ClientSecret != ""})
	return ConnectionView{OrgConnection: c, SecretSet: secretSet}, nil
}

// checkConnection applies the rules of a connection's fields: the provider-field rules of
// realm.yaml, the restriction that says who may come in, and the granting rules for join_roles.
func (s *Users) checkConnection(c *OrgConnection, callerCaps registry.OrgCaps) error {
	bad := func(field, reason string) error { return &OrgInvalidError{field, reason} }
	if msg := registry.ClientIDProblem(c.Provider, c.ClientID); msg != "" {
		return bad("client_id", msg)
	}
	switch c.Provider {
	case registry.ProviderMicrosoft:
		if c.HostedDomain != "" {
			return bad("hosted_domain", "applies to Google only")
		}
		if msg := registry.TenantProblem(c.Tenant, false); msg != "" {
			return bad("tenant", msg)
		}
	case registry.ProviderGoogle:
		if c.Tenant != "" {
			return bad("tenant", "applies to Microsoft only")
		}
		if c.HostedDomain == "" {
			return bad("hosted_domain", "is required: the Google Workspace domain whose people may sign in (a personal Google account has none, so it is refused)")
		}
		if msg := registry.DomainProblem(c.HostedDomain); msg != "" {
			return bad("hosted_domain", msg)
		}
	}
	if len(c.EmailDomains) > MaxConnectionEmailDomains {
		return bad("email_domains", fmt.Sprintf("at most %d", MaxConnectionEmailDomains))
	}
	for i, d := range c.EmailDomains {
		if msg := registry.DomainProblem(d); msg != "" {
			return bad(fmt.Sprintf("email_domains[%d]", i), msg)
		}
		if slices.Index(c.EmailDomains, d) != i {
			return bad(fmt.Sprintf("email_domains[%d]", i), fmt.Sprintf("%q is listed twice", d))
		}
	}
	if len(c.NativeClientIDs) > MaxConnectionNativeIDs {
		return bad("native_client_ids", fmt.Sprintf("at most %d", MaxConnectionNativeIDs))
	}
	if problems := registry.ClientIDListProblems("native_client_ids", c.NativeClientIDs); len(problems) > 0 {
		return bad("native_client_ids", problems[0])
	}
	// What joining gives follows the granting rules: a manager can't give what they don't hold.
	o := s.Settings.Organizations
	for _, r := range c.JoinRoles {
		if !o.Declared(r) {
			return bad("join_roles", fmt.Sprintf("%q is not a role of this realm's organizations", r))
		}
	}
	if len(c.JoinRoles) > 0 && !s.mayGive(callerCaps, c.JoinRoles) {
		return ErrOrgForbidden
	}
	return nil
}

// mayGive says whether someone with these capabilities could give the roles by the granting rules
// (design/organizations.md §5.1): manage: roles gives any, manage: members the roles that open
// nothing the giver lacks. Holding manage: sso alone gives none: the roles of a connection go to
// every person of a company, so they are held to the rule an invitation is.
func (s *Users) mayGive(callerCaps registry.OrgCaps, roles []string) bool {
	if callerCaps.Has(registry.CapRoles) {
		return true
	}
	return callerCaps.Has(registry.CapMembers) && callerCaps.Covers(s.capsOfRoles(roles))
}

// DeleteOrgConnection removes a connection and its client secret for a member who holds
// manage: sso (the HTTP layer asks for a fresh proof first).
func (s *Users) DeleteOrgConnection(ctx context.Context, userID, orgID, provider string) error {
	if _, err := s.ssoProvider(provider); err != nil {
		return err
	}
	m, err := s.MembershipOf(ctx, userID, orgID)
	if err != nil {
		return err
	}
	if _, err := s.requireCaps(m, registry.CapSSO); err != nil {
		return err
	}
	return s.deleteConnection(ctx, orgID, provider, "user:"+userID)
}

// AdminDeleteOrgConnection is DeleteOrgConnection for a realm administrator.
func (s *Users) AdminDeleteOrgConnection(ctx context.Context, orgID, provider string) error {
	if _, err := s.ssoProvider(provider); err != nil {
		return err
	}
	if _, err := s.Store.OrgByID(ctx, orgID); err != nil {
		return err
	}
	return s.deleteConnection(ctx, orgID, provider, "admin")
}

func (s *Users) deleteConnection(ctx context.Context, orgID, provider, by string) error {
	if err := s.Store.DeleteOrgConnection(ctx, orgID, provider); errors.Is(err, ErrNotFound) {
		return ErrConnectionNotFound
	} else if err != nil {
		return err
	}
	// The secret goes with the connection; one that was never stored is no error.
	if err := s.DeleteOrgSecret(ctx, orgID, SSOSecretName(provider)); err != nil && !errors.Is(err, ErrSecretNotFound) {
		return err
	}
	// The sessions the connection opened end with it.
	if _, err := s.Store.DeleteOrgSessions(context.WithoutCancel(ctx), orgID, "", provider); err != nil && s.Log != nil {
		s.Log.Warn("end the sessions a removed connection opened", "org", orgID, "provider", provider, "error", err)
	}
	s.Audit(ctx, AuditOrgSSODeleted, orgTarget(orgID), map[string]any{"provider": provider, "by": by})
	return nil
}

// HasBlocked reports whether a manager removed the user while the connection existed.
func (c OrgConnection) HasBlocked(user string) bool { return slices.Contains(c.Blocked, user) }

// SignInClaims is what a verified ID token says that a connection's restriction checks.
type SignInClaims struct {
	Email         string
	EmailVerified bool
	// HostedDomain is Google's hd, Tenant Microsoft's tid.
	HostedDomain string
	Tenant       string
	// Username is Microsoft's preferred_username.
	Username string
}

// Admits says whether the person the token describes may come in through the connection: the
// Workspace domain or the tenant is the connection's (the cryptographic restriction), Google's
// address is verified, and the address is in one of email_domains when the connection lists any.
// A token is checked for its signature, issuer and audience before this.
func (c OrgConnection) Admits(in SignInClaims) bool {
	addr, err := registry.NormalizeEmail(in.Email)
	if err != nil {
		addr = ""
	}
	switch c.Provider {
	case registry.ProviderGoogle:
		if c.HostedDomain == "" || !strings.EqualFold(in.HostedDomain, c.HostedDomain) || !in.EmailVerified {
			return false
		}
	case registry.ProviderMicrosoft:
		// The tenant proves the company, but the address in the token is whatever the tenant's
		// administrator set: it could name someone else's mailbox and pre-empt their account. The
		// user principal name can only be on a domain the tenant verified, so the address has to be
		// on the domain of the account's own.
		if c.Tenant == "" || !strings.EqualFold(in.Tenant, c.Tenant) {
			return false
		}
		if addr != "" && (domainOf(in.Username) == "" || domainOf(in.Username) != domainOf(addr)) {
			return false
		}
	default:
		return false
	}
	if len(c.EmailDomains) == 0 {
		return true
	}
	return addr != "" && slices.Contains(c.EmailDomains, domainOf(addr))
}

// domainOf is the part of an address after its last @, in lower case; "" for none.
func domainOf(address string) string {
	i := strings.LastIndex(address, "@")
	if i < 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(address[i+1:]))
}

// CheckConnectionAuthority makes sure the roles a connection gives on joining are still within
// reach of whoever set it: a manager who lost a role doesn't leave a powerful default behind.
// A connection whose last setter could no longer give those roles by the granting rules is
// disabled (audited) and ErrConnectionUnusable is returned. A connection with no join roles, and
// connections set by a realm administrator, are always fine.
func (s *Users) CheckConnectionAuthority(ctx context.Context, c OrgConnection) error {
	by, isUser := strings.CutPrefix(c.UpdatedBy, "user:")
	if len(c.JoinRoles) == 0 || !isUser {
		return nil
	}
	holds := false
	if m, err := s.MembershipOf(ctx, by, c.Org); err == nil {
		holds = s.mayGive(s.caps(m), c.JoinRoles)
	} else if !errors.Is(err, ErrNotOrgMember) {
		return err
	}
	if holds {
		return nil
	}
	c.Enabled, c.UpdatedAt = false, s.now()
	if err := s.Store.PutOrgConnection(ctx, c); err != nil {
		return err
	}
	s.Audit(ctx, AuditOrgSSODisabled, orgTarget(c.Org), map[string]any{"provider": c.Provider, "reason": "the manager who set it no longer holds the roles it gives"})
	return ErrConnectionUnusable
}

// AfterOrgSignIn is what a successful sign-in through an organization's connection does to the
// membership (design/org-sso.md §6). The person joins with the connection's join roles only
// when this sign-in made the identity (a new person, or an explicit link): someone who is
// already a member stays as they are, and someone a manager removed stays out until invited
// again. A failure to join never fails the sign-in: the person is signed in and not joined.
func (s *Users) AfterOrgSignIn(ctx context.Context, orgID, provider string, res ProviderResult) (joined bool) {
	defer func() {
		s.Audit(ctx, AuditOrgSSOLogin, orgTarget(orgID), map[string]any{"user": res.User.ID, "provider": provider, "joined": joined})
	}()
	if !res.NewUser && !res.Linked {
		return false
	}
	o := s.Settings.Organizations
	c, err := s.Store.OrgConnection(ctx, orgID, provider)
	if err != nil || o == nil || c.HasBlocked(res.User.ID) {
		return false
	}
	org, err := s.Store.OrgByID(ctx, orgID)
	if err != nil || org.Status != OrgActive {
		return false
	}
	if _, err := s.Store.OrgMember(ctx, orgID, res.User.ID); err == nil {
		return false // a member already
	}
	if err := s.CheckConnectionAuthority(ctx, c); err != nil {
		return false
	}
	var roles []string
	for _, r := range c.JoinRoles {
		if o.Declared(r) { // a role removed from realm.yaml grants nothing
			roles = append(roles, r)
		}
	}
	if _, err := s.addMember(ctx, o, org, res.User.ID, roles, "", "sso"); err != nil {
		if s.Log != nil {
			s.Log.Warn("a person signed in through a connection could not join the organization", "org", orgID, "provider", provider, "error", err)
		}
		return false
	}
	return true
}

// RemoveOrgConnections removes every sign-in connection of an organization and every secret
// kept for it, and returns how many connections there were. It is what the cleanup of a deleted
// organization does, and it can be repeated: a second run finds nothing.
func (s *Users) RemoveOrgConnections(ctx context.Context, orgID string) (int, error) {
	list, err := s.Store.OrgConnections(ctx, orgID)
	if err != nil {
		return 0, err
	}
	for _, c := range list {
		if err := s.Store.DeleteOrgConnection(ctx, orgID, c.Provider); err != nil && !errors.Is(err, ErrNotFound) {
			return 0, err
		}
	}
	// The secrets are looked for by scope, not by connection: one outlives its connection when
	// an earlier run stopped half way.
	secrets, err := s.Store.ListSecrets(ctx)
	if err != nil {
		return 0, err
	}
	for _, sec := range secrets {
		if sec.Org == orgID {
			if err := s.DeleteOrgSecret(ctx, orgID, sec.Name); err != nil && !errors.Is(err, ErrSecretNotFound) {
				return 0, err
			}
		}
	}
	return len(list), nil
}
