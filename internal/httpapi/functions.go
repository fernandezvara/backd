package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/xid"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/executor"
	"github.com/fernandezvara/backd/internal/metrics"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/rules"
)

// FunctionRunner runs an invocation: the executor client, or a fake in tests.
type FunctionRunner interface {
	Invoke(ctx context.Context, req executor.InvokeRequest) (executor.Result, error)
}

// Error codes of function calls.
const (
	codeFunctionFailed         = "function_failed"
	codeFunctionTimeout        = "function_timeout"
	codeInvalidOutput          = "invalid_output"
	codeSecretMissing          = "secret_missing"
	codeIdempotencyKeyRequired = "idempotency_key_required"
	codeIdempotencyKeyReused   = "idempotency_key_reused"
	codeRequestInProgress      = "request_in_progress"
	codeCallNotDeclared        = "call_not_declared"
	codeCallTooDeep            = "call_too_deep"
)

// Where an invocation came from, as the invocation history records it.
const (
	originHTTP     = "http"
	originFunction = "function"
	originCron     = "cron"
	originAdmin    = "admin"
)

// callMeta says how an invocation started: its place in a chain of
// ctx.call calls and how long its caller can still wait.
type callMeta struct {
	Origin    string
	ParentID  string        // the invocation that called this one
	Depth     int           // nested calls above this one
	Remaining time.Duration // > 0: the caller's remaining time caps this call's timeout
}

// callbackMargin keeps a callback token valid a little past the deadline,
// for requests the function started just before it.
const callbackMargin = 5 * time.Second

// functions serves function calls (public listener) and bundles (internal).
type functions struct {
	docs          *documents
	runner        FunctionRunner
	callbackURL   string
	executorToken string
	log           *slog.Logger
	// dev rereads the manifests on every request instead of once, so
	// BACKD_DEV's background rebuilds are served without a restart.
	dev bool
	// concurrency limits calls per function and per realm on this
	// instance (roadmap F8); nil on the internal listener, which never
	// invokes (it only serves bundles).
	concurrency *concurrencyLimiter
	// metrics records calls, refusals and replays; nil turns it off.
	metrics *metrics.Metrics

	once    sync.Once
	mu      sync.RWMutex
	bundles map[string]string // sha256 → bundle file
	hashes  map[string]string // realm/database/name → sha256
}

// ensureIndex reads the bundle manifests: once, unless dev mode rereads
// them every call so a background rebuild is picked up immediately.
func (f *functions) ensureIndex() {
	if f.dev {
		f.reindex()
		return
	}
	f.once.Do(f.reindex)
}

func (f *functions) reindex() {
	bundles, hashes := map[string]string{}, map[string]string{}
	for _, db := range f.docs.reg.Databases() {
		if db.Functions == nil {
			continue
		}
		m, err := registry.ReadManifest(db.Functions.Dir)
		if err != nil {
			continue
		}
		for name, b := range m.Functions {
			bundles[b.SHA256] = db.Functions.BundlePath(m, name)
			hashes[db.Realm+"/"+db.Name+"/"+name] = b.SHA256
		}
	}
	f.mu.Lock()
	f.bundles, f.hashes = bundles, hashes
	f.mu.Unlock()
}

// resolveHash returns the built sha256 of realm/database/name, if any.
func (f *functions) resolveHash(key string) (string, bool) {
	f.ensureIndex()
	f.mu.RLock()
	defer f.mu.RUnlock()
	h, ok := f.hashes[key]
	return h, ok
}

// bundlePath returns the file of a built sha256, if any.
func (f *functions) bundlePath(sha string) (string, bool) {
	f.ensureIndex()
	f.mu.RLock()
	defer f.mu.RUnlock()
	p, ok := f.bundles[sha]
	return p, ok
}

// internalRoutes are the function routes of the internal listener: a
// function calling another with ctx.call, and reading the job of an async
// callee, both with a callback token.
func (f *functions) internalRoutes(r chi.Router) {
	r.With(noStore).Post("/v1/{realm}/{database}/_func/{function}", f.invoke)
	r.With(noStore).Get("/v1/{realm}/{database}/_jobs/{id}", f.getJob)
	r.With(noStore).Post("/v1/{realm}/_email/send", f.emailSend)
}

func (f *functions) routes(r chi.Router) {
	// Content-Type is checked inside invoke: sync and async need JSON,
	// but a webhook sender's body isn't necessarily JSON at all.
	r.With(noStore).Post("/v1/{realm}/{database}/_func/{function}", f.invoke)
	r.With(noStore).Get("/v1/{realm}/{database}/_jobs/{id}", f.getJob)
}

