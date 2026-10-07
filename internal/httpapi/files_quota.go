package httpapi

import (
	"net/http"
	"strconv"

	"github.com/fernandezvara/backd/internal/registry"
)

// Storage quotas (storage.quota in realm.yaml): the bytes the files of all documents of a realm,
// and of the documents one user owns, may add up to. They are checked before an upload stores a
// byte, against the running totals of what documents reference; an upload that replaces a single
// field's file only counts the difference. Under concurrent uploads the limit is approximate: a
// few uploads that start together can each fit and together pass it.

const codeQuotaExceeded = "quota_exceeded"

// quota says how much more an owner's files may add: room is that many bytes (-1: no limit)
// and which names the quota that is nearest to running out ("realm" or "user").
type quota struct {
	room  int64
	which string
}

// quotaFor reads the totals and works out the room left, counting freed bytes (the file a
// replace removes) as free.
func (d *documents) quotaFor(r *http.Request, c *registry.Collection, owner string, freed int64) (quota, error) {
	st := d.reg.Realms[c.Realm].Settings.Storage
	q := quota{room: -1}
	if st == nil || (st.QuotaRealm == 0 && st.QuotaUser == 0) {
		return q, nil
	}
	svc := d.users(c.Realm)
	if svc == nil {
		return q, nil
	}
	realm, user, err := svc.StorageUsageOf(r.Context(), owner)
	if err != nil {
		return q, err
	}
	consider := func(limit, used int64, which string) {
		if limit == 0 {
			return
		}
		room := max(limit-used+freed, 0)
		if q.room < 0 || room < q.room {
			q = quota{room: room, which: which}
		}
	}
	consider(st.QuotaRealm, realm.Bytes, "realm")
	if owner != "" {
		consider(st.QuotaUser, user.Bytes, "user")
	}
	return q, nil
}

// exceeded answers 413 quota_exceeded.
func (d *documents) quotaExceeded(w http.ResponseWriter, r *http.Request, c *registry.Collection, which string) {
	st := d.reg.Realms[c.Realm].Settings.Storage
	limit := st.QuotaRealm
	who := "this realm's files"
	if which == "user" {
		limit, who = st.QuotaUser, "this user's files"
	}
	writeError(w, r, http.StatusRequestEntityTooLarge, codeQuotaExceeded, "storing this file would take "+who+" over their quota of "+strconv.FormatInt(limit, 10)+" bytes")
}

// quotaAllows checks that size more bytes fit, answering 413 quota_exceeded when they don't.
func (d *documents) quotaAllows(w http.ResponseWriter, r *http.Request, c *registry.Collection, owner string, size, freed int64) bool {
	q, err := d.quotaFor(r, c, owner, freed)
	if err != nil {
		storageError(w, r, err)
		return false
	}
	if q.room >= 0 && size > q.room {
		d.quotaExceeded(w, r, c, q.which)
		return false
	}
	return true
}

// fileSizes adds up the sizes of file details.
func fileSizes(files []map[string]any) int64 {
	var n int64
	for _, f := range files {
		n += fileSize(f)
	}
	return n
}

// quotaPlan reads the room an upload of size bytes (-1: not known yet) has, answering 413
// quota_exceeded when a size it already knows doesn't fit.
func (d *documents) quotaPlan(w http.ResponseWriter, r *http.Request, c *registry.Collection, owner string, freed, size int64) (quota, bool) {
	q, err := d.quotaFor(r, c, owner, freed)
	if err != nil {
		storageError(w, r, err)
		return q, false
	}
	if q.room >= 0 && size > q.room {
		d.quotaExceeded(w, r, c, q.which)
		return q, false
	}
	return q, true
}
