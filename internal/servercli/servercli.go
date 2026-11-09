package servercli

import (
	"fmt"
	"io"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/buildinfo"
	"github.com/fernandezvara/backd/internal/cli/admin"
	"github.com/fernandezvara/backd/internal/cmdutil"
	"github.com/fernandezvara/backd/internal/registry"
)

// Run executes one backd-server command line and returns the process exit code.
func Run(args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		args = []string{"version"}
	}
	return cmdutil.Execute(newCLI(getenv, stdin, stdout, stderr), "backd-server", args, stderr)
}

// newCLI builds backd-server's command tree: everything that opens MongoDB or runs Deno. The
// developer's and administrator's commands are the backd CLI's.
func newCLI(getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) *kli.Config {
	cfg := kli.New().SetName("backd-server").SetEnv(getenv).SetIO(stdin, stdout, stderr)
	cfg.Command("version").ShortHelp("print the version").
		Func(cmdutil.Act("backd-server version", func(c *kli.CommandContext) error {
			fmt.Fprintln(c.Stdout(), "backd-server", buildinfo.Get())
			return nil
		}))
	registerRoles(cfg)

	fns := cfg.Command("functions").ShortHelp("bundle the functions of CONFIG_DIR")
	fns.SubCommand("build").ShortHelp("bundle every function in CONFIG_DIR with deno").
		LongHelp("Bundles every function in CONFIG_DIR with `deno bundle` into\n<realm>/<database>/_functions/.build/, one file per function named by its\ncontent hash, and records them in .build/manifest.json. Needs only CONFIG_DIR\nand Deno " + registry.DenoVersion + " (DENO, or deno on the PATH). --check also type-checks\nTypeScript. backd refuses to start while any bundle is missing or stale, so\nbuild after every change to a functions project, before provisioning or\ndeploying.").
		Func(cmdutil.Act("backd-server functions build", functionsBuild)).Config(func(cc *kli.CommandConfig) {
		cmdutil.Boolean(cc, "check", "also type-check TypeScript")
	})

	cfg.Command("bootstrap").ShortHelp("create a realm's first administrator").
		LongHelp("Creates a realm's first administrator: a user holding an admin role (one with\nadmin: true in realm.yaml), with the password read from the terminal without\necho, or as the first line of standard input when it isn't a terminal.\n\nIt refuses when a user of the realm already holds an admin role, or the email\nis already registered: from then on, administrators log in (`backd login`, the CLI) and\nmanage users through the admin API.\n\nUnlike user and apikey, bootstrap writes to MongoDB directly, so it needs the\nserver's CONFIG_DIR and MONGO_URI, and a provisioned realm: run it where backd\nruns, for example\n  docker compose exec backd backd-server bootstrap --realm <realm> --email <email>").
		Func(cmdutil.Act("backd-server bootstrap", bootstrapAction)).
		Config(func(cc *kli.CommandConfig) {
			cmdutil.Required(cc, "realm", "the realm")
			cmdutil.Required(cc, "email", "the administrator's email")
			cmdutil.Optional(cc, "role", "the admin role, when realm.yaml declares several")
		})

	sec := cfg.Command("secret").ShortHelp("field keys and the master key, straight on MongoDB")
	admin.RegisterFieldKeys(sec, direct)
	sec.SubCommand("rotate-key").ShortHelp("re-encrypt every realm's secrets under a new master key").
		LongHelp("Re-encrypts every realm's secrets from BACKD_SECRETS_KEY (the currently\nconfigured, old key) to BACKD_SECRETS_NEW_KEY (the new one; BACKD_SECRETS_NEW_KEY_FILE\nalso works). It writes to MongoDB directly, so it\nneeds the server's CONFIG_DIR and MONGO_URI: run it where backd runs, for example\n  docker compose exec backd backd-server secret rotate-key\nRestart every backd instance with BACKD_SECRETS_KEY set to the new key afterward.").
		Func(cmdutil.Act("backd-server secret rotate-key", secretRotateKey)).Config(func(*kli.CommandConfig) {})

	u := cfg.Command("user").ShortHelp("break-glass user recovery, straight on MongoDB")
	u.SubCommand("mfa-reset").ShortHelp("remove a user's second factor and recovery codes and end their sessions").
		LongHelp("Removes a user's second factor and recovery codes and ends their sessions, acting on the instance's database\ndirectly: for the only administrator of a realm who lost it (the CLI's `backd user mfa-reset` goes through the admin\nAPI, and an administrator can't reset their own). It needs CONFIG_DIR and MONGO_URI where backd runs, and is\naudited as `instance`.").
		Func(cmdutil.Act("backd-server user mfa-reset", userMFAResetLocal)).
		Config(func(cc *kli.CommandConfig) {
			cmdutil.Required(cc, "realm", "the realm")
			cmdutil.Required(cc, "email", "the user's email")
			cmdutil.Boolean(cc, "yes", "confirm the reset, which ends all of the user's sessions")
		})

	admin.RegisterTransfer(cfg, direct)

	trg := cfg.Command("triggers").ShortHelp("re-emit a collection's changes to its triggers")
	trg.SubCommand("rescan").ShortHelp("queue a collection's recently written documents again").
		LongHelp("Re-emits the documents of a collection written in a time range as synthetic `updated`\nevents, to the triggers that can be evaluated from a document alone, after a gap. It is not a\nreplay of history: a document changed twice is read once, as it is now; deletions can't be\nfound; and a trigger whose condition needs `before` or `changed()` is reported and skipped.\nThe jobs carry `rescan` in their input and have ids of their own, so handlers (which must be\nidempotent anyway) see them as new work.\n\nIt runs here, not through the API: it reads CONFIG_DIR and MONGO_URI like `backd-server provision`,\nand may read a whole collection (over 10,000 documents it asks for --confirm). Without an\nindex on _meta.updated_at, that is a scan of the collection. Audited as triggers.rescan.").
		Func(cmdutil.Act("backd-server triggers rescan", triggersRescan)).Config(func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "realm", "the realm")
		cmdutil.Required(cc, "database", "the database")
		cmdutil.Required(cc, "collection", "the collection")
		cmdutil.Optional(cc, "trigger", "only this trigger (default: every one that can be rescanned)")
		cmdutil.Optional(cc, "updated-since", "documents written from this time: RFC 3339, or a duration back from now such as 24h or 7d")
		cmdutil.Optional(cc, "until", "...before this time (default: now)")
		cmdutil.Boolean(cc, "confirm", "allow reading more than 10,000 documents")
	})
	return cfg
}
