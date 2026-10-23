package httpapi

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/fieldcrypt"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

// encryptionCollections lists the collections of the realm an encryption request is about:
// those named (database, collection), or every one.
func (a *adminAPI) encryptionCollections(realm, database, collection string) []*registry.Collection {
	rl := a.reg.Realms[realm]
	if rl == nil {
		return nil
	}
	var out []*registry.Collection
	for dn, db := range rl.Databases {
		if database != "" && dn != database {
			continue
		}
		for cn, c := range db.Collections {
			if collection == "" || cn == collection {
				out = append(out, c)
			}
		}
	}
	slices.SortFunc(out, func(x, y *registry.Collection) int {
		return strings.Compare(x.Database+"/"+x.Name, y.Database+"/"+y.Name)
	})
	return out
}

// migrator is the collection's storage as something that can migrate encrypted fields, if it is.
func (a *adminAPI) migrator(c *registry.Collection) (storage.Migrator, bool) {
	if a.fns == nil || a.fns.docs == nil || a.fns.docs.store == nil {
		return nil, false
	}
	m, ok := a.fns.docs.store.Repository(c).(storage.Migrator)
	return m, ok
}

// getEncryption handles GET /encryption: for each collection that encrypts, or once did, how its
// values are stored: plaintext, current and old, blind index coverage, the keys with their
// fingerprints and generations, and the migration that is running. complete says nothing is left
// to migrate.
func (a *adminAPI) getEncryption(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	svc := usersOf(r)
	realm := chi.URLParam(r, "realm")
	if collection := q.Get("collection"); collection != "" && q.Get("database") == "" {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid query parameters", Detail{Path: "database", Reason: "is required with collection"})
		return
	}
	var items []map[string]any
	for _, c := range a.encryptionCollections(realm, q.Get("database"), q.Get("collection")) {
		m, ok := a.migrator(c)
		if !ok {
			continue
		}
		statuses, err := m.EncryptionStatus(r.Context())
		if err != nil {
			storageError(w, r, err)
			return
		}
		if len(statuses) == 0 {
			continue
		}
		complete := true
		fields := make([]map[string]any, 0, len(statuses))
		for _, st := range statuses {
			gens, err := m.Generations(r.Context(), st.Path)
			if err != nil {
				storageError(w, r, err)
				return
			}
			f := a.encryptionField(r, svc, c, st, gens)
			if f["complete"] != true {
				complete = false
			}
			fields = append(fields, f)
		}
		item := map[string]any{"database": c.Database, "collection": c.Name, "fields": fields, "complete": complete, "migration": a.encryptionJob(r, svc, c)}
		items = append(items, item)
	}
	if items == nil {
		items = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"collections": items})
}

func (a *adminAPI) encryptionField(r *http.Request, svc *auth.Users, c *registry.Collection, st storage.FieldStatus, gens []storage.KeyGeneration) map[string]any {
	f := map[string]any{
		"path": st.Path, "configured": st.Configured, "search": st.Search,
		"plaintext": st.Plaintext, "encrypted": st.Encrypted, "current": st.Current, "old": st.Old,
	}
	complete := false
	if st.Configured {
		ef, _ := c.EncryptedAt(st.Path)
		f["key"] = ef.Key
		if sec, err := svc.Store.SecretByScope(r.Context(), "", ef.Key); err == nil {
			f["fingerprint"] = sec.Fingerprint
		}
		complete = st.Plaintext == 0 && st.Old == 0
		if st.Search {
			f["indexed"], f["unindexed"] = st.Indexed, st.Encrypted-st.Indexed
			complete = complete && st.Indexed >= st.Encrypted
		}
	} else {
		complete = st.Encrypted == 0 // no longer encrypted: nothing may be left encrypted
	}
	f["complete"] = complete
	list := make([]map[string]any, 0, len(gens))
	for _, g := range gens {
		e := map[string]any{"kind": g.Kind, "generation": g.Generation, "status": g.Status, "key": g.Key, "wrap_version": g.WrapVersion}
		if g.Kind == fieldcrypt.KindData {
			e["values"] = g.Values
		}
		list = append(list, e)
	}
	f["generations"] = list
	return f
}

// encryptionJob is the latest migration job of a collection, or nil.
func (a *adminAPI) encryptionJob(r *http.Request, svc *auth.Users, c *registry.Collection) any {
	for _, status := range []string{auth.JobRunning, auth.JobQueued, auth.JobDone} {
		jobs, _, err := svc.Jobs(r.Context(), auth.JobFilter{Origin: auth.EncryptOrigin, Status: status, Limit: 50})
		if err != nil {
			continue
		}
		for _, j := range jobs {
			if j.Encrypt != nil && j.Encrypt.Database == c.Database && j.Encrypt.Collection == c.Name {
				out := jobSummaryJSON(j)
				out["counts"], out["dry_run"] = j.Encrypt.EncryptCounts, j.Encrypt.DryRun
				return out
			}
		}
	}
	return nil
}

