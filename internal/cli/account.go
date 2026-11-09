package cli

import (
	"bufio"
	"errors"
	"fmt"
	"strings"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/cmdutil"
	"github.com/fernandezvara/backd/pkg/client"
)

// `backd account …` manages the signed-in user's own sign-in security through the API with the
// session `backd login` stored: the second factor and its recovery codes. Each change asks for
// a fresh proof of identity, as the server does.

// accountTarget resolves the stored session of the realm (an API key is not a person).
func accountTarget(c *kli.CommandContext) (*target, cmdutil.IO, error) {
	t, err := targetOf(c, true)
	if err != nil {
		return nil, cmdutil.IO{}, err
	}
	uio := cmdutil.IOOf(c)
	if !cmdutil.IsTerminal(uio.Stdin) {
		uio.Stdin = bufio.NewReader(uio.Stdin)
	}
	return t, uio, nil
}

// accountMFAStatus shows whether the user has a second factor.
func accountMFAStatus(c *kli.CommandContext) error {
	t, uio, err := accountTarget(c)
	if err != nil {
		return err
	}
	st, err := t.cl.Auth().MFA.Status(t.ctx)
	if err != nil {
		return err
	}
	switch {
	case st.Enabled:
		fmt.Fprintf(uio.Stdout, "two-step verification: on (%s); recovery codes left: %d; required by a role: %t\n", joinList(st.Methods), st.RecoveryCodesLeft, st.Required)
	case !st.Available:
		fmt.Fprintln(uio.Stdout, "two-step verification: not available in this realm")
	default:
		fmt.Fprintf(uio.Stdout, "two-step verification: off; required by a role: %t (turn it on with `backd account mfa-enable`)\n", st.Required)
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
	setup, err := t.cl.Auth().MFA.Setup(t.ctx, "", withProof(proof)...)
	if err != nil {
		return err
	}
	showSetup(uio, setup.OtpauthURI, setup.Secret)
	var out *client.MFAConfirmResult
	for tries := 0; ; tries++ {
		code, err := cmdutil.ReadLine(uio, "Code shown by the app, to confirm")
		if err != nil {
			return err
		}
		out, err = t.cl.Auth().MFA.Confirm(t.ctx, client.MFAConfirmInput{SetupID: setup.SetupID, Code: code}, withProof(proof)...)
		var ae *apiError
		if err == nil {
			break
		}
		if !errors.As(err, &ae) || ae.Code != "invalid_code" || tries >= 2 || !cmdutil.IsTerminal(uio.Stdin) {
			return err
		}
		fmt.Fprintln(uio.Stderr, "that code is not right: try the next one the app shows")
	}
	fmt.Fprintln(uio.Stderr, "two-step verification is on; your other sessions ended")
	showRecoveryCodes(uio, out.RecoveryCodes)
	return nil
}

// accountMFADisable turns the second factor off.
func accountMFADisable(c *kli.CommandContext) error {
	if !cmdutil.Flag(c, "yes") {
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
	if err := t.cl.Auth().MFA.Disable(t.ctx, withProof(proof)...); err != nil {
		return err
	}
	fmt.Fprintln(uio.Stdout, "two-step verification is off")
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
	codes, err := t.cl.Auth().MFA.RegenerateRecoveryCodes(t.ctx, withProof(proof)...)
	if err != nil {
		return err
	}
	fmt.Fprintln(uio.Stderr, "the earlier recovery codes no longer work")
	showRecoveryCodes(uio, codes)
	return nil
}
