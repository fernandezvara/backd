package oauth

import (
	"net/url"

	"github.com/fernandezvara/backd/internal/registry"
)

// Endpoints are the URLs of a provider.
type Endpoints struct {
	Authorize, Token, JWKS, Revoke string
}

// BuiltinEndpoints are the endpoints of the built-in providers.
func BuiltinEndpoints(p *registry.Provider) Endpoints {
	switch p.Name {
	case registry.ProviderGoogle:
		return Endpoints{
			Authorize: "https://accounts.google.com/o/oauth2/v2/auth",
			Token:     "https://oauth2.googleapis.com/token",
			JWKS:      "https://www.googleapis.com/oauth2/v3/certs",
		}
	case registry.ProviderMicrosoft:
		base := "https://login.microsoftonline.com/" + url.PathEscape(p.Tenant)
		return Endpoints{Authorize: base + "/oauth2/v2.0/authorize", Token: base + "/oauth2/v2.0/token", JWKS: base + "/discovery/v2.0/keys"}
	case registry.ProviderApple:
		return Endpoints{
			Authorize: "https://appleid.apple.com/auth/authorize",
			Token:     "https://appleid.apple.com/auth/token",
			JWKS:      "https://appleid.apple.com/auth/keys",
			Revoke:    "https://appleid.apple.com/auth/revoke",
		}
	}
	return Endpoints{}
}
