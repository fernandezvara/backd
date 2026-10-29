package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	reconnectBase = 500 * time.Millisecond
	configSubID   = "_config"
	maxEventBytes = 16 << 20
)

// LiveStatus says where a live view stands.
type LiveStatus string

const (
	// StatusConnecting: nothing has been loaded yet.
	StatusConnecting LiveStatus = "connecting"
	// StatusLive: loaded and the stream is open.
	StatusLive LiveStatus = "live"
	// StatusResyncing: the stream dropped or was reset; the items shown are the last known.
	StatusResyncing LiveStatus = "resyncing"
	// StatusClosed: closed by Close, or by an error (see [LiveView.Err]).
	StatusClosed LiveStatus = "closed"
)

// LoadSpec says how the initial list is read and how the live list is kept.
type LoadSpec struct {
	// Sort is the order, as an order_by ("-_meta.created_at"); by id when empty.
	Sort []string
	// Limit is the most documents kept (100 by default); the list keeps the first Limit in Sort order.
	Limit int
}

// ReferenceSpec names documents referenced by the listed ones, to keep beside the list.
type ReferenceSpec struct {
	// Field is a field of the listed documents (a dot path) holding an id or a list of ids.
	Field string
	// DB is the referenced collection's database; the subscription's by default.
	DB         string
	Collection string
	// Fields keeps only these fields of each referenced document (and its id and _meta).
	Fields []string
}

// SubscriptionSpec says what a live view follows.
type SubscriptionSpec struct {
	// ID names the subscription in LiveState.Subscriptions; its position by default.
	ID string
	// DB and Collection name a collection that declares realtime: true.
	DB, Collection string
	// Where is the filter, in the query language limited to comparisons, $in, $nin, $between,
	// $isNull and $and/$or (no $search or text operators).
	Where any
	// Doc follows one document by id instead of a filter.
	Doc  string
	Load LoadSpec
	// References are referenced documents to keep beside the list, by name.
	References map[string]ReferenceSpec
}

// SubscribeOptions tune a live view.
type SubscribeOptions struct {
	// Jitter: a reload after a reset waits a random time up to this (1s by default), so that
	// many clients reset at once don't all reload at once.
	Jitter time.Duration
	// Random is the source of randomness for the jitter, in [0, 1); math/rand by default.
	Random func() float64
	// MaxBackoff is the longest wait before reconnecting after a failure (30s by default).
	MaxBackoff time.Duration
}

// LiveSubscriptionState is one subscription of a view.
type LiveSubscriptionState struct {
	Items  []Doc
	Status LiveStatus
}

// LiveState is what a view holds.
type LiveState struct {
	Status LiveStatus
	// Items are the first subscription's documents (the usual case: one).
	Items []Doc
	// Subscriptions are all of them, by id.
	Subscriptions map[string]LiveSubscriptionState
	// References are the referenced documents: by reference name, then by id.
	References map[string]map[string]Doc
}

// ResetInfo is what a reset says.
type ResetInfo struct {
	Subscription string
	// Reason is "history_expired", "slow_consumer", "preimage_missing", "config_changed" or "access_changed".
	Reason string
}

type configFollower struct {
	paths   []string
	ready   func()
	changed func([]string)
	err     func(error)
}

// Realtime follows collections live over one multiplexed stream: c.Realtime(). Subscribing is
// per view; the client opens one stream for all of them and keeps it open, resuming after drops.
type Realtime struct {
	c *Client

	mu      sync.Mutex
	subs    map[string]*sub // by wire id
	order   []*sub          // in subscription order
	views   map[*LiveView]struct{}
	config  *configFollower
	seq     int
	token   string
	gen     int
	cancel  context.CancelFunc
	ready   bool
	syncing bool
	closed  bool
}

func newRealtime(c *Client) *Realtime {
	r := &Realtime{c: c, subs: map[string]*sub{}, views: map[*LiveView]struct{}{}}
	c.OnClose(r.shutdown)
	c.OnCredentialChange(func() {
		r.mu.Lock()
		follows := r.config != nil
		r.mu.Unlock()
		if follows {
			r.schedule()
		}
	})
	return r
}

// Realtime returns the live views of the client.
func (c *Client) Realtime() *Realtime { return c.rt }

// Open returns the number of live views that are open.
func (r *Realtime) Open() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.views)
}

func (r *Realtime) shutdown() {
	r.mu.Lock()
	r.closed = true
	r.gen++
	if r.cancel != nil {
		r.cancel()
	}
	views := make([]*LiveView, 0, len(r.views))
	for v := range r.views {
		views = append(views, v)
	}
	r.mu.Unlock()
	for _, v := range views {
		v.Close()
	}
}