// invoke handles POST /v1/{realm}/{database}/_func/{function}: authentication,
// the invoke rule, the input schema, the executor, the output schema.
func (f *functions) invoke(w http.ResponseWriter, r *http.Request) {
	realm, database, name := chi.URLParam(r, "realm"), chi.URLParam(r, "database"), chi.URLParam(r, "function")
	fn := f.lookup(realm, database, name)
	// An internal function has no HTTP route: it answers like one that
	// doesn't exist, before looking at the caller, whoever that is. (The
	// internal listener is the way in for ctx.call, below.)
	if !f.docs.internal && (fn == nil || fn.Internal) {
		notFound(w, r)
		return
	}
	caller, hasAuth, ok := f.docs.identify(w, r, realm)
	if !ok {
		return
	}
	if !f.docs.internal && hasAuth && !scopeAllows(w, r, caller, auth.ScopeCall, database, name) {
		return
	}
	meta := callMeta{Origin: originHTTP}
	if f.docs.internal {
		// A function calling another: the callee must be declared in the
		// caller's `calls`, in the same database, within the depth limit.
		fc := caller.Func
		if fc == nil || fc.Admin {
			writeError(w, r, http.StatusForbidden, codeCallNotDeclared, "this credential can't call functions")
			return
		}
		if callerDB, _, _ := strings.Cut(strings.TrimPrefix(fc.Name, realm+"/"), "/"); callerDB != database || !slices.Contains(fc.Calls, name) {
			writeError(w, r, http.StatusForbidden, codeCallNotDeclared, "this function didn't declare "+database+"/"+name+" in its `calls`")
			return
		}
		if fn == nil {
			notFound(w, r)
			return
		}
		if fc.Depth+2 > registry.MaxCallChain {
			writeError(w, r, http.StatusForbidden, codeCallTooDeep, fmt.Sprintf("calls can be nested to %d functions", registry.MaxCallChain))
			return
		}
		meta = callMeta{Origin: originFunction, ParentID: fc.Invocation, Depth: fc.Depth + 1, Remaining: max(fc.Expires.Sub(f.docs.now())-callbackMargin, time.Millisecond)}
		// The callee inherits the caller's user and nothing else: no API
		// key's access, no admin access.
		caller = auth.Caller{User: caller.User}
		hasAuth = f.docs.users(realm) != nil
	} else if hasAuth && !f.mayInvoke(w, r, fn, caller) {
		return
	}
	var input json.RawMessage
	var webhook *executor.WebhookRequest
	var hashInput []byte
	if fn.Mode == registry.ModeWebhook {
		body, err := io.ReadAll(r.Body)
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			tooLarge(w, r, tooBig.Limit)
			return
		}
		if err != nil {
			writeError(w, r, http.StatusBadRequest, codeInvalidJSON, "could not read request body")
			return
		}
		webhook = &executor.WebhookRequest{Body: string(body), Headers: r.Header.Clone()}
		hashInput = body
	} else {
		if !contentTypeIs(r, "application/json") {
			writeError(w, r, http.StatusUnsupportedMediaType, codeUnsupportedMedia, "Content-Type must be application/json")
			return
		}
		var ok bool
		input, ok = readJSON(w, r)
		if !ok {
			return
		}
		if !validInput(w, r, fn, input) {
			return
		}
		hashInput = input
	}
	f.run(w, r, fn, realm, database, name, caller, meta, input, webhook, hashInput)
}

