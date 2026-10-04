package auth

import (
	"fmt"
	"slices"
	"strings"

	"github.com/fernandezvara/backd/internal/registry"
)

// Scopes limit what a data API key reaches, so a leaked key exposes only what
// it was made for. A key without scopes keeps full access (every key made
// before scopes existed); a key with scopes may do exactly what they grant and
// nothing else. A grant is `ops` or `ops:target`:
//
//	read                  read every collection
//	read:blog             read the collections of the database blog
//	write:blog/posts      create, update and delete in blog/posts
//	call                  call every function
//	call:main             call the functions of the database main
//	call:main/export      call that function (and read its jobs)
//
// write means create, update and delete; it doesn't include read. The targets
// are relative to the key's realm.
type Scope struct {
	Op       ScopeOp
	Database string // "" is every database
	Name     string // a collection (read, write) or a function (call); "" is all of the database
}

// ScopeOp is the kind of access a grant gives.
type ScopeOp string

const (
	ScopeRead  ScopeOp = "read"
	ScopeWrite ScopeOp = "write"
	ScopeCall  ScopeOp = "call"
)

// MaxScopes bounds the grants of one key.
const MaxScopes = 32

// ParseScope parses one grant.
func ParseScope(s string) (Scope, error) {
	opText, target, hasTarget := strings.Cut(strings.TrimSpace(s), ":")
	sc := Scope{Op: ScopeOp(opText)}
	switch sc.Op {
	case ScopeRead, ScopeWrite, ScopeCall:
	default:
		return Scope{}, fmt.Errorf("invalid scope %q: it starts with read, write or call", s)
	}
	if !hasTarget {
		return sc, nil
	}
	db, name, hasName := strings.Cut(target, "/")
	if !registry.ValidName(db) || (hasName && !registry.ValidName(name)) {
		return Scope{}, fmt.Errorf("invalid scope %q: the target is <database> or <database>/<name>, in lowercase letters and digits", s)
	}
	sc.Database, sc.Name = db, name
	return sc, nil
}

func (sc Scope) String() string {
	switch {
	case sc.Database == "":
		return string(sc.Op)
	case sc.Name == "":
		return string(sc.Op) + ":" + sc.Database
	}
	return string(sc.Op) + ":" + sc.Database + "/" + sc.Name
}

// covers reports whether the grant gives op on database/name.
func (sc Scope) covers(op ScopeOp, database, name string) bool {
	return sc.Op == op && (sc.Database == "" || (sc.Database == database && (sc.Name == "" || sc.Name == name)))
}

// Scopes is the list of grants of a key.
type Scopes []Scope

// ParseScopes parses grants; duplicates collapse, and the order is kept.
func ParseScopes(list []string) (Scopes, error) {
	if len(list) > MaxScopes {
		return nil, fmt.Errorf("at most %d scopes per key", MaxScopes)
	}
	var out Scopes
	for _, s := range list {
		sc, err := ParseScope(s)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, sc) {
			out = append(out, sc)
		}
	}
	return out, nil
}

// Strings writes the grants as they are parsed.
func (s Scopes) Strings() []string {
	out := make([]string, len(s))
	for i, sc := range s {
		out[i] = sc.String()
	}
	return out
}

// Limited reports whether the key has scopes at all: without, it has full access.
func (s Scopes) Limited() bool { return len(s) > 0 }

// Allows reports whether op on database/name is granted. A key without scopes
// allows everything.
func (s Scopes) Allows(op ScopeOp, database, name string) bool {
	if !s.Limited() {
		return true
	}
	for _, sc := range s {
		if sc.covers(op, database, name) {
			return true
		}
	}
	return false
}
