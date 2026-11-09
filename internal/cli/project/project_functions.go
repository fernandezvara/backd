package project

import (
	"fmt"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/cmdutil"
	"github.com/fernandezvara/backd/internal/functions"
	"github.com/fernandezvara/backd/internal/registry"
)

func FunctionsTypes(c *kli.CommandContext) error {
	dir, err := cmdutil.ConfigDir(c)
	if err != nil {
		return err
	}
	reg, err := registry.Load(dir)
	if err != nil {
		return fmt.Errorf("invalid config:\n%w", err)
	}
	fmt.Fprint(c.Stdout(), functions.JSDocTypes(reg))
	return nil
}
