package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/appconfig"
	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/query"
	"github.com/fernandezvara/backd/internal/realtime"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/rules"
	"github.com/fernandezvara/backd/internal/storage"
)

// Realtime error codes (design/triggers-and-realtime.md §17.1).
const (
	codeRealtimeNotEnabled   = "realtime_not_enabled"
	codeTooManySubscriptions = "too_many_subscriptions"
	codeInvalidResumeToken   = "invalid_resume_token"
	codeRealtimeUnavailable  = "realtime_unavailable"
)

// realtimeRequest is the body of POST /v1/{realm}/_realtime.
type realtimeRequest struct {
	Subscriptions []realtimeSubscription `json:"subscriptions"`
	ResumeFrom    string                 `json:"resume_from"`
}

type realtimeSubscription struct {
	ID         string         `json:"id"`
	DB         string         `json:"db"`
	Collection string         `json:"collection"`
	Where      map[string]any `json:"where"`
	Doc        string         `json:"doc"`
	// Config follows the realm's app configuration instead of a collection.
	Config *realtimeConfigSub `json:"config"`
}

// realtimeConfigSub is a `_config` subscription: the branches to follow (default: everything
// the caller may read).
type realtimeConfigSub struct {
	Paths []string `json:"paths"`
}

// maxRealtimeBody bounds a subscribe request: twenty subscriptions of small filters.
const maxRealtimeBody = 64 << 10

// resumeKey is the key that signs a realm's resume tokens: derived per realm from the
// instance's callback key, which every instance of a deployment shares, so any of them
// verifies a token another made, and nothing has to be stored. Without a callback key
// (development) it comes from a random key that lives as long as the process.
func (d *documents) resumeKey(realm string) []byte {
	base := d.callbackKey
	if len(base) == 0 {
		base = d.processKey()
	}
	h := hmac.New(sha256.New, base)
	h.Write([]byte("backd:realtime-resume:" + realm))
	return h.Sum(nil)
}

