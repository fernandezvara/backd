package registry

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const domainsRealm = `auth:
  strategies:
    password: [password]
  signup: open
organizations:
  roles:
    owner: { manage: [org, members, roles, teams] }
    member: {}
  default_roles: [member]
  domains: DOMAINS
`

func TestOrganizationDomainsSettings(t *testing.T) {
	// Off unless the section is there and enabled.
	s, errs := parseRealmSettings([]byte(strings.Replace(domainsRealm, "  domains: DOMAINS\n", "", 1)))
	if len(errs) > 0 || s.Organizations.Domains != nil {
		t.Fatalf("without the section: %v %+v", errs, s.Organizations.Domains)
	}
	s, errs = parseRealmSettings([]byte(strings.Replace(domainsRealm, "DOMAINS", "{ enabled: false }", 1)))
	if len(errs) > 0 || s.Organizations.Domains != nil {
		t.Fatalf("disabled: %v %+v", errs, s.Organizations.Domains)
	}
	s, errs = parseRealmSettings([]byte(strings.Replace(domainsRealm, "DOMAINS", "{ enabled: true }", 1)))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	d := s.Organizations.Domains
	if d == nil || d.MaxPerOrg != 10 || d.Recheck != 7*24*time.Hour || d.LapseAfter != 3 || len(d.Blocked) != 0 || len(d.Resolvers) != 0 {
		t.Fatalf("defaults: %+v", d)
	}
	s, errs = parseRealmSettings([]byte(strings.Replace(domainsRealm, "DOMAINS", `{ max_per_org: 3, recheck: 24h, lapse_after: 5, blocked: [free.example], resolvers: ["1.1.1.1", "9.9.9.9:53"] }`, 1)))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	d = s.Organizations.Domains
	if d.MaxPerOrg != 3 || d.Recheck != 24*time.Hour || d.LapseAfter != 5 || len(d.Blocked) != 1 || len(d.Resolvers) != 2 {
		t.Fatalf("set: %+v", d)
	}
	// Whole days, as retention is written.
	if s, errs = parseRealmSettings([]byte(strings.Replace(domainsRealm, "DOMAINS", "{ recheck: 14d }", 1))); len(errs) > 0 || s.Organizations.Domains.Recheck != 14*24*time.Hour {
		t.Errorf("14d: %v %+v", errs, s.Organizations.Domains)
	}
}

func TestOrganizationDomainsSettingsRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ domains, want string }{
		"no claims":     {"{ max_per_org: 0 }", "domains.max_per_org: must be between 1 and 100"},
		"too many":      {"{ max_per_org: 101 }", "domains.max_per_org: must be between 1 and 100"},
		"recheck":       {"{ recheck: soon }", "domains.recheck:"},
		"recheck short": {"{ recheck: 1m }", "too short"},
		"lapse":         {"{ lapse_after: 0 }", "domains.lapse_after: must be between 1 and 20"},
		"blocked":       {"{ blocked: [Not A Domain] }", "domains.blocked[0]"},
		"blocked twice": {"{ blocked: [a.example, a.example] }", "listed twice"},
		"resolver":      {"{ resolvers: [\"not a server:x\"] }", "domains.resolvers[0]"},
		"resolver ipv6": {"{ resolvers: [\"::1\"] }", "IPv6"},
		"resolver port": {"{ resolvers: [\"1.1.1.1:99999\"] }", "domains.resolvers[0]"},
		"unknown key":   {"{ approve: true }", "approve"},
	} {
		t.Run(name, func(t *testing.T) {
			_, errs := parseRealmSettings([]byte(strings.Replace(domainsRealm, "DOMAINS", tc.domains, 1)))
			if len(errs) == 0 || !strings.Contains(errors.Join(errs...).Error(), tc.want) {
				t.Fatalf("errors = %v, want %q", errs, tc.want)
			}
		})
	}
}

// The kinds are one table: what an organization may connect is derived from it, and a name that is
// reserved can't be a provider.
func TestKindsAreOneTable(t *testing.T) {
	if got := strings.Join(SSOProviders, ","); got != "google,microsoft" {
		t.Errorf("organizations may connect %s", got)
	}
	for _, name := range BuiltinProviders {
		spec := kindSpecs[name]
		if spec == nil || spec.parse == nil || len(spec.fields) == 0 {
			t.Errorf("%s has no complete spec", name)
		}
	}
	for _, name := range ReservedProviderNames {
		if kindSpecs[name] != nil && name != kindOIDC && name != kindOAuth2 {
			t.Errorf("%s is reserved and has a kind", name) // oidc is the kind any other name takes with type: oidc
		}
	}
	apple := &Provider{Name: "apple", Kind: "apple"}
	conn := &Provider{Name: "google", Kind: "google", Org: "o1"}
	if !apple.TrustsEmail() || conn.TrustsEmail() || (&Provider{Name: "microsoft", Kind: "microsoft"}).TrustsEmail() {
		t.Error("TrustsEmail follows the kind, and a connection never trusts")
	}
}

func TestIssuerMatches(t *testing.T) {
	const ms = "https://login.microsoftonline.com/"
	tid := "11111111-2222-3333-4444-555555555555"
	google := &Provider{Name: "google", Kind: "google"}
	apple := &Provider{Name: "apple", Kind: "apple"}
	common := &Provider{Name: "microsoft", Kind: "microsoft", Tenant: "common"}
	own := &Provider{Name: "microsoft", Kind: "microsoft", Tenant: tid, Org: "o1"}
	for name, tc := range map[string]struct {
		p    *Provider
		iss  string
		want bool
	}{
		"google":                   {google, "https://accounts.google.com", true},
		"google, another":          {google, "https://evil.example", false},
		"apple":                    {apple, "https://appleid.apple.com", true},
		"apple, google's":          {apple, "https://accounts.google.com", false},
		"microsoft common":         {common, ms + tid + "/v2.0", true},
		"microsoft common, odd":    {common, "https://evil.example/" + tid + "/v2.0", false},
		"microsoft common, nested": {common, ms + tid + "/x/v2.0", false},
		"microsoft own tenant":     {own, ms + tid + "/v2.0", true},
		"microsoft other":          {own, ms + "99999999-2222-3333-4444-555555555555/v2.0", false},
		"not recorded yet":         {own, "", true},
	} {
		if got := tc.p.IssuerMatches(tc.iss); got != tc.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}
