package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/fernandezvara/cli"

	"github.com/fernandezvara/backd/internal/registry"
)

// newCLI builds backd's command tree. Commands read their flags with str,
// flag and num, and never touch the process environment or standard
// streams directly, so tests run them in-process.
func newCLI(getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) *cli.Config {
	cfg := cli.New().SetName("backd").SetEnv(getenv).SetIO(stdin, stdout, stderr)

	cfg.Command("version").ShortHelp("print the version").
		Func(act("version", func(c *cli.CommandContext) error {
			fmt.Fprintln(c.Stdout(), "backd", version)
			return nil
		}))

	tpl := cfg.Command("template").ShortHelp("create config files under CONFIG_DIR").
		LongHelp("Creates configuration files for realms, databases and functions under\nCONFIG_DIR, leaving existing files unchanged, and lists what it created.")
	tpl.SubCommand("realm").ShortHelp("create <realm>/realm.yaml; --sample adds the blog database").
		Func(act("template realm", templateRealm)).
		Config(func(cc *cli.CommandConfig) {
			required(cc, "realm", "the realm to create")
			boolean(cc, "sample", "also add the blog sample database \"main\"")
		})
	tpl.SubCommand("database").ShortHelp("create a database directory; --sample adds the posts collection").
		Func(act("template database", templateDatabase)).
		Config(func(cc *cli.CommandConfig) {
			required(cc, "realm", "the realm of the database")
			required(cc, "database", "the database to create")
			boolean(cc, "sample", "also add the blog sample collection \"posts\"")
		})
	tpl.SubCommand("function").ShortHelp("create a function in the database's _functions project").
		Func(act("template function", templateFunction)).
		Config(func(cc *cli.CommandConfig) {
			required(cc, "realm", "the realm of the function")
			required(cc, "database", "the database of the function")
			required(cc, "name", "the function to create")
		})
	tpl.SubCommand("email-capture").ShortHelp("add a development delivery function that stores emails in an outbox").
		LongHelp("Adds the function email-capture to the database's _functions project, and the\ncollection outbox it writes to: instead of sending an email, it stores it (links\nincluded) so you can read it. For local development only: the function is\ndev_only, and backd refuses to start with it unless BACKD_DEV=true.").
		Func(act("template email-capture", templateEmailCapture)).
		Config(func(cc *cli.CommandConfig) {
			required(cc, "realm", "the realm of the database")
			required(cc, "database", "the database to add the function and the outbox to")
		})
	tpl.SubCommand("collection-policy").ShortHelp("add a commented collection.yaml: what an erase does to a collection").
		LongHelp("Creates <realm>/<database>/<collection>/collection.yaml, with everything commented\nout: without a policy an erase leaves the collection alone. Uncomment what you need\n(delete or anonymize the documents a user owns, remove the user from other\ndocuments' arrays and fields). See the docs, Auth -> Erasing users.").
		Func(act("template collection-policy", templateCollectionPolicy)).
		Config(func(cc *cli.CommandConfig) {
			required(cc, "realm", "the realm of the collection")
			required(cc, "database", "the database of the collection")
			required(cc, "collection", "the collection to add the file to")
		})
	tpl.SubCommand("project").ShortHelp("create a complete config repository").
		LongHelp("Creates a complete config repository at --dir (which must not exist yet): the\nrealm and sample database, a sample function with tests, Dockerfiles and a\ncompose stack for local development, and a GitHub Actions CI workflow.\nSee the generated README.md.").
		Func(act("template project", templateProject)).
		Config(func(cc *cli.CommandConfig) {
			required(cc, "dir", "the directory to create")
			required(cc, "realm", "the realm of the project")
		})

	cfg.Command("databases").
		ShortHelp("print the MongoDB databases (or collections) the config uses").
		LongHelp("Prints the MongoDB databases the config uses, one per line, for granting a\nleast-privilege user access to exactly these. With --collections, every\ncollection as <database>.<collection>. Needs only CONFIG_DIR.").
		Func(act("databases", listDatabases)).
		Config(func(cc *cli.CommandConfig) {
			boolean(cc, "collections", "print <database>.<collection> instead of databases")
		})

	conf := cfg.Command("config").ShortHelp("check or fingerprint CONFIG_DIR").
		LongHelp("Both need only CONFIG_DIR.")
	conf.SubCommand("check").ShortHelp("validate CONFIG_DIR as startup would").
		LongHelp("Runs every startup check that needs only CONFIG_DIR: names, realm.yaml,\nschemas, indexes, rules, functions and their bundles. Exits non-zero, with the\nsame messages as startup, when backd would refuse to start. For CI, before\npackaging.").
		Func(act("config check", checkConfig)).
		Config(func(*cli.CommandConfig) {})
	conf.SubCommand("fingerprint").ShortHelp("print the fingerprint of CONFIG_DIR").
		LongHelp("Prints one hash of every file backd reads, function bundles included.\n`backd provision` records it; instances in PROVISION_MODE=verify refuse to\nstart with a different one.").
		Func(act("config fingerprint", configFingerprint)).
		Config(func(*cli.CommandConfig) {})

	cfg.Command("bootstrap").ShortHelp("create a realm's first administrator").
		LongHelp("Creates a realm's first administrator: a user holding an admin role (one with\nadmin: true in realm.yaml), with the password read from the terminal without\necho, or as the first line of standard input when it isn't a terminal.\n\nIt refuses when a user of the realm already holds an admin role, or the email\nis already registered: from then on, administrators log in (`backd login`) and\nmanage users through the admin API.\n\nUnlike user and apikey, bootstrap writes to MongoDB directly, so it needs the\nserver's CONFIG_DIR and MONGO_URI, and a provisioned realm: run it where backd\nruns, for example\n  docker compose exec backd /backd bootstrap --realm <realm> --email <email>").
		Func(act("bootstrap", bootstrapAction)).
		Config(func(cc *cli.CommandConfig) {
			required(cc, "realm", "the realm")
			required(cc, "email", "the administrator's email")
			optional(cc, "role", "the admin role, when realm.yaml declares several")
		})
	registerRoles(cfg)
	registerRemote(cfg)
	return cfg
}

