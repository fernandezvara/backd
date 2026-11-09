package servercli

import (
	"context"
	"fmt"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/cmdutil"
	"github.com/fernandezvara/backd/internal/functions"
	"github.com/fernandezvara/backd/internal/registry"
)

func functionsBuild(c *kli.CommandContext) error {
	dir, err := cmdutil.ConfigDir(c)
	if err != nil {
		return err
	}
	reg, err := registry.Load(dir)
	if err != nil {
		return fmt.Errorf("invalid config:\n%w", err)
	}
	built := 0
	err = functions.Build(context.Background(), reg, functions.Options{
		Deno:  c.Getenv("DENO"),
		Check: cmdutil.Flag(c, "check"),
		Log: func(format string, a ...any) {
			built++
			fmt.Fprintf(c.Stdout(), format+"\n", a...)
		},
	})
	if err != nil {
		return err
	}
	if built == 0 {
		fmt.Fprintln(c.Stdout(), "no functions in CONFIG_DIR")
	}
	return nil
}
