package main

import (
	"fmt"
	"net/url"

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
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	var res consentNotice
	if err := t.adm.Consents.Notify(t.ctx, map[string]any{"consent": str(c, "consent")}, &res); err != nil {
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

// consentsRecord handles `backd consents record`: an administrator records a consent a
// person gave, or withdrew, outside the app, with the evidence for it.
func consentsRecord(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	var page struct {
		Items []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"items"`
	}
	email := str(c, "email")
	if err := t.adm.Users.List(t.ctx, url.Values{"email": {email}}, &page); err != nil {
		return err
	}
	if len(page.Items) == 0 {
		return fmt.Errorf("user not found: %s", email)
	}
	body := map[string]any{"consent": str(c, "consent"), "action": str(c, "action"), "evidence": str(c, "evidence")}
	if v := str(c, "version"); v != "" {
		body["version"] = v
	}
	var out struct {
		Consent  string `json:"consent"`
		Version  string `json:"version"`
		Action   string `json:"action"`
		Evidence string `json:"evidence"`
		At       string `json:"at"`
	}
	if err := t.adm.Users.AcceptConsents(t.ctx, page.Items[0].ID, body, &out); err != nil {
		return err
	}
	fmt.Fprintf(c.Stdout(), "recorded: %s %s %s for %s (evidence: %s)\n", out.Action, out.Consent, out.Version, page.Items[0].Email, out.Evidence)
	return nil
}
