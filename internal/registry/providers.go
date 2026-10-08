package registry

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// Sign-in with external identity providers (providers: in realm.yaml). Besides
// the three built-in ones nothing is configurable yet: another OpenID Connect
// provider needs `type: oidc`, which comes later.

// The built-in providers, which are reserved names.
const (
	ProviderGoogle    = "google"
	ProviderMicrosoft = "microsoft"
	ProviderApple     = "apple"
)

// BuiltinProviders are the providers backd knows, sorted.
var BuiltinProviders = []string{ProviderApple, ProviderGoogle, ProviderMicrosoft}

var (
	clientIDPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~+-]{0,254}$`)
	guidPattern        = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	appleIDPattern     = regexp.MustCompile(`^[A-Z0-9]{10}$`)
	appleBundlePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,254}$`)
)

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
	Name string // google, microsoft or apple
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
func (p *Provider) TrustsEmail() bool { return p.Name == ProviderGoogle || p.Name == ProviderApple }

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

// parseProviders checks providers: of realm.yaml.
func parseProviders(docs map[string]*providerDoc) (map[string]*Provider, []error) {
	out := map[string]*Provider{}
	var errs []error
	for _, name := range sortedKeys(docs) {
		d := docs[name]
		add := func(format string, args ...any) {
			errs = append(errs, fmt.Errorf("providers."+name+": "+format, args...))
		}
		if d == nil {
			add("is empty: it needs at least its client_id (see `backd template realm`)")
			continue
		}
		if d.Type != "" {
			add("type: %q isn't available yet; the built-in providers (%s) need none", d.Type, strings.Join(BuiltinProviders, ", "))
			continue
		}
		if !slices.Contains(BuiltinProviders, name) {
			add("isn't a provider backd knows; the built-in ones are %s", strings.Join(BuiltinProviders, ", "))
			continue
		}
		p := &Provider{Name: name, ClientID: d.ClientID, NativeClientIDs: d.NativeClientIDs, RevokeOnDelete: true}
		only := func(field string, present bool, names ...string) {
			if present && !slices.Contains(names, name) {
				add("%s: doesn't apply to %s", field, name)
			}
		}
		only("client_id", d.ClientID != "", ProviderGoogle, ProviderMicrosoft)
		only("client_secret", d.ClientSecret != "", ProviderGoogle, ProviderMicrosoft)
		only("native_client_ids", len(d.NativeClientIDs) > 0, ProviderGoogle, ProviderMicrosoft)
		only("tenant", d.Tenant != "", ProviderMicrosoft)
		for field, set := range map[string]bool{"services_id": d.ServicesID != "", "bundle_ids": len(d.BundleIDs) > 0, "team_id": d.TeamID != "", "key_id": d.KeyID != "", "private_key": d.PrivateKey != "", "revoke_on_delete": d.RevokeOnDelete != nil} {
			only(field, set, ProviderApple)
		}
		secret := func(field, value string, dst *string) {
			n, found := strings.CutPrefix(value, secretPrefix)
			switch {
			case value == "":
				add("%s: is required, written secret:NAME (a secret of the realm: backd secret set)", field)
			case !found || !ValidSecretName(n):
				add("%s: %q must be secret:NAME with NAME in upper case letters, digits and _ (the secret itself never goes in realm.yaml)", field, value)
			default:
				*dst = n
			}
		}
		ids := func(field string, list []string, pattern *regexp.Regexp) {
			seen := map[string]bool{}
			for i, id := range list {
				switch {
				case !pattern.MatchString(id):
					add("%s[%d]: %q isn't a valid id", field, i, id)
				case seen[id]:
					add("%s[%d]: %q is listed twice", field, i, id)
				}
				seen[id] = true
			}
		}
		switch name {
		case ProviderGoogle, ProviderMicrosoft:
			if !clientIDPattern.MatchString(d.ClientID) {
				add("client_id: is required and must be the application's client id")
			}
			secret("client_secret", d.ClientSecret, &p.ClientSecret)
			ids("native_client_ids", d.NativeClientIDs, clientIDPattern)
			if name == ProviderMicrosoft {
				p.Tenant = d.Tenant
				switch {
				case p.Tenant == "":
					p.Tenant = "common"
				case slices.Contains([]string{"common", "organizations", "consumers"}, p.Tenant) || guidPattern.MatchString(p.Tenant):
				default:
					add("tenant: %q must be common, organizations, consumers or a tenant id (a GUID)", d.Tenant)
				}
				if d.ClientID != "" && !guidPattern.MatchString(d.ClientID) {
					add("client_id: %q isn't an application (client) id, a GUID", d.ClientID)
				}
			}
		case ProviderApple:
			if d.ServicesID == "" && len(d.BundleIDs) == 0 {
				add("services_id: is required for the redirect flow (bundle_ids alone serve only native apps)")
			}
			if d.ServicesID != "" && !appleBundlePattern.MatchString(d.ServicesID) {
				add("services_id: %q isn't a Services ID such as com.acme.web", d.ServicesID)
			}
			p.ClientID = d.ServicesID
			p.NativeClientIDs = d.BundleIDs
			ids("bundle_ids", d.BundleIDs, appleBundlePattern)
			for field, v := range map[string]string{"team_id": d.TeamID, "key_id": d.KeyID} {
				if !appleIDPattern.MatchString(v) {
					add("%s: is required and is 10 upper-case letters and digits, as the Apple developer console shows it", field)
				}
			}
			p.TeamID, p.KeyID = d.TeamID, d.KeyID
			secret("private_key", d.PrivateKey, &p.PrivateKey)
			if d.RevokeOnDelete != nil {
				p.RevokeOnDelete = *d.RevokeOnDelete
			}
		}
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

// ProviderCallbackPath is where a provider sends the user back to, relative to backd's
// public address: the URL to register in the provider's console.
func ProviderCallbackPath(realm, provider string) string {
	return "/v1/" + url.PathEscape(realm) + "/_auth/oauth/" + url.PathEscape(provider) + "/callback"
}
