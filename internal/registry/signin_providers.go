package registry

import (
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/fernandezvara/backd/sso"
)

// Sign-in with external identity providers (auth.providers in realm.yaml). A provider is a kind plus
// configuration (design/sso.md §4): the kind says which fields apply and what is checked about
// them, how far the provider's word about an address is trusted, and whether an organization may
// connect it. Everything that differs between providers lives in kindSpecs; nothing else in the
// package switches on a provider's name.

// The built-in providers, which are reserved names. A built-in provider's kind is its name.
const (
	ProviderGoogle    = "google"
	ProviderMicrosoft = "microsoft"
	ProviderApple     = "apple"
)

// Kinds that are designed and not built yet: their names are reserved so that nobody takes them
// for a provider of their own before they exist.
const (
	kindGitHub = "github"
	kindOIDC   = "oidc"
	kindOAuth2 = "oauth2"
)

// BuiltinProviders are the providers backd knows, sorted.
var BuiltinProviders = []string{ProviderApple, ProviderGoogle, ProviderMicrosoft}

// ReservedProviderNames are the names no provider may take: backd's own sign-in methods (an
// identity's provider is `password`, magic links record `email`, a strategy method is `sso`) and the
// kinds still to come.
var ReservedProviderNames = []string{"password", "email", "sso", kindGitHub, kindOIDC, kindOAuth2}

// kindSpec is what one kind of provider is.
type kindSpec struct {
	// fields are the keys of providerDoc that apply to the kind; any other is refused.
	fields []string
	// trustsEmail: the provider's verified addresses may link an existing account (a connection of an
	// organization never does).
	trustsEmail bool
	// orgs: an organization may connect it.
	orgs bool
	// parse checks the fields and fills the provider.
	parse func(c *providerCheck, d *providerDoc, p *Provider)
}

var kindSpecs = map[string]*kindSpec{
	ProviderGoogle:    {fields: []string{"client_id", "client_secret", "native_client_ids"}, trustsEmail: true, orgs: true, parse: parseGoogleLike},
	ProviderMicrosoft: {fields: []string{"client_id", "client_secret", "native_client_ids", "tenant"}, orgs: true, parse: parseGoogleLike},
	ProviderApple:     {fields: []string{"services_id", "bundle_ids", "team_id", "key_id", "private_key", "revoke_on_delete"}, trustsEmail: true, parse: parseApple},
}

// SSOProviders are the providers an organization may connect, sorted.
var SSOProviders = func() []string {
	var out []string
	for _, name := range BuiltinProviders {
		if kindSpecs[name].orgs {
			out = append(out, name)
		}
	}
	return out
}()

// providerDoc mirrors one entry of providers: in realm.yaml.
type providerDoc struct {
	Type            string   `yaml:"type"`
	ClientID        string   `yaml:"client_id"`
	ClientSecret    string   `yaml:"client_secret"`
	NativeClientIDs []string `yaml:"native_client_ids"`
	Tenant          string   `yaml:"tenant"`
	ServicesID      string   `yaml:"services_id"`
	BundleIDs       []string `yaml:"bundle_ids"`
	TeamID          string   `yaml:"team_id"`
	KeyID           string   `yaml:"key_id"`
	PrivateKey      string   `yaml:"private_key"`
	RevokeOnDelete  *bool    `yaml:"revoke_on_delete"`
}

// Provider is an external identity provider a realm lets its users sign in with.
type Provider struct {
	Name string // what identities, URLs and the audit call it
	// Kind is how it is spoken to and checked: google, microsoft or apple.
	Kind string
	// ClientID is the application's id with the provider (Apple: the Services ID);
	// ClientSecret names the secret that holds its secret (Apple has none: it
	// signs one from PrivateKey).
	ClientID     string
	ClientSecret string
	// NativeClientIDs (Apple: BundleIDs) are the extra audiences the ID-token
	// endpoint accepts, the ids of the mobile apps.
	NativeClientIDs []string
	// Tenant is Microsoft's: common, organizations, consumers or a tenant id.
	Tenant string
	// Apple only: the team and key that sign the client secret, the secret that
	// holds the private key, and whether a user's tokens are revoked when the
	// account goes away.
	TeamID, KeyID, PrivateKey string
	RevokeOnDelete            bool
	// Org is set on a provider built from an organization's connection (design/sso.md §9): the
	// id of the organization. Its ClientSecret then names an organization secret, and the
	// restrictions of the connection apply on top of the token checks.
	Org string
}

