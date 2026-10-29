package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// JobResult is how an async job ended, once its status is "done": the same shape a synchronous
// call's own answer would carry for the same outcome.
type JobResult struct {
	// Status is "ok", "function_error", "timeout", "memory", "cpu", "crash", "output_too_large" or "bundle".
	Status string `json:"status"`
	// Output is the function's output, when Status is "ok".
	Output json.RawMessage `json:"output,omitempty"`
	// HTTPStatus is the status a synchronous call would have answered with; 0 when Status is "ok".
	HTTPStatus int `json:"http_status,omitempty"`
	// Code is the function's own code (function_error) or one of backd's own.
	Code       string   `json:"code,omitempty"`
	Message    string   `json:"message,omitempty"`
	Details    []Detail `json:"details,omitempty"`
	DurationMs int64    `json:"duration_ms"`
}

// JobStep is one step the function reported with ctx.step(), and how far it got with ctx.progress().
type JobStep struct {
	// N is 1 for the first step of the attempt (it counts steps left out too).
	N    int    `json:"n"`
	Name string `json:"name"`
	// Status is "running", "done", "failed", "timed_out" or "cancelled".
	Status     string  `json:"status"`
	StartedAt  string  `json:"started_at"`
	EndedAt    *string `json:"ended_at"`
	DurationMs *int64  `json:"duration_ms"`
	// Current is how far the step got; Total how much there is to do, when the function said.
	Current   int     `json:"current"`
	Total     *int    `json:"total"`
	Message   *string `json:"message"`
	UpdatedAt string  `json:"updated_at"`
}

// JobData is a job as the API returns it.
type JobData struct {
	ID string `json:"id"`
	// Function is "<database>/<name>".
	Function string `json:"function"`
	// Status is "queued", "running" or "done".
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	// Attempts is how many times a worker has started it.
	Attempts int `json:"attempts,omitempty"`
	// NextAttemptAt is when a failed attempt will be retried (the function's retry policy); nil otherwise.
	NextAttemptAt *string    `json:"next_attempt_at,omitempty"`
	Result        *JobResult `json:"result"`
	// Steps are what the running (or last) attempt reported; at most 100.
	Steps []JobStep `json:"steps,omitempty"`
	// StepsOmitted is how many were left out of the middle when there were more than 100.
	StepsOmitted int `json:"steps_omitted,omitempty"`
}

// JobTimeoutError means Wait gave up before the job finished. The job itself is unaffected and
// still running: wait again, or poll with Status.
type JobTimeoutError struct{ JobID string }

func (e *JobTimeoutError) Error() string {
	return fmt.Sprintf("job %s did not finish within the given timeout", e.JobID)
}

// Job is a handle to an async function's job: a call that the function answers with 202 returns one.
// Poll with Status, or Wait for the output the way a synchronous call returns it.
type Job struct {
	c  *Client
	db string

	// ID and Function are fixed; Data is as of the last Status or Wait poll.
	ID       string
	Function string
	Data     JobData
}

// Steps returns the steps reported as of the last poll (the last is the current step).
func (j *Job) Steps() []JobStep { return j.Data.Steps }

// Status polls the job's current status ("queued", "running" or "done").
func (j *Job) Status(ctx context.Context, opts ...RequestOption) (string, error) {
	if err := j.fetch(ctx, opts); err != nil {
		return "", err
	}
	return j.Data.Status, nil
}

func (j *Job) fetch(ctx context.Context, opts []RequestOption) error {
	var d JobData
	if _, err := j.c.Do(ctx, http.MethodGet, []string{j.db, "_jobs", j.ID}, nil, &d, opts...); err != nil {
		return err
	}
	j.Data = d
	return nil
}

// WaitOptions are the options of Wait.
type WaitOptions struct {
	// PollInterval is the time between polls; 500ms when zero.
	PollInterval time.Duration
	// Timeout gives up with a *JobTimeoutError; without it Wait waits until the job is done or the context ends.
	Timeout time.Duration
	// OnProgress is called after every poll with the steps reported so far.
	OnProgress func(steps []JobStep, job *Job)
}

// Wait polls until the job is done, then returns its output, the value a synchronous call would
// have returned, or an *Error matching what a synchronous call would have returned for the same
// outcome (with the function's own code, when it raised one).
func (j *Job) Wait(ctx context.Context, o WaitOptions, opts ...RequestOption) (json.RawMessage, error) {
	interval := o.PollInterval
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	var deadline time.Time
	if o.Timeout > 0 {
		deadline = time.Now().Add(o.Timeout)
	}
	for {
		if err := j.fetch(ctx, opts); err != nil {
			return nil, err
		}
		if o.OnProgress != nil {
			o.OnProgress(j.Data.Steps, j)
		}
		if j.Data.Status == "done" {
			return outputOrError(j.Data.Result, j.ID)
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return nil, &JobTimeoutError{JobID: j.ID}
		}
		wait := interval
		if !deadline.IsZero() {
			wait = min(wait, max(time.Until(deadline), time.Millisecond))
		}
		if err := sleep(ctx, wait); err != nil {
			return nil, networkError(ctx, err)
		}
	}
}

