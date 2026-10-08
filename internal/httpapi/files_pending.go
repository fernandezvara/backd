package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/rules"
	"github.com/fernandezvara/backd/internal/storage"
)

// Pending uploads: a file uploaded before the document that will hold it exists.
// The upload answers an id and a secret token (only its hash is kept); a later
// create, PUT or PATCH names them in the file field, `{"upload": id, "token": t}`,
// and the document is written with the file's details in the same write, under the
// create or update rule applied to the final document. Required file fields work
// because the file is already there when the document is validated.

const (
	// pendingRateLimit and pendingRateWindow bound how fast one caller may start them.
	pendingRateLimit  = 60
	pendingRateWindow = 10 * time.Minute

	uploadInvalid = "the upload doesn't exist, has expired, was already used, or isn't yours"
)

// pendingTTL is how long an unused pending upload of the collection's realm lives.
func (d *documents) pendingTTL(c *registry.Collection) time.Duration {
	if st := d.reg.Realms[c.Realm].Settings.Storage; st != nil && st.PendingTTL > 0 {
		return st.PendingTTL
	}
	return registry.DefaultPendingTTL
}

// uploadCallerKey is who the open-uploads limit counts an upload for: the user or
// the key, and for an anonymous caller the address.
func uploadCallerKey(r *http.Request) string {
	if c, ok := callerOf(r); ok && c.Subject() != "anonymous" {
		return c.Subject()
	}
	return "ip:" + auth.IPKey(clientIP(r))
}

// uploadPending handles POST …/_files/{field}/uploads: the body is a file, stored as
// a pending upload that a write attaches by naming it with the token answered here.
func (d *documents) uploadPending(w http.ResponseWriter, r *http.Request) {
	c, _ := d.collection(r)
	f := fileField(w, r, c)
	if f == nil {
		return
	}
	svc := d.users(c.Realm)
	if svc == nil {
		notFound(w, r)
		return
	}
	obj, ok := d.objectsFor(w, r, c.Realm)
	if !ok {
		return
	}
	plan := uploadPlan{name: sanitizeFileName(requestedFileName(r)), id: "fl_" + xid.New().String()}
	plan.known = map[string]any{"id": plan.id, "name": plan.name, "size": nil, "type": nil, "sha256": nil, "uploaded_at": nil}
	if f.Upload == registry.UploadDirect { // the body declares the file; the client sends the bytes to the bucket
		d.startDirectPending(w, r, c, f, svc, obj)
		return
	}
	if r.ContentLength >= 0 {
		plan.known["size"] = r.ContentLength
	}
	if !d.pendingGate(w, r, c, f, plan.known) {
		return
	}
	callerKey, ok := d.uploadGate(w, r, svc)
	if !ok {
		return
	}
	if limit := min(f.MaxSize, d.maxUpload); r.ContentLength > limit {
		tooLarge(w, r, limit)
		return
	}
	// The file will belong to the document its creator makes: their quota counts it.
	if plan.quota, ok = d.quotaPlan(w, r, c, ownerOf(r), 0, r.ContentLength); !ok {
		return
	}

	token, hash := auth.NewUploadToken()
	template := auth.FileJournalEntry{Pending: true, TokenHash: hash, CallerKey: callerKey, Owner: ownerOf(r)}
	up, ok := d.storeUpload(w, r, c, f, svc, obj, plan, template)
	if !ok {
		return
	}
	defer up.cancel()
	expires := d.timestamp().Add(d.pendingTTL(c)).UTC()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{
		"upload_id": plan.id, "upload_token": token, "expires_at": expires.Format(timeFormat),
		"file": map[string]any{"name": up.meta["name"], "size": up.meta["size"], "type": up.meta["type"], "sha256": up.meta["sha256"]},
	})
}

// pendingGate decides whether the caller may start a pending upload to the field: a
// signed-in user needs the collection to have a create rule; an anonymous caller
// needs it to say yes to a document that holds just this file (known, what is known
// of it before a byte is stored). It answers and returns false when not.
func (d *documents) pendingGate(w http.ResponseWriter, r *http.Request, c *registry.Collection, f *registry.FileField, known map[string]any) bool {
	a := d.accessFor(r)
	if !a.ruled {
		return true
	}
	switch {
	case c.Rules.For(rules.Create) == nil:
		deny(w, r, a, c, rules.Create, "no create rule")
		return false
	case a.caller.User == nil:
		var data map[string]any
		if f.Multiple {
			data = map[string]any{f.Name: []any{known}}
		} else {
			data = map[string]any{f.Name: known}
		}
		return allowWrite(w, r, a, c, rules.Create, nil, data)
	}
	return true
}

