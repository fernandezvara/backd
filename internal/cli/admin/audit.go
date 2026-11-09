package admin

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/fernandezvara/backd/internal/cli/clicore"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/cmdutil"
)

type AuditRecord struct {
	ID        string         `json:"id"`
	At        string         `json:"at"`
	Action    string         `json:"action"`
	Actor     string         `json:"actor"`
	Target    *string        `json:"target"`
	Details   map[string]any `json:"details"`
	RequestID *string        `json:"request_id"`
	ClientIP  *string        `json:"client_ip"`
}

// auditAction handles `backd audit`.
func AuditAction(c *kli.CommandContext) error {
	q := url.Values{}
	limit, err := auditQuery(c, q, time.Now())
	if err != nil {
		return cmdutil.UsageErr(err)
	}
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	return printAudit(t, q, c, limit, cmdutil.IOOf(c))
}

// auditQuery turns the flags into query parameters and returns the limit.
func auditQuery(c *kli.CommandContext, q url.Values, now time.Time) (int, error) {
	for _, k := range []string{"action", "actor", "target"} {
		if v := cmdutil.Str(c, k); v != "" {
			q.Set(k, v)
		}
	}
	if cmdutil.Str(c, "target") != "" && cmdutil.Str(c, "user") != "" {
		return 0, errors.New("use --user or --target, not both")
	}
	if v := cmdutil.Str(c, "since"); v != "" {
		since, err := cmdutil.SinceParam(v, now)
		if err != nil {
			return 0, err
		}
		q.Set("since", since)
	}
	if v := cmdutil.Str(c, "until"); v != "" {
		if _, err := time.Parse(time.RFC3339, v); err != nil {
			return 0, fmt.Errorf("--until: want an RFC 3339 time, got %q", v)
		}
		q.Set("until", v)
	}
	return int(cmdutil.Num(c, "limit")), nil
}

func printAudit(t *clicore.Target, q url.Values, c *kli.CommandContext, limit int, uio cmdutil.IO) error {
	if email := cmdutil.Str(c, "user"); email != "" {
		u, err := (&UserCtx{Target: t, Email: email}).find()
		if err != nil {
			return fmt.Errorf("%w (for a deleted user, use --target user:<id>)", err)
		}
		q.Set("target", "user:"+u.ID)
	}
	recs, err := fetchPages[AuditRecord](t.Ctx, q, limit, t.Adm.Audit.List)
	if err != nil {
		return err
	}
	if cmdutil.Flag(c, "json") {
		return cmdutil.PrintJSONLines(uio.Stdout, recs)
	}
	tw := tabwriter.NewWriter(uio.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tACTION\tACTOR\tTARGET\tCLIENT\tDETAILS")
	for _, r := range recs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.At, r.Action, r.Actor, orDash(r.Target), orDash(r.ClientIP), detailsText(r.Details))
	}
	return tw.Flush()
}

// detailsText renders details as sorted key=value pairs.
func detailsText(d map[string]any) string {
	if len(d) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		v := d[k]
		if list, ok := v.([]any); ok {
			strs := make([]string, len(list))
			for j, e := range list {
				strs[j] = fmt.Sprint(e)
			}
			v = "[" + strings.Join(strs, ",") + "]"
		}
		parts[i] = fmt.Sprintf("%s=%v", k, v)
	}
	return strings.Join(parts, " ")
}