// Subscribe follows one or more subscriptions as one view. The view is loaded from normal reads
// when the stream is open, and kept current from its events; Close it when done.
func (r *Realtime) Subscribe(specs []SubscriptionSpec, o SubscribeOptions) (*LiveView, error) {
	if len(specs) == 0 {
		return nil, errors.New("client: Subscribe needs at least one subscription")
	}
	v := &LiveView{rt: r, opts: o, status: StatusConnecting, changed: make(chan struct{}), refs: map[string]map[string]Doc{}, refsAsked: map[string]bool{}}
	names := map[string]bool{}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, errors.New("client: the client is closed")
	}
	for i, spec := range specs {
		if spec.DB == "" || spec.Collection == "" {
			return nil, errors.New("client: each subscription needs DB and Collection")
		}
		name := spec.ID
		if name == "" {
			name = strconv.Itoa(i)
		}
		if names[name] {
			return nil, errors.New("client: duplicate subscription id " + strconv.Quote(name))
		}
		names[name] = true
	}
	for i, spec := range specs {
		name := spec.ID
		if name == "" {
			name = strconv.Itoa(i)
		}
		r.seq++
		s := &sub{wire: "s" + strconv.Itoa(r.seq), name: name, spec: spec, view: v, items: map[string]Doc{}, needsLoad: true}
		v.subs = append(v.subs, s)
		r.subs[s.wire] = s
		r.order = append(r.order, s)
	}
	r.views[v] = struct{}{}
	r.scheduleLocked()
	return v, nil
}

// Subscribe follows this collection. The spec's DB and Collection are filled in; in a collection
// scoped to a team ([Collection.InTeam]) the filter is limited to the team.
func (c *Collection) Subscribe(spec SubscriptionSpec, o SubscribeOptions) (*LiveView, error) {
	spec.DB, spec.Collection = c.path[0], c.path[1]
	if c.team != "" {
		w := Where{}
		switch x := spec.Where.(type) {
		case nil:
		case string:
			if err := json.Unmarshal([]byte(x), &w); err != nil {
				return nil, errors.New("client: the where is not a JSON object")
			}
		default:
			b, err := json.Marshal(x)
			if err != nil {
				return nil, err
			}
			if err := json.Unmarshal(b, &w); err != nil {
				return nil, errors.New("client: the where is not a JSON object")
			}
		}
		w["_meta.team"] = c.team
		spec.Where = w
	}
	return c.c.Realtime().Subscribe([]SubscriptionSpec{spec}, o)
}

func (r *Realtime) remove(v *LiveView) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removeLocked(v)
	r.scheduleLocked()
}

func (r *Realtime) removeLocked(v *LiveView) {
	delete(r.views, v)
	for _, s := range v.subs {
		delete(r.subs, s.wire)
	}
	kept := r.order[:0]
	for _, s := range r.order {
		if s.view != v {
			kept = append(kept, s)
		}
	}
	r.order = kept
}

func (r *Realtime) followConfig(f *configFollower) (stop func()) {
	r.mu.Lock()
	r.config = f
	r.scheduleLocked()
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.config != f {
			return
		}
		r.config = nil
		r.scheduleLocked()
	}
}

func (r *Realtime) schedule() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scheduleLocked()
}

// scheduleLocked makes the stream match the subscriptions, once for a burst of changes.
func (r *Realtime) scheduleLocked() {
	if r.syncing || r.closed {
		return
	}
	r.syncing = true
	time.AfterFunc(time.Millisecond, func() {
		r.mu.Lock()
		r.syncing = false
		r.reconcileLocked()
		r.mu.Unlock()
	})
}

func (r *Realtime) reconcileLocked() {
	if r.closed {
		return
	}
	r.gen++
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	r.ready = false
	if len(r.subs) == 0 && r.config == nil {
		return
	}
	for v := range r.views {
		v.statusLocked()
	}
	ctx, cancel := context.WithCancel(r.c.life)
	r.cancel = cancel
	go r.connect(r.gen, ctx)
}

type wireSub struct {
	ID         string         `json:"id"`
	DB         string         `json:"db,omitempty"`
	Collection string         `json:"collection,omitempty"`
	Doc        string         `json:"doc,omitempty"`
	Where      any            `json:"where,omitempty"`
	Config     map[string]any `json:"config,omitempty"`
}

func (r *Realtime) maxBackoff() time.Duration {
	m := 30 * time.Second
	for v := range r.views {
		if v.opts.MaxBackoff > 0 {
			m = min(m, v.opts.MaxBackoff)
		}
	}
	return m
}