// run does what follows the checks of a call: idempotency, rate limit, then
// the executor (sync) or the queue (async), and the answer. hashInput is
// what the idempotency key is bound to.
func (f *functions) run(w http.ResponseWriter, r *http.Request, fn *registry.Function, realm, database, name string, caller auth.Caller, meta callMeta, input json.RawMessage, webhook *executor.WebhookRequest, hashInput []byte) {
	claimID, ok := f.checkIdempotency(w, r, fn, realm, database, name, caller, hashInput)
	if !ok {
		return
	}
	// Rate limits guard callers on the internet; a nested call is bounded
	// by the call graph and the concurrency limits instead.
	if meta.Origin == originHTTP && !f.checkRateLimit(w, r, fn, realm, database, name, caller) {
		f.releaseClaim(r.Context(), realm, claimID)
		return
	}
	if fn.Mode == registry.ModeAsync {
		f.enqueue(w, r, fn, realm, database, name, caller, input, claimID, meta)
		return
	}

	var secrets map[string]string
	if len(fn.Secrets) > 0 {
		values, missing, err := f.docs.users(realm).ResolveSecrets(r.Context(), fn.Secrets, database)
		if err != nil {
			f.releaseClaim(r.Context(), realm, claimID)
			writeError(w, r, http.StatusInternalServerError, codeInternal, "could not read the function's secrets")
			logger(r.Context()).Error("function secrets", "function", realm+"/"+database+"/"+name, "error", err)
			return
		}
		if len(missing) > 0 {
			f.releaseClaim(r.Context(), realm, claimID)
			writeError(w, r, http.StatusInternalServerError, codeSecretMissing, "a secret the function declares is not set")
			logger(r.Context()).Error("function secret missing", "function", realm+"/"+database+"/"+name, "secrets", missing)
			return
		}
		secrets = values
	}
	hash, built := f.resolveHash(realm + "/" + database + "/" + name)
	if !built || f.runner == nil {
		f.releaseClaim(r.Context(), realm, claimID)
		writeError(w, r, http.StatusServiceUnavailable, codeUnavailable, "functions are not available on this server")
		return
	}
	if f.concurrency != nil {
		// Webhook senders (Stripe and the like) retry on 5xx, not 4xx —
		// full answers 503 for them, matching F8's original design
		// (deferred at the time since webhook mode didn't exist yet).
		fullStatus, fullCode := http.StatusTooManyRequests, codeTooManyRequests
		if fn.Mode == registry.ModeWebhook {
			fullStatus, fullCode = http.StatusServiceUnavailable, codeUnavailable
		}
		funcKey := realm + "/" + database + "/" + name
		if !f.concurrency.tryAcquire(funcKey, fn.Concurrency) {
			f.metrics.FunctionRefused(realm, database+"/"+name, "concurrency")
			f.releaseClaim(r.Context(), realm, claimID)
			w.Header().Set("Retry-After", "1")
			writeError(w, r, fullStatus, fullCode, "this function is at its concurrency limit on this instance; retry shortly")
			return
		}
		realmKey := "realm:" + realm
		realmMax := f.docs.reg.Realms[realm].Settings.FunctionsMaxConcurrency
		if !f.concurrency.tryAcquire(realmKey, realmMax) {
			f.metrics.FunctionRefused(realm, database+"/"+name, "concurrency")
			f.concurrency.release(funcKey, fn.Concurrency)
			f.releaseClaim(r.Context(), realm, claimID)
			w.Header().Set("Retry-After", "1")
			writeError(w, r, fullStatus, fullCode, "this realm is at its function concurrency limit on this instance; retry shortly")
			return
		}
		defer func() {
			f.concurrency.release(funcKey, fn.Concurrency)
			f.concurrency.release(realmKey, realmMax)
		}()
	}
	// A nested call never outlives the function waiting for it.
	timeout := fn.Timeout
	if meta.Remaining > 0 && meta.Remaining < timeout {
		timeout = meta.Remaining
	}
	if timeout < time.Second {
		f.releaseClaim(r.Context(), realm, claimID)
		writeError(w, r, http.StatusGatewayTimeout, codeFunctionTimeout, "the calling function has no time left to wait for this call")
		return
	}
	deadline := f.docs.now().Add(timeout)
	// Let the response be written after a long function.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(timeout + 30*time.Second))
	invID := xid.New().String()
	req := executor.InvokeRequest{
		Function:  realm + "/" + database + "/" + name,
		Bundle:    executor.Bundle{SHA256: hash, URL: strings.TrimRight(f.callbackURL, "/") + "/_internal/functions/" + hash},
		TimeoutMS: timeout.Milliseconds(),
		MemoryMB:  (fn.Memory + 1<<20 - 1) >> 20,
		MaxOutput: fn.MaxOutput,
		Network:   fn.Network,
		Envelope: executor.Envelope{
			Mode:           fn.Mode,
			Input:          input,
			Webhook:        webhook,
			User:           userEnvelope(caller),
			Secrets:        secrets,
			Callback:       f.callback(realm, database, name, fn, caller, deadline.Add(callbackMargin), invID, meta.Depth),
			IdempotencyKey: r.Header.Get("Idempotency-Key"),
			RequestID:      requestID(r.Context()),
		},
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout+10*time.Second)
	defer cancel()
	res, err := f.runner.Invoke(ctx, req)
	f.logResult(r.Context(), req.Function, res, err)
	if err != nil {
		f.metrics.ExecutorError("unavailable")
		f.releaseClaim(r.Context(), realm, claimID)
		writeError(w, r, http.StatusServiceUnavailable, codeUnavailable, "the function executor is unavailable")
		return
	}
	f.recordInvocation(r.Context(), requestID(r.Context()), realm, database, name, fn.Mode, caller, res, "", invID, meta)
	if claimID != "" {
		if res.Status == executor.StatusBusy {
			f.releaseClaim(r.Context(), realm, claimID)
		} else {
			jr := jobResultFromExecutor(res)
			if err := f.docs.users(realm).CompleteIdempotency(r.Context(), claimID, &jr, ""); err != nil {
				logger(r.Context()).Error("complete idempotency key", "function", realm+"/"+database+"/"+name, "error", err)
			}
		}
	}
	f.respond(w, r, fn, res)
}

