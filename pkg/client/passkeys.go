package client

import (
	"context"
	"encoding/json"
)

// Passkeys are the signed-in user's passkeys, and the calls of the ceremonies: c.Auth().Passkeys.
//
// A passkey is made and used by an authenticator (a phone, a security key, a password manager), which a
// program does not have, so there is no SignIn here: the browser or the app does the ceremony. What this
// gives is the plain calls around it: list, rename and remove, and the options and verify calls of each
// ceremony, so a test (with a software authenticator) or an app with its own platform code can drive the
// server. The options are the JSON of the WebAuthn standard (`{"publicKey": …}`), and the credential
// the authenticator answers is the JSON of the PublicKeyCredential; both pass through untouched.
type Passkeys struct{ a *Auth }

// Passkey is a registered passkey. Never the key material.
type Passkey struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	CreatedAt      string   `json:"created_at"`
	LastUsedAt     *string  `json:"last_used_at"`
	Transports     []string `json:"transports"`
	BackupEligible bool     `json:"backup_eligible"`
	BackupState    bool     `json:"backup_state"`
}

// PasskeyCeremony is the start of a ceremony: the options for the authenticator and, for the calls
// that keep one, the id to send back with the credential.
type PasskeyCeremony struct {
	ChallengeID string          `json:"challenge_id"`
	Strategy    string          `json:"strategy"`
	Options     json.RawMessage `json:"options"`
}

