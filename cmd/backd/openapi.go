package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/openapigen"
	"github.com/fernandezvara/backd/internal/registry"
)

// openapiCommand handles `backd openapi`: it generates a realm's OpenAPI document from the
// local CONFIG_DIR, to standard output or --out, or with --check compares the file.
func openapiCommand(c *kli.CommandContext) error {
	realm, out := str(c, "realm"), str(c, "out")
	check := flag(c, "check")
	if check && out == "" {
		return errors.New("--check needs --out: the file to compare with")
	}
	dir, err := configDir(c)
	if err != nil {
		return err
	}
	reg, err := registry.Load(dir)
	if err != nil {
		return fmt.Errorf("invalid config:\n%w", err)
	}
	doc, err := openapigen.Public(reg, realm)
	if err != nil {
		return err
	}
	switch {
	case check:
		have, err := os.ReadFile(out)
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s doesn't exist: run `backd openapi --realm %s --out %s`", out, realm, out)
		}
		if err != nil {
			return err
		}
		if !bytes.Equal(have, doc.JSON) {
			return fmt.Errorf("%s is stale: run `backd openapi --realm %s --out %s` and commit the result", out, realm, out)
		}
		fmt.Fprintf(c.Stdout(), "%s is up to date\n", out)
	case out != "":
		if err := os.WriteFile(out, doc.JSON, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(c.Stdout(), "wrote %s\n", out)
	default:
		_, err = c.Stdout().Write(doc.JSON)
		return err
	}
	return nil
}
