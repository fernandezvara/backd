package registry

import (
	"fmt"
	"regexp"
	"slices"
)

// The rules for the fields of a provider's application: one set of functions, used by the
// providers of realm.yaml, by an organization's connections and by an import, so they are held to
// the same checks (design/sso.md §5). Each returns what is wrong, or "" when the value is fine.

var (
	clientIDPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~+-]{0,254}$`)
	guidPattern        = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	appleIDPattern     = regexp.MustCompile(`^[A-Z0-9]{10}$`)
	appleBundlePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,254}$`)
)

// The checks of a Google or Microsoft application's fields live in the functions below, so the
// providers: of realm.yaml and an organization's sign-in connections (design/sso.md §9) are
// held to the same rules. Each returns what is wrong, or "" when the value is fine.

var domainPattern = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// SharedMicrosoftTenants are the tenant values that admit people of any organization.
var SharedMicrosoftTenants = []string{"common", "organizations", "consumers"}

// ClientIDProblem checks the client id of a Google or Microsoft application (Microsoft: a GUID).
func ClientIDProblem(provider, id string) string {
	if !clientIDPattern.MatchString(id) {
		return "is required and must be the application's client id"
	}
	if provider == ProviderMicrosoft && !guidPattern.MatchString(id) {
		return fmt.Sprintf("%q isn't an application (client) id, a GUID", id)
	}
	return ""
}

// ClientIDListProblems checks a list of extra audiences (native app client ids): each a valid
// id, none twice. Messages start with field[index].
func ClientIDListProblems(field string, list []string) []string {
	return idListProblems(field, list, clientIDPattern)
}

func idListProblems(field string, list []string, pattern *regexp.Regexp) []string {
	var out []string
	seen := map[string]bool{}
	for i, id := range list {
		switch {
		case !pattern.MatchString(id):
			out = append(out, fmt.Sprintf("%s[%d]: %q isn't a valid id", field, i, id))
		case seen[id]:
			out = append(out, fmt.Sprintf("%s[%d]: %q is listed twice", field, i, id))
		}
		seen[id] = true
	}
	return out
}

// TenantProblem checks a Microsoft tenant: a tenant id (GUID), and with shared also common,
// organizations and consumers. An organization's connection must name its own tenant.
func TenantProblem(tenant string, shared bool) string {
	switch {
	case guidPattern.MatchString(tenant):
		return ""
	case shared && slices.Contains(SharedMicrosoftTenants, tenant):
		return ""
	case shared:
		return fmt.Sprintf("%q must be common, organizations, consumers or a tenant id (a GUID)", tenant)
	}
	return fmt.Sprintf("%q must be the tenant id (a GUID) of the organization's Microsoft Entra tenant: common, organizations and consumers admit people of any organization", tenant)
}

// DomainProblem checks a lower-case domain name such as acme.example: a Google Workspace
// domain or an email domain.
func DomainProblem(domain string) string {
	if len(domain) > 253 || !domainPattern.MatchString(domain) {
		return fmt.Sprintf("%q must be a lower-case domain name such as acme.example", domain)
	}
	return ""
}
