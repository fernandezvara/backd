package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/query"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

// Moving documents between organizations is an administrator's act (design/organizations.md
// §10): never an update. Both routes are part of the admin data route.

const codeTeamNotAllowed = "team_not_allowed"

// targetOrg checks an organization an administrator names for a collection. It answers and
// returns false when it can't be used.
func (d *documents) targetOrg(w http.ResponseWriter, r *http.Request, c *registry.Collection, org string) bool {
	svc := d.users(c.Realm)
	if svc == nil {
		notFound(w, r)
		return false
	}
	o, err := svc.AdminOrg(r.Context(), org)
	switch {
	case errors.Is(err, auth.ErrNotFound):
		writeError(w, r, http.StatusNotFound, codeNotFound, "no such organization", Detail{Path: "org", Reason: "no organization has this id"})
		return false
	case err != nil:
		storageError(w, r, err)
		return false
	case o.Status == auth.OrgDeleted:
		writeError(w, r, http.StatusConflict, "org_not_active", "the organization was deleted", Detail{Path: "org", Reason: "is deleted"})
		return false
	}
	return true
}

// orgBody reads `org` (a string, or null) and `team` (only null: collections have no teams
// yet) of a request.
func orgBody(w http.ResponseWriter, r *http.Request, obj map[string]any, allowed ...string) (org string, isNull, ok bool) {
	for k := range obj {
		known := false
		for _, a := range allowed {
			known = known || a == k
		}
		if !known {
			writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: k, Reason: "is not allowed"})
			return "", false, false
		}
	}
	if team, has := obj["team"]; has && team != nil {
		writeError(w, r, http.StatusBadRequest, codeTeamNotAllowed, "this collection has no teams", Detail{Path: "team", Reason: "documents of this collection don't belong to teams"})
		return "", false, false
	}
	v, has := obj["org"]
	switch t := v.(type) {
	case nil:
		if !has {
			writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "org", Reason: "is required (an organization id, or null to take the document out of its organization)"})
			return "", false, false
		}
		return "", true, true
	case string:
		if t == "" {
			writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "org", Reason: "must be an organization id or null"})
			return "", false, false
		}
		return t, false, true
	}
	writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "org", Reason: "must be an organization id or null"})
	return "", false, false
}

// transfer handles POST /_admin/data/{database}/{collection}/{id}/_transfer: it moves one
// document to another organization (or takes it out of its organization, when the collection
// has them optional), bumps its version, honors If-Match and is audited with both.
func (d *documents) transfer(w http.ResponseWriter, r *http.Request) {
	c := r.Context().Value(collectionKey{}).(*registry.Collection)
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	if !c.InOrganizations() {
		writeError(w, r, http.StatusBadRequest, codeOrgNotAllowed, "this collection has no organizations")
		return
	}
	org, toNone, ok := orgBody(w, r, obj, "org", "team")
	if !ok {
		return
	}
	if toNone && c.Organization == registry.OrgRequired {
		writeError(w, r, http.StatusBadRequest, codeOrgRequired, "documents of this collection belong to an organization", Detail{Path: "org", Reason: "can't be null in a collection that requires one"})
		return
	}
	if !toNone && !d.targetOrg(w, r, c, org) {
		return
	}
	cond, err := parseIfMatch(r.Header.Values("If-Match"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidHeader, err.Error())
		return
	}
	_, repo := d.collection(r)
	id := chi.URLParam(r, "id")
	current, err := storage.Fetch(r.Context(), repo, id, hideDeleted(c, nil))
	if err != nil {
		storageError(w, r, err)
		return
	}
	read := version(current)
	if !cond.matches(read) {
		versionMismatch(w, r, read)
		return
	}
	stored, _ := current["_meta"].(map[string]any)
	from, _ := stored["org"].(string)
	meta := map[string]any{}
	for k, v := range stored {
		meta[k] = v
	}
	meta["version"], meta["updated_at"] = read+1, d.timestamp()
	stampUpdate(r, stored, meta) // keeps the owner and records who moved it
	if toNone {
		delete(meta, "org")
	} else {
		meta["org"] = org
	}
	fields := map[string]any{}
	for k, v := range current {
		fields[k] = v
	}
	fields["_meta"] = meta
	switch err := repo.Replace(r.Context(), fields, read); {
	case err == nil:
	case errors.Is(err, storage.ErrVersionMismatch):
		writeError(w, r, http.StatusPreconditionFailed, codeVersionMismatch, "document was modified concurrently; try again")
		return
	default:
		storageError(w, r, err)
		return
	}
	if svc := d.users(c.Realm); svc != nil {
		svc.Audit(r.Context(), auth.AuditDataTransfer, "doc:"+c.Database+"/"+c.Name+"/"+id, map[string]any{
			"database": c.Database, "collection": c.Name, "id": id, "from": from, "to": org,
		})
	}
	w.Header().Set("ETag", etag(version(fields)))
	writeDocument(w, http.StatusOK, fields)
}

// assignOrg handles POST /_admin/data/{database}/{collection}/_assign_org: the adoption of a
// collection that had no organizations. It queues a job that gives the organization to the
// documents that have none and match `where`; ?dry_run=true only counts them.
func (d *documents) assignOrg(w http.ResponseWriter, r *http.Request) {
	c := r.Context().Value(collectionKey{}).(*registry.Collection)
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	if !c.InOrganizations() {
		writeError(w, r, http.StatusBadRequest, codeOrgNotAllowed, "this collection has no organizations")
		return
	}
	where, hasWhere := obj["where"]
	delete(obj, "where")
	org, toNone, ok := orgBody(w, r, obj, "org", "team")
	if !ok {
		return
	}
	if toNone {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "org", Reason: "must be an organization id"})
		return
	}
	if !d.targetOrg(w, r, c, org) {
		return
	}
	whereJSON := ""
	if hasWhere {
		b, err := json.Marshal(where)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "where", Reason: "must be a JSON object"})
			return
		}
		whereJSON = string(b)
	}
	filter, err := query.ParseWhere(whereJSON, c.Fields)
	if err != nil {
		var qe query.Errors
		var details []Detail
		if errors.As(err, &qe) {
			for _, e := range qe {
				details = append(details, Detail{Path: e.Path, Reason: e.Reason})
			}
		}
		writeError(w, r, http.StatusBadRequest, codeInvalidQuery, "invalid where", details...)
		return
	}
	without := storage.Condition{Field: registry.MetaOrg, Op: storage.OpIsNull, Value: true}
	_, repo := d.collection(r)
	page, err := repo.List(r.Context(), storage.Query{Filter: and(filter, without), Limit: 1, Count: true})
	if err != nil {
		storageError(w, r, err)
		return
	}
	var matched int64
	if page.Total != nil {
		matched = *page.Total
	}
	if r.URL.Query().Get("dry_run") == "true" {
		writeJSON(w, http.StatusOK, map[string]any{"matched": matched})
		return
	}
	svc := d.users(c.Realm)
	if svc == nil {
		notFound(w, r)
		return
	}
	job, err := svc.StartAssignOrg(r.Context(), c.Database, auth.AssignOrgJob{Collection: c.Name, Org: org, Where: whereJSON}, auth.ActorOf(r.Context()), requestID(r.Context()))
	switch {
	case errors.Is(err, auth.ErrAssignRunning):
		writeError(w, r, http.StatusConflict, "assign_running", err.Error())
		return
	case err != nil:
		storageError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job.ID, "matched": matched})
}
