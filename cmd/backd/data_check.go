package main

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/fernandezvara/kli"
)

// checkWarning is said before every check: it is a full read of the data.
const checkWarning = "warning: a schema check reads every stored document of the collections, so on a big collection it can take a long time and loads MongoDB. It runs in the background and only one runs per realm; Ctrl-C stops following it, not the check (cancel it with `backd functions cancel --realm R --job ID`)."

type checkProblem struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type checkedDoc struct {
	ID           string         `json:"id"`
	Deleted      bool           `json:"deleted"`
	Problems     []checkProblem `json:"problems"`
	MoreProblems int            `json:"more_problems"`
}

type checkReport struct {
	Database   string       `json:"database"`
	Collection string       `json:"collection"`
	JobID      string       `json:"job_id"`
	StartedAt  string       `json:"started_at"`
	FinishedAt string       `json:"finished_at"`
	Scanned    int64        `json:"scanned"`
	Invalid    int64        `json:"invalid"`
	Complete   bool         `json:"complete"`
	StoppedBy  *string      `json:"stopped_by"`
	Limit      int          `json:"limit"`
	SchemaHash string       `json:"schema_hash"`
	Documents  []checkedDoc `json:"documents"`
}

// dataCheck handles `backd data check`: it starts a schema check, follows its
// progress, prints the reports and exits non-zero when it found drift.
func dataCheck(c *kli.CommandContext) error {
	database, collection := str(c, "database"), str(c, "collection")
	if collection != "" && database == "" {
		return usageErr(errors.New("--collection needs --database"))
	}
	interval, err := time.ParseDuration(str(c, "interval"))
	if err != nil || interval <= 0 {
		return usageErr(errors.New("--interval must be a duration such as 2s"))
	}
	t, err := newTarget(str(c, "url"), str(c, "realm"), c.Getenv, false)
	if err != nil {
		return err
	}
	body := map[string]any{}
	if database != "" {
		body["database"] = database
	}
	if collection != "" {
		body["collection"] = collection
	}
	if n := num(c, "limit"); n > 0 {
		body["limit"] = n
	}
	stderr := c.Stderr()
	fmt.Fprintln(stderr, checkWarning)
	var started struct {
		ID                 string   `json:"id"`
		Scope              string   `json:"scope"`
		Collections        []string `json:"collections"`
		EstimatedDocuments *int64   `json:"estimated_documents"`
	}
	if err := t.call("POST", "_admin/data-checks", nil, body, &started); err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.Code == "check_running" && len(ae.Details) > 0 {
			return fmt.Errorf("a schema check is already running in this realm (job %s): wait for it, follow it with `backd functions jobs --realm %s --job %s`, or cancel it with `backd functions cancel --realm %s --job %s`", ae.Details[0].Reason, t.realm, ae.Details[0].Reason, t.realm, ae.Details[0].Reason)
		}
		return err
	}
	size := ""
	if started.EstimatedDocuments != nil {
		size = fmt.Sprintf(", about %d documents", *started.EstimatedDocuments)
	}
	fmt.Fprintf(stderr, "schema check %s started (%s: %d collections%s)\n", started.ID, started.Scope, len(started.Collections), size)
	if flag(c, "no-wait") {
		fmt.Fprintln(c.Stdout(), started.ID)
		return nil
	}

	// Follow it: a line on stderr when the step or its percentage changes.
	last := ""
	for {
		var job jobRecord
		if err := t.call("GET", "_admin/jobs/"+url.PathEscape(started.ID), nil, nil, &job); err != nil {
			return err
		}
		if p := job.Progress; p != nil {
			line := fmt.Sprintf("%s %.0f%%", p.Name, p.Current)
			if p.Message != nil {
				line += " (" + *p.Message + ")"
			}
			if line != last {
				fmt.Fprintln(stderr, line)
				last = line
			}
		}
		if job.Status == "done" {
			if job.Result == nil || job.Result.Status != "ok" {
				status := "no result"
				if job.Result != nil {
					status = job.Result.Status
				}
				return fmt.Errorf("the schema check ended without a report: %s", status)
			}
			break
		}
		time.Sleep(interval)
	}

	var reports []checkReport
	for _, key := range started.Collections {
		db, name, _ := strings.Cut(key, "/")
		var r checkReport
		if err := t.call("GET", "_admin/data-checks/"+url.PathEscape(db)+"/"+url.PathEscape(name), nil, nil, &r); err != nil {
			return err
		}
		reports = append(reports, r)
	}
	if flag(c, "json") {
		if err := encodeJSONLines(c.Stdout(), reports); err != nil {
			return err
		}
	} else {
		for _, r := range reports {
			printCheckReport(c.Stdout(), r)
		}
	}
	var invalid, incomplete int
	for _, r := range reports {
		invalid += int(r.Invalid)
		if !r.Complete && r.Invalid == 0 {
			incomplete++
		}
	}
	switch {
	case invalid > 0:
		return kli.Exit(1, fmt.Errorf("drift found: %d invalid documents in %d collections", invalid, countInvalid(reports)))
	case incomplete > 0:
		return kli.Exit(1, fmt.Errorf("%d collections were not checked to the end (the time limit)", incomplete))
	}
	return nil
}

