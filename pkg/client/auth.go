package client

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"
)

// User is a realm user as the user sees themselves.
type User struct {
	ID            string   `json:"id"`
	Email         string   `json:"email"`
	EmailVerified bool     `json:"email_verified"`
	Roles         []string `json:"roles"`
	// Locale is the user's language, one the realm lists (used for their emails).
	Locale    string `json:"locale"`
	CreatedAt string `json:"created_at"`
}

// Identity is one way the user signs in: a password or an external provider.
type Identity struct {
	Provider      string  `json:"provider"`
	Email         string  `json:"email"`
	EmailVerified bool    `json:"email_verified"`
	CreatedAt     string  `json:"created_at"`
	LastUsedAt    *string `json:"last_used_at"`
}

// MeOrg is the organization a scoped session belongs to.
type MeOrg struct {
	ID    string   `json:"id"`
	Name  string   `json:"name,omitempty"`
	Slug  *string  `json:"slug,omitempty"`
	Roles []string `json:"roles,omitempty"`
	Teams []string `json:"teams,omitempty"`
}

// Me is the signed-in user with their sign-in methods.
type Me struct {
	User
	Identities    []Identity `json:"identities"`
	ReauthMethods []string   `json:"reauth_methods"`
	SignInMethods []string   `json:"sign_in_methods"`
	// Consents maps each consent the user holds to its version (with consents declared in the realm).
	Consents map[string]string `json:"consents,omitempty"`
	// ToAccept are the required consents still to accept.
	ToAccept []ConsentRef `json:"to_accept,omitempty"`
	Org      *MeOrg       `json:"org,omitempty"`
}

// Profile is what a provider says of a person who just signed up, handed over once. Treat it as a hint.
type Profile struct {
	Name       string `json:"name,omitempty"`
	GivenName  string `json:"given_name,omitempty"`
	FamilyName string `json:"family_name,omitempty"`
	Picture    string `json:"picture,omitempty"`
}

// Session is a new session. The client has stored its token already.
type Session struct {
	Token     string `json:"token"`
	TokenType string `json:"token_type,omitempty"`
	SessionID string `json:"session_id"`
	// ExpiresAt is RFC 3339; pushed forward as the session is used.
	ExpiresAt string `json:"expires_at"`
	User      *User  `json:"user,omitempty"`
	// Org is the organization the session is scoped to: its token reaches that organization and no other.
	Org string `json:"org,omitempty"`
	// NewUser and Profile are set by a sign-in with a provider.
	NewUser           *bool    `json:"new_user,omitempty"`
	Profile           *Profile `json:"profile,omitempty"`
	RecoveryCodesLeft *int     `json:"recovery_codes_left,omitempty"`
}

// StepKind says what a first factor led to.
type StepKind int

const (
	// StepSession: signed in; Step.Session has the session.
	StepSession StepKind = iota
	// StepMFA: a code of the user's second factor is needed; send it with VerifyMFA(Challenge, method, code).
	StepMFA
	// StepEnrollment: the user's role requires a second factor and they have none; set one up with
	// MFA().Setup and MFA().Confirm using the Challenge.
	StepEnrollment
	// StepConsent: the sign-in is done but consents must be accepted first; show their texts
	// (Consents().Text) and send the acceptance with AcceptConsents(Challenge, consents).
	StepConsent
	// StepLinked: a provider was linked to the signed-in user (CompleteSignIn of a link); Step.Linked names it.
	StepLinked
)

// Step is what a first factor answered: the session, or the second step the user has to take.
type Step struct {
	Kind      StepKind
	Session   *Session
	Challenge string
	// Methods are the second factors the user can use with StepMFA ("totp", "recovery_code").
	Methods []string
	// Consents are the ones to accept with StepConsent.
	Consents []ConsentRef
	Linked   string
}

// SecondStepRequiredError is what Login, Signup and the methods that promise a session return when
// the sign-in needs a second step: use SignIn to get the step as a value instead.
type SecondStepRequiredError struct {
	*baseError
	Challenge string
	Methods   []string
	Consents  []ConsentRef
}

func (e *SecondStepRequiredError) Unwrap() error { return e.baseError }

// SessionInfo is one of the user's sessions.
type SessionInfo struct {
	ID         string `json:"id"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at"`
	ExpiresAt  string `json:"expires_at"`
	// Current is the session making this request.
	Current bool `json:"current"`
	// Org is the id of the organization the session is scoped to (none for an operator's).
	Org string `json:"org,omitempty"`
	// Connection is the provider of the organization's connection that opened it.
	Connection string `json:"connection,omitempty"`
}

// AuthEvent says what changed.
type AuthEvent string

const (
	// SignedIn: a sign-in (signup or login).
	SignedIn AuthEvent = "SIGNED_IN"
	// SignedOut: a sign-out (logout, logout everywhere, account deleted).
	SignedOut AuthEvent = "SIGNED_OUT"
	// SessionExpired: the server refused the stored token.
	SessionExpired AuthEvent = "SESSION_EXPIRED"
)

// AuthChange is what a listener receives.
type AuthChange struct {
	Event AuthEvent
	// Session is the new session for SignedIn, otherwise nil.
	Session *Session
}

// Auth is the sign-up, sign-in and session side of a client: c.Auth().
type Auth struct {
	c *Client

	mu        sync.Mutex
	listeners map[int]func(AuthChange)
	nextID    int
	current   *Session

	// MFA is the second factor of the signed-in user.
	MFA *MFA
	// MagicLink signs in with a link sent by email, and a code to type when the link opens elsewhere.
	MagicLink *MagicLink
}

