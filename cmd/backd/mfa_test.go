package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/pquerna/otp/totp"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/httpapi"
	"github.com/fernandezvara/backd/internal/registry"
)

// lockedBuffer is an output two goroutines may use.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// script is a standard input that answers each read with the next step: a fixed line, or
// one made from what the command has printed so far (the secret it showed).
type script struct {
	steps []func() string
	next  int
}

func (s *script) Read(p []byte) (int, error) {
	if s.next >= len(s.steps) {
		return 0, io.EOF
	}
	out := s.steps[s.next]()
	s.next++
	return copy(p, out), nil
}

// cliAgainst serves one realm "acme" in memory and returns a way to run the CLI against it.
type cliFixture struct {
	t     *testing.T
	svc   *auth.Users
	store *authtest.MemStore
	url   string
	creds string
}

func newCLIFixture(t *testing.T, realmYAML string) *cliFixture {
	t.Helper()
	root := writeConfig(t, map[string]string{"acme/realm.yaml": realmYAML, "acme/app/notes/schema.json": `{}`})
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	store := authtest.NewMemStore()
	svc := &auth.Users{
		Store: store, Hasher: auth.NewHasher(2, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}),
		Settings: reg.Realms["acme"].Settings, Realm: "acme",
	}
	svc.Cipher, _ = auth.NewSecretCipher([]byte("01234567890123456789012345678901"))
	srv := httptest.NewServer(httpapi.NewHandler(httpapi.Config{
		Log: slog.New(slog.NewJSONHandler(io.Discard, nil)), Registry: reg, Ready: func(context.Context) error { return nil },
		Users: func(realm string) *auth.Users {
			if realm == "acme" {
				return svc
			}
			return nil
		},
	}))
	t.Cleanup(srv.Close)
	return &cliFixture{t: t, svc: svc, store: store, url: srv.URL, creds: filepath.Join(t.TempDir(), "backd", "credentials")}
}