func (r *Realtime) connect(gen int, ctx context.Context) {
	attempt := 0
	for {
		r.mu.Lock()
		if gen != r.gen {
			r.mu.Unlock()
			return
		}
		var subscriptions []wireSub
		for _, s := range r.order {
			w := wireSub{ID: s.wire, DB: s.spec.DB, Collection: s.spec.Collection}
			if s.spec.Doc != "" {
				w.Doc = s.spec.Doc
			} else if s.spec.Where != nil {
				w.Where = s.spec.Where
			}
			subscriptions = append(subscriptions, w)
		}
		if r.config != nil {
			paths := r.config.paths
			if paths == nil {
				paths = []string{}
			}
			subscriptions = append(subscriptions, wireSub{ID: configSubID, Config: map[string]any{"paths": paths}})
		}
		body := map[string]any{"subscriptions": subscriptions}
		if r.token != "" {
			body["resume_from"] = r.token
		}
		r.mu.Unlock()

		reconnectNow := false
		res, err := r.c.openStream(ctx, []string{"_realtime"}, body)
		if err == nil {
			attempt = 0
			reconnectNow = r.read(res.Body, gen)
			res.Body.Close()
		} else {
			r.mu.Lock()
			if gen != r.gen {
				r.mu.Unlock()
				return
			}
			retryNow, stop := r.handleOpenErrorLocked(err)
			r.mu.Unlock()
			if stop {
				return
			}
			if retryNow {
				continue
			}
		}
		r.mu.Lock()
		if gen != r.gen {
			r.mu.Unlock()
			return
		}
		r.ready = false
		for v := range r.views {
			v.statusLocked()
		}
		wait := min(reconnectBase<<min(attempt, 20), r.maxBackoff())
		r.mu.Unlock()
		if reconnectNow {
			continue
		}
		attempt++
		if sleep(ctx, wait) != nil {
			return
		}
	}
}

var subscriptionIndex = regexp.MustCompile(`^subscriptions\[(\d+)\]`)

// handleOpenErrorLocked decides what a refused or failed stream means: try again at once, stop
// for good, or reconnect after the backoff.
func (r *Realtime) handleOpenErrorLocked(err error) (retryNow, stop bool) {
	var e *Error
	if !errors.As(err, &e) {
		return false, false
	}
	switch {
	case errors.Is(err, ErrInvalidResumeToken):
		r.token = ""
		for _, s := range r.order {
			s.needsLoad = true
		}
		return true, false
	case errors.Is(err, ErrRealtimeNotEnabled), errors.Is(err, ErrForbidden), errors.Is(err, ErrValidation), errors.Is(err, ErrAuthentication):
		var at = -1
		if len(e.Details) > 0 {
			if m := subscriptionIndex.FindStringSubmatch(e.Details[0].Path); m != nil {
				at, _ = strconv.Atoi(m[1])
			}
		}
		var bad *sub
		if at >= 0 && at < len(r.order) {
			bad = r.order[at]
		}
		if at >= 0 && bad == nil && r.config != nil && at == len(r.order) {
			f := r.config
			r.config = nil
			go f.err(err)
			return true, len(r.order) == 0
		}
		if bad != nil && len(r.views) > 1 {
			bad.view.failLocked(err) // the rest resume where they were
			return true, false
		}
		r.failLocked(err)
		return false, true
	}
	return false, false
}

func (r *Realtime) failLocked(err error) {
	f := r.config
	r.config = nil
	if f != nil {
		go f.err(err)
	}
	r.gen++
	if r.cancel != nil {
		r.cancel()
	}
	r.ready = false
	for v := range r.views {
		v.failLocked(err)
	}
}

type sseEvent struct {
	id    string
	event string
	data  json.RawMessage
}

// read follows the stream until it ends; it reports whether to reconnect at once.
func (r *Realtime) read(body io.Reader, gen int) bool {
	br := bufio.NewReader(body)
	var id, event string
	var data []string
	size := 0
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return false
		}
		size += len(line)
		if size > maxEventBytes {
			return false
		}
		line = strings.TrimRight(line, "\r\n")
		if line != "" {
			switch {
			case strings.HasPrefix(line, ":"):
			case strings.HasPrefix(line, "id:"):
				id = strings.TrimSpace(line[3:])
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimSpace(line[6:])
			case strings.HasPrefix(line, "data:"):
				data = append(data, strings.TrimLeft(line[5:], " "))
			}
			continue
		}
		ev := sseEvent{id: id, event: event}
		if len(data) > 0 {
			ev.data = json.RawMessage(strings.Join(data, "\n"))
		}
		id, event, data, size = "", "", nil, 0
		if ev.event == "" || (len(ev.data) > 0 && !json.Valid(ev.data)) {
			continue
		}
		r.mu.Lock()
		if gen != r.gen {
			r.mu.Unlock()
			return false
		}
		again, stop := r.handleLocked(ev)
		r.mu.Unlock()
		if stop {
			return again
		}
	}
}