func newAuth(c *Client) *Auth {
	a := &Auth{c: c, listeners: map[int]func(AuthChange){}}
	a.MFA = &MFA{a}
	a.MagicLink = &MagicLink{a}
	c.expired = func(ctx context.Context) { _ = a.signedOut(ctx, SessionExpired) }
	return a
}

// body builds a request body without the fields that were not given.
type body map[string]any

func (b body) set(key string, v any) body {
	switch x := v.(type) {
	case nil:
		return b
	case string:
		if x == "" {
			return b
		}
	case map[string]string:
		if len(x) == 0 {
			return b
		}
	}
	b[key] = v
	return b
}

func authBase(org string) []string {
	if org != "" {
		return []string{"_auth", "orgs", org}
	}
	return []string{"_auth"}
}

func (a *Auth) base(org string) []string {
	if org = strings.TrimSpace(org); org != "" {
		return authBase(org)
	}
	return authBase(a.c.org)
}

func path(parts ...string) []string { return parts }

func join(base []string, rest ...string) []string {
	return append(append([]string{}, base...), rest...)
}

// accepted posts a request that the server answers 202 or 204 to whether or not the address is known.
func (a *Auth) accepted(ctx context.Context, p []string, b body, opts ...RequestOption) error {
	_, err := a.c.Do(ctx, "POST", p, b, nil, append([]RequestOption{WithoutAuth()}, opts...)...)
	return err
}

// SignUpInput is the input of Signup.
type SignUpInput struct {
	Email, Password string
	// Invitation is an invitation token, for realms with signup: invite.
	Invitation string
	Locale     string
	// RedirectTo is where the verification link sends the person back to.
	RedirectTo string
	// Org signs up through this organization's routes (the client's own by default).
	Org string
	// Consents accepted at sign-up (name to version).
	Consents map[string]string
}

// Signup creates an account and signs in. When the realm requires a verified address first it
// returns an error matching ErrVerificationRequired and nothing is stored; a person with a second
// step to take gets a *SecondStepRequiredError.
func (a *Auth) Signup(ctx context.Context, in SignUpInput, opts ...RequestOption) (*Session, error) {
	b := body{"email": in.Email, "password": in.Password}
	b.set("consents", in.Consents).set("invitation", in.Invitation).set("locale", in.Locale).set("redirect_to", in.RedirectTo)
	var raw json.RawMessage
	res, err := a.c.Do(ctx, "POST", join(a.base(in.Org), "signup"), b, &raw, append([]RequestOption{WithoutAuth()}, opts...)...)
	if err != nil {
		return nil, err
	}
	if res.Status == 202 {
		return nil, newError(202, "verification_required", "the account was created: verify the email address, then sign in", kVerification)
	}
	step, err := a.step(ctx, raw)
	if err != nil {
		return nil, err
	}
	return a.session(step)
}

// SignInInput is the input of SignIn and Login.
type SignInInput struct {
	Email, Password string
	// Org signs in through this organization's routes (the client's own by default).
	Org        string
	Invitation string
}

// SignIn checks the first factor and returns the step it leads to: the session (stored), or the
// second step to take. Use Login when a second step should be an error.
func (a *Auth) SignIn(ctx context.Context, in SignInInput, opts ...RequestOption) (*Step, error) {
	b := body{}.set("email", in.Email).set("password", in.Password).set("invitation", in.Invitation)
	var raw json.RawMessage
	if _, err := a.c.Do(ctx, "POST", join(a.base(in.Org), "login"), b, &raw, append([]RequestOption{WithoutAuth()}, opts...)...); err != nil {
		return nil, err
	}
	return a.step(ctx, raw)
}

// Login signs in with email and password and returns the session. A second step to take is a
// *SecondStepRequiredError carrying the challenge.
func (a *Auth) Login(ctx context.Context, in SignInInput, opts ...RequestOption) (*Session, error) {
	step, err := a.SignIn(ctx, in, opts...)
	if err != nil {
		return nil, err
	}
	return a.session(step)
}

// session turns a step into the session a method promises, or a *SecondStepRequiredError.
func (a *Auth) session(s *Step) (*Session, error) {
	switch s.Kind {
	case StepSession:
		return s.Session, nil
	case StepConsent:
		return nil, &SecondStepRequiredError{
			baseError: newError(200, "consent_required", "consents must be accepted: show Consents().Text(name, version) and call AcceptConsents(challenge, consents)", kSecondStep),
			Challenge: s.Challenge, Consents: s.Consents,
		}
	case StepEnrollment:
		return nil, &SecondStepRequiredError{
			baseError: newError(200, "mfa_enrollment_required", "this account must set up a second factor: MFA.Setup with the challenge", kSecondStep),
			Challenge: s.Challenge,
		}
	case StepMFA:
		return nil, &SecondStepRequiredError{
			baseError: newError(200, "mfa_required", "a code of the second factor is needed: VerifyMFA(challenge, method, code)", kSecondStep),
			Challenge: s.Challenge, Methods: s.Methods,
		}
	}
	return nil, errors.New("client: the sign-in did not give a session")
}

