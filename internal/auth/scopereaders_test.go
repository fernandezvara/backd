package auth

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// allMembershipReaders are the calls that read every membership of a person, not those of the
// organization a session is scoped to. A session reaches one organization (design/scoped-sessions.md
// §4.2), so a new reader of this kind is a way to see the others: it fails this test until
// somebody decides, here and with a reason, that it may exist.
var allMembershipReaders = map[string]bool{"MyOrgs": true, "OrgMembershipsOf": true, "CountOrgMembershipsOf": true}

// allowedMembershipReaders names each caller (file#function, relative to internal/) and why it
// may read the memberships of a person across organizations.
var allowedMembershipReaders = map[string]string{
	// The readers themselves and the checks about the person, not about a session.
	"auth/orgs.go#MyOrgs":               "the reader itself; its callers are listed below",
	"auth/orgs.go#checkMembershipCap":   "counts memberships against max_memberships_per_user when somebody joins",
	"auth/orgdelete.go#OrgsManagedBy":   "erasing or leaving an account must not orphan an organization, in any of them",
	"auth/accountexport.go#accountOrgs": "the person's own export is account-level, and refused to connection sessions",
	// Before there is a session: the person asks which companies they are in.
	"auth/orgsignin.go#SignInLinks": "the `find` email goes to the owner of the address, never to a session",
	// Reads that only schedule work or shape an envelope.
	"auth/orgteamroles.go#NextAccessChange": "the next moment a window opens or closes; it returns a time, not data",
	"httpapi/functions.go#userOrgsEnvelope": "keeps only the entry of the session's organization",
}

func TestEveryReaderOfAllMembershipsIsKnown(t *testing.T) {
	root := ".."
	fset := token.NewFileSet()
	found := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		if strings.Contains(path, "/mongodb/") || strings.Contains(path, "/authtest/") {
			return nil // the stores implement the readers; they don't use them
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && allMembershipReaders[sel.Sel.Name] {
						found[filepath.ToSlash(rel)+"#"+fn.Name.Name] = true
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var unknown []string
	for k := range found {
		if _, ok := allowedMembershipReaders[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		t.Errorf("these read every membership of a person, not the session's organization; scope them or list them in allowedMembershipReaders with the reason: %v", unknown)
	}
	for k := range allowedMembershipReaders {
		if !found[k] && !strings.HasSuffix(k, "#MyOrgs") {
			t.Errorf("%s is listed but no longer reads the memberships: remove it", k)
		}
	}
}
