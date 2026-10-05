package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/fernandezvara/cli"

	"github.com/fernandezvara/backd/internal/functions"
	"github.com/fernandezvara/backd/internal/httpapi"
	"github.com/fernandezvara/backd/internal/registry"
)

func functionsBuild(c *cli.CommandContext) error {
	dir, err := configDir(c)
	if err != nil {
		return err
	}
	reg, err := registry.Load(dir)
	if err != nil {
		return fmt.Errorf("invalid config:\n%w", err)
	}
	built := 0
	err = functions.Build(context.Background(), reg, functions.Options{
		Deno:  c.Getenv("DENO"),
		Check: flag(c, "check"),
		Log: func(format string, a ...any) {
			built++
			fmt.Fprintf(c.Stdout(), format+"\n", a...)
		},
	})
	if err != nil {
		return err
	}
	if built == 0 {
		fmt.Fprintln(c.Stdout(), "no functions in CONFIG_DIR")
	}
	return nil
}

// functionTarget resolves --function <realm>/<database>/<name> and the
// server to talk to.
func functionTarget(c *cli.CommandContext) (t *target, database, name string, err error) {
	realm, database, name, err := splitFunctionPath(str(c, "function"))
	if err != nil {
		return nil, "", "", usageErr(err)
	}
	t, err = newTarget(str(c, "url"), realm, c.Getenv, false)
	return t, database, name, err
}

// functionsInvoke handles `backd functions invoke`: a normal call to a
// running backd, through the same route and rules any other caller goes
// through (not a local shortcut), so what it shows is what production does.
func functionsInvoke(c *cli.CommandContext) error {
	realm, database, name, err := splitFunctionPath(str(c, "function"))
	if err != nil {
		return usageErr(err)
	}
	stderr := c.Stderr()

	var body json.RawMessage
	if p := str(c, "input"); p != "" {
		var data []byte
		if p == "-" {
			data, err = io.ReadAll(c.Stdin())
		} else {
			data, err = os.ReadFile(p)
		}
		if err != nil {
			return err
		}
		if !json.Valid(data) {
			return fmt.Errorf("%s is not valid JSON", p)
		}
		body = json.RawMessage(data)
	}

	t, err := newTarget(str(c, "url"), realm, c.Getenv, false)
	if err != nil {
		return err
	}
	reqHeaders := map[string]string{}
	if email := str(c, "as"); email != "" {
		var page struct{ Items []adminUser }
		if err := t.call("GET", "_admin/users", url.Values{"email": {email}}, nil, &page); err != nil {
			return fmt.Errorf("--as %s: %w", email, err)
		}
		if len(page.Items) == 0 {
			return fmt.Errorf("--as %s: user not found", email)
		}
		reqHeaders["X-Backd-On-Behalf-Of"] = page.Items[0].ID
	}

	start := time.Now()
	var out json.RawMessage
	hdr, callErr := t.callHeaders("POST", database+"/_func/"+name, nil, body, &out, reqHeaders)
	// An internal function has no _func route (it answers like a missing
	// one): an administrator runs it through the admin route instead.
	var ae *apiError
	if errors.As(callErr, &ae) && ae.Status == http.StatusNotFound && ae.Code == "not_found" {
		adminBody := map[string]any{"input": body}
		if body == nil {
			adminBody["input"] = nil
		}
		if email := str(c, "as"); email != "" {
			adminBody["as"] = email
		}
		var viaAdmin json.RawMessage
		if h2, err := t.callHeaders("POST", "_admin/functions/"+database+"/"+name+"/invoke", nil, adminBody, &viaAdmin, nil); err == nil {
			hdr, callErr, out = h2, nil, viaAdmin
			fmt.Fprintln(stderr, "(run through the admin API: the function is internal)")
		} else if errors.As(err, &ae) && ae.Status != http.StatusNotFound && ae.Status != http.StatusUnauthorized && ae.Status != http.StatusForbidden {
			callErr = err // the function exists and failed: that error is the answer
		}
	}
	elapsed := time.Since(start)

	if hdr != nil {
		if logs := hdr.Get(httpapi.DevLogsHeader); logs != "" {
			var lines []struct{ Level, Line string }
			if json.Unmarshal([]byte(logs), &lines) == nil && len(lines) > 0 {
				fmt.Fprintln(stderr, "logs:")
				for _, l := range lines {
					fmt.Fprintf(stderr, "  [%s] %s\n", l.Level, l.Line)
				}
			}
		}
		if ms := hdr.Get(httpapi.DevDurationHeader); ms != "" {
			fmt.Fprintf(stderr, "time: %s (executor: %sms)\n", elapsed, ms)
		} else {
			fmt.Fprintf(stderr, "time: %s\n", elapsed)
		}
	}
	if callErr != nil {
		return callErr
	}
	pretty, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		pretty = out
	}
	fmt.Fprintln(c.Stdout(), string(pretty))
	return nil
}

