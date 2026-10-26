package httpapi

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fernandezvara/backd/internal/docutil"
	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/exportpkg"
	"github.com/fernandezvara/backd/internal/fieldcrypt"
	"github.com/fernandezvara/backd/internal/query"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

const (
	codeExportRunning = "export_running"
	codeExportLease   = "export_lease"
	leaseExport       = "export"
	leaseImport       = "import"

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
	kind    string // leaseExport or leaseImport
	actor   string
	expires time.Time
	scope   string
	flags   exportpkg.Flags
	// collections are the ones the export covers, as <database>/<collection>.
	collections []string
	// fields and where narrow every collection of the export (a reporting extract).
	fields []string
	where  string
	// anon is set when the export is anonymized (a staging copy).
	anon *exportAnon
	// An import's options and what it has done, by outcome.
	imp *importRun
	// exportKey seals the realm-sealed fields of authentication data (--include auth).
	exportKey *auth.SecretCipher
	outcomes  map[string]int64
	// What this export has streamed, for the audit record of its end.
	documents map[string]int64
	files     int64
	bytes     int64
}

// exportAnon is what an anonymized export does, fixed by its plan.
type exportAnon struct {
	rules        map[string]*registry.AnonRule // by <database>/<collection>
	dropFiles    bool                          // the files are left out, so the references to them go
	passwordHash string                        // the known password's hash, if one was asked for
}

func (l *exportLease) anonAuth() *auth.AuthAnonymize {
	if l.anon == nil {
		return nil
	}
	return &auth.AuthAnonymize{PasswordHash: l.anon.passwordHash}
}

