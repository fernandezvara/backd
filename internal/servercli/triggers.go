package servercli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fernandezvara/kli"
	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/cmdutil"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/server"
	"github.com/fernandezvara/backd/internal/settings"
	"github.com/fernandezvara/backd/internal/triggers"
)

// triggersRescan handles `backd-server triggers rescan`: it runs on the server side (CONFIG_DIR and
// MONGO_URI, like `backd-server provision`) because it may read a whole collection, which does not
// belong in a request.
func triggersRescan(c *kli.CommandContext) error {
	now := time.Now().UTC()
	parse := func(flagName string) (time.Time, error) {
		v := cmdutil.Str(c, flagName)
		if v == "" {
			return time.Time{}, nil
		}
		s, err := cmdutil.SinceParam(v, now)
		if err != nil {
			return time.Time{}, cmdutil.UsageErr(errors.New(strings.Replace(err.Error(), "--since", "--"+flagName, 1)))
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
		return cmdutil.UsageErr(errors.New("--updated-since is required: a rescan reads the documents written from then on"))
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
	a := server.NewApp(cfg, slog.New(slog.NewJSONHandler(c.Stderr(), &slog.HandlerOptions{Level: cfg.LogLevel})), reg, client, "")
	defer a.Close()
	req := triggers.RescanRequest{
		Realm: cmdutil.Str(c, "realm"), Database: cmdutil.Str(c, "database"), Collection: cmdutil.Str(c, "collection"),
		Trigger: cmdutil.Str(c, "trigger"), Since: since, Until: until, Confirm: cmdutil.Flag(c, "confirm"), Run: xid.New().String(),
	}
	res, err := a.Rescan(ctx, req)
	if err != nil {
		return err
	}
	svc := &auth.Users{Store: mongodb.NewAuthStore(a.Client(), req.Realm), Realm: req.Realm, Log: a.Log()}
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