// handleLocked applies one event. stop is true when the stream must be left, with again saying
// whether to reconnect at once.
func (r *Realtime) handleLocked(ev sseEvent) (again, stop bool) {
	if ev.id != "" {
		r.token = ev.id
	}
	switch ev.event {
	case "ready":
		r.ready = true
		for _, s := range r.order {
			if s.needsLoad || !s.loaded {
				s.view.loadLocked(s, false)
			}
		}
		for v := range r.views {
			v.statusLocked()
		}
		if r.config != nil {
			go r.config.ready()
		}
	case "config_changed":
		var d struct {
			Paths []string `json:"paths"`
		}
		_ = json.Unmarshal(ev.data, &d)
		if r.config != nil {
			go r.config.changed(d.Paths)
		}
	case "created", "updated":
		var d struct {
			Sub string `json:"sub"`
			Doc Doc    `json:"doc"`
		}
		if json.Unmarshal(ev.data, &d) == nil {
			if s := r.subs[d.Sub]; s != nil {
				s.view.applyLocked(s, liveEvent{doc: d.Doc})
			}
		}
	case "removed":
		var d struct {
			Sub     string `json:"sub"`
			ID      string `json:"id"`
			Version int64  `json:"version"`
		}
		if json.Unmarshal(ev.data, &d) == nil {
			if s := r.subs[d.Sub]; s != nil {
				s.view.applyLocked(s, liveEvent{removed: true, id: d.ID, version: d.Version})
			}
		}
	case "reset":
		var d struct {
			Subs   []string `json:"subs"`
			Reason string   `json:"reason"`
		}
		_ = json.Unmarshal(ev.data, &d)
		reconnect := false
		for _, id := range d.Subs {
			s := r.subs[id]
			if s == nil {
				continue
			}
			s.view.emit(func(v *LiveView) { v.fireReset(ResetInfo{Subscription: s.name, Reason: d.Reason}) })
			if d.Reason == "config_changed" {
				s.needsLoad = true
				reconnect = true
			} else {
				s.view.loadLocked(s, true)
			}
		}
		return reconnect, reconnect
	case "session_ended":
		r.failLocked(newError(401, "session_ended", "the session, user or API key behind the stream is gone: sign in again", kSessionEnded))
		return false, true
	case "reauth":
		return true, true // the credential's lifetime is over: reconnect, which sends the current one
	}
	return false, false
}

// openStream opens a server-sent event stream: a POST to /v1/{realm}/… that answers
// text/event-stream. The answer is returned unread; a refusal is the error the API sent.
func (c *Client) openStream(ctx context.Context, path []string, body any) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, newError(0, "invalid_request", "the body can't be encoded: "+err.Error(), kValidation)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(path, nil), strings.NewReader(string(payload)))
	if err != nil {
		return nil, newError(0, "invalid_request", "the request can't be made: "+err.Error(), kValidation)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	for k, v := range c.opts.Headers {
		req.Header.Set(k, v)
	}
	if c.opts.Types != "" {
		req.Header.Set("X-Backd-Types", c.opts.Types)
	}
	h, sentSession, _, err := c.credentials(ctx)
	if err != nil {
		return nil, err
	}
	if h != "" {
		req.Header.Set("Authorization", h)
	}
	res, err := c.doer.Do(req)
	if err != nil {
		return nil, networkError(ctx, err)
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return res, nil
	}
	apiErr := errorFromResponse(res)
	var e *Error
	if errors.As(apiErr, &e) && sessionGone(e) && sentSession && c.expired != nil {
		c.expired(ctx)
	}
	return nil, apiErr
}

type sub struct {
	wire, name string
	spec       SubscriptionSpec
	view       *LiveView

	items      map[string]Doc
	loaded     bool
	needsLoad  bool
	buffer     []liveEvent
	generation int
	everLoaded bool
}

type liveEvent struct {
	doc     Doc
	removed bool
	id      string
	version int64
}

// LiveView is the documents of one or more subscriptions, kept current. Read it with [LiveView.State]
// or [LiveView.Items]; react to changes with OnChange or the Updates channel. It is safe for
// concurrent use; callbacks run one at a time, in order, on a goroutine of their own, never while the
// view is locked, so they can read the view.
type LiveView struct {
	rt   *Realtime
	opts SubscribeOptions
	subs []*sub

	// guarded by rt.mu
	status    LiveStatus
	closed    bool
	err       error
	changed   chan struct{} // closed and replaced when the status or the error changes
	refs      map[string]map[string]Doc
	refsAsked map[string]bool
	refsBusy  bool
	pending   map[string]*pendingRefs
	changing  bool

	hmu          sync.Mutex
	onChange     map[int]func(LiveState)
	onStatus     map[int]func(LiveStatus)
	onReset      map[int]func(ResetInfo)
	onAccessLost map[int]func(string)
	onError      map[int]func(error)
	nextHandler  int
	updates      chan LiveState

	qmu      sync.Mutex
	queue    []func()
	draining bool
}