// adminInvoke handles POST /v1/{realm}/_admin/functions/{database}/{name}/invoke:
// an administrator runs any function (internal or public) on purpose, as a
// given user or with none, to re-run a clean-up or test a scheduled
// function (which then runs as its schedule would). The function's invoke rule is not evaluated (the credential is
// an admin one) and its rate limit doesn't apply; the run is audited.
func (f *functions) adminInvoke(w http.ResponseWriter, r *http.Request) {
	realm, database, name := chi.URLParam(r, "realm"), chi.URLParam(r, "database"), chi.URLParam(r, "name")
	fn := f.lookup(realm, database, name)
	if fn == nil {
		notFound(w, r)
		return
	}
	if fn.Mode == registry.ModeWebhook {
		writeError(w, r, http.StatusBadRequest, codeValidation, "a webhook function can't be run by hand: its sender calls it over HTTP")
		return
	}
	data, err := io.ReadAll(r.Body)
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		tooLarge(w, r, tooBig.Limit)
		return
	}
	var body struct {
		Input json.RawMessage `json:"input"`
		As    string          `json:"as"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err != nil || dec.Decode(&body) != nil || dec.More() {
		writeError(w, r, http.StatusBadRequest, codeInvalidJSON, `the body must be a JSON object with optional "input" and "as"`)
		return
	}
	input := body.Input
	if len(bytes.TrimSpace(input)) == 0 {
		input = json.RawMessage("null")
	}
	svc := usersOf(r)
	var caller auth.Caller
	var asID any
	if body.As != "" {
		email, err := registry.NormalizeEmail(body.As)
		var u auth.User
		if err == nil {
			u, err = svc.Store.UserByEmail(r.Context(), email)
		}
		if err != nil {
			writeError(w, r, http.StatusNotFound, codeNotFound, "no user with that email in this realm")
			return
		}
		if u.Disabled {
			writeError(w, r, http.StatusConflict, codeConflict, "that user is disabled")
			return
		}
		caller = auth.Caller{User: &auth.Principal{User: u}}
		asID = u.ID
	}
	// A scheduled function run by hand, with no user, runs as its schedule
	// would (no caller, full access as the function): an operator re-runs
	// it as it normally runs, with no need for `admin: true` just to test it.
	if body.As == "" && fn.Schedule != nil {
		caller = auth.Caller{Func: &auth.FuncCaller{Name: realm + "/" + database + "/" + name, Admin: true}}
	}
	if !validInput(w, r, fn, input) {
		return
	}
	// Ids only, never the email.
	svc.Audit(r.Context(), "function.invoke_manual", database+"/"+name, map[string]any{"as": asID})
	f.run(w, r, fn, realm, database, name, caller, callMeta{Origin: originAdmin}, input, nil, input)
}

// validInput checks a call's input against the function's input schema,
// answering 400 when it doesn't match.
func validInput(w http.ResponseWriter, r *http.Request, fn *registry.Function, input json.RawMessage) bool {
	if fn.InputSchema == nil {
		return true
	}
	v, _ := jsonschema.UnmarshalJSON(bytes.NewReader(input))
	if err := fn.InputSchema.Validate(v); err != nil {
		writeError(w, r, http.StatusBadRequest, codeValidation, "input failed schema validation", validationDetails(err)...)
		return false
	}
	return true
}

// validIdempotencyKey bounds a client-supplied Idempotency-Key to what's
// safe to store as (part of) a MongoDB document id and to log.
var validIdempotencyKey = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,200}$`)

// checkIdempotency handles the Idempotency-Key header (roadmap F12). A
// fresh key proceeds normally: the caller must complete or release the
// claim id it returns once the real work is done. A repeated key with
// the same input replays the stored response; different input answers
// 422; a key whose first call hasn't finished answers 409. Returns ""
// when there's nothing to track (no key, and the function allows that).
func (f *functions) checkIdempotency(w http.ResponseWriter, r *http.Request, fn *registry.Function, realm, database, name string, caller auth.Caller, input json.RawMessage) (claimID string, ok bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		if fn.Idempotency == "required" {
			writeError(w, r, http.StatusBadRequest, codeIdempotencyKeyRequired, "this function requires an Idempotency-Key header")
			return "", false
		}
		return "", true
	}
	if !validIdempotencyKey.MatchString(key) {
		writeError(w, r, http.StatusBadRequest, codeInvalidHeader, "Idempotency-Key must be 1-200 characters of letters, digits, . _ : -")
		return "", false
	}
	sum := sha256.Sum256(input)
	rec := auth.IdempotencyRecord{
		ID:          auth.IdempotencyID(database, name, caller.Subject(), key),
		Function:    database + "/" + name,
		CallerActor: caller.Actor(),
		InputHash:   hex.EncodeToString(sum[:]),
		Mode:        fn.Mode,
	}
	existing, claimed, err := f.docs.users(realm).ClaimIdempotency(r.Context(), rec)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, codeInternal, "could not check the idempotency key")
		logger(r.Context()).Error("claim idempotency key", "function", rec.Function, "error", err)
		return "", false
	}
	if claimed {
		return rec.ID, true
	}
	if existing.InputHash != rec.InputHash {
		f.metrics.FunctionRefused(realm, rec.Function, "idempotency_reused")
		writeError(w, r, http.StatusUnprocessableEntity, codeIdempotencyKeyReused, "this idempotency key was already used with different input")
		return "", false
	}
	if existing.Status != auth.IdempotencyDone {
		f.metrics.FunctionRefused(realm, rec.Function, "idempotency_conflict")
		writeError(w, r, http.StatusConflict, codeRequestInProgress, "a call with this idempotency key is still running")
		return "", false
	}
	f.metrics.FunctionReplayed(realm, rec.Function)
	w.Header().Set("Idempotent-Replayed", "true")
	f.replayIdempotent(w, r, fn, realm, database, existing)
	return "", false
}

