package httpapi

import (
	"context"
	"errors"
	htmltemplate "html/template"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/registry"
)

// A hosted flow is what a link in an email opens: backd serves the page
// (GET shows a form, POST acts) and the same route also answers JSON for
// apps that host their own pages. GET never uses up a token, so a mail
// scanner that opens every link can't spoil it; only the POST does.
//
// The flows themselves (verify an address, reset a password, ...) are
// hostedAction values; this file is the machinery they plug into.

// hostedInput is what a POST carries.
type hostedInput struct {
	Token    string
	Password string // only for actions with Password
	Locale   string // only for actions with Locale; may be empty
}

// hostedAction is one flow behind a link.
type hostedAction struct {
	Purpose  email.Purpose
	Page     string // the page kind its form uses (email.Page…)
	Password bool   // the form and the JSON body have a password
	Locale   bool   // the JSON body may carry a locale
	// Run does the work. It redeems the token itself (auth.Users.RedeemEmailToken)
	// at the point it can't be undone, and returns it for its redirect_to. A
	// token that doesn't work is auth.ErrInvalidToken; a password the policy
	// refuses is *auth.PolicyError.
	Run func(ctx context.Context, svc *auth.Users, in hostedInput) (auth.EmailToken, error)
}

// hostedActions are the flows this version serves. Each of them arrives with
// the feature it belongs to (verifying an address, resetting a password, ...).
func hostedActions() []hostedAction {
	return []hostedAction{
		{Purpose: "verify-email", Page: email.PageVerifyEmail, Run: func(ctx context.Context, svc *auth.Users, in hostedInput) (auth.EmailToken, error) {
			return svc.VerifyEmail(ctx, in.Token)
		}},
		{Purpose: "reset-password", Page: email.PageResetPassword, Password: true, Run: func(ctx context.Context, svc *auth.Users, in hostedInput) (auth.EmailToken, error) {
			return svc.ResetPassword(ctx, in.Token, in.Password)
		}},
	}
}

// flowKey is the name a purpose has in realm.yaml (redirects.verify_email).
func flowKey(p email.Purpose) string { return strings.ReplaceAll(string(p), "-", "_") }

// pageCSP lets a page style itself and post to backd, and nothing else: no
// scripts, no images, no frames, no other origins.
const pageCSP = "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// maxFormBytes bounds a posted form.
const maxFormBytes = 16 << 10

func (a *authAPI) hostedRoutes(r chi.Router) {
	jsonOnly := requireContentType("application/json")
	for _, act := range a.actions {
		r.Get("/"+act.Purpose.LinkPath(), a.hostedGet(act))
		r.Post("/"+act.Purpose.LinkPath(), a.hostedPost(act, jsonOnly))
	}
}

// pageHeaders marks a response as a page that must not leak its address: the
// token is in the query of the link.
func pageHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", pageCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
}

// hostedRealm returns the realm when it sends email (it has pages).
func (a *authAPI) hostedRealm(r *http.Request) (*registry.Realm, bool) {
	rl := a.reg.Realms[chi.URLParam(r, "realm")]
	return rl, rl != nil && rl.Pages != nil && rl.Settings.Email != nil
}

type hostedPage struct {
	rl     *registry.Realm
	act    hostedAction
	locale string
}

func (a *authAPI) newHostedPage(r *http.Request, act hostedAction) (hostedPage, bool) {
	rl, ok := a.hostedRealm(r)
	if !ok {
		return hostedPage{}, false
	}
	return hostedPage{rl: rl, act: act, locale: rl.Settings.BestLocale(acceptLanguages(r.Header.Get("Accept-Language"))...)}, true
}

func (p hostedPage) data(r *http.Request) email.PageData {
	realm := chi.URLParam(r, "realm")
	return email.PageData{
		Realm: realm, Locale: p.locale, Action: "/v1/" + realm + "/_auth/" + p.act.Purpose.LinkPath(), Kind: string(p.act.Purpose),
		BackURL: htmltemplate.URL(p.rl.Settings.Email.Redirects[flowKey(p.act.Purpose)]),
	}
}