type pendingRefs struct {
	ref ReferenceSpec
	ids []string
}

// emit queues f to run on the view's callback goroutine.
func (v *LiveView) emit(f func(*LiveView)) {
	v.qmu.Lock()
	v.queue = append(v.queue, func() { f(v) })
	start := !v.draining
	v.draining = true
	v.qmu.Unlock()
	if start {
		go v.drain()
	}
}

func (v *LiveView) drain() {
	for {
		v.qmu.Lock()
		if len(v.queue) == 0 {
			v.draining = false
			v.qmu.Unlock()
			return
		}
		f := v.queue[0]
		v.queue = v.queue[1:]
		v.qmu.Unlock()
		safely("LiveView callback", f)
	}
}

func addHandler[F any](v *LiveView, m *map[int]F, f F) (off func()) {
	v.hmu.Lock()
	defer v.hmu.Unlock()
	if *m == nil {
		*m = map[int]F{}
	}
	id := v.nextHandler
	v.nextHandler++
	(*m)[id] = f
	return func() { v.hmu.Lock(); delete(*m, id); v.hmu.Unlock() }
}

func handlers[F any](v *LiveView, m map[int]F) []F {
	v.hmu.Lock()
	defer v.hmu.Unlock()
	out := make([]F, 0, len(m))
	for _, f := range m {
		out = append(out, f)
	}
	return out
}

// OnChange registers f to be called with the new state after the items, the status or the
// references change (several changes at once are one call). It returns the function that removes it.
func (v *LiveView) OnChange(f func(LiveState)) (off func()) { return addHandler(v, &v.onChange, f) }

// OnStatus registers f to be called when the status changes.
func (v *LiveView) OnStatus(f func(LiveStatus)) (off func()) { return addHandler(v, &v.onStatus, f) }

// OnReset registers f to be called when the server asks a subscription to reload.
func (v *LiveView) OnReset(f func(ResetInfo)) (off func()) { return addHandler(v, &v.onReset, f) }

// OnAccessLost registers f to be called, with the subscription's name, when the caller can no
// longer read what the subscription follows: its items are emptied.
func (v *LiveView) OnAccessLost(f func(string)) (off func()) {
	return addHandler(v, &v.onAccessLost, f)
}

// OnError registers f to be called when the view fails for good (it is then closed).
func (v *LiveView) OnError(f func(error)) (off func()) { return addHandler(v, &v.onError, f) }

func (v *LiveView) fireReset(i ResetInfo) {
	for _, f := range handlers(v, v.onReset) {
		f(i)
	}
}

// Updates returns a channel that receives the state after each change. It keeps only the latest
// state a slow reader hasn't taken, and is closed when the view is closed.
func (v *LiveView) Updates() <-chan LiveState {
	v.hmu.Lock()
	if v.updates == nil {
		v.updates = make(chan LiveState, 1)
	}
	ch := v.updates
	v.hmu.Unlock()
	v.rt.mu.Lock()
	closed := v.closed
	v.rt.mu.Unlock()
	if closed {
		v.closeUpdates()
	}
	return ch
}

func (v *LiveView) sendUpdate(s LiveState) {
	v.hmu.Lock()
	defer v.hmu.Unlock()
	if v.updates == nil {
		return
	}
	select {
	case v.updates <- s:
	default:
		select {
		case <-v.updates:
		default:
		}
		select {
		case v.updates <- s:
		default:
		}
	}
}

func (v *LiveView) closeUpdates() {
	v.hmu.Lock()
	defer v.hmu.Unlock()
	if v.updates != nil {
		select {
		case <-v.updates:
		default:
		}
		func() {
			defer func() { _ = recover() }() // already closed
			close(v.updates)
		}()
	}
}

// Err returns the error that closed the view, if any.
func (v *LiveView) Err() error {
	v.rt.mu.Lock()
	defer v.rt.mu.Unlock()
	return v.err
}

// State returns what the view holds now.
func (v *LiveView) State() LiveState {
	v.rt.mu.Lock()
	defer v.rt.mu.Unlock()
	return v.snapshotLocked()
}

// Items returns the first subscription's documents.
func (v *LiveView) Items() []Doc { return v.State().Items }

// Status returns where the view stands.
func (v *LiveView) Status() LiveStatus {
	v.rt.mu.Lock()
	defer v.rt.mu.Unlock()
	return v.status
}

// Ready waits until the view is live and returns its state, or returns the error that closed it.
func (v *LiveView) Ready(ctx context.Context) (LiveState, error) {
	for {
		v.rt.mu.Lock()
		if v.err != nil {
			err := v.err
			v.rt.mu.Unlock()
			return LiveState{}, err
		}
		if v.computeStatusLocked() == StatusLive {
			s := v.snapshotLocked()
			v.rt.mu.Unlock()
			return s, nil
		}
		wait := v.changed
		v.rt.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return LiveState{}, networkError(ctx, ctx.Err())
		}
	}
}

