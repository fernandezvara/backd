package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/exportpkg"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

const (
	codeImportRefused = "import_refused"
	codeChecksum      = "checksum_mismatch"

	importModeCreate  = "create"
	importModeSkip    = "skip"
	importModeReplace = "replace"

	// importWindow bounds the work on one chunk.
	importWindow = 5 * time.Minute

	// outcomes of one document
	outCreated   = "created"
	outReplaced  = "replaced"
	outSkipped   = "skipped"
	outUnchanged = "unchanged"
	outConflict  = "conflict"
	outInvalid   = "invalid"
)

// importRun is what an import's begin request fixed for its lease.
type importRun struct {
	mode        string
	skipInvalid bool
	dryRun      bool
	sourceRealm string

	mu     sync.Mutex
	owners map[string]bool // user id -> exists, remembered across chunks
	orgs   map[string]bool
}

// importBegin is the body of POST /_admin/import.
type importBegin struct {
	Manifest    *exportpkg.Manifest `json:"manifest"`
	Mode        string              `json:"mode"`
	SkipInvalid bool                `json:"skip_invalid"`
	DryRun      bool                `json:"dry_run"`
}

// import answers POST /v1/{realm}/_admin/import. Without `phase` and `file` it starts an import:
// it checks that the package can be read at all (format, not partial, not a CSV extract, every
// collection configured here), takes the realm's lease and answers it. With `phase=validate`
// or `phase=write` and `part` it takes a chunk of the part's JSON Lines; with `file` (PUT) a
// file's bytes. Needs full admin access, and a session a fresh re-authentication to begin.
func (a *adminAPI) importHandler(w http.ResponseWriter, r *http.Request) {
	if !adminAccessOf(r).Full() {
		rightRefused(w, r, "importing into a realm needs full admin access; you hold: "+adminAccessOf(r).String())
		return
	}
	realm := chi.URLParam(r, "realm")
	q := r.URL.Query()
	switch {
	case q.Get("file") != "" && r.Method == http.MethodPut:
		a.importFile(w, r, realm, q.Get("file"))
	case q.Get("phase") != "":
		a.importChunk(w, r, realm, q.Get("phase"), q.Get("part"))
	default:
		a.importBegin(w, r, realm)
	}
}

func refuse(w http.ResponseWriter, r *http.Request, reason string, details ...Detail) {
	writeError(w, r, http.StatusUnprocessableEntity, codeImportRefused, reason, details...)
}

