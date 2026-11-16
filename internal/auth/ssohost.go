package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/sso"
)

// The sso engine (design/sso.md §3) runs sign-ins with external providers over four ports; this
// file is backd's side of them for one realm: SSOHost binds a realm's Users and builds the
// engine. Scopes are "realm" for the realm's own providers and "org:<id>" for the connections of
// an organization.

// SSOScopeRealm is the scope of the realm's own providers.
const SSOScopeRealm = "realm"

// SSOScopeOf is the scope of the callback of a provider: the realm's, or its connection's organization.
func SSOScopeOf(org string) string {
	if org == "" {
		return SSOScopeRealm
	}
	return "org:" + org
}

// UnknownOrgScope is the organization scope of an attempt started at the address of an organization
// that can't be signed in to (it is not there, or is deleted): the attempt goes on like any other, so
// nothing says so, and ends in the refusal a stranger to a real organization gets.
const UnknownOrgScope = "?"

// SSOHostData is what a sign-in attempt carries for backd: the engine stores it with the attempt
// and hands it back untouched.
type SSOHostData struct {
	// ScopeOrg is the organization the person signs in to (the session records it): the
	// connection's organization, or for a realm provider used to sign in to one, that
	// organization. UnknownOrgScope for one that can't be signed in to.
	ScopeOrg string `json:"scope_org,omitempty"`
	// Invitation is the hash of the invitation token the attempt carried.
	Invitation string `json:"invitation,omitempty"`
	// Locales are the languages asked for, best first.
	Locales []string `json:"locales,omitempty"`
	// Consents are the consents the app accepted for a sign-up through the attempt.
	Consents map[string]string `json:"consents,omitempty"`
}

// Encode is the bytes the engine stores.
func (d SSOHostData) Encode() []byte {
	b, _ := json.Marshal(d)
	return b
}

// DecodeSSOHost reads what SSOHostData.Encode wrote.
func DecodeSSOHost(raw []byte) SSOHostData {
	var d SSOHostData
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &d)
	}
	return d
}

// SSOHost is a realm's side of the sso engine's ports.
type SSOHost struct {
	Users *Users
	Realm string
	// BaseURL is backd's public address for this realm (email.public_url, else BACKD_URL).
	BaseURL string
	// Endpoints overrides a provider's URLs (tests point it at a fake); the built-in ones when nil.
	Endpoints func(*registry.Provider) sso.Endpoints

	mu    sync.Mutex
	known map[string]User // the users returned as accounts, for the effects that need them whole
}

// Ports are the four ports of the engine over this host, for tests and for callers that build their own.
func (h *SSOHost) Ports() (sso.Store, sso.Catalog, sso.Directory, sso.Events) {
	return ssoStore{h}, ssoCatalog{h}, ssoDirectory{h}, ssoEvents{h}
}

// Engine is the engine over this host and the given protocol.
func (h *SSOHost) Engine(proto sso.Protocol) *sso.Engine {
	st, cat, dir, ev := h.Ports()
	return &sso.Engine{Store: st, Catalog: cat, Directory: dir, Events: ev, Protocol: proto, Now: h.Users.now}
}

// ProviderOf is the provider as the engine uses it, for a realm provider or a connection's:
// secrets read, endpoints set, the restrictions and the authority check attached. ok is false when
// a secret it needs is not set (the provider is then unavailable for a redirect flow; the
// provider returned has no secret then, which a native sign-in of Google or Microsoft does not need).
func (h *SSOHost) ProviderOf(ctx context.Context, p *registry.Provider, conn *OrgConnection) (sso.Provider, bool, error) {
	secrets, ok, err := h.Users.ProviderSecrets(ctx, p)
	if err != nil {
		return sso.Provider{}, false, err
	}
	sp := p.SSO()
	if h.Endpoints != nil {
		sp.Endpoints = h.Endpoints(p)
	}
	sp.Org = p.Org
	sp.Method = p.Name
	if p.Org != "" {
		sp.Method = ConnectionMethod(p.Org, p.Name)
	}
	if p.TrustsEmail() {
		sp.Link = sso.LinkVerifiedEmail
	}
	sp.Secret = secrets[p.ClientSecret]
	if p.Name == registry.ProviderApple {
		sp.Secret = secrets[p.PrivateKey]
		sp.RevokeOnDelete = p.RevokeOnDelete
	}
	if conn != nil {
		c := *conn
		h.linkByDomain(ctx, &sp, c)
		sp.Admit = func(cl sso.Claims) bool {
			return c.Admits(SignInClaims{Email: cl.Email, EmailVerified: cl.EmailVerified, HostedDomain: cl.HostedDomain, Tenant: cl.Tenant, Username: cl.Username})
		}
		sp.Authority = func(ctx context.Context) error { return h.Users.CheckConnectionAuthority(ctx, c) }
	}
	return sp, ok, nil
}

