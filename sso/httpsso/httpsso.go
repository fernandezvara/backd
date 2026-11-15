// Package httpsso is the HTTP side of the sso engine that does not depend on the host: reading
// what a provider's callback carries and sending the browser back to the app. The host keeps its
// routes, its error envelope and its rate limits; each of its handlers parses a request, calls one
// Engine method and writes the Outcome with the helpers here.
package httpsso

import (
	"net/http"
	"net/url"

	"github.com/fernandezvara/backd/sso"
)

// Callback reads a provider's callback (a GET with the query, or Apple's form POST). ok is false
// when the request can't be read at all.
func Callback(r *http.Request, scope, provider string) (in sso.CallbackInput, ok bool) {
	if err := r.ParseForm(); err != nil {
		return sso.CallbackInput{}, false
	}
	return sso.CallbackInput{
		Scope: scope, Provider: provider,
		State: r.Form.Get("state"), Code: r.Form.Get("code"), Error: r.Form.Get("error"), User: r.Form.Get("user"),
	}, true
}

// Redirect sends the browser to the app's redirect_to (checked when the attempt began) with the
// given query parameters added (name, value, name, value…). It returns false when to is not a URL.
func Redirect(w http.ResponseWriter, r *http.Request, to string, params ...string) bool {
	u, err := url.Parse(to)
	if err != nil {
		return false
	}
	q := u.Query()
	for i := 0; i+1 < len(params); i += 2 {
		q.Set(params[i], params[i+1])
	}
	u.RawQuery = q.Encode()
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	status := http.StatusFound
	if r.Method == http.MethodPost {
		status = http.StatusSeeOther // the provider's POST becomes the browser's GET
	}
	h.Set("Location", u.String())
	w.WriteHeader(status)
	return true
}
