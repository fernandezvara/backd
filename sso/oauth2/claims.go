package oauth2

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/fernandezvara/backd/sso"
	"github.com/fernandezvara/backd/sso/safehttp"
)

// Who reads the person an access token stands for. There is no ID token to verify (no signature, no
// nonce): the answer comes over TLS from the provider's own hosts, once, and the access token is
// dropped by the caller. GitHub's answers are read as GitHub gives them; a plain OAuth 2 service's
// through the field names the operator configured (design/sso.md §4.2).
func (s *Client) Who(ctx context.Context, p sso.Provider, accessToken string) (sso.Claims, error) {
	if accessToken == "" {
		return sso.Claims{}, fmt.Errorf("%w: no access token", sso.ErrInvalidToken)
	}
	switch p.Kind {
	case sso.GitHub:
		return s.gitHub(ctx, p, accessToken)
	case sso.OAuth2:
		return s.generic(ctx, p, accessToken)
	}
	return sso.Claims{}, fmt.Errorf("%w: %s has no userinfo", sso.ErrInvalidToken, p.Kind)
}

// get reads a JSON answer, at most 1 MiB, with the access token as a bearer.
func (s *Client) get(ctx context.Context, endpoint, accessToken string) (map[string]any, []any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", sso.ErrUnavailable, err)
	}
	defer resp.Body.Close()
	data, err := safehttp.ReadBody(resp)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", sso.ErrUnavailable, err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, nil, fmt.Errorf("%w: the provider refused the access token", sso.ErrInvalidToken)
	case resp.StatusCode != http.StatusOK:
		return nil, nil, fmt.Errorf("%w: userinfo answered %d", sso.ErrUnavailable, resp.StatusCode)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber() // an id is a number: read it as written, never through a float
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, nil, fmt.Errorf("%w: userinfo isn't JSON", sso.ErrUnavailable)
	}
	switch v := v.(type) {
	case nil:
		return nil, nil, nil // an empty list may be written null
	case map[string]any:
		return v, nil, nil
	case []any:
		return nil, v, nil
	}
	return nil, nil, fmt.Errorf("%w: userinfo isn't an object", sso.ErrUnavailable)
}

// text reads a field as text: a string, or a whole number written as is. Anything else is "".
func text(v any) string {
	switch v := v.(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		if s := v.String(); !strings.ContainsAny(s, ".eE") {
			return s
		}
	}
	return ""
}

func truth(v any) bool {
	switch v := v.(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}

// path reads a field, or a dotted path such as data.user.id, of a decoded object.
func path(obj map[string]any, name string) any {
	var cur any = obj
	for _, part := range strings.Split(name, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		if cur, ok = m[part]; !ok {
			return nil
		}
	}
	return cur
}

// gitHub reads /user for the numeric id (never the login, which can be renamed and taken by
// someone else) and /user/emails for the primary address GitHub has verified.
func (s *Client) gitHub(ctx context.Context, p sso.Provider, accessToken string) (sso.Claims, error) {
	user, _, err := s.get(ctx, p.Endpoints.UserInfo, accessToken)
	if err != nil {
		return sso.Claims{}, err
	}
	id := text(user["id"])
	if id == "" {
		return sso.Claims{}, fmt.Errorf("%w: GitHub gave no user id", sso.ErrInvalidToken)
	}
	c := sso.Claims{Issuer: p.Issuer, Subject: id, Name: text(user["name"]), Picture: text(user["avatar_url"])}
	if c.Name == "" {
		c.Name = text(user["login"])
	}
	_, list, err := s.get(ctx, p.Endpoints.Emails, accessToken)
	if err != nil {
		return sso.Claims{}, err
	}
	for _, e := range list {
		m, _ := e.(map[string]any)
		if m != nil && truth(m["primary"]) && truth(m["verified"]) {
			c.Email, c.EmailVerified = text(m["email"]), true
			break
		}
	}
	return c, nil
}

// generic reads the userinfo of a plain OAuth 2 service through the configured field names. An
// address counts as verified only when a verified flag is mapped and true.
func (s *Client) generic(ctx context.Context, p sso.Provider, accessToken string) (sso.Claims, error) {
	obj, _, err := s.get(ctx, p.Endpoints.UserInfo, accessToken)
	if err != nil {
		return sso.Claims{}, err
	}
	field := func(key string) any {
		if name := p.Claims[key]; name != "" {
			return path(obj, name)
		}
		return nil
	}
	c := sso.Claims{
		Issuer: p.Issuer, Subject: text(field("subject")), Email: text(field("email")), Name: text(field("name")),
		GivenName: text(field("given_name")), FamilyName: text(field("family_name")), Picture: text(field("picture")),
	}
	if c.Subject == "" {
		return sso.Claims{}, fmt.Errorf("%w: the userinfo has no %q", sso.ErrInvalidToken, p.Claims["subject"])
	}
	c.EmailVerified = c.Email != "" && p.Claims["email_verified"] != "" && truth(field("email_verified"))
	return c, nil
}