// execute runs args (without the program name) and returns the exit code:
// 0, 1 for a failed command, 2 for a usage error, or what the command chose.
func execute(cfg *cli.Config, args []string, stderr io.Writer) int {
	err := cfg.Execute(append([]string{"backd"}, args...))
	if err != nil && !cli.IsReported(err) && err.Error() != "" {
		msg := err.Error()
		if !strings.HasPrefix(msg, "backd") {
			msg = "backd: " + msg
		}
		fmt.Fprintln(stderr, msg)
	}
	return cli.ExitCode(err)
}

// act wraps a command: stray arguments are a usage error, and failures are
// prefixed with the command's name.
func act(name string, fn cli.CommandFunc) cli.CommandFunc {
	return func(c *cli.CommandContext) error {
		if extra := c.Positional(); len(extra) > 0 {
			return cli.Exit(cli.ExitUsage, fmt.Errorf("backd %s: unexpected arguments: %v (values are given as flags: see `backd %s --help`)", name, extra, name))
		}
		if err := fn(c); err != nil {
			var ee *cli.ExitError
			if errors.As(err, &ee) {
				return err
			}
			return fmt.Errorf("backd %s: %w", name, err)
		}
		return nil
	}
}

// usageErr marks err as a usage error (exit code 2): the command was
// invoked wrongly, not one that ran and failed.
func usageErr(err error) error { return cli.Exit(cli.ExitUsage, err) }

func required(cc *cli.CommandConfig, key, description string) {
	cc.Define(key).String().Flag(key).Required().Description(description)
}

func optional(cc *cli.CommandConfig, key, description string) {
	cc.Define(key).String().Flag(key).Default("").Description(description)
}

func boolean(cc *cli.CommandConfig, key, description string) {
	cc.Define(key).Bool().Flag(key).Default(false).Description(description)
}

// str, flag and num read a command's flags.
func str(c *cli.CommandContext, key string) string { return cli.MustGet[string](c, key) }
func flag(c *cli.CommandContext, key string) bool  { return cli.MustGet[bool](c, key) }
func num(c *cli.CommandContext, key string) int64  { return cli.MustGet[int64](c, key) }