// replayIdempotent answers a repeated Idempotency-Key with the stored
// outcome of its first call, without running the function again.
func (f *functions) replayIdempotent(w http.ResponseWriter, r *http.Request, fn *registry.Function, realm, database string, rec auth.IdempotencyRecord) {
	if fn.Mode == registry.ModeAsync {
		job, found, err := f.docs.users(realm).GetJob(r.Context(), rec.JobID)
		if err != nil || !found {
			writeError(w, r, http.StatusInternalServerError, codeInternal, "could not read the job this idempotency key belongs to")
			if err != nil {
				logger(r.Context()).Error("get job for idempotency replay", "job_id", rec.JobID, "error", err)
			}
			return
		}
		w.Header().Set("Location", "/v1/"+realm+"/"+database+"/_jobs/"+job.ID)
		writeJSON(w, http.StatusAccepted, jobJSON(job))
		return
	}
	f.respond(w, r, fn, executorResultFromJobResult(rec.Result))
}

// releaseClaim removes an idempotency claim that shouldn't be
// remembered (an infrastructure failure or transient limit, not a real
// outcome), so a retry with the same key can go through cleanly.
func (f *functions) releaseClaim(ctx context.Context, realm, claimID string) {
	if claimID == "" {
		return
	}
	if err := f.docs.users(realm).ReleaseIdempotency(ctx, claimID); err != nil {
		logger(ctx).Error("release idempotency claim", "error", err)
	}
}

// checkRateLimit enforces fn.RateLimit if any (roadmap F9), for both
// sync and async calls. Returns false if it already answered the
// caller (429, or 500 on an unexpected error checking the limit).
func (f *functions) checkRateLimit(w http.ResponseWriter, r *http.Request, fn *registry.Function, realm, database, name string, caller auth.Caller) bool {
	if fn.RateLimit == nil {
		return true
	}
	key := "func:" + realm + "/" + database + "/" + name + ":"
	if fn.RateLimit.Per == "user" && caller.User != nil {
		key += "user:" + caller.User.User.ID
	} else {
		key += auth.IPKey(clientIP(r))
	}
	if err := f.docs.users(realm).RateLimit(r.Context(), key, fn.RateLimit.Limit, fn.RateLimit.Window); err != nil {
		var te *auth.ThrottledError
		if errors.As(err, &te) {
			f.metrics.FunctionRefused(realm, database+"/"+name, "rate_limit")
			f.metrics.RateLimited(realm, "function")
			secs := int((te.RetryAfter + time.Second - 1) / time.Second)
			w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
			writeError(w, r, http.StatusTooManyRequests, codeTooManyRequests, "this function's rate limit was reached; retry later")
			return false
		}
		writeError(w, r, http.StatusInternalServerError, codeInternal, "could not check the function's rate limit")
		logger(r.Context()).Error("function rate limit", "function", realm+"/"+database+"/"+name, "error", err)
		return false
	}
	return true
}

// enqueue stores an async call as a job (roadmap F11) and answers 202
// with its id and where to read its result. Secrets and the bundle are
// resolved later, by a worker: a job queued for a while always uses
// their current values, not what they were at enqueue time. claimID is
// "" unless the caller sent an Idempotency-Key (roadmap F12), in which
// case the job's id is recorded against it (or the claim released, on
// failure) once enqueuing is decided.
func (f *functions) enqueue(w http.ResponseWriter, r *http.Request, fn *registry.Function, realm, database, name string, caller auth.Caller, input json.RawMessage, claimID string, meta callMeta) {
	job := auth.Job{
		Database: database, Function: name, Input: input, CallerActor: caller.Actor(),
		TimeoutMS: fn.Timeout.Milliseconds(), RequestID: requestID(r.Context()),
		Origin: meta.Origin, ParentID: meta.ParentID, Depth: meta.Depth,
		ActsAsFunction: caller.Func != nil && caller.Func.Admin,
	}
	if caller.User != nil {
		job.CallerUserID = caller.User.User.ID
	}
	if caller.Key != nil {
		job.CallerKeyHash = caller.Key.Hash
	}
	job, err := f.docs.users(realm).EnqueueJob(r.Context(), job)
	if err != nil {
		f.releaseClaim(r.Context(), realm, claimID)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "could not queue the function call")
		logger(r.Context()).Error("enqueue function job", "function", realm+"/"+database+"/"+name, "error", err)
		return
	}
	logger(r.Context()).Info("function job queued", "function", realm+"/"+database+"/"+name, "job_id", job.ID)
	if claimID != "" {
		if err := f.docs.users(realm).CompleteIdempotency(r.Context(), claimID, nil, job.ID); err != nil {
			logger(r.Context()).Error("complete idempotency key", "function", realm+"/"+database+"/"+name, "error", err)
		}
	}
	w.Header().Set("Location", "/v1/"+realm+"/"+database+"/_jobs/"+job.ID)
	writeJSON(w, http.StatusAccepted, jobJSON(job))
}

