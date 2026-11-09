package admin

import (
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

	"github.com/fernandezvara/backd/internal/cli/clicore"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/apiheaders"
	"github.com/fernandezvara/backd/internal/cmdutil"
	"github.com/fernandezvara/backd/pkg/client"
)

// functionTarget resolves --function <realm>/<database>/<name> and the
// server to talk to.
func functionTarget(c *kli.CommandContext) (t *clicore.Target, database, name string, err error) {
	realm, database, name, err := splitFunctionPath(cmdutil.Str(c, "function"))
	if err != nil {
		return nil, "", "", cmdutil.UsageErr(err)
	}
	t, err = clicore.TargetFor(c, realm, false)
	return t, database, name, err
}

// functionsInvoke handles `backd functions invoke`: a normal call to a
// running backd, through the same route and rules any other caller goes
// through (not a local shortcut), so what it shows is what production does.
func FunctionsInvoke(c *kli.CommandContext) error {
	realm, database, name, err := splitFunctionPath(cmdutil.Str(c, "function"))
	if err != nil {
		return cmdutil.UsageErr(err)
	}
	stderr := c.Stderr()

	var body json.RawMessage
	if p := cmdutil.Str(c, "input"); p != "" {
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

	t, err := clicore.TargetFor(c, realm, false)
	if err != nil {
		return err
	}
	reqHeaders := map[string]string{}
	if email := cmdutil.Str(c, "as"); email != "" {
		var page struct{ Items []adminUser }
		if err := t.Adm.Users.List(t.Ctx, url.Values{"email": {email}}, &page); err != nil {
			return fmt.Errorf("--as %s: %w", email, err)
		}
		if len(page.Items) == 0 {
			return fmt.Errorf("--as %s: user not found", email)
		}
		reqHeaders["X-Backd-On-Behalf-Of"] = page.Items[0].ID
	}

	start := time.Now()
	var out json.RawMessage
	var hdr http.Header
	callRes, callErr := t.Client.Do(t.Ctx, "POST", []string{database, "_func", name}, body, &out, clicore.WithHeaders(reqHeaders)...)
	if callRes != nil {
		hdr = callRes.Header
	} else if ce, ok := errors.AsType[*client.Error](callErr); ok {
		hdr = ce.Header // an error answer carries the development logs too
	}
	// An internal function has no _func route (it answers like a missing
	// one): an administrator runs it through the admin route instead.
	var ae *clicore.APIError
	if errors.As(callErr, &ae) && ae.Status == http.StatusNotFound && ae.Code == "not_found" {
		adminBody := map[string]any{"input": body}
		if body == nil {
			adminBody["input"] = nil
		}
		if email := cmdutil.Str(c, "as"); email != "" {
			adminBody["as"] = email
		}
		var viaAdmin json.RawMessage
		if h2, err := t.Adm.Functions.Invoke(t.Ctx, database, name, adminBody, &viaAdmin); err == nil {
			hdr, callErr, out = h2, nil, viaAdmin
			fmt.Fprintln(stderr, "(run through the admin API: the function is internal)")
		} else if errors.As(err, &ae) && ae.Status != http.StatusNotFound && ae.Status != http.StatusUnauthorized && ae.Status != http.StatusForbidden {
			callErr = err // the function exists and failed: that error is the answer
		}
	}
	elapsed := time.Since(start)

	if hdr != nil {
		if logs := hdr.Get(apiheaders.DevLogs); logs != "" {
			var lines []struct{ Level, Line string }
			if json.Unmarshal([]byte(logs), &lines) == nil && len(lines) > 0 {
				fmt.Fprintln(stderr, "logs:")
				for _, l := range lines {
					fmt.Fprintf(stderr, "  [%s] %s\n", l.Level, l.Line)
				}
			}
		}
		if ms := hdr.Get(apiheaders.DevDuration); ms != "" {
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

// invocationRecord mirrors what GET _admin/invocations returns.
type invocationRecord struct {
	ID         string  `json:"id"`
	At         string  `json:"at"`
	Function   string  `json:"function"`
	Actor      string  `json:"actor"`
	Mode       string  `json:"mode"`
	Status     string  `json:"status"`
	Code       *string `json:"code"`
	DurationMS int64   `json:"duration_ms"`
	RequestID  *string `json:"request_id"`
	JobID      *string `json:"job_id"`
	// Origin is what started the call (`trigger:<database>/<collection>/<trigger>` for a
	// trigger run) and EventID the change a trigger run was made for.
	Origin  *string         `json:"origin"`
	EventID *string         `json:"event_id"`
	Logs    []invocationLog `json:"logs"`
	// Steps are what the call reported with ctx.step, closed when it ended.
	Steps        []stepRecord `json:"steps"`
	StepsOmitted int          `json:"steps_omitted"`
}

// stepRecord mirrors a step of the admin API.
type stepRecord struct {
	N          int      `json:"n"`
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	StartedAt  string   `json:"started_at"`
	EndedAt    *string  `json:"ended_at"`
	DurationMS *int64   `json:"duration_ms"`
	Current    float64  `json:"current"`
	Total      *float64 `json:"total"`
	Message    *string  `json:"message"`
	UpdatedAt  string   `json:"updated_at"`
}

// progressText is "name 4/10", "name 4" without a total, or "-".
func progressText(name string, current float64, total *float64) string {
	if name == "" {
		return "-"
	}
	if total != nil {
		return fmt.Sprintf("%s %s/%s", name, strconv.FormatFloat(current, 'f', -1, 64), strconv.FormatFloat(*total, 'f', -1, 64))
	}
	if current > 0 {
		return fmt.Sprintf("%s %s", name, strconv.FormatFloat(current, 'f', -1, 64))
	}
	return name
}

func lastStepText(steps []stepRecord) string {
	if len(steps) == 0 {
		return "-"
	}
	l := steps[len(steps)-1]
	return progressText(l.Name, l.Current, l.Total)
}

type invocationLog struct {
	Level string `json:"level"`
	Line  string `json:"line"`
}

// functionsHistory handles `backd functions history`.
func FunctionsHistory(c *kli.CommandContext) error {
	q := url.Values{}
	limit, err := invocationsQuery(c, q, time.Now())
	if err != nil {
		return cmdutil.UsageErr(err)
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
	if cmdutil.Flag(c, "json") {
		return cmdutil.PrintJSONLines(c.Stdout(), recs)
	}
	tw := tabwriter.NewWriter(c.Stdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tSTATUS\tCODE\tDURATION\tLAST STEP\tACTOR\tREQUEST-ID\tORIGIN\tEVENT-ID")
	for _, r := range recs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%dms\t%s\t%s\t%s\t%s\t%s\n", r.At, r.Status, orDash(r.Code), r.DurationMS, lastStepText(r.Steps), r.Actor, orDash(r.RequestID), orDash(r.Origin), orDash(r.EventID))
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
	// Progress is the current step; the job's detail (--job) has every Step.
	Progress *struct {
		Step    int      `json:"step"`
		Name    string   `json:"name"`
		Status  string   `json:"status"`
		Current float64  `json:"current"`
		Total   *float64 `json:"total"`
		Message *string  `json:"message"`
	} `json:"progress"`
	Steps        []stepRecord `json:"steps,omitempty"`
	StepsOmitted int          `json:"steps_omitted,omitempty"`
	Result       *struct {
		Status     string  `json:"status"`
		Code       *string `json:"code"`
		DurationMS int64   `json:"duration_ms"`
	} `json:"result"`
}

// functionsJobs handles `backd functions jobs`: the realm's async and
// scheduled jobs, newest first, for one function or all of them.
func FunctionsJobs(c *kli.CommandContext) error {
	if id := cmdutil.Str(c, "job"); id != "" {
		return functionsJobDetail(c, id)
	}
	function, realm := cmdutil.Str(c, "function"), cmdutil.Str(c, "realm")
	q := url.Values{}
	switch {
	case function != "" && realm != "":
		return cmdutil.UsageErr(errors.New("give --function <realm>/<database>/<name> or --realm, not both"))
	case function != "":
		r, database, name, err := splitFunctionPath(function)
		if err != nil {
			return cmdutil.UsageErr(err)
		}
		realm = r
		q.Set("function", database+"/"+name)
	case realm == "":
		return cmdutil.UsageErr(errors.New("give --function <realm>/<database>/<name>, or --realm to list every function's jobs"))
	}
	limit, err := invocationsQuery(c, q, time.Now())
	if err != nil {
		return cmdutil.UsageErr(err)
	}
	if v := cmdutil.Str(c, "status"); v != "" {
		q.Set("status", v)
	}
	if cmdutil.Flag(c, "scheduled") {
		q.Set("scheduled", "true")
	}
	t, err := clicore.TargetFor(c, realm, false)
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
		if err := t.Adm.Jobs.List(t.Ctx, q, &page); err != nil {
			return err
		}
		jobs = append(jobs, page.Items...)
		if !page.HasMore {
			break
		}
		skip += len(page.Items)
	}
	if cmdutil.Flag(c, "json") {
		return cmdutil.PrintJSONLines(c.Stdout(), jobs)
	}
	tw := tabwriter.NewWriter(c.Stdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CREATED\tFUNCTION\tSTATUS\tRESULT\tCODE\tDURATION\tTRIES\tNEXT ATTEMPT\tSCHEDULED\tPROGRESS\tID")
	for _, j := range jobs {
		result, code, duration := "-", "-", "-"
		if j.Result != nil {
			result, code, duration = j.Result.Status, orDash(j.Result.Code), fmt.Sprintf("%dms", j.Result.DurationMS)
		}
		next := "-"
		if j.NextAttempt != nil {
			next = *j.NextAttempt
		}
		progress := "-"
		if p := j.Progress; p != nil {
			progress = progressText(p.Name, p.Current, p.Total)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%t\t%s\t%s\n", j.CreatedAt, j.Function, j.Status, result, code, duration, j.Attempts, next, j.Scheduled, progress, j.ID)
	}
	return tw.Flush()
}

// functionsJobDetail handles `backd functions jobs --job <id>`: one job with every
// step its running or last attempt reported.
func functionsJobDetail(c *kli.CommandContext, id string) error {
	if cmdutil.Str(c, "function") != "" || cmdutil.Str(c, "realm") == "" {
		return cmdutil.UsageErr(errors.New("--job needs --realm (the job's id is unique within it) and no --function"))
	}
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	var j jobRecord
	if err := t.Adm.Jobs.Get(t.Ctx, id, &j); err != nil {
		return err
	}
	if cmdutil.Flag(c, "json") {
		return cmdutil.PrintJSONLines(c.Stdout(), []jobRecord{j})
	}
	out := c.Stdout()
	result := "-"
	if j.Result != nil {
		result = j.Result.Status
	}
	fmt.Fprintf(out, "%s  %s  %s  result %s  tries %d\n", j.ID, j.Function, j.Status, result, j.Attempts)
	if len(j.Steps) == 0 {
		fmt.Fprintln(out, "no steps reported")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STEP\tNAME\tSTATUS\tDURATION\tPROGRESS\tMESSAGE")
	for _, s := range j.Steps {
		duration := "-"
		if s.DurationMS != nil {
			duration = fmt.Sprintf("%dms", *s.DurationMS)
		}
		msg := "-"
		if s.Message != nil {
			msg = *s.Message
		}
		prog := "-"
		if s.Total != nil {
			prog = fmt.Sprintf("%s/%s", strconv.FormatFloat(s.Current, 'f', -1, 64), strconv.FormatFloat(*s.Total, 'f', -1, 64))
		} else if s.Current > 0 {
			prog = strconv.FormatFloat(s.Current, 'f', -1, 64)
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", s.N, s.Name, s.Status, duration, prog, msg)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if j.StepsOmitted > 0 {
		fmt.Fprintf(out, "(%d steps left out of the middle)\n", j.StepsOmitted)
	}
	return nil
}

// functionsCancel handles `backd functions cancel`: ends a job that hasn't
// finished.
func FunctionsCancel(c *kli.CommandContext) error { return jobControl(c, "cancel", "cancelled") }

// functionsRerun handles `backd functions rerun`: queues a finished job again.
func FunctionsRerun(c *kli.CommandContext) error { return jobControl(c, "rerun", "queued as") }

// jobControl posts to _admin/jobs/{id}/{op} and prints the job the answer names.
func jobControl(c *kli.CommandContext, op, verb string) error {
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	var job jobRecord
	if err := t.Adm.Jobs.Control(t.Ctx, cmdutil.Str(c, "job"), op, &job); err != nil {
		return err
	}
	if cmdutil.Flag(c, "json") {
		return cmdutil.PrintJSONLines(c.Stdout(), []jobRecord{job})
	}
	fmt.Fprintf(c.Stdout(), "%s %s (%s)\n", verb, job.ID, job.Function)
	return nil
}

// scheduleRecord mirrors what GET _admin/schedules returns.
type scheduleRecord struct {
	Function  string  `json:"function"`
	Schedule  string  `json:"schedule"`
	Timezone  string  `json:"timezone"`
	Overlap   string  `json:"overlap"`
	Paused    bool    `json:"paused"`
	ChangedAt *string `json:"changed_at"`
	ChangedBy *string `json:"changed_by"`
}

// functionsSchedules handles `backd functions schedules`.
func FunctionsSchedules(c *kli.CommandContext) error {
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	var out struct{ Items []scheduleRecord }
	if err := t.Adm.Functions.Schedules(t.Ctx, &out); err != nil {
		return err
	}
	if cmdutil.Flag(c, "json") {
		return cmdutil.PrintJSONLines(c.Stdout(), out.Items)
	}
	tw := tabwriter.NewWriter(c.Stdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "FUNCTION\tSCHEDULE\tTIMEZONE\tOVERLAP\tSTATE\tCHANGED\tBY")
	for _, s := range out.Items {
		state, changed, by := "running", "-", "-"
		if s.Paused {
			state = "paused"
		}
		if s.ChangedAt != nil {
			changed = *s.ChangedAt
		}
		if s.ChangedBy != nil {
			by = *s.ChangedBy
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.Function, s.Schedule, s.Timezone, s.Overlap, state, changed, by)
	}
	return tw.Flush()
}

func FunctionsPause(c *kli.CommandContext) error  { return scheduleControl(c, "pause") }
func FunctionsResume(c *kli.CommandContext) error { return scheduleControl(c, "resume") }

// scheduleControl posts to _admin/functions/{database}/{name}/{op}.
func scheduleControl(c *kli.CommandContext, op string) error {
	t, database, name, err := functionTarget(c)
	if err != nil {
		return err
	}
	var s scheduleRecord
	if err := t.Adm.Functions.Control(t.Ctx, database, name, op, &s); err != nil {
		return err
	}
	if cmdutil.Flag(c, "json") {
		return cmdutil.PrintJSONLines(c.Stdout(), []scheduleRecord{s})
	}
	state := "running"
	if s.Paused {
		state = "paused"
	}
	fmt.Fprintf(c.Stdout(), "%s: schedule %q is %s\n", s.Function, s.Schedule, state)
	return nil
}

// functionsLogs handles `backd functions logs`.
func FunctionsLogs(c *kli.CommandContext) error {
	q := url.Values{}
	limit, err := invocationsQuery(c, q, time.Now())
	if err != nil {
		return cmdutil.UsageErr(err)
	}
	t, database, name, err := functionTarget(c)
	if err != nil {
		return err
	}
	q.Set("function", database+"/"+name)
	if v := cmdutil.Str(c, "request-id"); v != "" {
		q.Set("request_id", v)
	}
	recs, err := fetchInvocations(t, q, limit)
	if err != nil {
		return err
	}
	if cmdutil.Flag(c, "json") {
		return cmdutil.PrintJSONLines(c.Stdout(), recs)
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
func invocationsQuery(c *kli.CommandContext, q url.Values, now time.Time) (int, error) {
	if v := cmdutil.Str(c, "since"); v != "" {
		since, err := cmdutil.SinceParam(v, now)
		if err != nil {
			return 0, err
		}
		q.Set("since", since)
	}
	return int(cmdutil.Num(c, "limit")), nil
}

// fetchInvocations pages through GET _admin/invocations until limit
// records are collected or the server has no more.
func fetchInvocations(t *clicore.Target, q url.Values, limit int) ([]invocationRecord, error) {
	var recs []invocationRecord
	for skip := 0; len(recs) < limit; {
		var page struct {
			Items   []invocationRecord
			HasMore bool `json:"has_more"`
		}
		q.Set("limit", strconv.Itoa(min(100, limit-len(recs))))
		q.Set("skip", strconv.Itoa(skip))
		if err := t.Adm.Functions.Invocations(t.Ctx, q, &page); err != nil {
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