// run runs the CLI with the fixture's credentials file and returns its exit code, stdout and stderr.
func (f *cliFixture) run(stdin io.Reader, args ...string) (int, string, string) {
	var stdout, stderr lockedBuffer
	code := run(args, func(k string) string {
		if k == "BACKD_CREDENTIALS" {
			return f.creds
		}
		return ""
	}, stdin, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

const cliPassword = "dev-p4ssw0rd!"

// user makes a user and gives them a second factor; it returns the secret.
func (f *cliFixture) userWithTOTP(address string) (secret string, recovery []string) {
	f.t.Helper()
	ctx := context.Background()
	pw := cliPassword
	u, err := f.svc.Create(ctx, address, &pw)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.svc.AddRole(ctx, address, "ops"); err != nil {
		f.t.Fatal(err)
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "acme", AccountName: address})
	if err != nil {
		f.t.Fatal(err)
	}
	sealed, _ := f.svc.SealToken(key.Secret())
	// The recovery codes the way enrolling makes them: keyed hashes of the typed form.
	recovery = []string{"AAAAA-BBBBB"}
	hashes := []string{f.svc.Cipher.MAC("recovery", "acme", u.ID, "AAAAABBBBB")}
	if err := f.store.PutMFA(ctx, auth.MFARecord{UserID: u.ID, Secret: sealed, EnabledAt: f.svc.Clock(), RecoveryCodes: hashes}); err != nil {
		f.t.Fatal(err)
	}
	return key.Secret(), recovery
}

func (f *cliFixture) code(secret string) string {
	c, err := totp.GenerateCode(secret, f.svc.Clock())
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

const cliRealm = "roles:\n  ops:\n    admin: true\n"

func TestLoginAsksForTheSecondFactor(t *testing.T) {
	f := newCLIFixture(t, cliRealm)
	secret, recovery := f.userWithTOTP("ada@example.com")
	args := []string{"login", "--realm", "acme", "--url", f.url, "--email", "ada@example.com"}

	// The password and then the code, as the two lines of standard input.
	code, out, errOut := f.run(strings.NewReader(cliPassword+"\n"+f.code(secret)+"\n"), args...)
	if code != 0 || !strings.Contains(out, "logged in to realm acme") {
		t.Fatalf("with a code: %d %q %q", code, out, errOut)
	}
	if code, out, _ := f.run(nil, "whoami", "--realm", "acme", "--url", f.url); code != 0 || !strings.Contains(out, "ada@example.com") {
		t.Errorf("whoami after: %d %q", code, out)
	}
	// A recovery code works, and says how many are left.
	code, out, errOut = f.run(strings.NewReader(cliPassword+"\n"+recovery[0]+"\n"), args...)
	if code != 0 || !strings.Contains(errOut, "a recovery code was used; 0 left") {
		t.Errorf("with a recovery code: %d %q %q", code, out, errOut)
	}
	// A wrong code stores no session.
	_ = os.Remove(f.creds)
	code, out, errOut = f.run(strings.NewReader(cliPassword+"\n000000\n"), args...)
	if code == 0 || !strings.Contains(errOut, "invalid_code") {
		t.Errorf("a wrong code: %d %q %q", code, out, errOut)
	}
	if _, err := os.Stat(f.creds); err == nil {
		t.Error("a session was stored for a wrong code")
	}
	// No code on standard input is an error, not a hang.
	if code, _, errOut := f.run(strings.NewReader(cliPassword+"\n"), args...); code == 0 || !strings.Contains(errOut, "no code given") {
		t.Errorf("no code: %d %q", code, errOut)
	}
}

func TestLoginEnrollsWhenARoleRequiresIt(t *testing.T) {
	f := newCLIFixture(t, "mfa:\n  required_roles: [ops]\n"+cliRealm)
	ctx := context.Background()
	pw := cliPassword
	if _, err := f.svc.Create(ctx, "boss@example.com", &pw); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.AddRole(ctx, "boss@example.com", "ops"); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr lockedBuffer
	secretRe := regexp.MustCompile(`secret:\s+([A-Z2-7]+)`)
	in := &script{steps: []func() string{
		func() string { return cliPassword + "\n" },
		func() string { // the code of the secret the command showed
			m := secretRe.FindStringSubmatch(stderr.String())
			if m == nil {
				t.Errorf("no secret in %q", stderr.String())
				return "000000\n"
			}
			return f.code(m[1]) + "\n"
		},
	}}
	code := run([]string{"login", "--realm", "acme", "--url", f.url, "--email", "boss@example.com"}, func(k string) string {
		if k == "BACKD_CREDENTIALS" {
			return f.creds
		}
		return ""
	}, in, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "logged in to realm acme") {
		t.Fatalf("login: %d %q %q", code, stdout.String(), stderr.String())
	}
	// The QR code drawn with blocks, the address, and ten recovery codes shown once.
	for _, want := range []string{"must use two-step verification", "otpauth://totp/", "█", "Recovery codes"} {
		if !strings.Contains(stdout.String()+stderr.String(), want) {
			t.Errorf("output lacks %q:\n%s%s", want, stdout.String(), stderr.String())
		}
	}
	if n := len(regexp.MustCompile(`(?m)^  [A-Z2-9]{5}-[A-Z2-9]{5}$`).FindAllString(stdout.String(), -1)); n != 10 {
		t.Errorf("recovery codes shown: %d", n)
	}
	// The user has the factor now, and the next login asks for it.
	u, _ := f.svc.Find(ctx, "boss@example.com")
	if _, err := f.store.MFAOf(ctx, u.ID); err != nil {
		t.Errorf("not enrolled: %v", err)
	}
}

func TestLoginWithAMagicLinkCode(t *testing.T) {
	f := newCLIFixture(t, cliRealm)
	// (The realm has no delivery function: the email job only has to exist, and the code is made
	// as the worker would make it.)
	f.svc.Settings.MagicLink = registry.MagicLinkSettings{Enabled: true}
	f.svc.Settings.Email = &registry.EmailSettings{Function: "app/deliver", From: "a@example.com", DefaultLocale: "en", Limits: registry.EmailLimits{PerKindPerHour: 100, PerDay: 100, PerIPPerHour: 100}}
	ctx := context.Background()
	if _, err := f.svc.Create(ctx, "ada@example.com", nil); err != nil {
		t.Fatal(err)
	}
	code := func() string {
		jobs, _, _ := f.svc.Jobs(ctx, auth.JobFilter{Limit: 5})
		for _, j := range jobs {
			if j.Email != nil && j.Email.Kind == "magic-link" {
				_, c, _, err := f.svc.MakeMagicLinkSecrets(ctx, j.Email.ChallengeID)
				if err != nil {
					t.Fatal(err)
				}
				return c + "\n"
			}
		}
		t.Error("no magic-link job")
		return "000000\n"
	}
	in := &script{steps: []func() string{code}}
	c, out, errOut := f.run(in, "login", "--magic-link", "--realm", "acme", "--url", f.url, "--email", "ada@example.com")
	if c != 0 || !strings.Contains(out, "logged in to realm acme") || !strings.Contains(errOut, "a message with a 6-digit code is on its way") {
		t.Errorf("magic link: %d %q %q", c, out, errOut)
	}
}

func TestAccountSecondFactorCommands(t *testing.T) {
	f := newCLIFixture(t, cliRealm)
	ctx := context.Background()
	pw := cliPassword
	if _, err := f.svc.Create(ctx, "ada@example.com", &pw); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := f.run(strings.NewReader(cliPassword+"\n"), "login", "--realm", "acme", "--url", f.url, "--email", "ada@example.com"); code != 0 {
		t.Fatal(errOut)
	}
	if _, out, _ := f.run(nil, "account", "mfa-status", "--realm", "acme"); !strings.Contains(out, "two-step verification: off") {
		t.Errorf("status: %q", out)
	}
	// Enabling asks for the password (the proof), shows the setup and takes the first code.
	var stdout, stderr lockedBuffer
	secretRe := regexp.MustCompile(`secret:\s+([A-Z2-7]+)`)
	in := &script{steps: []func() string{
		func() string { return cliPassword + "\n" },
		func() string {
			m := secretRe.FindStringSubmatch(stderr.String())
			if m == nil {
				return "000000\n"
			}
			return f.code(m[1]) + "\n"
		},
	}}
	code := run([]string{"account", "mfa-enable", "--realm", "acme"}, func(k string) string {
		if k == "BACKD_CREDENTIALS" {
			return f.creds
		}
		return ""
	}, in, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "Recovery codes") || !strings.Contains(stderr.String(), "two-step verification is on") {
		t.Fatalf("enable: %d %q %q", code, stdout.String(), stderr.String())
	}
	if _, out, _ := f.run(nil, "account", "mfa-status", "--realm", "acme"); !strings.Contains(out, "two-step verification: on (totp, recovery_code); recovery codes left: 10") {
		t.Errorf("status after: %q", out)
	}
	secret := secretRe.FindStringSubmatch(stderr.String())[1]
	// The proof is a code now; replacing the recovery codes needs the next time step's.
	next, _ := totp.GenerateCode(secret, f.svc.Clock().Add(30e9))
	code2, out, errOut := f.run(strings.NewReader(next+"\n"), "account", "recovery-codes", "--realm", "acme")
	if code2 != 0 || strings.Count(out, "-") < 10 {
		t.Errorf("recovery codes: %d %q %q", code2, out, errOut)
	}
	// Turning it off asks for confirmation on the command line, and a proof: a recovery code does.
	newCodes := regexp.MustCompile(`(?m)^  ([A-Z2-9]{5}-[A-Z2-9]{5})$`).FindAllStringSubmatch(out, -1)
	if len(newCodes) != 10 {
		t.Fatalf("recovery codes: %q", out)
	}
	if c, _, e := f.run(strings.NewReader(newCodes[0][1]+"\n"), "account", "mfa-disable", "--realm", "acme"); c == 0 || !strings.Contains(e, "add --yes") {
		t.Errorf("disable without --yes: %d %q", c, e)
	}
	if c, out, e := f.run(strings.NewReader(newCodes[0][1]+"\n"), "account", "mfa-disable", "--yes", "--realm", "acme"); c != 0 || !strings.Contains(out, "two-step verification is off") {
		t.Errorf("disable: %d %q %q", c, out, e)
	}
	if _, out, _ := f.run(nil, "account", "mfa-status", "--realm", "acme"); !strings.Contains(out, "two-step verification: off") {
		t.Errorf("status at the end: %q", out)
	}
}

func TestQRBlocks(t *testing.T) {
	art, err := qrBlocks("otpauth://totp/acme:ada@example.com?secret=JBSWY3DPEHPK3PXP&issuer=acme")
	if err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(art, "\n")
	if len(rows) < 10 {
		t.Fatalf("rows: %d", len(rows))
	}
	width := len([]rune(rows[0]))
	for i, r := range rows {
		if len([]rune(r)) != width {
			t.Errorf("row %d has %d characters, want %d", i, len([]rune(r)), width)
		}
	}
	if !strings.ContainsAny(art, "█▀▄") || !strings.Contains(art, " ") {
		t.Error("the code has no blocks or no blanks")
	}
}
