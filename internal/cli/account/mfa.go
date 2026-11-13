package account

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fernandezvara/backd/internal/cli/clicore"

	"github.com/boombuler/barcode/qr"

	"github.com/fernandezvara/backd/internal/cmdutil"
	"github.com/fernandezvara/backd/pkg/client"
)

// The second step of a sign-in in the terminal (design/mfa-passwordless.md §11): a code when the
// user has a second factor, setting one up when their role requires it, and magic links.

// finishStep takes what a first factor led to, to a session: it asks for the code of a second
// factor, or sets one up, as the step says. res is filled with the session.
func finishStep(t *clicore.Target, uio cmdutil.IO, step *client.Step, res *sessionAnswer) error {
	switch step.Kind {
	case client.StepSession:
		*res = sessionOf(step.Session)
		return nil
	case client.StepMFA:
		return askSecondFactor(t, uio, step.Challenge, res)
	case client.StepEnrollment:
		return enrollInTerminal(t, uio, step.Challenge, res)
	}
	return errors.New("the server answered with neither a session nor a second step")
}

// codeIsRecovery says a typed code is a recovery code (xxxxx-xxxxx) and not six digits.
func codeIsRecovery(code string) bool {
	code = strings.TrimSpace(code)
	return strings.Contains(code, "-") || len(strings.ReplaceAll(code, " ", "")) > 6
}

// askSecondFactor asks for the code of an authenticator app, or a recovery code, up to three times
// on a terminal (the challenge allows five).
func askSecondFactor(t *clicore.Target, uio cmdutil.IO, challenge string, res *sessionAnswer) error {
	tries := 1
	if cmdutil.IsTerminal(uio.Stdin) {
		tries = 3
	}
	var last error
	for range tries {
		code, err := cmdutil.ReadLine(uio, "Code from your authenticator app (or a recovery code)")
		if err != nil {
			return err
		}
		method := "totp"
		if codeIsRecovery(code) {
			method = "recovery_code"
		}
		step, err := t.Client.Auth().VerifyStep(t.Ctx, challenge, method, code)
		var ae *clicore.APIError
		switch {
		case err == nil && step.Kind != client.StepSession:
			// A strategy of several steps: the next one.
			return finishStep(t, uio, step, res)
		case err == nil:
			session := step.Session
			*res = sessionOf(session)
			if session.RecoveryCodesLeft != nil {
				fmt.Fprintf(uio.Stderr, "note: a recovery code was used; %d left. Replace them with `backd account recovery-codes` when you can\n", *session.RecoveryCodesLeft)
			}
			return nil
		case errors.As(err, &ae) && ae.Code == "invalid_code":
			last = err
			fmt.Fprintln(uio.Stderr, "that code is not right")
		default:
			return err
		}
	}
	return last
}

// enrollInTerminal sets TOTP up for a sign-in whose role requires it: it shows the secret as an
// otpauth:// address and as a QR code of block characters, asks for the first code, and shows
// the recovery codes once.
func enrollInTerminal(t *clicore.Target, uio cmdutil.IO, challenge string, res *sessionAnswer) error {
	fmt.Fprintln(uio.Stderr, "this account must use two-step verification: set up an authenticator app")
	setup, err := t.Client.Auth().MFA.Setup(t.Ctx, challenge)
	if err != nil {
		return err
	}
	showSetup(uio, setup.OtpauthURI, setup.Secret)
	out, err := confirmSetup(uio, func(code string) (*client.MFAConfirmResult, error) {
		return t.Client.Auth().MFA.Confirm(t.Ctx, client.MFAConfirmInput{Challenge: challenge, SetupID: setup.SetupID, Code: code})
	})
	if err != nil {
		return err
	}
	if out.Session != nil {
		*res = sessionOf(out.Session)
	}
	showRecoveryCodes(uio, out.RecoveryCodes)
	return nil
}

