package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ModeMigrate is the Envelope.Mode of a batch of documents going through a collection's schema
// migrations (design/schema-versioning.md §8.2): migrate.js runs it with no permissions but
// reading the bundle.
const ModeMigrate = "migrate"

// MigrateRequest is one batch, sent by backd's migration job.
type MigrateRequest struct {
	// Collection is <realm>/<database>/<collection>, for logs and the per-realm limit.
	Collection  string   `json:"collection"`
	Bundle      Bundle   `json:"bundle"`
	Current     int      `json:"current"` // the collection's current schema version
	Docs        []Doc    `json:"docs"`
	TimeoutMS   int64    `json:"timeout_ms"`
	MemoryBytes int64    `json:"memory_bytes"`
	MaxOutput   int64    `json:"max_output"`
	Mask        []string `json:"mask,omitempty"` // values that must not appear in the migration's console output
}

// Doc is one document of a batch: the fields as stored, without id and _meta, and what a
// migration may read of its meta (id, created_at, updated_at, created_by, updated_by, owner, org, team).
type Doc struct {
	ID            string          `json:"id"`
	Version       int64           `json:"version"`        // _meta.version, the write version
	SchemaVersion int             `json:"schema_version"` // 0 or missing: 1
	Meta          json.RawMessage `json:"meta,omitempty"`
	Fields        json.RawMessage `json:"fields"`
}

// MigrateOutcome is what became of one document: Fields when it migrated, Skipped when it was
// already at the current version, Error when it failed (and stays as it was).
type MigrateOutcome struct {
	ID      string          `json:"id"`
	Fields  json.RawMessage `json:"fields,omitempty"`
	Skipped bool            `json:"skipped,omitempty"`
	Error   *MigrateError   `json:"error,omitempty"`
}

// MigrateError is why one document failed. Kind is threw, async, not_json or no_step; Step is
// "2 → 3"; Message is the migration's own error text, which may name a value: callers keep it out
// of reports.
type MigrateError struct {
	Kind    string `json:"kind"`
	Step    string `json:"step"`
	Message string `json:"message"`
}

// MigrateResult is how a batch ended: Outcomes has one entry per document when Status is "ok".
type MigrateResult struct {
	Status     string           `json:"status"`
	Outcomes   []MigrateOutcome `json:"outcomes,omitempty"`
	Message    string           `json:"message,omitempty"`
	Logs       []LogLine        `json:"logs,omitempty"`
	DurationMS int64            `json:"duration_ms"`
}

// Migrate runs one batch to its end, in its own process, under the migration limits (not the
// functions').
func (e *Executor) Migrate(ctx context.Context, req MigrateRequest) MigrateResult {
	start := time.Now()
	res := e.migrateBatch(ctx, req)
	res.DurationMS = time.Since(start).Milliseconds()
	attrs := []any{"migration", req.Collection, "status", res.Status, "documents", len(req.Docs), "duration_ms", res.DurationMS}
	if res.Message != "" && res.Status != StatusOK {
		attrs = append(attrs, "message", res.Message)
	}
	e.cfg.Log.Info("migration batch", attrs...)
	return res
}

func (e *Executor) migrateBatch(ctx context.Context, req MigrateRequest) MigrateResult {
	realm, _, _ := strings.Cut(req.Collection, "/")
	e.migMu.Lock()
	if e.migRealm[realm] >= e.cfg.MigrationsPerRealm {
		e.migMu.Unlock()
		return MigrateResult{Status: StatusBusy, Message: fmt.Sprintf("realm %q already has %d migration batches running", realm, e.cfg.MigrationsPerRealm)}
	}
	select {
	case e.migSlots <- struct{}{}:
	default:
		e.migMu.Unlock()
		return MigrateResult{Status: StatusBusy, Message: fmt.Sprintf("the executor runs its maximum of %d migration batches", e.cfg.MaxMigrations)}
	}
	e.migRealm[realm]++
	e.migMu.Unlock()
	defer func() {
		e.migMu.Lock()
		e.migRealm[realm]--
		e.migMu.Unlock()
		<-e.migSlots
	}()

	bundle, err := e.bundle(ctx, req.Bundle)
	if err != nil {
		return MigrateResult{Status: StatusBundle, Message: err.Error()}
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 || req.MemoryBytes <= 0 || req.MaxOutput <= 0 || req.Current < 2 {
		return MigrateResult{Status: StatusError, Message: "invalid limits or current version"}
	}
	input, err := json.Marshal(struct {
		Current int   `json:"current"`
		Docs    []Doc `json:"docs"`
	}{req.Current, req.Docs})
	if err != nil {
		return MigrateResult{Status: StatusError, Message: "invalid documents: " + err.Error()}
	}
	inv := InvokeRequest{
		Function:  "migrate:" + req.Collection,
		Bundle:    req.Bundle,
		TimeoutMS: req.TimeoutMS, MemoryBytes: req.MemoryBytes, MaxOutput: req.MaxOutput,
		Envelope: Envelope{Mode: ModeMigrate, Input: input, Mask: req.Mask},
	}
	run := e.run(ctx, bundle, inv, timeout, nil)
	out := MigrateResult{Status: run.Status, Message: run.Message, Logs: run.Logs}
	if run.Status != StatusOK {
		return out
	}
	if err := json.Unmarshal(run.Output, &out.Outcomes); err != nil || len(out.Outcomes) != len(req.Docs) {
		return MigrateResult{Status: StatusError, Message: "unreadable migration result", Logs: run.Logs}
	}
	return out
}
