package main

import (
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/fernandezvara/kli"
)

// filesRegenerate handles `backd files regenerate`: it starts the job that
// makes the stale image versions of a file field again and follows it.
func filesRegenerate(c *kli.CommandContext) error {
	interval, err := time.ParseDuration(str(c, "interval"))
	if err != nil || interval <= 0 {
		return usageErr(errors.New("--interval must be a duration such as 2s"))
	}
	t, err := newTarget(str(c, "url"), str(c, "realm"), c.Getenv, false)
	if err != nil {
		return err
	}
	body := map[string]any{"database": str(c, "database"), "collection": str(c, "collection"), "field": str(c, "field"), "missing_only": flag(c, "missing-only")}
	if v := str(c, "version"); v != "" {
		body["version"] = v
	}
	if n := num(c, "rate"); n > 0 {
		body["rate"] = n
	}
	var started struct {
		ID string `json:"id"`
	}
	if err := t.call("POST", "_admin/files/versions/regenerate", nil, body, &started); err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.Code == "regenerate_running" {
			return fmt.Errorf("a regeneration of this field is already running: follow it with `backd functions jobs --realm %s`, or cancel it with `backd functions cancel --realm %s --job <id>`", t.realm, t.realm)
		}
		return err
	}
	stderr := c.Stderr()
	fmt.Fprintf(stderr, "regeneration %s started: a worker makes the versions again; the old copies are served until the new ones are ready\n", started.ID)
	if flag(c, "no-wait") {
		fmt.Fprintln(c.Stdout(), started.ID)
		return nil
	}
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
				return kli.Exit(1, fmt.Errorf("the regeneration did not finish: %s (see `backd functions jobs --realm %s --job %s`)", status, t.realm, started.ID))
			}
			fmt.Fprintln(c.Stdout(), "done")
			return nil
		}
		time.Sleep(interval)
	}
}