func ioOf(c *cli.CommandContext) userIO {
	return userIO{stdin: c.Stdin(), stdout: c.Stdout(), stderr: c.Stderr()}
}

// configDir is CONFIG_DIR, required by commands that read the config.
func configDir(c *cli.CommandContext) (string, error) {
	dir := c.Getenv("CONFIG_DIR")
	if dir == "" {
		return "", errors.New("CONFIG_DIR is required")
	}
	return dir, nil
}

// remote adds the flags every command that talks to a running backd has.
func remote(cc *cli.CommandConfig) {
	required(cc, "realm", "the realm")
	optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
}

const adminAPINote = "Uses the realm's admin API on a running backd, as the session stored by\n`backd login` or with the admin API key in BACKD_API_KEY."

// registerRemote adds the commands that manage a running backd through its
// admin API.
func registerRemote(cfg *cli.Config) {
	loginHelp := "Signs in to a realm of a running backd and keeps the session for later\ncommands, per server and realm, in ~/.config/backd/credentials (or\n$XDG_CONFIG_HOME/backd/credentials, or the file BACKD_CREDENTIALS names),\nreadable only by you. The password is read from the terminal without echo,\nor as the first line of standard input when it isn't a terminal (then\n--email is required).\n\nThe server is --url, else BACKD_URL, else the last one logged in to.\nCommands use BACKD_API_KEY (an admin API key) instead of a stored session\nwhen it is set. BACKD_CA_CERT names a PEM file of extra CAs to trust, for\nservers with a private CA."
	cfg.Command("login").ShortHelp("sign in to a realm and keep the session").LongHelp(loginHelp).
		Func(act("login", loginAction)).
		Config(func(cc *cli.CommandConfig) {
			remote(cc)
			optional(cc, "email", "the email to sign in with (asked for on a terminal)")
		})
	cfg.Command("logout").ShortHelp("end the stored session and forget it").
		Func(act("logout", logoutAction)).Config(remote)
	cfg.Command("whoami").ShortHelp("show who the stored session belongs to").
		Func(act("whoami", whoamiAction)).Config(remote)

	u := cfg.Command("user").ShortHelp("manage a realm's users").
		LongHelp(adminAPINote + " The first administrator of a realm is\ncreated with `backd bootstrap`. Passwords are read from the terminal without\necho, or as the first line of standard input when it isn't a terminal. Emails\ncan't be changed.")
	user := func(name, short string, withEmail bool, run func(*userCtx) error, extra func(*cli.CommandConfig)) {
		u.SubCommand(name).ShortHelp(short).Func(act("user "+name, userAction(withEmail, run))).
			Config(func(cc *cli.CommandConfig) {
				remote(cc)
				if withEmail {
					required(cc, "email", "the user's email")
				}
				if extra != nil {
					extra(cc)
				}
			})
	}
	user("create", "create a user; asks for a password unless --no-password", true, userCreate, func(cc *cli.CommandConfig) {
		boolean(cc, "no-password", "create the user without a password")
	})
	user("list", "list the realm's users", false, userList, nil)
	user("set-password", "set or replace the password; revokes all sessions", true, userSetPassword, nil)
	user("change-email", "change the user's email at once (needs email in realm.yaml); revokes all sessions", true, userChangeEmail, func(cc *cli.CommandConfig) {
		required(cc, "new-email", "the new email address")
	})
	user("owned", "show what erasing the user would do, per collection that declares a policy", true, userOwned, nil)
	user("verify-email", "mark the email as verified", true, userPatch(map[string]bool{"email_verified": true}, "email of %s marked as verified\n"), nil)
	user("disable", "block sign-in and revoke all sessions", true, userPatch(map[string]bool{"disabled": true}, "%s disabled; all of their sessions were revoked\n"), nil)
	user("enable", "allow a disabled user to sign in again", true, userPatch(map[string]bool{"disabled": false}, "%s enabled\n"), nil)
	user("delete", "erase the user: a tombstone, and the collections' policies applied", true, userDelete, func(cc *cli.CommandConfig) {
		boolean(cc, "yes", "confirm the erase, which can't be undone")
		cc.Define("wait").String().Flag("wait").Default("2m").Description("how long to wait for the erase to finish (such as 30s; 0s: don't wait)")
	})
	user("add-role", "assign a role declared in realm.yaml", true, func(c *userCtx) error {
		return userRole(c, "add", str(c.cmd, "role"))
	}, func(cc *cli.CommandConfig) { required(cc, "role", "the role") })
	user("remove-role", "take a role away", true, func(c *userCtx) error {
		return userRole(c, "remove", str(c.cmd, "role"))
	}, func(cc *cli.CommandConfig) { required(cc, "role", "the role") })
	user("networks", "show or limit where the user may use the admin API and log in from", true, userNetworks, func(cc *cli.CommandConfig) {
		optional(cc, "admin", "where the admin API may be used from: comma-separated IP addresses or CIDR networks, or \"none\" to lift the limit")
		optional(cc, "login", "where the user may log in and use their session from (same format)")
	})

	k := cfg.Command("apikey").ShortHelp("manage a realm's API keys").
		LongHelp(adminAPINote + "\n\nAPI keys give server-side services full access to their realm's data.\nStore them as secrets.")
	k.SubCommand("create").ShortHelp("create an API key and print it once").
		LongHelp("--role data (default) reaches data routes; admin also reaches the admin API.\n--expires takes days (\"90d\") or Go durations (\"12h\"); default: never (not\nrecommended: startup warns about such keys). --networks limits where the key\nmay be used from.").
		Func(act("apikey create", apikeyCreate)).Config(func(cc *cli.CommandConfig) {
		remote(cc)
		required(cc, "name", "the key's name")
		cc.Define("role").String().Flag("role").Default("data").OneOf("data", "admin").Description("data (reaches data routes) or admin (also reaches the admin API)")
		optional(cc, "expires", "how long the key is valid, such as 90d or 12h")
		optional(cc, "networks", "where the key may be used from: comma-separated IP addresses or CIDR networks")
	})
	k.SubCommand("list").ShortHelp("list the realm's API keys (never the keys themselves)").
		Func(act("apikey list", apikeyList)).Config(remote)
	k.SubCommand("revoke").ShortHelp("delete the key; it stops working at once").
		Func(act("apikey revoke", apikeyRevoke)).Config(func(cc *cli.CommandConfig) {
		remote(cc)
		required(cc, "name", "the key's name")
	})

	sec := cfg.Command("secret").ShortHelp("manage functions' secrets").
		LongHelp("set, list and delete use the realm's admin API on a running backd, as the\nsession stored by `backd login` or with the admin API key in BACKD_API_KEY.")
	sec.SubCommand("set").ShortHelp("create or replace a secret's value").
		LongHelp("The value is read from the terminal without echo, or the first line of\nstandard input when it isn't a terminal. Without --database it's the realm\nscope (realm.NAME in function.yaml); with it, that database's scope (NAME, for\nfunctions of that database only, never another's).").
		Func(act("secret set", secretSet)).Config(secretFlags)
	sec.SubCommand("list").ShortHelp("list the realm's secrets (never their values)").
		Func(act("secret list", secretList)).Config(remote)
	sec.SubCommand("delete").ShortHelp("delete a secret; a function that declares it answers secret_missing again").
		Func(act("secret delete", secretDelete)).Config(secretFlags)
	sec.SubCommand("rotate-key").ShortHelp("re-encrypt every realm's secrets under a new master key").
		LongHelp("Re-encrypts every realm's secrets from BACKD_SECRETS_KEY (the currently\nconfigured, old key) to BACKD_SECRETS_NEW_KEY (the new one; BACKD_SECRETS_NEW_KEY_FILE\nalso works). Unlike the commands above, this writes to MongoDB directly, so it\nneeds the server's CONFIG_DIR and MONGO_URI: run it where backd runs, for example\n  docker compose exec backd /backd secret rotate-key\nRestart every backd instance with BACKD_SECRETS_KEY set to the new key afterward.").
		Func(act("secret rotate-key", secretRotateKey)).Config(func(*cli.CommandConfig) {})

	cfg.Command("audit").ShortHelp("print the realm's audit trail, newest first").
		LongHelp("Prints who created, changed or deleted users, roles, network restrictions,\nAPI keys and invitations, administrators' logins, and admin requests refused\nby network. " + adminAPINote).
		Func(act("audit", auditAction)).Config(func(cc *cli.CommandConfig) {
		remote(cc)
		optional(cc, "action", "only this action, such as role.add or apikey.create")
		optional(cc, "actor", "only this actor: user:<id>, key:<name>, anonymous, config:realm.yaml or cli:bootstrap")
		optional(cc, "user", "only records about this (existing) user's email")
		optional(cc, "target", "only this target: user:<id>, key:<name> or invitation:<id>")
		optional(cc, "since", "from this time: RFC 3339, or a duration back from now such as 24h or 7d")
		optional(cc, "until", "before this time (RFC 3339)")
		limit(cc)
		boolean(cc, "json", "one JSON object per line, for other tools")
	})

	fn := cfg.Command("functions").ShortHelp("build, call and inspect server-side functions")
	fn.SubCommand("build").ShortHelp("bundle every function in CONFIG_DIR with deno").
		LongHelp("Bundles every function in CONFIG_DIR with `deno bundle` into\n<realm>/<database>/_functions/.build/, one file per function named by its\ncontent hash, and records them in .build/manifest.json. Needs only CONFIG_DIR\nand Deno " + registry.DenoVersion + " (DENO, or deno on the PATH). --check also type-checks\nTypeScript. backd refuses to start while any bundle is missing or stale, so\nbuild after every change to a functions project, before provisioning or\ndeploying.").
		Func(act("functions build", functionsBuild)).Config(func(cc *cli.CommandConfig) {
		boolean(cc, "check", "also type-check TypeScript")
	})
	fn.SubCommand("types").ShortHelp("print JSDoc types generated from the functions' schemas").
		LongHelp("Prints JSDoc types generated from every function's input and output JSON\nSchemas, for editor autocomplete in ctx.input and the return value. Needs only\nCONFIG_DIR.").
		Func(act("functions types", functionsTypes)).Config(func(*cli.CommandConfig) {})
	fnFlag := func(cc *cli.CommandConfig) {
		required(cc, "function", "the function, as <realm>/<database>/<name>")
		optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
	}
	fn.SubCommand("invoke").ShortHelp("call the function through a running backd").
		LongHelp("Calls the function through a running backd's real HTTP path: the same invoke\nrule, schemas and secrets apply as for any other caller. An internal function has\nno such route: it is run through the admin API instead (as --as, if given). --input reads the\nrequest body from a JSON file (\"-\" for standard input); without it, the body\nis null. --as <email> calls on the user's behalf (needs an admin API key,\nBACKD_API_KEY); without it, calls as whatever credential is active. When the\nserver runs with BACKD_DEV=true, also prints the function's console logs and\nits time inside the executor.").
		Func(act("functions invoke", functionsInvoke)).Config(func(cc *cli.CommandConfig) {
		fnFlag(cc)
		optional(cc, "input", "a JSON file with the request body, or - for standard input")
		optional(cc, "as", "call on behalf of this user's email (needs an admin API key)")
	})
	fn.SubCommand("history").ShortHelp("print the function's recent calls, newest first").
		LongHelp("Prints time, status, error code, duration, actor and request id of the\nfunction's recent calls (never the input or output). Records expire after the\nrealm's functions.log_retention (default 7d): a short debugging window, not a\nlog archive. " + adminAPINote).
		Func(act("functions history", functionsHistory)).Config(func(cc *cli.CommandConfig) {
		fnFlag(cc)
		optional(cc, "since", "from this time: RFC 3339, or a duration back from now such as 24h or 7d")
		limit(cc)
		boolean(cc, "json", "one JSON object per line")
	})
	fn.SubCommand("jobs").ShortHelp("list async and scheduled jobs, newest first").
		LongHelp("Lists the realm's async jobs and cron runs with their state and how they ended\n(never their input or output): for one function with --function, or every\nfunction of a realm with --realm. --status narrows to queued, running or done,\n--scheduled to cron runs. A cron run's id is cron_<database>_<function>_<yyyymmddhhmm>\n(UTC). " + adminAPINote).
		Func(act("functions jobs", functionsJobs)).Config(func(cc *cli.CommandConfig) {
		optional(cc, "function", "the function, as <realm>/<database>/<name>")
		optional(cc, "realm", "list every function's jobs in this realm instead")
		optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		cc.Define("status").String().Flag("status").Default("").OneOf("", "queued", "running", "done").Description("only jobs in this state")
		boolean(cc, "scheduled", "only cron runs")
		optional(cc, "since", "from this time: RFC 3339, or a duration back from now such as 24h or 7d")
		limit(cc)
		boolean(cc, "json", "one JSON object per line")
	})
	fn.SubCommand("logs").ShortHelp("print the function's own console output for its recent calls").
		LongHelp("Prints the function's own console output for its recent calls (already masked of\nany declared secrets), oldest first within each call. " + adminAPINote).
		Func(act("functions logs", functionsLogs)).Config(func(cc *cli.CommandConfig) {
		fnFlag(cc)
		optional(cc, "request-id", "only this call")
		optional(cc, "since", "from this time: RFC 3339, or a duration back from now such as 24h or 7d")
		limit(cc)
		boolean(cc, "json", "one JSON object per line")
	})
}

