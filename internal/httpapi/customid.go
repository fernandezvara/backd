package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

const (
	codeIDRequired   = "id_required"
	codeInvalidID    = "invalid_id"
	codeIDNotAllowed = "id_not_allowed"
	codeIDTaken      = "id_taken"
)

// idProblem is why a create's id was refused.
type idProblem struct {
	code   string
	status int
	reason string
}

// newID reads the id a create asks for from its body (`raw`, which has not lost its system
// fields yet) and checks it against the collection: "" for a collection that generates ids.
// The checks need no storage, so they run first; the caller's create rule and the insert
// follow.
func newID(c *registry.Collection, r *http.Request, raw map[string]any) (string, *idProblem) {
	v, has := raw["id"]
	if c.CustomID == nil {
		if has {
			return "", &idProblem{codeIDNotAllowed, http.StatusBadRequest, "this collection generates its ids: remove id from the document"}
		}
		return "", nil
	}
	if !has || v == nil {
		return "", &idProblem{codeIDRequired, http.StatusBadRequest, "this collection takes the id from the client: send id"}
	}
	id, ok := v.(string)
	if !ok {
		return "", &idProblem{codeInvalidID, http.StatusBadRequest, "id must be a string"}
	}
	if reason := c.CustomID.Check(id); reason != "" {
		return "", &idProblem{codeInvalidID, http.StatusBadRequest, reason}
	}
	if c.CustomID.User {
		caller, _ := callerOf(r)
		if caller.User == nil || caller.User.User.ID != id {
			return "", &idProblem{codeInvalidID, http.StatusBadRequest, "the id must be the id of the user creating the document"}
		}
	}
	return id, nil
}

// isIDConflict reports whether a storage conflict is the document id being taken, not another
// unique index.
func isIDConflict(err error) bool {
	var ce *storage.ConflictError
	return errors.As(err, &ce) && len(ce.Fields) == 1 && ce.Fields[0] == "id"
}

// dupWindow is how long a duplicate attempt counts against a caller.
const dupWindow = time.Minute

// dupLimiter limits how many creates of an id that already exists a caller may attempt: each
// is an answer about which ids exist. It is kept per process, so with several instances a
// caller gets the limit on each.
type dupLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

// blocked reports whether key already made limit duplicate attempts in the last minute, and
// how long until one of them stops counting.
func (l *dupLimiter) blocked(key string, limit int, now time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	hits := l.prune(key, now)
	if len(hits) < limit {
		return 0, false
	}
	return hits[0].Add(dupWindow).Sub(now), true
}

// record counts one duplicate attempt.
func (l *dupLimiter) record(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hits == nil {
		l.hits = map[string][]time.Time{}
	}
	l.hits[key] = append(l.prune(key, now), now)
	if len(l.hits) > 10000 { // callers that stopped trying
		for k := range l.hits {
			if len(l.prune(k, now)) == 0 {
				delete(l.hits, k)
			}
		}
	}
}

func (l *dupLimiter) prune(key string, now time.Time) []time.Time {
	hits := l.hits[key]
	i := 0
	for i < len(hits) && !hits[i].After(now.Add(-dupWindow)) {
		i++
	}
	if i > 0 {
		hits = hits[i:]
		if len(hits) == 0 {
			delete(l.hits, key)
		} else {
			l.hits[key] = hits
		}
	}
	return hits
}

// dupKey identifies who is attempting: the signed-in user or key, or the address for anonymous
// callers.
func dupKey(r *http.Request, c *registry.Collection) string {
	who := "anonymous"
	if caller, ok := callerOf(r); ok {
		who = caller.Subject()
	}
	if who == "anonymous" {
		who = "ip:" + clientIP(r)
	}
	return c.Realm + "/" + c.Database + "/" + c.Name + "|" + who
}

// dupBlocked answers 429 when the caller has used up the collection's duplicate attempts.
func (d *documents) dupBlocked(w http.ResponseWriter, r *http.Request, c *registry.Collection) bool {
	if c.CustomID == nil {
		return false
	}
	wait, blocked := d.dups.blocked(dupKey(r, c), c.CustomID.DuplicateLimit, d.now())
	if !blocked {
		return false
	}
	d.metrics.RateLimited(c.Realm, "duplicate_id")
	secs := int(wait/time.Second) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, r, http.StatusTooManyRequests, codeTooManyRequests, "too many creates of ids that exist; retry later")
	return true
}

// idTaken counts a duplicate attempt and answers 409.
func (d *documents) idTaken(w http.ResponseWriter, r *http.Request, c *registry.Collection, message string, details ...Detail) {
	d.dups.record(dupKey(r, c), d.now())
	d.metrics.DuplicateID(c.Realm, c.Database, c.Name)
	writeError(w, r, http.StatusConflict, codeIDTaken, message, details...)
}
