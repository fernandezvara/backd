package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/jsonnum"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/rules"
	"github.com/fernandezvara/backd/internal/storage"
)

// maxBatchOperations bounds one batch request (roadmap F7).
const maxBatchOperations = 100

// batchOp is one parsed and schema-validated operation, ready to run
// inside the transaction. create and replace are already schema-checked;
// patch is checked after merging with the stored document, which isn't
// known until the transaction reads it.
type batchOp struct {
	index      int
	kind       string // create | replace | patch | delete
	collection *registry.Collection
	id         string
	fields     map[string]any // create, replace: the new document's fields
	patch      map[string]any // patch: the raw merge patch
	cond       ifMatch
}

// accessDeniedErr marks a batch operation the caller's rules don't allow.
type accessDeniedErr struct{ anonymous bool }

func (accessDeniedErr) Error() string { return "access denied" }

// patchValidationErr carries a patch operation's post-merge schema
// validation failure (only known once the stored document is read,
// inside the transaction).
type patchValidationErr struct{ err error }

func (e *patchValidationErr) Error() string { return e.err.Error() }
func (e *patchValidationErr) Unwrap() error { return e.err }

// batchIndexErr names which operation of a batch failed.
type batchIndexErr struct {
	index int
	err   error
}

func (e *batchIndexErr) Error() string { return e.err.Error() }
func (e *batchIndexErr) Unwrap() error { return e.err }

// batch handles POST /v1/{realm}/{database}/_batch: create, replace,
// patch and delete across the database's collections, committed in one
// MongoDB transaction. Every operation is validated (and, for ctx.db,
// checked against the caller's rules) before anything is written; the
// first failure aborts the whole batch and names its index.
func (d *documents) batch(w http.ResponseWriter, r *http.Request) {
	realm, database := chi.URLParam(r, "realm"), chi.URLParam(r, "database")
	if !databaseExists(d.reg, realm, database) {
		notFound(w, r)
		return
	}
	body, ok := readObject(w, r)
	if !ok {
		return
	}
	rawOps, ok := body["operations"].([]any)
	if !ok {
		writeError(w, r, http.StatusBadRequest, codeValidation, `"operations" must be an array`, Detail{Path: "operations", Reason: "must be an array"})
		return
	}
	switch {
	case len(rawOps) == 0:
		writeError(w, r, http.StatusBadRequest, codeValidation, "operations must not be empty", Detail{Path: "operations", Reason: "must not be empty"})
		return
	case len(rawOps) > maxBatchOperations:
		writeError(w, r, http.StatusBadRequest, codeValidation,
			fmt.Sprintf("at most %d operations are allowed per batch", maxBatchOperations),
			Detail{Path: "operations", Reason: fmt.Sprintf("has %d, at most %d allowed", len(rawOps), maxBatchOperations)})
		return
	}

	caller, hasAuth, ok := d.identify(w, r, realm)
	if !ok {
		return
	}
	if hasAuth {
		r = r.WithContext(context.WithValue(r.Context(), callerKey{}, caller))
	}
	a := d.accessFor(r)
	claim, ok := d.claimKey(w, r, "_batch", body) // Idempotency-Key, if the request has one
	if !ok {
		return
	}
	defer claim.release(r.Context())

	ops := make([]batchOp, len(rawOps))
	for i, raw := range rawOps {
		op, ok := d.parseBatchOp(w, r, realm, database, i, raw)
		if !ok {
			return
		}
		// Every operation of a batch writes.
		if hasAuth && !scopeAllows(w, r, caller, auth.ScopeWrite, database, op.collection.Name) {
			return
		}
		ops[i] = op
	}

	results := make([]map[string]any, len(ops))
	err := d.store.Transact(r.Context(), func(ctx context.Context) error {
		for i, op := range ops {
			doc, err := d.applyBatchOp(ctx, r, a, op)
			if err != nil {
				return &batchIndexErr{index: i, err: err}
			}
			results[i] = doc
		}
		return nil
	})
	if err != nil {
		writeBatchError(w, r, err)
		return
	}
	rendered := make([]map[string]any, len(results))
	for i, doc := range results {
		rendered[i] = render(doc)
	}
	answer := map[string]any{"results": rendered}
	claim.complete(r.Context(), http.StatusOK, answer)
	writeJSON(w, http.StatusOK, answer)
}

