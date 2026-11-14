package admin

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/cli/clicore"
	"github.com/fernandezvara/backd/internal/cmdutil"
)

type migrationFailure struct {
	ID      string `json:"id"`
	Step    string `json:"step"`
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Keyword string `json:"keyword"`
}

type migrationReport struct {
	JobID           string             `json:"job_id"`
	Database        string             `json:"database"`
	Collection      string             `json:"collection"`
	Target          int                `json:"target"`
	DryRun          bool               `json:"dry_run"`
	Status          string             `json:"status"`
	Ending          *string            `json:"ending"`
	FinishedAt      *string            `json:"finished_at"`
	Scanned         int64              `json:"scanned"`
	Migrated        int64              `json:"migrated"`
	Skipped         int64              `json:"skipped"`
	Failed          int64              `json:"failed"`
	Failures        []migrationFailure `json:"failures"`
	FailuresOmitted int                `json:"failures_omitted"`
}

// DataMigrate handles `backd data migrate`: it queues the migration of a collection's documents to
// its current schema version (workers also do it at every start), and with --wait follows it and
// exits 0 when every document migrated and 1 when some did not.
func DataMigrate(c *kli.CommandContext) error {
	database, collection := cmdutil.Str(c, "database"), cmdutil.Str(c, "collection")
	interval, err := time.ParseDuration(cmdutil.Str(c, "interval"))
	if err != nil || interval <= 0 {
		return cmdutil.UsageErr(errors.New("--interval must be a duration such as 2s"))
	}
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	dry := cmdutil.Flag(c, "dry-run")
	var started struct {
		ID             string `json:"id"`
		Target         int    `json:"target"`
		AlreadyRunning bool   `json:"already_running"`
		DocumentsBelow *int64 `json:"documents_below_current"`
	}
	body := map[string]any{"database": database, "collection": collection, "dry_run": dry}
	if err := t.Adm.DataMigrations.Start(t.Ctx, body, &started); err != nil {
		var ae *clicore.APIError
		if errors.As(err, &ae) && ae.Code == "migration_running" && len(ae.Details) > 0 {
			return fmt.Errorf("a migration of this collection to another version is already running (job %s): follow it with `backd functions jobs --realm %s --job %s`, or cancel it with `backd functions cancel --realm %s --job %s`", ae.Details[0].Reason, t.Realm, ae.Details[0].Reason, t.Realm, ae.Details[0].Reason)
		}
		return err
	}
	size := ""
	if started.DocumentsBelow != nil {
		size = fmt.Sprintf(", about %d documents", *started.DocumentsBelow)
	}
	verb := "started"
	if started.AlreadyRunning {
		verb = "was already running"
	}
	what := "migration"
	if dry {
		what = "dry run"
	}
	fmt.Fprintf(c.Stderr(), "schema %s %s %s (%s/%s to version %d%s)\n", what, started.ID, verb, database, collection, started.Target, size)
	if !cmdutil.Flag(c, "wait") {
		fmt.Fprintln(c.Stdout(), started.ID)
		return nil
	}
	job, err := followJob(t, started.ID, interval, c.Stderr(), stepProgress)
	if err != nil {
		return err
	}
	if status := resultStatus(job); status != "ok" {
		return fmt.Errorf("the schema migration ended without a report: %s", status)
	}
	var r migrationReport
	if err := t.Adm.DataMigrations.Get(t.Ctx, database, collection, &r); err != nil {
		return err
	}
	if cmdutil.Flag(c, "json") {
		if err := cmdutil.PrintJSONLines(c.Stdout(), []migrationReport{r}); err != nil {
			return err
		}
	} else {
		printMigrationReport(c.Stdout(), r)
	}
	if r.Failed > 0 {
		return cmdutil.Failed(fmt.Errorf("%d documents did not migrate and stay at their version", r.Failed))
	}
	return nil
}

func printMigrationReport(w io.Writer, r migrationReport) {
	ending := "running"
	if r.Ending != nil {
		ending = *r.Ending
	}
	verb := "migrated"
	if r.DryRun {
		verb = "would migrate"
	}
	fmt.Fprintf(w, "%s/%s to version %d: %s (read %d, %s %d, already current %d, failed %d)\n", r.Database, r.Collection, r.Target, ending, r.Scanned, verb, r.Migrated, r.Skipped, r.Failed)
	if len(r.Failures) == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  ID\tSTEP\tWHY")
	for _, f := range r.Failures {
		why := f.Kind
		if f.Path != "" {
			why += " at " + f.Path
		}
		if f.Keyword != "" {
			why += ": " + f.Keyword
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", f.ID, f.Step, why)
	}
	tw.Flush()
	if r.FailuresOmitted > 0 {
		fmt.Fprintf(w, "  and %d more\n", r.FailuresOmitted)
	}
}

// DataMigrations handles `backd data migrations`: every collection with migrations (or one
// report in full).
func DataMigrations(c *kli.CommandContext) error {
	database, collection := cmdutil.Str(c, "database"), cmdutil.Str(c, "collection")
	if collection != "" && database == "" {
		return cmdutil.UsageErr(errors.New("--collection needs --database"))
	}
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	if collection != "" {
		var r migrationReport
		if err := t.Adm.DataMigrations.Get(t.Ctx, database, collection, &r); err != nil {
			return err
		}
		if cmdutil.Flag(c, "json") {
			return cmdutil.PrintJSONLines(c.Stdout(), []migrationReport{r})
		}
		printMigrationReport(c.Stdout(), r)
		return nil
	}
	var out struct {
		Running []jobRecord `json:"running"`
		Items   []struct {
			Database       string           `json:"database"`
			Collection     string           `json:"collection"`
			CurrentVersion int              `json:"current_version"`
			Below          *int64           `json:"documents_below_current"`
			Report         *migrationReport `json:"report"`
		} `json:"items"`
	}
	if err := t.Adm.DataMigrations.List(t.Ctx, &out); err != nil {
		return err
	}
	if cmdutil.Flag(c, "json") {
		return cmdutil.PrintJSONLines(c.Stdout(), []any{out})
	}
	for _, j := range out.Running {
		p := "starting"
		if j.Progress != nil {
			p = fmt.Sprintf("%s: %.0f documents", j.Progress.Name, j.Progress.Current)
		}
		fmt.Fprintf(c.Stderr(), "a schema migration is %s (job %s, %s): %s\n", j.Status, j.ID, strings.TrimPrefix(j.Function, "schema-migrate:"), p)
	}
	tw := tabwriter.NewWriter(c.Stdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "COLLECTION\tVERSION\tBELOW\tLAST MIGRATION")
	for _, it := range out.Items {
		if database != "" && it.Database != database {
			continue
		}
		below := "?"
		if it.Below != nil {
			below = fmt.Sprint(*it.Below)
		}
		last := "none kept"
		if r := it.Report; r != nil {
			ending := "running"
			if r.Ending != nil {
				ending = *r.Ending
			}
			last = fmt.Sprintf("%s (migrated %d, failed %d)", ending, r.Migrated, r.Failed)
			if r.DryRun {
				last = "dry run: " + last
			}
		}
		fmt.Fprintf(tw, "%s/%s\t%d\t%s\t%s\n", it.Database, it.Collection, it.CurrentVersion, below, last)
	}
	return tw.Flush()
}