// step reads what a first factor answered; a session is stored.
func (a *Auth) step(ctx context.Context, raw json.RawMessage) (*Step, error) {
	var d struct {
		MFARequired           bool         `json:"mfa_required"`
		MFAEnrollmentRequired bool         `json:"mfa_enrollment_required"`
		ConsentRequired       bool         `json:"consent_required"`
		Challenge             string       `json:"challenge"`
		Methods               []string     `json:"methods"`
		Consents              []ConsentRef `json:"consents"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, newError(0, "invalid_response", "the sign-in answer is not the JSON expected: "+err.Error(), kBackd)
	}
	switch {
	case d.MFARequired:
		return &Step{Kind: StepMFA, Challenge: d.Challenge, Methods: d.Methods}, nil
	case d.MFAEnrollmentRequired:
		return &Step{Kind: StepEnrollment, Challenge: d.Challenge}, nil
	case d.ConsentRequired:
		return &Step{Kind: StepConsent, Challenge: d.Challenge, Consents: d.Consents}, nil
	}
	var s Session
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, newError(0, "invalid_response", "the sign-in answer is not the JSON expected: "+err.Error(), kBackd)
	}
	if err := a.signedIn(ctx, &s); err != nil {
		return nil, err
	}
	return &Step{Kind: StepSession, Session: &s}, nil
}

// VerifyMFA finishes a sign-in with a code of the second factor (method "totp" or "recovery_code").
func (a *Auth) VerifyMFA(ctx context.Context, challenge, method, code string, opts ...RequestOption) (*Session, error) {
	var raw json.RawMessage
	b := body{"challenge": challenge, "method": method, "code": code}
	if _, err := a.c.Do(ctx, "POST", path("_auth", "mfa", "verify"), b, &raw, append([]RequestOption{WithoutAuth()}, opts...)...); err != nil {
		return nil, err
	}
	return a.sessionOf(ctx, raw)
}

// AcceptConsents finishes a sign-in that had consents to accept first (name to version).
func (a *Auth) AcceptConsents(ctx context.Context, challenge string, consents map[string]string, opts ...RequestOption) (*Session, error) {
	var raw json.RawMessage
	b := body{"challenge": challenge, "consents": consents}
	if _, err := a.c.Do(ctx, "POST", path("_auth", "consents", "accept"), b, &raw, append([]RequestOption{WithoutAuth()}, opts...)...); err != nil {
		return nil, err
	}
	return a.sessionOf(ctx, raw)
}

// sessionOf stores a session answer.
func (a *Auth) sessionOf(ctx context.Context, raw json.RawMessage) (*Session, error) {
	var s Session
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, newError(0, "invalid_response", "the answer is not the JSON expected: "+err.Error(), kBackd)
	}
	if err := a.signedIn(ctx, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// OrgSession is the session of Auth.SignUpWithOrg with the organization that was made.
type OrgSession struct {
	*Session
	Organization json.RawMessage
}

// SignUpWithOrgInput is the input of SignUpWithOrg.
type SignUpWithOrgInput struct {
	Email, Password string
	// Name is the organization's name; Slug the short name people know it by.
	Name, Slug string
	Invitation string
	Locale     string
	RedirectTo string
	Consents   map[string]string
}

// SignUpWithOrg creates an account and an organization together and signs in. The session is
// scoped to the new organization: with a storage that can keep a token per organization, it is kept
// in the client of that organization (c.Org(slug)) and returned from there.
func (a *Auth) SignUpWithOrg(ctx context.Context, in SignUpWithOrgInput, opts ...RequestOption) (*OrgSession, error) {
	b := body{}.set("email", in.Email).set("password", in.Password).set("name", in.Name).set("slug", in.Slug).
		set("invitation", in.Invitation).set("locale", in.Locale).set("redirect_to", in.RedirectTo).set("consents", in.Consents)
	var raw json.RawMessage
	res, err := a.c.Do(ctx, "POST", path("_auth", "orgs"), b, &raw, append([]RequestOption{WithoutAuth()}, opts...)...)
	if err != nil {
		return nil, err
	}
	if res.Status == 202 {
		return nil, newError(202, "verification_required", "the account and the organization were created: verify the email address, then sign in", kVerification)
	}
	var d struct {
		Organization json.RawMessage `json:"organization"`
	}
	_ = json.Unmarshal(raw, &d)
	target := a
	if _, ok := a.c.rootClient().store.(SlotStorage); ok && in.Slug != "" {
		oc, err := a.c.Org(in.Slug)
		if err != nil {
			return nil, err
		}
		target = oc.auth
	}
	step, err := target.step(ctx, raw)
	if err != nil {
		return nil, err
	}
	s, err := target.session(step)
	if err != nil {
		return nil, err
	}
	return &OrgSession{Session: s, Organization: d.Organization}, nil
}

// FindOrgs asks the server to email the person the organizations their address belongs to. The
// answer is the same whether or not the address is known.
func (a *Auth) FindOrgs(ctx context.Context, email, locale string, opts ...RequestOption) error {
	return a.accepted(ctx, path("_auth", "orgs", "find"), body{}.set("email", email).set("locale", locale), opts...)
}

// InvitationPreview is what an invitation to an organization offers.
type InvitationPreview struct {
	Org struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"org"`
	Roles []string `json:"roles"`
	Teams []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"teams"`
	Binding   string `json:"binding"`
	ExpiresAt string `json:"expires_at"`
}

// PreviewInvitation says what an invitation offers before the person has signed in, so the app can
// ask "Join Acme as editor?". org is the organization the token is for, by id or slug (the client's
// own by default). Every token that does not work fails alike with invalid_invitation.
func (a *Auth) PreviewInvitation(ctx context.Context, token, org string, opts ...RequestOption) (*InvitationPreview, error) {
	base := a.base(org)
	if len(base) < 3 {
		return nil, errors.New("PreviewInvitation: the organization is required (the client's Org option or the org argument)")
	}
	var out InvitationPreview
	_, err := a.c.Do(ctx, "POST", join(base, "invitations", "preview"), body{"token": token}, &out, append([]RequestOption{WithoutAuth()}, opts...)...)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ReauthInput proves who the user is again before a sensitive action.
type ReauthInput struct {
	// Method is one of the user's reauth_methods: "totp", "recovery_code", "password" or "email_code".
	Method   string
	Code     string
	Password string
}

// ReauthProof is the fresh proof of identity; the client sends it with sensitive calls while it is good.
type ReauthProof struct {
	Token     string
	ExpiresAt string
}

// Reauth gets a fresh proof of identity. The client keeps it and sends it with the sensitive calls
// (changing a password, an email address, the second factor, deleting the account) until it expires.
func (a *Auth) Reauth(ctx context.Context, in ReauthInput, opts ...RequestOption) (*ReauthProof, error) {
	var out struct {
		Token     string `json:"reauth_token"`
		ExpiresAt string `json:"expires_at"`
	}
	b := body{}.set("method", in.Method).set("code", in.Code).set("password", in.Password)
	if _, err := a.c.Do(ctx, "POST", path("_auth", "reauth"), b, &out, opts...); err != nil {
		return nil, err
	}
	exp, _ := time.Parse(time.RFC3339Nano, out.ExpiresAt)
	a.c.proof.Store(&reauthProof{token: out.Token, expires: exp})
	return &ReauthProof{Token: out.Token, ExpiresAt: out.ExpiresAt}, nil
}

// HasFreshProof reports whether a proof from Reauth is still good.
func (a *Auth) HasFreshProof() bool {
	p := a.c.proof.Load()
	return p != nil && p.expires.After(time.Now())
}

// RequestReauthCode emails the user a code to prove who they are with Reauth (method "email_code").
func (a *Auth) RequestReauthCode(ctx context.Context, opts ...RequestOption) error {
	_, err := a.c.Do(ctx, "POST", path("_auth", "reauth", "email-code"), nil, nil, opts...)
	return err
}

// WithProof runs action; when the server asks for a fresh proof (reauth_required) it asks the
// person with ask (given the methods they can use), reauthenticates and runs action again.
func (a *Auth) WithProof(ctx context.Context, ask func(ctx context.Context, methods []string) (ReauthInput, error), action func(ctx context.Context) error) error {
	err := action(ctx)
	var e *Error
	if err == nil || !errors.As(err, &e) || !errors.Is(err, ErrAuthentication) || e.Code != "reauth_required" {
		return err
	}
	me, err := a.Me(ctx)
	if err != nil {
		return err
	}
	in, err := ask(ctx, me.ReauthMethods)
	if err != nil {
		return err
	}
	if _, err := a.Reauth(ctx, in); err != nil {
		return err
	}
	return action(ctx)
}

// OAuthOptions are the options of a redirect sign-in or link.
type OAuthOptions struct {
	// RedirectTo is where backd sends the browser back to (it must be within the realm's
	// sign_in.allowed_redirects); outside a browser the realm's sign_in.app_url when empty.
	RedirectTo string
	// Invitation is an invitation token, for realms with signup: invite.
	Invitation string
	Locale     string
	// Consents accepted for the account the sign-in may make (name to version).
	Consents map[string]string
	// Org signs in through the connection of this organization instead of the realm's own providers.
	Org string
}

// SignInWith starts a sign-in with a provider ("google", "microsoft", "apple" or a configured
// one) and returns the address to send the person to. The PKCE verifier is kept in the OAuth
// storage until CompleteSignIn gets the address the person comes back to.
func (a *Auth) SignInWith(ctx context.Context, provider string, o OAuthOptions, opts ...RequestOption) (string, error) {
	return a.startOAuth(ctx, provider, "signin", o, opts)
}

// LinkProvider starts linking a provider to the signed-in user; it needs a recent proof (Reauth).
func (a *Auth) LinkProvider(ctx context.Context, provider string, o OAuthOptions, opts ...RequestOption) (string, error) {
	return a.startOAuth(ctx, provider, "link", o, opts)
}

func (a *Auth) startOAuth(ctx context.Context, provider, intent string, o OAuthOptions, opts []RequestOption) (string, error) {
	verifier, challenge := newPKCE()
	b := body{"code_challenge": challenge, "intent": intent}
	b.set("redirect_to", o.RedirectTo).set("invitation", o.Invitation).set("locale", o.Locale).set("consents", o.Consents)
	base := a.base(o.Org)
	if intent == "link" {
		base = authBase(strings.TrimSpace(o.Org))
	}
	if intent != "link" {
		opts = append([]RequestOption{WithoutAuth()}, opts...)
	}
	var out struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	if _, err := a.c.Do(ctx, "POST", join(base, "oauth", provider, "start"), b, &out, opts...); err != nil {
		return "", err
	}
	if err := savePending(ctx, a.c.oauth, pending{Verifier: verifier, Provider: provider, Intent: intent}); err != nil {
		return "", err
	}
	return out.AuthorizeURL, nil
}

// CompleteSignIn finishes a redirect sign-in (or a link, or a magic link) from the address the
// person came back to. It returns nil, nil when the address carries no sign-in. The result is the
// step the sign-in led to (StepLinked after a link). A sign-in through an organization's
// connection is kept by that organization's client when the storage allows it.
func (a *Auth) CompleteSignIn(ctx context.Context, address string, opts ...RequestOption) (*Step, error) {
	ret := ParseOAuthReturn(address)
	if ret == nil {
		return nil, nil
	}
	p := loadPending(ctx, a.c.oauth)
	_ = a.c.oauth.Remove(ctx)
	if ret.Error != "" {
		provider := ret.Provider
		if provider == "" && p != nil {
			provider = p.Provider
		}
		return nil, oauthError(ret.Error, provider)
	}
	if ret.Linked != "" {
		return &Step{Kind: StepLinked, Linked: ret.Linked}, nil
	}
	if p == nil {
		return nil, newError(400, "wrong_device", "this sign-in was not started here: open the link on the device and in the program where you started it", kValidation)
	}
	var raw json.RawMessage
	b := body{"code": ret.Code, "code_verifier": p.Verifier}
	if _, err := a.c.Do(ctx, "POST", path("_auth", "token"), b, &raw, append([]RequestOption{WithoutAuth()}, opts...)...); err != nil {
		return nil, err
	}
	target := a
	if _, ok := a.c.rootClient().store.(SlotStorage); ok && ret.Org != "" && a.c.org == "" {
		oc, err := a.c.Org(ret.Org)
		if err != nil {
			return nil, err
		}
		target = oc.auth
	}
	return target.step(ctx, raw)
}

// UnlinkProvider removes a provider from the signed-in user; it needs a recent proof.
func (a *Auth) UnlinkProvider(ctx context.Context, provider, org string, opts ...RequestOption) error {
	if org != "" {
		opts = append(opts, WithQuery(url.Values{"org": {org}}))
	}
	_, err := a.c.Do(ctx, "DELETE", path("_auth", "identities", provider), nil, nil, append(opts, WithReauth())...)
	return err
}

// SignInOptions says how people can sign in to the realm (or to an organization).
type SignInOptions struct {
	// Providers are the external providers that are enabled.
	Providers []string
	// Methods are the other ways: "password", "magic_link".
	Methods     []string
	SSORequired bool
}

// Options asks how people can sign in; org is an organization by id or slug (the client's own by default).
func (a *Auth) Options(ctx context.Context, org string, opts ...RequestOption) (*SignInOptions, error) {
	var out struct {
		Items       []string `json:"items"`
		Methods     []string `json:"methods"`
		SSORequired bool     `json:"sso_required"`
	}
	if _, err := a.c.Do(ctx, "GET", join(a.base(org), "providers"), nil, &out, append([]RequestOption{WithoutAuth()}, opts...)...); err != nil {
		return nil, err
	}
	return &SignInOptions{Providers: out.Items, Methods: out.Methods, SSORequired: out.SSORequired}, nil
}

// Providers lists the external providers that are enabled.
func (a *Auth) Providers(ctx context.Context, org string, opts ...RequestOption) ([]string, error) {
	o, err := a.Options(ctx, org, opts...)
	if err != nil {
		return nil, err
	}
	return o.Providers, nil
}

// IDTokenInput is the input of SignInWithIDToken and LinkProviderWithIDToken: the identity token
// a native sign-in (Sign in with Apple, Google) gave the program.
type IDTokenInput struct {
	IDToken           string
	Nonce             string
	AuthorizationCode string
	Invitation        string
	Locale            string
	Org               string
	Consents          map[string]string
}

func (in IDTokenInput) body() body {
	return body{}.set("id_token", in.IDToken).set("nonce", in.Nonce).set("authorization_code", in.AuthorizationCode).
		set("invitation", in.Invitation).set("locale", in.Locale).set("consents", in.Consents)
}

// SignInWithIDToken signs in with an identity token from a provider's native flow.
func (a *Auth) SignInWithIDToken(ctx context.Context, provider string, in IDTokenInput, opts ...RequestOption) (*Step, error) {
	var raw json.RawMessage
	if _, err := a.c.Do(ctx, "POST", join(a.base(in.Org), "oauth", provider, "id-token"), in.body(), &raw, append([]RequestOption{WithoutAuth()}, opts...)...); err != nil {
		return nil, err
	}
	return a.step(ctx, raw)
}

// LinkProviderWithIDToken links a provider to the signed-in user with an identity token.
func (a *Auth) LinkProviderWithIDToken(ctx context.Context, provider string, in IDTokenInput, opts ...RequestOption) error {
	b := in.body()
	b["intent"] = "link"
	_, err := a.c.Do(ctx, "POST", join(authBase(strings.TrimSpace(in.Org)), "oauth", provider, "id-token"), b, nil, opts...)
	return err
}

// ResendVerification asks for the verification email again; the answer is the same whether or not the address is known.
func (a *Auth) ResendVerification(ctx context.Context, email, redirectTo string, opts ...RequestOption) error {
	return a.accepted(ctx, path("_auth", "verify-email", "resend"), body{}.set("email", email).set("redirect_to", redirectTo), opts...)
}

// VerifyEmail confirms an address with the token of the link.
func (a *Auth) VerifyEmail(ctx context.Context, token string, opts ...RequestOption) error {
	return a.accepted(ctx, path("_auth", "verify-email"), body{"token": token}, opts...)
}

// RequestPasswordReset emails a reset link; the answer is the same whether or not the address is known.
func (a *Auth) RequestPasswordReset(ctx context.Context, email, redirectTo, org string, opts ...RequestOption) error {
	return a.accepted(ctx, join(a.base(org), "reset-password", "request"), body{}.set("email", email).set("redirect_to", redirectTo), opts...)
}

// ResetPassword sets a new password with the token of the reset link.
func (a *Auth) ResetPassword(ctx context.Context, token, password string, opts ...RequestOption) error {
	return a.accepted(ctx, path("_auth", "reset-password"), body{"token": token, "password": password}, opts...)
}

// UpdatePasswordInput is the input of UpdatePassword: the way a person whose password expired
// chooses a new one, proving the old one (and a second factor when they have one).
type UpdatePasswordInput struct {
	Email, Password, NewPassword string
	Code, Method                 string
}

// UpdatePassword replaces an expired password.
func (a *Auth) UpdatePassword(ctx context.Context, in UpdatePasswordInput, opts ...RequestOption) error {
	b := body{}.set("email", in.Email).set("password", in.Password).set("new_password", in.NewPassword).set("code", in.Code).set("method", in.Method)
	return a.accepted(ctx, path("_auth", "password", "update"), b, opts...)
}

// RequestEmailChange asks to change the signed-in user's address; a confirmation goes to the new
// one. It needs a recent proof (Reauth).
func (a *Auth) RequestEmailChange(ctx context.Context, newEmail, redirectTo string, opts ...RequestOption) error {
	_, err := a.c.Do(ctx, "POST", path("_auth", "email"), body{}.set("new_email", newEmail).set("redirect_to", redirectTo), nil, append(opts, WithReauth())...)
	return err
}

// ConfirmEmailChange confirms the change with the token of the link sent to the new address.
func (a *Auth) ConfirmEmailChange(ctx context.Context, token string, opts ...RequestOption) error {
	return a.accepted(ctx, path("_auth", "confirm-email-change"), body{"token": token}, opts...)
}

// RevertEmailChange undoes a change with the token of the link sent to the old address.
func (a *Auth) RevertEmailChange(ctx context.Context, token string, opts ...RequestOption) error {
	return a.accepted(ctx, path("_auth", "revert-email-change"), body{"token": token}, opts...)
}

// AcceptInvitation accepts an invitation by its token; password sets one for a new account.
func (a *Auth) AcceptInvitation(ctx context.Context, token, password, locale string, opts ...RequestOption) error {
	return a.accepted(ctx, path("_auth", "accept-invitation"), body{}.set("token", token).set("password", password).set("locale", locale), opts...)
}

// Logout ends the current session. The local token is removed even when the server says it was already gone.
func (a *Auth) Logout(ctx context.Context, opts ...RequestOption) error {
	_, err := a.c.Do(ctx, "POST", path("_auth", "logout"), nil, nil, append([]RequestOption{WithoutExpire()}, opts...)...)
	if err != nil && !errors.Is(err, ErrAuthentication) {
		return err
	}
	return a.signedOut(ctx, SignedOut)
}

// LogoutAll ends every session of the user.
func (a *Auth) LogoutAll(ctx context.Context, opts ...RequestOption) error {
	if _, err := a.c.Do(ctx, "POST", path("_auth", "logout-all"), nil, nil, opts...); err != nil {
		return err
	}
	return a.signedOut(ctx, SignedOut)
}

// Me returns the signed-in user with their sign-in methods.
func (a *Auth) Me(ctx context.Context, opts ...RequestOption) (*Me, error) {
	var me Me
	if _, err := a.c.Do(ctx, "GET", path("_auth", "me"), nil, &me, opts...); err != nil {
		return nil, err
	}
	return &me, nil
}

// Org returns the organization the session is scoped to, or nil.
func (a *Auth) Org(ctx context.Context, opts ...RequestOption) (*MeOrg, error) {
	me, err := a.Me(ctx, opts...)
	if err != nil {
		return nil, err
	}
	if me.Org != nil && me.Org.Name != "" && a.c.org != "" {
		_ = a.remember(ctx, map[string]any{"name": me.Org.Name, "org": me.Org.ID})
	}
	return me.Org, nil
}

// UpdateMe changes the signed-in user's own settings: today their language ("locale"), which must
// be one the realm lists.
func (a *Auth) UpdateMe(ctx context.Context, changes map[string]any, opts ...RequestOption) (*User, error) {
	var u User
	if _, err := a.c.Do(ctx, "PATCH", path("_auth", "me"), changes, &u, opts...); err != nil {
		return nil, err
	}
	return &u, nil
}

// DeleteAccount deactivates the signed-in user's account (it can't sign in; its data is kept). It
// needs a recent proof (Reauth).
func (a *Auth) DeleteAccount(ctx context.Context, opts ...RequestOption) error {
	if _, err := a.c.Do(ctx, "DELETE", path("_auth", "me"), nil, nil, append(opts, WithReauth())...); err != nil {
		return err
	}
	return a.signedOut(ctx, SignedOut)
}

// ChangePassword sets a new password for the signed-in user (or the first one, for an account that
// signs in with a provider). It needs a recent proof (Reauth).
func (a *Auth) ChangePassword(ctx context.Context, newPassword string, opts ...RequestOption) error {
	_, err := a.c.Do(ctx, "POST", path("_auth", "password"), body{"new_password": newPassword}, nil, append(opts, WithReauth())...)
	return err
}

// RemovePassword removes the password, leaving the other ways of signing in. It needs a recent proof.
func (a *Auth) RemovePassword(ctx context.Context, opts ...RequestOption) error {
	_, err := a.c.Do(ctx, "DELETE", path("_auth", "password"), nil, nil, append(opts, WithReauth())...)
	return err
}

// Sessions lists the signed-in user's sessions.
func (a *Auth) Sessions(ctx context.Context, opts ...RequestOption) ([]SessionInfo, error) {
	var out struct {
		Items []SessionInfo `json:"items"`
	}
	if _, err := a.c.Do(ctx, "GET", path("_auth", "sessions"), nil, &out, opts...); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// RevokeSession ends one of the user's sessions by id.
func (a *Auth) RevokeSession(ctx context.Context, id string, opts ...RequestOption) error {
	_, err := a.c.Do(ctx, "DELETE", path("_auth", "sessions", id), nil, nil, opts...)
	return err
}

// Token returns the stored session token, or "" when signed out.
func (a *Auth) Token(ctx context.Context) (string, error) { return a.c.store.Get(ctx) }

// HasSession reports whether a session token is stored (it may have expired).
func (a *Auth) HasSession(ctx context.Context) (bool, error) {
	t, err := a.c.store.Get(ctx)
	return t != "", err
}

// Current returns the session of the last sign-in made by this client, or nil.
func (a *Auth) Current() *Session {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.current
}

// OnChange registers f to be called on every sign-in, sign-out and expiry, and returns the
// function that removes it. f runs on the goroutine that made the change; keep it short.
func (a *Auth) OnChange(f func(AuthChange)) (stop func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	id := a.nextID
	a.nextID++
	a.listeners[id] = f
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		delete(a.listeners, id)
	}
}

func (a *Auth) emit(ch AuthChange) {
	a.mu.Lock()
	fs := make([]func(AuthChange), 0, len(a.listeners))
	for _, f := range a.listeners {
		fs = append(fs, f)
	}
	a.mu.Unlock()
	for _, f := range fs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("backd: an Auth.OnChange listener panicked", "panic", r)
				}
			}()
			f(ch)
		}()
	}
}

func (a *Auth) signedIn(ctx context.Context, s *Session) error {
	a.c.proof.Store(nil)
	a.c.credentialsChanged() // what was read as someone else (or no one) isn't theirs
	if err := a.c.store.Set(ctx, s.Token); err != nil {
		return err
	}
	a.mu.Lock()
	a.current = s
	a.mu.Unlock()
	_ = a.remember(ctx, map[string]any{"org": s.Org, "expires_at": s.ExpiresAt})
	a.emit(AuthChange{Event: SignedIn, Session: s})
	return nil
}

func (a *Auth) signedOut(ctx context.Context, ev AuthEvent) error {
	a.c.proof.Store(nil)
	a.c.credentialsChanged()
	err := a.c.store.Remove(ctx)
	a.mu.Lock()
	a.current = nil
	a.mu.Unlock()
	if slots, ok := a.c.rootClient().store.(SlotStorage); ok && a.c.org != "" {
		_ = slots.Slot("meta:" + a.c.org).Remove(ctx)
	}
	a.emit(AuthChange{Event: ev})
	return err
}

// remember keeps what the list of organizations needs to show a signed-in organization (its name
// and when the session ends) in a slot of the storage.
func (a *Auth) remember(ctx context.Context, fields map[string]any) error {
	slots, ok := a.c.rootClient().store.(SlotStorage)
	if !ok || a.c.org == "" {
		return nil
	}
	note := slots.Slot("meta:" + a.c.org)
	held := map[string]any{}
	if raw, _ := note.Get(ctx); raw != "" {
		_ = json.Unmarshal([]byte(raw), &held)
	}
	for k, v := range fields {
		if s, isString := v.(string); (isString && s == "") || v == nil {
			continue
		}
		held[k] = v
	}
	b, _ := json.Marshal(held)
	return note.Set(ctx, string(b))
}

// MFA is the second factor (an authenticator app) of the signed-in user: c.Auth().MFA.
type MFA struct{ a *Auth }

// MFAStatus says where the user stands with the second factor.
type MFAStatus struct {
	Available         bool     `json:"available"`
	Enabled           bool     `json:"enabled"`
	Methods           []string `json:"methods"`
	RecoveryCodesLeft int      `json:"recovery_codes_left"`
	Required          bool     `json:"required"`
}

// Status says whether the second factor is available and enabled for the user.
func (m *MFA) Status(ctx context.Context, opts ...RequestOption) (*MFAStatus, error) {
	var out MFAStatus
	if _, err := m.a.c.Do(ctx, "GET", path("_auth", "mfa"), nil, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// MFASetup is what an authenticator app needs to be set up: show the QR code or the secret.
type MFASetup struct {
	SetupID    string `json:"setup_id"`
	Secret     string `json:"secret"`
	OtpauthURI string `json:"otpauth_uri"`
	QR         string `json:"qr"`
	ExpiresAt  string `json:"expires_at"`
}

// Setup starts setting up the authenticator app. A user who must set one up before they can sign
// in (StepEnrollment) passes the challenge; a signed-in user needs a recent proof (Reauth).
func (m *MFA) Setup(ctx context.Context, challenge string, opts ...RequestOption) (*MFASetup, error) {
	var out MFASetup
	var b any
	if challenge != "" {
		b = body{"challenge": challenge}
		opts = append([]RequestOption{WithoutAuth()}, opts...)
	} else {
		opts = append(opts, WithReauth())
	}
	if _, err := m.a.c.Do(ctx, "POST", path("_auth", "mfa", "totp", "setup"), b, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// MFAConfirmInput is the input of MFA.Confirm.
type MFAConfirmInput struct {
	SetupID, Code string
	// Challenge is the one of StepEnrollment; empty for a signed-in user.
	Challenge string
}

// MFAConfirmResult is the answer to confirming the setup.
type MFAConfirmResult struct {
	// RecoveryCodes are shown once: the user keeps them for when they lose the app.
	RecoveryCodes []string
	// Session is set when the confirmation finished a sign-in.
	Session *Session
	// Consent is set when the sign-in still has consents to accept.
	Consent *Step
}

// Confirm checks the first code of the authenticator app and turns the second factor on.
func (m *MFA) Confirm(ctx context.Context, in MFAConfirmInput, opts ...RequestOption) (*MFAConfirmResult, error) {
	b := body{"setup_id": in.SetupID, "code": in.Code}.set("challenge", in.Challenge)
	if in.Challenge != "" {
		opts = append([]RequestOption{WithoutAuth()}, opts...)
	} else {
		opts = append(opts, WithReauth())
	}
	var raw json.RawMessage
	if _, err := m.a.c.Do(ctx, "POST", path("_auth", "mfa", "totp", "confirm"), b, &raw, opts...); err != nil {
		return nil, err
	}
	var d struct {
		RecoveryCodes   []string     `json:"recovery_codes"`
		ConsentRequired bool         `json:"consent_required"`
		Challenge       string       `json:"challenge"`
		Consents        []ConsentRef `json:"consents"`
		Token           string       `json:"token"`
		SessionID       string       `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, newError(0, "invalid_response", "the answer is not the JSON expected: "+err.Error(), kBackd)
	}
	out := &MFAConfirmResult{RecoveryCodes: d.RecoveryCodes}
	switch {
	case d.ConsentRequired:
		out.Consent = &Step{Kind: StepConsent, Challenge: d.Challenge, Consents: d.Consents}
	case d.Token != "" || d.SessionID != "":
		s, err := m.a.sessionOf(ctx, raw)
		if err != nil {
			return nil, err
		}
		out.Session = s
	}
	return out, nil
}