// databaseExists reports whether database is configured in realm.
func databaseExists(reg *registry.Registry, realm, database string) bool {
	rl, ok := reg.Realms[realm]
	if !ok {
		return false
	}
	_, ok = rl.Databases[database]
	return ok
}

// parseBatchOp validates one operation's shape and, for create and
// replace, its document against the collection's schema. It answers the
// request and returns false on the first problem, so a malformed batch
// never opens a transaction.
func (d *documents) parseBatchOp(w http.ResponseWriter, r *http.Request, realm, database string, index int, raw any) (batchOp, bool) {
	idx := strconv.Itoa(index)
	m, ok := raw.(map[string]any)
	if !ok {
		writeError(w, r, http.StatusBadRequest, codeValidation, fmt.Sprintf("operation %d must be a JSON object", index), Detail{Path: idx, Reason: "must be a JSON object"})
		return batchOp{}, false
	}
	kind, _ := m["op"].(string)
	switch kind {
	case "create", "replace", "patch", "delete":
	default:
		writeError(w, r, http.StatusBadRequest, codeValidation, fmt.Sprintf(`operation %d: "op" must be one of create, replace, patch, delete`, index), Detail{Path: idx + ".op", Reason: "must be one of create, replace, patch, delete"})
		return batchOp{}, false
	}
	collName, _ := m["collection"].(string)
	c, ok := d.reg.Collection(realm, database, collName)
	if !ok {
		writeError(w, r, http.StatusNotFound, codeNotFound, fmt.Sprintf("operation %d: no such collection: %s", index, collName), Detail{Path: idx + ".collection", Reason: "no such collection"})
		return batchOp{}, false
	}
	op := batchOp{index: index, kind: kind, collection: c}

	if kind != "create" {
		id, _ := m["id"].(string)
		if id == "" {
			writeError(w, r, http.StatusBadRequest, codeValidation, fmt.Sprintf(`operation %d: "id" is required for %s`, index, kind), Detail{Path: idx + ".id", Reason: "is required"})
			return batchOp{}, false
		}
		op.id = id
	}
	if raw, present := m["if_match"]; present {
		s, ok := raw.(string)
		if !ok {
			writeError(w, r, http.StatusBadRequest, codeInvalidHeader, fmt.Sprintf(`operation %d: "if_match" must be a string`, index), Detail{Path: idx + ".if_match", Reason: "must be a string"})
			return batchOp{}, false
		}
		cond, err := parseIfMatch([]string{s})
		if err != nil {
			writeError(w, r, http.StatusBadRequest, codeInvalidHeader, fmt.Sprintf("operation %d: %v", index, err), Detail{Path: idx + ".if_match", Reason: err.Error()})
			return batchOp{}, false
		}
		op.cond = cond
	}

	switch kind {
	case "create", "replace":
		doc, ok := m["document"].(map[string]any)
		if !ok {
			writeError(w, r, http.StatusBadRequest, codeValidation, fmt.Sprintf(`operation %d: "document" is required for %s`, index, kind), Detail{Path: idx + ".document", Reason: "is required"})
			return batchOp{}, false
		}
		stripSystemFields(doc)
		if err := c.Schema.Validate(doc); err != nil {
			writeError(w, r, http.StatusBadRequest, codeValidation, fmt.Sprintf("operation %d failed schema validation", index), prefixDetails(idx, validationDetails(err))...)
			return batchOp{}, false
		}
		if kind == "create" {
			op.fields = c.DatesToStorage(jsonnum.Normalize(doc).(map[string]any))
		} else {
			op.fields = c.DatesToStorage(jsonnum.Normalize(userFields(doc)).(map[string]any))
		}
	case "patch":
		patch, ok := m["patch"].(map[string]any)
		if !ok {
			writeError(w, r, http.StatusBadRequest, codeValidation, fmt.Sprintf(`operation %d: "patch" is required for patch`, index), Detail{Path: idx + ".patch", Reason: "is required"})
			return batchOp{}, false
		}
		stripSystemFields(patch)
		op.patch = patch
	}
	return op, true
}