// realtime opens a stream of the changes the caller may see.
func (d *documents) realtime(w http.ResponseWriter, r *http.Request) {
	realm := chi.URLParam(r, "realm")
	rl, ok := d.reg.Realms[realm]
	if !ok {
		notFound(w, r)
		return
	}
	if d.hub == nil {
		writeError(w, r, http.StatusServiceUnavailable, codeRealtimeUnavailable, "this instance does not serve realtime subscriptions (BACKD_EVENTS=false)")
		return
	}
	caller, hasAuth, ok := d.identify(w, r, realm)
	if !ok {
		return
	}
	if hasAuth {
		r = r.WithContext(context.WithValue(r.Context(), callerKey{}, caller))
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRealtimeBody+1))
	if err != nil || len(body) > maxRealtimeBody {
		writeError(w, r, http.StatusBadRequest, codeInvalidJSON, "the request body is missing or too large")
		return
	}
	var req realtimeRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidJSON, "request body is not valid JSON: "+err.Error())
		return
	}
	if req.ResumeFrom == "" {
		req.ResumeFrom = r.Header.Get("Last-Event-ID")
	}
	lim := d.realtimeLimits
	if rl.Settings.Realtime.MaxSubscriptionsPerStream > 0 {
		lim.SubscriptionsPerStream = min(lim.OrDefault().SubscriptionsPerStream, rl.Settings.Realtime.MaxSubscriptionsPerStream)
	}
	if len(req.Subscriptions) == 0 {
		writeError(w, r, http.StatusBadRequest, codeInvalidQuery, "subscriptions must name at least one collection", Detail{Path: "subscriptions", Reason: "required"})
		return
	}
	if max := lim.OrDefault().SubscriptionsPerStream; len(req.Subscriptions) > max {
		writeError(w, r, http.StatusBadRequest, codeTooManySubscriptions, "a stream may have at most "+strconv.Itoa(max)+" subscriptions")
		return
	}

	subs := make([]*realtime.Subscription, 0, len(req.Subscriptions))
	var built []*builtSub
	var cfgSub *realtime.ConfigSub
	seen := map[string]bool{}
	for i, s := range req.Subscriptions {
		at := "subscriptions[" + strconv.Itoa(i) + "]"
		bad := func(path, reason string) {
			writeError(w, r, http.StatusBadRequest, codeInvalidQuery, "invalid subscription", Detail{Path: at + "." + path, Reason: reason})
		}
		if s.ID == "" || len(s.ID) > 64 || seen[s.ID] {
			bad("id", "must be a unique string of 1 to 64 characters")
			return
		}
		seen[s.ID] = true
		if s.Config != nil {
			if s.DB != "" || s.Collection != "" || s.Doc != "" || s.Where != nil {
				bad("config", "cannot be combined with a collection")
				return
			}
			if cfgSub != nil {
				bad("config", "a stream follows the configuration once")
				return
			}
			var problem string
			if cfgSub, problem = d.configSub(r, realm, caller, hasAuth, s.ID, s.Config.Paths); cfgSub == nil {
				bad("config", problem)
				return
			}
			continue
		}
		c, ok := d.reg.Collection(realm, s.DB, s.Collection)
		if !ok {
			bad("collection", "no such collection")
			return
		}
		if !c.Realtime {
			writeError(w, r, http.StatusBadRequest, codeRealtimeNotEnabled, c.Database+"/"+c.Name+" does not declare realtime: true", Detail{Path: at + ".collection", Reason: "realtime is not enabled"})
			return
		}
		if hasAuth && !scopeAllows(w, r, caller, auth.ScopeRead, c.Database, c.Name) {
			return
		}
		b, status := d.subscription(r, c, s)
		if status != nil {
			status(w, r, at)
			return
		}
		b.sub.ID = s.ID
		built = append(built, b)
		subs = append(subs, b.sub)
	}

	key := d.resumeKey(realm)
	principal := "anon:" + clientIP(r)
	if hasAuth {
		if sub := caller.Subject(); sub != "anonymous" {
			principal = sub
		}
	}
	stream, err := d.hub.Open(realtime.Request{
		Realm: realm, Principal: principal, Key: key, Subscriptions: subs, Resume: req.ResumeFrom,
		MaxStreamsPerUser: rl.Settings.Realtime.MaxStreamsPerUser,
		Access:            d.accessOf(r, realm, caller, built),
		Config:            cfgSub,
	})
	switch {
	case errors.Is(err, realtime.ErrBadToken):
		writeError(w, r, http.StatusBadRequest, codeInvalidResumeToken, "the resume token is not valid for this caller")
		return
	case errors.Is(err, realtime.ErrTooManySubscriptions):
		writeError(w, r, http.StatusBadRequest, codeTooManySubscriptions, "too many subscriptions")
		return
	case errors.Is(err, realtime.ErrOpenTooFast):
		w.Header().Set("Retry-After", "1")
		writeError(w, r, http.StatusTooManyRequests, codeTooManyRequests, "streams are opened too fast: retry in a second")
		return
	case errors.Is(err, realtime.ErrUserStreams), errors.Is(err, realtime.ErrTooManyStreams):
		w.Header().Set("Retry-After", "5")
		writeError(w, r, http.StatusTooManyRequests, codeTooManyRequests, "too many open streams: close one and retry")
		return
	case err != nil:
		writeError(w, r, http.StatusInternalServerError, "internal", "could not open the stream")
		return
	}
	defer stream.Close()

	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // a stream lives past the server's write timeout
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	write := func(f realtime.Frame) error {
		var b strings.Builder
		switch f.Event {
		case "":
			b.WriteString(": heartbeat\n\n")
		default:
			if f.ID != "" {
				b.WriteString("id: " + f.ID + "\n")
			}
			b.WriteString("event: " + f.Event + "\n")
			b.WriteString("data: " + string(f.Data) + "\n\n")
		}
		if _, err := io.WriteString(w, b.String()); err != nil {
			return err
		}
		return rc.Flush()
	}
	ids := stream.IDs()
	if cfgSub != nil {
		ids = append(ids, cfgSub.ID)
	}
	ready, _ := json.Marshal(map[string]any{"subscriptions": ids})
	if err := write(realtime.Frame{Event: "ready", Data: ready}); err != nil {
		return
	}
	_ = stream.Run(r.Context(), write)
}

