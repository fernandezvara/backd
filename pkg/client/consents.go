package client

import (
	"context"
	"net/http"
	"net/url"
)

// Consents are the consents the realm asks for, and what the signed-in user holds: c.Consents().
type Consents struct{ c *Client }

// Consents returns the realm's consents: their texts, and what the signed-in user holds.
func (c *Client) Consents() *Consents { return &Consents{c: c} }

// ConsentInfo is a consent the realm asks for.
type ConsentInfo struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Current  struct {
		Version  string `json:"version"`
		Reaccept bool   `json:"reaccept"`
		// ReacceptBy is the date by which the new version has to be accepted again.
		ReacceptBy string `json:"reaccept_by,omitempty"`
	} `json:"current"`
	Locales []string `json:"locales"`
}

// ConsentText is the text of a consent version in a language.
type ConsentText struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Locale  string `json:"locale"`
	// Text is Markdown.
	Text string `json:"text"`
	// Hash is the SHA-256 of the text, kept with each acceptance.
	Hash string `json:"hash"`
	// Current: the version in force.
	Current bool `json:"current"`
}

// ConsentState is what the signed-in user holds.
type ConsentState struct {
	// Consents maps each consent accepted to its version.
	Consents map[string]string `json:"consents"`
	// ToAccept are the required consents still to accept, at the version to show.
	ToAccept []ConsentRef `json:"to_accept"`
}

// ConsentEvent is one entry of the history of a user's consents.
type ConsentEvent struct {
	Consent string `json:"consent"`
	Version string `json:"version"`
	Action  string `json:"action"` // "accepted" or "withdrawn"
	Method  string `json:"method"` // "signup", "prompt", "api" or "admin"
	Locale  string `json:"locale"`
	At      string `json:"at"`
}

// ConsentHistory is the state with the history.
type ConsentHistory struct {
	ConsentState
	History []ConsentEvent `json:"history"`
}

// Current lists the consents the realm asks for, with the version in force. It needs no sign-in.
func (s *Consents) Current(ctx context.Context, opts ...RequestOption) ([]ConsentInfo, error) {
	var out struct {
		Items []ConsentInfo `json:"items"`
	}
	if _, err := s.c.Do(ctx, http.MethodGet, []string{"_consents"}, nil, &out, append([]RequestOption{WithoutAuth()}, opts...)...); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// Text returns the text of a consent version in a language (the realm's default when locale is empty).
func (s *Consents) Text(ctx context.Context, name, version, locale string, opts ...RequestOption) (*ConsentText, error) {
	q := url.Values{}
	if locale != "" {
		q.Set("locale", locale)
	}
	var out ConsentText
	if _, err := s.c.Do(ctx, http.MethodGet, []string{"_consents", name, version}, nil, &out, append([]RequestOption{WithoutAuth(), WithQuery(q)}, opts...)...); err != nil {
		return nil, err
	}
	return &out, nil
}

// State returns what the signed-in user holds, with the history of acceptances and withdrawals.
func (s *Consents) State(ctx context.Context, opts ...RequestOption) (*ConsentHistory, error) {
	var out ConsentHistory
	if _, err := s.c.Do(ctx, http.MethodGet, []string{"_auth", "consents"}, nil, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Accept records the acceptance of consents (name to version) by the signed-in user.
func (s *Consents) Accept(ctx context.Context, consents map[string]string, opts ...RequestOption) (*ConsentState, error) {
	var out ConsentState
	if _, err := s.c.Do(ctx, http.MethodPost, []string{"_auth", "consents"}, map[string]any{"consents": consents}, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Withdraw records the withdrawal of a consent; a required one can't be withdrawn.
func (s *Consents) Withdraw(ctx context.Context, name string, opts ...RequestOption) (*ConsentState, error) {
	var out ConsentState
	if _, err := s.c.Do(ctx, http.MethodDelete, []string{"_auth", "consents", name}, nil, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}
