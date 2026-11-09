package account

import (
	"bufio"
	"errors"
	"fmt"
	"strings"

	"github.com/fernandezvara/backd/internal/cli/clicore"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/cmdutil"
	"github.com/fernandezvara/backd/pkg/client"
)

// `backd account …` manages the signed-in user's own sign-in security through the API with the
// session `backd login` stored: the second factor and its recovery codes. Each change asks for
// a fresh proof of identity, as the server does.

// accountTarget resolves the stored session of the realm (an API key is not a person).
func accountTarget(c *kli.CommandContext) (*clicore.Target, cmdutil.IO, error) {
	t, err := clicore.TargetOf(c, true)
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
func AccountMFAStatus(c *kli.CommandContext) error {
	t, uio, err := accountTarget(c)
	if err != nil {
		return err
	}
	st, err := t.Client.Auth().MFA.Status(t.Ctx)
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
func AccountMFAEnable(c *kli.CommandContext) error {
	t, uio, err := accountTarget(c)
	if err != nil {
		return err
	}
	proof, err := t.Proof(uio)
	if err != nil {
		return err
	}
	setup, err := t.Client.Auth().MFA.Setup(t.Ctx, "", clicore.WithProof(proof)...)
	if err != nil {
		return err
	}
	showSetup(uio, setup.OtpauthURI, setup.Secret)
	out, err := confirmSetup(uio, func(code string) (*client.MFAConfirmResult, error) {
		return t.Client.Auth().MFA.Confirm(t.Ctx, client.MFAConfirmInput{SetupID: setup.SetupID, Code: code}, clicore.WithProof(proof)...)
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(uio.Stderr, "two-step verification is on; your other sessions ended")
	showRecoveryCodes(uio, out.RecoveryCodes)
	return nil
}

// accountMFADisable turns the second factor off.
func AccountMFADisable(c *kli.CommandContext) error {
	if !cmdutil.Flag(c, "yes") {
		return errors.New("turning two-step verification off weakens the account: add --yes to confirm")
	}
	t, uio, err := accountTarget(c)
	if err != nil {
		return err
	}
	proof, err := t.Proof(uio)
	if err != nil {
		return err
	}
	if err := t.Client.Auth().MFA.Disable(t.Ctx, clicore.WithProof(proof)...); err != nil {
		return err
	}
	fmt.Fprintln(uio.Stdout, "two-step verification is off")
	return nil
}

// accountRecoveryCodes replaces the recovery codes.
func AccountRecoveryCodes(c *kli.CommandContext) error {
	t, uio, err := accountTarget(c)
	if err != nil {
		return err
	}
	proof, err := t.Proof(uio)
	if err != nil {
		return err
	}
	codes, err := t.Client.Auth().MFA.RegenerateRecoveryCodes(t.Ctx, clicore.WithProof(proof)...)
	if err != nil {
		return err
	}
	fmt.Fprintln(uio.Stderr, "the earlier recovery codes no longer work")
	showRecoveryCodes(uio, codes)
	return nil
}