// SSO is the provider as the sign-in library speaks to it, with the endpoints of its kind.
func (p *Provider) SSO() sso.Provider {
	kind := sso.Kind(p.Kind)
	return sso.Provider{
		Name: p.Name, Kind: kind, ClientID: p.ClientID, NativeClientIDs: p.NativeClientIDs,
		Tenant: p.Tenant, TeamID: p.TeamID, KeyID: p.KeyID,
		Endpoints: sso.BuiltinEndpoints(kind, p.Tenant),
	}
}

// IssuerMatches says whether an identity vouched for by iss is one the provider signs in now: the
// provider's issuer is the one that vouched for it. An identity without an issuer (not recorded yet)
// matches.
func (p *Provider) IssuerMatches(iss string) bool {
	if iss == "" {
		return true
	}
	const msBase = "https://login.microsoftonline.com/"
	switch p.Kind {
	case ProviderGoogle:
		return iss == "https://accounts.google.com"
	case ProviderApple:
		return iss == "https://appleid.apple.com"
	case ProviderMicrosoft:
		tenant, ok := strings.CutSuffix(strings.TrimPrefix(iss, msBase), "/v2.0")
		if !strings.HasPrefix(iss, msBase) || !ok || tenant == "" || strings.Contains(tenant, "/") {
			return false
		}
		return slices.Contains(SharedMicrosoftTenants, p.Tenant) || strings.EqualFold(tenant, p.Tenant)
	}
	return false
}

