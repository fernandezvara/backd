package registry

import (
	"errors"
	"fmt"
	"slices"
)

// parseSignIn reads what the realm says about signing in with external providers: auth.providers and
// auth.sign_in (where a sign-in may send the user back to). The providers of an organization's
// connections are not here: organizations.sso is read with the organizations (signin_org.go).
func parseSignIn(s *RealmSettings, auth *authDoc) []error {
	var errs []error
	if len(auth.Providers) == 0 && auth.SignIn == nil {
		return nil
	}
	if !s.AuthEnabled {
		return []error{errors.New("providers: only apply when auth is enabled")}
	}
	var es []error
	s.Providers, es = parseProviders(auth.Providers)
	errs = append(errs, es...)
	if si := auth.SignIn; si != nil {
		for i, o := range si.AllowedRedirects {
			norm, ok := normalizeRedirectOrigin(o)
			switch {
			case !ok:
				errs = append(errs, fmt.Errorf("auth.sign_in.allowed_redirects[%d]: %q must be an origin such as https://app.example.com (no path), or an app scheme such as acme://", i, o))
			case slices.Contains(s.SignInRedirects, norm):
				errs = append(errs, fmt.Errorf("auth.sign_in.allowed_redirects[%d]: %q is listed twice", i, o))
			default:
				s.SignInRedirects = append(s.SignInRedirects, norm)
			}
		}
		if si.AppURL != "" {
			s.SignInAppURL = si.AppURL
			if !s.SignInRedirectAllowed(si.AppURL) {
				errs = append(errs, fmt.Errorf("auth.sign_in.app_url: %q must be within auth.sign_in.allowed_redirects (or email.allowed_redirects when it has none)", si.AppURL))
			}
		}
	}
	if len(s.Providers) > 0 && len(s.SignInRedirects) == 0 && (s.Email == nil || len(s.Email.AllowedRedirects) == 0) {
		errs = append(errs, errors.New("providers: need auth.sign_in.allowed_redirects (or email.allowed_redirects): the origins and app schemes a sign-in may send the user back to"))
	}
	return errs
}