// prefixDetails folds a batch operation's index into its validation
// details' paths, so "title" becomes "2.title".
func prefixDetails(idx string, details []Detail) []Detail {
	out := make([]Detail, len(details))
	for i, d := range details {
		if d.Path == "" {
			out[i] = Detail{Path: idx, Reason: d.Reason}
		} else {
			out[i] = Detail{Path: idx + "." + d.Path, Reason: d.Reason}
		}
	}
	return out
}

// applyBatchOp runs one operation inside the batch's transaction: create
// needs no read; replace, patch and delete read the current document
// (through the caller's read rule, so a document they can't read answers
// not-found, not forbidden), check If-Match, check the write rule, then
// write. The returned document is what the API answers for this
// operation (for delete, just its id).
func (d *documents) applyBatchOp(ctx context.Context, r *http.Request, a access, op batchOp) (map[string]any, error) {
	repo := d.store.Repository(op.collection)

	if op.kind == "create" {
		if err := checkWrite(a, op.collection, rules.Create, nil, op.fields); err != nil {
			return nil, err
		}
		now := d.timestamp()
		doc := op.fields
		doc["id"] = xid.New().String()
		meta := map[string]any{"created_at": now, "updated_at": now, "version": int64(1)}
		stampCreate(r, meta)
		doc["_meta"] = meta
		if err := repo.Create(ctx, doc); err != nil {
			return nil, err
		}
		return doc, nil
	}

	filter, err := checkReadFilter(a, op.collection)
	if err != nil {
		return nil, err
	}
	current, err := storage.Fetch(ctx, repo, op.id, filter)
	if err != nil {
		return nil, err
	}
	read := version(current)
	if !op.cond.matches(read) {
		return nil, fmt.Errorf("%w: If-Match does not match the current version %s", storage.ErrVersionMismatch, etag(read))
	}

	switch op.kind {
	case "replace":
		if err := checkWrite(a, op.collection, rules.Update, current, op.fields); err != nil {
			return nil, err
		}
		fields := op.fields
		stored, _ := current["_meta"].(map[string]any)
		meta := map[string]any{"created_at": stored["created_at"], "updated_at": d.timestamp(), "version": read + 1}
		stampUpdate(r, stored, meta)
		fields["id"] = current["id"]
		fields["_meta"] = meta
		if err := repo.Replace(ctx, fields, read); err != nil {
			return nil, err
		}
		return fields, nil

	case "patch":
		merged := mergePatch(timesToStrings(userFields(current)).(map[string]any), deepCopy(op.patch))
		if err := op.collection.Schema.Validate(merged); err != nil {
			return nil, &patchValidationErr{err: err}
		}
		fields := op.collection.DatesToStorage(jsonnum.Normalize(merged).(map[string]any))
		if err := checkWrite(a, op.collection, rules.Update, current, fields); err != nil {
			return nil, err
		}
		stored, _ := current["_meta"].(map[string]any)
		meta := map[string]any{"created_at": stored["created_at"], "updated_at": d.timestamp(), "version": read + 1}
		stampUpdate(r, stored, meta)
		fields["id"] = current["id"]
		fields["_meta"] = meta
		if err := repo.Replace(ctx, fields, read); err != nil {
			return nil, err
		}
		return fields, nil

	default: // delete
		if err := checkWrite(a, op.collection, rules.Delete, current, nil); err != nil {
			return nil, err
		}
		if op.collection.SoftDelete != nil {
			if err := repo.Replace(ctx, d.deletedDocument(r, op.collection, current), read); err != nil {
				return nil, err
			}
			return map[string]any{"id": op.id}, nil
		}
		if err := repo.Delete(ctx, op.id, &read); err != nil {
			return nil, err
		}
		return map[string]any{"id": op.id}, nil
	}
}