// WaitInto is Wait that decodes the output into out.
func (j *Job) WaitInto(ctx context.Context, out any, o WaitOptions, opts ...RequestOption) error {
	raw, err := j.Wait(ctx, o, opts...)
	if err != nil {
		return err
	}
	return decodeOutput(raw, out)
}

func decodeOutput(raw json.RawMessage, out any) error {
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return newError(0, "invalid_response", "the function's output is not the JSON expected: "+err.Error(), kBackd)
	}
	return nil
}

func outputOrError(r *JobResult, id string) (json.RawMessage, error) {
	if r == nil {
		return nil, fmt.Errorf("client: job %s is done but has no result", id)
	}
	if r.Status == "ok" {
		return r.Output, nil
	}
	status := r.HTTPStatus
	if status == 0 {
		status = 500
	}
	code, msg := r.Code, r.Message
	if code == "" {
		code = "function_failed"
	}
	if msg == "" {
		msg = "the function failed"
	}
	return nil, &Error{Status: status, Code: code, Message: msg, Details: r.Details, kind: kindFor(code, status)}
}

// WithRespondAsync asks for the call to be answered at once with a job (Prefer: respond-async)
// instead of waiting for the output, when the function allows it.
func WithRespondAsync() RequestOption { return WithHeader("Prefer", "respond-async") }

// Call calls a function of the database with an input. A synchronous function answers with its
// output, which is decoded into out (when not nil) and the returned job is nil. An async function,
// or a call made with [WithRespondAsync], answers with a job: it is returned, and out is untouched;
// Wait for the output. [WithIdempotencyKey] makes a repeat return the first answer.
func (d *Database) Call(ctx context.Context, name string, input, out any, opts ...RequestOption) (*Job, error) {
	var raw json.RawMessage
	res, err := d.c.Do(ctx, http.MethodPost, []string{d.name, "_func", name}, input, &raw, opts...)
	if err != nil {
		return nil, err
	}
	if res.Status == http.StatusAccepted {
		var data JobData
		if err := json.Unmarshal(raw, &data); err != nil {
			return nil, newError(res.Status, "invalid_response", "the job is not the JSON expected: "+err.Error(), kBackd)
		}
		return &Job{c: d.c, db: d.name, ID: data.ID, Function: data.Function, Data: data}, nil
	}
	return nil, decodeOutput(raw, out)
}

// Run calls a function and, if it answers with a job, waits for it: the output ends in out either
// way. It is Call followed by Wait.
func (d *Database) Run(ctx context.Context, name string, input, out any, o WaitOptions, opts ...RequestOption) error {
	job, err := d.Call(ctx, name, input, out, opts...)
	if err != nil || job == nil {
		return err
	}
	return job.WaitInto(ctx, out, o)
}

// Job returns the job with an id (saved from an earlier call), with its current data.
func (d *Database) Job(ctx context.Context, id string, opts ...RequestOption) (*Job, error) {
	j := &Job{c: d.c, db: d.name, ID: id}
	if err := j.fetch(ctx, opts); err != nil {
		return nil, err
	}
	j.Function = j.Data.Function
	return j, nil
}

// Functions calls functions by their full name: c.Functions().Call(ctx, "main/refund", input, &out).
type Functions struct{ c *Client }

// Functions returns the functions of the realm, addressed as "<database>/<name>".
func (c *Client) Functions() *Functions { return &Functions{c: c} }

func (f *Functions) split(name string) (*Database, string, error) {
	db, fn, ok := strings.Cut(name, "/")
	if !ok || db == "" || fn == "" || strings.Contains(fn, "/") {
		return nil, "", fmt.Errorf("client: a function is named <database>/<name>, not %q", name)
	}
	return f.c.DB(db), fn, nil
}

// Call is Database.Call for "<database>/<name>".
func (f *Functions) Call(ctx context.Context, name string, input, out any, opts ...RequestOption) (*Job, error) {
	db, fn, err := f.split(name)
	if err != nil {
		return nil, err
	}
	return db.Call(ctx, fn, input, out, opts...)
}

// Run is Database.Run for "<database>/<name>".
func (f *Functions) Run(ctx context.Context, name string, input, out any, o WaitOptions, opts ...RequestOption) error {
	db, fn, err := f.split(name)
	if err != nil {
		return err
	}
	return db.Run(ctx, fn, input, out, o, opts...)
}