// Close stops following. It is safe to call twice.
func (v *LiveView) Close() {
	v.rt.mu.Lock()
	if v.closed {
		v.rt.mu.Unlock()
		return
	}
	v.closed = true
	v.rt.removeLocked(v)
	v.rt.scheduleLocked()
	v.setStatusLocked(StatusClosed)
	v.rt.mu.Unlock()
	v.finish()
}

// finish closes the updates channel once the pending callbacks have run.
func (v *LiveView) finish() {
	v.emit(func(v *LiveView) { v.closeUpdates() })
}

func (v *LiveView) failLocked(err error) {
	if v.closed {
		return
	}
	v.err, v.closed = err, true
	delete(v.rt.views, v)
	for _, s := range v.subs {
		delete(v.rt.subs, s.wire)
	}
	kept := v.rt.order[:0]
	for _, s := range v.rt.order {
		if s.view != v {
			kept = append(kept, s)
		}
	}
	v.rt.order = kept
	v.setStatusLocked(StatusClosed)
	old := v.changed
	v.changed = make(chan struct{})
	close(old)
	v.emit(func(v *LiveView) {
		for _, f := range handlers(v, v.onError) {
			f(err)
		}
		v.closeUpdates()
	})
}

func (v *LiveView) computeStatusLocked() LiveStatus {
	if v.closed {
		return StatusClosed
	}
	allLoaded, someEver := true, false
	for _, s := range v.subs {
		allLoaded = allLoaded && s.loaded
		someEver = someEver || s.everLoaded
	}
	switch {
	case allLoaded && v.rt.ready:
		return StatusLive
	case someEver:
		return StatusResyncing
	}
	return StatusConnecting
}

func (v *LiveView) statusLocked() { v.setStatusLocked(v.computeStatusLocked()) }

func (v *LiveView) setStatusLocked(s LiveStatus) {
	if s == v.status {
		return
	}
	v.status = s
	old := v.changed
	v.changed = make(chan struct{})
	close(old)
	v.emit(func(v *LiveView) {
		for _, f := range handlers(v, v.onStatus) {
			f(s)
		}
	})
	v.scheduleChangeLocked()
}

func (v *LiveView) scheduleChangeLocked() {
	if v.changing {
		return
	}
	v.changing = true
	v.emit(func(v *LiveView) {
		v.rt.mu.Lock()
		v.changing = false
		st := v.snapshotLocked()
		v.rt.mu.Unlock()
		v.sendUpdate(st)
		for _, f := range handlers(v, v.onChange) {
			f(st)
		}
	})
}

func (v *LiveView) snapshotLocked() LiveState {
	st := LiveState{Status: v.status, Subscriptions: map[string]LiveSubscriptionState{}, References: map[string]map[string]Doc{}}
	for _, s := range v.subs {
		status := StatusConnecting
		switch {
		case s.loaded && v.rt.ready:
			status = StatusLive
		case s.loaded || s.everLoaded:
			status = StatusResyncing
		}
		st.Subscriptions[s.name] = LiveSubscriptionState{Items: s.ordered(), Status: status}
	}
	if len(v.subs) > 0 {
		st.Items = st.Subscriptions[v.subs[0].name].Items
	}
	for name, m := range v.refs {
		c := make(map[string]Doc, len(m))
		for id, d := range m {
			c[id] = d
		}
		st.References[name] = c
	}
	return st
}

func (v *LiveView) loadLocked(s *sub, jitter bool) {
	if v.closed {
		return
	}
	s.needsLoad, s.loaded, s.buffer = false, false, nil
	s.generation++
	generation := s.generation
	v.statusLocked()
	delay := time.Duration(0)
	if jitter {
		max := v.opts.Jitter
		if max <= 0 {
			max = time.Second
		}
		rnd := v.opts.Random
		if rnd == nil {
			rnd = rand.Float64
		}
		delay = time.Duration(rnd() * float64(max))
	}
	go v.run(s, generation, delay)
}

func (v *LiveView) current(s *sub, generation int) bool {
	return !v.closed && generation == s.generation
}

