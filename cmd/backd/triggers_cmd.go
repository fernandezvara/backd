package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/fernandezvara/kli"
	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/settings"
	"github.com/fernandezvara/backd/internal/triggers"
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
func triggersStatus(c *kli.CommandContext) error {
	t, err := newTarget(str(c, "url"), str(c, "realm"), c.Getenv, false)
	if err != nil {
		return err
	}
	var st triggerStatus
	if err := t.call("GET", "_admin/triggers/status", nil, nil, &st); err != nil {
		return err
	}
	if flag(c, "json") {
		return encodeJSONLines(c.Stdout(), []triggerStatus{st})
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
func triggersGaps(c *kli.CommandContext) error {
	t, err := newTarget(str(c, "url"), str(c, "realm"), c.Getenv, false)
	if err != nil {
		return err
	}
	q := url.Values{}
	if flag(c, "all") {
		q.Set("all", "true")
	}
	var out struct{ Items []triggerGap }
	if err := t.call("GET", "_admin/triggers/gaps", q, nil, &out); err != nil {
		return err
	}
	if flag(c, "json") {
		return encodeJSONLines(c.Stdout(), out.Items)
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
func triggersResolve(c *kli.CommandContext) error {
	t, err := newTarget(str(c, "url"), str(c, "realm"), c.Getenv, false)
	if err != nil {
		return err
	}
	var out struct {
		ID         string `json:"id"`
		ResolvedAt string `json:"resolved_at"`
	}
	if err := t.call("POST", "_admin/triggers/gaps/"+url.PathEscape(str(c, "id"))+"/resolve", nil, nil, &out); err != nil {
		return err
	}
	fmt.Fprintf(c.Stdout(), "gap %s resolved at %s\n", out.ID, out.ResolvedAt)
	return nil
}

// triggersRescan handles `backd triggers rescan`: it runs on the server side (CONFIG_DIR and
// MONGO_URI, like `backd provision`) because it may read a whole collection, which does not
// belong in a request.
func triggersRescan(c *kli.CommandContext) error {
	now := time.Now().UTC()
	parse := func(flagName string) (time.Time, error) {
		v := str(c, flagName)
		if v == "" {
			return time.Time{}, nil
		}
		s, err := sinceParam(v, now)
		if err != nil {
			return time.Time{}, usageErr(errors.New(strings.Replace(err.Error(), "--since", "--"+flagName, 1)))
		}
		return time.Parse(time.RFC3339, s)
	}
	since, err := parse("updated-since")
	if err != nil {
		return err
	}
	until, err := parse("until")
	if err != nil {
		return err
	}
	if since.IsZero() {
		return usageErr(errors.New("--updated-since is required: a rescan reads the documents written from then on"))
	}
	ctx := context.Background()
	// Not setup(): a rescan runs no function, so it doesn't need the bundles built.
	cfg, err := settings.Load(c.Getenv)
	if err != nil {
		return err
	}
	reg, err := registry.Load(cfg.ConfigDir)
	if err != nil {
		return fmt.Errorf("invalid config:\n%w", err)
	}
	client, err := mongodb.Connect(ctx, cfg.MongoURI)
	if err != nil {
		return err
	}
	a := &app{cfg: cfg, log: slog.New(slog.NewJSONHandler(c.Stderr(), &slog.HandlerOptions{Level: cfg.LogLevel})), reg: reg, client: client}
	defer a.close()
	req := triggers.RescanRequest{
		Realm: str(c, "realm"), Database: str(c, "database"), Collection: str(c, "collection"),
		Trigger: str(c, "trigger"), Since: since, Until: until, Confirm: flag(c, "confirm"), Run: xid.New().String(),
	}
	res, err := triggers.Rescan(ctx, a.reg, req, &mongodb.DocScanner{Client: a.client}, jobQueue{a}, func(ctx context.Context, realm string) map[string]any {
		return a.triggerConfig(ctx, a.reg, realm)
	})
	if err != nil {
		return err
	}
	svc := &auth.Users{Store: mongodb.NewAuthStore(a.client, req.Realm), Realm: req.Realm, Log: a.log}
	svc.AuditAs(ctx, "cli", auth.AuditTriggerRescan, req.Database+"/"+req.Collection, map[string]any{
		"trigger": req.Trigger, "since": since.Format(time.RFC3339), "scanned": res.Scanned, "queued": res.Queued, "run": req.Run,
	})
	out := c.Stdout()
	fmt.Fprintf(out, "rescan %s: %d documents read, %d jobs queued, %d already queued\n", req.Run, res.Scanned, res.Queued, res.Duplicates)
	for name, why := range res.NotRescannable {
		fmt.Fprintf(out, "not rescanned: trigger %s: %s\n", name, why)
	}
	return nil
}
