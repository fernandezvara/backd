package admin

import (
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/fernandezvara/backd/internal/cli/clicore"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/cmdutil"
)

// triggerStatus mirrors GET _admin/triggers/status.
type triggerStatus struct {
	Dispatcher *struct {
		Started   bool     `json:"started"`
		Held      bool     `json:"held"`
		Epoch     int64    `json:"epoch"`
		Watermark *string  `json:"watermark_at"`
		AgeSecs   *float64 `json:"watermark_age_seconds"`
		Expires   *string  `json:"lease_expires_at"`
	} `json:"dispatcher"`
	Triggers []struct {
		Database   string   `json:"database"`
		Collection string   `json:"collection"`
		Name       string   `json:"name"`
		Events     []string `json:"events"`
		When       *string  `json:"when"`
		Function   string   `json:"function"`
		Fields     []string `json:"fields"`
		Session    bool     `json:"session"`
		Ordered    bool     `json:"ordered"`
	} `json:"triggers"`
	Backlog  *int `json:"backlog"`
	OpenGaps *int `json:"open_gaps"`
}

// triggerGap mirrors a gap of GET _admin/triggers/gaps.
type triggerGap struct {
	ID         string   `json:"id"`
	Reason     string   `json:"reason"`
	Realm      *string  `json:"realm"`
	Collection *string  `json:"collection"`
	Trigger    *string  `json:"trigger"`
	From       string   `json:"from"`
	To         string   `json:"to"`
	Count      int      `json:"count"`
	Keys       []string `json:"keys"`
	RecordedAt string   `json:"recorded_at"`
	ResolvedAt *string  `json:"resolved_at"`
}

// triggersStatus handles `backd triggers status`.
func TriggersStatus(c *kli.CommandContext) error {
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	var st triggerStatus
	if err := t.Adm.Triggers.Status(t.Ctx, &st); err != nil {
		return err
	}
	if cmdutil.Flag(c, "json") {
		return cmdutil.PrintJSONLines(c.Stdout(), []triggerStatus{st})
	}
	out := c.Stdout()
	switch d := st.Dispatcher; {
	case d == nil:
		fmt.Fprintln(out, "dispatcher: this instance can't read its state")
	case !d.Started:
		fmt.Fprintln(out, "dispatcher: never started (no instance with BACKD_TRIGGERS has held the lease)")
	default:
		held := "no instance holds the lease"
		if d.Held {
			held = "leased"
		}
		age := "-"
		if d.AgeSecs != nil {
			age = (time.Duration(*d.AgeSecs) * time.Second).Round(time.Second).String()
		}
		fmt.Fprintf(out, "dispatcher: %s (epoch %d), position stored %s ago\n", held, d.Epoch, age)
	}
	if st.Backlog != nil {
		fmt.Fprintf(out, "unfinished trigger jobs: %d\n", *st.Backlog)
	}
	if st.OpenGaps != nil {
		fmt.Fprintf(out, "open gaps: %d\n", *st.OpenGaps)
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\nTRIGGER\tEVENTS\tFUNCTION\tORDERED\tBODIES\tWHEN\tACCESS")
	for _, tr := range st.Triggers {
		bodies := "ids only"
		if len(tr.Fields) > 0 {
			bodies = strings.Join(tr.Fields, ",")
		}
		when := "always"
		if tr.When != nil {
			when = *tr.When
		}
		fmt.Fprintf(tw, "%s/%s/%s\t%s\t%s\t%v\t%s\t%s\truns with admin data access\n",
			tr.Database, tr.Collection, tr.Name, strings.Join(tr.Events, ","), tr.Function, tr.Ordered, bodies, when)
	}
	return tw.Flush()
}

// triggersGaps handles `backd triggers gaps`.
func TriggersGaps(c *kli.CommandContext) error {
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	q := url.Values{}
	if cmdutil.Flag(c, "all") {
		q.Set("all", "true")
	}
	var out struct{ Items []triggerGap }
	if err := t.Adm.Triggers.Gaps(t.Ctx, q, &out); err != nil {
		return err
	}
	if cmdutil.Flag(c, "json") {
		return cmdutil.PrintJSONLines(c.Stdout(), out.Items)
	}
	tw := tabwriter.NewWriter(c.Stdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tREASON\tCOLLECTION\tTRIGGER\tFROM\tTO\tCOUNT\tSTATE")
	for _, g := range out.Items {
		state := "open"
		if g.ResolvedAt != nil {
			state = "resolved " + *g.ResolvedAt
		}
		coll := "(every collection)"
		if g.Collection != nil {
			coll = *g.Collection
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n", g.ID, g.Reason, coll, orDash(g.Trigger), g.From, g.To, g.Count, state)
	}
	return tw.Flush()
}

// triggersResolve handles `backd triggers resolve`.
func TriggersResolve(c *kli.CommandContext) error {
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	var out struct {
		ID         string `json:"id"`
		ResolvedAt string `json:"resolved_at"`
	}
	if err := t.Adm.Triggers.ResolveGap(t.Ctx, cmdutil.Str(c, "id"), &out); err != nil {
		return err
	}
	fmt.Fprintf(c.Stdout(), "gap %s resolved at %s\n", out.ID, out.ResolvedAt)
	return nil
}