// getJob answers GET /v1/{realm}/{database}/_jobs/{id}: a job's status
// and, once done, its result. Readable only by the caller who enqueued
// it, or by any API key (roadmap F11) — the same "API keys may always
// act" carve-out mayInvoke already gives them.
func (f *functions) getJob(w http.ResponseWriter, r *http.Request) {
	realm, database, id := chi.URLParam(r, "realm"), chi.URLParam(r, "database"), chi.URLParam(r, "id")
	rl, ok := f.docs.reg.Realms[realm]
	if !ok {
		notFound(w, r)
		return
	}
	if _, ok := rl.Databases[database]; !ok {
		notFound(w, r)
		return
	}
	caller, hasAuth, ok := f.docs.identify(w, r, realm)
	if !ok {
		return
	}
	if !hasAuth {
		notFound(w, r) // no system database: no jobs are possible
		return
	}
	if caller.User == nil && caller.Key == nil {
		unauthenticated(w, r, false, "authentication required to read a job")
		return
	}
	job, found, err := f.docs.users(realm).GetJob(r.Context(), id)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, codeInternal, "could not read the job")
		logger(r.Context()).Error("get job", "job_id", id, "error", err)
		return
	}
	// An administrator (an admin API key, or a user holding an admin role) reads
	// any job, such as the one they started by hand.
	admin := caller.User != nil && f.docs.users(realm).Settings.AdminRights(caller.User.User.Roles).Has(registry.RightFunctions)
	// A scoped key reads the jobs of the functions it may call, and no others.
	scoped := caller.Key != nil && !caller.Key.Scopes.Allows(auth.ScopeCall, job.Database, job.Function)
	if !found || job.Database != database || scoped || !(admin || jobVisibleTo(job, caller)) {
		notFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, jobJSON(job))
}

func jobVisibleTo(job auth.Job, c auth.Caller) bool {
	// A function sees the jobs its own invocation queued with ctx.call.
	if c.Func != nil && !c.Func.Admin && job.ParentID != "" && job.ParentID == c.Func.Invocation {
		return true
	}
	if c.Key != nil {
		return true
	}
	return c.User != nil && job.CallerUserID == c.User.User.ID
}

// jobJSON renders a job the way callers see it: never its input, and its
// result only once done.
func jobJSON(job auth.Job) map[string]any {
	out := map[string]any{
		"id":              job.ID,
		"function":        job.Database + "/" + job.Function,
		"status":          job.Status,
		"created_at":      formatTime(job.CreatedAt),
		"attempts":        job.Attempts,
		"next_attempt_at": nil,
		"result":          nil,
	}
	if !job.NextAttemptAt.IsZero() && job.Status != auth.JobDone {
		out["next_attempt_at"] = formatTime(job.NextAttemptAt)
	}
	if job.Status == auth.JobDone && job.Result != nil {
		out["result"] = jobResultJSON(job)
	}
	return out
}

// jobResultJSON renders a done job's result with the same status, code,
// message and HTTP status a sync call's own answer would carry for the
// same end reason (roadmap F14, so a client can treat a job's outcome
// uniformly with a sync call's) — never Result.Message verbatim outside
// function_error, since it may hold a stack trace and is backd's logs
// only, the same rule respond() already follows for sync.
func jobResultJSON(job auth.Job) map[string]any {
	r := job.Result
	res := map[string]any{"status": r.Status, "duration_ms": r.DurationMS}
	switch r.Status {
	case executor.StatusOK:
		res["output"] = json.RawMessage(r.Output)
	case executor.StatusFunctionError:
		res["http_status"] = r.HTTPStatus
		res["code"] = r.Code
		res["message"] = r.Message
		if len(r.Details) > 0 {
			res["details"] = json.RawMessage(r.Details)
		}
	case executor.StatusTimeout:
		res["http_status"] = http.StatusGatewayTimeout
		res["code"] = codeFunctionTimeout
		res["message"] = fmt.Sprintf("the function didn't finish within %s", time.Duration(job.TimeoutMS)*time.Millisecond)
	case executor.StatusOutputTooBig:
		res["http_status"] = http.StatusInternalServerError
		res["code"] = codeInvalidOutput
		res["message"] = "the function's output is larger than its max_output"
	default:
		res["http_status"] = http.StatusInternalServerError
		res["code"] = codeFunctionFailed
		res["message"] = "the function failed"
	}
	return res
}

