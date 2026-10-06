package auth

import (
	"context"
	"log/slog"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/registry"
)

// LogLine is one line of a function's captured console output, already
// masked by the executor.
type LogLine struct {
	Level string
	Line  string
}

// InvocationRecord is one function call, recorded in the realm's system
// database (roadmap F10). Inputs and outputs are never stored — only
// what's needed to debug what happened and read the function's own logs.
type InvocationRecord struct {
	ID         string
	At         time.Time
	ExpiresAt  time.Time // removed after this (the realm's log retention)
	Function   string    // <database>/<name>
	Actor      string    // "user:<id>", "key:<name>", "anonymous", or "func:<name> as ..."
	Mode       string    // sync today; async, webhook later
	Status     string    // executor.Status* (ok, function_error, timeout, memory, cpu, crash, ...)
	Code       string    // the function's own error code, when Status is function_error
	DurationMS int64
	RequestID  string
	JobID      string // async jobs (roadmap F11); empty until then
	ParentID   string // the invocation that called this one (ctx.call); empty otherwise
	Origin     string // http, function, cron, admin or backd:<event>
	Logs       []LogLine
	// Steps are what the call reported with ctx.step and ctx.progress, closed
	// when it ended; StepsOmitted counts those dropped from the middle.
	Steps        []Step
	StepsOmitted int
}

// InvocationFilter selects invocation records. Zero fields match everything.
type InvocationFilter struct {
	Function, RequestID string
	Since, Until        time.Time
	Limit, Skip         int
}

func (s *Users) logRetention() time.Duration {
	if s.Settings.FunctionsLogRetention > 0 {
		return s.Settings.FunctionsLogRetention
	}
	return registry.DefaultLogRetention
}

// RecordInvocation stores one function call. A record that can't be
// stored is logged as an error; the call already happened and isn't
// undone by a failed write, the same philosophy as Audit.
func (s *Users) RecordInvocation(ctx context.Context, rec InvocationRecord) {
	now := s.now()
	if rec.ID == "" {
		rec.ID = xid.New().String()
	}
	rec.At = now
	rec.ExpiresAt = now.Add(s.logRetention())
	log := s.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	// The record must outlive the request that caused it.
	if err := s.Store.RecordInvocation(context.WithoutCancel(ctx), rec); err != nil {
		log.Error("invocation record not stored", "function", rec.Function, "request_id", rec.RequestID, "error", err)
	}
}

// Invocations returns invocation records matching f, newest first, and
// whether more follow.
func (s *Users) Invocations(ctx context.Context, f InvocationFilter) ([]InvocationRecord, bool, error) {
	return s.Store.ListInvocations(ctx, f)
}
