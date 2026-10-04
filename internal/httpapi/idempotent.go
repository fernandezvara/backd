package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
)

// Idempotency-Key on document creates: a client that retries a create (a lost
// answer, a timeout) sends the same key, and gets the original answer instead
// of a second document. It is the mechanism of function calls, with the
// scope of the key being the collection and the caller:
//
//   - the first request with a key claims it, runs, and stores its answer
//     (201 and the document; for a batch, 200 and the results);
//   - the same key with the same body returns that stored answer, with
//     Idempotent-Replayed: true, whatever happened to the document since;
//   - the same key with another body answers 422 idempotency_key_reused;
//   - the same key while the first request is still running answers 409
//     request_in_progress.
//
// Only a request that succeeds is remembered: one that fails (invalid, refused,
// a conflict) created nothing, so the key is free for a corrected retry. Keys
// are kept for 24 hours. They need a caller and a realm with authentication:
// anonymous callers share one identity, so they could read each other's
// answers.

// idemClaim is a claimed key. Its methods are safe on a nil claim (no key).
type idemClaim struct {
	svc  *auth.Users
	id   string
	done bool
}

// claimKey handles the Idempotency-Key of a create (scope = the collection's
// name) or a batch (scope = "_batch"). It returns:
//   - (nil, true) when the request carries no key: go on;
//   - (claim, true) when the key is new: go on, then complete or release it;
//   - (nil, false) when it already answered (a replay, or an error).
func (d *documents) claimKey(w http.ResponseWriter, r *http.Request, scope string, body map[string]any) (*idemClaim, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		return nil, true
	}
	if !validIdempotencyKey.MatchString(key) {
		writeError(w, r, http.StatusBadRequest, codeInvalidHeader, "Idempotency-Key must be 1-200 characters of letters, digits, . _ : -")
		return nil, false
	}
	realm, database := chi.URLParam(r, "realm"), chi.URLParam(r, "database")
	svc := d.users(realm)
	caller, hasCaller := callerOf(r)
	switch {
	case svc == nil:
		writeError(w, r, http.StatusBadRequest, codeInvalidHeader, "Idempotency-Key needs a realm with authentication enabled: this realm has no place to remember keys")
		return nil, false
	case !hasCaller || (caller.User == nil && caller.Key == nil && caller.Func == nil):
		writeError(w, r, http.StatusBadRequest, codeInvalidHeader, "Idempotency-Key needs a signed-in user or an API key: anonymous callers share one identity")
		return nil, false
	}
	raw, _ := json.Marshal(body) // keys sorted: the same content hashes the same
	sum := sha256.Sum256(raw)
	rec := auth.IdempotencyRecord{
		// "doc:" keeps these apart from functions' records (function names never hold a colon).
		ID:          auth.IdempotencyID("doc:"+database, scope, caller.Subject(), key),
		Function:    database + "/" + scope,
		CallerActor: caller.Actor(),
		InputHash:   hex.EncodeToString(sum[:]),
		Mode:        "sync",
	}
	existing, claimed, err := svc.ClaimIdempotency(r.Context(), rec)
	switch {
	case err != nil:
		writeError(w, r, http.StatusInternalServerError, codeInternal, "could not check the idempotency key")
		logger(r.Context()).Error("claim idempotency key", "scope", rec.Function, "error", err)
		return nil, false
	case claimed:
		return &idemClaim{svc: svc, id: rec.ID}, true
	case existing.InputHash != rec.InputHash:
		writeError(w, r, http.StatusUnprocessableEntity, codeIdempotencyKeyReused, "this idempotency key was already used with different input")
		return nil, false
	case existing.Status != auth.IdempotencyDone || existing.Result == nil:
		writeError(w, r, http.StatusConflict, codeRequestInProgress, "a request with this idempotency key is still running")
		return nil, false
	}
	status := existing.Result.HTTPStatus
	var stored any
	_ = json.Unmarshal(existing.Result.Output, &stored)
	w.Header().Set("Idempotent-Replayed", "true")
	if scope != "_batch" {
		if m, ok := stored.(map[string]any); ok {
			if id, _ := m["id"].(string); id != "" {
				w.Header().Set("Location", r.URL.JoinPath(url.PathEscape(id)).Path)
			}
			if meta, _ := m["_meta"].(map[string]any); meta != nil {
				if v, ok := meta["version"].(float64); ok {
					w.Header().Set("ETag", etag(int64(v))) // the version it was created at
				}
			}
		}
	}
	writeJSON(w, status, stored)
	return nil, false
}

// complete remembers the request's answer, so a retry with the key gets it.
func (c *idemClaim) complete(ctx context.Context, status int, body any) {
	if c == nil {
		return
	}
	raw, err := json.Marshal(body)
	if err == nil {
		err = c.svc.CompleteIdempotency(ctx, c.id, &auth.JobResult{Status: "ok", Output: raw, HTTPStatus: status}, "")
	}
	if err != nil {
		// The document exists but the key stays "running": a retry is told so
		// (409) instead of creating a second one.
		logger(ctx).Error("store the idempotent answer", "error", err)
	}
	c.done = true
}

// release forgets a claim that was not completed (the request failed), so a
// corrected retry can use the same key. Meant for `defer`.
func (c *idemClaim) release(ctx context.Context) {
	if c == nil || c.done {
		return
	}
	if err := c.svc.ReleaseIdempotency(context.WithoutCancel(ctx), c.id); err != nil {
		logger(ctx).Error("release idempotency claim", "error", err)
	}
}
