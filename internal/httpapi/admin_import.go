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
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fernandezvara/backd/internal/docutil"
	"github.com/go-chi/chi/v5"
	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/exportpkg"
	"github.com/fernandezvara/backd/internal/fieldcrypt"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

// fileIDPattern is the shape of the id of a file, which the file fields' schema also demands.
var fileIDPattern = regexp.MustCompile(`^fl_[0-9a-z]{20}$`)

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
	// fields are the encrypted fields the package holds as ciphertext that this server opens with
	// the key the target shares with the source, by <database>/<collection>.
	fields map[string][]importField

	mu     sync.Mutex
	owners map[string]bool // user id -> exists, remembered across chunks
	orgs   map[string]bool
	// dataKeys are the package's wrapped data keys, by id, sent before the documents.
	dataKeys map[string]fieldcrypt.Record
}

// importField is an encrypted field of the package the target opens itself: both realms hold
// the same field key (same fingerprint), so the package's wrapped data keys open with the
// target's copy and the values are sealed again under the target's own keys as they are written.
type importField struct {
	path string
	key  string // the target's field key name
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
	}
	if _, portable := usersOf(r).Store.(auth.PortableStore); m.Flags.Auth && !portable {
		refuse(w, r, "this package holds authentication data and this realm's store can't import it")
		return
	}
	for _, p := range m.Auth {
		if !m.Flags.Auth || !slices.Contains(auth.PortableCollections, p.Name) || p.Part != exportpkg.AuthPart(p.Name) {
			refuse(w, r, "the manifest names an authentication part this backd doesn't know: "+p.Part)
			return
		}
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
		refuse(w, r, "the package can't be imported into this realm as it is (collections the realm doesn't configure need adding to its configuration first)", missing...)
		return
	}
	var warnings []string
	convert, needs := a.planEncrypted(r.Context(), usersOf(r), rl, m)
	if len(needs) > 0 && !in.DryRun {
		refuse(w, r, "the package holds encrypted fields that can't be opened here: give the key each was encrypted with (--field <path>=<key:|env:|file:>), or leave their collections out", needs...)
		return
	}
	for _, n := range needs {
		warnings = append(warnings, n.Path+": "+n.Reason)
	}
	if m.Fingerprint != "" && m.Fingerprint != a.fingerprint {
		warnings = append(warnings, "the package was made from a different configuration than this instance runs; every document is validated against this one")
	}
	switch {
	case !m.Flags.Anonymized && a.env == "staging":
		warnings = append(warnings, "this package is not anonymized and this instance is BACKD_ENV=staging: real personal data would be copied into it (export with --anonymize)")
	case m.Flags.Anonymized && a.env == "production":
		warnings = append(warnings, "this package is anonymized (emails, passwords and some fields replaced) and this instance is BACKD_ENV=production: the documents it holds would replace real data with staging data")
	}
	if m.Realm != "" && m.Realm != realm {
		warnings = append(warnings, fmt.Sprintf("the package comes from realm %q", m.Realm))
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	now := time.Now()
	l := &exportLease{kind: leaseImport, id: hex.EncodeToString(id), actor: caller.Actor(), expires: now.Add(exportLeaseTTL), scope: realm,
		outcomes: map[string]int64{}, imp: &importRun{fields: convert, mode: in.Mode, skipInvalid: in.SkipInvalid, dryRun: in.DryRun, sourceRealm: m.Realm, owners: map[string]bool{}, orgs: map[string]bool{}}}
	if !a.exports.take(realm, caller.Actor(), r.Header.Get(exportHeader), l, now) {
		writeError(w, r, http.StatusConflict, codeExportRunning, "an export or import of this realm is already running; wait for it to finish (a silent one is released after ten minutes)")
		return
	}
	usersOf(r).Audit(r.Context(), auth.AuditImport, "import:"+realm, map[string]any{
		"mode": in.Mode, "dry_run": in.DryRun, "skip_invalid": in.SkipInvalid, "databases": len(m.Databases), "source_realm": m.Realm,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"lease": l.id, "expires_in": int(exportLeaseTTL.Seconds()), "max_chunk_bytes": a.fns.docs.maxBody / 2,
		"warnings": orStrings(warnings), "file_fields": fileFields, "needs_keys": orDetails(needs),
		"keys_needed": len(convert) > 0,
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
	if r.Method != http.MethodPost || (phase != "validate" && phase != "write" && phase != "keys") {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", Detail{Path: "phase", Reason: "POST with phase validate, write or keys"})
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
	if part == exportpkg.DataKeysName && phase == "keys" {
		a.importKeys(w, r, l)
		return
	}
	if coll, isAuth := strings.CutPrefix(strings.TrimSuffix(part, ".jsonl"), "_system/auth/"); isAuth && strings.HasSuffix(part, ".jsonl") && slices.Contains(auth.PortableCollections, coll) {
		a.importAuthChunk(w, r, l, coll, phase == "write")
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
	if tooBig, ok := errors.AsType[*http.MaxBytesError](err); ok {
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
		if problems := a.openEncrypted(ctx, r, c, run, doc); len(problems) > 0 {
			res.Outcome, res.Problems = outInvalid, problems
			out.docs = append(out.docs, res)
			continue
		}
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
	dropFileURLs(c, doc)
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
	problems = append(problems, checkImportedMeta(meta)...)
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
	problems = append(problems, a.checkImportedPublic(c, doc)...)
	return problems
}

// uniqueClash names the unique index (as its fields) a document that doesn't exist yet would
// clash with in the target, or "".
func (a *adminAPI) uniqueClash(ctx context.Context, repo storage.Repository, c *registry.Collection, doc map[string]any) (string, error) {
	for _, ix := range c.Indexes {
		if !ix.Unique || ix.TTL || ix.Blind {
			continue
		}
		var conds storage.And
		var names []string
		complete := true
		for _, k := range ix.Keys {
			v := docutil.Get(doc, k.Field)
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
	if len(parts) != 3 || rl.Databases[parts[0]] == nil || rl.Databases[parts[0]].Collections[parts[1]] == nil || len(rl.Databases[parts[0]].Collections[parts[1]].Files) == 0 || !fileIDPattern.MatchString(parts[2]) {
		writeError(w, r, http.StatusNotFound, codeNotFound, "no such file field in the target realm")
		return
	}
	// The biggest file any of the collection's file fields takes, and never past the instance's
	// upload limit: the bytes are counted as they arrive.
	var limit int64
	for _, f := range rl.Databases[parts[0]].Collections[parts[1]].Files {
		limit = max(limit, f.MaxSize)
	}
	if d := a.fns.docs.maxUpload; d > 0 {
		limit = min(limit, d)
	}
	if len(sum) != 64 {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", Detail{Path: "X-Backd-Sha256", Reason: "is required: the hex SHA-256 of the file"})
		return
	}
	if r.URL.Query().Get("public") == "true" {
		if a.importPublicFile(w, r, realm, parts, r.URL.Query().Get("key_name"), sum, limit) {
			a.exports.count(l, "", 0, 1, 0)
			w.WriteHeader(http.StatusNoContent)
		}
		return
	}
	obj, err := a.fns.docs.objects.For(r.Context(), realm, false)
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
	body := io.TeeReader(deadlineReader{http.MaxBytesReader(w, r.Body, limit), http.NewResponseController(w)}, h)
	if err := obj.PutStream(r.Context(), key, body, ""); err != nil {
		if tooBig, ok := errors.AsType[*http.MaxBytesError](err); ok {
			_ = obj.Delete(r.Context(), key)
			tooLarge(w, r, tooBig.Limit)
			return
		}
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

// importAuthChunk validates or writes a chunk of one collection of authentication data. The
// documents are raw system documents: there is no schema of the app to check them against, so
// the store says what it would do with each (the id, the unique values, the shape its
// collection stores), and the users and organizations it holds are remembered as owners and
// organizations that exist for the documents imported after them.
func (a *adminAPI) importAuthChunk(w http.ResponseWriter, r *http.Request, l *exportLease, coll string, write bool) {
	body, err := io.ReadAll(r.Body)
	if tooBig, ok := errors.AsType[*http.MaxBytesError](err); ok {
		tooLarge(w, r, tooBig.Limit)
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidJSON, "could not read the request body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), importWindow)
	defer cancel()
	var results []importDocResult
	var docs []map[string]any
	var at []int
	for i, line := range bytes.Split(body, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		doc, err := exportpkg.DecodeDocument(line)
		res := importDocResult{Line: i + 1}
		if err != nil {
			res.Outcome, res.Problems = outInvalid, []Detail{{Path: "", Reason: "not a document: " + err.Error()}}
		} else {
			res.ID, _ = doc["id"].(string)
			docs, at = append(docs, doc), append(at, len(results))
		}
		results = append(results, res)
	}
	got, err := usersOf(r).ImportAuthDocs(ctx, coll, docs, l.imp.mode, write)
	if err != nil {
		adminError(w, r, err)
		return
	}
	run := l.imp
	for j, g := range got {
		res := &results[at[j]]
		res.Outcome = g.Outcome
		if g.Reason != "" {
			res.Problems = []Detail{{Path: "", Reason: g.Reason}}
		}
		if g.Outcome == auth.PortableInvalid && !write {
			continue
		}
		switch coll {
		case "users":
			run.mu.Lock()
			run.owners[g.ID] = true
			run.mu.Unlock()
		case "orgs":
			run.mu.Lock()
			run.orgs[g.ID] = true
			run.mu.Unlock()
		}
	}
	counts := map[string]int{}
	problems := []importDocResult{}
	for _, res := range results {
		counts[res.Outcome]++
		if (res.Outcome == outInvalid || res.Outcome == outConflict) && len(problems) < 200 {
			problems = append(problems, res)
		}
	}
	a.exports.mu.Lock()
	for k, n := range counts {
		l.outcomes["auth_"+k] += int64(n)
	}
	a.exports.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"documents": len(results), "counts": counts, "problems": problems, "missing_owners": 0, "missing_orgs": 0})
}

// checkImportedMeta holds `_meta` to what the server itself writes: a package can't smuggle in
// a field the API reserves, or give one the wrong type.
func checkImportedMeta(meta map[string]any) []Detail {
	var problems []Detail
	for _, k := range slices.Sorted(maps.Keys(meta)) {
		v := meta[k]
		switch k {
		case "version", "created_at", "updated_at":
			// checked by the caller
		case "owner", "created_by", "updated_by", "org", "team", "deleted_by":
			if _, ok := v.(string); !ok && v != nil {
				problems = append(problems, Detail{Path: "_meta." + k, Reason: "must be text"})
			}
		case "deleted_at", "purge_at":
			if _, ok := v.(time.Time); !ok {
				problems = append(problems, Detail{Path: "_meta." + k, Reason: "must be a date"})
			}
		case "chain":
			c, _ := v.(map[string]any)
			_, root := c["root"].(string)
			_, depth := c["depth"].(int64)
			if !root || !depth || len(c) != 2 {
				problems = append(problems, Detail{Path: "_meta.chain", Reason: "must be {root, depth}"})
			}
		default:
			problems = append(problems, Detail{Path: "_meta." + k, Reason: "is not a field the server keeps"})
		}
	}
	return problems
}

func orDetails(d []Detail) []Detail {
	if d == nil {
		return []Detail{}
	}
	return d
}

// planEncrypted decides what the import does with each encrypted field the package holds as
// ciphertext. If the target encrypts it with a key that is the same as the package's (same
// fingerprint), the target opens the values itself; otherwise the importer must have opened them
// with the source key before sending, and the field is reported as needing it.
func (a *adminAPI) planEncrypted(ctx context.Context, svc *auth.Users, rl *registry.Realm, m *exportpkg.Manifest) (convert map[string][]importField, needs []Detail) {
	convert = map[string][]importField{}
	for _, e := range m.Encrypted {
		if e.Form != exportpkg.FormCiphertext {
			continue
		}
		where := e.Database + "/" + e.Collection + "." + e.Path
		db := rl.Databases[e.Database]
		if db == nil || db.Collections[e.Collection] == nil {
			continue // reported as a collection the target doesn't have
		}
		c := db.Collections[e.Collection]
		tf, configured := c.EncryptedAt(e.Path)
		same := false
		if configured && e.Fingerprint != "" {
			if sec, err := svc.Store.SecretByScope(ctx, "", tf.Key); err == nil && sec.Type == auth.SecretTypeKey && sec.Fingerprint == e.Fingerprint {
				same = true
			}
		}
		if same {
			convert[e.Database+"/"+e.Collection] = append(convert[e.Database+"/"+e.Collection], importField{path: e.Path, key: tf.Key})
			continue
		}
		target := "the target does not encrypt it"
		if configured {
			target = "the target encrypts it with the key " + tf.Key + " (another one)"
		}
		needs = append(needs, Detail{Path: where, Reason: fmt.Sprintf("encrypted with the key %s (fingerprint %s) and %s", e.Key, orDash(e.Fingerprint), target)})
	}
	return convert, needs
}

func orDash(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// importKeys takes the package's wrapped data keys (`_system/data_keys.jsonl`), which the
// conversion of ciphertext needs. They are useless without the field key and are kept in memory
// for the import only.
func (a *adminAPI) importKeys(w http.ResponseWriter, r *http.Request, l *exportLease) {
	body, err := io.ReadAll(r.Body)
	if tooBig, ok := errors.AsType[*http.MaxBytesError](err); ok {
		tooLarge(w, r, tooBig.Limit)
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidJSON, "could not read the request body")
		return
	}
	n := 0
	for i, line := range bytes.Split(body, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		rec, err := fieldcrypt.DecodeKeyLine(line)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: fmt.Sprintf("line %d", i+1), Reason: err.Error()})
			return
		}
		l.imp.mu.Lock()
		if l.imp.dataKeys == nil {
			l.imp.dataKeys = map[string]fieldcrypt.Record{}
		}
		if len(l.imp.dataKeys) < 1_000_000 {
			l.imp.dataKeys[rec.ID] = rec
		}
		l.imp.mu.Unlock()
		n++
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": n})
}

// openEncrypted opens, in place, the encrypted values of an imported document that the target
// can open itself (see importField), so the rest of the import sees plaintext, like any write;
// writing seals them under the target's own keys. A value it can't open makes the document
// invalid, saying where and never what.
func (a *adminAPI) openEncrypted(ctx context.Context, r *http.Request, c *registry.Collection, run *importRun, doc map[string]any) []Detail {
	var problems []Detail
	id, _ := doc["id"].(string)
	svc := usersOf(r)
	for _, f := range run.fields[c.Database+"/"+c.Name] {
		v, ok := docutil.GetPath(doc, f.path)
		if !ok {
			continue
		}
		ct, sealed := v.(fieldcrypt.Ciphertext)
		if !sealed {
			continue
		}
		problem := func(reason string) { problems = append(problems, Detail{Path: f.path, Reason: reason}) }
		kid, err := fieldcrypt.ParseID(ct)
		if err != nil {
			problem("is not a well-formed encrypted value")
			continue
		}
		run.mu.Lock()
		rec, found := run.dataKeys[fmt.Sprintf("%x", kid)]
		run.mu.Unlock()
		if !found {
			problem("its data key is not in the package")
			continue
		}
		m, err := svc.KeyMaterial(ctx, f.key)
		if err != nil {
			problem("the key " + f.key + " can't be read here (does this instance have the secrets master key?)")
			continue
		}
		plain, err := fieldcrypt.OpenWith(m.Current, rec, run.sourceRealm, ct, id)
		if err != nil {
			problem("can't be opened with the key " + f.key + ": " + err.Error())
			continue
		}
		docutil.SetPath(doc, f.path, plain)
	}
	// A value left encrypted would be stored as binary where the schema expects its plain form.
	return problems
}
