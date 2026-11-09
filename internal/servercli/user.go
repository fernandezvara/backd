package servercli

import (
	"context"
	"errors"
	"fmt"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/cmdutil"
	"github.com/fernandezvara/backd/internal/server"
)

// userMFAResetLocal is the break-glass form: it acts on the database directly, like
// `backd-server bootstrap`, so an operator can recover the only administrator of a realm.
func userMFAResetLocal(c *kli.CommandContext) error {
	if !cmdutil.Flag(c, "yes") {
		return errors.New("resetting a second factor ends all of the user's sessions and can't be undone: add --yes to confirm")
	}
	ctx := context.Background()
	uio := cmdutil.IOOf(c)
	a, err := server.Setup(ctx, c.Getenv, uio.Stderr)
	if err != nil {
		return err
	}
	defer a.Close()
	users, err := a.UsersOf(ctx, cmdutil.Str(c, "realm"))
	if err != nil {
		return err
	}
	u, err := users.Find(ctx, cmdutil.Str(c, "email"))
	if err != nil {
		return fmt.Errorf("user not found: %s", cmdutil.Str(c, "email"))
	}
	ctx = auth.WithAuditSource(ctx, func() auth.AuditSource { return auth.AuditSource{Actor: auth.ActorInstance} })
	if err := users.ResetMFA(ctx, u.ID, ""); err != nil {
		return mfaResetError(u.Email, err)
	}
	fmt.Fprintf(uio.Stdout, "second factor of %s removed; their sessions ended (audited as %s)\n", u.Email, auth.ActorInstance)
	return nil
}

// mfaResetError says what a failed second-factor reset means.
func mfaResetError(email string, err error) error {
	if errors.Is(err, auth.ErrMFANotEnabled) {
		return fmt.Errorf("%s has no second factor", email)
	}
	return err
}
