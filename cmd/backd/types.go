package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"net/url"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/typegen"
)

// typesCommand handles `backd types`: it generates a realm's TypeScript types from the local
// CONFIG_DIR, or from a running instance with --from, and writes them, or checks or diffs
// them against the files already there.
func typesCommand(c *kli.CommandContext) error {
	realm, db, out := str(c, "realm"), str(c, "db"), str(c, "out")
	check, diff := flag(c, "check"), flag(c, "diff")

	var gen *typegen.Output
	var err error
	if from := str(c, "from"); from != "" {
		gen, err = typesFromServer(c, from, realm, db)
	} else {
		gen, err = typesFromConfig(c, realm, db)
	}
	if err != nil {
		return err
	}
	for _, w := range gen.Warnings {
		fmt.Fprintf(c.Stderr(), "warning: %s\n", w)
	}

	typesPath, infoPath := filepath.Join(out, typegen.TypesFile), filepath.Join(out, typegen.InfoFile)
	if !check && !diff {
		if err := os.MkdirAll(out, 0o755); err != nil {
			return err
		}
		info, err := marshalInfo(gen.Info)
		if err != nil {
			return err
		}
		if err := os.WriteFile(typesPath, []byte(gen.Types), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(infoPath, info, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(c.Stdout(), "wrote %s and %s (types %s)\n", typesPath, infoPath, gen.Info.Fingerprint[:16])
		return nil
	}

	existing, err := os.ReadFile(typesPath)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return err
	}
	var old typegen.Info
	if prev, err := os.ReadFile(infoPath); err == nil {
		old, err = unmarshalInfo(prev)
		if err != nil {
			return fmt.Errorf("%s: %w", infoPath, err)
		}
	}
	changes := typegen.Diff(old, gen.Info)
	if diff {
		if missing {
			fmt.Fprintf(c.Stdout(), "no generated types in %s yet\n", out)
		}
		for _, l := range changes {
			fmt.Fprintln(c.Stdout(), l)
		}
		if len(changes) == 0 && !missing {
			fmt.Fprintln(c.Stdout(), "no type-level changes")
		}
	}
	if check {
		switch {
		case missing:
			return fmt.Errorf("%s doesn't exist: run `backd types --realm %s --out %s`", typesPath, realm, out)
		case string(existing) != gen.Types:
			if !diff {
				for _, l := range changes {
					fmt.Fprintln(c.Stdout(), l)
				}
			}
			return fmt.Errorf("%s is stale: run `backd types --realm %s --out %s` and commit the result", typesPath, realm, out)
		}
		fmt.Fprintf(c.Stdout(), "types are up to date (types %s)\n", gen.Info.Fingerprint[:16])
	}
	return nil
}

func typesFromConfig(c *kli.CommandContext, realm, db string) (*typegen.Output, error) {
	dir, err := configDir(c)
	if err != nil {
		return nil, err
	}
	reg, err := registry.Load(dir)
	if err != nil {
		return nil, fmt.Errorf("invalid config:\n%w", err)
	}
	fp, err := reg.Fingerprint()
	if err != nil {
		return nil, err
	}
	return typegen.Generate(reg, typegen.Options{Realm: realm, Database: db, Config: fp})
}

func typesFromServer(c *kli.CommandContext, from, realm, db string) (*typegen.Output, error) {
	t, err := newTarget(from, realm, c.Getenv, false)
	if err != nil {
		return nil, err
	}
	var body struct {
		Types    string       `json:"types"`
		Info     typegen.Info `json:"info"`
		Warnings []string     `json:"warnings"`
	}
	var query url.Values
	if db != "" {
		query = url.Values{"db": {db}}
	}
	if err := t.call("GET", "_admin/types", query, nil, &body); err != nil {
		return nil, err
	}
	return &typegen.Output{Types: body.Types, Info: body.Info, Warnings: body.Warnings}, nil
}

func marshalInfo(i typegen.Info) ([]byte, error) {
	b, err := json.MarshalIndent(i, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func unmarshalInfo(b []byte) (typegen.Info, error) {
	var i typegen.Info
	err := json.Unmarshal(b, &i)
	return i, err
}