// recordInvocation stores the call in the realm's invocation history
// (roadmap F10): what happened, not the input or output. Realms with
// auth disabled have no system database to store it in, so it's skipped
// there — the same limitation secrets and rate limits already have.
// jobID is "" for sync calls; a worker passes it for async ones (F11).
func (f *functions) recordInvocation(ctx context.Context, requestID, realm, database, name, mode string, caller auth.Caller, res executor.Result, jobID, invID string, meta callMeta) {
	f.metrics.FunctionRan(realm, database+"/"+name, mode, res.Status, time.Duration(res.DurationMS)*time.Millisecond)
	u := f.docs.users(realm)
	if u == nil {
		return
	}
	var code string
	if res.Status == executor.StatusFunctionError && res.FunctionError != nil {
		code = res.FunctionError.Code
	}
	logs := make([]auth.LogLine, len(res.Logs))
	for i, l := range res.Logs {
		logs[i] = auth.LogLine{Level: l.Level, Line: l.Line}
	}
	u.RecordInvocation(ctx, auth.InvocationRecord{
		ID:         invID,
		Function:   database + "/" + name,
		Actor:      caller.Actor(),
		Mode:       mode,
		Status:     res.Status,
		Code:       code,
		DurationMS: res.DurationMS,
		RequestID:  requestID,
		JobID:      jobID,
		ParentID:   meta.ParentID,
		Origin:     meta.Origin,
		Logs:       logs,
	})
}

func (f *functions) lookup(realm, database, name string) *registry.Function {
	rl, ok := f.docs.reg.Realms[realm]
	if !ok {
		return nil
	}
	db, ok := rl.Databases[database]
	if !ok || db.Functions == nil {
		return nil
	}
	return db.Functions.Functions[name]
}

// mayInvoke applies the function's invoke rule. API keys may always call;
// without a rule, only they may.
func (f *functions) mayInvoke(w http.ResponseWriter, r *http.Request, fn *registry.Function, c auth.Caller) bool {
	if c.Key != nil {
		return true
	}
	allowed := false
	if fn.Invoke != nil {
		ok, err := fn.Invoke.Allow(rules.Values{User: rulesUser(c), Now: f.docs.now()})
		if err != nil {
			logger(r.Context()).Debug("invoke rule failed", "error", err)
		}
		allowed = ok && err == nil
	}
	switch {
	case allowed:
		return true
	case c.User == nil:
		unauthenticated(w, r, false, "authentication required to call this function")
	default:
		writeError(w, r, http.StatusForbidden, codeForbidden, "not allowed to call this function")
	}
	return false
}

func rulesUser(c auth.Caller) *rules.User {
	if c.User == nil {
		return nil
	}
	u := c.User.User
	return &rules.User{ID: u.ID, Email: u.Email, EmailVerified: u.EmailVerified, Roles: u.Roles}
}

// userEnvelope is ctx.user: the user the call acts as, or null.
func userEnvelope(c auth.Caller) json.RawMessage {
	if c.User == nil {
		return nil
	}
	u := c.User.User
	roles := u.Roles
	if roles == nil {
		roles = []string{}
	}
	data, _ := json.Marshal(map[string]any{"id": u.ID, "email": u.Email, "email_verified": u.EmailVerified, "roles": roles})
	return data
}

// callback gives the function its tokens: ctx.db acts as the caller,
// ctx.admin.db (admin: true only) has full access as the function.
func (f *functions) callback(realm, database, name string, fn *registry.Function, c auth.Caller, expires time.Time, invID string, depth int) *executor.Callback {
	claims := auth.CallbackClaims{Realm: realm, Function: database + "/" + name, Expires: expires, Calls: fn.Calls, Depth: depth, Inv: invID, Email: fn.Email}
	if c.User != nil {
		claims.UserID = c.User.User.ID
	}
	if c.Key != nil {
		claims.KeyHash = c.Key.Hash
	}
	cb := &executor.Callback{URL: f.callbackURL, Realm: realm, Database: database, Token: auth.SignCallback(f.docs.callbackKey, claims), Email: fn.Email}
	if fn.Admin || (c.Func != nil && c.Func.Admin) {
		cb.AdminToken = auth.SignCallback(f.docs.callbackKey, auth.CallbackClaims{Realm: realm, Function: database + "/" + name, Admin: true, Expires: expires})
	}
	return cb
}

var functionErrorCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// DevDurationHeader and DevLogsHeader carry the function's timing and
// console output back to the caller, only in dev mode (BACKD_DEV): a
// production call never leaks another caller's function logs. `backd
// functions invoke` reads them.
const (
	DevDurationHeader = "X-Backd-Dev-Duration-Ms"
	DevLogsHeader     = "X-Backd-Dev-Logs"
)

