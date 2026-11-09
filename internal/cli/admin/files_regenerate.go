package admin

import (
	"errors"
	"fmt"
	"time"

	"github.com/fernandezvara/backd/internal/cli/clicore"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/cmdutil"
)

// filesRegenerate handles `backd files regenerate`: it starts the job that
// makes the stale image versions of a file field again and follows it.
func FilesRegenerate(c *kli.CommandContext) error {
	interval, err := time.ParseDuration(cmdutil.Str(c, "interval"))
	if err != nil || interval <= 0 {
		return cmdutil.UsageErr(errors.New("--interval must be a duration such as 2s"))
	}
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	body := map[string]any{"database": cmdutil.Str(c, "database"), "collection": cmdutil.Str(c, "collection"), "field": cmdutil.Str(c, "field"), "missing_only": cmdutil.Flag(c, "missing-only")}
	if v := cmdutil.Str(c, "version"); v != "" {
		body["version"] = v
	}
	if n := cmdutil.Num(c, "rate"); n > 0 {
		body["rate"] = n
	}
	var started struct {
		ID string `json:"id"`
	}
	if err := t.Adm.Files.RegenerateVersions(t.Ctx, body, &started); err != nil {
		var ae *clicore.APIError
		if errors.As(err, &ae) && ae.Code == "regenerate_running" {
			return fmt.Errorf("a regeneration of this field is already running: follow it with `backd functions jobs --realm %s`, or cancel it with `backd functions cancel --realm %s --job <id>`", t.Realm, t.Realm)
		}
		return err
	}
	stderr := c.Stderr()
	fmt.Fprintf(stderr, "regeneration %s started: a worker makes the versions again; the old copies are served until the new ones are ready\n", started.ID)
	if cmdutil.Flag(c, "no-wait") {
		fmt.Fprintln(c.Stdout(), started.ID)
		return nil
	}
	last := ""
	for {
		var job jobRecord
		if err := t.Adm.Jobs.Get(t.Ctx, started.ID, &job); err != nil {
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
				return cmdutil.Failed(fmt.Errorf("the regeneration did not finish: %s (see `backd functions jobs --realm %s --job %s`)", status, t.Realm, started.ID))
			}
			fmt.Fprintln(c.Stdout(), "done")
			return nil
		}
		time.Sleep(interval)
	}
}