func (a *adminAPI) importBegin(w http.ResponseWriter, r *http.Request, realm string) {
	caller, _ := callerOf(r)
	if caller.Key == nil && caller.User != nil {
		if err := usersOf(r).CheckReauth(r.Context(), *caller.User, r.Header.Get(reauthHeader)); err != nil {
			authError(w, r, err)
			return
		}
	}
	var in importBegin
	data, err := io.ReadAll(r.Body)
	if err != nil || json.Unmarshal(data, &in) != nil || in.Manifest == nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidJSON, `the body must be {"manifest", "mode"?, "skip_invalid"?, "dry_run"?}`)
		return
	}
	if in.Mode == "" {
		in.Mode = importModeCreate
	}
	if !slices.Contains([]string{importModeCreate, importModeSkip, importModeReplace}, in.Mode) {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "mode", Reason: "must be create, skip or replace"})
		return
	}
	m := in.Manifest
	switch {
	case m.Format != exportpkg.FormatVersion:
		refuse(w, r, fmt.Sprintf("package format %d is not supported (this backd reads format %d)", m.Format, exportpkg.FormatVersion))
		return
	case m.Flags.Partial:
		refuse(w, r, "this package was made with --fields: a partial package can't be imported")
		return
	case m.Flags.CSV:
		refuse(w, r, "a CSV extract can't be imported: it is a flat, lossy view")
		return
	case m.Flags.Auth:
		refuse(w, r, "this package holds authentication data, which this version of backd can't import yet")
		return
	}
	rl := a.reg.Realms[realm]
	var missing []Detail
	fileFields := map[string][]string{}
	for _, db := range m.Databases {
		for _, col := range db.Collections {
			c := rl.Databases[db.Name]
			if c == nil || c.Collections[col.Name] == nil {
				missing = append(missing, Detail{Path: db.Name + "/" + col.Name, Reason: "this collection is not configured in the target realm"})
				continue
			}
			if !exportpkg.SafePart(col.Part) || col.Part != exportpkg.CollectionPart(db.Name, col.Name) {
				missing = append(missing, Detail{Path: db.Name + "/" + col.Name, Reason: "the manifest names an unexpected part"})
				continue
			}
			if files := c.Collections[col.Name].Files; len(files) > 0 {
				fileFields[db.Name+"/"+col.Name] = slices.Sorted(maps.Keys(files))
			}
		}
	}
	if len(missing) > 0 {
		refuse(w, r, "the package holds collections the target realm doesn't have; add them to its configuration first", missing...)
		return
	}
	var warnings []string
	if m.Fingerprint != "" && m.Fingerprint != a.fingerprint {
		warnings = append(warnings, "the package was made from a different configuration than this instance runs; every document is validated against this one")
	}
	if m.Realm != "" && m.Realm != realm {
		warnings = append(warnings, fmt.Sprintf("the package comes from realm %q", m.Realm))
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	now := time.Now()
	l := &exportLease{kind: leaseImport, id: hex.EncodeToString(id), actor: caller.Actor(), expires: now.Add(exportLeaseTTL), scope: realm,
		outcomes: map[string]int64{}, imp: &importRun{mode: in.Mode, skipInvalid: in.SkipInvalid, dryRun: in.DryRun, sourceRealm: m.Realm, owners: map[string]bool{}, orgs: map[string]bool{}}}
	if !a.exports.take(realm, caller.Actor(), r.Header.Get(exportHeader), l, now) {
		writeError(w, r, http.StatusConflict, codeExportRunning, "an export or import of this realm is already running; wait for it to finish (a silent one is released after ten minutes)")
		return
	}
	usersOf(r).Audit(r.Context(), auth.AuditImport, "import:"+realm, map[string]any{
		"mode": in.Mode, "dry_run": in.DryRun, "skip_invalid": in.SkipInvalid, "databases": len(m.Databases), "source_realm": m.Realm,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"lease": l.id, "expires_in": int(exportLeaseTTL.Seconds()), "max_chunk_bytes": a.fns.docs.maxBody / 2,
		"warnings": orStrings(warnings), "file_fields": fileFields,
	})
}

// importDocResult is what one document of a chunk came to.
type importDocResult struct {
	ID       string   `json:"id"`
	Outcome  string   `json:"outcome"`
	Line     int      `json:"line"`
	Problems []Detail `json:"problems,omitempty"`
}

