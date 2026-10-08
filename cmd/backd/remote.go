package main

import (
	"bytes"
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
	"strings"
	"time"

	"github.com/fernandezvara/kli"
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

// target is a server and realm the CLI talks to, with a credential.
type target struct {
	url, realm string
	credential string // a session token or an API key; "" when none
	fromKey    bool   // credential is BACKD_API_KEY
	http       *http.Client
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
	t := &target{url: u, realm: realm, http: hc}
	if key := getenv("BACKD_API_KEY"); key != "" && !sessionOnly {
		t.credential, t.fromKey = key, true
		return t, nil
	}
	if s, ok := creds.Sessions[u][realm]; ok {
		t.credential = s.Token
		return t, nil
	}
	return nil, fmt.Errorf("not logged in to realm %q on %s: run `backd login --realm %s --url %s` (or set BACKD_API_KEY to an admin API key)", realm, u, realm, u)
}

// targetOf resolves the server, realm and credential of an API command
// from its --realm and --url flags.
func targetOf(c *kli.CommandContext, sessionOnly bool) (*target, error) {
	return newTarget(str(c, "url"), str(c, "realm"), c.Getenv, sessionOnly)
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

// apiError is an error answer from backd, in its envelope.
type apiError struct {
	Status    int
	Code      string
	Message   string
	Details   []struct{ Path, Reason string }
	RequestID string
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

// call sends a request to /v1/{realm}/{path} with the target's credential
// and decodes a JSON answer into out (when not nil).
func (t *target) call(method, path string, query url.Values, body, out any) error {
	_, err := t.callHeaders(method, path, query, body, out, nil)
	return err
}

// callHeaders is call, but also sends extra request headers and returns
// the response headers (for `backd functions invoke`'s dev diagnostics).
func (t *target) callHeaders(method, path string, query url.Values, body, out any, reqHeaders map[string]string) (http.Header, error) {
	u := t.url + "/v1/" + url.PathEscape(t.realm) + "/" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, u, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if t.credential != "" {
		req.Header.Set("Authorization", "Bearer "+t.credential)
	}
	for k, v := range reqHeaders {
		req.Header.Set(k, v)
	}
	res, err := t.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("can't reach %s: %w", t.url, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		var env struct {
			Error struct {
				Code      string `json:"code"`
				Message   string `json:"message"`
				Details   []struct{ Path, Reason string }
				RequestID string `json:"request_id"`
			} `json:"error"`
		}
		e := &apiError{Status: res.StatusCode, Code: "http_error", Message: http.StatusText(res.StatusCode)}
		if json.Unmarshal(data, &env) == nil && env.Error.Code != "" {
			e.Code, e.Message, e.RequestID = env.Error.Code, env.Error.Message, env.Error.RequestID
			e.Details = env.Error.Details
		}
		return res.Header, t.explain(path, e)
	}
	if out != nil && len(data) > 0 {
		return res.Header, json.Unmarshal(data, out)
	}
	return res.Header, nil
}

// explain adds a hint to errors a CLI user can act on.
func (t *target) explain(path string, e *apiError) error {
	admin := strings.HasPrefix(path, "_admin")
	switch {
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
