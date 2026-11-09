package servercli

import (
	"context"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/cmdutil"
	"github.com/fernandezvara/backd/internal/server"
)

// role runs one of backd's server roles with its flags (see server.Run).
func role(name string) kli.CommandFunc {
	return cmdutil.Act("backd-server "+name, func(c *kli.CommandContext) error {
		o := server.Options{WithWorker: name == "serve" && cmdutil.Flag(c, "with-worker")}
		if name == "provision" {
			o.DisablePreImages, o.Yes = cmdutil.Flag(c, "disable-preimages"), cmdutil.Flag(c, "yes")
		}
		return server.Run(context.Background(), name, c.Getenv, c.Stderr(), o)
	})
}

// registerRoles adds the commands that run backd's server roles. Plain `backd` starts
// none of them: it prints the help (see isGlobalHelp).
func registerRoles(cfg *kli.Config) {
	serveHelp := "Loads CONFIG_DIR, provisions or verifies MongoDB (PROVISION_MODE), and serves\nHTTP. --with-worker also runs the worker role (async jobs) in this process; it\nneeds BACKD_EXECUTOR_URL."
	withWorker := func(cc *kli.CommandConfig) {
		cmdutil.Boolean(cc, "with-worker", "also run the worker role (async jobs) in this process")
	}
	cfg.Command("serve").ShortHelp("load config, provision or verify MongoDB, serve HTTP").LongHelp(serveHelp).
		Func(role("serve")).Config(withWorker)
	cfg.Command("worker").ShortHelp("run the worker role alone: claims and runs async jobs").
		LongHelp("Claims and runs async jobs (mode: async) and scheduled functions. Needs\nBACKD_EXECUTOR_URL; WORKER_CONCURRENCY caps jobs running at once (default 10).").
		Func(role("worker")).Config(func(*kli.CommandConfig) {})
	cfg.Command("provision").ShortHelp("load config, apply provisioning once and exit").
		LongHelp("Creates missing collections, validators and indexes, and enables pre-change images on\nrealtime collections. Nothing is ever removed, except with --disable-preimages --yes:\nthat switches the images off on collections that no longer need them, and deletes the\nimages stored so far for good.").
		Func(role("provision")).Config(func(cc *kli.CommandConfig) {
		cmdutil.Boolean(cc, "disable-preimages", "switch off pre-change images on collections that no longer need them (irreversible)")
		cmdutil.Boolean(cc, "yes", "confirm --disable-preimages")
	})
	cfg.Command("executor").ShortHelp("run server-side functions for backd").LongHelp(server.ExecutorHelp).
		Func(cmdutil.Act("backd-server executor", func(c *kli.CommandContext) error { return server.ServeExecutor(c.Getenv, c.Stderr()) })).
		Config(func(*kli.CommandConfig) {})
	cfg.Command("egress").ShortHelp("run the egress role, the executor's only way out").LongHelp(server.EgressHelp).
		Func(cmdutil.Act("backd-server egress", func(c *kli.CommandContext) error { return server.ServeEgress(c.Getenv, c.Stderr()) })).
		Config(func(*kli.CommandConfig) {})
}