// showSetup prints what an authenticator app needs: the QR code, the address it holds and the secret
// for typing in.
func showSetup(uio cmdutil.IO, uri, secret string) {
	fmt.Fprintln(uio.Stderr)
	if art, err := QRBlocks(uri); err == nil {
		fmt.Fprintln(uio.Stderr, art)
	}
	fmt.Fprintf(uio.Stderr, "Scan the code with your authenticator app, or add it by hand:\n  address: %s\n  secret:  %s\n\n", uri, secret)
}

// showRecoveryCodes prints the codes once and, on a terminal, waits until the user says they are
// saved: they can't be shown again.
func showRecoveryCodes(uio cmdutil.IO, codes []string) {
	fmt.Fprintln(uio.Stdout, "Recovery codes (each works once; they are shown only now):")
	for _, c := range codes {
		fmt.Fprintf(uio.Stdout, "  %s\n", c)
	}
	if cmdutil.IsTerminal(uio.Stdin) {
		fmt.Fprint(uio.Stderr, "Save them somewhere safe (not in the phone with the app), then press Enter: ")
		_, _ = cmdutil.ReadLine(uio, "")
	} else {
		fmt.Fprintln(uio.Stderr, "save them somewhere safe: they can't be shown again")
	}
}

// qrBlocks draws text as a QR code of half-block characters, two rows of modules per line, with a
// quiet zone. Light modules (and the quiet zone) are blocks and dark ones are blank, which is a
// dark code on a light ground in the usual light-on-dark terminal; readers also take it inverted.
func QRBlocks(text string) (string, error) {
	code, err := qr.Encode(text, qr.M, qr.Auto)
	if err != nil {
		return "", err
	}
	dark := func(x, y int) bool {
		b := code.Bounds()
		if x < b.Min.X || y < b.Min.Y || x >= b.Max.X || y >= b.Max.Y {
			return false // the quiet zone
		}
		r, _, _, _ := code.At(x, y).RGBA()
		return r < 0x8000
	}
	b := code.Bounds()
	const quiet = 2
	var sb strings.Builder
	for y := b.Min.Y - quiet; y < b.Max.Y+quiet; y += 2 {
		for x := b.Min.X - quiet; x < b.Max.X+quiet; x++ {
			top, bottom := !dark(x, y), !dark(x, y+1)
			switch {
			case top && bottom:
				sb.WriteString("█")
			case top:
				sb.WriteString("▀")
			case bottom:
				sb.WriteString("▄")
			default:
				sb.WriteString(" ")
			}
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// magicLinkSignIn signs in with a link or a code sent to an address: it asks for a message, then
// for the code in it, and ends with the session or, for a user with a second factor, the second step.
func magicLinkSignIn(t *clicore.Target, uio cmdutil.IO, address, strategy string, res *sessionAnswer) error {
	if _, err := t.Client.Auth().MagicLink.Request(t.Ctx, address, client.MagicLinkOptions{Strategy: strategy}); err != nil {
		return err
	}
	fmt.Fprintf(uio.Stderr, "if %s can sign in, a message with a 6-digit code is on its way\n", address)
	code, err := cmdutil.ReadLine(uio, "Code from the email")
	if err != nil {
		return err
	}
	step, err := t.Client.Auth().MagicLink.VerifyCode(t.Ctx, code)
	if err != nil {
		return err
	}
	return finishStep(t, uio, step, res)
}

// confirmSetup asks for the first code of a new authenticator and confirms it, asking again
// after a wrong code (up to three times, on a terminal only).
func confirmSetup(uio cmdutil.IO, confirm func(code string) (*client.MFAConfirmResult, error)) (*client.MFAConfirmResult, error) {
	for tries := 0; ; tries++ {
		code, err := cmdutil.ReadLine(uio, "Code shown by the app, to confirm")
		if err != nil {
			return nil, err
		}
		out, err := confirm(code)
		if err == nil {
			return out, nil
		}
		var ae *clicore.APIError
		if !errors.As(err, &ae) || ae.Code != "invalid_code" || tries >= 2 || !cmdutil.IsTerminal(uio.Stdin) {
			return nil, err
		}
		fmt.Fprintln(uio.Stderr, "that code is not right: try the next one the app shows")
	}
}
