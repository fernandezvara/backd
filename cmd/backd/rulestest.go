package main

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/fernandezvara/cli"

	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/rulestest"
)

// rulesTest handles `backd rules test`: it runs every collection's
// rules.test.yaml against its rules, with no database. Needs only CONFIG_DIR.
func rulesTest(c *cli.CommandContext) error {
	dir, err := configDir(c)
	if err != nil {
		return err
	}
	reg, err := registry.Load(dir)
	if err != nil {
		return fmt.Errorf("invalid config:\n%w", err)
	}
	only, verbose, strict := str(c, "collection"), flag(c, "verbose"), flag(c, "strict")
	out := c.Stdout()

	var tested, passed, failed, broken int
	var untested []string
	for _, rl := range reg.SortedRealms() {
		roles := slices.Sorted(maps.Keys(rl.Settings.Roles))
		for _, db := range sortedDatabases(rl) {
			for _, coll := range db.SortedCollections() {
				name := rl.Name + "/" + db.Name + "/" + coll.Name
				if only != "" && !strings.HasPrefix(name, only) {
					continue
				}
				fixture, err := os.ReadFile(filepath.Join(filepath.Dir(coll.SchemaPath), rulestest.FileName))
				if errors.Is(err, os.ErrNotExist) {
					if coll.Rules != nil {
						untested = append(untested, name)
					}
					continue
				}
				if err != nil {
					return err
				}
				checks, problems := rulestest.Run(coll, roles, fixture)
				tested++
				if len(problems) > 0 {
					broken++
					fmt.Fprintf(out, "%s: the fixture is wrong\n", name)
					for _, p := range problems {
						fmt.Fprintf(out, "  ! %v\n", p)
					}
					continue
				}
				bad := 0
				for _, ch := range checks {
					if !ch.Pass {
						bad++
					}
				}
				passed += len(checks) - bad
				failed += bad
				status := "ok"
				if bad > 0 {
					status = fmt.Sprintf("%d FAILED", bad)
				}
				fmt.Fprintf(out, "%s: %d assertions, %s\n", name, len(checks), status)
				for _, ch := range checks {
					switch {
					case !ch.Pass:
						fmt.Fprintf(out, "  ✗ %s [%s]\n      %s\n", ch.What, ch.Test, ch.Detail)
					case verbose:
						fmt.Fprintf(out, "  ✓ %s\n", ch.What)
					}
				}
			}
		}
	}
	if only != "" && tested == 0 && len(untested) == 0 {
		return fmt.Errorf("no collection matches --collection %q (use realm/database/collection, or a prefix)", only)
	}
	for _, name := range untested {
		fmt.Fprintf(out, "%s: has rules but no %s\n", name, rulestest.FileName)
	}
	fmt.Fprintf(out, "%d collections tested, %d assertions passed, %d failed", tested, passed, failed)
	if broken > 0 {
		fmt.Fprintf(out, ", %d fixtures wrong", broken)
	}
	fmt.Fprintln(out)
	switch {
	case failed > 0 || broken > 0:
		return cli.Exit(1, errors.New(""))
	case strict && len(untested) > 0:
		return cli.Exit(1, fmt.Errorf("%d collections with rules have no tests (--strict)", len(untested)))
	}
	return nil
}

func sortedDatabases(rl *registry.Realm) []*registry.Database {
	out := make([]*registry.Database, 0, len(rl.Databases))
	for _, name := range slices.Sorted(maps.Keys(rl.Databases)) {
		out = append(out, rl.Databases[name])
	}
	return out
}
