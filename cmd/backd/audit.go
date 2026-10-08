package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/registry"
)

type auditRecord struct {
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
func auditAction(c *kli.CommandContext) error {
	q := url.Values{}
	limit, err := auditQuery(c, q, time.Now())
	if err != nil {
		return usageErr(err)
	}
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	return printAudit(t, q, c, limit, ioOf(c))
}

// auditQuery turns the flags into query parameters and returns the limit.
func auditQuery(c *kli.CommandContext, q url.Values, now time.Time) (int, error) {
	for _, k := range []string{"action", "actor", "target"} {
		if v := str(c, k); v != "" {
			q.Set(k, v)
		}
	}
	if str(c, "target") != "" && str(c, "user") != "" {
		return 0, errors.New("use --user or --target, not both")
	}
	if v := str(c, "since"); v != "" {
		since, err := sinceParam(v, now)
		if err != nil {
			return 0, err
		}
		q.Set("since", since)
	}
	if v := str(c, "until"); v != "" {
		if _, err := time.Parse(time.RFC3339, v); err != nil {
			return 0, fmt.Errorf("--until: want an RFC 3339 time, got %q", v)
		}
		q.Set("until", v)
	}
	return int(num(c, "limit")), nil
}

// sinceParam turns --since (an RFC 3339 time, or a duration back from now
// such as 24h or 7d) into the time the API takes.
func sinceParam(v string, now time.Time) (string, error) {
	if d, err := registry.ParseDuration(v); err == nil && d > 0 {
		return now.Add(-d).UTC().Format(time.RFC3339), nil
	}
	if _, err := time.Parse(time.RFC3339, v); err == nil {
		return v, nil
	}
	return "", fmt.Errorf("--since: want an RFC 3339 time or a duration such as 24h or 7d, got %q", v)
}

func printAudit(t *target, q url.Values, c *kli.CommandContext, limit int, uio userIO) error {
	if email := str(c, "user"); email != "" {
		u, err := (&userCtx{t: t, email: email}).find()
		if err != nil {
			return fmt.Errorf("%w (for a deleted user, use --target user:<id>)", err)
		}
		q.Set("target", "user:"+u.ID)
	}
	var recs []auditRecord
	for skip := 0; len(recs) < limit; {
		var page struct {
			Items   []auditRecord
			HasMore bool `json:"has_more"`
		}
		q.Set("limit", strconv.Itoa(min(100, limit-len(recs))))
		q.Set("skip", strconv.Itoa(skip))
		if err := t.call("GET", "_admin/audit", q, nil, &page); err != nil {
			return err
		}
		recs = append(recs, page.Items...)
		if !page.HasMore {
			break
		}
		skip += len(page.Items)
	}
	if flag(c, "json") {
		enc := json.NewEncoder(uio.stdout)
		for _, r := range recs {
			if err := enc.Encode(r); err != nil {
				return err
			}
		}
		return nil
	}
	tw := tabwriter.NewWriter(uio.stdout, 0, 0, 2, ' ', 0)
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
