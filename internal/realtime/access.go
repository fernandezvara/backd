package realtime

import (
	"context"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/changefeed"
)

// The system collections a stream's access depends on, as the change feed names them.
const (
	sessionsColl   = "sessions"
	usersColl      = "users"
	apiKeysColl    = "api_keys"
	orgsColl       = "orgs"
	orgMembersColl = "org_members"
	orgTeamsColl   = "org_teams"
)

// Events that end a stream (design §17.2).
const (
	EventSessionEnded = "session_ended" // the session, user or key is gone: sign in again
	EventReauth       = "reauth"        // the credential's lifetime is over: reconnect with a fresh one
)

// Access ties a stream to who is on the other end, so changes of that access reach it.
type Access struct {
	SessionID string // the session's id (its document key)
	UserID    string
	KeyID     string // an API key's document key (its hash)
	Orgs      []string
	// Expires is when the credential stops being good; zero: never.
	Expires time.Time
	// Reload reads the caller's access again and returns what each subscription (by id) shows
	// now. Ended means the credential is no longer valid.
	Reload func(ctx context.Context) (Reloaded, error)
}

// Reloaded is the answer of Access.Reload.
type Reloaded struct {
	Ended   bool
	Orgs    []string
	Expires time.Time
	Subs    map[string]Visibility
}

// Visibility is what decides, after a reload, what a subscription shows.
type Visibility struct {
	// Changed says the caller's effective access to the subscription differs from before.
	Changed bool
	Visible func(doc map[string]any) (bool, error)
	Keys    []Key
}

// minReload is the least time between two reloads of one stream: bursts of changes in an
// organization are coalesced into one.
const minReload = 250 * time.Millisecond

// systemChange routes a change of a system collection to the streams it affects.
func (h *Hub) systemChange(c changefeed.Change) {
	h.mu.Lock()
	var streams []*Stream
	for s := range h.streams {
		if s.realm == c.Realm && s.access != nil {
			streams = append(streams, s)
		}
	}
	h.mu.Unlock()
	for _, s := range streams {
		s.systemChange(c)
	}
}

func (s *Stream) systemChange(c changefeed.Change) {
	a := s.access
	removed := c.Op == changefeed.Removed
	switch c.Coll {
	case sessionsColl:
		if a.SessionID != "" && c.Key == a.SessionID {
			if removed {
				s.end(EventSessionEnded)
			} else {
				s.reload()
			}
		}
	case usersColl:
		if a.UserID != "" && c.Key == a.UserID {
			if removed {
				s.end(EventSessionEnded)
			} else {
				s.reload()
			}
		}
	case apiKeysColl:
		if a.KeyID != "" && c.Key == a.KeyID {
			if removed {
				s.end(EventSessionEnded)
			} else {
				s.reload()
			}
		}
	case orgMembersColl:
		if a.UserID != "" && strings.HasSuffix(c.Key, "/"+a.UserID) {
			s.reload()
		}
	case orgsColl:
		if s.inOrg(c.Key) {
			s.reload()
		}
	case orgTeamsColl:
		// A team's key says nothing of its organization: any stream with memberships looks.
		s.mu.Lock()
		member := len(a.Orgs) > 0
		s.mu.Unlock()
		if member {
			s.reload()
		}
	}
}

func (s *Stream) inOrg(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.access.Orgs {
		if o == id {
			return true
		}
	}
	return false
}

// end tells the client its stream is over, and stops delivering to it once that is written.
func (s *Stream) end(event string) {
	s.mu.Lock()
	if s.closed || s.ending {
		s.mu.Unlock()
		return
	}
	s.ending = true
	s.queue = s.queue[:0]
	s.hub.charge(-int64(s.size))
	s.size = 0
	s.queue = append(s.queue, Frame{Event: event, Data: []byte("{}")})
	s.size = len(event) + 18
	s.mu.Unlock()
	s.hub.charge(int64(len(event) + 18))
	s.wake()
}

// reload re-reads the access, at most one reload at a time and not more often than
// minReload: changes that arrive meanwhile cause one more.
func (s *Stream) reload() {
	s.mu.Lock()
	if s.closed || s.ending || s.access.Reload == nil {
		s.mu.Unlock()
		return
	}
	if s.reloading {
		s.dirty = true
		s.mu.Unlock()
		return
	}
	s.reloading = true
	s.mu.Unlock()
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			res, err := s.access.Reload(ctx)
			cancel()
			s.applyReload(res, err)
			time.Sleep(minReload)
			s.mu.Lock()
			if !s.dirty || s.closed || s.ending {
				s.reloading, s.dirty = false, false
				s.mu.Unlock()
				return
			}
			s.dirty = false
			s.mu.Unlock()
		}
	}()
}

func (s *Stream) applyReload(res Reloaded, err error) {
	if err != nil {
		// The access can't be read right now. Nothing may be shown on a guess: reload the
		// client's view, which fails closed on its own reads.
		s.resetAll(ReasonAccessChanged)
		return
	}
	if res.Ended {
		s.end(EventSessionEnded)
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.access.Orgs = res.Orgs
	if !res.Expires.IsZero() {
		s.access.Expires = res.Expires
	}
	var changed []string
	var reindex []*Subscription
	for _, sub := range s.subs {
		v, ok := res.Subs[sub.ID]
		if !ok || !v.Changed {
			continue
		}
		sub.Visible = v.Visible
		if !sameKeys(sub.Keys, v.Keys) {
			reindex = append(reindex, sub)
		}
		sub.pendingKeys = v.Keys
		changed = append(changed, sub.ID)
	}
	s.mu.Unlock()
	if len(changed) == 0 {
		return
	}
	s.hub.reindex(reindex)
	s.mu.Lock()
	delta := s.enqueueLocked(s.resetFrame(changed, ReasonAccessChanged))
	s.mu.Unlock()
	s.hub.charge(delta)
	if s.hub.Observer != nil {
		s.hub.Observer.Reset(s.realm, ReasonAccessChanged)
	}
	s.wake()
}

func sameKeys(a, b []Key) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Field != b[i].Field || len(a[i].Values) != len(b[i].Values) {
			return false
		}
		for j := range a[i].Values {
			if keyOf(a[i].Values[j]) != keyOf(b[i].Values[j]) {
				return false
			}
		}
	}
	return true
}

// reindex files subscriptions again under the keys they have after a reload.
func (h *Hub) reindex(subs []*Subscription) {
	if len(subs) == 0 {
		return
	}
	h.unindex(subs)
	h.imu.Lock()
	for _, sub := range subs {
		sub.Keys = sub.pendingKeys
		ci := h.index[sub.collKey()]
		if ci == nil {
			ci = newCollIndex()
			h.index[sub.collKey()] = ci
		}
		ci.add(sub)
	}
	h.imu.Unlock()
}

// expiry closes the stream with `reauth` when the credential's lifetime ends.
func (s *Stream) startExpiry() {
	if s.access == nil {
		return
	}
	s.mu.Lock()
	exp := s.access.Expires
	s.mu.Unlock()
	if exp.IsZero() {
		return
	}
	var arm func(time.Time)
	arm = func(at time.Time) {
		d := time.Until(at)
		if d < 0 {
			d = 0
		}
		t := time.AfterFunc(d, func() {
			s.mu.Lock()
			next := s.access.Expires
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return
			}
			if next.After(at) && time.Now().Before(next) {
				arm(next) // the lifetime was extended meanwhile
				return
			}
			s.end(EventReauth)
		})
		go func() {
			<-s.done
			t.Stop()
		}()
	}
	arm(exp)
}
