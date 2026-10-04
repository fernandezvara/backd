package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/rules"
	"github.com/fernandezvara/backd/internal/storage"
)

type callerKey struct{}

// callerOf returns the request's caller; ok is false in realms with auth
// disabled, which have no callers.
func callerOf(r *http.Request) (c auth.Caller, ok bool) {
	c, ok = r.Context().Value(callerKey{}).(auth.Caller)
	return c, ok
}

// stampCreate adds the ownership fields of a new document to meta: the
// owner (the signed-in user, or null for API keys) and who created it.
func stampCreate(r *http.Request, meta map[string]any) {
	c, ok := callerOf(r)
	if !ok {
		return
	}
	var owner any
	if c.User != nil && !adminData(r) { // an administrator fixing data doesn't own it
		owner = c.User.User.ID
	}
	meta["owner"] = owner
	meta["created_by"] = c.Subject()
	meta["updated_by"] = c.Subject()
}

// stampUpdate carries the ownership fields over from the stored meta,
// which never change, and records who made this write.
func stampUpdate(r *http.Request, stored, meta map[string]any) {
	for _, k := range []string{"owner", "created_by"} {
		if v, ok := stored[k]; ok {
			meta[k] = v
		}
	}
	if c, ok := callerOf(r); ok {
		meta["updated_by"] = c.Subject()
	}
}

// authorize resolves the caller of data routes in realms with auth
// enabled and puts it in the request context. Credentials that are sent
// but invalid get 401. What the caller may do is decided per operation by
// the collection's access rules (see ruled); API keys bypass them.
// Realms with auth disabled pass through unchanged. On the internal
// listener only callback tokens are accepted (see identify).
func (d *documents) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := r.Context().Value(collectionKey{}).(*registry.Collection)
		caller, hasAuth, ok := d.identify(w, r, c.Realm)
		if !ok {
			return
		}
		if !hasAuth {
			next.ServeHTTP(w, r)
			return
		}
		op := auth.ScopeWrite
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			op = auth.ScopeRead
		}
		if !scopeAllows(w, r, caller, op, c.Database, c.Name) {
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, caller)))
	})
}

// scopeAllows answers 403 and returns false when the caller is an API key
// whose scopes don't grant op on database/name (a collection or a function).
// Keys without scopes, users and functions pass: scopes only narrow keys.
func scopeAllows(w http.ResponseWriter, r *http.Request, caller auth.Caller, op auth.ScopeOp, database, name string) bool {
	if caller.Key == nil || caller.Key.Scopes.Allows(op, database, name) {
		return true
	}
	logger(r.Context()).Debug("API key scope refused", "key", caller.Key.Name, "operation", string(op), "target", database+"/"+name)
	verb := map[auth.ScopeOp]string{auth.ScopeRead: "read", auth.ScopeWrite: "write", auth.ScopeCall: "call"}[op]
	writeError(w, r, http.StatusForbidden, codeForbidden, "this API key's scopes don't allow you to "+verb+" "+database+"/"+name)
	return false
}

// identify resolves who calls realm: hasAuth is false in realms with auth
// disabled (no caller). ok is false when it already answered the request
// (bad credentials, network or on-behalf errors).
//
// On the public listener, callback tokens (bdf_…) are refused: they only
// work on the internal listener, which in turn accepts nothing else.
func (d *documents) identify(w http.ResponseWriter, r *http.Request, realm string) (caller auth.Caller, hasAuth, ok bool) {
	svc := d.users(realm)
	credential := ""
	if h := r.Header.Get("Authorization"); h != "" {
		token, valid := bearerToken(r)
		if !valid {
			unauthenticated(w, r, true, "Authorization must be \"Bearer <token or API key>\"")
			return caller, false, false
		}
		credential = token
	}
	isCallback := strings.HasPrefix(credential, auth.CallbackTokenPrefix)
	if d.internal {
		return d.identifyCallback(w, r, realm, svc, credential)
	}
	if isCallback {
		unauthenticated(w, r, true, "callback tokens are only accepted from functions, on the internal listener")
		return caller, false, false
	}
	if svc == nil {
		return caller, false, true
	}
	// No Authorization header: a realm with cookies takes the session cookie.
	fromCookieJar := false
	if credential == "" {
		if token, src, _ := credentialOf(r, svc.Settings); src == fromCookie {
			credential, fromCookieJar = token, true
		}
	}
	caller, err := svc.Identify(r.Context(), credential)
	if err != nil {
		if fromCookieJar {
			clearSessionCookie(w, r, svc.Settings) // the session is gone: stop sending it
		}
		authError(w, r, err)
		return caller, true, false
	}
	if fromCookieJar && !csrfCheck(w, r, svc.Settings) {
		return caller, true, false
	}
	if !allowedFrom(w, r, caller) {
		return caller, true, false
	}
	if id := r.Header.Get(onBehalfHeader); id != "" {
		if caller, err = svc.OnBehalfOf(r.Context(), caller, id); err != nil {
			onBehalfError(w, r, err)
			return caller, true, false
		}
	}
	setActor(r.Context(), caller.Actor())
	return caller, true, true
}

