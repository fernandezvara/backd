package httpapi

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/exportpkg"
	"github.com/fernandezvara/backd/internal/storage"
)

const (
	codeExportRunning = "export_running"
	codeExportLease   = "export_lease"

	// exportHeader carries the export's lease on every part request.
	exportHeader = "X-Backd-Export"
	// exportLeaseTTL is how long an export may stay silent before another can start: the
	// lease is renewed by every request, so only an abandoned export loses it.
	exportLeaseTTL = 10 * time.Minute
	// exportBatch is how many documents are read at a time; memory is bounded by it.
	exportBatch = 500
	// exportWriteWindow is how long one write to the client may take.
	exportWriteWindow = 2 * time.Minute
)

// exportLease is the export running in a realm. There is one per realm and instance: the
// plan takes it, every part request renews it and DELETE (or a silent TTL) releases it.
type exportLease struct {
	id      string
	actor   string
	expires time.Time
	scope   string
	flags   exportpkg.Flags
	// What this export has streamed, for the audit record of its end.
	documents map[string]int64
	files     int64
	bytes     int64
}

// exportLeases is the exports in progress, by realm.
type exportLeases struct {
	mu     sync.Mutex
	leases map[string]*exportLease
}

// take starts an export in the realm. It fails (nil) while another holds the realm, unless
// resume is the id of the lease being retried by the same actor, which replaces it.
func (e *exportLeases) take(realm, actor, resume string, l *exportLease, now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.leases == nil {
		e.leases = map[string]*exportLease{}
	}
	if cur, ok := e.leases[realm]; ok && now.Before(cur.expires) && (cur.id != resume || cur.actor != actor) {
		return false
	}
	e.leases[realm] = l
	return true
}

// renew returns the realm's lease when id is it and the actor holds it, extending it.
func (e *exportLeases) renew(realm, actor, id string, now time.Time) *exportLease {
	e.mu.Lock()
	defer e.mu.Unlock()
	l, ok := e.leases[realm]
	if !ok || l.id != id || l.actor != actor || !now.Before(l.expires) {
		return nil
	}
	l.expires = now.Add(exportLeaseTTL)
	return l
}

func (e *exportLeases) release(realm string, l *exportLease) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.leases[realm] == l {
		delete(e.leases, realm)
	}
}

// count adds to what a lease has streamed.
func (e *exportLeases) count(l *exportLease, collection string, documents, files, bytes int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if collection != "" {
		if l.documents == nil {
			l.documents = map[string]int64{}
		}
		l.documents[collection] += documents
	}
	l.files += files
	l.bytes += bytes
}

// skipPath is the middleware mw for every request but the ones ending in suffix.
func skipPath(suffix string, mw func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		with := mw(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, suffix) {
				next.ServeHTTP(w, r)
				return
			}
			with.ServeHTTP(w, r)
		})
	}
}

// exportPlan is what GET /_admin/export answers: the package's manifest as far as it can be
// known before reading (no checksums, counts estimated), the lease to send with every part,
// and the file fields the CLI looks in for the files to fetch.
type exportPlan struct {
	Lease      string              `json:"lease"`
	ExpiresIn  int                 `json:"expires_in"`
	Manifest   *exportpkg.Manifest `json:"manifest"`
	FileFields map[string][]string `json:"file_fields"`
}

// export answers GET /v1/{realm}/_admin/export. Without `part` or `file` it takes the realm's
// export lease and answers the plan; with them it streams one part of the package: the
// documents of a collection as JSON Lines (`part=<database>/<collection>.jsonl`), or a file's
// object (`file=<database>/<collection>/<file id>`). Only a full administrator may export,
// and a session must carry a fresh re-authentication; data keys never leave the server
// unwrapped and encrypted fields are sent as stored.
func (a *adminAPI) export(w http.ResponseWriter, r *http.Request) {
	if !adminAccessOf(r).Full() {
		rightRefused(w, r, "exporting a realm needs full admin access; you hold: "+adminAccessOf(r).String())
		return
	}
	realm := chi.URLParam(r, "realm")
	q := r.URL.Query()
	switch {
	case q.Get("part") != "":
		a.exportPart(w, r, realm, q.Get("part"))
	case q.Get("file") != "":
		a.exportFile(w, r, realm, q.Get("file"))
	default:
		a.exportPlan(w, r, realm)
	}
}