// checkReadFilter is readFilter without writing an HTTP response: batch
// folds a denial into its own per-operation error instead.
func checkReadFilter(a access, c *registry.Collection) (storage.Filter, error) {
	f, err := checkReadable(a, c)
	return hideDeleted(c, f), err
}

func checkReadable(a access, c *registry.Collection) (storage.Filter, error) {
	if !a.ruled {
		return nil, nil
	}
	rule := c.Rules.For(rules.Read)
	if rule == nil {
		return nil, accessDeniedErr{anonymous: a.caller.User == nil}
	}
	f, err := rule.Filter(a.values)
	if err != nil {
		return nil, accessDeniedErr{anonymous: a.caller.User == nil}
	}
	switch f {
	case storage.Const(false):
		return nil, accessDeniedErr{anonymous: a.caller.User == nil}
	case storage.Const(true):
		return nil, nil
	}
	return f, nil
}

// checkWrite is allowWrite without writing an HTTP response.
func checkWrite(a access, c *registry.Collection, op rules.Op, document, data map[string]any) error {
	if !a.ruled {
		return nil
	}
	rule := c.Rules.For(op)
	if rule == nil {
		return accessDeniedErr{anonymous: a.caller.User == nil}
	}
	v := a.values
	v.Document, v.Data = document, data
	ok, err := rule.Allow(v)
	if err != nil || !ok {
		return accessDeniedErr{anonymous: a.caller.User == nil}
	}
	return nil
}

// writeBatchError answers a batch request from the transaction's error:
// a *batchIndexErr names which operation failed and why; anything else
// is an unexpected storage or transaction failure.
func writeBatchError(w http.ResponseWriter, r *http.Request, err error) {
	var ie *batchIndexErr
	if !errors.As(err, &ie) {
		storageError(w, r, err)
		return
	}
	idx := strconv.Itoa(ie.index)
	var ve *patchValidationErr
	var ad accessDeniedErr
	switch {
	case errors.As(ie.err, &ve):
		writeError(w, r, http.StatusBadRequest, codeValidation, fmt.Sprintf("operation %d failed schema validation", ie.index), prefixDetails(idx, validationDetails(ve.err))...)
	case errors.As(ie.err, &ad):
		if ad.anonymous {
			unauthenticated(w, r, false, fmt.Sprintf("operation %d: authentication required", ie.index))
			return
		}
		writeError(w, r, http.StatusForbidden, codeForbidden, fmt.Sprintf("operation %d: no access rule allows this operation", ie.index), Detail{Path: idx, Reason: "no access rule allows this operation"})
	case errors.Is(ie.err, storage.ErrNotFound):
		writeError(w, r, http.StatusNotFound, codeNotFound, fmt.Sprintf("operation %d: document not found", ie.index), Detail{Path: idx, Reason: "document not found"})
	case errors.Is(ie.err, storage.ErrVersionMismatch):
		writeError(w, r, http.StatusPreconditionFailed, codeVersionMismatch, fmt.Sprintf("operation %d: %v", ie.index, ie.err), Detail{Path: idx, Reason: ie.err.Error()})
	case errors.Is(ie.err, storage.ErrConflict):
		var ce *storage.ConflictError
		var details []Detail
		if errors.As(ie.err, &ce) {
			reason := "must be unique"
			if len(ce.Fields) > 1 {
				reason = "must be unique in combination with " + strings.Join(ce.Fields, ", ")
			}
			for _, f := range ce.Fields {
				details = append(details, Detail{Path: idx + "." + f, Reason: reason})
			}
		}
		writeError(w, r, http.StatusConflict, codeConflict, fmt.Sprintf("operation %d: a document with the same unique value already exists", ie.index), details...)
	default:
		storageError(w, r, ie.err)
	}
}