// identifyCallback accepts only a valid, unexpired callback token for
// realm, and resolves the caller the function acts as.
func (d *documents) identifyCallback(w http.ResponseWriter, r *http.Request, realm string, svc *auth.Users, credential string) (caller auth.Caller, hasAuth, ok bool) {
	claims, err := auth.VerifyCallback(d.callbackKey, credential, d.now())
	if err != nil || claims.Realm != realm {
		unauthenticated(w, r, credential != "", "a valid callback token for this realm is required")
		return caller, false, false
	}
	if r.Header.Get(onBehalfHeader) != "" {
		writeError(w, r, http.StatusBadRequest, codeInvalidHeader, onBehalfHeader+" is not allowed with a callback token")
		return caller, false, false
	}
	setActor(r.Context(), "func:"+claims.Realm+"/"+claims.Function)
	if svc == nil {
		return auth.Caller{Func: auth.FuncCallerOf(claims)}, false, true
	}
	if caller, err = svc.CallbackCaller(r.Context(), claims); err != nil {
		unauthenticated(w, r, true, "the function's caller can no longer act")
		return caller, true, false
	}
	setActor(r.Context(), caller.Actor())
	return caller, true, true
}

// onBehalfHeader lets an API key act as a user: that user's access rules
// and ownership apply.
const onBehalfHeader = "X-Backd-On-Behalf-Of"

func onBehalfError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrNotAllowedOnBehalf):
		writeError(w, r, http.StatusBadRequest, codeInvalidHeader, onBehalfHeader+": "+err.Error())
	case errors.Is(err, auth.ErrNotFound):
		writeError(w, r, http.StatusBadRequest, codeInvalidHeader, onBehalfHeader+": no user with this id in the realm")
	case errors.Is(err, auth.ErrUserDisabled):
		writeError(w, r, http.StatusForbidden, codeForbidden, onBehalfHeader+": "+err.Error())
	default:
		authError(w, r, err)
	}
}

// access is how access rules apply to one request.
type access struct {
	ruled  bool // false: API key or auth disabled, everything is allowed
	caller auth.Caller
	values rules.Values
}

// accessFor returns how rules apply to the request.
func (d *documents) accessFor(r *http.Request) access {
	caller, ok := callerOf(r)
	if !ok || caller.BypassesRules() || adminData(r) {
		return access{}
	}
	a := access{ruled: true, caller: caller, values: rules.Values{Now: d.now().UTC()}}
	if caller.User != nil {
		u := caller.User.User
		a.values.User = &rules.User{ID: u.ID, Email: u.Email, EmailVerified: u.EmailVerified, Roles: u.Roles}
	}
	return a
}

// deny answers a request the rules don't allow: 401 for anonymous callers
// (they may be allowed once signed in), 403 for users. The reason is only
// logged, at debug level.
func deny(w http.ResponseWriter, r *http.Request, a access, c *registry.Collection, op rules.Op, reason string) {
	logger(r.Context()).Debug("access denied",
		"collection", c.Realm+"/"+c.Database+"/"+c.Name, "operation", string(op), "reason", reason)
	if a.caller.User == nil {
		unauthenticated(w, r, false, "authentication required")
		return
	}
	writeError(w, r, http.StatusForbidden, codeForbidden, "no access rule allows this operation")
}

// readFilter returns the filter of documents the caller may read, nil
// when unrestricted. It answers the request and returns false when the
// caller may read nothing at all.
func readFilter(w http.ResponseWriter, r *http.Request, a access, c *registry.Collection) (storage.Filter, bool) {
	f, ok := readableFilter(w, r, a, c)
	return hideDeleted(c, f), ok
}

// readableFilter is the read rule as a filter, before soft-deleted documents
// are taken out (see hideDeleted).
func readableFilter(w http.ResponseWriter, r *http.Request, a access, c *registry.Collection) (storage.Filter, bool) {
	if !a.ruled {
		return nil, true
	}
	rule := c.Rules.For(rules.Read)
	if rule == nil {
		deny(w, r, a, c, rules.Read, "no read rule")
		return nil, false
	}
	f, err := rule.Filter(a.values)
	if err != nil {
		deny(w, r, a, c, rules.Read, "rule error: "+err.Error())
		return nil, false
	}
	switch f {
	case storage.Const(false):
		deny(w, r, a, c, rules.Read, "rule "+rule.Key+" is false")
		return nil, false
	case storage.Const(true):
		return nil, true
	}
	return f, true
}

