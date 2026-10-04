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
	RightData                                // the admin data route: documents, past their collections' rules
	RightConfig                              // the read-only view of the realm's configuration

	// AllRights is `admin: true`.
	AllRights = RightUsers | RightInvitations | RightAPIKeys | RightSecrets | RightAudit | RightFunctions | RightData | RightConfig
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
	{"data", RightData},
	{"config", RightConfig},
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

// adminDoc is a role's `admin:` in realm.yaml: true (every area, to change),
// false, `read` (every area, to read only), or a list of areas, which may
// include `read` too: `[read, secrets]` reads everything and changes secrets.
type adminDoc struct {
	Rights AdminRights // areas it may change
	Read   bool        // may read every area (subject to admin.read_access for users and data)
}

const adminReadKeyword = "read"

func (a *adminDoc) UnmarshalYAML(n *yaml.Node) error {
	usage := fmt.Sprintf("admin must be true, false, read or a list of areas (%s, or read)", rightNameList())
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Value == adminReadKeyword {
			a.Read = true
			return nil
		}
		var all bool
		if err := n.Decode(&all); err != nil {
			return fmt.Errorf("line %d: %s", n.Line, usage)
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
			if item.Kind == yaml.ScalarNode && item.Value == adminReadKeyword {
				if a.Read {
					return fmt.Errorf("line %d: admin area %q is listed twice", item.Line, item.Value)
				}
				a.Read = true
				continue
			}
			right, ok := RightNamed(item.Value)
			if item.Kind != yaml.ScalarNode || !ok {
				return fmt.Errorf("line %d: unknown admin area %q (want %s, or read)", item.Line, item.Value, rightNameList())
			}
			if a.Rights.Has(right) {
				return fmt.Errorf("line %d: admin area %q is listed twice", item.Line, item.Value)
			}
			a.Rights |= right
		}
		return nil
	}
	return fmt.Errorf("line %d: %s", n.Line, usage)
}

// AdminAccess is what a user's admin roles let them do in the admin API:
// change some areas, and/or read every area (a read-only level, `admin:
// read`). Roles add up.
type AdminAccess struct {
	Write   AdminRights
	ReadAll bool
}

// Any reports whether the access makes someone an administrator at all.
func (a AdminAccess) Any() bool { return a.Write.Any() || a.ReadAll }

// Full reports whether it is `admin: true`: every area, to change.
func (a AdminAccess) Full() bool { return a.Write == AllRights }

// CanWrite reports whether the areas in want may be changed.
func (a AdminAccess) CanWrite(want AdminRights) bool { return a.Write.Has(want) }

// CanRead reports whether the areas in want may be read: what the access
// changes it reads too; a read-only level reads every area except users and
// data, which realm.yaml's admin.read_access has to grant.
func (a AdminAccess) CanRead(s RealmSettings, want AdminRights) bool {
	have := a.Write
	if a.ReadAll {
		have |= AllRights
		if !s.ReadAccess.Users {
			have &^= RightUsers
			have |= a.Write & RightUsers
		}
		if !s.ReadAccess.Data {
			have &^= RightData
			have |= a.Write & RightData
		}
	}
	return have.Has(want)
}

// Covers reports whether a can do everything b can: nobody hands out, or
// changes someone who holds, more than they have.
func (a AdminAccess) Covers(b AdminAccess) bool {
	return a.Write.Has(b.Write) && (a.ReadAll || a.Full() || !b.ReadAll)
}

// String describes the access, for messages.
func (a AdminAccess) String() string {
	switch {
	case a.Full():
		return "all"
	case a.ReadAll && !a.Write.Any():
		return "read"
	case a.ReadAll:
		return "read, " + a.Write.String()
	}
	return a.Write.String()
}

// ReadAccess is realm.yaml's admin.read_access: what read-only administrators
// may see beyond operations. Users and data hold personal data, so a realm
// opts in.
type ReadAccess struct {
	Users bool
	Data  bool
}

// AdminRights is what the roles let a user change together: the union of the
// areas each role opens.
func (s RealmSettings) AdminRights(roles []string) AdminRights {
	return s.AdminAccess(roles).Write
}

// AdminAccess is what the roles let a user do in the admin API, added up.
func (s RealmSettings) AdminAccess(roles []string) AdminAccess {
	var out AdminAccess
	for _, r := range roles {
		out.Write |= s.Roles[r].Admin
		out.ReadAll = out.ReadAll || s.Roles[r].AdminRead
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
