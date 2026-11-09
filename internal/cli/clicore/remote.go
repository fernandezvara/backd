package clicore

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/adminclient"
	"github.com/fernandezvara/backd/internal/cmdutil"
	"github.com/fernandezvara/backd/pkg/client"
)

// The CLI manages realms through backd's HTTP API: `backd login` stores a
// session per server and realm, and `user`, `apikey` and later commands
// use it (or an admin API key from BACKD_API_KEY). Only local commands
// (serve, provision, template, databases, bootstrap) touch CONFIG_DIR or
// MongoDB.

// credentials is the CLI's credentials file: sessions per server URL and
// realm, and the server used last.
type Credentials struct {
	DefaultURL string                           `json:"default_url,omitempty"`
	Sessions   map[string]map[string]StoredAuth `json:"sessions"`
}

type StoredAuth struct {
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
	if home := userHomeDir(getenv, runtime.GOOS); home != "" {
		return filepath.Join(home, ".config", "backd", "credentials"), nil
	}
	return "", fmt.Errorf("can't find where to keep credentials: set %s, XDG_CONFIG_HOME or BACKD_CREDENTIALS", homeVar(runtime.GOOS))
}

// userHomeDir is the user's home directory the way os.UserHomeDir finds it (USERPROFILE on
// Windows, home on Plan 9, HOME elsewhere), read through getenv so commands stay testable in
// process; "" when it isn't set.
func userHomeDir(getenv func(string) string, goos string) string {
	return getenv(homeVar(goos))
}

func homeVar(goos string) string {
	switch goos {
	case "windows":
		return "USERPROFILE"
	case "plan9":
		return "home"
	}
	return "HOME"
}

func LoadCredentials(getenv func(string) string) (*Credentials, string, error) {
	path, err := credentialsPath(getenv)
	if err != nil {
		return nil, "", err
	}
	c := &Credentials{Sessions: map[string]map[string]StoredAuth{}}
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
		c.Sessions = map[string]map[string]StoredAuth{}
	}
	return c, path, nil
}

