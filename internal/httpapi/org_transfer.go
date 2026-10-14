package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"

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

// targetTeam checks a team an administrator names: the organization the document ends up in
// must have it.
func (d *documents) targetTeam(w http.ResponseWriter, r *http.Request, c *registry.Collection, org, team string) bool {
	svc := d.users(c.Realm)
	if svc == nil {
		notFound(w, r)
		return false
	}
	if !svc.OrgTeamExists(r.Context(), org, team) {
		writeError(w, r, http.StatusNotFound, codeNotFound, "no such team", Detail{Path: "team", Reason: "the organization has no team with this id"})
		return false
	}
	return true
}

// teamBody reads `team` of a transfer: whether it is there, and its id ("" for null).
func teamBody(w http.ResponseWriter, r *http.Request, c *registry.Collection, obj map[string]any) (team string, set, ok bool) {
	v, has := obj["team"]
	if !has {
		return "", false, true
	}
	s, isStr := v.(string)
	switch {
	case v == nil:
		return "", true, true
	case !isStr || s == "":
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "team", Reason: "must be a team id or null"})
	case !c.InTeams():
		writeError(w, r, http.StatusBadRequest, codeTeamNotAllowed, "this collection has no teams", Detail{Path: "team", Reason: "documents of this collection don't belong to teams"})
	default:
		return s, true, true
	}
	return "", false, false
}

// orgBody reads `org` (a string, or null) of a request, rejecting what isn't allowed.
func orgBody(w http.ResponseWriter, r *http.Request, obj map[string]any, allowed ...string) (org string, isNull, ok bool) {
	for k := range obj {
		if !slices.Contains(allowed, k) {
			writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: k, Reason: "is not allowed"})
			return "", false, false
		}
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
	team, teamSet, ok := teamBody(w, r, c, obj)
	if !ok {
		return
	}
	if toNone && teamSet && team != "" {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "team", Reason: "a document outside organizations has no team"})
		return
	}
	if teamSet && team == "" && c.Team == registry.TeamRequired {
		writeError(w, r, http.StatusBadRequest, codeTeamRequired, "documents of this collection belong to a team", Detail{Path: "team", Reason: "can't be null in a collection that requires one"})
		return
	}
	if toNone && c.Organization == registry.OrgRequired {
		writeError(w, r, http.StatusBadRequest, codeOrgRequired, "documents of this collection belong to an organization", Detail{Path: "org", Reason: "can't be null in a collection that requires one"})
		return
	}
	if !toNone && !d.targetOrg(w, r, c, org) {
		return
	}
	if team != "" && !d.targetTeam(w, r, c, org, team) {
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
	teamFrom, _ := stored["team"].(string)
	// The team belongs to an organization: moving to another one (or out) leaves the team,
	// which must then be named again when the collection needs one.
	if c.InTeams() && !teamSet && teamFrom != "" && (toNone || org != from) {
		if c.Team == registry.TeamRequired && !toNone {
			writeError(w, r, http.StatusBadRequest, codeTeamRequired, "the team belongs to the organization the document is leaving: name the team it joins in the new one", Detail{Path: "team", Reason: "is required to move a document to another organization"})
			return
		}
		teamSet = true
	}
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
	if teamSet {
		delete(meta, "team")
		if team != "" {
			meta["team"] = team
		}
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
			"team_from": teamFrom, "team_to": team,
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
	org, toNone, ok := orgBody(w, r, obj, "org")
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
