package registry

import (
	"fmt"
	"maps"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/fernandezvara/backd/internal/strutil"
)

// What the realm lets organizations do about signing in (design/sso.md §5.2): organizations.sso, the
// providers they may connect, and organizations.domains, the domains they may prove. Both are off
// unless the section is there; the surfaces they govern answer 404 when they are off.

// orgSSODoc mirrors organizations.sso.
type orgSSODoc struct {
	Enabled   *bool    `yaml:"enabled"`
	Providers []string `yaml:"providers"`
	MaxPerOrg *int     `yaml:"max_per_org"`
	OIDC      *struct {
		Issuers []string `yaml:"issuers"`
	} `yaml:"oidc"`
}

// OrgSSOSettings is organizations.sso: the realm's switch for organization sign-in.
type OrgSSOSettings struct {
	// Providers are the ones an organization may connect.
	Providers []string
	// MaxPerOrg caps the connections of one organization.
	MaxPerOrg int
	// OIDCIssuers are the patterns an organization's OpenID Connect issuer must match
	// (organizations.sso.oidc.issuers); none: organizations cannot use OpenID Connect.
	OIDCIssuers []IssuerPattern
}

// Allows reports whether organizations may connect the provider.
func (s *OrgSSOSettings) Allows(provider string) bool {
	return s != nil && slices.Contains(s.Providers, provider)
}

// parseOrgSSO reads organizations.sso. add reports a problem under organizations.
func parseOrgSSO(d *orgSSODoc, o *OrgSettings, add func(format string, args ...any)) *OrgSSOSettings {
	if d == nil || (d.Enabled != nil && !*d.Enabled) {
		return nil
	}
	sso := &OrgSSOSettings{Providers: d.Providers}
	if len(sso.Providers) == 0 {
		sso.Providers = slices.Clone(DefaultSSOProviders)
	}
	seen := map[string]bool{}
	for i, name := range sso.Providers {
		switch {
		case !slices.Contains(SSOProviders, name):
			add("sso.providers[%d]: %q isn't available to organizations yet; the choices are %s", i, name, strings.Join(SSOProviders, ", "))
		case seen[name]:
			add("sso.providers[%d]: %q is listed twice", i, name)
		}
		seen[name] = true
	}
	sso.MaxPerOrg = len(sso.Providers)
	if m := d.MaxPerOrg; m != nil {
		if *m < 1 || *m > len(sso.Providers) {
			add("sso.max_per_org: must be between 1 and %d (an organization has one connection per provider), got %d", len(sso.Providers), *m)
		} else {
			sso.MaxPerOrg = *m
		}
	}
	if d.OIDC != nil {
		for i, raw := range d.OIDC.Issuers {
			p, msg := ParseIssuerPattern(raw)
			if msg != "" {
				add("sso.oidc.issuers[%d]: %s", i, msg)
				continue
			}
			sso.OIDCIssuers = append(sso.OIDCIssuers, p)
		}
	}
	if slices.Contains(sso.Providers, kindOIDC) {
		if len(sso.OIDCIssuers) == 0 {
			add("sso.providers: oidc needs organizations.sso.oidc.issuers: the issuers organizations may connect (without it they cannot use OpenID Connect)")
		}
		if o.Domains == nil {
			add("sso.providers: oidc needs organizations.domains: a company's OpenID Connect server is admitted by an address on a domain it proved")
		}
	} else if d.OIDC != nil && len(d.OIDC.Issuers) > 0 {
		add("sso.oidc: lists issuers, but oidc is not one of sso.providers")
	}
	if !slices.ContainsFunc(slices.Collect(maps.Values(o.Roles)), func(r OrgRole) bool { return r.Manage.Has(CapSSO) }) {
		add("sso: needs a role with manage: sso, or no member of an organization could set up its sign-in (realm administrators still could)")
	}
	return sso
}

// Defaults and limits of organizations.domains.
const (
	DefaultDomainsPerOrg    = 10
	MaxDomainsPerOrg        = 100
	DefaultDomainRecheck    = 7 * 24 * time.Hour
	MinDomainRecheck        = time.Hour
	DefaultDomainLapseAfter = 3
	MaxDomainLapseAfter     = 20
	maxDomainBlocked        = 1000
	maxDomainResolvers      = 8
)

// orgDomainsDoc mirrors organizations.domains.
type orgDomainsDoc struct {
	Enabled    *bool    `yaml:"enabled"`
	MaxPerOrg  *int     `yaml:"max_per_org"`
	Recheck    string   `yaml:"recheck"`
	LapseAfter *int     `yaml:"lapse_after"`
	Blocked    []string `yaml:"blocked"`
	Resolvers  []string `yaml:"resolvers"`
}

// OrgDomainsSettings is organizations.domains: the realm's switch for verified domains.
type OrgDomainsSettings struct {
	// MaxPerOrg caps the claims of one organization, verified and pending.
	MaxPerOrg int
	// Recheck is how often a verified domain is checked again.
	Recheck time.Duration
	// LapseAfter is how many consecutive failed checks make a domain lapse.
	LapseAfter int
	// Blocked are more domains nobody may claim, added to the built-in list of free-mail providers.
	Blocked []string
	// Resolvers are the DNS servers (host:port) to ask; empty: the host's resolver.
	Resolvers []string
}