func functionsTypes(c *cli.CommandContext) error {
	dir, err := configDir(c)
	if err != nil {
		return err
	}
	reg, err := registry.Load(dir)
	if err != nil {
		return fmt.Errorf("invalid config:\n%w", err)
	}
	fmt.Fprint(c.Stdout(), functions.JSDocTypes(reg))
	return nil
}

// invocationRecord mirrors what GET _admin/invocations returns.
type invocationRecord struct {
	ID         string          `json:"id"`
	At         string          `json:"at"`
	Function   string          `json:"function"`
	Actor      string          `json:"actor"`
	Mode       string          `json:"mode"`
	Status     string          `json:"status"`
	Code       *string         `json:"code"`
	DurationMS int64           `json:"duration_ms"`
	RequestID  *string         `json:"request_id"`
	JobID      *string         `json:"job_id"`
	Logs       []invocationLog `json:"logs"`
}

type invocationLog struct {
	Level string `json:"level"`
	Line  string `json:"line"`
}

// functionsHistory handles `backd functions history`.
func functionsHistory(c *cli.CommandContext) error {
	q := url.Values{}
	limit, err := invocationsQuery(c, q, time.Now())
	if err != nil {
		return usageErr(err)
	}
	t, database, name, err := functionTarget(c)
	if err != nil {
		return err
	}
	q.Set("function", database+"/"+name)
	recs, err := fetchInvocations(t, q, limit)
	if err != nil {
		return err
	}
	if flag(c, "json") {
		return encodeJSONLines(c.Stdout(), recs)
	}
	tw := tabwriter.NewWriter(c.Stdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tSTATUS\tCODE\tDURATION\tACTOR\tREQUEST-ID")
	for _, r := range recs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%dms\t%s\t%s\n", r.At, r.Status, orDash(r.Code), r.DurationMS, r.Actor, orDash(r.RequestID))
	}
	return tw.Flush()
}

// jobRecord mirrors what GET _admin/jobs returns.
type jobRecord struct {
	ID          string  `json:"id"`
	Function    string  `json:"function"`
	Status      string  `json:"status"`
	Scheduled   bool    `json:"scheduled"`
	RerunOf     *string `json:"rerun_of"`
	Attempts    int     `json:"attempts"`
	NextAttempt *string `json:"next_attempt_at"`
	CreatedAt   string  `json:"created_at"`
	CompletedAt *string `json:"completed_at"`
	Result      *struct {
		Status     string  `json:"status"`
		Code       *string `json:"code"`
		DurationMS int64   `json:"duration_ms"`
	} `json:"result"`
}