func countInvalid(reports []checkReport) int {
	n := 0
	for _, r := range reports {
		if r.Invalid > 0 {
			n++
		}
	}
	return n
}

// printCheckReport prints one collection's report: its line, then the invalid documents.
func printCheckReport(w io.Writer, r checkReport) {
	state := "complete"
	if !r.Complete {
		why := "stopped"
		if r.StoppedBy != nil {
			why = "stopped by the " + *r.StoppedBy
		}
		state = "incomplete: " + why
	}
	fmt.Fprintf(w, "%s/%s: scanned %d, invalid %d (%s), finished %s, schema %s\n", r.Database, r.Collection, r.Scanned, r.Invalid, state, r.FinishedAt, r.SchemaHash)
	if len(r.Documents) == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  ID\tTRASH\tPROBLEMS")
	for _, d := range r.Documents {
		var ps []string
		for _, p := range d.Problems {
			ps = append(ps, p.Path+": "+p.Reason)
		}
		if d.MoreProblems > 0 {
			ps = append(ps, fmt.Sprintf("and %d more", d.MoreProblems))
		}
		trash := "-"
		if d.Deleted {
			trash = "yes"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", d.ID, trash, strings.Join(ps, "; "))
	}
	tw.Flush()
}

// dataChecks handles `backd data checks`: the latest reports (or one in full).
func dataChecks(c *kli.CommandContext) error {
	database, collection := str(c, "database"), str(c, "collection")
	if collection != "" && database == "" {
		return usageErr(errors.New("--collection needs --database"))
	}
	t, err := newTarget(str(c, "url"), str(c, "realm"), c.Getenv, false)
	if err != nil {
		return err
	}
	if collection != "" {
		var r checkReport
		if err := t.call("GET", "_admin/data-checks/"+url.PathEscape(database)+"/"+url.PathEscape(collection), nil, nil, &r); err != nil {
			return err
		}
		if flag(c, "json") {
			return encodeJSONLines(c.Stdout(), []checkReport{r})
		}
		printCheckReport(c.Stdout(), r)
		return nil
	}
	var out struct {
		Running *jobRecord `json:"running"`
		Items   []struct {
			Database   string       `json:"database"`
			Collection string       `json:"collection"`
			Report     *checkReport `json:"report"`
		} `json:"items"`
	}
	if err := t.call("GET", "_admin/data-checks", nil, nil, &out); err != nil {
		return err
	}
	if flag(c, "json") {
		return encodeJSONLines(c.Stdout(), []any{out})
	}
	if out.Running != nil {
		p := "starting"
		if out.Running.Progress != nil {
			p = fmt.Sprintf("%s %.0f%%", out.Running.Progress.Name, out.Running.Progress.Current)
		}
		fmt.Fprintf(c.Stderr(), "a schema check is %s (job %s): %s\n", out.Running.Status, out.Running.ID, p)
	}
	tw := tabwriter.NewWriter(c.Stdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "COLLECTION\tSCANNED\tINVALID\tCOMPLETE\tFINISHED")
	for _, it := range out.Items {
		if database != "" && it.Database != database {
			continue
		}
		if r := it.Report; r != nil {
			fmt.Fprintf(tw, "%s/%s\t%d\t%d\t%t\t%s\n", it.Database, it.Collection, r.Scanned, r.Invalid, r.Complete, r.FinishedAt)
		} else {
			fmt.Fprintf(tw, "%s/%s\t-\t-\t-\tnever checked\n", it.Database, it.Collection)
		}
	}
	return tw.Flush()
}