func (a *adminAPI) exportPlan(w http.ResponseWriter, r *http.Request, realm string) {
	q := r.URL.Query()
	caller, _ := callerOf(r)
	if caller.Key == nil && caller.User != nil {
		if err := usersOf(r).CheckReauth(r.Context(), *caller.User, r.Header.Get(reauthHeader)); err != nil {
			authError(w, r, err)
			return
		}
	}
	database, names := q.Get("database"), q["collection"]
	if len(names) > 0 && database == "" {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", Detail{Path: "database", Reason: "is required with collection"})
		return
	}
	noFiles := q.Get("files") == "false"
	rl := a.reg.Realms[realm]
	if rl == nil || (database != "" && rl.Databases[database] == nil) {
		writeError(w, r, http.StatusNotFound, codeNotFound, "no such database in this realm")
		return
	}
	for _, name := range names {
		if rl.Databases[database].Collections[name] == nil {
			writeError(w, r, http.StatusNotFound, codeNotFound, "no collection "+database+"/"+name+" in this realm")
			return
		}
	}
	hasStorage := rl.Settings.Storage != nil
	m := &exportpkg.Manifest{
		Format: exportpkg.FormatVersion, BackdVersion: a.version, Realm: realm, CreatedAt: time.Now().UTC(),
		Operator: caller.Actor(), Fingerprint: a.fingerprint,
		Flags:     exportpkg.Flags{NoFiles: noFiles || !hasStorage},
		Encrypted: []exportpkg.EncryptedField{}, Checksums: map[string]string{},
	}
	fileFields := map[string][]string{}
	scope := realm
	if database != "" {
		scope += "/" + database
	}
	if len(names) > 0 {
		scope += " " + strings.Join(names, ",")
	}
	for _, dbName := range slices.Sorted(maps.Keys(rl.Databases)) {
		if database != "" && dbName != database {
			continue
		}
		db := exportpkg.Database{Name: dbName}
		for _, name := range slices.Sorted(maps.Keys(rl.Databases[dbName].Collections)) {
			if len(names) > 0 && !slices.Contains(names, name) {
				continue
			}
			c := rl.Databases[dbName].Collections[name]
			col := exportpkg.Collection{Name: name, Part: exportpkg.CollectionPart(dbName, name)}
			if sc, ok := a.fns.docs.store.Repository(c).(storage.Scanner); ok {
				if n, err := sc.EstimatedCount(r.Context()); err == nil {
					col.Documents = n
				}
			}
			db.Collections = append(db.Collections, col)
			if !m.Flags.NoFiles && len(c.Files) > 0 {
				fileFields[dbName+"/"+name] = slices.Sorted(maps.Keys(c.Files))
			}
		}
		m.Databases = append(m.Databases, db)
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	now := time.Now()
	l := &exportLease{id: hex.EncodeToString(id), actor: caller.Actor(), expires: now.Add(exportLeaseTTL), scope: scope, flags: m.Flags}
	if !a.exports.take(realm, caller.Actor(), r.Header.Get(exportHeader), l, now) {
		writeError(w, r, http.StatusConflict, codeExportRunning, "an export of this realm is already running; wait for it to finish (a silent one is released after ten minutes)")
		return
	}
	usersOf(r).Audit(r.Context(), auth.AuditExport, "export:"+scope, map[string]any{
		"database": database, "collections": len(names), "files": !m.Flags.NoFiles, "partial": m.Flags.Partial,
	})
	writeJSON(w, http.StatusOK, exportPlan{Lease: l.id, ExpiresIn: int(exportLeaseTTL.Seconds()), Manifest: m, FileFields: fileFields})
}

// leaseOf checks the export lease of a part request.
func (a *adminAPI) leaseOf(w http.ResponseWriter, r *http.Request, realm string) *exportLease {
	caller, _ := callerOf(r)
	l := a.exports.renew(realm, caller.Actor(), r.Header.Get(exportHeader), time.Now())
	if l == nil {
		writeError(w, r, http.StatusConflict, codeExportLease, "no export of this realm is running for this administrator: start one with GET /_admin/export and send its lease in "+exportHeader)
	}
	return l
}

// exportPart streams the documents of one collection, soft-deleted ones included, in pages
// so memory doesn't depend on the collection's size. Each line is a document in the API
// representation with typed values in Extended JSON.
func (a *adminAPI) exportPart(w http.ResponseWriter, r *http.Request, realm, part string) {
	l := a.leaseOf(w, r, realm)
	if l == nil {
		return
	}
	database, name, ok := strings.Cut(strings.TrimSuffix(part, ".jsonl"), "/")
	rl := a.reg.Realms[realm]
	if !ok || !strings.HasSuffix(part, ".jsonl") || rl == nil || rl.Databases[database] == nil || rl.Databases[database].Collections[name] == nil {
		writeError(w, r, http.StatusNotFound, codeNotFound, "no such part in this package")
		return
	}
	c := rl.Databases[database].Collections[name]
	sc, ok := a.fns.docs.store.Repository(c).(storage.Scanner)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "this storage can't be scanned")
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	rc := http.NewResponseController(w)
	bw := bufio.NewWriterSize(w, 64<<10)
	after := r.URL.Query().Get("after")
	var docs, size int64
	defer func() { a.exports.count(l, database+"/"+name, docs, 0, size) }()
	for {
		_ = rc.SetWriteDeadline(time.Now().Add(exportWriteWindow))
		ctx, cancel := context.WithTimeout(r.Context(), a.fns.docs.opTimeout)
		batch, err := sc.ScanAfter(ctx, after, exportBatch)
		cancel()
		if err != nil {
			// The status line is gone once bytes were sent: cut the stream, which the CLI
			// sees as a failed part (its checksum never matches) and retries.
			logger(r.Context()).Error("export read failed", "error", err, "collection", database+"/"+name)
			if docs == 0 {
				storageError(w, r, err)
			}
			panic(http.ErrAbortHandler)
		}
		for _, doc := range batch {
			line, err := exportpkg.EncodeDocument(doc)
			if err != nil {
				logger(r.Context()).Error("export encode failed", "error", err, "collection", database+"/"+name)
				panic(http.ErrAbortHandler)
			}
			_, _ = bw.Write(line)
			_ = bw.WriteByte('\n')
			size += int64(len(line)) + 1
			docs++
			after, _ = doc["id"].(string)
		}
		if err := bw.Flush(); err != nil {
			return
		}
		_ = rc.Flush()
		if len(batch) < exportBatch {
			return
		}
	}
}