// builtSub is a subscription with what it needs to be evaluated again after the caller's
// access changed.
type builtSub struct {
	sub    *realtime.Subscription
	c      *registry.Collection
	where  storage.Filter
	filter storage.Filter // the resolved filter now in force
}

// resolve combines the subscription's filter with the caller's read rule and soft deletion.
// why is non-empty when the caller may read nothing of the collection.
func (d *documents) resolve(r *http.Request, c *registry.Collection, where storage.Filter) (storage.Filter, access, string) {
	a := d.accessIn(r, c)
	ruleFilter, why := readRuleFilter(a, c)
	if why != "" {
		return storage.Const(false), a, why
	}
	return hideDeleted(c, and(where, ruleFilter)), a, ""
}

func visibleBy(filter storage.Filter) func(map[string]any) (bool, error) {
	return func(doc map[string]any) (bool, error) { return storage.Match(doc, filter) }
}

// subscription builds what one stream entry follows. On a refusal it returns the function
// that answers the request.
func (d *documents) subscription(r *http.Request, c *registry.Collection, s realtimeSubscription) (*builtSub, func(http.ResponseWriter, *http.Request, string)) {
	invalid := func(path, reason string) func(http.ResponseWriter, *http.Request, string) {
		return func(w http.ResponseWriter, r *http.Request, at string) {
			writeError(w, r, http.StatusBadRequest, codeInvalidQuery, "invalid subscription", Detail{Path: at + "." + path, Reason: reason})
		}
	}
	if s.Doc != "" && len(s.Where) > 0 {
		return nil, invalid("doc", "use doc or where, not both")
	}
	var where storage.Filter
	if len(s.Where) > 0 {
		raw, _ := json.Marshal(s.Where)
		f, err := query.ParseWhere(string(raw), c.Fields)
		if err != nil {
			var qe query.Errors
			if errors.As(err, &qe) && len(qe) > 0 {
				return nil, invalid("where", qe[0].Path+": "+qe[0].Reason)
			}
			return nil, invalid("where", err.Error())
		}
		if err := storage.MatchSupported(f); err != nil {
			return nil, invalid("where", err.Error())
		}
		where = f
	}
	filter, a, why := d.resolve(r, c, where)
	if why != "" {
		return nil, func(w http.ResponseWriter, r *http.Request, at string) {
			if a.caller.User == nil {
				deny(w, r, a, c, rules.Read, why) // 401 for anonymous callers
				return
			}
			logger(r.Context()).Debug("access denied", "collection", c.Realm+"/"+c.Database+"/"+c.Name, "operation", "read", "reason", why)
			// The detail names the subscription, so a client with several views refuses only the one.
			writeError(w, r, http.StatusForbidden, codeForbidden, "no access rule allows this operation", Detail{Path: at + ".collection", Reason: "the caller may not read it"})
		}
	}
	// An event carries the document as a read would: encrypted fields follow their reveal rules,
	// evaluated for the caller the subscription was made by (a change of their access ends the stream).
	rv := d.revealerFor(r, c)
	show := func(doc map[string]any) map[string]any {
		shown, _ := rv.view(doc)
		return render(shown)
	}
	return &builtSub{
		c: c, where: where, filter: filter,
		sub: &realtime.Subscription{
			DB: c.MongoDatabase, Coll: c.Name, Doc: s.Doc, Keys: realtime.KeysOf(filter),
			Visible: visibleBy(filter), Render: show,
		},
	}, nil
}