// readEncryptionBody reads the body of a rekey or a migration.
func readEncryptionBody(w http.ResponseWriter, r *http.Request, spec map[string]string) (encryptionBody, bool) {
	obj, ok := readObject(w, r)
	if !ok {
		return encryptionBody{}, false
	}
	f, ok := fields(w, r, obj, spec)
	if !ok {
		return encryptionBody{}, false
	}
	var in encryptionBody
	in.Database, _ = f["database"].(string)
	in.Collection, _ = f["collection"].(string)
	in.Field, _ = f["field"].(string)
	in.Fields, _ = f["fields"].([]string)
	in.DryRun, _ = f["dry_run"].(bool)
	return in, true
}

type encryptionBody struct {
	Database   string   `json:"database"`
	Collection string   `json:"collection"`
	Field      string   `json:"field"`
	Fields     []string `json:"fields"`
	DryRun     bool     `json:"dry_run"`
}

func (a *adminAPI) encryptionTarget(w http.ResponseWriter, r *http.Request, in encryptionBody) (*registry.Collection, bool) {
	c, ok := a.reg.Collection(chi.URLParam(r, "realm"), in.Database, in.Collection)
	if !ok {
		writeError(w, r, http.StatusNotFound, codeNotFound, "no collection "+in.Database+"/"+in.Collection+" in this realm")
		return nil, false
	}
	return c, true
}

// rekeyField handles POST /encryption/rekey: a new generation of a field's data key, for data
// keys that may have leaked. Values under the old one stay readable until a migration seals
// them again.
func (a *adminAPI) rekeyField(w http.ResponseWriter, r *http.Request) {
	if !needFullWithReauth(w, r, "rekeying a field") {
		return
	}
	in, ok := readEncryptionBody(w, r, map[string]string{"database": "string", "collection": "string", "field": "string"})
	if !ok {
		return
	}
	c, ok := a.encryptionTarget(w, r, in)
	if !ok {
		return
	}
	if _, enc := c.EncryptedAt(in.Field); !enc {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "field", Reason: "is not an encrypted field of " + in.Database + "/" + in.Collection})
		return
	}
	m, ok := a.migrator(c)
	if !ok {
		writeError(w, r, http.StatusConflict, codeConflict, "this storage can't rekey")
		return
	}
	g, err := m.Rekey(r.Context(), in.Field)
	if err != nil {
		storageError(w, r, err)
		return
	}
	usersOf(r).Audit(r.Context(), auth.AuditEncryptRekey, "doc:"+c.Database+"/"+c.Name, map[string]any{"database": c.Database, "collection": c.Name, "field": in.Field, "generation": g.Generation})
	writeJSON(w, http.StatusOK, map[string]any{"database": c.Database, "collection": c.Name, "field": in.Field, "generation": g.Generation, "status": g.Status})
}

// migrateEncryption handles POST /encryption/migrate: it queues the job that brings the
// collection's stored documents in line with its configuration (or, with dry_run, counts what it
// would change).
func (a *adminAPI) migrateEncryption(w http.ResponseWriter, r *http.Request) {
	if !needFullWithReauth(w, r, "migrating encrypted fields") {
		return
	}
	in, ok := readEncryptionBody(w, r, map[string]string{"database": "string", "collection": "string", "fields": "strings?", "dry_run": "bool?"})
	if !ok {
		return
	}
	c, ok := a.encryptionTarget(w, r, in)
	if !ok {
		return
	}
	for _, f := range in.Fields {
		if _, enc := c.EncryptedAt(f); !enc {
			writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "fields", Reason: f + " is not an encrypted field of " + in.Database + "/" + in.Collection})
			return
		}
	}
	caller, _ := callerOf(r)
	svc := usersOf(r)
	job, err := svc.StartEncryptMigration(r.Context(), auth.EncryptJob{Database: c.Database, Collection: c.Name, Fields: in.Fields, DryRun: in.DryRun}, caller.Actor(), requestID(r.Context()))
	var running *auth.ErrEncryptRunning
	switch {
	case errors.As(err, &running):
		writeError(w, r, http.StatusConflict, codeConflict, err.Error())
		return
	case err != nil:
		adminError(w, r, err)
		return
	}
	svc.Audit(r.Context(), auth.AuditEncryptStart, "doc:"+c.Database+"/"+c.Name, map[string]any{
		"database": c.Database, "collection": c.Name, "fields": strings.Join(in.Fields, ","), "dry_run": in.DryRun, "job_id": job.ID,
	})
	writeJSON(w, http.StatusAccepted, jobSummaryJSON(job))
}