// exportBody is the JSON body of POST /_admin/export: the options that don't belong in a URL.
type exportBody struct {
	Anonymize       bool   `json:"anonymize"`
	AnonymizeFiles  string `json:"anonymize_files"`
	Config          string `json:"anonymize_config"`
	StagingPassword string `json:"staging_password"`
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
func (e *exportLeases) renew(realm, actor, id, kind string, now time.Time) *exportLease {
	e.mu.Lock()
	defer e.mu.Unlock()
	l, ok := e.leases[realm]
	if !ok || l.id != id || l.actor != actor || l.kind != kind || !now.Before(l.expires) {
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
	// ExportKey seals the realm-sealed fields of authentication data. It is in this answer only:
	// the operator keeps it, and backd never shows it again.
	ExportKey string `json:"export_key,omitempty"`
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
	case r.Method == http.MethodPost:
		a.exportPlan(w, r, realm)
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
	var body exportBody
	if r.Method == http.MethodPost {
		data, err := io.ReadAll(r.Body)
		if err != nil || json.Unmarshal(data, &body) != nil {
			writeError(w, r, http.StatusBadRequest, codeInvalidJSON, `the body must be {"anonymize"?, "anonymize_files"?, "anonymize_config"?, "staging_password"?}`)
			return
		}
	}
	database, names := q.Get("database"), q["collection"]
	if len(names) > 0 && database == "" {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", Detail{Path: "database", Reason: "is required with collection"})
		return
	}
	csvOut := q.Get("format") == "csv"
	if f := q.Get("format"); f != "" && f != "csv" && f != "jsonl" {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", Detail{Path: "format", Reason: "must be jsonl or csv"})
		return
	}
	noFiles := q.Get("files") == "false" || csvOut || (body.Anonymize && body.AnonymizeFiles != "keep")
	var fields []string
	for f := range strings.SplitSeq(q.Get("fields"), ",") {
		if f = strings.TrimSpace(f); f != "" && f != "id" {
			fields = append(fields, f)
		}
	}
	where := q.Get("where")
	includeAuth := false
	for inc := range strings.SplitSeq(q.Get("include"), ",") {
		switch strings.TrimSpace(inc) {
		case "":
		case "auth":
			includeAuth = true
		default:
			writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", Detail{Path: "include", Reason: "must be auth"})
			return
		}
	}
	if includeAuth && (database != "" || len(fields) > 0 || where != "" || csvOut) {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", Detail{Path: "include", Reason: "authentication data is exported with the whole realm: not with a database, collections, fields, a filter or CSV"})
		return
	}
	if !body.Anonymize && (body.AnonymizeFiles != "" || body.Config != "" || body.StagingPassword != "") {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", Detail{Path: "anonymize", Reason: "is required with anonymize_files, anonymize_config and staging_password"})
		return
	}
	if body.AnonymizeFiles != "" && body.AnonymizeFiles != "keep" {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", Detail{Path: "anonymize_files", Reason: "must be keep"})
		return
	}
	if body.StagingPassword != "" && !includeAuth {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", Detail{Path: "staging_password", Reason: "needs include=auth: there are no users without it"})
		return
	}
	if _, portable := usersOf(r).Store.(auth.PortableStore); includeAuth && !portable {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", Detail{Path: "include", Reason: "this realm's store can't export authentication data"})
		return
	}
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
	if details := a.checkExtract(rl, database, names, fields, where); len(details) > 0 {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", details...)
		return
	}
	var anon *exportAnon
	if body.Anonymize {
		overrides, err := registry.ParseAnonymizeConfig([]byte(body.Config))
		if err != nil {
			writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", Detail{Path: "anonymize_config", Reason: err.Error()})
			return
		}
		rules, err := registry.AnonymizeRules(rl, overrides)
		if err != nil {
			var details []Detail
			for e := range strings.SplitSeq(err.Error(), "\n") {
				details = append(details, Detail{Path: "anonymize_config", Reason: e})
			}
			writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", details...)
			return
		}
		anon = &exportAnon{rules: rules, dropFiles: noFiles}
		if body.StagingPassword != "" {
			svc := usersOf(r)
			if len(body.StagingPassword) < svc.Settings.PasswordMinLength {
				writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", Detail{Path: "staging_password", Reason: fmt.Sprintf("must be at least %d characters (the realm's password.min_length)", svc.Settings.PasswordMinLength)})
				return
			}
			if anon.passwordHash, err = svc.Hasher.Hash(r.Context(), body.StagingPassword); err != nil {
				adminError(w, r, err)
				return
			}
		}
	}
	hasStorage := rl.Settings.Storage != nil
	m := &exportpkg.Manifest{
		Format: exportpkg.FormatVersion, BackdVersion: a.version, Realm: realm, CreatedAt: time.Now().UTC(),
		Operator: caller.Actor(), Fingerprint: a.fingerprint,
		Flags:     exportpkg.Flags{NoFiles: noFiles || !hasStorage, Partial: len(fields) > 0, CSV: csvOut},
		Encrypted: []exportpkg.EncryptedField{}, Checksums: map[string]string{},
	}
	m.Flags.Auth = includeAuth
	m.Flags.Anonymized = anon != nil
	var exportKey string
	var keyCipher *auth.SecretCipher
	if includeAuth && anon != nil {
		// Nothing sealed by the realm is in an anonymized copy: no export key.
		for _, name := range auth.AnonymizedCollections() {
			m.Auth = append(m.Auth, exportpkg.Collection{Name: name, Part: exportpkg.AuthPart(name)})
		}
	} else if includeAuth {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			adminError(w, r, err)
			return
		}
		exportKey = base64.RawURLEncoding.EncodeToString(raw)
		keyCipher, _ = auth.ExportKey(exportKey)
		m.AuthKey = keyCipher.KeyID
		for _, name := range auth.PortableCollections {
			m.Auth = append(m.Auth, exportpkg.Collection{Name: name, Part: exportpkg.AuthPart(name)})
		}
	}
	fileFields := map[string][]string{}
	var exported []string
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
			exported = append(exported, dbName+"/"+name)
			entries, err := a.encryptedEntries(r.Context(), usersOf(r), c)
			if err != nil {
				storageError(w, r, err)
				return
			}
			m.Encrypted = append(m.Encrypted, entries...)
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
	l := &exportLease{kind: leaseExport, id: hex.EncodeToString(id), actor: caller.Actor(), expires: now.Add(exportLeaseTTL), scope: scope, flags: m.Flags, fields: fields, where: where, exportKey: keyCipher, anon: anon, collections: exported}
	if !a.exports.take(realm, caller.Actor(), r.Header.Get(exportHeader), l, now) {
		writeError(w, r, http.StatusConflict, codeExportRunning, "an export of this realm is already running; wait for it to finish (a silent one is released after ten minutes)")
		return
	}
	usersOf(r).Audit(r.Context(), auth.AuditExport, "export:"+scope, map[string]any{
		"database": database, "collections": len(names), "files": !m.Flags.NoFiles, "partial": m.Flags.Partial,
		"fields": len(fields), "filtered": where != "", "csv": csvOut, "anonymized": anon != nil,
	})
	if includeAuth {
		usersOf(r).Audit(r.Context(), auth.AuditExportAuth, "export:"+scope, map[string]any{"collections": len(m.Auth), "key_fingerprint": m.AuthKey})
	}
	writeJSON(w, http.StatusOK, exportPlan{Lease: l.id, ExpiresIn: int(exportLeaseTTL.Seconds()), Manifest: m, FileFields: fileFields, ExportKey: exportKey})
}

// checkExtract validates the options of a reporting extract: --where needs exactly one
// collection and is parsed against its schema as the API does; every field path must exist in
// at least one of the collections.
func (a *adminAPI) checkExtract(rl *registry.Realm, database string, names, fields []string, where string) []Detail {
	var details []Detail
	if where != "" {
		if database == "" || len(names) != 1 {
			return []Detail{{Path: "where", Reason: "needs exactly one collection (database and collection)"}}
		}
		if _, err := query.ParseWhere(where, rl.Databases[database].Collections[names[0]].Fields); err != nil {
			details = append(details, queryDetails(err)...)
		}
	}
	for _, f := range fields {
		found := false
		for dbName, db := range rl.Databases {
			if database != "" && dbName != database {
				continue
			}
			for name, c := range db.Collections {
				if (len(names) == 0 || slices.Contains(names, name)) && hasFieldPath(c, f) {
					found = true
				}
			}
		}
		if !found {
			details = append(details, Detail{Path: "fields", Reason: "no such field: " + f})
		}
	}
	return details
}

// hasFieldPath reports whether path names a field of the collection, or an object holding some.
func hasFieldPath(c *registry.Collection, path string) bool {
	if path == "_meta" || strings.HasPrefix(path, "_meta.") {
		return true
	}
	if _, ok := c.Fields[path]; ok {
		return true
	}
	for k := range c.Fields {
		if strings.HasPrefix(k, path+".") {
			return true
		}
	}
	return false
}

// leaseOf checks the export lease of a part request.
func (a *adminAPI) leaseOf(w http.ResponseWriter, r *http.Request, realm string) *exportLease {
	return a.leaseKind(w, r, realm, leaseExport)
}

func (a *adminAPI) leaseKind(w http.ResponseWriter, r *http.Request, realm, kind string) *exportLease {
	caller, _ := callerOf(r)
	l := a.exports.renew(realm, caller.Actor(), r.Header.Get(exportHeader), kind, time.Now())
	if l == nil {
		writeError(w, r, http.StatusConflict, codeExportLease, "no "+kind+" of this realm is running for this administrator: start one and send its lease in "+exportHeader)
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
	if coll, isAuth := strings.CutPrefix(strings.TrimSuffix(part, ".jsonl"), "_system/auth/"); isAuth && strings.HasSuffix(part, ".jsonl") {
		a.exportAuthPart(w, r, l, coll)
		return
	}
	if part == exportpkg.DataKeysName {
		a.exportDataKeys(w, r, l, realm)
		return
	}
	database, name, ok := strings.Cut(strings.TrimSuffix(part, ".jsonl"), "/")
	rl := a.reg.Realms[realm]
	if !ok || !strings.HasSuffix(part, ".jsonl") || rl == nil || rl.Databases[database] == nil || rl.Databases[database].Collections[name] == nil {
		writeError(w, r, http.StatusNotFound, codeNotFound, "no such part in this package")
		return
	}
	c := rl.Databases[database].Collections[name]
	repo := a.fns.docs.store.Repository(c)
	var filter storage.Filter
	if l.where != "" {
		var err error
		if filter, err = query.ParseWhere(l.where, c.Fields); err != nil {
			writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", queryDetails(err)...)
			return
		}
	}
	sc, scannable := repo.(storage.Scanner)
	if filter == nil && !scannable {
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
		// The server never decrypts for an export: encrypted fields leave as they are stored.
		ctx, cancel := context.WithTimeout(storage.WithRawEncrypted(r.Context()), a.fns.docs.opTimeout)
		var batch []storage.Document
		var err error
		if filter == nil {
			batch, err = sc.ScanAfter(ctx, after, exportBatch)
		} else {
			q := storage.Query{Filter: filter, Sort: []storage.SortField{{Field: "id"}}, Limit: exportBatch}
			if after != "" {
				q.After = storage.Condition{Field: "id", Op: storage.OpGt, Value: after}
			}
			var page storage.Page
			page, err = repo.List(ctx, q)
			batch = page.Items
		}
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
			after, _ = doc["id"].(string)
			if l.anon != nil {
				a.anonymizeDocument(ctx, r, c, database+"/"+name, l.anon, doc)
			}
			if len(l.fields) > 0 {
				doc = exportpkg.Select(doc, l.fields)
			}
			line, err := exportpkg.EncodeDocument(doc)
			if err != nil {
				logger(r.Context()).Error("export encode failed", "error", err, "collection", database+"/"+name)
				panic(http.ErrAbortHandler)
			}
			_, _ = bw.Write(line)
			_ = bw.WriteByte('\n')
			size += int64(len(line)) + 1
			docs++
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
	if len(parts) != 3 || rl == nil || rl.Databases[parts[0]] == nil || rl.Databases[parts[0]].Collections[parts[1]] == nil || len(rl.Databases[parts[0]].Collections[parts[1]].Files) == 0 || !fileIDPattern.MatchString(parts[2]) {
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
// releases the realm and writes the audit record with what was streamed. 200 with those totals, and how many second factors and devices could not be carried because the realm's secrets key was not available.
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
	unavailable := l.outcomes["auth_unavailable"]
	details := map[string]any{"collections": len(l.documents), "documents": docs, "files": l.files, "bytes": l.bytes, "complete": r.URL.Query().Get("complete") == "true"}
	if unavailable > 0 {
		details["auth_unavailable"] = unavailable
	}
	a.exports.mu.Unlock()
	a.exports.release(realm, l)
	usersOf(r).Audit(r.Context(), auth.AuditExportDone, "export:"+l.scope, details)
	writeJSON(w, http.StatusOK, map[string]any{"collections": len(l.documents), "documents": docs, "files": l.files, "bytes": l.bytes, "auth_unavailable": unavailable})
}

// exportAuthPart streams a collection of authentication data. Fields sealed with the realm's
// secrets are opened here and sealed again under the export's one-time key; the rest is sent
// as stored (password hashes included, which is why this needs --include auth).
func (a *adminAPI) exportAuthPart(w http.ResponseWriter, r *http.Request, l *exportLease, coll string) {
	allowed := auth.PortableCollections
	if l.anon != nil {
		allowed = auth.AnonymizedCollections()
	}
	if (l.exportKey == nil && l.anon == nil) || !slices.Contains(allowed, coll) {
		writeError(w, r, http.StatusNotFound, codeNotFound, "no such part in this package")
		return
	}
	svc := usersOf(r)
	w.Header().Set("Content-Type", "application/x-ndjson")
	rc := http.NewResponseController(w)
	bw := bufio.NewWriterSize(w, 64<<10)
	var docs, size, unavailable int64
	defer func() {
		a.exports.count(l, "auth/"+coll, docs, 0, size)
		a.exports.mu.Lock()
		if l.outcomes == nil {
			l.outcomes = map[string]int64{}
		}
		l.outcomes["auth_unavailable"] += unavailable
		a.exports.mu.Unlock()
	}()
	after := ""
	for {
		_ = rc.SetWriteDeadline(time.Now().Add(exportWriteWindow))
		ctx, cancel := context.WithTimeout(r.Context(), a.fns.docs.opTimeout)
		batch, lost, last, read, err := svc.ExportAuthDocs(ctx, coll, after, exportBatch, l.exportKey, l.anonAuth())
		cancel()
		if err != nil {
			logger(r.Context()).Error("export of authentication data failed", "error", err, "collection", coll)
			if docs == 0 {
				adminError(w, r, err)
				return
			}
			panic(http.ErrAbortHandler)
		}
		unavailable += int64(lost)
		for _, doc := range batch {
			line, err := exportpkg.EncodeDocument(doc)
			if err != nil {
				panic(http.ErrAbortHandler)
			}
			_, _ = bw.Write(line)
			_ = bw.WriteByte('\n')
			size += int64(len(line)) + 1
			docs++
		}
		if err := bw.Flush(); err != nil {
			return
		}
		_ = rc.Flush()
		after = last
		if read < exportBatch {
			return
		}
	}
}

// anonymizeDocument applies an anonymized export's rules to one document: the collection's
// removed and replaced fields, the addresses of users it holds (rewritten like the users' own),
// and the references to files that are not in the copy.
func (a *adminAPI) anonymizeDocument(ctx context.Context, r *http.Request, c *registry.Collection, key string, anon *exportAnon, doc map[string]any) {
	if rule := anon.rules[key]; rule != nil {
		for _, f := range rule.Remove {
			docutil.RemovePath(doc, f)
		}
		for f, v := range rule.Replace {
			docutil.SetPath(doc, f, v)
		}
		svc := usersOf(r)
		rewrite := func(v any) any {
			email, ok := v.(string)
			if !ok {
				return v
			}
			if u, err := svc.Find(ctx, email); err == nil {
				return auth.AnonymousEmail(u.ID)
			}
			sum := sha256.Sum256([]byte(strings.ToLower(email)))
			return "unknown-" + hex.EncodeToString(sum[:4]) + "@example.invalid"
		}
		for _, f := range rule.Emails {
			switch v := docutil.Get(doc, f).(type) {
			case string:
				docutil.SetPath(doc, f, rewrite(v))
			case []any:
				out := make([]any, len(v))
				for i, e := range v {
					out[i] = rewrite(e)
				}
				docutil.SetPath(doc, f, out)
			}
		}
	}
	if anon.dropFiles {
		for name := range c.Files {
			delete(doc, name)
		}
	}
}

// encryptedEntries lists, for the manifest, the encrypted fields of a collection: those the
// configuration encrypts and those that once were and may still hold encrypted values. The
// documents of a package always hold them as ciphertext; the fingerprint is the field key's.
func (a *adminAPI) encryptedEntries(ctx context.Context, svc *auth.Users, c *registry.Collection) ([]exportpkg.EncryptedField, error) {
	fingerprint := func(key string) string {
		if sec, err := svc.Store.SecretByScope(ctx, "", key); err == nil {
			return sec.Fingerprint
		}
		return ""
	}
	var out []exportpkg.EncryptedField
	seen := map[string]bool{}
	for _, ef := range c.Encrypted {
		seen[ef.Path] = true
		out = append(out, exportpkg.EncryptedField{Database: c.Database, Collection: c.Name, Path: ef.Path, Key: ef.Key, Fingerprint: fingerprint(ef.Key), Form: exportpkg.FormCiphertext})
	}
	ks, ok := svc.Store.(fieldcrypt.Store)
	if !ok {
		return out, nil
	}
	paths, err := ks.DataKeyPaths(ctx, c.Database, c.Name)
	if err != nil {
		return nil, err
	}
	for _, p := range paths {
		if seen[p] {
			continue
		}
		recs, err := ks.DataKeysOf(ctx, c.Database, c.Name, p)
		if err != nil || len(recs) == 0 {
			return out, err
		}
		key := recs[len(recs)-1].Key
		out = append(out, exportpkg.EncryptedField{Database: c.Database, Collection: c.Name, Path: p, Key: key, Fingerprint: fingerprint(key), Form: exportpkg.FormCiphertext})
	}
	return out, nil
}

// exportDataKeys streams the wrapped keys (data and index) of the encrypted fields of the
// collections the export covers, one record per line (`_system/data_keys.jsonl`). They are
// useless without the field keys, which never leave the developer's hands.
func (a *adminAPI) exportDataKeys(w http.ResponseWriter, r *http.Request, l *exportLease, realm string) {
	svc := usersOf(r)
	ks, ok := svc.Store.(fieldcrypt.Store)
	w.Header().Set("Content-Type", "application/x-ndjson")
	if !ok {
		return
	}
	bw := bufio.NewWriter(w)
	for _, key := range l.collections {
		database, name, _ := strings.Cut(key, "/")
		paths, err := ks.DataKeyPaths(r.Context(), database, name)
		if err != nil {
			logger(r.Context()).Error("export of data keys failed", "error", err, "collection", key)
			panic(http.ErrAbortHandler)
		}
		for _, p := range paths {
			recs, err := ks.DataKeysOf(r.Context(), database, name, p)
			if err != nil {
				logger(r.Context()).Error("export of data keys failed", "error", err, "collection", key)
				panic(http.ErrAbortHandler)
			}
			for _, rec := range recs {
				line, err := fieldcrypt.EncodeKeyLine(rec)
				if err != nil {
					panic(http.ErrAbortHandler)
				}
				_, _ = bw.Write(line)
				_ = bw.WriteByte('\n')
			}
		}
	}
	_ = bw.Flush()
}
