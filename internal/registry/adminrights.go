package registry

import (
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// AdminRights is the part of the admin API a role opens: a set of areas.
// `admin: true` in realm.yaml is every area (and any area added later);
// `admin: [users, invitations]` is just those. The zero value is none.
type AdminRights uint8

const (
	RightUsers       AdminRights = 1 << iota // users, their roles, passwords, addresses, networks, erasure
	RightInvitations                         // invitations
	RightAPIKeys                             // API keys
	RightSecrets                             // secrets
	RightAudit                               // the audit trail
	RightFunctions                           // running functions by hand, their invocations and every job

	// AllRights is `admin: true`.
	AllRights = RightUsers | RightInvitations | RightAPIKeys | RightSecrets | RightAudit | RightFunctions
)

// rightNames are the names realm.yaml and the docs use, in a fixed order.
var rightNames = []struct {
	name  string
	right AdminRights
}{
	{"users", RightUsers},
	{"invitations", RightInvitations},
	{"apikeys", RightAPIKeys},
	{"secrets", RightSecrets},
	{"audit", RightAudit},
	{"functions", RightFunctions},
}

// Any reports whether r opens anything: whether holding it makes someone an
// administrator.
func (r AdminRights) Any() bool { return r != 0 }

// Has reports whether r includes every right in want.
func (r AdminRights) Has(want AdminRights) bool { return r&want == want }

// Names lists the rights by name, in the documented order; "all" for every
// right.
func (r AdminRights) Names() []string {
	if r == AllRights {
		return []string{"all"}
	}
	var out []string
	for _, n := range rightNames {
		if r.Has(n.right) {
			out = append(out, n.name)
		}
	}
	return out
}

// String is the names, comma separated ("none" for no right).
func (r AdminRights) String() string {
	if !r.Any() {
		return "none"
	}
	return strings.Join(r.Names(), ", ")
}

// RightNamed is the right of a name used in realm.yaml and the docs.
func RightNamed(name string) (AdminRights, bool) {
	for _, n := range rightNames {
		if n.name == name {
			return n.right, true
		}
	}
	return 0, false
}

// rightNameList is the accepted names, for error messages.
func rightNameList() string {
	names := make([]string, len(rightNames))
	for i, n := range rightNames {
		names[i] = n.name
	}
	return strings.Join(names, ", ")
}

// adminDoc is a role's `admin:` in realm.yaml: true, false, or a list of
// the areas the role may administer.
type adminDoc struct{ Rights AdminRights }

func (a *adminDoc) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		var all bool
		if err := n.Decode(&all); err != nil {
			return fmt.Errorf("line %d: admin must be true, false or a list of areas (%s)", n.Line, rightNameList())
		}
		if all {
			a.Rights = AllRights
		}
		return nil
	case yaml.SequenceNode:
		if len(n.Content) == 0 {
			return fmt.Errorf("line %d: admin: [] opens nothing; write admin: false (or leave it out)", n.Line)
		}
		for _, item := range n.Content {
			right, ok := RightNamed(item.Value)
			if item.Kind != yaml.ScalarNode || !ok {
				return fmt.Errorf("line %d: unknown admin area %q (want %s)", item.Line, item.Value, rightNameList())
			}
			if a.Rights.Has(right) {
				return fmt.Errorf("line %d: admin area %q is listed twice", item.Line, item.Value)
			}
			a.Rights |= right
		}
		return nil
	}
	return fmt.Errorf("line %d: admin must be true, false or a list of areas (%s)", n.Line, rightNameList())
}

// AdminRights is what the roles hold together: the union of the rights of
// each role that opens any.
func (s RealmSettings) AdminRights(roles []string) AdminRights {
	var out AdminRights
	for _, r := range roles {
		out |= s.Roles[r].Admin
	}
	return out
}

// FullAdminRoles returns the names of the roles that open every area (`admin:
// true`), sorted: the ones that can recover a realm whatever else is lost.
func (s RealmSettings) FullAdminRoles() []string {
	var out []string
	for name, r := range s.Roles {
		if r.Admin == AllRights {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}