// linkByDomain lets a connection link existing accounts for the addresses on the verified domains of
// its organization that allow it (design/sso.md §10.4). The domains are read each time the provider
// is resolved, so one that lapsed stops linking at once.
func (h *SSOHost) linkByDomain(ctx context.Context, sp *sso.Provider, c OrgConnection) {
	if h.Users.Settings.Organizations == nil || h.Users.Settings.Organizations.Domains == nil {
		return
	}
	list, err := h.Users.Store.OrgDomains(ctx, c.Org)
	if err != nil {
		return
	}
	for _, d := range list {
		if d.State == DomainVerified && d.LinkAccounts {
			sp.LinkDomains = append(sp.LinkDomains, d.Domain)
		}
	}
	if len(sp.LinkDomains) == 0 {
		return
	}
	sp.Link = sso.LinkVerifiedDomain
	// Google says whether an address is verified; a Microsoft tenant only vouches for an address on the
	// domain of its account's name, a domain the tenant has verified (Admits has checked that already).
	sp.VerifiesEmail = func(cl sso.Claims) bool {
		if c.Provider == registry.ProviderMicrosoft {
			return cl.Username != "" && domainOf(cl.Username) == domainOf(cl.Email)
		}
		return cl.EmailVerified
	}
}

// UserOf returns a user the engine returned as an account (or reads it).
func (h *SSOHost) UserOf(ctx context.Context, id string) (User, error) { return h.user(ctx, id) }

// ProviderSecrets reads the secrets the provider needs, by name; ok is false when one is not set.
func (s *Users) ProviderSecrets(ctx context.Context, p *registry.Provider) (map[string]string, bool, error) {
	if p.Org != "" {
		// An organization's secret is read where only the sign-in code looks.
		v, ok, err := s.OrgSecret(ctx, p.Org, p.ClientSecret)
		if err != nil || !ok {
			return nil, false, err
		}
		return map[string]string{p.ClientSecret: v}, true, nil
	}
	names := p.SecretNames()
	refs := make([]registry.SecretRef, len(names))
	for i, n := range names {
		refs[i] = registry.SecretRef{Realm: true, Name: n}
	}
	values, missing, err := s.ResolveSecrets(ctx, refs, "")
	if err != nil || len(missing) > 0 {
		return nil, false, err
	}
	out := make(map[string]string, len(names))
	for _, n := range names {
		out[n] = values["realm."+n]
	}
	return out, true, nil
}

// ---- Store

type ssoStore struct{ h *SSOHost }

func (s ssoStore) PutAttempt(ctx context.Context, key string, a sso.Attempt) error {
	hd := DecodeSSOHost(a.Host)
	now := s.h.Users.now()
	return s.h.Users.Store.PutOAuthState(ctx, OAuthState{
		ID: key, Provider: a.Provider, Strategy: a.Strategy, Org: a.Org, ScopeOrg: hd.ScopeOrg, Intent: string(a.Intent),
		CodeChallenge: a.Challenge, Verifier: a.Verifier, Nonce: a.Nonce, RedirectTo: a.RedirectTo,
		InvitationID: hd.Invitation, LinkUserID: a.LinkUserID, Locales: hd.Locales, Consents: hd.Consents, Host: a.Host,
		CreatedAt: now, ExpiresAt: a.ExpiresAt,
	})
}

func (s ssoStore) TakeAttempt(ctx context.Context, key string) (sso.Attempt, error) {
	st, err := s.h.Users.Store.ClaimOAuthState(ctx, key, s.h.Users.now())
	if errors.Is(err, ErrNotFound) {
		return sso.Attempt{}, sso.ErrNotFound
	}
	if err != nil {
		return sso.Attempt{}, err
	}
	host := st.Host
	if len(host) == 0 {
		host = SSOHostData{ScopeOrg: st.ScopeOrg, Invitation: st.InvitationID, Locales: st.Locales, Consents: st.Consents}.Encode()
	}
	return sso.Attempt{
		Provider: st.Provider, Scope: SSOScopeOf(st.Org), Org: st.Org, Intent: sso.Intent(st.Intent), Strategy: st.Strategy,
		Challenge: st.CodeChallenge, Verifier: st.Verifier, Nonce: st.Nonce, RedirectTo: st.RedirectTo, LinkUserID: st.LinkUserID,
		Host:      host,
		ExpiresAt: st.ExpiresAt,
	}, nil
}

