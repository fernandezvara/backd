package main

import (
	"bufio"
	"errors"
	"fmt"
	"strings"

	"github.com/fernandezvara/kli"
)

// `backd account …` manages the signed-in user's own sign-in security through the API with the
// session `backd login` stored: the second factor and its recovery codes. Each change asks for
// a fresh proof of identity, as the server does.

// accountTarget resolves the stored session of the realm (an API key is not a person).
func accountTarget(c *kli.CommandContext) (*target, userIO, error) {
	t, err := targetOf(c, true)
	if err != nil {
		return nil, userIO{}, err
	}
	uio := ioOf(c)
	if !isTerminal(uio.stdin) {
		uio.stdin = bufio.NewReader(uio.stdin)
	}
	return t, uio, nil
}

// accountMFAStatus shows whether the user has a second factor.
func accountMFAStatus(c *kli.CommandContext) error {
	t, uio, err := accountTarget(c)
	if err != nil {
		return err
	}
	var st struct {
		Available         bool     `json:"available"`
		Enabled           bool     `json:"enabled"`
		Methods           []string `json:"methods"`
		RecoveryCodesLeft int      `json:"recovery_codes_left"`
		Required          bool     `json:"required"`
	}
	if err := t.call("GET", "_auth/mfa", nil, nil, &st); err != nil {
		return err
	}
	switch {
	case st.Enabled:
		fmt.Fprintf(uio.stdout, "two-step verification: on (%s); recovery codes left: %d; required by a role: %t\n", joinList(st.Methods), st.RecoveryCodesLeft, st.Required)
	case !st.Available:
		fmt.Fprintln(uio.stdout, "two-step verification: not available in this realm")
	default:
		fmt.Fprintf(uio.stdout, "two-step verification: off; required by a role: %t (turn it on with `backd account mfa-enable`)\n", st.Required)
	}
	return nil
}

func joinList(items []string) string {
	var out strings.Builder
	for i, s := range items {
		if i > 0 {
			out.WriteString(", ")
		}
		out.WriteString(s)
	}
	return out.String()
}

// accountMFAEnable sets an authenticator app up for the signed-in user.
func accountMFAEnable(c *kli.CommandContext) error {
	t, uio, err := accountTarget(c)
	if err != nil {
		return err
	}
	proof, err := t.proof(uio)
	if err != nil {
		return err
	}
	var setup struct {
		SetupID string `json:"setup_id"`
		Secret  string `json:"secret"`
		URI     string `json:"otpauth_uri"`
	}
	if _, err := t.callHeaders("POST", "_auth/mfa/totp/setup", nil, nil, &setup, proof); err != nil {
		return err
	}
	showSetup(uio, setup.URI, setup.Secret)
	var out struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	for tries := 0; ; tries++ {
		code, err := readLine(uio, "Code shown by the app, to confirm")
		if err != nil {
			return err
		}
		_, err = t.callHeaders("POST", "_auth/mfa/totp/confirm", nil, map[string]string{"setup_id": setup.SetupID, "code": code}, &out, proof)
		var ae *apiError
		if err == nil {
			break
		}
		if !errors.As(err, &ae) || ae.Code != "invalid_code" || tries >= 2 || !isTerminal(uio.stdin) {
			return err
		}
		fmt.Fprintln(uio.stderr, "that code is not right: try the next one the app shows")
	}
	fmt.Fprintln(uio.stderr, "two-step verification is on; your other sessions ended")
	showRecoveryCodes(uio, out.RecoveryCodes)
	return nil
}

// accountMFADisable turns the second factor off.
func accountMFADisable(c *kli.CommandContext) error {
	if !flag(c, "yes") {
		return errors.New("turning two-step verification off weakens the account: add --yes to confirm")
	}
	t, uio, err := accountTarget(c)
	if err != nil {
		return err
	}
	proof, err := t.proof(uio)
	if err != nil {
		return err
	}
	if _, err := t.callHeaders("DELETE", "_auth/mfa/totp", nil, nil, nil, proof); err != nil {
		return err
	}
	fmt.Fprintln(uio.stdout, "two-step verification is off")
	return nil
}

// accountRecoveryCodes replaces the recovery codes.
func accountRecoveryCodes(c *kli.CommandContext) error {
	t, uio, err := accountTarget(c)
	if err != nil {
		return err
	}
	proof, err := t.proof(uio)
	if err != nil {
		return err
	}
	var out struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	if _, err := t.callHeaders("POST", "_auth/mfa/recovery-codes", nil, nil, &out, proof); err != nil {
		return err
	}
	fmt.Fprintln(uio.stderr, "the earlier recovery codes no longer work")
	showRecoveryCodes(uio, out.RecoveryCodes)
	return nil
}