// respond answers the caller from the executor's result.
func (f *functions) respond(w http.ResponseWriter, r *http.Request, fn *registry.Function, res executor.Result) {
	if f.dev {
		w.Header().Set(DevDurationHeader, strconv.FormatInt(res.DurationMS, 10))
		if data, err := json.Marshal(res.Logs); err == nil {
			w.Header().Set(DevLogsHeader, string(data))
		}
	}
	switch res.Status {
	case executor.StatusOK:
		if fn.Mode == registry.ModeWebhook {
			f.respondWebhook(w, r, res.Webhook)
			return
		}
		if fn.OutputSchema != nil {
			v, err := jsonschema.UnmarshalJSON(bytes.NewReader(res.Output))
			if err == nil {
				err = fn.OutputSchema.Validate(v)
			}
			if err != nil {
				logger(r.Context()).Error("function output failed its schema", "details", validationDetails(err))
				writeError(w, r, http.StatusInternalServerError, codeInvalidOutput, "the function's output doesn't match its schema")
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(res.Output)
	case executor.StatusFunctionError:
		fe := res.FunctionError
		if fe == nil || fe.Status < 400 || fe.Status > 499 || !functionErrorCode.MatchString(fe.Code) {
			writeError(w, r, http.StatusInternalServerError, codeFunctionFailed, "the function failed")
			return
		}
		var details []Detail
		if len(fe.Details) > 0 && json.Unmarshal(fe.Details, &details) != nil {
			details = nil // not [{path, reason}]: left out
		}
		writeError(w, r, fe.Status, fe.Code, fe.Message, details...)
	case executor.StatusTimeout:
		writeError(w, r, http.StatusGatewayTimeout, codeFunctionTimeout, fmt.Sprintf("the function didn't finish within %s", fn.Timeout))
	case executor.StatusOutputTooBig:
		writeError(w, r, http.StatusInternalServerError, codeInvalidOutput, "the function's output is larger than its max_output")
	case executor.StatusBusy:
		w.Header().Set("Retry-After", "1")
		writeError(w, r, http.StatusServiceUnavailable, codeUnavailable, "the function executor is busy; retry shortly")
	default:
		writeError(w, r, http.StatusInternalServerError, codeFunctionFailed, "the function failed")
	}
}

// respondWebhook writes a webhook function's own chosen status, headers
// and body (roadmap F13) — unlike sync/async, backd doesn't interpret
// or validate the body against any schema; the function is answering
// its sender directly.
func (f *functions) respondWebhook(w http.ResponseWriter, r *http.Request, wh *executor.WebhookResponse) {
	if wh == nil || wh.Status < 200 || wh.Status > 599 {
		writeError(w, r, http.StatusInternalServerError, codeFunctionFailed, "the function didn't set a valid response")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for k, v := range wh.Headers {
		w.Header().Set(k, v)
	}
	w.WriteHeader(wh.Status)
	io.WriteString(w, wh.Body)
}

// logResult logs the invocation and the function's log lines, tagged with
// the request id, function and realm. Log lines are masked by the executor.
func (f *functions) logResult(ctx context.Context, function string, res executor.Result, err error) {
	log := logger(ctx).With("function", function)
	if err != nil {
		log.Error("function executor unreachable", "error", err)
		return
	}
	for _, l := range res.Logs {
		log.Info("function log", "level", l.Level, "line", l.Line)
	}
	attrs := []any{"status", res.Status, "duration_ms", res.DurationMS}
	if res.Message != "" {
		attrs = append(attrs, "message", res.Message)
	}
	switch res.Status {
	case executor.StatusOK, executor.StatusFunctionError:
		log.Info("function called", attrs...)
	default:
		log.Warn("function failed", attrs...)
	}
}

// readJSON reads the body as one JSON value (an empty body is null).
func readJSON(w http.ResponseWriter, r *http.Request) (json.RawMessage, bool) {
	data, err := io.ReadAll(r.Body)
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		tooLarge(w, r, tooBig.Limit)
		return nil, false
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidJSON, "could not read request body")
		return nil, false
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return json.RawMessage("null"), true
	}
	if !json.Valid(data) {
		writeError(w, r, http.StatusBadRequest, codeInvalidJSON, "request body is not valid JSON")
		return nil, false
	}
	return json.RawMessage(bytes.TrimSpace(data)), true
}

// bundle serves GET /_internal/functions/{sha256} to the executor.
func (f *functions) bundle(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || f.executorToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(f.executorToken)) != 1 {
		unauthenticated(w, r, ok, "the executor's credential is required")
		return
	}
	path, found := f.bundlePath(chi.URLParam(r, "sha256"))
	if !found {
		notFound(w, r)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, codeInternal, "could not read the bundle")
		return
	}
	w.Header().Set("Content-Type", "text/javascript")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}
