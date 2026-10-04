package httpapi

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
)

// Session cookies let a browser app keep its session out of JavaScript's
// reach: an HttpOnly cookie that the browser sends on its own. A realm turns
// them on (sessions.cookie.enabled); a login or sign-up then asks for one with
// "cookie": true, and gets the cookie and a body without the token. Any other
// login in the same realm still gets a token, so the command line and servers
// are unaffected.
//
// Because the browser sends cookies on its own, requests that change anything
// and carry only a cookie are checked against CSRF: their Origin must be this
// API's own, or one of the realm's cors.origins. Cookies are SameSite=Lax by
// default. The admin API never takes cookies: it needs an Authorization header.
const cookieName = "__Secure-backd_session"

// credentialSource says where a request's credential came from.
type credentialSource int

const (
	noCredential credentialSource = iota
	fromHeader                    // Authorization: Bearer …
	fromCookie                    // the session cookie
)

// credentialOf returns the request's credential: the Authorization header when
// there is one (explicit wins), else the session cookie when the realm has
// cookies on. headerOK is false for an Authorization header that isn't a
// Bearer one.
func credentialOf(r *http.Request, s registry.RealmSettings) (token string, src credentialSource, headerOK bool) {
	if r.Header.Get("Authorization") != "" {
		t, ok := bearerToken(r)
		return t, fromHeader, ok
	}
	if s.Cookie.Enabled {
		if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
			return c.Value, fromCookie, true
		}
	}
	return "", noCredential, true
}

// safeMethod are the methods that don't change anything.
func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// originAllowed reports whether an Origin may use the realm's cookies: this
// API's own origin, or one the realm lists.
func originAllowed(s registry.RealmSettings, origin string, r *http.Request) bool {
	if origin == "" || origin == "null" {
		return false
	}
	if slices.Contains(s.CORSOrigins, origin) {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && u.Host == r.Host
}

// csrfCheck refuses a cookie-authenticated request that changes data unless it
// comes from an allowed origin. Browsers always send Origin on such requests.
func csrfCheck(w http.ResponseWriter, r *http.Request, s registry.RealmSettings) bool {
	if safeMethod(r.Method) || originAllowed(s, r.Header.Get("Origin"), r) {
		return true
	}
	logger(r.Context()).Warn("cookie request from an origin the realm doesn't allow", "origin", r.Header.Get("Origin"), "method", r.Method)
	writeError(w, r, http.StatusForbidden, codeForbidden, "requests that change data with a session cookie must come from this API's own origin or one of the realm's cors.origins")
	return false
}

func sameSite(s registry.RealmSettings) http.SameSite {
	switch s.Cookie.SameSite {
	case "strict":
		return http.SameSiteStrictMode
	case "none":
		return http.SameSiteNoneMode
	}
	return http.SameSiteLaxMode
}

// setSessionCookie sends the session's token in an HttpOnly cookie, valid for
// the longest the session can live (the server decides when it ends sooner).
func setSessionCookie(w http.ResponseWriter, r *http.Request, svc *auth.Users, p auth.Principal, token string) {
	maxAge := int(max(svc.MaxSessionExpiry(p.User, p.Session.CreatedAt).Sub(svc.Clock()), time.Second) / time.Second)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/v1/" + chi.URLParam(r, "realm"), MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: sameSite(svc.Settings),
	})
}

// clearSessionCookie tells the browser to forget the cookie.
func clearSessionCookie(w http.ResponseWriter, r *http.Request, s registry.RealmSettings) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: "", Path: "/v1/" + chi.URLParam(r, "realm"), MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: sameSite(s),
	})
}

// forgetCookie clears the session cookie of a request that ended its session
// (or all of them), when the realm uses cookies and the request carried one.
func forgetCookie(w http.ResponseWriter, r *http.Request) {
	s := usersOf(r).Settings
	if _, err := r.Cookie(cookieName); err == nil && s.Cookie.Enabled {
		clearSessionCookie(w, r, s)
	}
}

// askedForCookie checks a login or sign-up that asks for a cookie: the realm
// must have them on, and the request must come from an allowed origin, so a
// page of another site can't sign a visitor in to an account of its choosing.
func askedForCookie(w http.ResponseWriter, r *http.Request, s registry.RealmSettings, asked bool) (use, ok bool) {
	if !asked {
		return false, true
	}
	if !s.Cookie.Enabled {
		writeError(w, r, http.StatusBadRequest, codeValidation, "this realm doesn't use session cookies", Detail{Path: "cookie", Reason: "sessions.cookie isn't enabled in realm.yaml"})
		return false, false
	}
	if !originAllowed(s, r.Header.Get("Origin"), r) {
		writeError(w, r, http.StatusForbidden, codeForbidden, "session cookies are only issued to requests from this API's own origin or one of the realm's cors.origins")
		return false, false
	}
	return true, true
}

// corsCredentials says whether responses to origin may carry credentials:
// the realm uses cookies and lists the origin (a wildcard is refused at startup).
func corsCredentials(reg *registry.Registry, path, origin string) bool {
	parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 3)
	if len(parts) < 2 || parts[0] != "v1" {
		return false
	}
	rl, ok := reg.Realms[parts[1]]
	return ok && rl.Settings.Cookie.Enabled && slices.Contains(rl.Settings.CORSOrigins, origin)
}