func (s ssoStore) PutCode(ctx context.Context, key string, c sso.Code) error {
	return s.h.Users.Store.PutLoginCode(ctx, LoginCode{
		ID: key, UserID: c.UserID, Method: c.Method, Strategy: c.Strategy, CodeChallenge: c.Challenge,
		NewUser: c.NewUser, Profile: c.Profile, Org: c.Scope, CreatedAt: s.h.Users.now(), ExpiresAt: c.ExpiresAt,
	})
}

func (s ssoStore) TakeCode(ctx context.Context, key string) (sso.Code, error) {
	lc, err := s.h.Users.Store.ClaimLoginCode(ctx, key, s.h.Users.now())
	if errors.Is(err, ErrNotFound) {
		return sso.Code{}, sso.ErrNotFound
	}
	if err != nil {
		return sso.Code{}, err
	}
	return sso.Code{
		UserID: lc.UserID, Method: lc.Method, Strategy: lc.Strategy, Challenge: lc.CodeChallenge,
		NewUser: lc.NewUser, Profile: lc.Profile, Scope: lc.Org, ExpiresAt: lc.ExpiresAt,
	}, nil
}

// ---- Catalog

type ssoCatalog struct{ h *SSOHost }

func (c ssoCatalog) Provider(ctx context.Context, scope, name string) (sso.Provider, error) {
	u := c.h.Users
	var p *registry.Provider
	var conn *OrgConnection
	switch orgID, isOrg := strings.CutPrefix(scope, "org:"); {
	case scope == SSOScopeRealm:
		if p = u.Settings.Providers[name]; p == nil {
			return sso.Provider{}, sso.ErrNotFound
		}
	case isOrg:
		o := u.Settings.Organizations
		if o == nil || o.SSO == nil || !o.SSO.Allows(name) {
			return sso.Provider{}, sso.ErrNotFound
		}
		org, err := u.Store.OrgByID(ctx, orgID)
		if err != nil || org.Status != OrgActive {
			return sso.Provider{}, sso.ErrNotFound
		}
		oc, err := u.Store.OrgConnection(ctx, orgID, name)
		if err != nil || !oc.Enabled {
			return sso.Provider{}, sso.ErrNotFound
		}
		conn = &oc
		p = ConnectionProvider(oc)
	default:
		return sso.Provider{}, sso.ErrNotFound
	}
	sp, ok, err := c.h.ProviderOf(ctx, p, conn)
	if err != nil {
		return sso.Provider{}, err
	}
	if !ok {
		return sso.Provider{}, sso.ErrNotFound
	}
	return sp, nil
}

// ConnectionProvider is the provider an organization's connection stands for.
func ConnectionProvider(c OrgConnection) *registry.Provider {
	return &registry.Provider{
		Name: c.Provider, Kind: c.Provider, ClientID: c.ClientID, ClientSecret: SSOSecretName(c.Provider),
		NativeClientIDs: c.NativeClientIDs, Tenant: c.Tenant, Org: c.Org,
	}
}

func (c ssoCatalog) CallbackURL(_ string, p sso.Provider) (string, bool) {
	base := c.h.BaseURL
	if base == "" {
		return "", false
	}
	base = strings.TrimSuffix(base, "/")
	if p.Org != "" {
		return base + registry.OrgProviderCallbackPath(c.h.Realm, p.Org, p.Name), true
	}
	return base + registry.ProviderCallbackPath(c.h.Realm, p.Name), true
}

// ---- Directory

type ssoDirectory struct{ h *SSOHost }

func accountOf(u User) sso.Account {
	return sso.Account{ID: u.ID, Email: u.Email, EmailVerified: u.EmailVerified, Disabled: u.Disabled, Erased: !u.ErasedAt.IsZero()}
}

