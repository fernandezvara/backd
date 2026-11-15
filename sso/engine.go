package sso

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/fernandezvara/backd/sso/pkce"
)

// The query parameters the engine adds to the app's redirect_to. They carry the prefix so that
// they can't collide with the app's own, since the app may be any page.
const (
	ParamCode     = "backd_code"     // the one-time login code
	ParamError    = "backd_error"    // what went wrong
	ParamProvider = "backd_provider" // with account_exists: the provider that was used
	ParamLinked   = "backd_linked"   // a link succeeded: the provider
	ParamOrg      = "backd_org"      // with the code: where the session will be scoped to
)

// Lifetimes of the records the engine makes.
const (
	AttemptTTL = 10 * time.Minute
	CodeTTL    = time.Minute
)

// Errors of the engine's own.
var (
	// ErrInvalidCode: a login code that is unknown, used, expired or not for this verifier.
	ErrInvalidCode = errors.New("the login code is not valid")
	// ErrAuthorizationCodeRequired: Apple's native sign-in needs the authorization code.
	ErrAuthorizationCodeRequired = errors.New("the authorization code is required")
)

// Engine runs sign-ins with external providers over the host's ports. It holds no state of its
// own, so one serves every realm.
type Engine struct {
	Store     Store
	Catalog   Catalog
	Directory Directory
	Events    Events
	Protocol  Protocol
	// Now is the clock; time.Now when nil.
	Now func() time.Time
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// hash is the key a state value or a login code is stored under.
func hash(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

func random() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// StartInput describes a sign-in or a link the host has checked (redirect allowed, strategy
// chosen, secrets set) and wants begun.
type StartInput struct {
	// Scope is where the attempt begins; the callback must be in the same one.
	Scope       string
	Provider    Provider
	CallbackURL string
	Intent      Intent
	Strategy    string
	// Challenge is the app's PKCE challenge.
	Challenge  string
	RedirectTo string
	LinkUserID string
	// Host is the host's own data, returned untouched at the callback.
	Host []byte
}

// Start remembers the attempt and returns the provider's authorize URL.
func (e *Engine) Start(ctx context.Context, in StartInput) (string, error) {
	state, err := random()
	if err != nil {
		return "", err
	}
	nonce, err := random()
	if err != nil {
		return "", err
	}
	verifier, err := random()
	if err != nil {
		return "", err
	}
	a := Attempt{
		Provider: in.Provider.Name, Scope: in.Scope, Org: in.Provider.Org, Intent: in.Intent, Strategy: in.Strategy,
		Challenge: in.Challenge, Verifier: verifier, Nonce: nonce, RedirectTo: in.RedirectTo, LinkUserID: in.LinkUserID,
		Host: in.Host, ExpiresAt: e.now().Add(AttemptTTL),
	}
	if err := e.Store.PutAttempt(ctx, hash(state), a); err != nil {
		return "", err
	}
	params := AuthorizeParams{RedirectURI: in.CallbackURL, State: state, Nonce: nonce}
	if e.Protocol.UsesPKCE(in.Provider) {
		params.CodeChallenge = pkce.Challenge(verifier)
	}
	return e.Protocol.AuthorizeURL(in.Provider, params), nil
}

// CallbackInput is what the provider's callback carried.
type CallbackInput struct {
	// Scope is the scope of the address the callback arrived at; empty when the address names a
	// place that can't be signed in to.
	Scope    string
	Provider string
	State    string
	Code     string
	// Error is the provider's own error code, if it sent one.
	Error string
	// User is Apple's first-authorization form field.
	User    string
	Request Request
}

// Outcome is what the engine tells the host to do at the end of a callback.
type Outcome struct {
	// Page: the attempt can't be traced to an app (unknown, used, expired or foreign state): show the
	// error page.
	Page bool
	// RedirectTo and Params: send the browser back to the app with these query parameters (name,
	// value, name, value…).
	RedirectTo string
	Params     []string
	// UserID is the user a successful sign-in is for.
	UserID string
	// Err is why the attempt failed, for the host's log; the app only gets the code.
	Err error
}

// Callback finishes a browser flow: it checks the attempt, runs the exchange and the checks of the
// place, decides who the person is and ends with a login code (or a code for what went wrong).
func (e *Engine) Callback(ctx context.Context, in CallbackInput) Outcome {
	if in.State == "" || in.Scope == "" {
		return Outcome{Page: true}
	}
	a, err := e.Store.TakeAttempt(ctx, hash(in.State))
	// The attempt must finish where it began: the same provider and the same scope, so a code
	// started for one organization can't be finished at another's callback.
	if err != nil || a.Provider != in.Provider || a.Scope != in.Scope {
		if err != nil && !errors.Is(err, ErrNotFound) {
			return Outcome{Page: true, Err: err}
		}
		return Outcome{Page: true}
	}
	back := func(err error, params ...string) Outcome {
		return Outcome{RedirectTo: a.RedirectTo, Params: params, Err: err}
	}
	fail := func(err error, code string) Outcome { return back(err, ParamError, code) }
	p, err := e.Catalog.Provider(ctx, a.Scope, a.Provider)
	if err != nil {
		return fail(err, CodeProviderError)
	}
	if a.Intent != IntentLink {
		if code := e.Directory.Superseded(ctx, a, p); code != "" {
			return fail(nil, code)
		}
	}
	if in.Error != "" {
		if in.Error == "access_denied" || in.Error == "user_cancelled_authorize" {
			return fail(nil, CodeCancelled)
		}
		return fail(fmt.Errorf("the provider answered %q", in.Error), CodeProviderError)
	}
	callback, ok := e.Catalog.CallbackURL(a.Scope, p)
	if in.Code == "" || !ok {
		return fail(errors.New("sign-in with a provider can't go on: no code or no public address"), CodeProviderError)
	}
	verifier := ""
	if e.Protocol.UsesPKCE(p) {
		verifier = a.Verifier
	}
	tokens, err := e.Protocol.Exchange(ctx, p, in.Code, callback, verifier)
	if err != nil {
		return fail(fmt.Errorf("the provider didn't give a token: %w", err), CodeProviderError)
	}
	claims, err := e.Protocol.Verify(ctx, p, tokens.IDToken, a.Nonce, false)
	if err != nil {
		return fail(fmt.Errorf("the provider's ID token is not valid: %w", err), CodeProviderError)
	}
	if p.Admit != nil && !p.Admit(claims) {
		return fail(fmt.Errorf("%w: a person the connection does not admit", ErrNotAllowed), CodeNotAllowed)
	}
	if p.Authority != nil {
		if err := p.Authority(ctx); err != nil {
			return fail(err, CodeProviderError)
		}
	}
	login := Login{
		Provider: p, Claims: claims, Intent: a.Intent, LinkUserID: a.LinkUserID, Host: a.Host, Request: in.Request,
		Profile: e.Directory.CleanProfile(claims, e.Protocol.FirstAuthorization(p, in.User)),
	}
	res, err := e.Resolve(ctx, login)
	if err != nil {
		var refused *Refused
		if errors.As(err, &refused) {
			e.emit(ctx, Event{Kind: EventRefused, Provider: p, UserID: refused.UserID, Reason: refused.Reason})
		}
		if errors.Is(err, ErrAccountExists) {
			return back(nil, ParamError, CodeAccountExists, ParamProvider, p.Name)
		}
		return Outcome{RedirectTo: a.RedirectTo, Params: []string{ParamError, CodeOf(err)}, Err: unexpected(err)}
	}
	e.keepAppleToken(ctx, p, res.Account.ID, p.ClientID, tokens.RefreshToken)
	scope, allowed := e.Directory.Enter(ctx, Entry{Login: login, Result: res, Attempt: a})
	if !allowed {
		return fail(nil, CodeNotAllowed)
	}
	if a.Intent == IntentLink {
		return back(nil, ParamLinked, p.Name)
	}
	code, err := e.issueCode(ctx, Code{
		UserID: res.Account.ID, Method: p.Method, Strategy: a.Strategy, Challenge: a.Challenge,
		NewUser: res.NewUser, Profile: res.Profile, Scope: scope,
	})
	if err != nil {
		return fail(fmt.Errorf("issue a login code: %w", err), CodeProviderError)
	}
	out := back(nil, ParamCode, code)
	out.UserID = res.Account.ID
	if scope != "" {
		out.Params = append(out.Params, ParamOrg, scope)
	}
	return out
}

// unexpected is err when the host should log it: the ones the rules produce on purpose are not.
func unexpected(err error) error {
	if CodeOf(err) == CodeProviderError {
		return err
	}
	return nil
}

func (e *Engine) emit(ctx context.Context, ev Event) {
	if e.Events != nil {
		e.Events.Emit(ctx, ev)
	}
}

func (e *Engine) keepAppleToken(ctx context.Context, p Provider, userID, audience, token string) {
	if p.Kind != Apple || token == "" {
		return
	}
	e.emit(ctx, Event{Kind: EventAppleToken, Provider: p, UserID: userID, Token: token, Audience: audience})
}

func (e *Engine) issueCode(ctx context.Context, c Code) (string, error) {
	code, err := random()
	if err != nil {
		return "", err
	}
	if !c.NewUser {
		c.Profile = nil
	}
	c.ExpiresAt = e.now().Add(CodeTTL)
	return code, e.Store.PutCode(ctx, hash(code), c)
}

// Redeem trades a login code and the PKCE verifier for the record the host makes a session from.
// The code is used up whether or not the verifier fits, so a stolen code can't be tried again.
// The record is returned with ErrInvalidCode when the code was good and the verifier was not, so the
// host can audit whose it was.
func (e *Engine) Redeem(ctx context.Context, code, verifier string) (Code, error) {
	if code == "" || verifier == "" {
		return Code{}, ErrInvalidCode
	}
	c, err := e.Store.TakeCode(ctx, hash(code))
	if errors.Is(err, ErrNotFound) {
		return Code{}, ErrInvalidCode
	}
	if err != nil {
		return Code{}, err
	}
	if subtle.ConstantTimeCompare([]byte(pkce.Challenge(verifier)), []byte(c.Challenge)) != 1 {
		return c, ErrInvalidCode
	}
	return c, nil
}

// IDTokenInput is a native app's sign-in: the ID token the platform gave it.
type IDTokenInput struct {
	Scope             string
	Provider          Provider
	IDToken, Nonce    string
	AuthorizationCode string
	Intent            Intent
	LinkUserID        string
	Host              []byte
	Request           Request
}

// Native is the outcome of a native sign-in.
type Native struct {
	Result
	// Scope is what the session is scoped to.
	Scope string
}

// IDToken signs a person in (or links their account) with an ID token the app already holds. The
// errors are ErrInvalidToken (401), ErrUnavailable (the provider could not be reached), ErrNotAllowed,
// ErrNotFound (a connection that can't be used), ErrAuthorizationCodeRequired, ErrExchange (Apple's
// code), or the rules' (CodeOf).
func (e *Engine) IDToken(ctx context.Context, in IDTokenInput) (Native, error) {
	p := in.Provider
	claims, err := e.Protocol.Verify(ctx, p, in.IDToken, in.Nonce, true)
	if err != nil {
		return Native{}, err
	}
	if p.Admit != nil && !p.Admit(claims) {
		return Native{}, ErrNotAllowed
	}
	if p.Authority != nil {
		if err := p.Authority(ctx); err != nil {
			return Native{}, fmt.Errorf("%w: %v", ErrNotFound, err)
		}
	}
	var refresh string
	if p.Kind == Apple && p.RevokeOnDelete {
		if in.AuthorizationCode == "" {
			return Native{}, ErrAuthorizationCodeRequired
		}
		if refresh, err = e.Protocol.AppleRefresh(ctx, p, claims, in.AuthorizationCode); err != nil {
			return Native{}, err
		}
	}
	login := Login{
		Provider: p, Claims: claims, Intent: in.Intent, LinkUserID: in.LinkUserID, Host: in.Host, Request: in.Request,
		Profile: e.Directory.CleanProfile(claims, nil),
	}
	res, err := e.Resolve(ctx, login)
	if err != nil {
		var refused *Refused
		if errors.As(err, &refused) {
			e.emit(ctx, Event{Kind: EventRefused, Provider: p, UserID: refused.UserID, Reason: refused.Reason})
		}
		return Native{}, err
	}
	scope, allowed := e.Directory.Enter(ctx, Entry{Login: login, Result: res, Attempt: Attempt{Provider: p.Name, Intent: in.Intent, LinkUserID: in.LinkUserID, Host: in.Host}})
	if !allowed {
		return Native{}, ErrNotAllowed
	}
	e.keepAppleToken(ctx, p, res.Account.ID, claims.Audience, refresh)
	return Native{Result: res, Scope: scope}, nil
}

// Resolve decides who a provider's person is under the rules of design/sso.md §8.1 and carries
// the decision out through the Directory. It starts no session.
func (e *Engine) Resolve(ctx context.Context, l Login) (Result, error) {
	d := e.Directory
	email := d.NormalizeEmail(l.Claims.Email)
	key := IdentityKey{Provider: l.Provider.Name, Org: l.Provider.Org, Subject: l.Claims.Subject}
	f := Facts{Intent: l.Intent, Email: email, EmailVerified: l.Claims.EmailVerified, LinkUserID: l.LinkUserID}
	find := func(get func() (Account, error)) (*Account, error) {
		a, err := get()
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return &a, nil
	}
	var err error
	if f.Known, err = find(func() (Account, error) { return d.Identity(ctx, key) }); err != nil {
		return Result{}, err
	}
	if f.Known == nil && l.Intent == IntentLink {
		if f.LinkUser, err = find(func() (Account, error) { return d.User(ctx, l.LinkUserID) }); err != nil {
			return Result{}, err
		}
	}
	if f.Known == nil && l.Intent != IntentLink && email != "" {
		if f.ByEmail, err = find(func() (Account, error) { return d.UserByEmail(ctx, email) }); err != nil {
			return Result{}, err
		}
	}
	dec := Decide(l.Provider, f)
	switch dec.Action {
	case SignInKnown:
		if l.Intent != IntentLink {
			if err := d.MaySignIn(ctx, *f.Known, l.Request); err != nil {
				return Result{}, err
			}
		}
		d.Touch(ctx, key, l, email)
		return Result{Account: *f.Known}, nil
	case LinkToSignedIn:
		if err := d.Link(ctx, *f.LinkUser, l, email); err != nil {
			return Result{}, err
		}
		return Result{Account: *f.LinkUser, Linked: true}, nil
	case LinkToExisting:
		if err := d.MaySignIn(ctx, *f.ByEmail, l.Request); err != nil {
			return Result{}, err
		}
		if err := d.Link(ctx, *f.ByEmail, l, email); err != nil {
			return Result{}, err
		}
		return Result{Account: *f.ByEmail, Linked: true}, nil
	case Create:
		a, err := d.Create(ctx, l, email, dec.Verified)
		if err != nil {
			return Result{}, err
		}
		res := Result{Account: a, NewUser: true, Profile: l.Profile}
		if err := d.MaySignIn(ctx, a, l.Request); err != nil {
			return res, err
		}
		return res, nil
	}
	return Result{}, dec.Err
}