func (a *adminAPI) importChunk(w http.ResponseWriter, r *http.Request, realm, phase, part string) {
	if r.Method != http.MethodPost || (phase != "validate" && phase != "write") {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", Detail{Path: "phase", Reason: "POST with phase validate or write"})
		return
	}
	l := a.leaseKind(w, r, realm, leaseImport)
	if l == nil {
		return
	}
	if phase == "write" && l.imp.dryRun {
		writeError(w, r, http.StatusConflict, codeExportLease, "this import is a dry run: it validates and writes nothing")
		return
	}
	database, name, ok := strings.Cut(strings.TrimSuffix(part, ".jsonl"), "/")
	rl := a.reg.Realms[realm]
	if !ok || !strings.HasSuffix(part, ".jsonl") || rl.Databases[database] == nil || rl.Databases[database].Collections[name] == nil {
		writeError(w, r, http.StatusNotFound, codeNotFound, "no such part in the target realm")
		return
	}
	c := rl.Databases[database].Collections[name]
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(importWindow))
	body, err := io.ReadAll(r.Body)
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		tooLarge(w, r, tooBig.Limit)
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidJSON, "could not read the request body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), importWindow)
	defer cancel()
	results, err := a.importDocs(ctx, r, c, l, body, phase == "write")
	if err != nil {
		storageError(w, r, err)
		return
	}
	counts := map[string]int{}
	detail := []importDocResult{}
	var missingOwners, missingOrgs int
	for _, res := range results.docs {
		counts[res.Outcome]++
		if res.Outcome == outInvalid || res.Outcome == outConflict {
			if len(detail) < 200 {
				detail = append(detail, res)
			}
		}
	}
	missingOwners, missingOrgs = results.missingOwners, results.missingOrgs
	a.exports.count(l, "", 0, 0, int64(len(body)))
	a.exports.mu.Lock()
	for k, n := range counts {
		l.outcomes[k] += int64(n)
	}
	l.outcomes["missing_owners"] += int64(missingOwners)
	l.outcomes["missing_orgs"] += int64(missingOrgs)
	l.documents = ensureMap(l.documents)
	l.documents[database+"/"+name] += int64(len(results.docs))
	a.exports.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"documents": len(results.docs), "counts": counts, "problems": detail,
		"missing_owners": missingOwners, "missing_orgs": missingOrgs,
	})
}

func ensureMap(m map[string]int64) map[string]int64 {
	if m == nil {
		return map[string]int64{}
	}
	return m
}

type importChunkResult struct {
	docs                       []importDocResult
	missingOwners, missingOrgs int
}

// importDocs validates the documents of a chunk against the target's configuration and, when
// write is set, writes the ones that pass according to the import's mode.
func (a *adminAPI) importDocs(ctx context.Context, r *http.Request, c *registry.Collection, l *exportLease, body []byte, write bool) (importChunkResult, error) {
	d := a.fns.docs
	repo := d.store.Repository(c)
	run := l.imp
	var out importChunkResult
	type parsed struct {
		doc  map[string]any
		line int
		at   int // index in out.docs
	}
	var docs []*parsed
	for i, line := range bytes.Split(body, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		res := importDocResult{Line: i + 1}
		doc, err := exportpkg.DecodeDocument(line)
		if err != nil {
			res.Outcome, res.Problems = outInvalid, []Detail{{Path: "", Reason: "not a document: " + err.Error()}}
			out.docs = append(out.docs, res)
			continue
		}
		res.ID, _ = doc["id"].(string)
		if problems := a.checkImported(c, doc); len(problems) > 0 {
			res.Outcome, res.Problems = outInvalid, problems
			out.docs = append(out.docs, res)
			continue
		}
		out.docs = append(out.docs, res)
		docs = append(docs, &parsed{doc: doc, line: i + 1, at: len(out.docs) - 1})
	}
	// Which of the valid documents the target already holds.
	existing := map[string]storage.Document{}
	if len(docs) > 0 {
		ids := make([]any, len(docs))
		for i, p := range docs {
			ids[i] = out.docs[p.at].ID
		}
		page, err := repo.List(ctx, storage.Query{Filter: storage.Condition{Field: "id", Op: storage.OpIn, Value: ids}, Sort: []storage.SortField{{Field: "id"}}, Limit: len(ids)})
		if err != nil {
			return out, err
		}
		for _, e := range page.Items {
			existing[e["id"].(string)] = e
		}
	}
	svc := usersOf(r)
	for _, p := range docs {
		res := &out.docs[p.at]
		have, exists := existing[res.ID]
		same := false
		if exists {
			x, _ := exportpkg.EncodeDocument(have)
			y, _ := exportpkg.EncodeDocument(p.doc)
			same = bytes.Equal(x, y)
		}
		switch {
		case exists && same:
			res.Outcome = outUnchanged
		case exists && run.mode == importModeCreate:
			res.Outcome, res.Problems = outConflict, []Detail{{Path: "id", Reason: "a different document with this id exists in the target (use --mode skip or replace)"}}
		case exists && run.mode == importModeSkip:
			res.Outcome = outSkipped
		case exists:
			res.Outcome = outReplaced
		default:
			res.Outcome = outCreated
			if clash, err := a.uniqueClash(ctx, repo, c, p.doc); err != nil {
				return out, err
			} else if clash != "" {
				res.Outcome, res.Problems = outConflict, []Detail{{Path: clash, Reason: "a document with this unique value exists in the target"}}
			}
		}
		if owner := docOwner(p.doc); owner != "" && !run.known(run.owners, owner, func() bool { _, err := svc.UserByID(ctx, owner); return err == nil }) {
			out.missingOwners++
		}
		if meta, _ := p.doc["_meta"].(map[string]any); meta != nil {
			if org, _ := meta["org"].(string); org != "" && !run.known(run.orgs, org, func() bool { return svc.OrgActive(ctx, org) }) {
				out.missingOrgs++
			}
		}
		if !write || (res.Outcome != outCreated && res.Outcome != outReplaced) {
			continue
		}
		doc := p.doc
		resetVersions(c, doc)
		var err error
		if res.Outcome == outCreated {
			err = repo.Create(ctx, doc)
		} else {
			err = repo.Replace(ctx, doc, version(have))
		}
		var ce *storage.ConflictError
		switch {
		case errors.As(err, &ce):
			res.Outcome, res.Problems = outConflict, []Detail{{Path: strings.Join(ce.Fields, ","), Reason: "a document with this unique value exists in the target"}}
		case errors.Is(err, storage.ErrVersionMismatch), errors.Is(err, storage.ErrNotFound):
			res.Outcome, res.Problems = outConflict, []Detail{{Path: "id", Reason: "the document changed in the target while it was imported: run the import again"}}
		case err != nil:
			return out, err
		default:
			d.importedFiles(ctx, c, have, doc, res.Outcome == outReplaced)
		}
	}
	return out, nil
}

