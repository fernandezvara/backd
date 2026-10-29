package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/boombuler/barcode/qr"

	"github.com/fernandezvara/backd/pkg/client"
)

// The second step of a sign-in in the terminal (design/mfa-passwordless.md §11): a code when the
// user has a second factor, setting one up when their role requires it, and magic links.

// finishStep takes what a first factor led to, to a session: it asks for the code of a second
// factor, or sets one up, as the step says. res is filled with the session.
func finishStep(t *target, uio userIO, step *client.Step, res *sessionAnswer) error {
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
func askSecondFactor(t *target, uio userIO, challenge string, res *sessionAnswer) error {
	tries := 1
	if isTerminal(uio.stdin) {
		tries = 3
	}
	var last error
	for range tries {
		code, err := readLine(uio, "Code from your authenticator app (or a recovery code)")
		if err != nil {
			return err
		}
		method := "totp"
		if codeIsRecovery(code) {
			method = "recovery_code"
		}
		session, err := t.cl.Auth().VerifyMFA(t.ctx, challenge, method, code)
		var ae *apiError
		switch {
		case err == nil:
			*res = sessionOf(session)
			if session.RecoveryCodesLeft != nil {
				fmt.Fprintf(uio.stderr, "note: a recovery code was used; %d left. Replace them with `backd account recovery-codes` when you can\n", *session.RecoveryCodesLeft)
			}
			return nil
		case errors.As(err, &ae) && ae.Code == "invalid_code":
			last = err
			fmt.Fprintln(uio.stderr, "that code is not right")
		default:
			return err
		}
	}
	return last
}

// enrollInTerminal sets TOTP up for a sign-in whose role requires it: it shows the secret as an
// otpauth:// address and as a QR code of block characters, asks for the first code, and shows
// the recovery codes once.
func enrollInTerminal(t *target, uio userIO, challenge string, res *sessionAnswer) error {
	fmt.Fprintln(uio.stderr, "this account must use two-step verification: set up an authenticator app")
	setup, err := t.cl.Auth().MFA.Setup(t.ctx, challenge)
	if err != nil {
		return err
	}
	showSetup(uio, setup.OtpauthURI, setup.Secret)
	var out *client.MFAConfirmResult
	for tries := 0; ; tries++ {
		code, err := readLine(uio, "Code shown by the app, to confirm")
		if err != nil {
			return err
		}
		out, err = t.cl.Auth().MFA.Confirm(t.ctx, client.MFAConfirmInput{Challenge: challenge, SetupID: setup.SetupID, Code: code})
		var ae *apiError
		if err == nil {
			break
		}
		if !errors.As(err, &ae) || ae.Code != "invalid_code" || tries >= 2 || !isTerminal(uio.stdin) {
			return err
		}
		fmt.Fprintln(uio.stderr, "that code is not right: try the next one the app shows")
	}
	if out.Session != nil {
		*res = sessionOf(out.Session)
	}
	showRecoveryCodes(uio, out.RecoveryCodes)
	return nil
}

// showSetup prints what an authenticator app needs: the QR code, the address it holds and the secret
// for typing in.
func showSetup(uio userIO, uri, secret string) {
	fmt.Fprintln(uio.stderr)
	if art, err := qrBlocks(uri); err == nil {
		fmt.Fprintln(uio.stderr, art)
	}
	fmt.Fprintf(uio.stderr, "Scan the code with your authenticator app, or add it by hand:\n  address: %s\n  secret:  %s\n\n", uri, secret)
}

// showRecoveryCodes prints the codes once and, on a terminal, waits until the user says they are
// saved: they can't be shown again.
func showRecoveryCodes(uio userIO, codes []string) {
	fmt.Fprintln(uio.stdout, "Recovery codes (each works once; they are shown only now):")
	for _, c := range codes {
		fmt.Fprintf(uio.stdout, "  %s\n", c)
	}
	if isTerminal(uio.stdin) {
		fmt.Fprint(uio.stderr, "Save them somewhere safe (not in the phone with the app), then press Enter: ")
		_, _ = readLine(uio, "")
	} else {
		fmt.Fprintln(uio.stderr, "save them somewhere safe: they can't be shown again")
	}
}

// qrBlocks draws text as a QR code of half-block characters, two rows of modules per line, with a
// quiet zone. Light modules (and the quiet zone) are blocks and dark ones are blank, which is a
// dark code on a light ground in the usual light-on-dark terminal; readers also take it inverted.
func qrBlocks(text string) (string, error) {
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
func magicLinkSignIn(t *target, uio userIO, address string, res *sessionAnswer) error {
	if _, err := t.cl.Auth().MagicLink.Request(t.ctx, address, client.MagicLinkOptions{}); err != nil {
		return err
	}
	fmt.Fprintf(uio.stderr, "if %s can sign in, a message with a 6-digit code is on its way\n", address)
	code, err := readLine(uio, "Code from the email")
	if err != nil {
		return err
	}
	step, err := t.cl.Auth().MagicLink.VerifyCode(t.ctx, code)
	if err != nil {
		return err
	}
	return finishStep(t, uio, step, res)
}