// remember returns the user as an account and keeps it for the effects that need it whole.
func (h *SSOHost) remember(u User) sso.Account {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.known == nil {
		h.known = map[string]User{}
	}
	h.known[u.ID] = u
	return accountOf(u)
}

func (h *SSOHost) user(ctx context.Context, id string) (User, error) {
	h.mu.Lock()
	u, ok := h.known[id]
	h.mu.Unlock()
	if ok {
		return u, nil
	}
	return h.Users.Store.UserByID(ctx, id)
}

// ssoError turns an error of the account rules into the library's. The original stays in the
// chain, so a caller can still tell ErrInvitationRequired from ErrInvalidInvitation.
func ssoError(err error) error {
	var refused *RefusedError
	var mapped error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &refused):
		mapped = &sso.Refused{UserID: refused.UserID, Reason: refused.Reason}
	case errors.Is(err, ErrAccountExists):
		mapped = sso.ErrAccountExists
	case errors.Is(err, ErrLinkConflict):
		mapped = sso.ErrLinkConflict
	case errors.Is(err, ErrSignupClosed):
		mapped = sso.ErrSignupClosed
	case errors.Is(err, ErrInvitationRequired), errors.Is(err, ErrInvalidInvitation):
		mapped = sso.ErrInvitationInvalid
	case errors.Is(err, ErrEmailNotVerified):
		mapped = sso.ErrEmailNotVerified
	case errors.Is(err, ErrEmailRequired):
		mapped = sso.ErrEmailRequired
	case errors.Is(err, ErrNotFound):
		return sso.ErrNotFound
	default:
		return err
	}
	return fmt.Errorf("%w: %w", mapped, err)
}

func (d ssoDirectory) NormalizeEmail(raw string) string {
	if raw == "" {
		return ""
	}
	email, err := registry.NormalizeEmail(raw)
	if err != nil {
		return ""
	}
	return email
}

func (d ssoDirectory) Identity(ctx context.Context, k sso.IdentityKey) (sso.Account, error) {
	id, err := d.h.Users.Store.IdentityIn(ctx, k.Provider, k.Org, k.Issuer, k.Subject)
	if err != nil {
		return sso.Account{}, ssoError(err)
	}
	u, err := d.h.user(ctx, id.UserID)
	if errors.Is(err, ErrNotFound) {
		return sso.Account{}, &sso.Refused{UserID: id.UserID, Reason: "user missing"}
	}
	if err != nil {
		return sso.Account{}, err
	}
	return d.h.remember(u), nil
}

func (d ssoDirectory) User(ctx context.Context, id string) (sso.Account, error) {
	u, err := d.h.Users.Store.UserByID(ctx, id)
	if err != nil {
		return sso.Account{}, ssoError(err)
	}
	return d.h.remember(u), nil
}

func (d ssoDirectory) UserByEmail(ctx context.Context, email string) (sso.Account, error) {
	u, err := d.h.Users.Store.UserByEmail(ctx, email)
	if err != nil {
		return sso.Account{}, ssoError(err)
	}
	return d.h.remember(u), nil
}

func (d ssoDirectory) MaySignIn(ctx context.Context, a sso.Account, req sso.Request) error {
	u, err := d.h.user(ctx, a.ID)
	if err != nil {
		return err
	}
	return ssoError(d.h.Users.mayStartSession(u, req.IP))
}

func providerLoginOf(l sso.Login) ProviderLogin {
	hd := DecodeSSOHost(l.Host)
	in := ProviderLogin{
		Provider: l.Provider.Name, Org: l.Provider.Org, Issuer: l.Claims.Issuer, Subject: l.Claims.Subject, Email: l.Claims.Email, EmailVerified: l.Claims.EmailVerified,
		TrustsEmail: l.Provider.Link == sso.LinkVerifiedEmail, Intent: string(l.Intent), LinkUserID: l.LinkUserID, Locales: hd.Locales,
		IP: l.Request.IP, Consents: hd.Consents, UserAgent: l.Request.UserAgent, Profile: l.Profile,
	}
	InvitationOfScope(&in, hd.Invitation, hd.ScopeOrg)
	return in
}

// InvitationOfScope puts the invitation of a sign-in attempt where it belongs: with an
// organization it is that organization's, which lets a new person make an account and join;
// otherwise it is an account invitation of the realm.
func InvitationOfScope(login *ProviderLogin, hash, scopeOrg string) {
	switch scopeOrg {
	case "":
		login.Invitation = hash
	case UnknownOrgScope:
	default:
		login.ScopeOrg, login.OrgInvitation = scopeOrg, hash
	}
}

