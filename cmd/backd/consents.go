package main

import (
	"fmt"

	"github.com/fernandezvara/kli"
)

type consentNotice struct {
	Consent string `json:"consent"`
	Version string `json:"version"`
	Matched int    `json:"matched"`
	Queued  int    `json:"queued"`
	Limited int    `json:"limited"`
}

// consentsNotify handles `backd consents notify`: ask the users who hold an older version
// of a consent to review the current one, by email.
func consentsNotify(c *kli.CommandContext) error {
	t, err := newTarget(str(c, "url"), str(c, "realm"), c.Getenv, false)
	if err != nil {
		return err
	}
	var res consentNotice
	if err := t.call("POST", "_admin/consents/notify", nil, map[string]any{"consent": str(c, "consent")}, &res); err != nil {
		return err
	}
	if flag(c, "json") {
		return encodeJSONLines(c.Stdout(), []consentNotice{res})
	}
	fmt.Fprintf(c.Stdout(), "%s %s: %d users hold an older version; %d emails queued", res.Consent, res.Version, res.Matched, res.Queued)
	if res.Limited > 0 {
		fmt.Fprintf(c.Stdout(), ", %d skipped by the email limits (run it again later: those still holding an older version are asked again)", res.Limited)
	}
	fmt.Fprintln(c.Stdout())
	return nil
}
