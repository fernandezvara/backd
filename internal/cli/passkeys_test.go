package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/auth/passkey/passkeytest"
)

const cliPasskeyOrigin = "https://app.example.com"

// cliRealmPasskeys has a strategy that starts with a passkey, one that ends with it, and the
// password alone.
const cliRealmPasskeys = `auth:
  strategies:
    password: [password]
    passkey: [passkey]
    password_passkey: [password, passkey]
  sign_in:
    app_url: https://app.example.com/
    allowed_redirects: [https://app.example.com]
  passkeys:
    enabled: true
roles:
  ops:
    admin: true
`

func TestLoginRefusesAPasskeyInTheTerminal(t *testing.T) {
	f := newCLIFixture(t, cliRealmPasskeys)
	ctx := context.Background()
	pw := cliPassword
	if _, err := f.svc.Create(ctx, "ada@example.com", &pw); err != nil {
		t.Fatal(err)
	}
	args := []string{"login", "--realm", "acme", "--url", f.url, "--email", "ada@example.com"}

	// A strategy that starts with a passkey: refused before the password is asked.
	code, out, errOut := f.run(strings.NewReader(""), append(args, "--strategy", "passkey")...)
	if code == 0 || !strings.Contains(errOut, "this method needs a browser or an app") || strings.Contains(out, "logged in") {
		t.Errorf("a passkey first: %d %q %q", code, out, errOut)
	}
	// A strategy with a passkey after the password: the person has none yet, so the sign-in would add one.
	code, out, errOut = f.run(strings.NewReader(cliPassword+"\n"), append(args, "--strategy", "password_passkey")...)
	if code == 0 || !strings.Contains(errOut, "this method needs a browser or an app") || strings.Contains(out, "logged in") {
		t.Errorf("a passkey later: %d %q %q", code, out, errOut)
	}
	// And with one, the step asks for it.
	u, _ := f.svc.Find(ctx, "ada@example.com")
	addCLIPasskey(t, f, u.ID)
	code, out, errOut = f.run(strings.NewReader(cliPassword+"\n"), append(args, "--strategy", "password_passkey")...)
	if code == 0 || !strings.Contains(errOut, "this method needs a browser or an app") || strings.Contains(out, "logged in") {
		t.Errorf("a passkey step: %d %q %q", code, out, errOut)
	}
	// The password alone still works.
	if code, out, errOut := f.run(strings.NewReader(cliPassword+"\n"), append(args, "--strategy", "password")...); code != 0 || !strings.Contains(out, "logged in") {
		t.Errorf("password: %d %q %q", code, out, errOut)
	}
}

func TestAccountPasskeysListAndRemove(t *testing.T) {
	f := newCLIFixture(t, cliRealmPasskeys)
	ctx := context.Background()
	secret, _ := f.userWithTOTP("ada@example.com")
	if code, out, errOut := f.run(strings.NewReader(cliPassword+"\n"), "login", "--realm", "acme", "--url", f.url, "--email", "ada@example.com", "--strategy", "password"); code != 0 {
		t.Fatalf("login: %d %q %q", code, out, errOut)
	}
	base := []string{"--realm", "acme", "--url", f.url}
	code, out, errOut := f.run(nil, append([]string{"account", "passkeys-list"}, base...)...)
	if code != 0 || !strings.Contains(out, "no passkeys") {
		t.Fatalf("empty list: %d %q %q", code, out, errOut)
	}
	u, _ := f.svc.Find(ctx, "ada@example.com")
	id := addCLIPasskey(t, f, u.ID)
	code, out, _ = f.run(nil, append([]string{"account", "passkeys-list"}, base...)...)
	if code != 0 || !strings.Contains(out, id) || !strings.Contains(out, "Laptop") {
		t.Fatalf("list: %d %q", code, out)
	}
	// Removing asks for a fresh proof: the authenticator code (a passkey cannot be given in the terminal).
	code, out, errOut = f.run(strings.NewReader(f.code(secret)+"\n"), append([]string{"account", "passkeys-remove", "--id", id}, base...)...)
	if code != 0 || !strings.Contains(out, "the passkey is removed") {
		t.Fatalf("remove: %d %q %q", code, out, errOut)
	}
	if code, out, _ := f.run(nil, append([]string{"account", "passkeys-list"}, base...)...); code != 0 || !strings.Contains(out, "no passkeys") {
		t.Errorf("list after: %d %q", code, out)
	}
}

func TestAccountPasskeysRemoveNeedsAnotherProofThanAPasskey(t *testing.T) {
	f := newCLIFixture(t, cliRealmPasskeys)
	ctx := context.Background()
	pw := cliPassword
	u, err := f.svc.Create(ctx, "bob@example.com", &pw)
	if err != nil {
		t.Fatal(err)
	}
	addCLIPasskey(t, f, u.ID)
	if code, out, errOut := f.run(strings.NewReader(cliPassword+"\n"), "login", "--realm", "acme", "--url", f.url, "--email", "bob@example.com", "--strategy", "password"); code != 0 {
		t.Fatalf("login: %d %q %q", code, out, errOut)
	}
	// A passkey is the only proof this person gives, and the terminal cannot give it.
	code, _, errOut := f.run(nil, "account", "passkeys-remove", "--id", "x", "--realm", "acme", "--url", f.url)
	if code == 0 || !strings.Contains(errOut, "needs a browser or an app") {
		t.Errorf("remove: %d %q", code, errOut)
	}
}

// addCLIPasskey registers a software authenticator for the user, and returns the passkey's id.
func addCLIPasskey(t *testing.T, f *cliFixture, userID string) string {
	t.Helper()
	ctx := context.Background()
	u, err := f.store.UserByID(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	opts, ceremony, err := f.svc.BeginPasskeyRegistration(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	a := passkeytest.New(cliPasskeyOrigin)
	info, err := f.svc.FinishPasskeyRegistration(ctx, u, ceremony, a.Register(t, opts), "Laptop")
	if err != nil {
		t.Fatal(err)
	}
	return info.ID
}