// functionsJobs handles `backd functions jobs`: the realm's async and
// scheduled jobs, newest first, for one function or all of them.
func functionsJobs(c *cli.CommandContext) error {
	function, realm := str(c, "function"), str(c, "realm")
	q := url.Values{}
	switch {
	case function != "" && realm != "":
		return usageErr(errors.New("give --function <realm>/<database>/<name> or --realm, not both"))
	case function != "":
		r, database, name, err := splitFunctionPath(function)
		if err != nil {
			return usageErr(err)
		}
		realm = r
		q.Set("function", database+"/"+name)
	case realm == "":
		return usageErr(errors.New("give --function <realm>/<database>/<name>, or --realm to list every function's jobs"))
	}
	limit, err := invocationsQuery(c, q, time.Now())
	if err != nil {
		return usageErr(err)
	}
	if v := str(c, "status"); v != "" {
		q.Set("status", v)
	}
	if flag(c, "scheduled") {
		q.Set("scheduled", "true")
	}
	t, err := newTarget(str(c, "url"), realm, c.Getenv, false)
	if err != nil {
		return err
	}
	var jobs []jobRecord
	for skip := 0; len(jobs) < limit; {
		var page struct {
			Items   []jobRecord
			HasMore bool `json:"has_more"`
		}
		q.Set("limit", strconv.Itoa(min(100, limit-len(jobs))))
		q.Set("skip", strconv.Itoa(skip))
		if err := t.call("GET", "_admin/jobs", q, nil, &page); err != nil {
			return err
		}
		jobs = append(jobs, page.Items...)
		if !page.HasMore {
			break
		}
		skip += len(page.Items)
	}
	if flag(c, "json") {
		return encodeJSONLines(c.Stdout(), jobs)
	}
	tw := tabwriter.NewWriter(c.Stdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CREATED\tFUNCTION\tSTATUS\tRESULT\tCODE\tDURATION\tTRIES\tNEXT ATTEMPT\tSCHEDULED\tID")
	for _, j := range jobs {
		result, code, duration := "-", "-", "-"
		if j.Result != nil {
			result, code, duration = j.Result.Status, orDash(j.Result.Code), fmt.Sprintf("%dms", j.Result.DurationMS)
		}
		next := "-"
		if j.NextAttempt != nil {
			next = *j.NextAttempt
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%t\t%s\n", j.CreatedAt, j.Function, j.Status, result, code, duration, j.Attempts, next, j.Scheduled, j.ID)
	}
	return tw.Flush()
}

// functionsCancel handles `backd functions cancel`: ends a job that hasn't
// finished.
func functionsCancel(c *cli.CommandContext) error { return jobControl(c, "cancel", "cancelled") }

// functionsRerun handles `backd functions rerun`: queues a finished job again.
func functionsRerun(c *cli.CommandContext) error { return jobControl(c, "rerun", "queued as") }

// jobControl posts to _admin/jobs/{id}/{op} and prints the job the answer names.
func jobControl(c *cli.CommandContext, op, verb string) error {
	t, err := newTarget(str(c, "url"), str(c, "realm"), c.Getenv, false)
	if err != nil {
		return err
	}
	var job jobRecord
	if err := t.call("POST", "_admin/jobs/"+url.PathEscape(str(c, "job"))+"/"+op, nil, nil, &job); err != nil {
		return err
	}
	if flag(c, "json") {
		return encodeJSONLines(c.Stdout(), []jobRecord{job})
	}
	fmt.Fprintf(c.Stdout(), "%s %s (%s)\n", verb, job.ID, job.Function)
	return nil
}

// functionsLogs handles `backd functions logs`.
func functionsLogs(c *cli.CommandContext) error {
	q := url.Values{}
	limit, err := invocationsQuery(c, q, time.Now())
	if err != nil {
		return usageErr(err)
	}
	t, database, name, err := functionTarget(c)
	if err != nil {
		return err
	}
	q.Set("function", database+"/"+name)
	if v := str(c, "request-id"); v != "" {
		q.Set("request_id", v)
	}
	recs, err := fetchInvocations(t, q, limit)
	if err != nil {
		return err
	}
	if flag(c, "json") {
		return encodeJSONLines(c.Stdout(), recs)
	}
	for _, r := range recs {
		for _, l := range r.Logs {
			fmt.Fprintf(c.Stdout(), "%s %s [%s] %s\n", r.At, orDash(r.RequestID), l.Level, l.Line)
		}
	}
	return nil
}

// splitFunctionPath splits "<realm>/<database>/<name>" into its parts.
func splitFunctionPath(s string) (realm, database, name string, err error) {
	parts := strings.SplitN(s, "/", 3)
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("want <realm>/<database>/<name>, got %q", s)
	}
	return parts[0], parts[1], parts[2], nil
}

// invocationsQuery turns --since and --limit into query parameters and
// returns the limit (same shape as audit.go's auditQuery).
func invocationsQuery(c *cli.CommandContext, q url.Values, now time.Time) (int, error) {
	if v := str(c, "since"); v != "" {
		since, err := sinceParam(v, now)
		if err != nil {
			return 0, err
		}
		q.Set("since", since)
	}
	return int(num(c, "limit")), nil
}

// fetchInvocations pages through GET _admin/invocations until limit
// records are collected or the server has no more.
func fetchInvocations(t *target, q url.Values, limit int) ([]invocationRecord, error) {
	var recs []invocationRecord
	for skip := 0; len(recs) < limit; {
		var page struct {
			Items   []invocationRecord
			HasMore bool `json:"has_more"`
		}
		q.Set("limit", strconv.Itoa(min(100, limit-len(recs))))
		q.Set("skip", strconv.Itoa(skip))
		if err := t.call("GET", "_admin/invocations", q, nil, &page); err != nil {
			return nil, err
		}
		recs = append(recs, page.Items...)
		if !page.HasMore {
			break
		}
		skip += len(page.Items)
	}
	return recs, nil
}

// encodeJSONLines writes one JSON object per line (--json), the same
// format audit.go's printAudit uses.
func encodeJSONLines[T any](stdout io.Writer, items []T) error {
	enc := json.NewEncoder(stdout)
	for _, it := range items {
		if err := enc.Encode(it); err != nil {
			return err
		}
	}
	return nil
}