// uploadGate applies the limits of unused uploads: a rate of starting them and how
// many a caller holds. It answers 429 and returns false past either, and otherwise
// returns who the upload is counted for.
func (d *documents) uploadGate(w http.ResponseWriter, r *http.Request, svc *auth.Users) (string, bool) {
	callerKey := uploadCallerKey(r)
	if err := svc.RateLimit(r.Context(), "pending:"+callerKey, pendingRateLimit, pendingRateWindow); err != nil {
		var te *auth.ThrottledError
		if errors.As(err, &te) {
			w.Header().Set("Retry-After", strconv.Itoa(max(int((te.RetryAfter+time.Second-1)/time.Second), 1)))
			writeError(w, r, http.StatusTooManyRequests, codeTooManyRequests, "too many uploads started; retry later")
			return "", false
		}
		storageError(w, r, err)
		return "", false
	}
	open, err := svc.OpenPendingUploads(r.Context(), callerKey)
	if err != nil {
		storageError(w, r, err)
		return "", false
	}
	if open >= auth.MaxOpenPendingUploads {
		w.Header().Set("Retry-After", "60")
		writeError(w, r, http.StatusTooManyRequests, codeTooManyRequests, "this caller already holds "+strconv.Itoa(auth.MaxOpenPendingUploads)+" unused uploads: use or let them expire first")
		return "", false
	}
	return callerKey, true
}

// contextForUpload outlives the request: the work that follows a verified upload
// must finish even if the client goes away.
func contextForUpload(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), uploadTimeout)
}

// fileRef is a write's reference to a pending upload.
type fileRef struct{ upload, token string }

// takeFileRefs removes the file fields from a write's body, as stripFileFields
// does, and returns the pending uploads it names. A value that isn't a reference
// (the file details a client got from a read and sends back) is ignored; a
// reference that is malformed is a validation error.
func takeFileRefs(c *registry.Collection, body map[string]any) (map[string][]fileRef, []Detail) {
	var refs map[string][]fileRef
	var details []Detail
	seen := map[string]bool{}
	for name, f := range c.Files {
		v, present := body[name]
		if !present {
			continue
		}
		delete(body, name)
		var items []any
		switch t := v.(type) {
		case map[string]any:
			items = []any{t}
		case []any:
			items = t
		}
		for _, it := range items {
			m, ok := it.(map[string]any)
			if !ok {
				continue
			}
			up, has := m["upload"]
			if !has {
				continue
			}
			id, _ := up.(string)
			token, _ := m["token"].(string)
			switch {
			case id == "":
				details = append(details, Detail{Path: name, Reason: "upload must be the id of a pending upload"})
			case token == "":
				details = append(details, Detail{Path: name, Reason: "token is required with upload"})
			case seen[id]:
				details = append(details, Detail{Path: name, Reason: "an upload can be used once"})
			default:
				seen[id] = true
				if refs == nil {
					refs = map[string][]fileRef{}
				}
				refs[name] = append(refs[name], fileRef{upload: id, token: token})
			}
		}
		if !f.Multiple && len(refs[name]) > 1 {
			details = append(details, Detail{Path: name, Reason: "this field holds one file"})
		}
	}
	return refs, details
}

// pendingSet is the pending uploads one write attaches.
type pendingSet struct {
	d        *documents
	svc      *auth.Users
	obj      *storage.Objects
	files    map[string][]pendingFile // by field
	replaced []map[string]any         // files a single field lost, set by apply
	claimed  []string
}

type pendingFile struct {
	ref  fileRef
	meta map[string]any
}