// accessOf describes the stream's tie to its caller and how to read that access again.
func (d *documents) accessOf(r *http.Request, realm string, caller auth.Caller, built []*builtSub) *realtime.Access {
	svc := d.users(realm)
	if svc == nil || (caller.User == nil && caller.Key == nil) {
		return nil
	}
	acc := &realtime.Access{}
	fill := func(c auth.Caller) {
		if c.User != nil {
			acc.SessionID, acc.UserID = c.User.Session.ID, c.User.User.ID
			if !c.User.Session.ExpiresAt.IsZero() {
				acc.Expires = c.User.Session.ExpiresAt
			}
		}
		if c.Key != nil {
			acc.KeyID = c.Key.Hash
			if c.Key.ExpiresAt != nil {
				acc.Expires = *c.Key.ExpiresAt
			}
		}
	}
	fill(caller)
	acc.Orgs = d.orgIDs(r, realm, caller)
	acc.Fingerprint = d.accessFingerprint(r, realm, caller)
	for _, b := range built {
		if b.c.Rules.UsesConfig() {
			acc.UsesConfig = true
		}
	}
	reid := d.reidentifier(r, realm, caller)
	acc.Reload = func(ctx context.Context) (realtime.Reloaded, error) {
		cur, err := reid(ctx)
		switch {
		case errors.Is(err, auth.ErrUnauthenticated), errors.Is(err, auth.ErrUserDisabled), errors.Is(err, auth.ErrNotFound):
			return realtime.Reloaded{Ended: true}, nil
		case err != nil:
			return realtime.Reloaded{}, err
		}
		ctx = context.WithValue(ctx, callerKey{}, cur)
		rr := r.WithContext(ctx)
		out := realtime.Reloaded{Subs: map[string]realtime.Visibility{}, Orgs: d.orgIDs(rr, realm, cur), Fingerprint: d.accessFingerprint(rr, realm, cur)}
		if cur.User != nil && !cur.User.Session.ExpiresAt.IsZero() {
			out.Expires = cur.User.Session.ExpiresAt
		}
		for _, b := range built {
			filter, _, _ := d.resolve(rr, b.c, b.where)
			changed := !reflect.DeepEqual(filter, b.filter)
			if changed {
				b.filter = filter
			}
			out.Subs[b.sub.ID] = realtime.Visibility{Changed: changed, Visible: visibleBy(filter), Keys: realtime.KeysOf(filter)}
		}
		return out, nil
	}
	return acc
}