// SecretNames are the realm secrets the provider needs.
func (p *Provider) SecretNames() []string {
	var out []string
	for _, n := range []string{p.ClientSecret, p.PrivateKey} {
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

// TrustsEmail says whether an address the provider asserts as verified may link
// an existing account on its own (Google and Apple; not Microsoft, whose address
// claim a tenant administrator controls).
func (p *Provider) TrustsEmail() bool {
	// A connection an organization's managers control never vouches for an address: it can't
	// link a sign-in to an account (design/sso.md §8).
	spec := kindSpecs[p.Kind]
	return p.Org == "" && spec != nil && spec.trustsEmail
}

// Audiences are the client ids an ID token may be issued to: the web client and, for
// the ID-token endpoint, the native ones.
func (p *Provider) Audiences(native bool) []string {
	out := []string{}
	if p.ClientID != "" {
		out = append(out, p.ClientID)
	}
	if native {
		out = append(out, p.NativeClientIDs...)
	}
	return out
}

// providerCheck collects what is wrong with one provider's entry.
type providerCheck struct {
	name string
	errs *[]error
}

func (c *providerCheck) add(format string, args ...any) {
	*c.errs = append(*c.errs, fmt.Errorf("auth.providers."+c.name+": "+format, args...))
}

// secret reads a secret:NAME reference into dst.
func (c *providerCheck) secret(field, value string, dst *string) {
	n, found := strings.CutPrefix(value, secretPrefix)
	switch {
	case value == "":
		c.add("%s: is required, written secret:NAME (a secret of the realm: backd secret set)", field)
	case !found || !ValidSecretName(n):
		c.add("%s: %q must be secret:NAME with NAME in upper case letters, digits and _ (the secret itself never goes in realm.yaml)", field, value)
	default:
		*dst = n
	}
}

// ids checks a list of client or bundle ids.
func (c *providerCheck) ids(field string, list []string, pattern *regexp.Regexp) {
	for _, msg := range idListProblems(field, list, pattern) {
		c.add("%s", msg)
	}
}

// parseGoogleLike checks Google and Microsoft: a client id, a client secret and, for Microsoft, a tenant.
func parseGoogleLike(c *providerCheck, d *providerDoc, p *Provider) {
	if msg := ClientIDProblem(p.Kind, d.ClientID); msg != "" {
		c.add("client_id: %s", msg)
	}
	c.secret("client_secret", d.ClientSecret, &p.ClientSecret)
	c.ids("native_client_ids", d.NativeClientIDs, clientIDPattern)
	if p.Kind == ProviderMicrosoft {
		p.Tenant = d.Tenant
		if p.Tenant == "" {
			p.Tenant = "common"
		} else if msg := TenantProblem(p.Tenant, true); msg != "" {
			c.add("tenant: %s", msg)
		}
	}
}

// parseApple checks Sign in with Apple: the Services ID, the bundle ids, the key that signs the
// client secret and whether tokens are revoked when the account goes away.
func parseApple(c *providerCheck, d *providerDoc, p *Provider) {
	if d.ServicesID == "" && len(d.BundleIDs) == 0 {
		c.add("services_id: is required for the redirect flow (bundle_ids alone serve only native apps)")
	}
	if d.ServicesID != "" && !appleBundlePattern.MatchString(d.ServicesID) {
		c.add("services_id: %q isn't a Services ID such as com.acme.web", d.ServicesID)
	}
	p.ClientID = d.ServicesID
	p.NativeClientIDs = d.BundleIDs
	c.ids("bundle_ids", d.BundleIDs, appleBundlePattern)
	for field, v := range map[string]string{"team_id": d.TeamID, "key_id": d.KeyID} {
		if !appleIDPattern.MatchString(v) {
			c.add("%s: is required and is 10 upper-case letters and digits, as the Apple developer console shows it", field)
		}
	}
	p.TeamID, p.KeyID = d.TeamID, d.KeyID
	c.secret("private_key", d.PrivateKey, &p.PrivateKey)
	if d.RevokeOnDelete != nil {
		p.RevokeOnDelete = *d.RevokeOnDelete
	}
}

// docFields says which keys of a provider's entry are set.
func (d *providerDoc) docFields() map[string]bool {
	return map[string]bool{
		"client_id": d.ClientID != "", "client_secret": d.ClientSecret != "", "native_client_ids": len(d.NativeClientIDs) > 0,
		"tenant": d.Tenant != "", "services_id": d.ServicesID != "", "bundle_ids": len(d.BundleIDs) > 0,
		"team_id": d.TeamID != "", "key_id": d.KeyID != "", "private_key": d.PrivateKey != "", "revoke_on_delete": d.RevokeOnDelete != nil,
	}
}

// parseProviders checks auth.providers of realm.yaml.
func parseProviders(docs map[string]*providerDoc) (map[string]*Provider, []error) {
	out := map[string]*Provider{}
	var errs []error
	for _, name := range sortedKeys(docs) {
		d := docs[name]
		c := &providerCheck{name: name, errs: &errs}
		if d == nil {
			c.add("is empty: it needs at least its client_id (see `backd template realm`)")
			continue
		}
		if slices.Contains([]string{kindGitHub, kindOIDC, kindOAuth2}, name) {
			c.add("is the name of a kind of provider that isn't available yet; the built-in providers (%s) are", strings.Join(BuiltinProviders, ", "))
			continue
		}
		if slices.Contains(ReservedProviderNames, name) {
			c.add("is reserved for backd's own sign-in methods (%s) and can't name a provider", strings.Join(ReservedProviderNames, ", "))
			continue
		}
		if d.Type != "" {
			c.add("type: %q isn't available yet; the built-in providers (%s) need none", d.Type, strings.Join(BuiltinProviders, ", "))
			continue
		}
		spec := kindSpecs[name]
		if spec == nil {
			c.add("isn't a provider backd knows; the built-in ones are %s", strings.Join(BuiltinProviders, ", "))
			continue
		}
		set := d.docFields()
		for _, field := range slices.Sorted(maps.Keys(set)) {
			if set[field] && !slices.Contains(spec.fields, field) {
				c.add("%s: doesn't apply to %s", field, name)
			}
		}
		p := &Provider{Name: name, Kind: name, ClientID: d.ClientID, NativeClientIDs: d.NativeClientIDs, RevokeOnDelete: true}
		spec.parse(c, d, p)
		out[name] = p
	}
	return out, errs
}

// SignInRedirectAllowed says whether a sign-in may send the user to raw: it is on
// sign_in.allowed_redirects, or, when the realm has none, on email.allowed_redirects.
func (s RealmSettings) SignInRedirectAllowed(raw string) bool {
	list := s.SignInRedirects
	if len(list) == 0 && s.Email != nil {
		list = s.Email.AllowedRedirects
	}
	return redirectAllowed(list, raw)
}

// OrgProviderCallbackPath is where a provider sends the user back to after an organization's
// sign-in, relative to backd's public address: the URL a manager registers in the provider's
// console. It always names the organization by its id.
func OrgProviderCallbackPath(realm, orgID, provider string) string {
	return "/v1/" + url.PathEscape(realm) + "/_auth/orgs/" + url.PathEscape(orgID) + "/oauth/" + url.PathEscape(provider) + "/callback"
}

// ProviderCallbackPath is where a provider sends the user back to, relative to backd's
// public address: the URL to register in the provider's console.
func ProviderCallbackPath(realm, provider string) string {
	return "/v1/" + url.PathEscape(realm) + "/_auth/oauth/" + url.PathEscape(provider) + "/callback"
}
