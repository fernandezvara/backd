package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Doer sends an HTTP request. *http.Client is one; tests and unusual deployments (a proxy, a
// signing layer) plug their own, the way backd-js takes a fetch.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Retry says how many times a call is repeated when the server answers 429 or 503. Only safe
// calls are repeated: reads, and writes that carry If-Match or an Idempotency-Key.
type Retry struct {
	// Attempts is the number of extra tries after the first; 0 turns retries off.
	Attempts int
	// MaxDelay is the longest wait between tries; 30s when zero.
	MaxDelay time.Duration
}

// Options configure a [Client]. URL and Realm are required.
type Options struct {
	// URL is the base URL of backd, such as "https://api.example.com".
	URL string
	// Realm is the realm to talk to.
	Realm string
	// APIKey is a server-side credential (bdk_…) with full access to the realm. Keep it out of anything
	// a user's device can read.
	APIKey string
	// Org binds the client to an organization, by id or by the slug people know ("acme"): sign-in
	// calls go to /_auth/orgs/{org}/…, so the session is scoped to it. See [Client.Org].
	Org string
	// Storage keeps the session token; memory by default.
	Storage TokenStorage
	// OAuthStorage keeps the pending redirect sign-in (its PKCE verifier) between its start and its end; memory by default.
	OAuthStorage OAuthStorage
	// Cache tunes the revalidation cache of document and list reads; on by default.
	Cache CacheOptions
	// ConfigStorage keeps the app configuration between runs ([Client.Config]); memory only by default.
	ConfigStorage ConfigStorage
	// Retry configures the retries for 429 and 503; off by default.
	Retry Retry
	// Headers are sent with every call.
	Headers map[string]string
	// Doer sends the requests; an *http.Client by default.
	Doer Doer
	// Types is the fingerprint of the generated types the program was written against (the
	// fingerprint field of realm.json, made by `backd types`). It is sent as X-Backd-Types; when
	// the server's config has moved on it answers X-Backd-Types-Stale and OnStaleTypes is called, once.
	// Leave it out in production: nothing is sent then.
	Types string
	// MapError, when set, is given every error [Client.Do] and the calls built on it return, and
	// returns what the caller sees: to add a hint, to turn it into the error type of a larger program.
	// What it returns should still wrap the original ([errors.Is] and [errors.As] keep working).
	MapError func(error) error
	// OnStaleTypes is called once when the server says the types are stale; a warning is logged by default.
	OnStaleTypes func()
}

// Client talks to one realm. It is safe for concurrent use.
type Client struct {
	opts   Options
	url    string
	realm  string
	apiKey string
	org    string
	doer   Doer
	retry  Retry
	store  TokenStorage

	auth          *Auth
	cache         *RevalidationCache
	fieldMu       sync.Mutex
	fields        map[string]*FileField
	config        *Config
	push          *Push
	rt            *Realtime
	life          context.Context
	stopLife      context.CancelFunc
	oauth         OAuthStorage
	credHooks     []func()
	staleReported atomic.Bool
	proof         atomic.Pointer[reauthProof]
	// expired is called when the server refuses the session token; auth sets it.
	expired func(context.Context)

	root     *Client
	mu       sync.Mutex
	orgs     map[string]*Client
	closers  []func()
	isClosed bool
}

type reauthProof struct {
	token   string
	expires time.Time
}

// New creates a client for one realm.
func New(opts Options) (*Client, error) { return newClient(opts, nil) }

func newClient(opts Options, root *Client) (*Client, error) {
	if opts.URL == "" {
		return nil, errors.New("client.New: URL is required")
	}
	if opts.Realm == "" {
		return nil, errors.New("client.New: Realm is required")
	}
	if _, err := url.ParseRequestURI(opts.URL); err != nil {
		return nil, errors.New("client.New: URL is not a valid address: " + err.Error())
	}
	c := &Client{
		opts:   opts,
		url:    strings.TrimRight(opts.URL, "/"),
		realm:  opts.Realm,
		apiKey: opts.APIKey,
		org:    NormalizeOrg(opts.Org),
		doer:   opts.Doer,
		retry:  opts.Retry,
		store:  opts.Storage,
		root:   root,
	}
	if c.doer == nil {
		c.doer = &http.Client{}
	}
	if c.store == nil {
		c.store = NewMemoryStorage()
	}
	if c.oauth = opts.OAuthStorage; c.oauth == nil {
		c.oauth = &MemoryOAuthStorage{}
	}
	if root != nil {
		c.life = root.life
	} else {
		c.life, c.stopLife = context.WithCancel(context.Background())
	}
	if !opts.Cache.Disable {
		c.cache = NewRevalidationCache(opts.Cache)
	}
	c.OnCredentialChange(c.ClearCache) // what was read as someone else (or no one) isn't theirs
	c.auth = newAuth(c)
	c.config = newConfig(c, opts.ConfigStorage)
	c.push = newPush(c)
	c.rt = newRealtime(c)
	return c, nil
}

// NormalizeOrg is an organization as the server compares it: case and surrounding spaces do not matter.
func NormalizeOrg(ref string) string { return strings.ToLower(strings.TrimSpace(ref)) }

// URL returns the base URL of the server, without a trailing slash.
func (c *Client) URL() string { return c.url }

// Realm returns the realm the client talks to.
func (c *Client) Realm() string { return c.realm }

// OrgRef returns the organization the client is bound to, as it was given, or "".
func (c *Client) OrgRef() string { return c.org }

// HasAPIKey reports whether the client was created with an API key.
func (c *Client) HasAPIKey() bool { return c.apiKey != "" }

// Config returns the realm's app configuration: flags, switches and limits, cached per user.
func (c *Client) Config() *Config { return c.config }

// Auth returns the sign-up, sign-in and session calls.
func (c *Client) Auth() *Auth { return c.auth }

// OnCredentialChange registers f to run when the signed-in caller changes (a sign-in, a sign-out,
// an expired session): whatever was read as the previous caller is not the new one's. The caches use it.
func (c *Client) OnCredentialChange(f func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.credHooks = append(c.credHooks, f)
}

func (c *Client) credentialsChanged() {
	c.mu.Lock()
	hooks := append([]func(){}, c.credHooks...)
	c.mu.Unlock()
	for _, f := range hooks {
		f()
	}
}

// Storage returns where the client keeps its session token.
func (c *Client) Storage() TokenStorage { return c.store }

func (c *Client) rootClient() *Client {
	if c.root != nil {
		return c.root
	}
	return c
}

// Org returns the client of one organization, for a program that is signed in to several at once:
// it has the same address, realm and settings and a token of its own, so its calls reach that
// organization and no other, and signing out of it leaves the others signed in. The organization
// is its id or its slug; asking again for the same gives the same client. It needs a storage that
// can keep a token per organization ([MemoryStorage] and [FileStorage] can).
func (c *Client) Org(ref string) (*Client, error) {
	key := NormalizeOrg(ref)
	if key == "" {
		return nil, errors.New("client.Org: the organization (an id or a slug) is required")
	}
	root := c.rootClient()
	if root.org == key {
		return root, nil
	}
	root.mu.Lock()
	oc, ok := root.orgs[key]
	root.mu.Unlock()
	if ok {
		return oc, nil
	}
	slots, ok := root.store.(SlotStorage)
	if !ok {
		return nil, errors.New("client.Org: this storage cannot keep a token per organization; use MemoryStorage or FileStorage")
	}
	o := root.opts
	o.Org, o.Storage = key, slots.Slot(key)
	made, err := newClient(o, root) // takes the root's lock to register what it closes
	if err != nil {
		return nil, err
	}
	root.mu.Lock()
	defer root.mu.Unlock()
	if oc, ok := root.orgs[key]; ok { // another goroutine made it first
		return oc, nil
	}
	if root.orgs == nil {
		root.orgs = map[string]*Client{}
	}
	root.orgs[key] = made
	return made, nil
}

// OnClose registers f to run when the client is closed; realtime streams use it to end.
func (c *Client) OnClose(f func()) {
	r := c.rootClient()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.isClosed {
		go f()
		return
	}
	r.closers = append(r.closers, f)
}

// Close ends what the client keeps running (live views) and releases idle connections. The client
// can't be used for live views afterwards. It is safe to call twice.
func (c *Client) Close() error {
	r := c.rootClient()
	r.mu.Lock()
	if r.isClosed {
		r.mu.Unlock()
		return nil
	}
	r.isClosed = true
	r.stopLife()
	closers := r.closers
	r.closers = nil
	r.mu.Unlock()
	for _, f := range closers {
		f()
	}
	if h, ok := r.doer.(interface{ CloseIdleConnections() }); ok {
		h.CloseIdleConnections()
	}
	return nil
}