// resolvePending checks every referenced upload: it exists, is a pending upload of
// this collection and field that finished and hasn't expired, the token matches and,
// when a signed-in user made it, the caller is that user. Any other answer is the
// same 400, so nothing says which part was wrong. It answers and returns false when
// one fails.
func (d *documents) resolvePending(w http.ResponseWriter, r *http.Request, c *registry.Collection, refs map[string][]fileRef) (*pendingSet, bool) {
	svc := d.users(c.Realm)
	if svc == nil {
		notFound(w, r)
		return nil, false
	}
	obj, ok := d.objectsFor(w, r, c.Realm)
	if !ok {
		return nil, false
	}
	p := &pendingSet{d: d, svc: svc, obj: obj, files: map[string][]pendingFile{}}
	caller, _ := callerOf(r)
	now := d.now()
	var details []Detail
	for _, name := range sortedKeys(refs) {
		for _, ref := range refs[name] {
			e, err := svc.PendingUpload(r.Context(), ref.upload)
			if err != nil && !errors.Is(err, auth.ErrNotFound) {
				storageError(w, r, err)
				return nil, false
			}
			good := err == nil && e.Database == c.Database && e.Collection == c.Name && e.Field == name && e.Status == auth.JournalStored &&
				e.PendingUntil.After(now) && subtle.ConstantTimeCompare([]byte(e.TokenHash), []byte(auth.HashUploadToken(ref.token))) == 1 &&
				(e.Owner == "" || (caller.User != nil && caller.User.User.ID == e.Owner))
			if !good {
				details = append(details, Detail{Path: name, Reason: uploadInvalid})
				continue
			}
			meta := map[string]any{
				"id": e.ID, "name": e.Name, "size": e.Size, "type": e.Type, "sha256": e.SHA256, "uploaded_at": e.UploadedAt.UTC().Format(timeFormat)}
			p.files[name] = append(p.files[name], pendingFile{ref: ref, meta: withImage(meta, e.Image)})
		}
	}
	if len(details) > 0 {
		writeError(w, r, http.StatusBadRequest, codeValidation, "a file upload can't be used", details...)
		return nil, false
	}
	return p, true
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// apply puts the pending files into doc (the user fields a write will store):
// replacing a single field's file, appending to a multiple one's after what current
// holds. A multiple field over max_files is answered 409, like an upload would, and
// false returned. The files a single field loses are in p.replaced.
func (p *pendingSet) apply(w http.ResponseWriter, r *http.Request, c *registry.Collection, doc, current map[string]any) bool {
	p.replaced = nil
	for _, name := range sortedKeys(p.files) {
		f := c.Files[name]
		existing := filesOf(f, current)
		if f.Multiple {
			list := make([]any, 0, len(existing)+len(p.files[name]))
			for _, e := range existing {
				list = append(list, e)
			}
			for _, pf := range p.files[name] {
				list = append(list, pf.meta)
			}
			if len(list) > f.MaxFiles {
				writeError(w, r, http.StatusConflict, codeTooManyFiles, "this field would hold more than "+strconv.Itoa(f.MaxFiles)+" files (max_files)")
				return false
			}
			doc[name] = list
			continue
		}
		doc[name] = p.files[name][0].meta
		p.replaced = append(p.replaced, existing...)
	}
	// What the document will hold must fit the quotas: the uploads were made earlier,
	// when there may have been room that others have used since.
	var added int64
	for _, files := range p.files {
		for _, pf := range files {
			added += fileSize(pf.meta)
		}
	}
	owner := ownerOf(r)
	if current != nil {
		owner = docOwner(current)
	}
	return p.d.quotaAllows(w, r, c, owner, added, fileSizes(p.replaced))
}

// claim takes every pending upload, once, for the document the write is about to
// store: another write naming the same upload gets nothing. It answers 400 and
// releases what it took when one is gone.
func (p *pendingSet) claim(w http.ResponseWriter, r *http.Request, docID string) bool {
	ctx := r.Context()
	p.claimed = nil
	for _, name := range sortedKeys(p.files) {
		for _, pf := range p.files[name] {
			_, err := p.svc.ClaimPendingUpload(ctx, pf.ref.upload, pf.ref.token, docID)
			if err != nil {
				p.release(r)
				if errors.Is(err, auth.ErrNotFound) {
					writeError(w, r, http.StatusBadRequest, codeValidation, "a file upload can't be used", Detail{Path: name, Reason: uploadInvalid})
				} else {
					storageError(w, r, err)
				}
				return false
			}
			p.claimed = append(p.claimed, pf.ref.upload)
		}
	}
	return true
}

// release gives the claimed uploads back: the write didn't happen.
func (p *pendingSet) release(r *http.Request) {
	for _, id := range p.claimed {
		_ = p.svc.ReleasePendingUpload(r.Context(), id)
	}
	p.claimed = nil
}

// attached records that the document (owned by owner) references the files, counts
// them, and queues the objects of the files a single field lost.
func (p *pendingSet) attached(r *http.Request, d *documents, c *registry.Collection, docID, owner string) {
	for _, id := range p.claimed {
		_ = p.svc.SetUploadStatus(r.Context(), id, auth.JournalAttached, docID)
	}
	for _, name := range sortedKeys(p.files) {
		for _, pf := range p.files[name] {
			d.fileAdded(r.Context(), c, docID, name, owner, pf.meta)
		}
	}
	d.filesGone(r.Context(), c, owner, p.replaced, "replaced")
	p.claimed = nil
}

// refsError answers a body whose file references are malformed.
func refsError(w http.ResponseWriter, r *http.Request, details []Detail) {
	writeError(w, r, http.StatusBadRequest, codeValidation, "a file reference is not valid", details...)
}