// allowWrite evaluates the create, update or delete rule. It answers the
// request and returns false when the rule doesn't allow the operation.
func allowWrite(w http.ResponseWriter, r *http.Request, a access, c *registry.Collection, op rules.Op, document, data map[string]any) bool {
	if !a.ruled {
		return true
	}
	rule := c.Rules.For(op)
	if rule == nil {
		deny(w, r, a, c, op, "no "+string(op)+" rule")
		return false
	}
	v := a.values
	v.Document, v.Data = document, data
	ok, err := rule.Allow(v)
	switch {
	case err != nil:
		deny(w, r, a, c, op, "rule error: "+err.Error())
		return false
	case !ok:
		deny(w, r, a, c, op, "rule "+rule.Key+" is false")
		return false
	}
	return true
}

// Soft delete (collection.yaml): DELETE marks a document with
// _meta.deleted_at instead of removing it. Reads, writes and batches never see
// a marked document; the trash is reached only through `deleted=only|include`,
// restore and purge, each under its own rule.
var (
	liveCondition  = storage.Condition{Field: registry.MetaDeletedAt, Op: storage.OpIsNull, Value: true}
	trashCondition = storage.Condition{Field: registry.MetaDeletedAt, Op: storage.OpIsNull, Value: false}
)

// and combines filters, skipping nil (which matches everything).
func and(fs ...storage.Filter) storage.Filter {
	var out storage.And
	for _, f := range fs {
		if f != nil {
			out = append(out, f)
		}
	}
	return storage.Simplify(out)
}

// hideDeleted narrows a read filter to the documents that are not deleted, for
// a collection that soft-deletes; other collections are left alone.
func hideDeleted(c *registry.Collection, f storage.Filter) storage.Filter {
	if c.SoftDelete == nil {
		return f
	}
	return and(f, liveCondition)
}

// trashView says which documents a read asks for.
type trashView int

const (
	viewLive  trashView = iota // not deleted (the default)
	viewTrash                  // only the deleted ones (deleted=only)
	viewAll                    // both (deleted=include)
)

// parseTrash reads the `deleted` query parameter.
func parseTrash(w http.ResponseWriter, r *http.Request, c *registry.Collection) (trashView, bool) {
	q := r.URL.Query()
	if !q.Has("deleted") {
		return viewLive, true
	}
	var view trashView
	switch q.Get("deleted") {
	case "only":
		view = viewTrash
	case "include":
		view = viewAll
	default:
		writeError(w, r, http.StatusBadRequest, codeInvalidQuery, "invalid query parameters", Detail{Path: "deleted", Reason: "must be only or include"})
		return 0, false
	}
	if c.SoftDelete == nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidQuery, "invalid query parameters", Detail{Path: "deleted", Reason: "this collection doesn't soft-delete (see soft_delete in collection.yaml)"})
		return 0, false
	}
	return view, true
}

// restoreFilter is the restore rule as a filter: which deleted documents the
// caller may see and bring back. Without a rule nobody may (callers outside
// the rules, like API keys, see them all).
func restoreFilter(w http.ResponseWriter, r *http.Request, a access, c *registry.Collection) (storage.Filter, bool) {
	if !a.ruled {
		return nil, true
	}
	rule := c.Rules.For(rules.Restore)
	if rule == nil {
		deny(w, r, a, c, rules.Restore, "no restore rule")
		return nil, false
	}
	f, err := rule.Filter(a.values)
	if err != nil {
		deny(w, r, a, c, rules.Restore, "rule error: "+err.Error())
		return nil, false
	}
	if f == storage.Const(false) {
		deny(w, r, a, c, rules.Restore, "rule restore is false")
		return nil, false
	}
	if f == storage.Const(true) {
		return nil, true
	}
	return f, true
}

// viewFilter is the access filter of a read in the given view. The trash needs
// the read rule and the restore rule both.
func viewFilter(w http.ResponseWriter, r *http.Request, a access, c *registry.Collection, view trashView) (storage.Filter, bool) {
	if view == viewLive {
		return readFilter(w, r, a, c)
	}
	readable, ok := readableFilter(w, r, a, c)
	if !ok {
		return nil, false
	}
	restorable, ok := restoreFilter(w, r, a, c)
	if !ok {
		return nil, false
	}
	deleted := and(readable, restorable, trashCondition)
	if view == viewTrash {
		return deleted, true
	}
	return storage.Simplify(storage.Or{and(readable, liveCondition), deleted}), true
}