func (d ssoDirectory) Touch(ctx context.Context, k sso.IdentityKey, l sso.Login, email string) {
	id, err := d.h.Users.Store.IdentityIn(ctx, k.Provider, k.Org, k.Issuer, k.Subject)
	if err != nil {
		return
	}
	d.h.Users.recordIdentityUse(ctx, id, providerLoginOf(l), email)
}

func (d ssoDirectory) Link(ctx context.Context, a sso.Account, l sso.Login, email string) error {
	u, err := d.h.user(ctx, a.ID)
	if err != nil {
		return err
	}
	in := providerLoginOf(l)
	err = ssoError(d.h.Users.addIdentity(ctx, u, in, email))
	// A signed-in person linking again what an issuer change left dormant replaces their own old
	// identity; no sign-in ever does (design/sso.md §6.1).
	if errors.Is(err, sso.ErrLinkConflict) && l.Intent == sso.IntentLink && d.h.Users.replaceDormant(ctx, u, in) {
		err = ssoError(d.h.Users.addIdentity(ctx, u, in, email))
	}
	return err
}

// Create makes the user as the realm's sign-up mode allows. The address counts as verified when the
// provider is trusted and says so, which is what verified already says.
func (d ssoDirectory) Create(ctx context.Context, l sso.Login, email string, _ bool) (sso.Account, error) {
	res, err := d.h.Users.signUpWithProvider(ctx, providerLoginOf(l), email)
	if res.User.ID == "" {
		return sso.Account{}, ssoError(err)
	}
	// A user made but not allowed to start a session: the engine asks MaySignIn and gets the same answer.
	return d.h.remember(res.User), nil
}

func (d ssoDirectory) Enter(ctx context.Context, e sso.Entry) (string, bool) {
	hd := DecodeSSOHost(e.Attempt.Host)
	p := e.Login.Provider
	u, err := d.h.user(ctx, e.Result.Account.ID)
	if err != nil {
		return "", false
	}
	res := ProviderResult{User: u, NewUser: e.Result.NewUser, Linked: e.Result.Linked, Profile: e.Result.Profile}
	if e.Login.Intent == sso.IntentLink {
		if p.Org != "" {
			d.h.Users.AfterOrgSignIn(ctx, p.Org, p.Name, res)
		}
		return "", true
	}
	switch hd.ScopeOrg {
	case "":
		return "", true
	case UnknownOrgScope:
		return "", false
	}
	member := d.h.Users.EnterOrg(ctx, u, hd.ScopeOrg, hd.Invitation)
	if p.Org != "" {
		d.h.Users.AfterOrgSignIn(ctx, p.Org, p.Name, res)
		return hd.ScopeOrg, true
	}
	return hd.ScopeOrg, member
}

func (d ssoDirectory) Superseded(ctx context.Context, a sso.Attempt, p sso.Provider) string {
	scopeOrg := DecodeSSOHost(a.Host).ScopeOrg
	if scopeOrg == "" || scopeOrg == UnknownOrgScope || p.Org != "" {
		return ""
	}
	// The realm's own providers are no way into an organization that requires its connection.
	if _, required := d.h.Users.OrgSSORequired(ctx, scopeOrg); required {
		return sso.CodeSSORequired
	}
	return ""
}

func (d ssoDirectory) CleanProfile(c sso.Claims, extra map[string]string) sso.Profile {
	in := map[string]string{"name": c.Name, "given_name": c.GivenName, "family_name": c.FamilyName, "picture": c.Picture}
	for k, v := range extra {
		if in[k] == "" {
			in[k] = v
		}
	}
	return CleanProfile(in)
}

// ---- Events

type ssoEvents struct{ h *SSOHost }