// exportFile streams a file's object through backd's storage client.
func (a *adminAPI) exportFile(w http.ResponseWriter, r *http.Request, realm, file string) {
	l := a.leaseOf(w, r, realm)
	if l == nil {
		return
	}
	parts := strings.Split(file, "/")
	rl := a.reg.Realms[realm]
	if len(parts) != 3 || rl == nil || rl.Databases[parts[0]] == nil || rl.Databases[parts[0]].Collections[parts[1]] == nil || len(rl.Databases[parts[0]].Collections[parts[1]].Files) == 0 || !exportpkg.SafePart(parts[2]) {
		writeError(w, r, http.StatusNotFound, codeNotFound, "no such file in this package")
		return
	}
	obj, err := a.fns.docs.objects.For(r.Context(), realm)
	switch {
	case errors.Is(err, errNoStorage):
		writeError(w, r, http.StatusNotFound, codeNotFound, "this realm has no storage configured (storage: in realm.yaml)")
		return
	case errors.Is(err, errStorageUnavailable):
		writeError(w, r, http.StatusServiceUnavailable, codeStorageUnavailable, err.Error())
		return
	case err != nil:
		adminError(w, r, err)
		return
	}
	body, info, err := obj.Get(r.Context(), obj.Key(realm, parts[0], parts[1], parts[2]), "")
	if errors.Is(err, storage.ErrObjectNotFound) {
		writeError(w, r, http.StatusNotFound, codeNotFound, "the object of this file is not in the storage")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, codeStorageUnavailable, "the storage could not be read")
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	if info.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	}
	rc := http.NewResponseController(w)
	buf := make([]byte, 256<<10)
	var n int64
	for {
		_ = rc.SetWriteDeadline(time.Now().Add(exportWriteWindow))
		k, rerr := body.Read(buf)
		if k > 0 {
			if _, werr := w.Write(buf[:k]); werr != nil {
				return
			}
			n += int64(k)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			panic(http.ErrAbortHandler)
		}
	}
	a.exports.count(l, "", 0, 1, n)
}

// finishExport answers DELETE /v1/{realm}/_admin/export: the CLI says the export ended, which
// releases the realm and writes the audit record with what was streamed. 204.
func (a *adminAPI) finishExport(w http.ResponseWriter, r *http.Request) {
	if !adminAccessOf(r).Full() {
		rightRefused(w, r, "exporting a realm needs full admin access; you hold: "+adminAccessOf(r).String())
		return
	}
	realm := chi.URLParam(r, "realm")
	l := a.leaseOf(w, r, realm)
	if l == nil {
		return
	}
	a.exports.mu.Lock()
	var docs int64
	for _, n := range l.documents {
		docs += n
	}
	details := map[string]any{"collections": len(l.documents), "documents": docs, "files": l.files, "bytes": l.bytes, "complete": r.URL.Query().Get("complete") == "true"}
	a.exports.mu.Unlock()
	a.exports.release(realm, l)
	usersOf(r).Audit(r.Context(), auth.AuditExportDone, "export:"+l.scope, details)
	w.WriteHeader(http.StatusNoContent)
}
