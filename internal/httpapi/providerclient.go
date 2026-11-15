package httpapi

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/sso"
	"github.com/fernandezvara/backd/sso/apple"
	"github.com/fernandezvara/backd/sso/oauth2"
	"github.com/fernandezvara/backd/sso/oidc"
	"github.com/fernandezvara/backd/sso/safehttp"
)

// ProviderClient is what the sign-in handlers ask of identity providers: it turns a realm's
// provider into the library's and runs the OAuth 2 exchange, the ID-token checks and Apple's
// client secret through the packages of sso/. The zero value needs HTTP set.
type ProviderClient struct {
	// HTTP reaches the providers: safehttp.NewClient(…) in production.
	HTTP *http.Client
	// Endpoints gives a provider's URLs; the built-in ones when nil. Tests point it at a fake.
	Endpoints func(*registry.Provider) sso.Endpoints
	// Now is the clock; time.Now when nil.
	Now func() time.Time

	once   sync.Once
	oidc   *oidc.Verifier
	oauth2 *oauth2.Client
	apple  *apple.Signer
}

// newProviderClient is the client for the built-in providers: it dials only their hosts.
func newProviderClient(dev bool) *ProviderClient {
	return &ProviderClient{HTTP: safehttp.NewClient(safehttp.BuiltinHostAllowed, dev)}
}

func (c *ProviderClient) init() {
	c.once.Do(func() {
		c.oidc = &oidc.Verifier{HTTP: c.HTTP, Now: c.Now}
		c.oauth2 = &oauth2.Client{HTTP: c.HTTP}
		c.apple = &apple.Signer{Now: c.Now}
	})
}

func (c *ProviderClient) provider(p *registry.Provider) sso.Provider {
	sp := p.SSO()
	if c.Endpoints != nil {
		sp.Endpoints = c.Endpoints(p)
	}
	return sp
}

// AuthorizeURL is where to send the user to sign in with the provider.
func (c *ProviderClient) AuthorizeURL(p *registry.Provider, a oauth2.AuthorizeParams) string {
	c.init()
	return c.oauth2.AuthorizeURL(c.provider(p), a)
}

// Exchange trades an authorization code for the user's ID token.
func (c *ProviderClient) Exchange(ctx context.Context, p *registry.Provider, clientID, secret, code, redirectURI, codeVerifier string) (oauth2.Tokens, error) {
	c.init()
	return c.oauth2.Exchange(ctx, c.provider(p), clientID, secret, code, redirectURI, codeVerifier)
}

// Verify checks an ID token of the provider and returns what it says about the user.
func (c *ProviderClient) Verify(ctx context.Context, p *registry.Provider, idToken, nonce string, native bool) (oidc.Claims, error) {
	c.init()
	return c.oidc.Verify(ctx, c.provider(p), idToken, nonce, native)
}

// Subject verifies an ID token like Verify, except for the nonce, and returns who it is for.
func (c *ProviderClient) Subject(ctx context.Context, p *registry.Provider, idToken string, native bool) (subject, audience string, err error) {
	c.init()
	return c.oidc.Subject(ctx, c.provider(p), idToken, native)
}

// AppleClientSecret returns the client secret to send to Apple, signed with the private key.
func (c *ProviderClient) AppleClientSecret(p *registry.Provider, clientID, privateKeyPEM string) (string, error) {
	c.init()
	return c.apple.ClientSecret(c.provider(p), clientID, privateKeyPEM)
}

// Revoke tells Apple to revoke a refresh token.
func (c *ProviderClient) Revoke(ctx context.Context, p *registry.Provider, clientID, clientSecret, refreshToken string) error {
	c.init()
	return apple.Revoke(ctx, c.oauth2, c.provider(p), clientID, clientSecret, refreshToken)
}

// UsesPKCE says whether backd sends a PKCE challenge to the provider.
func UsesPKCE(p *registry.Provider) bool { return oauth2.UsesPKCE(p.SSO()) }