// save writes the file readable only by its owner: it holds session tokens.
func (c *Credentials) Save(path string) error {
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
type Target struct {
	URL, Realm string
	credential string // a session token or an API key; "" when none
	fromKey    bool   // credential is BACKD_API_KEY

	Ctx    context.Context
	Client *client.Client // 60 seconds for a whole call
	Adm    *adminclient.Admin
	// long is the same without a time limit, for answers and uploads too big to hold (an export's
	// parts, an import's chunks).
	long    *client.Client
	AdmLong *adminclient.Admin
}

// resolveURL picks the server: --url, BACKD_URL, or the one used last.
func ResolveURL(flagURL string, getenv func(string) string, creds *Credentials) (string, error) {
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
func MakeTarget(u, realm, credential string, fromKey bool, hc *http.Client) (*Target, error) {
	t := &Target{URL: u, Realm: realm, credential: credential, fromKey: fromKey, Ctx: context.Background()}
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
			if err := s.Set(t.Ctx, credential); err != nil {
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
	if t.Client, t.Adm, err = build(hc); err != nil {
		return nil, err
	}
	if t.long, t.AdmLong, err = build(&long); err != nil {
		return nil, err
	}
	return t, nil
}

// newTarget resolves server, realm and credential for API commands: an
// admin API key from BACKD_API_KEY (unless sessionOnly), else the session
// stored by `backd login` for that server and realm.
func NewTarget(flagURL, realm string, getenv func(string) string, sessionOnly bool) (*Target, error) {
	creds, _, err := LoadCredentials(getenv)
	if err != nil {
		return nil, err
	}
	u, err := ResolveURL(flagURL, getenv, creds)
	if err != nil {
		return nil, err
	}
	hc, err := HTTPClient(getenv)
	if err != nil {
		return nil, err
	}
	if key := getenv("BACKD_API_KEY"); key != "" && !sessionOnly {
		return MakeTarget(u, realm, key, true, hc)
	}
	if s, ok := creds.Sessions[u][realm]; ok {
		return MakeTarget(u, realm, s.Token, false, hc)
	}
	return nil, fmt.Errorf("not logged in to realm %q on %s: run `backd login --realm %s --url %s` (or set BACKD_API_KEY to an admin API key)", realm, u, realm, u)
}

// targetOf resolves the server, realm and credential of an API command
// from its --realm and --url flags.
func TargetOf(c *kli.CommandContext, sessionOnly bool) (*Target, error) {
	return TargetFor(c, cmdutil.Str(c, "realm"), sessionOnly)
}

// targetFor is targetOf for a realm resolved apart from the --realm flag.
func TargetFor(c *kli.CommandContext, realm string, sessionOnly bool) (*Target, error) {
	return NewTarget(cmdutil.Str(c, "url"), realm, c.Getenv, sessionOnly)
}

// httpClient trusts the system's CAs, plus BACKD_CA_CERT (a PEM file) if
// set, for servers with a private CA such as the local stack.
func HTTPClient(getenv func(string) string) (*http.Client, error) {
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
type APIError struct {
	Status    int
	Code      string
	Message   string
	Details   []struct{ Path, Reason string }
	RequestID string
	err       *client.Error
}

func (e *APIError) Error() string {
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

func (e *APIError) Unwrap() error { return e.err }

var anonymousStatusCode = regexp.MustCompile(`^http_\d+$`)

// mapError is the client's MapError: it turns what the client returns into the errors the CLI
// shows, with the hints of explain.
func (t *Target) mapError(err error) error {
	var ce *client.Error
	if !errors.As(err, &ce) {
		return err
	}
	if errors.Is(err, client.ErrNetwork) {
		cause := errors.Unwrap(ce)
		if cause == nil {
			cause = ce
		}
		return fmt.Errorf("can't reach %s: %w", t.URL, cause)
	}
	if ce.Status == 0 {
		return err
	}
	e := &APIError{Status: ce.Status, Code: ce.Code, Message: ce.Message, RequestID: ce.RequestID, err: ce}
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
func (t *Target) Proof(uio cmdutil.IO) (map[string]string, error) {
	if t.fromKey {
		return nil, nil
	}
	me, err := t.Client.Auth().Me(t.Ctx)
	if err != nil {
		return nil, err
	}
	if len(me.ReauthMethods) == 0 {
		return nil, nil
	}
	in := client.ReauthInput{Method: me.ReauthMethods[0]}
	switch in.Method {
	case "password":
		pw, err := cmdutil.ReadSecret(uio, false, "Password (to confirm it is you)")
		if err != nil {
			return nil, err
		}
		in.Password = pw
	case "email_code":
		if err := t.Client.Auth().RequestReauthCode(t.Ctx); err != nil {
			return nil, err
		}
		fmt.Fprintln(uio.Stderr, "a confirmation code was sent to your email address")
		code, err := cmdutil.ReadLine(uio, "Code from the email")
		if err != nil {
			return nil, err
		}
		in.Code = code
	default: // totp: the code of the authenticator app
		code, err := cmdutil.ReadLine(uio, "Code from your authenticator app (or a recovery code)")
		if err != nil {
			return nil, err
		}
		if strings.ContainsAny(code, "-") || len(strings.TrimSpace(code)) > 6 {
			in.Method = "recovery_code"
		}
		in.Code = code
	}
	p, err := t.Client.Auth().Reauth(t.Ctx, in)
	if err != nil {
		return nil, err
	}
	return map[string]string{"X-Backd-Reauth": p.Token}, nil
}

// withProof turns the header a proof is carried in (or any headers) into request options.
func WithProof(headers map[string]string) []client.RequestOption { return adminclient.Proof(headers) }

// withHeaders is withProof for headers that are not a proof (an export's lease).
var WithHeaders = WithProof

// explain adds a hint to errors a CLI user can act on.
func (t *Target) explain(path string, e *APIError) error {
	admin := strings.HasPrefix(path, "_admin")
	switch {
	case e.Status == http.StatusUnauthorized && e.Code == "reauth_required":
		return fmt.Errorf("%w\nthis needs a fresh proof that it is you", e)
	case e.Status == http.StatusUnauthorized && !t.fromKey && t.credential != "":
		return fmt.Errorf("%w\nthe stored session isn't accepted (expired, revoked, or not allowed from this network): run `backd login --realm %s --url %s`", e, t.Realm, t.URL)
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

// offlineTarget is the target of an in-process handler (backd-server's export, import and field
// key commands): there is no network
// and no credential, only the handler the transport calls.
func OfflineTarget(u, realm string, hc *http.Client) *Target {
	t, err := MakeTarget(u, realm, "", true, hc)
	if err != nil {
		panic(err) // the address is a constant
	}
	return t
}