func (e ssoEvents) Emit(ctx context.Context, ev sso.Event) {
	switch ev.Kind {
	case sso.EventRefused:
		e.h.Users.Audit(ctx, AuditSignInRefused, "user:"+ev.UserID, map[string]any{"provider": ev.Provider.Name, "reason": ev.Reason})
	case sso.EventLinkedByDomain:
		u, err := e.h.user(ctx, ev.UserID)
		if err != nil {
			return
		}
		e.h.Users.Audit(ctx, AuditOrgSSOLinked, orgTarget(ev.Provider.Org), map[string]any{"user": u.ID, "provider": ev.Provider.Name, "by": "domain"})
		if e.h.Users.Settings.Email != nil {
			e.h.Users.queueNotice(ctx, EmailRequest{Kind: email.SecurityNotice, UserID: u.ID, Address: u.Email, Locale: e.h.Users.LocaleOf(u), Org: ev.Provider.Org, Notice: true, Event: "sso-linked"})
		}
	case sso.EventAppleToken:
		if err := e.h.Users.SaveAppleToken(ctx, ev.UserID, ev.Audience, ev.Token); err != nil && e.h.Users.Log != nil {
			e.h.Users.Log.Error("keep Apple's refresh token for revocation", "error", err)
		}
	}
}

// ResolveProviderLogin decides who a provider's person is under the rules of design/sso.md §8.1:
// the engine's decision, carried out on the realm's accounts. It starts no session. The sign-in
// handlers run it inside the engine; this is the same decision for callers that already hold the
// verified login.
func (s *Users) ResolveProviderLogin(ctx context.Context, in ProviderLogin) (ProviderResult, error) {
	h := &SSOHost{Users: s}
	hd := SSOHostData{Locales: in.Locales, Consents: in.Consents, Invitation: in.Invitation}
	if in.ScopeOrg != "" {
		hd.ScopeOrg, hd.Invitation = in.ScopeOrg, in.OrgInvitation
	}
	link := sso.LinkNever
	if in.TrustsEmail {
		link = sso.LinkVerifiedEmail
	}
	res, err := h.Engine(nil).Resolve(ctx, sso.Login{
		Provider: sso.Provider{Name: in.Provider, Kind: sso.Kind(in.Provider), Org: in.Org, Link: link},
		Claims:   sso.Claims{Issuer: in.Issuer, Subject: in.Subject, Email: in.Email, EmailVerified: in.EmailVerified},
		Profile:  in.Profile, Intent: sso.Intent(in.Intent), LinkUserID: in.LinkUserID, Host: hd.Encode(),
		Request: sso.Request{IP: in.IP, UserAgent: in.UserAgent},
	})
	out := ProviderResult{NewUser: res.NewUser, Linked: res.Linked, Profile: res.Profile}
	if res.Account.ID != "" {
		out.User, _ = h.user(ctx, res.Account.ID)
	}
	return out, authError(err)
}

// authError is the realm's own error for one of the library's, keeping the library's in the chain.
func authError(err error) error {
	var refused *sso.Refused
	var own error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &refused):
		if errors.Is(err, ErrSignInRefused) {
			return err
		}
		return &RefusedError{UserID: refused.UserID, Reason: refused.Reason}
	case errors.Is(err, sso.ErrAccountExists) && !errors.Is(err, ErrAccountExists):
		own = ErrAccountExists
	case errors.Is(err, sso.ErrLinkConflict) && !errors.Is(err, ErrLinkConflict):
		own = ErrLinkConflict
	case errors.Is(err, sso.ErrEmailRequired) && !errors.Is(err, ErrEmailRequired):
		own = ErrEmailRequired
	default:
		return err
	}
	return fmt.Errorf("%w: %w", own, err)
}

// replaceDormant removes the user's own identity of the provider (and organization) when it was
// vouched for by another issuer than the one now linking, so the new one can take its place. It
// does nothing, and says so, when the new identity is already someone's or the old one is not
// dormant.
func (s *Users) replaceDormant(ctx context.Context, u User, in ProviderLogin) bool {
	if in.Issuer == "" {
		return false
	}
	own, err := s.Store.IdentityOfIn(ctx, u.ID, in.Provider, in.Org)
	if err != nil || own.Issuer == "" || own.Issuer == in.Issuer {
		return false
	}
	if _, err := s.Store.IdentityIn(ctx, in.Provider, in.Org, in.Issuer, in.Subject); err == nil {
		return false // the new identity belongs to someone: a conflict, not a replacement
	}
	if err := s.Store.DeleteIdentityIn(ctx, u.ID, in.Provider, in.Org); err != nil {
		return false
	}
	details := map[string]any{"provider": in.Provider, "by": "self", "replaced": "dormant"}
	if in.Org != "" {
		details["org"] = in.Org
	}
	s.Audit(ctx, AuditIdentityUnlinked, userTarget(u.ID), details)
	return true
}