func (v *LiveView) run(s *sub, generation int, delay time.Duration) {
	ctx := v.rt.c.life
	if delay > 0 && sleep(ctx, delay) != nil {
		return
	}
	v.rt.mu.Lock()
	ok := v.current(s, generation)
	v.rt.mu.Unlock()
	if !ok {
		return
	}
	items, err := v.fetch(ctx, s)
	v.rt.mu.Lock()
	defer v.rt.mu.Unlock()
	if !v.current(s, generation) {
		return
	}
	if err != nil {
		switch {
		case errors.Is(err, ErrForbidden) && s.everLoaded:
			s.items, s.buffer, s.loaded = map[string]Doc{}, nil, true
			name := s.name
			v.emit(func(v *LiveView) {
				for _, f := range handlers(v, v.onAccessLost) {
					f(name)
				}
			})
			v.statusLocked()
			v.scheduleChangeLocked()
		case errors.Is(err, ErrNetwork) && ctx.Err() != nil:
			// the client is closing
		case errors.Is(err, ErrNetwork), isServerError(err):
			go func() {
				if sleep(ctx, time.Second) == nil {
					v.rt.mu.Lock()
					v.loadLocked(s, false)
					v.rt.mu.Unlock()
				}
			}()
		default:
			v.failLocked(err)
		}
		return
	}
	s.items = make(map[string]Doc, len(items))
	for _, d := range items {
		s.items[d.ID()] = d
	}
	for _, ev := range s.buffer {
		s.applyNow(ev)
	}
	s.buffer = nil
	s.loaded, s.everLoaded = true, true
	s.trim()
	v.collectReferencesLocked(s)
	v.statusLocked()
	v.scheduleChangeLocked()
}

func isServerError(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status >= 500
}