// parseOrgDomains reads organizations.domains.
func parseOrgDomains(d *orgDomainsDoc, add func(format string, args ...any)) *OrgDomainsSettings {
	if d == nil || (d.Enabled != nil && !*d.Enabled) {
		return nil
	}
	out := &OrgDomainsSettings{MaxPerOrg: DefaultDomainsPerOrg, Recheck: DefaultDomainRecheck, LapseAfter: DefaultDomainLapseAfter}
	if m := d.MaxPerOrg; m != nil {
		if *m < 1 || *m > MaxDomainsPerOrg {
			add("domains.max_per_org: must be between 1 and %d, got %d", MaxDomainsPerOrg, *m)
		} else {
			out.MaxPerOrg = *m
		}
	}
	if d.Recheck != "" {
		switch v, err := strutil.ParseDuration(d.Recheck); {
		case err != nil:
			add("domains.recheck: %v", err)
		case v < MinDomainRecheck:
			add("domains.recheck: %q is too short: at least %s", d.Recheck, MinDomainRecheck)
		default:
			out.Recheck = v
		}
	}
	if l := d.LapseAfter; l != nil {
		if *l < 1 || *l > MaxDomainLapseAfter {
			add("domains.lapse_after: must be between 1 and %d, got %d", MaxDomainLapseAfter, *l)
		} else {
			out.LapseAfter = *l
		}
	}
	if len(d.Blocked) > maxDomainBlocked {
		add("domains.blocked: at most %d domains", maxDomainBlocked)
	}
	seen := map[string]bool{}
	for i, dom := range d.Blocked {
		switch {
		case DomainProblem(dom) != "":
			add("domains.blocked[%d]: %s", i, DomainProblem(dom))
		case seen[dom]:
			add("domains.blocked[%d]: %q is listed twice", i, dom)
		default:
			out.Blocked = append(out.Blocked, dom)
		}
		seen[dom] = true
	}
	if len(d.Resolvers) > maxDomainResolvers {
		add("domains.resolvers: at most %d servers", maxDomainResolvers)
	}
	for i, r := range d.Resolvers {
		if msg := resolverProblem(r); msg != "" {
			add("domains.resolvers[%d]: %s", i, msg)
		} else {
			out.Resolvers = append(out.Resolvers, r)
		}
	}
	return out
}

// resolverProblem checks a DNS server given as host:port (or a bare IP address, which means port 53).
func resolverProblem(r string) string {
	host, port := r, "53"
	if h, p, ok := strings.Cut(r, ":"); ok && !strings.Contains(p, ":") {
		host, port = h, p
	}
	if strings.Count(r, ":") > 1 && !strings.HasPrefix(r, "[") {
		return fmt.Sprintf("%q must be an address or host:port (an IPv6 address goes in [brackets])", r)
	}
	n := 0
	if _, err := fmt.Sscanf(port, "%d", &n); err != nil || n < 1 || n > 65535 || host == "" {
		return fmt.Sprintf("%q must be an address or host:port", r)
	}
	return ""
}

// IssuerPattern is one entry of organizations.sso.oidc.issuers: an https origin, with `*.` for the first
// label ("https://*.okta.com" matches one label only: https://acme.okta.com, never a.b.okta.com and never
// https://evil.okta.com.attacker.example).
type IssuerPattern struct {
	Host     string // the host, or the suffix after `*.`
	Wildcard bool
}

func (p IssuerPattern) String() string {
	if p.Wildcard {
		return "https://*." + p.Host
	}
	return "https://" + p.Host
}

// ParseIssuerPattern reads a pattern. msg says what is wrong with it, or "".
func ParseIssuerPattern(raw string) (p IssuerPattern, msg string) {
	rest, ok := strings.CutPrefix(raw, "https://")
	if !ok || rest == "" {
		return p, fmt.Sprintf("%q must start with https://", raw)
	}
	rest = strings.TrimSuffix(rest, "/")
	if strings.ContainsAny(rest, "/?#@") {
		return p, fmt.Sprintf("%q must be an origin: no path, query, fragment or credentials", raw)
	}
	host := rest
	if h, port, found := strings.Cut(rest, ":"); found {
		if port != "443" {
			return p, fmt.Sprintf("%q: only the port 443 is allowed", raw)
		}
		host = h
	}
	host = strings.ToLower(host)
	if suffix, ok := strings.CutPrefix(host, "*."); ok {
		p.Wildcard, host = true, suffix
	}
	if strings.Contains(host, "*") {
		return p, fmt.Sprintf("%q: a * is only allowed as the first label (https://*.okta.com)", raw)
	}
	if net.ParseIP(host) != nil {
		return p, fmt.Sprintf("%q must name a domain such as https://sso.partner.example, not an address", raw)
	}
	if s, _ := publicsuffix.PublicSuffix(host); s == host {
		return p, fmt.Sprintf("%q is a public suffix: it would admit anybody's server", raw)
	}
	if DomainProblem(host) != "" {
		return p, fmt.Sprintf("%q must name a domain such as https://sso.partner.example", raw)
	}
	p.Host = host
	return p, ""
}

// Matches says whether an issuer URL is one the pattern allows: https, no credentials, port 443 or none.
func (p IssuerPattern) Matches(issuer string) bool {
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if !p.Wildcard {
		return host == p.Host
	}
	label, found := strings.CutSuffix(host, "."+p.Host)
	return found && label != "" && !strings.Contains(label, ".")
}

// AllowsIssuer says whether an organization may connect an OpenID Connect server with this issuer: one that
// matches a pattern of the realm. In development (BACKD_DEV) a server on this machine (localhost or a
// loopback address, any port, http too) is allowed as well, so a local Keycloak can be tried without
// a public name.
func (s *OrgSSOSettings) AllowsIssuer(issuer string, dev bool) bool {
	if s == nil {
		return false
	}
	if dev {
		if u, err := url.Parse(issuer); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
			if h := u.Hostname(); h == "localhost" || net.ParseIP(h).IsLoopback() {
				return true
			}
		}
	}
	return slices.ContainsFunc(s.OIDCIssuers, func(p IssuerPattern) bool { return p.Matches(issuer) })
}