// accessFingerprint summarizes what the caller's rules can depend on: who they are, their roles,
// how they signed in, their consents and their organizations with roles, teams and claims. A
// resume token carries it, so a replay never shows history made while it was different.
func (d *documents) accessFingerprint(r *http.Request, realm string, caller auth.Caller) string {
	type org struct {
		ID        string
		Roles     []string
		Teams     []string
		TeamRoles map[string][]string
		Claims    map[string]any
	}
	type view struct {
		Subject  string
		Roles    []string
		MFA      bool
		Methods  []string
		Consents any
		Orgs     []org
	}
	v := view{Subject: caller.Subject()}
	if caller.User != nil {
		u := caller.User.User
		v.Roles = slices.Sorted(slices.Values(u.Roles))
		mfa, methods := caller.SignInMethods()
		v.MFA, v.Methods = mfa, slices.Sorted(slices.Values(methods))
		v.Consents = u.Consents
		orgs := d.userOrgs(r, realm, caller)
		for _, id := range slices.Sorted(maps.Keys(orgs)) {
			o := orgs[id]
			tr := map[string][]string{}
			for k, roles := range o.TeamRoles {
				tr[k] = slices.Sorted(slices.Values(roles))
			}
			v.Orgs = append(v.Orgs, org{ID: id, Roles: slices.Sorted(slices.Values(o.Roles)), Teams: slices.Sorted(slices.Values(o.Teams)), TeamRoles: tr, Claims: o.Claims})
		}
	}
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

// orgIDs lists the organizations the caller belongs to (none without a user).
func (d *documents) orgIDs(r *http.Request, realm string, caller auth.Caller) []string {
	if caller.User == nil {
		return nil
	}
	orgs := d.userOrgs(r, realm, caller)
	out := make([]string, 0, len(orgs))
	for id := range orgs {
		out = append(out, id)
	}
	return out
}

// mergeValues keeps ctx's deadline and cancellation and the values of base.
func mergeValues(ctx, base context.Context) context.Context {
	return valuesCtx{Context: ctx, base: base}
}

type valuesCtx struct {
	context.Context
	base context.Context
}

func (v valuesCtx) Value(key any) any {
	if x := v.Context.Value(key); x != nil {
		return x
	}
	return v.base.Value(key)
}

// processKey is a random key that lives as long as the process.
func (d *documents) processKey() []byte {
	d.procOnce.Do(func() {
		d.procKey = make([]byte, 32)
		_, _ = rand.Read(d.procKey)
	})
	return d.procKey
}

// reidentifier returns what reads the caller of a stream again, with the credential it opened
// with: its roles, consents and memberships as they are now. The credential stays in memory
// and the stream ends with it.
func (d *documents) reidentifier(r *http.Request, realm string, caller auth.Caller) func(ctx context.Context) (auth.Caller, error) {
	svc := d.users(realm)
	if svc == nil || (caller.User == nil && caller.Key == nil) {
		return func(context.Context) (auth.Caller, error) { return caller, nil }
	}
	credential, _ := bearerToken(r)
	behalf := r.Header.Get(onBehalfHeader)
	if cs, src, _ := credentialOf(r, svc.Settings); credential == "" && src == fromCookie {
		credential = cs
	}
	base := context.WithoutCancel(r.Context())
	return func(ctx context.Context) (auth.Caller, error) {
		ctx = context.WithValue(mergeValues(ctx, base), callerKey{}, caller)
		cur, err := svc.Identify(ctx, credential)
		if err == nil && behalf != "" {
			cur, err = svc.OnBehalfOf(ctx, cur, behalf)
		}
		return cur, err
	}
}

// configSub follows the configuration for a stream: Check keeps what the caller read last and
// reports the keys that differ now, so a change reaches a subscriber only when it concerns
// something their rules let them read. It returns nil and the reason when the subscription
// can't be made.
func (d *documents) configSub(r *http.Request, realm string, caller auth.Caller, hasAuth bool, id string, paths []string) (*realtime.ConfigSub, string) {
	rl := d.reg.Realms[realm]
	if rl == nil || rl.AppConfig == nil || d.users(realm) == nil {
		return nil, "this realm declares no app configuration"
	}
	for _, p := range paths {
		if !rl.AppConfig.HasPath(p) {
			return nil, strconv.Quote(p) + " is not a configuration key"
		}
	}
	inScope := func(leaf string) bool {
		if len(paths) == 0 {
			return true
		}
		for _, p := range paths {
			if leaf == p || strings.HasPrefix(leaf, p+".") {
				return true
			}
		}
		return false
	}
	reid := d.reidentifier(r, realm, caller)
	var mu sync.Mutex
	var last map[string]any
	read := func(ctx context.Context) (map[string]any, int64, error) {
		cur, err := reid(ctx)
		if err != nil {
			return nil, 0, err
		}
		flat, version, _, _, err := d.readableConfig(ctx, realm, cur, hasAuth, inScope)
		return flat, version, err
	}
	// The first look is what the client is about to load: it is told of what differs from it.
	if flat, _, err := read(r.Context()); err == nil {
		last = flat
	}
	return &realtime.ConfigSub{ID: id, Check: func(ctx context.Context) (*realtime.ConfigNotice, error) {
		flat, version, err := read(ctx)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		defer mu.Unlock()
		var changed []string
		for p, v := range flat {
			if old, ok := last[p]; !ok || !reflect.DeepEqual(old, v) {
				changed = append(changed, p)
			}
		}
		for p := range last {
			if _, ok := flat[p]; !ok {
				changed = append(changed, p)
			}
		}
		last = flat
		if len(changed) == 0 {
			return nil, nil
		}
		slices.Sort(changed)
		return &realtime.ConfigNotice{Version: appconfig.FormatVersion(version), Paths: changed}, nil
	}}, ""
}
