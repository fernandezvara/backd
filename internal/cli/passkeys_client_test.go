package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/fernandezvara/backd/internal/auth/passkey/passkeytest"
	"github.com/fernandezvara/backd/pkg/client"
)

// The Go client has no passkey sign-in of its own (a program has no authenticator), but its plain calls
// drive the server: here a software authenticator does the ceremonies against the real handlers.
func TestGoClientPasskeyCalls(t *testing.T) {
	f := newCLIFixture(t, cliRealmPasskeys)
	ctx := context.Background()
	pw := cliPassword
	if _, err := f.svc.Create(ctx, "ada@example.com", &pw); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(client.Options{URL: f.url, Realm: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	auth := c.Auth()
	if _, err := auth.Login(ctx, client.SignInInput{Email: "ada@example.com", Password: cliPassword, Strategy: "password"}); err != nil {
		t.Fatal(err)
	}
	key := passkeytest.New(cliPasskeyOrigin)

	// Adding one needs a fresh proof, then the two calls around the authenticator.
	if _, err := auth.Passkeys.RegisterOptions(ctx); err == nil {
		t.Fatal("registering without a proof worked")
	}
	if _, err := auth.Reauth(ctx, client.ReauthInput{Method: "password", Password: cliPassword}); err != nil {
		t.Fatal(err)
	}
	start, err := auth.Passkeys.RegisterOptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	made, err := auth.Passkeys.RegisterVerify(ctx, start.ChallengeID, key.Register(t, start.Options), "Laptop")
	if err != nil || made.Name != "Laptop" {
		t.Fatalf("register: %v %+v", err, made)
	}
	list, err := auth.Passkeys.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != made.ID {
		t.Fatalf("list: %v %+v", err, list)
	}
	if renamed, err := auth.Passkeys.Rename(ctx, made.ID, "Work laptop"); err != nil || renamed.Name != "Work laptop" {
		t.Fatalf("rename: %v %+v", err, renamed)
	}

	// Signing in with it: options, the authenticator's signature, verify.
	other, _ := client.New(client.Options{URL: f.url, Realm: "acme"})
	defer other.Close()
	signIn, err := other.Auth().Passkeys.SignInOptions(ctx, client.PasskeySignInInput{Strategy: "passkey"})
	if err != nil {
		t.Fatal(err)
	}
	step, err := other.Auth().Passkeys.SignInVerify(ctx, "", signIn.ChallengeID, key.Assert(t, signIn.Options))
	if err != nil || step.Kind != client.StepSession || step.Session.User.Email != "ada@example.com" {
		t.Fatalf("sign in: %v %+v", err, step)
	}
	if _, err := other.Auth().Passkeys.List(ctx); err != nil {
		t.Errorf("the session of the sign-in: %v", err)
	}

	// As a later step: the password, then the passkey.
	third, _ := client.New(client.Options{URL: f.url, Realm: "acme"})
	defer third.Close()
	first, err := third.Auth().SignIn(ctx, client.SignInInput{Email: "ada@example.com", Password: cliPassword, Strategy: "password_passkey"})
	if err != nil || first.Kind != client.StepMFA || len(first.Next) != 1 || first.Next[0] != "passkey" {
		t.Fatalf("first step: %v %+v", err, first)
	}
	stepOpts, err := third.Auth().Passkeys.StepOptions(ctx, first.Challenge)
	if err != nil {
		t.Fatal(err)
	}
	done, err := third.Auth().Passkeys.VerifyStep(ctx, first.Challenge, key.Assert(t, stepOpts.Options))
	if err != nil || done.Kind != client.StepSession {
		t.Fatalf("later step: %v %+v", err, done)
	}

	// A passkey as the proof, and removing it with that proof.
	re, err := auth.Passkeys.ReauthOptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Reauth(ctx, client.ReauthInput{Method: "passkey", Credential: key.Assert(t, re.Options)}); err != nil {
		t.Fatalf("reauth: %v", err)
	}
	if err := auth.Passkeys.Remove(ctx, made.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	var e *client.Error
	if _, err := other.Auth().Passkeys.SignInVerify(ctx, "", signIn.ChallengeID, key.Assert(t, signIn.Options)); !errors.As(err, &e) {
		t.Errorf("a used ceremony: %v", err)
	}
}
