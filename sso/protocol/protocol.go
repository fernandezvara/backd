// Package protocol implements sso.Protocol over the oidc, oauth2 and apple packages: it is what
// the engine uses to speak to the real providers.
package protocol

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/fernandezvara/backd/sso"
	"github.com/fernandezvara/backd/sso/apple"
	"github.com/fernandezvara/backd/sso/oauth2"
	"github.com/fernandezvara/backd/sso/oidc"
	"github.com/fernandezvara/backd/sso/safehttp"
)

// Client speaks to identity providers. The zero value needs HTTP set.
type Client struct {
	// HTTP reaches the providers: safehttp.NewClient(…) in production.
	HTTP *http.Client
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// Dev allows http issuers (BACKD_DEV).
	Dev bool

	oidc      *oidc.Verifier
	oauth2    *oauth2.Client
	apple     *apple.Signer
	discovery *oidc.Discoverer
}

// New is the client for the built-in providers: it dials only their hosts (private addresses
// too when dev).
func New(dev bool) *Client {
	return (&Client{HTTP: safehttp.NewClient(safehttp.BuiltinHostAllowed, dev), Dev: dev}).Init()
}

// Init prepares a Client built from a struct literal and returns it.
func (c *Client) Init() *Client {
	c.oidc = &oidc.Verifier{HTTP: c.HTTP, Now: c.Now}
	c.oauth2 = &oauth2.Client{HTTP: c.HTTP}
	c.apple = &apple.Signer{Now: c.Now}
	c.discovery = &oidc.Discoverer{HTTP: c.HTTP, Now: c.Now, Dev: c.Dev}
	return c
}

var _ sso.Protocol = (*Client)(nil)

// reach lets a request reach the provider's own hosts, and its network when the operator said so.
func reach(ctx context.Context, p sso.Provider) context.Context {
	return safehttp.ForProvider(ctx, p.AllowPrivate, p.Hosts...)
}

// Prepare reads a generic OpenID Connect provider's endpoints from its issuer's discovery
// document (cached); the other kinds know theirs.
func (c *Client) Prepare(ctx context.Context, p sso.Provider) (sso.Provider, error) {
	if p.Kind != sso.OIDC {
		return p, nil
	}
	doc, err := c.discovery.Discover(ctx, p.Issuer, p.AllowPrivate)
	if err != nil {
		return p, err
	}
	p.Endpoints = sso.Endpoints{Authorize: doc.Authorization, Token: doc.Token, JWKS: doc.JWKS}
	p.Hosts = doc.Hosts
	return p, nil
}

// UsesPKCE says whether a PKCE challenge goes to the provider.
func (c *Client) UsesPKCE(p sso.Provider) bool { return oauth2.UsesPKCE(p) }

// AuthorizeURL is where to send the person to sign in with the provider.
func (c *Client) AuthorizeURL(p sso.Provider, a sso.AuthorizeParams) string {
	return c.oauth2.AuthorizeURL(p, oauth2.AuthorizeParams{RedirectURI: a.RedirectURI, State: a.State, Nonce: a.Nonce, CodeChallenge: a.CodeChallenge})
}

// secret is the client secret to send: the configured one, or for Apple the signed JWT.
func (c *Client) secret(p sso.Provider, clientID string) (string, error) {
	if p.Kind == sso.Apple {
		return c.apple.ClientSecret(p, clientID, p.Secret)
	}
	return p.Secret, nil
}

// Exchange trades the authorization code for the person's tokens.
func (c *Client) Exchange(ctx context.Context, p sso.Provider, code, redirectURI, verifier string) (sso.Tokens, error) {
	secret, err := c.secret(p, p.ClientID)
	if err != nil {
		return sso.Tokens{}, err
	}
	t, err := c.oauth2.Exchange(reach(ctx, p), p, p.ClientID, secret, code, redirectURI, verifier)
	return sso.Tokens{IDToken: t.IDToken, AccessToken: t.AccessToken, RefreshToken: t.RefreshToken}, err
}

// Identify says who the tokens of a code exchange are for: an OpenID Connect provider's ID token is
// verified, and the person of an OAuth 2 provider (GitHub, a plain service) is read with the access
// token, once.
func (c *Client) Identify(ctx context.Context, p sso.Provider, t sso.Tokens, nonce string) (sso.Claims, error) {
	if p.Kind == sso.GitHub || p.Kind == sso.OAuth2 {
		return c.oauth2.Who(reach(ctx, p), p, t.AccessToken)
	}
	return c.Verify(ctx, p, t.IDToken, nonce, false)
}

// Verify checks an ID token and returns what it says.
func (c *Client) Verify(ctx context.Context, p sso.Provider, idToken, nonce string, native bool) (sso.Claims, error) {
	cl, err := c.oidc.Verify(reach(ctx, p), p, idToken, nonce, native)
	return sso.Claims{
		Issuer: cl.Issuer, Subject: cl.Subject, Email: cl.Email, EmailVerified: cl.EmailVerified, Name: cl.Name, GivenName: cl.GivenName,
		FamilyName: cl.FamilyName, Picture: cl.Picture, Audience: cl.Audience, HostedDomain: cl.HostedDomain,
		Tenant: cl.Tenant, Username: cl.Username,
	}, err
}

// AppleRefresh trades the authorization code a native app sends with its ID token for Apple's
// refresh token. The code must belong to the same person and app as the token, or a client could
// make backd keep (and later revoke) someone else's token.
func (c *Client) AppleRefresh(ctx context.Context, p sso.Provider, claims sso.Claims, code string) (string, error) {
	secret, err := c.secret(p, claims.Audience)
	if err != nil {
		return "", err
	}
	tokens, err := c.oauth2.Exchange(ctx, p, claims.Audience, secret, code, "", "")
	if err != nil {
		return "", err
	}
	subject, audience, err := c.oidc.Subject(ctx, p, tokens.IDToken, true)
	if err != nil {
		return "", err
	}
	if subject != claims.Subject || audience != claims.Audience {
		return "", sso.ErrInvalidToken
	}
	return tokens.RefreshToken, nil
}

// FirstAuthorization reads the "user" field Apple posts to the callback the first time a person
// authorizes the app: {"name": {"firstName": …, "lastName": …}}. No other provider sends such a
// field, and one sent to them is ignored.
func (c *Client) FirstAuthorization(p sso.Provider, raw string) map[string]string {
	if p.Kind != sso.Apple || raw == "" || len(raw) > 4096 {
		return nil
	}
	var u struct {
		Name struct {
			First string `json:"firstName"`
			Last  string `json:"lastName"`
		} `json:"name"`
	}
	if json.Unmarshal([]byte(raw), &u) != nil {
		return nil
	}
	out := map[string]string{"given_name": u.Name.First, "family_name": u.Name.Last}
	if full := strings.TrimSpace(u.Name.First + " " + u.Name.Last); full != "" {
		out["name"] = full
	}
	return out
}

// Revoke tells Apple to revoke a refresh token.
func (c *Client) Revoke(ctx context.Context, p sso.Provider, clientID, refreshToken string) error {
	secret, err := c.secret(p, clientID)
	if err != nil {
		return err
	}
	return apple.Revoke(ctx, c.oauth2, p, clientID, secret, refreshToken)
}
