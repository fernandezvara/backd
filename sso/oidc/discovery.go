package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/fernandezvara/backd/sso"
	"github.com/fernandezvara/backd/sso/safehttp"
)

// Document is what a provider's discovery document says that backd uses.
type Document struct {
	Issuer        string
	Authorization string
	Token         string
	JWKS          string
	// Hosts are the issuer's host and the endpoints' hosts, which share its registered domain.
	Hosts []string
}

// Discoverer reads and caches the discovery documents of generic OpenID Connect providers
// (<issuer>/.well-known/openid-configuration). The zero value needs HTTP set.
type Discoverer struct {
	// HTTP reaches the issuers: safehttp.NewClient(…) in production.
	HTTP *http.Client
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// Dev allows http issuers and endpoints (BACKD_DEV); otherwise https only.
	Dev bool

	mu    sync.Mutex
	cache map[string]*docEntry // by issuer
}

type docEntry struct {
	doc     Document
	expires time.Time
}

func (d *Discoverer) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// IssuerProblem says what is wrong with an issuer URL, or "" when it is fine: https (http only in
// development), a host, and no credentials, query or fragment.
func IssuerProblem(issuer string, dev bool) string {
	u, err := url.Parse(issuer)
	switch {
	case err != nil || u.Host == "" || u.Hostname() == "":
		return fmt.Sprintf("%q isn't a URL such as https://sso.example.com/realms/acme", issuer)
	case u.Scheme != "https" && (!dev || u.Scheme != "http"):
		return fmt.Sprintf("%q must be an https URL", issuer)
	case u.User != nil || u.RawQuery != "" || u.Fragment != "":
		return fmt.Sprintf("%q must have no credentials, query or fragment", issuer)
	}
	return ""
}

// Discover returns the provider's endpoints. The issuer's host is the only one reached to read the
// document; the endpoints it names must be on the issuer's registered domain, or the provider is
// refused (a document must not send backd to a host the operator did not name).
func (d *Discoverer) Discover(ctx context.Context, issuer string, private bool) (Document, error) {
	if msg := IssuerProblem(issuer, d.Dev); msg != "" {
		return Document{}, fmt.Errorf("%w: %s", sso.ErrUnavailable, msg)
	}
	now := d.now()
	d.mu.Lock()
	e := d.cache[issuer]
	d.mu.Unlock()
	if e != nil && now.Before(e.expires) {
		return e.doc, nil
	}
	doc, ttl, err := d.fetch(ctx, issuer, private)
	if err != nil {
		if e != nil {
			return e.doc, nil // keep working on what is known while the issuer is unreachable
		}
		return Document{}, err
	}
	d.mu.Lock()
	if d.cache == nil {
		d.cache = map[string]*docEntry{}
	}
	d.cache[issuer] = &docEntry{doc: doc, expires: now.Add(ttl)}
	d.mu.Unlock()
	return doc, nil
}

func (d *Discoverer) fetch(ctx context.Context, issuer string, private bool) (Document, time.Duration, error) {
	unavailable := func(format string, args ...any) (Document, time.Duration, error) {
		return Document{}, 0, fmt.Errorf("%w: %s", sso.ErrUnavailable, fmt.Sprintf(format, args...))
	}
	iu, _ := url.Parse(issuer)
	ctx = safehttp.ForProvider(ctx, private, strings.ToLower(iu.Hostname()))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return unavailable("the discovery address: %v", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return unavailable("the discovery document of %s: %v", issuer, err)
	}
	defer resp.Body.Close()
	data, err := safehttp.ReadBody(resp)
	if err != nil || resp.StatusCode != http.StatusOK {
		return unavailable("the discovery document of %s answered %d", issuer, resp.StatusCode)
	}
	var raw struct {
		Issuer        string   `json:"issuer"`
		Authorization string   `json:"authorization_endpoint"`
		Token         string   `json:"token_endpoint"`
		JWKS          string   `json:"jwks_uri"`
		PKCE          []string `json:"code_challenge_methods_supported"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return unavailable("the discovery document of %s isn't JSON", issuer)
	}
	if raw.Issuer != issuer {
		return unavailable("the discovery document names the issuer %q, not %q", raw.Issuer, issuer)
	}
	if raw.PKCE != nil && !slices.Contains(raw.PKCE, "S256") {
		return unavailable("%s doesn't support S256 PKCE", issuer)
	}
	doc := Document{Issuer: raw.Issuer, Authorization: raw.Authorization, Token: raw.Token, JWKS: raw.JWKS, Hosts: []string{strings.ToLower(iu.Hostname())}}
	for name, endpoint := range map[string]string{"authorization_endpoint": raw.Authorization, "token_endpoint": raw.Token, "jwks_uri": raw.JWKS} {
		eu, err := url.Parse(endpoint)
		switch {
		case endpoint == "":
			return unavailable("the discovery document of %s has no %s", issuer, name)
		case err != nil || eu.Hostname() == "" || eu.User != nil || (eu.Scheme != "https" && (!d.Dev || eu.Scheme != "http")):
			return unavailable("the %s of %s isn't an https URL", name, issuer)
		case !sameSite(iu.Hostname(), eu.Hostname()):
			return unavailable("the %s of %s is on %s, outside the issuer's domain", name, issuer, eu.Hostname())
		}
		if h := strings.ToLower(eu.Hostname()); !slices.Contains(doc.Hosts, h) {
			doc.Hosts = append(doc.Hosts, h)
		}
	}
	slices.Sort(doc.Hosts)
	return doc, cacheTTL(resp.Header.Get("Cache-Control")), nil
}

// sameSite says whether two hosts are the same or share a registered domain (login.acme.example
// and api.acme.example); an address, or a name with no registered domain, only matches itself.
func sameSite(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	if a == b {
		return true
	}
	if net.ParseIP(a) != nil || net.ParseIP(b) != nil {
		return false
	}
	ra, err := publicsuffix.EffectiveTLDPlusOne(a)
	if err != nil {
		return false
	}
	rb, err := publicsuffix.EffectiveTLDPlusOne(b)
	return err == nil && ra == rb
}
