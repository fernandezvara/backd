package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/adminclient"
	"github.com/fernandezvara/backd/pkg/client"
)

// The CLI manages realms through backd's HTTP API: `backd login` stores a
// session per server and realm, and `user`, `apikey` and later commands
// use it (or an admin API key from BACKD_API_KEY). Only local commands
// (serve, provision, template, databases, bootstrap) touch CONFIG_DIR or
// MongoDB.

// credentials is the CLI's credentials file: sessions per server URL and
// realm, and the server used last.
type credentials struct {
	DefaultURL string                           `json:"default_url,omitempty"`
	Sessions   map[string]map[string]storedAuth `json:"sessions"`
}

type storedAuth struct {
	Token     string `json:"token"`
	Email     string `json:"email"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// credentialsPath is BACKD_CREDENTIALS, or credentials under
// $XDG_CONFIG_HOME/backd or ~/.config/backd.
func credentialsPath(getenv func(string) string) (string, error) {
	if p := getenv("BACKD_CREDENTIALS"); p != "" {
		return p, nil
	}
	if dir := getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "backd", "credentials"), nil
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".config", "backd", "credentials"), nil
	}
	return "", errors.New("can't find where to keep credentials: set HOME, XDG_CONFIG_HOME or BACKD_CREDENTIALS")
}

func loadCredentials(getenv func(string) string) (*credentials, string, error) {
	path, err := credentialsPath(getenv)
	if err != nil {
		return nil, "", err
	}
	c := &credentials{Sessions: map[string]map[string]storedAuth{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, path, nil
	}
	if err != nil {
		return nil, "", err
	}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, "", fmt.Errorf("%s: %w", path, err)
	}
	if c.Sessions == nil {
		c.Sessions = map[string]map[string]storedAuth{}
	}
	return c, path, nil
}

// save writes the file readable only by its owner: it holds session tokens.
func (c *credentials) save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// target is a server and realm the CLI talks to, with a credential. Everything it asks of the
// server goes through the Go client (pkg/client) and, for the administration, internal/adminclient
// on top of it.
type target struct {
	url, realm string
	credential string // a session token or an API key; "" when none
	fromKey    bool   // credential is BACKD_API_KEY

	ctx context.Context
	cl  *client.Client // 60 seconds for a whole call
	adm *adminclient.Admin
	// long is the same without a time limit, for answers and uploads too big to hold (an export's
	// parts, an import's chunks).
	long    *client.Client
	admLong *adminclient.Admin
}

// resolveURL picks the server: --url, BACKD_URL, or the one used last.
func resolveURL(flagURL string, getenv func(string) string, creds *credentials) (string, error) {
	u := flagURL
	if u == "" {
		u = getenv("BACKD_URL")
	}
	if u == "" && creds != nil {
		u = creds.DefaultURL
	}
	if u == "" {
		return "", errors.New("no server given: pass --url https://…, set BACKD_URL, or log in once with `backd login --realm <realm> --url …`")
	}
	parsed, err := url.Parse(u)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("invalid server URL %q: want http(s)://host[:port]", u)
	}
	return strings.TrimRight(u, "/"), nil
}

// makeTarget builds the clients of a server and realm, signed in with a session token or an API key
// (neither when credential is empty).
func makeTarget(u, realm, credential string, fromKey bool, hc *http.Client) (*target, error) {
	t := &target{url: u, realm: realm, credential: credential, fromKey: fromKey, ctx: context.Background()}
	long := *hc
	long.Timeout = 0
	build := func(doer client.Doer) (*client.Client, *adminclient.Admin, error) {
		o := client.Options{URL: u, Realm: realm, Doer: doer, MapError: t.mapError}
		switch {
		case credential == "":
		case fromKey:
			o.APIKey = credential
		default:
			s := client.NewMemoryStorage()
			if err := s.Set(t.ctx, credential); err != nil {
				return nil, nil, err
			}
			o.Storage = s
		}
		c, err := client.New(o)
		if err != nil {
			return nil, nil, err
		}
		return c, adminclient.New(c), nil
	}
	var err error
	if t.cl, t.adm, err = build(hc); err != nil {
		return nil, err
	}
	if t.long, t.admLong, err = build(&long); err != nil {
		return nil, err
	}
	return t, nil
}

// newTarget resolves server, realm and credential for API commands: an
// admin API key from BACKD_API_KEY (unless sessionOnly), else the session
// stored by `backd login` for that server and realm.
func newTarget(flagURL, realm string, getenv func(string) string, sessionOnly bool) (*target, error) {
	creds, _, err := loadCredentials(getenv)
	if err != nil {
		return nil, err
	}
	u, err := resolveURL(flagURL, getenv, creds)
	if err != nil {
		return nil, err
	}
	hc, err := httpClient(getenv)
	if err != nil {
		return nil, err
	}
	if key := getenv("BACKD_API_KEY"); key != "" && !sessionOnly {
		return makeTarget(u, realm, key, true, hc)
	}
	if s, ok := creds.Sessions[u][realm]; ok {
		return makeTarget(u, realm, s.Token, false, hc)
	}
	return nil, fmt.Errorf("not logged in to realm %q on %s: run `backd login --realm %s --url %s` (or set BACKD_API_KEY to an admin API key)", realm, u, realm, u)
}

// targetOf resolves the server, realm and credential of an API command
// from its --realm and --url flags.
func targetOf(c *kli.CommandContext, sessionOnly bool) (*target, error) {
	return targetFor(c, str(c, "realm"), sessionOnly)
}

// targetFor is targetOf for a realm resolved apart from the --realm flag.
func targetFor(c *kli.CommandContext, realm string, sessionOnly bool) (*target, error) {
	return newTarget(str(c, "url"), realm, c.Getenv, sessionOnly)
}

// httpClient trusts the system's CAs, plus BACKD_CA_CERT (a PEM file) if
// set, for servers with a private CA such as the local stack.
func httpClient(getenv func(string) string) (*http.Client, error) {
	c := &http.Client{Timeout: 60 * time.Second}
	if path := getenv("BACKD_CA_CERT"); path != "" {
		pem, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("BACKD_CA_CERT: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("BACKD_CA_CERT: no certificate in %s", path)
		}
		c.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	}
	return c, nil
}

// apiError is an error answer from backd as the CLI shows it: its status, code and message, the
// details and the request id, with a hint when the CLI user can act on it. It wraps the client's
// error, so errors.Is(err, client.ErrNotFound) and errors.As(err, &*client.Error) work.
type apiError struct {
	Status    int
	Code      string
	Message   string
	Details   []struct{ Path, Reason string }
	RequestID string
	err       *client.Error
}

func (e *apiError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s: %s", e.Status, e.Code, e.Message)
	for _, d := range e.Details {
		fmt.Fprintf(&b, "; %s: %s", d.Path, d.Reason)
	}
	if e.RequestID != "" {
		fmt.Fprintf(&b, " (request %s)", e.RequestID)
	}
	return b.String()
}

func (e *apiError) Unwrap() error { return e.err }

var anonymousStatusCode = regexp.MustCompile(`^http_\d+$`)

// mapError is the client's MapError: it turns what the client returns into the errors the CLI
// shows, with the hints of explain.
func (t *target) mapError(err error) error {
	var ce *client.Error
	if !errors.As(err, &ce) {
		return err
	}
	if errors.Is(err, client.ErrNetwork) {
		cause := errors.Unwrap(ce)
		if cause == nil {
			cause = ce
		}
		return fmt.Errorf("can't reach %s: %w", t.url, cause)
	}
	if ce.Status == 0 {
		return err
	}
	e := &apiError{Status: ce.Status, Code: ce.Code, Message: ce.Message, RequestID: ce.RequestID, err: ce}
	if anonymousStatusCode.MatchString(ce.Code) {
		e.Code = "http_error" // the answer was not backd's
	}
	for _, d := range ce.Details {
		e.Details = append(e.Details, struct{ Path, Reason string }{d.Path, d.Reason})
	}
	return t.explain(ce.Path, e)
}

// proof asks the signed-in user for a fresh proof of identity, the strongest they have
// (their second factor's code, else the password, else a code sent by email), and returns the
// header that sensitive requests carry. Nothing is asked of an API key, or of a user the server
// asks nothing of.
func (t *target) proof(uio userIO) (map[string]string, error) {
	if t.fromKey {
		return nil, nil
	}
	me, err := t.cl.Auth().Me(t.ctx)
	if err != nil {
		return nil, err
	}
	if len(me.ReauthMethods) == 0 {
		return nil, nil
	}
	in := client.ReauthInput{Method: me.ReauthMethods[0]}
	switch in.Method {
	case "password":
		pw, err := readSecret(uio, false, "Password (to confirm it is you)")
		if err != nil {
			return nil, err
		}
		in.Password = pw
	case "email_code":
		if err := t.cl.Auth().RequestReauthCode(t.ctx); err != nil {
			return nil, err
		}
		fmt.Fprintln(uio.stderr, "a confirmation code was sent to your email address")
		code, err := readLine(uio, "Code from the email")
		if err != nil {
			return nil, err
		}
		in.Code = code
	default: // totp: the code of the authenticator app
		code, err := readLine(uio, "Code from your authenticator app (or a recovery code)")
		if err != nil {
			return nil, err
		}
		if strings.ContainsAny(code, "-") || len(strings.TrimSpace(code)) > 6 {
			in.Method = "recovery_code"
		}
		in.Code = code
	}
	p, err := t.cl.Auth().Reauth(t.ctx, in)
	if err != nil {
		return nil, err
	}
	return map[string]string{"X-Backd-Reauth": p.Token}, nil
}

// withProof turns the header a proof is carried in (or any headers) into request options.
func withProof(headers map[string]string) []client.RequestOption { return adminclient.Proof(headers) }

// withHeaders is withProof for headers that are not a proof (an export's lease).
var withHeaders = withProof

// readLine reads one line the user types (a code): from the terminal with a prompt, or the first
// line of standard input when it isn't one.
func readLine(uio userIO, prompt string) (string, error) {
	if f, ok := uio.stdin.(*os.File); ok && isTerminal(f) {
		fmt.Fprintf(uio.stderr, "%s: ", prompt)
	}
	line, err := bufio.NewReader(uio.stdin).ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", errors.New("no code given")
	}
	return strings.TrimSpace(line), nil
}

// explain adds a hint to errors a CLI user can act on.
func (t *target) explain(path string, e *apiError) error {
	admin := strings.HasPrefix(path, "_admin")
	switch {
	case e.Status == http.StatusUnauthorized && e.Code == "reauth_required":
		return fmt.Errorf("%w\nthis needs a fresh proof that it is you", e)
	case e.Status == http.StatusUnauthorized && !t.fromKey && t.credential != "":
		return fmt.Errorf("%w\nthe stored session isn't accepted (expired, revoked, or not allowed from this network): run `backd login --realm %s --url %s`", e, t.realm, t.url)
	case e.Status == http.StatusForbidden && admin:
		return fmt.Errorf("%w\nadmin commands need a user holding an admin role (admin: true in realm.yaml) or an admin API key", e)
	case e.Status == http.StatusNotFound && e.Message == "resource not found":
		if admin {
			return fmt.Errorf("%w\nthe realm may not exist or have auth disabled, or its admin API may not be reachable from this network", e)
		}
		return fmt.Errorf("%w\nthe realm may not exist or have auth disabled", e)
	}
	return e
}

// offlineTarget is the target of an in-process handler (the --direct tools): there is no network
// and no credential, only the handler the transport calls.
func offlineTarget(u, realm string, hc *http.Client) *target {
	t, err := makeTarget(u, realm, "", true, hc)
	if err != nil {
		panic(err) // the address is a constant
	}
	return t
}