// Disable turns the second factor off. It needs a recent proof (Reauth).
func (m *MFA) Disable(ctx context.Context, opts ...RequestOption) error {
	_, err := m.a.c.Do(ctx, "DELETE", path("_auth", "mfa", "totp"), nil, nil, append(opts, WithReauth())...)
	return err
}

// RegenerateRecoveryCodes makes a new set of recovery codes, replacing the old. It needs a recent proof (Reauth).
func (m *MFA) RegenerateRecoveryCodes(ctx context.Context, opts ...RequestOption) ([]string, error) {
	var out struct {
		Codes []string `json:"recovery_codes"`
	}
	if _, err := m.a.c.Do(ctx, "POST", path("_auth", "mfa", "recovery-codes"), nil, &out, append(opts, WithReauth())...); err != nil {
		return nil, err
	}
	return out.Codes, nil
}

// MagicLink signs in with a link sent by email: c.Auth().MagicLink.
type MagicLink struct{ a *Auth }

// MagicLinkOptions are the options of MagicLink.Request.
type MagicLinkOptions struct {
	// RedirectTo is where the link sends the person back to (within the realm's sign_in.allowed_redirects).
	RedirectTo string
	Locale     string
	Org        string
	Consents   map[string]string
}

// Request emails the person a sign-in link and a code. The PKCE verifier is kept in the OAuth
// storage: finish with CompleteSignIn on the address the link opens, or VerifyCode with the code.
// It returns the request id.
func (m *MagicLink) Request(ctx context.Context, email string, o MagicLinkOptions, opts ...RequestOption) (string, error) {
	verifier, challenge := newPKCE()
	b := body{"email": email, "code_challenge": challenge}
	b.set("redirect_to", o.RedirectTo).set("locale", o.Locale).set("consents", o.Consents)
	var out struct {
		RequestID string `json:"request_id"`
	}
	if _, err := m.a.c.Do(ctx, "POST", join(m.a.base(o.Org), "magic-link"), b, &out, append([]RequestOption{WithoutAuth()}, opts...)...); err != nil {
		return "", err
	}
	if err := savePending(ctx, m.a.c.oauth, pending{Verifier: verifier, Provider: "magic-link", Intent: "signin", RequestID: out.RequestID}); err != nil {
		return "", err
	}
	return out.RequestID, nil
}

// VerifyCode finishes a magic-link sign-in with the code from the email, in the client that asked for the link.
func (m *MagicLink) VerifyCode(ctx context.Context, code string, opts ...RequestOption) (*Step, error) {
	p := loadPending(ctx, m.a.c.oauth)
	if p == nil || p.RequestID == "" {
		return nil, newError(400, "no_request", "ask for a link first, in this client: MagicLink.Request(email)", kValidation)
	}
	var raw json.RawMessage
	b := body{"request_id": p.RequestID, "code": strings.TrimSpace(code), "code_verifier": p.Verifier}
	if _, err := m.a.c.Do(ctx, "POST", path("_auth", "magic-link", "verify"), b, &raw, append([]RequestOption{WithoutAuth()}, opts...)...); err != nil {
		return nil, err
	}
	_ = m.a.c.oauth.Remove(ctx)
	return m.a.step(ctx, raw)
}