// List returns the user's passkeys.
func (p *Passkeys) List(ctx context.Context, opts ...RequestOption) ([]Passkey, error) {
	var out struct {
		Items []Passkey `json:"items"`
	}
	if _, err := p.a.c.Do(ctx, "GET", path("_auth", "passkeys"), nil, &out, opts...); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// Rename gives a passkey another name.
func (p *Passkeys) Rename(ctx context.Context, id, name string, opts ...RequestOption) (*Passkey, error) {
	var out Passkey
	if _, err := p.a.c.Do(ctx, "PATCH", path("_auth", "passkeys", id), body{"name": name}, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Remove deletes a passkey (and ends the sessions it opened). It needs a recent proof (Reauth);
// credential_required (a ConflictError) when a strategy of the user needs it and nothing replaces it.
func (p *Passkeys) Remove(ctx context.Context, id string, opts ...RequestOption) error {
	_, err := p.a.c.Do(ctx, "DELETE", path("_auth", "passkeys", id), nil, nil, append(opts, WithReauth())...)
	return err
}

// RegisterOptions starts adding a passkey: the options for navigator.credentials.create (or the
// platform's equivalent). It needs a recent proof (Reauth).
func (p *Passkeys) RegisterOptions(ctx context.Context, opts ...RequestOption) (*PasskeyCeremony, error) {
	var out PasskeyCeremony
	if _, err := p.a.c.Do(ctx, "POST", path("_auth", "passkeys", "register", "options"), body{}, &out, append(opts, WithReauth())...); err != nil {
		return nil, err
	}
	return &out, nil
}

// RegisterVerify finishes adding a passkey with the credential the authenticator made; name is what the
// user calls it (empty: "Passkey"). It needs a recent proof (Reauth).
func (p *Passkeys) RegisterVerify(ctx context.Context, challengeID string, credential json.RawMessage, name string, opts ...RequestOption) (*Passkey, error) {
	b := body{"challenge_id": challengeID, "credential": credential}.set("name", name)
	var out Passkey
	if _, err := p.a.c.Do(ctx, "POST", path("_auth", "passkeys", "register", "verify"), b, &out, append(opts, WithReauth())...); err != nil {
		return nil, err
	}
	return &out, nil
}

// PasskeySignInInput names the strategy (needed when more than one starts with a passkey) and, to sign
// in to an organization, the organization.
type PasskeySignInInput struct {
	Strategy string
	Org      string
}

// SignInOptions starts signing in with a passkey as the first step of a strategy: the options for
// navigator.credentials.get, with no allow-list (the authenticator offers what it holds).
func (p *Passkeys) SignInOptions(ctx context.Context, in PasskeySignInInput, opts ...RequestOption) (*PasskeyCeremony, error) {
	var out PasskeyCeremony
	b := body{}.set("strategy", in.Strategy)
	if _, err := p.a.c.Do(ctx, "POST", join(p.a.base(in.Org), "passkey", "login", "options"), b, &out, append([]RequestOption{WithoutAuth()}, opts...)...); err != nil {
		return nil, err
	}
	return &out, nil
}

// SignInVerify finishes it with the credential the authenticator signed: the session (the client holds
// it) or the next step of the strategy. Every failure is invalid_passkey.
func (p *Passkeys) SignInVerify(ctx context.Context, org, challengeID string, credential json.RawMessage, opts ...RequestOption) (*Step, error) {
	var raw json.RawMessage
	b := body{"challenge_id": challengeID, "credential": credential}
	if _, err := p.a.c.Do(ctx, "POST", join(p.a.base(org), "passkey", "login", "verify"), b, &raw, append([]RequestOption{WithoutAuth()}, opts...)...); err != nil {
		return nil, err
	}
	return p.a.step(ctx, raw)
}

// StepOptions gives the options for the passkey a sign-in challenge asks for next (Step.Next is "passkey").
func (p *Passkeys) StepOptions(ctx context.Context, challenge string, opts ...RequestOption) (*PasskeyCeremony, error) {
	var out PasskeyCeremony
	if _, err := p.a.c.Do(ctx, "POST", path("_auth", "mfa", "passkey", "options"), body{"challenge": challenge}, &out, append([]RequestOption{WithoutAuth()}, opts...)...); err != nil {
		return nil, err
	}
	return &out, nil
}

// VerifyStep answers a passkey step with the credential the authenticator signed: the session or the
// next step.
func (p *Passkeys) VerifyStep(ctx context.Context, challenge string, credential json.RawMessage, opts ...RequestOption) (*Step, error) {
	var raw json.RawMessage
	b := body{"challenge": challenge, "method": "passkey", "credential": credential}
	if _, err := p.a.c.Do(ctx, "POST", path("_auth", "mfa", "verify"), b, &raw, append([]RequestOption{WithoutAuth()}, opts...)...); err != nil {
		return nil, err
	}
	return p.a.step(ctx, raw)
}

// EnrollOptions starts adding the first passkey during a sign-in whose strategy needs one
// (Step.Kind is StepEnrollment and Step.Next is "passkey").
func (p *Passkeys) EnrollOptions(ctx context.Context, challenge string, opts ...RequestOption) (*PasskeyCeremony, error) {
	var out PasskeyCeremony
	if _, err := p.a.c.Do(ctx, "POST", path("_auth", "mfa", "passkey", "register", "options"), body{"challenge": challenge}, &out, append([]RequestOption{WithoutAuth()}, opts...)...); err != nil {
		return nil, err
	}
	return &out, nil
}

// EnrollVerify finishes it with the credential the authenticator made: the session or the next step.
func (p *Passkeys) EnrollVerify(ctx context.Context, challenge string, credential json.RawMessage, name string, opts ...RequestOption) (*Step, error) {
	var raw json.RawMessage
	b := body{"challenge": challenge, "credential": credential}.set("name", name)
	if _, err := p.a.c.Do(ctx, "POST", path("_auth", "mfa", "passkey", "register", "verify"), b, &raw, append([]RequestOption{WithoutAuth()}, opts...)...); err != nil {
		return nil, err
	}
	return p.a.step(ctx, raw)
}

// ReauthOptions gives the options for proving who you are with a passkey; the credential goes to
// Auth.Reauth as ReauthInput{Method: "passkey", Credential: …}.
func (p *Passkeys) ReauthOptions(ctx context.Context, opts ...RequestOption) (*PasskeyCeremony, error) {
	var out PasskeyCeremony
	if _, err := p.a.c.Do(ctx, "POST", path("_auth", "reauth", "passkey", "options"), body{}, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}
