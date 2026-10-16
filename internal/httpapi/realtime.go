package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

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
		sub, status := d.subscription(r, c, s, hasAuth)
		if status != nil {
			status(w, r, at)
			return
		}
		sub.ID = s.ID
		subs = append(subs, sub)
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
	ready, _ := json.Marshal(map[string]any{"subscriptions": stream.IDs()})
	if err := write(realtime.Frame{Event: "ready", Data: ready}); err != nil {
		return
	}
	_ = stream.Run(r.Context(), write)
}

// subscription builds what one stream entry follows. On a refusal it returns the function
// that answers the request.
func (d *documents) subscription(r *http.Request, c *registry.Collection, s realtimeSubscription, hasAuth bool) (*realtime.Subscription, func(http.ResponseWriter, *http.Request, string)) {
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
	a := d.accessIn(r, c)
	ruleFilter, why := readRuleFilter(a, c)
	if why != "" {
		return nil, func(w http.ResponseWriter, r *http.Request, at string) {
			deny(w, r, a, c, rules.Read, why)
		}
	}
	filter := and(where, ruleFilter)
	filter = hideDeleted(c, filter)
	return &realtime.Subscription{
		DB: c.MongoDatabase, Coll: c.Name, Doc: s.Doc, Keys: realtime.KeysOf(filter),
		Visible: func(doc map[string]any) (bool, error) { return storage.Match(doc, filter) },
		Render:  render,
	}, nil
}

// processKey is a random key that lives as long as the process.
func (d *documents) processKey() []byte {
	d.procOnce.Do(func() {
		d.procKey = make([]byte, 32)
		_, _ = rand.Read(d.procKey)
	})
	return d.procKey
}
