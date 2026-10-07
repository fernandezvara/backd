package main

import (
	"fmt"

	"github.com/fernandezvara/cli"

	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/settings"
)

// checkConfig and configFingerprint handle `backd config check|fingerprint`.
// They need only CONFIG_DIR.
func checkConfig(c *cli.CommandContext) error { return configCommand(c, false) }

func configFingerprint(c *cli.CommandContext) error { return configCommand(c, true) }

func configCommand(c *cli.CommandContext, fingerprintOnly bool) error {
	dir, err := configDir(c)
	if err != nil {
		return err
	}
	reg, err := registry.Load(dir)
	if err == nil {
		err = reg.CheckBundles()
	}
	if err == nil {
		var max int64
		if max, err = settings.ImageMaxPixels(c.Getenv); err == nil {
			err = reg.CheckImageLimits(max)
		}
	}
	if err != nil {
		return fmt.Errorf("invalid config:\n%w", err)
	}
	fp, err := reg.Fingerprint()
	if err != nil {
		return err
	}
	if fingerprintOnly {
		fmt.Fprintln(c.Stdout(), fp)
		return nil
	}
	fmt.Fprintf(c.Stdout(), "config OK: %d realms, %d databases; fingerprint %s\n", len(reg.Realms), len(reg.Databases()), fp)
	return nil
}