// fetch reads what a subscription follows, with normal reads.
func (v *LiveView) fetch(ctx context.Context, s *sub) ([]Doc, error) {
	col := v.rt.c.DB(s.spec.DB).Collection(s.spec.Collection)
	if s.spec.Doc != "" {
		d, err := col.Get(ctx, s.spec.Doc, ReadOptions{})
		var e *Error
		if errors.As(err, &e) && e.Status == 404 {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return []Doc{d}, nil
	}
	want := s.spec.Load.Limit
	if want <= 0 {
		want = 100
	}
	var out []Doc
	after := ""
	for len(out) < want {
		page, err := col.List(ctx, Query{Where: s.spec.Where, OrderBy: s.spec.Load.Sort, Limit: min(100, want-len(out)), After: after})
		if err != nil {
			return nil, err
		}
		out = append(out, page.Items...)
		if !page.HasMore || page.NextCursor == "" {
			break
		}
		after = page.NextCursor
	}
	return out, nil
}

func (v *LiveView) applyLocked(s *sub, ev liveEvent) {
	if v.closed {
		return
	}
	if !s.loaded {
		s.buffer = append(s.buffer, ev)
		return
	}
	s.applyNow(ev)
	s.trim()
	if !ev.removed {
		v.collectReferencesLocked(s)
	}
	v.scheduleChangeLocked()
}

func versionOf(d Doc) int64 {
	m, _ := d["_meta"].(map[string]any)
	f, _ := m["version"].(float64)
	return int64(f)
}

func (s *sub) applyNow(ev liveEvent) {
	if ev.removed {
		if held, ok := s.items[ev.id]; ok && versionOf(held) <= ev.version {
			delete(s.items, ev.id)
		}
		return
	}
	id := ev.doc.ID()
	if held, ok := s.items[id]; ok && versionOf(held) >= versionOf(ev.doc) {
		return
	}
	s.items[id] = ev.doc
}

func (s *sub) trim() {
	limit := s.spec.Load.Limit
	if limit <= 0 {
		limit = 100
	}
	if s.spec.Doc != "" || len(s.items) <= limit {
		return
	}
	keep := s.ordered()[:limit]
	s.items = make(map[string]Doc, len(keep))
	for _, d := range keep {
		s.items[d.ID()] = d
	}
}

type sortKey struct {
	path string
	dir  int
}

func (s *sub) ordered() []Doc {
	items := make([]Doc, 0, len(s.items))
	for _, d := range s.items {
		items = append(items, d)
	}
	if s.spec.Doc != "" {
		return items
	}
	var keys []sortKey
	hasID := false
	for _, k := range s.spec.Load.Sort {
		for _, p := range strings.Split(k, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			key := sortKey{p, 1}
			if strings.HasPrefix(p, "-") {
				key = sortKey{p[1:], -1}
			} else if strings.HasPrefix(p, "+") {
				key = sortKey{p[1:], 1}
			}
			hasID = hasID || key.path == "id"
			keys = append(keys, key)
		}
	}
	if !hasID {
		keys = append(keys, sortKey{"id", 1})
	}
	sort.SliceStable(items, func(i, j int) bool {
		for _, k := range keys {
			if c := compareValues(docAt(items[i], k.path), docAt(items[j], k.path)); c != 0 {
				return c*k.dir < 0
			}
		}
		return false
	})
	return items
}

func docAt(d Doc, path string) any {
	var v any = map[string]any(d)
	for _, p := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}

// compareValues orders values as the database does: missing and null first, then by type, then by value.
func compareValues(a, b any) int {
	rank := func(v any) int {
		switch v.(type) {
		case nil:
			return 0
		case bool:
			return 2
		case float64:
			return 1
		case string:
			return 3
		}
		return 4
	}
	ra, rb := rank(a), rank(b)
	if ra != rb {
		if ra < rb {
			return -1
		}
		return 1
	}
	switch x := a.(type) {
	case float64:
		y := b.(float64)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
	case string:
		return strings.Compare(x, b.(string))
	case bool:
		y := b.(bool)
		switch {
		case !x && y:
			return -1
		case x && !y:
			return 1
		}
	}
	return 0
}

func idsAt(d Doc, field string) []string {
	switch v := docAt(d, field).(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func (v *LiveView) collectReferencesLocked(s *sub) {
	for name, ref := range s.spec.References {
		held := v.refs[name]
		if held == nil {
			held = map[string]Doc{}
			v.refs[name] = held
		}
		for _, d := range s.items {
			for _, id := range idsAt(d, ref.Field) {
				key := name + "\x00" + id
				if _, ok := held[id]; !ok && !v.refsAsked[key] {
					v.refsAsked[key] = true
					if v.pending == nil {
						v.pending = map[string]*pendingRefs{}
					}
					p := v.pending[name]
					if p == nil {
						p = &pendingRefs{ref: ref}
						v.pending[name] = p
					}
					p.ids = append(p.ids, id)
				}
			}
		}
	}
	if len(v.pending) > 0 && !v.refsBusy {
		v.refsBusy = true
		go v.fetchReferences(s.spec.DB)
	}
}

func (v *LiveView) fetchReferences(defaultDB string) {
	v.rt.mu.Lock()
	pending := v.pending
	v.pending = nil
	v.refsBusy = false
	closed := v.closed
	v.rt.mu.Unlock()
	if closed || pending == nil {
		return
	}
	ctx := v.rt.c.life
	for name, p := range pending {
		db := p.ref.DB
		if db == "" {
			db = defaultDB
		}
		col := v.rt.c.DB(db).Collection(p.ref.Collection)
		for i := 0; i < len(p.ids); i += 100 {
			chunk := p.ids[i:min(i+100, len(p.ids))]
			in := make([]any, len(chunk))
			for k, id := range chunk {
				in[k] = id
			}
			page, err := col.List(ctx, Query{Where: Where{"id": map[string]any{"$in": in}}, Limit: len(chunk)})
			v.rt.mu.Lock()
			if err != nil {
				for _, id := range chunk {
					delete(v.refsAsked, name+"\x00"+id)
				}
			} else {
				held := v.refs[name]
				if held == nil {
					held = map[string]Doc{}
					v.refs[name] = held
				}
				for _, d := range page.Items {
					held[d.ID()] = trimFields(d, p.ref.Fields)
				}
			}
			v.rt.mu.Unlock()
		}
	}
	v.rt.mu.Lock()
	v.scheduleChangeLocked()
	v.rt.mu.Unlock()
}

func trimFields(d Doc, fields []string) Doc {
	if len(fields) == 0 {
		return d
	}
	out := Doc{"id": d["id"], "_meta": d["_meta"]}
	for _, f := range fields {
		if v := docAt(d, f); v != nil {
			out[f] = v
		}
	}
	return out
}

// DecodeDocs decodes documents into structs, for the items of a view.
func DecodeDocs[T any](docs []Doc) ([]T, error) {
	out := make([]T, len(docs))
	for i, d := range docs {
		b, err := json.Marshal(d)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Live keeps the held copies of the configuration current: it follows the paths over the realtime
// stream (all the caller reads, with none), and re-reads what changed. Changes are reported to
// [Config.OnChange]; failures to [Config.OnError]. It returns the function that stops it.
func (cfg *Config) Live(paths ...string) (stop func()) {
	cfg.lmu.Lock()
	if cfg.stopLive != nil {
		cfg.stopLive()
	}
	cfg.lmu.Unlock()
	list := strings.Split(normalizePaths(paths), ",")
	if len(list) == 1 && list[0] == "" {
		list = nil
	}
	var once sync.Once
	var stopFollow func()
	stopOnce := func() {
		once.Do(func() {
			cfg.lmu.Lock()
			cfg.stopLive = nil
			cfg.lmu.Unlock()
			stopFollow()
		})
	}
	stopFollow = cfg.c.rt.followConfig(&configFollower{
		paths:   list,
		ready:   func() { cfg.reread(cfg.c.life, nil) },
		changed: func(changed []string) { cfg.reread(cfg.c.life, changed) },
		err: func(err error) {
			cfg.lmu.Lock()
			cfg.stopLive = nil
			cfg.lmu.Unlock()
			cfg.emitError(err)
		},
	})
	cfg.lmu.Lock()
	cfg.stopLive = stopOnce
	cfg.lmu.Unlock()
	return stopOnce
}