func secretFlags(cc *cli.CommandConfig) {
	remote(cc)
	required(cc, "name", "the secret's name")
	optional(cc, "database", "the database scope; without it, the realm scope")
}

func limit(cc *cli.CommandConfig) {
	cc.Define("limit").Int64().Flag("limit").Default(50).Min(1).Description("at most this many records")
}

// userAction runs a user subcommand against the realm's admin API.
func userAction(withEmail bool, run func(*userCtx) error) cli.CommandFunc {
	return func(c *cli.CommandContext) error {
		uc := &userCtx{cmd: c, uio: ioOf(c), local: localSettings(str(c, "realm"), c.Getenv)}
		if withEmail {
			uc.email = str(c, "email")
		}
		t, err := targetOf(c, false)
		if err != nil {
			return err
		}
		uc.t = t
		return run(uc)
	}
}

// registerRoles adds the commands that run backd's server roles. serve is
// also what plain `backd` does.
func registerRoles(cfg *cli.Config) {
	serveHelp := "Loads CONFIG_DIR, provisions or verifies MongoDB (PROVISION_MODE), and serves\nHTTP. --with-worker also runs the worker role (async jobs) in this process; it\nneeds BACKD_EXECUTOR_URL."
	withWorker := func(cc *cli.CommandConfig) {
		boolean(cc, "with-worker", "also run the worker role (async jobs) in this process")
	}
	cfg.Command("").ShortHelp("load config, provision or verify MongoDB, serve HTTP (the default)").LongHelp(serveHelp).
		Func(role("serve", serve)).Config(withWorker)
	cfg.Command("serve").ShortHelp("load config, provision or verify MongoDB, serve HTTP (the default)").LongHelp(serveHelp).
		Func(role("serve", serve)).Config(withWorker)
	cfg.Command("worker").ShortHelp("run the worker role alone: claims and runs async jobs").
		LongHelp("Claims and runs async jobs (mode: async) and scheduled functions. Needs\nBACKD_EXECUTOR_URL; WORKER_CONCURRENCY caps jobs running at once (default 10).").
		Func(role("worker", worker)).Config(func(*cli.CommandConfig) {})
	cfg.Command("provision").ShortHelp("load config, apply provisioning once and exit").
		Func(role("provision", provision)).Config(func(*cli.CommandConfig) {})
	cfg.Command("executor").ShortHelp("run server-side functions for backd").LongHelp(executorHelp).
		Func(act("executor", func(c *cli.CommandContext) error { return serveExecutor(c.Getenv, c.Stderr()) })).
		Config(func(*cli.CommandConfig) {})
	cfg.Command("egress").ShortHelp("run the egress role, the executor's only way out").LongHelp(egressHelp).
		Func(act("egress", func(c *cli.CommandContext) error { return serveEgress(c.Getenv, c.Stderr()) })).
		Config(func(*cli.CommandConfig) {})
}