// known remembers, per import, whether a user or organization exists in the target.
func (run *importRun) known(cache map[string]bool, id string, lookup func() bool) bool {
	run.mu.Lock()
	ok, seen := cache[id]
	run.mu.Unlock()
	if seen {
		return ok
	}
	ok = lookup()
	run.mu.Lock()
	cache[id] = ok
	run.mu.Unlock()
	return ok
}

// checkImported validates one document from a package against the target's configuration for
// the collection, as the API would a document sent by a client: its id, its _meta, its fields
// against the schema and its files against the file fields.
func (a *adminAPI) checkImported(c *registry.Collection, doc map[string]any) []Detail {
	var problems []Detail
	id, _ := doc["id"].(string)
	switch {
	case id == "":
		return []Detail{{Path: "id", Reason: "is missing"}}
	case c.CustomID != nil:
		if reason := c.CustomID.Check(id); reason != "" {
			problems = append(problems, Detail{Path: "id", Reason: reason})
		}
	default:
		if _, err := xid.FromString(id); err != nil {
			problems = append(problems, Detail{Path: "id", Reason: "this collection generates its ids and this one is not one of them"})
		}
	}
	meta, ok := doc["_meta"].(map[string]any)
	if !ok {
		return append(problems, Detail{Path: "_meta", Reason: "is missing"})
	}
	if _, ok := meta["version"].(int64); !ok {
		problems = append(problems, Detail{Path: "_meta.version", Reason: "must be an integer"})
	}
	for _, k := range []string{"created_at", "updated_at"} {
		if _, ok := meta[k].(time.Time); !ok {
			problems = append(problems, Detail{Path: "_meta." + k, Reason: "must be a date"})
		}
	}
	// The schema sees the document as a client would send it: dates as text, numbers as numbers.
	raw, err := json.Marshal(render(userFields(doc)))
	if err != nil {
		return append(problems, Detail{Path: "", Reason: err.Error()})
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var api map[string]any
	if err := dec.Decode(&api); err != nil {
		return append(problems, Detail{Path: "", Reason: err.Error()})
	}
	if err := c.Schema.Validate(api); err != nil {
		problems = append(problems, validationDetails(err)...)
	}
	for _, name := range sortedKeys(c.Files) {
		f := c.Files[name]
		for _, file := range filesOf(f, doc) {
			fid, _ := file["id"].(string)
			if !exportpkg.SafePart(fid) {
				problems = append(problems, Detail{Path: name, Reason: "a file has no usable id"})
				continue
			}
			if size := fileSize(file); size > f.MaxSize || (a.fns.docs.maxUpload > 0 && size > a.fns.docs.maxUpload) {
				problems = append(problems, Detail{Path: name, Reason: fmt.Sprintf("file %s is larger than this field allows", fid)})
			}
		}
	}
	return problems
}

// uniqueClash names the unique index (as its fields) a document that doesn't exist yet would
// clash with in the target, or "".
func (a *adminAPI) uniqueClash(ctx context.Context, repo storage.Repository, c *registry.Collection, doc map[string]any) (string, error) {
	for _, ix := range c.Indexes {
		if !ix.Unique || ix.TTL {
			continue
		}
		var conds storage.And
		var names []string
		complete := true
		for _, k := range ix.Keys {
			v := valueAt(doc, k.Field)
			switch v.(type) {
			case nil, map[string]any, []any:
				complete = false
			}
			if !complete {
				break
			}
			conds = append(conds, storage.Condition{Field: k.Field, Op: storage.OpEq, Value: v})
			names = append(names, k.Field)
		}
		if !complete || len(conds) == 0 || (len(names) == 1 && names[0] == "id") {
			continue
		}
		page, err := repo.List(ctx, storage.Query{Filter: conds, Sort: []storage.SortField{{Field: "id"}}, Limit: 1})
		if err != nil {
			return "", err
		}
		if len(page.Items) > 0 {
			return strings.Join(names, ","), nil
		}
	}
	return "", nil
}

// resetVersions marks the image versions an exported file had made as waiting again: their
// objects are not in the package, and a worker makes them in the target.
func resetVersions(c *registry.Collection, doc map[string]any) {
	for _, name := range sortedKeys(c.Files) {
		for _, file := range filesOf(c.Files[name], doc) {
			versions, _ := file["versions"].(map[string]any)
			for vn, s := range versions {
				if st, _ := s.(map[string]any); st != nil && st["status"] == versionReady {
					versions[vn] = map[string]any{"status": versionPending}
				}
			}
		}
	}
}

// importedFiles counts the files of a written document in the storage usage and queues the
// image versions it waits for; for a replaced document, files it no longer holds go.
func (d *documents) importedFiles(ctx context.Context, c *registry.Collection, old, doc map[string]any, replaced bool) {
	if len(c.Files) == 0 {
		return
	}
	kept := map[string]bool{}
	if replaced {
		for _, f := range allFiles(c, old) {
			if id, _ := f["id"].(string); id != "" {
				kept[id] = false
			}
		}
	}
	for _, name := range sortedKeys(c.Files) {
		for _, file := range filesOf(c.Files[name], doc) {
			id, _ := file["id"].(string)
			if _, had := kept[id]; had {
				kept[id] = true
				continue
			}
			d.fileAdded(ctx, c, doc["id"].(string), name, docOwner(doc), file)
		}
	}
	if replaced {
		var gone []map[string]any
		for _, f := range allFiles(c, old) {
			if id, _ := f["id"].(string); id != "" && !kept[id] {
				gone = append(gone, f)
			}
		}
		d.filesGone(ctx, c, docOwner(old), gone, "replaced")
	}
}

// deadlineReader pushes the read deadline on with every read, so a long upload isn't cut by
// the server's read timeout while it makes progress.
type deadlineReader struct {
	io.Reader
	rc *http.ResponseController
}

func (d deadlineReader) Read(p []byte) (int, error) {
	_ = d.rc.SetReadDeadline(time.Now().Add(exportWriteWindow))
	_ = d.rc.SetWriteDeadline(time.Now().Add(exportWriteWindow))
	return d.Reader.Read(p)
}

// importFile stores a file's bytes under the key a document of the collection expects, after
// checking them against the SHA-256 the client sends.
func (a *adminAPI) importFile(w http.ResponseWriter, r *http.Request, realm, file string) {
	l := a.leaseKind(w, r, realm, leaseImport)
	if l == nil {
		return
	}
	if l.imp.dryRun {
		writeError(w, r, http.StatusConflict, codeExportLease, "this import is a dry run: it validates and writes nothing")
		return
	}
	parts := strings.Split(file, "/")
	rl := a.reg.Realms[realm]
	sum := strings.ToLower(r.Header.Get("X-Backd-Sha256"))
	if len(parts) != 3 || rl.Databases[parts[0]] == nil || rl.Databases[parts[0]].Collections[parts[1]] == nil || len(rl.Databases[parts[0]].Collections[parts[1]].Files) == 0 || !exportpkg.SafePart(parts[2]) {
		writeError(w, r, http.StatusNotFound, codeNotFound, "no such file field in the target realm")
		return
	}
	if len(sum) != 64 {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", Detail{Path: "X-Backd-Sha256", Reason: "is required: the hex SHA-256 of the file"})
		return
	}
	obj, err := a.fns.docs.objects.For(r.Context(), realm)
	switch {
	case errors.Is(err, errNoStorage):
		writeError(w, r, http.StatusNotFound, codeNotFound, "the target realm has no storage configured (storage: in realm.yaml)")
		return
	case errors.Is(err, errStorageUnavailable):
		writeError(w, r, http.StatusServiceUnavailable, codeStorageUnavailable, err.Error())
		return
	case err != nil:
		adminError(w, r, err)
		return
	}
	key := obj.Key(realm, parts[0], parts[1], parts[2])
	h := sha256.New()
	body := io.TeeReader(deadlineReader{r.Body, http.NewResponseController(w)}, h)
	if err := obj.PutStream(r.Context(), key, body, ""); err != nil {
		logger(r.Context()).Error("import: store a file", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, codeStorageUnavailable, "the storage could not take the file")
		return
	}
	if hex.EncodeToString(h.Sum(nil)) != sum {
		_ = obj.Delete(r.Context(), key)
		writeError(w, r, http.StatusBadRequest, codeChecksum, "the file arrived different from its checksum; it was not kept")
		return
	}
	a.exports.count(l, "", 0, 1, 0)
	w.WriteHeader(http.StatusNoContent)
}

// finishImport answers DELETE /v1/{realm}/_admin/import: releases the lease and audits what
// the import did, by outcome.
func (a *adminAPI) finishImport(w http.ResponseWriter, r *http.Request) {
	if !adminAccessOf(r).Full() {
		rightRefused(w, r, "importing into a realm needs full admin access; you hold: "+adminAccessOf(r).String())
		return
	}
	realm := chi.URLParam(r, "realm")
	l := a.leaseKind(w, r, realm, leaseImport)
	if l == nil {
		return
	}
	a.exports.mu.Lock()
	details := map[string]any{"dry_run": l.imp.dryRun, "mode": l.imp.mode, "files": l.files, "complete": r.URL.Query().Get("complete") == "true"}
	for k, n := range l.outcomes {
		details[k] = n
	}
	a.exports.mu.Unlock()
	a.exports.release(realm, l)
	usersOf(r).Audit(r.Context(), auth.AuditImportDone, "import:"+realm, details)
	w.WriteHeader(http.StatusNoContent)
}
