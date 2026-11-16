// Package oauth2 runs the OAuth 2 authorization-code flow against a provider: the authorize
// URL and the code exchange. It knows nothing of users or sessions.
package oauth2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/fernandezvara/backd/sso"
	"github.com/fernandezvara/backd/sso/safehttp"
)

// Client talks to a provider's authorize and token endpoints.
type Client struct {
	// HTTP reaches the providers: safehttp.NewClient(…) in production.
	HTTP *http.Client
}

// AuthorizeParams are what an authorization request carries.
type AuthorizeParams struct {
	RedirectURI   string
	State         string
	Nonce         string
	CodeChallenge string // PKCE (S256) between backd and the provider; empty: none
}

// UsesPKCE says whether backd sends a PKCE challenge to the provider (Apple's web flow
// does not take one).
func UsesPKCE(p sso.Provider) bool { return p.Kind != sso.Apple && !p.NoPKCE }

// hasIDToken says whether the provider's code exchange gives an ID token; GitHub and plain OAuth 2
// services give an access token only, which is used to read the person.
func hasIDToken(p sso.Provider) bool { return p.Kind != sso.GitHub && p.Kind != sso.OAuth2 }

// AuthorizeURL is where to send the user to sign in with the provider.
func (s *Client) AuthorizeURL(p sso.Provider, a AuthorizeParams) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", a.RedirectURI)
	q.Set("state", a.State)
	if hasIDToken(p) {
		q.Set("nonce", a.Nonce)
	}
	switch p.Kind {
	case sso.Apple:
		q.Set("scope", "name email")
		q.Set("response_mode", "form_post")
	case sso.GitHub:
		q.Set("scope", "read:user user:email")
	case sso.OAuth2:
		if len(p.Scopes) > 0 {
			q.Set("scope", strings.Join(p.Scopes, " "))
		}
	default:
		q.Set("scope", strings.Join(append([]string{"openid", "email", "profile"}, p.Scopes...), " "))
	}
	if a.CodeChallenge != "" && UsesPKCE(p) {
		q.Set("code_challenge", a.CodeChallenge)
		q.Set("code_challenge_method", "S256")
	}
	return p.Endpoints.Authorize + "?" + q.Encode()
}

// Tokens are what the code exchange gives back that backd keeps using: the ID token
// and, from Apple, the refresh token (kept only to revoke it).
type Tokens struct {
	IDToken      string
	AccessToken  string
	RefreshToken string
}

// Exchange trades an authorization code for the user's ID token. clientID is the
// application the code was issued to (the provider's web client, or for a native
// app its own id), and secret the client secret of Google or Microsoft, or Apple's
// already-signed one.
func (s *Client) Exchange(ctx context.Context, p sso.Provider, clientID, secret, code, redirectURI, codeVerifier string) (Tokens, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", clientID)
	form.Set("client_secret", secret)
	if redirectURI != "" {
		form.Set("redirect_uri", redirectURI)
	}
	if codeVerifier != "" && UsesPKCE(p) {
		form.Set("code_verifier", codeVerifier)
	}
	var out struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
	}
	status, err := s.PostForm(ctx, p.Endpoints.Token, form, &out)
	switch {
	case err != nil:
		return Tokens{}, err
	case status == http.StatusBadRequest || status == http.StatusUnauthorized:
		return Tokens{}, fmt.Errorf("%w (%s)", sso.ErrExchange, Sanitize(out.Error))
	case status == http.StatusOK && out.Error != "":
		// GitHub answers a refused code with 200 and an error.
		return Tokens{}, fmt.Errorf("%w (%s)", sso.ErrExchange, Sanitize(out.Error))
	case status != http.StatusOK || (hasIDToken(p) && out.IDToken == "") || (!hasIDToken(p) && out.AccessToken == ""):
		return Tokens{}, fmt.Errorf("%w: status %d", sso.ErrUnavailable, status)
	}
	return Tokens{IDToken: out.IDToken, AccessToken: out.AccessToken, RefreshToken: out.RefreshToken}, nil
}

// Sanitize keeps a provider's error code short and plain before it is logged.
func Sanitize(s string) string {
	if len(s) > 64 || strings.ContainsFunc(s, func(r rune) bool { return r < ' ' || r > '~' }) {
		return "?"
	}
	return s
}

func (s *Client) PostForm(ctx context.Context, endpoint string, form url.Values, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", sso.ErrUnavailable, err)
	}
	defer resp.Body.Close()
	data, err := safehttp.ReadBody(resp)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", sso.ErrUnavailable, err)
	}
	if len(data) > 0 && out != nil {
		if err := json.Unmarshal(data, out); err != nil && resp.StatusCode == http.StatusOK {
			return 0, fmt.Errorf("%w: unreadable answer", sso.ErrUnavailable)
		}
	}
	return resp.StatusCode, nil
}