func (p hostedPage) write(w http.ResponseWriter, r *http.Request, status int, kind string, d email.PageData) {
	body, err := p.rl.Pages.Render(kind, p.locale, d)
	if err != nil {
		logger(r.Context()).Error("render hosted page", "page", kind, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pageHeaders(w)
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// hostedGet shows the form when the token works, else the one error page.
func (a *authAPI) hostedGet(act hostedAction) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.newHostedPage(r, act)
		if !ok {
			writeError(w, r, http.StatusNotFound, codeNotFound, "resource not found")
			return
		}
		d := p.data(r)
		token := r.URL.Query().Get("token")
		if _, err := usersOf(r).PeekEmailToken(r.Context(), token, string(act.Purpose)); err != nil {
			if !errors.Is(err, auth.ErrInvalidToken) {
				authError(w, r, err)
				return
			}
			p.write(w, r, http.StatusBadRequest, email.PageInvalidToken, d)
			return
		}
		d.Token = token
		p.write(w, r, http.StatusOK, act.Page, d)
	}
}

// hostedPost acts: a form post answers with a page, a JSON body with JSON.
func (a *authAPI) hostedPost(act hostedAction, jsonOnly func(http.Handler) http.Handler) http.HandlerFunc {
	asJSON := jsonOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := map[string]string{"token": "string"}
		if act.Password {
			want["password"] = "string"
		}
		if act.Locale {
			want["locale"] = "string?"
		}
		obj, ok := readObject(w, r)
		if !ok {
			return
		}
		f, ok := fields(w, r, obj, want)
		if !ok {
			return
		}
		in := hostedInput{}
		in.Token, _ = f["token"].(string)
		in.Password, _ = f["password"].(string)
		in.Locale, _ = f["locale"].(string)
		if _, err := act.Run(r.Context(), usersOf(r), in); err != nil {
			if errors.Is(err, auth.ErrInvalidToken) {
				writeError(w, r, http.StatusBadRequest, codeInvalidToken, "invalid, expired or used token")
				return
			}
			authError(w, r, err, "password")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.hostedRealm(r); !ok {
			writeError(w, r, http.StatusNotFound, codeNotFound, "resource not found")
			return
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			asJSON.ServeHTTP(w, r)
			return
		}
		p, _ := a.newHostedPage(r, act)
		r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
		if err := r.ParseForm(); err != nil {
			p.write(w, r, http.StatusBadRequest, email.PageInvalidToken, p.data(r))
			return
		}
		d := p.data(r)
		in := hostedInput{Token: r.PostForm.Get("token"), Password: r.PostForm.Get("password")}
		d.Token = in.Token
		if act.Password && in.Password != r.PostForm.Get("password_confirm") {
			d.Error, d.ErrorCode = "the passwords don't match", "password_mismatch"
			p.write(w, r, http.StatusBadRequest, act.Page, d)
			return
		}
		tok, err := act.Run(r.Context(), usersOf(r), in)
		var pe *auth.PolicyError
		switch {
		case errors.Is(err, auth.ErrInvalidToken):
			d.Token = ""
			p.write(w, r, http.StatusBadRequest, email.PageInvalidToken, d)
		case errors.As(err, &pe):
			d.Error, d.ErrorCode = pe.Reason, "password_policy"
			p.write(w, r, http.StatusBadRequest, act.Page, d)
		case err != nil:
			logger(r.Context()).Error("hosted action", "purpose", act.Purpose, "error", err)
			pageHeaders(w)
			http.Error(w, "internal error", http.StatusInternalServerError)
		default:
			d.Token = ""
			es := p.rl.Settings.Email
			// The address stored with the token was checked when it was
			// asked for; check it again, in case realm.yaml has changed since.
			to := tok.RedirectTo
			if to == "" || !es.AllowedRedirect(to) {
				to = es.Redirects[flowKey(act.Purpose)]
			}
			d.Redirect, d.DelaySeconds = htmltemplate.URL(to), int(es.RedirectDelay/time.Second)
			p.write(w, r, http.StatusOK, email.PageResult, d)
		}
	}
}

// checkRedirect answers 400 invalid_redirect unless redirectTo is empty or
// within the realm's email.allowed_redirects.
func (a *authAPI) checkRedirect(w http.ResponseWriter, r *http.Request, redirectTo string) bool {
	if redirectTo == "" {
		return true
	}
	if rl := a.reg.Realms[chi.URLParam(r, "realm")]; rl != nil && rl.Settings.Email != nil && rl.Settings.Email.AllowedRedirect(redirectTo) {
		return true
	}
	writeError(w, r, http.StatusBadRequest, codeInvalidRedirect, "redirect_to must be an absolute URL within the realm's email.allowed_redirects", Detail{Path: "redirect_to", Reason: "is not allowed"})
	return false
}

// resendVerification handles POST /_auth/verify-email/resend. It answers
// 202 whatever the account is, so it can't be used to find out who is
// registered.
func (a *authAPI) resendVerification(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.hostedRealm(r); !ok {
		writeError(w, r, http.StatusNotFound, codeNotFound, "resource not found")
		return
	}
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"email": "string", "redirect_to": "string?"})
	if !ok {
		return
	}
	address, _ := f["email"].(string)
	redirectTo, _ := f["redirect_to"].(string)
	if _, err := registry.NormalizeEmail(address); err != nil {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", Detail{Path: "email", Reason: "must be a valid email address"})
		return
	}
	if !a.checkRedirect(w, r, redirectTo) {
		return
	}
	if err := usersOf(r).ResendVerification(r.Context(), address, redirectTo, clientIP(r), requestID(r.Context())); err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted"})
}

// requestPasswordReset handles POST /_auth/reset-password/request. Like
// resend, it answers 202 whatever the account is; past the per-address or
// per-client limit it answers 429 for every address alike.
func (a *authAPI) requestPasswordReset(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.hostedRealm(r); !ok {
		writeError(w, r, http.StatusNotFound, codeNotFound, "resource not found")
		return
	}
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"email": "string", "redirect_to": "string?"})
	if !ok {
		return
	}
	address, _ := f["email"].(string)
	redirectTo, _ := f["redirect_to"].(string)
	if _, err := registry.NormalizeEmail(address); err != nil {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", Detail{Path: "email", Reason: "must be a valid email address"})
		return
	}
	if !a.checkRedirect(w, r, redirectTo) {
		return
	}
	if err := usersOf(r).RequestPasswordReset(r.Context(), address, redirectTo, clientIP(r), requestID(r.Context())); err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted"})
}
