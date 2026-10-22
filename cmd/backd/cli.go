package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/registry"
)

// newCLI builds backd's command tree. Commands read their flags with str,
// flag and num, and never touch the process environment or standard
// streams directly, so tests run them in-process.
func newCLI(getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) *kli.Config {
	cfg := kli.New().SetName("backd").SetEnv(getenv).SetIO(stdin, stdout, stderr)

	cfg.Command("version").ShortHelp("print the version").
		Func(act("version", func(c *kli.CommandContext) error {
			fmt.Fprintln(c.Stdout(), "backd", version)
			return nil
		}))

	tpl := cfg.Command("template").ShortHelp("create config files under CONFIG_DIR").
		LongHelp("Creates configuration files for realms, databases and functions under\nCONFIG_DIR, leaving existing files unchanged, and lists what it created.")
	tpl.SubCommand("realm").ShortHelp("create <realm>/realm.yaml; --sample adds the blog database").
		Func(act("template realm", templateRealm)).
		Config(func(cc *kli.CommandConfig) {
			required(cc, "realm", "the realm to create")
			boolean(cc, "sample", "also add the blog sample database \"main\"")
		})
	tpl.SubCommand("database").ShortHelp("create a database directory; --sample adds the posts collection").
		Func(act("template database", templateDatabase)).
		Config(func(cc *kli.CommandConfig) {
			required(cc, "realm", "the realm of the database")
			required(cc, "database", "the database to create")
			boolean(cc, "sample", "also add the blog sample collection \"posts\"")
		})
	tpl.SubCommand("function").ShortHelp("create a function in the database's _functions project").
		Func(act("template function", templateFunction)).
		Config(func(cc *kli.CommandConfig) {
			required(cc, "realm", "the realm of the function")
			required(cc, "database", "the database of the function")
			required(cc, "name", "the function to create")
		})
	tpl.SubCommand("email-capture").ShortHelp("add a development delivery function that stores emails in an outbox").
		LongHelp("Adds the function email-capture to the database's _functions project, and the\ncollection outbox it writes to: instead of sending an email, it stores it (links\nincluded) so you can read it. For local development only: the function is\ndev_only, and backd refuses to start with it unless BACKD_DEV=true.").
		Func(act("template email-capture", templateEmailCapture)).
		Config(func(cc *kli.CommandConfig) {
			required(cc, "realm", "the realm of the database")
			required(cc, "database", "the database to add the function and the outbox to")
		})
	tpl.SubCommand("collection-policy").ShortHelp("add a commented collection.yaml: rules, soft delete and what an erase does").
		LongHelp("Creates <realm>/<database>/<collection>/collection.yaml, with everything commented\nout: without a policy an erase leaves the collection alone. Uncomment what you need\n(delete or anonymize the documents a user owns, remove the user from other\ndocuments' arrays and fields). See the docs, Auth -> Erasing users.").
		Func(act("template collection-policy", templateCollectionPolicy)).
		Config(func(cc *kli.CommandConfig) {
			required(cc, "realm", "the realm of the collection")
			required(cc, "database", "the database of the collection")
			required(cc, "collection", "the collection to add the file to")
		})
	tpl.SubCommand("project").ShortHelp("create a complete config repository").
		LongHelp("Creates a complete config repository at --dir (which must not exist yet): the\nrealm and sample database, a sample function with tests, a compose stack on the\nreleased images for local development (nothing is built), the check-only\nBackd config check workflow, an AGENTS.md and a README.\nSee the generated README.md.").
		Func(act("template project", templateProject)).
		Config(func(cc *kli.CommandConfig) {
			required(cc, "dir", "the directory to create")
			required(cc, "realm", "the realm of the project")
		})

	cfg.Command("databases").
		ShortHelp("print the MongoDB databases (or collections) the config uses").
		LongHelp("Prints the MongoDB databases the config uses, one per line, for granting a\nleast-privilege user access to exactly these. With --collections, every\ncollection as <database>.<collection>. Needs only CONFIG_DIR.").
		Func(act("databases", listDatabases)).
		Config(func(cc *kli.CommandConfig) {
			boolean(cc, "collections", "print <database>.<collection> instead of databases")
		})

	conf := cfg.Command("config").ShortHelp("check or fingerprint CONFIG_DIR").
		LongHelp("Both need only CONFIG_DIR.")
	conf.SubCommand("check").ShortHelp("validate CONFIG_DIR as startup would").
		LongHelp("Runs every startup check that needs only CONFIG_DIR: names, realm.yaml,\nschemas, indexes, rules, functions and their bundles. Exits non-zero, with the\nsame messages as startup, when backd would refuse to start. For CI, before\npackaging.").
		Func(act("config check", checkConfig)).
		Config(func(*kli.CommandConfig) {})
	conf.SubCommand("fingerprint").ShortHelp("print the fingerprint of CONFIG_DIR").
		LongHelp("Prints one hash of every file backd reads, function bundles included.\n`backd provision` records it; instances in PROVISION_MODE=verify refuse to\nstart with a different one.").
		Func(act("config fingerprint", configFingerprint)).
		Config(func(*kli.CommandConfig) {})

	rl := cfg.Command("rules").ShortHelp("test the access rules of CONFIG_DIR").
		LongHelp("Needs only CONFIG_DIR, no database.")
	rl.SubCommand("test").ShortHelp("run every collection's rules.test.yaml against its rules").
		LongHelp("Runs the fixture next to each collection's collection.yaml (rules.test.yaml): named\ncallers and stored documents, and per operation which of them are allowed or\ndenied. The decisions are the server's: read rules become the database filter,\nevaluated the way MongoDB does; an update or delete first needs the document to\nbe readable. Prints the failures and exits non-zero when an assertion fails or\na fixture is wrong, for CI. Collections with rules and no fixture are listed;\n--strict makes them fail.").
		Func(act("rules test", rulesTest)).
		Config(func(cc *kli.CommandConfig) {
			optional(cc, "collection", "only this collection, or a prefix: realm, realm/database or realm/database/collection")
			boolean(cc, "verbose", "also print the assertions that pass")
			boolean(cc, "strict", "fail when a collection with rules has no rules.test.yaml")
		})

	cfg.Command("bootstrap").ShortHelp("create a realm's first administrator").
		LongHelp("Creates a realm's first administrator: a user holding an admin role (one with\nadmin: true in realm.yaml), with the password read from the terminal without\necho, or as the first line of standard input when it isn't a terminal.\n\nIt refuses when a user of the realm already holds an admin role, or the email\nis already registered: from then on, administrators log in (`backd login`) and\nmanage users through the admin API.\n\nUnlike user and apikey, bootstrap writes to MongoDB directly, so it needs the\nserver's CONFIG_DIR and MONGO_URI, and a provisioned realm: run it where backd\nruns, for example\n  docker compose exec backd /backd bootstrap --realm <realm> --email <email>").
		Func(act("bootstrap", bootstrapAction)).
		Config(func(cc *kli.CommandConfig) {
			required(cc, "realm", "the realm")
			required(cc, "email", "the administrator's email")
			optional(cc, "role", "the admin role, when realm.yaml declares several")
		})
	registerRoles(cfg)
	registerRemote(cfg)
	registerTriggers(cfg)
	return cfg
}

// execute runs args (without the program name) and returns the exit code:
// 0, 1 for a failed command, 2 for a usage error, or what the command chose.
func execute(cfg *kli.Config, args []string, stderr io.Writer) int {
	err := cfg.Execute(append([]string{"backd"}, args...))
	if err != nil && !kli.IsReported(err) && err.Error() != "" {
		msg := err.Error()
		if !strings.HasPrefix(msg, "backd") {
			msg = "backd: " + msg
		}
		fmt.Fprintln(stderr, msg)
	}
	return kli.ExitCode(err)
}

// act wraps a command: stray arguments are a usage error, and failures are
// prefixed with the command's name.
func act(name string, fn kli.CommandFunc) kli.CommandFunc {
	return func(c *kli.CommandContext) error {
		if extra := c.Positional(); len(extra) > 0 {
			return kli.Exit(kli.ExitUsage, fmt.Errorf("backd %s: unexpected arguments: %v (values are given as flags: see `backd %s --help`)", name, extra, name))
		}
		if err := fn(c); err != nil {
			var ee *kli.ExitError
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
func usageErr(err error) error { return kli.Exit(kli.ExitUsage, err) }

func required(cc *kli.CommandConfig, key, description string) {
	cc.Define(key).String().Flag(key).Required().Description(description)
}

func optional(cc *kli.CommandConfig, key, description string) {
	cc.Define(key).String().Flag(key).Default("").Description(description)
}

func boolean(cc *kli.CommandConfig, key, description string) {
	cc.Define(key).Bool().Flag(key).Default(false).Description(description)
}

// str, flag and num read a command's flags.
func str(c *kli.CommandContext, key string) string { return kli.MustGet[string](c, key) }
func flag(c *kli.CommandContext, key string) bool  { return kli.MustGet[bool](c, key) }
func num(c *kli.CommandContext, key string) int64  { return kli.MustGet[int64](c, key) }

func ioOf(c *kli.CommandContext) userIO {
	return userIO{stdin: c.Stdin(), stdout: c.Stdout(), stderr: c.Stderr()}
}

// configDir is CONFIG_DIR, required by commands that read the config.
func configDir(c *kli.CommandContext) (string, error) {
	dir := c.Getenv("CONFIG_DIR")
	if dir == "" {
		return "", errors.New("CONFIG_DIR is required")
	}
	return dir, nil
}

// remote adds the flags every command that talks to a running backd has.
func remote(cc *kli.CommandConfig) {
	required(cc, "realm", "the realm")
	optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
}

const adminAPINote = "Uses the realm's admin API on a running backd, as the session stored by\n`backd login` or with the admin API key in BACKD_API_KEY."

// registerRemote adds the commands that manage a running backd through its
// admin API.
func registerRemote(cfg *kli.Config) {
	loginHelp := "Signs in to a realm of a running backd and keeps the session for later\ncommands, per server and realm, in ~/.config/backd/credentials (or\n$XDG_CONFIG_HOME/backd/credentials, or the file BACKD_CREDENTIALS names),\nreadable only by you. The password is read from the terminal without echo,\nor as the first line of standard input when it isn't a terminal (then\n--email is required).\n\nThe server is --url, else BACKD_URL, else the last one logged in to.\nCommands use BACKD_API_KEY (an admin API key) instead of a stored session\nwhen it is set. BACKD_CA_CERT names a PEM file of extra CAs to trust, for\nservers with a private CA."
	cfg.Command("login").ShortHelp("sign in to a realm and keep the session").LongHelp(loginHelp).
		Func(act("login", loginAction)).
		Config(func(cc *kli.CommandConfig) {
			remote(cc)
			optional(cc, "email", "the email to sign in with (asked for on a terminal)")
			boolean(cc, "magic-link", "sign in with a code sent to the email instead of a password (the realm needs magic_link)")
		})
	cfg.Command("logout").ShortHelp("end the stored session and forget it").
		Func(act("logout", logoutAction)).Config(remote)
	cfg.Command("whoami").ShortHelp("show who the stored session belongs to").
		Func(act("whoami", whoamiAction)).Config(remote)

	acc := cfg.Command("account").ShortHelp("manage your own sign-in security: the second factor and its recovery codes").
		LongHelp("Acts on the signed-in user with the session `backd login` stored (not an API key). Each\nchange asks for a fresh proof that it is you: your authenticator code if you have one,\nelse your password. Codes and passwords are read from the terminal, or from the lines of\nstandard input when it isn't one.")
	acc.SubCommand("mfa-status").ShortHelp("show whether two-step verification is on").
		Func(act("account mfa-status", accountMFAStatus)).Config(remote)
	acc.SubCommand("mfa-enable").ShortHelp("set an authenticator app up: shows a QR code and the recovery codes once").
		Func(act("account mfa-enable", accountMFAEnable)).Config(remote)
	acc.SubCommand("mfa-disable").ShortHelp("turn two-step verification off (not for roles that require it)").
		Func(act("account mfa-disable", accountMFADisable)).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		boolean(cc, "yes", "confirm that you want it off")
	})
	acc.SubCommand("recovery-codes").ShortHelp("replace the recovery codes with new ones, shown once").
		Func(act("account recovery-codes", accountRecoveryCodes)).Config(remote)

	u := cfg.Command("user").ShortHelp("manage a realm's users").
		LongHelp(adminAPINote + " The first administrator of a realm is\ncreated with `backd bootstrap`. Passwords are read from the terminal without\necho, or as the first line of standard input when it isn't a terminal. Emails\ncan't be changed.")
	user := func(name, short string, withEmail bool, run func(*userCtx) error, extra func(*kli.CommandConfig)) {
		u.SubCommand(name).ShortHelp(short).Func(act("user "+name, userAction(withEmail, run))).
			Config(func(cc *kli.CommandConfig) {
				remote(cc)
				if withEmail {
					required(cc, "email", "the user's email")
				}
				if extra != nil {
					extra(cc)
				}
			})
	}
	user("create", "create a user; asks for a password unless --no-password", true, userCreate, func(cc *kli.CommandConfig) {
		boolean(cc, "no-password", "create the user without a password")
	})
	user("list", "list the realm's users", false, userList, nil)
	user("set-password", "set or replace the password; revokes all sessions", true, userSetPassword, nil)
	user("expire-password", "expire the user's password: it stops opening the account until they reset it; sessions are kept", true, userExpirePassword, func(cc *kli.CommandConfig) {
		optional(cc, "reason", "why, for the audit trail")
	})
	user("expire-passwords", "expire every password of the realm and end every session (the first minutes of a leak)", false, userExpirePasswords, func(cc *kli.CommandConfig) {
		optional(cc, "reason", "why, for the audit trail")
		boolean(cc, "keep-sessions", "keep the sessions (a planned rotation, not a breach)")
		boolean(cc, "notify", "also email every user a link to choose a new password (needs email in realm.yaml)")
		boolean(cc, "yes", "confirm: every user will have to choose a new password")
	})
	user("notify-expired", "email every user whose password was expired a link to choose a new one; starts again after a cancel or failure", false, userNotifyExpired, nil)
	user("revoke-sessions", "end every session of the realm, leaving passwords alone", false, userRevokeSessions, func(cc *kli.CommandConfig) {
		boolean(cc, "all", "end the sessions of every user")
		boolean(cc, "yes", "confirm")
	})
	user("reset-link", "print a one-time link for the user to choose a new password, for a user the email can't reach (nothing is sent)", true, userResetLink, func(cc *kli.CommandConfig) {
		optional(cc, "redirect-to", "where the page sends the user afterwards (must be in email.allowed_redirects)")
	})
	user("change-email", "change the user's email at once (needs email in realm.yaml); revokes all sessions", true, userChangeEmail, func(cc *kli.CommandConfig) {
		required(cc, "new-email", "the new email address")
	})
	user("identities", "list the user's ways to sign in: a password and the providers they use", true, userIdentities, nil)
	user("unlink-identity", "remove one of the user's ways to sign in, never the last (password included)", true, userUnlinkIdentity, func(cc *kli.CommandConfig) {
		required(cc, "provider", "the sign-in method to remove: password, google, microsoft, apple…")
	})
	user("mfa-status", "show a user's second factor; without --email, who must set one up", false, userMFAStatus, func(cc *kli.CommandConfig) {
		optional(cc, "email", "the user to show (default: the users of required roles who have no second factor yet)")
	})
	u.SubCommand("mfa-reset").ShortHelp("remove a user's second factor and recovery codes and end their sessions").
		LongHelp(adminAPINote + " An administrator can't reset their own second factor; for the only administrator of a\nrealm who lost it, --local acts on the instance's database directly (it needs\nCONFIG_DIR and MONGO_URI where backd runs, and is audited as `instance`).").
		Func(act("user mfa-reset", func(c *kli.CommandContext) error {
			if flag(c, "local") {
				return userMFAResetLocal(c)
			}
			return userAction(true, userMFAReset)(c)
		})).
		Config(func(cc *kli.CommandConfig) {
			remote(cc)
			required(cc, "email", "the user's email")
			boolean(cc, "local", "break glass: act on the instance's database directly instead of the admin API")
			boolean(cc, "yes", "confirm the reset, which ends all of the user's sessions")
		})
	user("owned", "show what erasing the user would do, per collection that declares a policy", true, userOwned, nil)
	user("verify-email", "mark the email as verified", true, userPatch(map[string]bool{"email_verified": true}, "email of %s marked as verified\n"), nil)
	user("disable", "block sign-in and revoke all sessions", true, userPatch(map[string]bool{"disabled": true}, "%s disabled; all of their sessions were revoked\n"), nil)
	user("enable", "allow a disabled user to sign in again", true, userPatch(map[string]bool{"disabled": false}, "%s enabled\n"), nil)
	user("delete", "erase the user: a tombstone, and the collections' policies applied", true, userDelete, func(cc *kli.CommandConfig) {
		boolean(cc, "yes", "confirm the erase, which can't be undone")
		cc.Define("wait").String().Flag("wait").Default("2m").Description("how long to wait for the erase to finish (such as 30s; 0s: don't wait)")
	})
	user("add-role", "assign a role declared in realm.yaml", true, func(c *userCtx) error {
		return userRole(c, "add", str(c.cmd, "role"))
	}, func(cc *kli.CommandConfig) { required(cc, "role", "the role") })
	user("remove-role", "take a role away", true, func(c *userCtx) error {
		return userRole(c, "remove", str(c.cmd, "role"))
	}, func(cc *kli.CommandConfig) { required(cc, "role", "the role") })
	user("networks", "show or limit where the user may use the admin API and log in from", true, userNetworks, func(cc *kli.CommandConfig) {
		optional(cc, "admin", "where the admin API may be used from: comma-separated IP addresses or CIDR networks, or \"none\" to lift the limit")
		optional(cc, "login", "where the user may log in and use their session from (same format)")
	})

	pu := cfg.Command("push").ShortHelp("see a realm's push notification devices").
		LongHelp(adminAPINote + " Devices are registered by the users' apps (POST /_auth/devices); tokens are never shown.")
	pu.SubCommand("devices").ShortHelp("list a user's registered devices").
		Func(act("push devices", userAction(true, pushDevices))).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		required(cc, "email", "the user's email")
	})
	pu.SubCommand("sends").ShortHelp("list the realm's push sends, or show one with --id").
		LongHelp("A send's record holds counts, never a message, a user or a token. Needs the functions area.").
		Func(act("push sends", pushSends)).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		optional(cc, "id", "show this send (ps_…) with its progress")
		optional(cc, "kind", "list only sends of this template kind (raw for raw messages)")
		optional(cc, "function", "list only the sends of this function: <database>/<name>")
		optional(cc, "limit", "how many sends to list (1 to 200, default 50)")
	})
	pu.SubCommand("test").ShortHelp("send one user a test push (needs the users area; audited; counts toward their limits)").
		Func(act("push test", userAction(true, pushTest))).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		required(cc, "email", "the user's email")
		optional(cc, "kind", "a template kind of the realm")
		optional(cc, "data", "the data for the template, as a JSON object of strings")
		optional(cc, "title", "the title of a raw push (with --body)")
		optional(cc, "body", "the body of a raw push")
	})
	pu.SubCommand("web-keys").ShortHelp("make the realm's VAPID key pair for web push").
		LongHelp("Stores the private key as the realm secret VAPID_PRIVATE_KEY and prints the public key with the\npush.web snippet for realm.yaml. It refuses to replace an existing key without --rotate; rotating\nremoves every web device of the realm, because browsers made their subscriptions under the old\npublic key (apps subscribe again at their next launch).").
		Func(act("push web-keys", pushWebKeys)).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		optional(cc, "name", "the secret that holds the private key (default VAPID_PRIVATE_KEY)")
		boolean(cc, "rotate", "replace an existing key pair and remove the realm's web devices")
	})
	pu.SubCommand("remove-device").ShortHelp("remove one of a user's devices (audited)").
		Func(act("push remove-device", userAction(true, pushRemoveDevice))).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		required(cc, "email", "the user's email")
		required(cc, "id", "the device's id, from `backd push devices`")
	})

	og := cfg.Command("orgs").ShortHelp("manage a realm's organizations").
		LongHelp(adminAPINote + " Needs the users area. People create and manage organizations through the realm's /_orgs API; this is for administrators.")
	og.SubCommand("list").ShortHelp("list the realm's organizations").
		Func(act("orgs list", orgsList)).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		cc.Define("status").String().Flag("status").Default("").OneOf("", "active", "orphaned", "deleted").Description("only organizations with this status (orphaned: no manager left)")
	})
	og.SubCommand("show").ShortHelp("show an organization with its claims").
		Func(act("orgs show", orgsShow)).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		required(cc, "id", "the organization's id")
	})
	og.SubCommand("create").ShortHelp("create an organization owned by a user").
		Func(act("orgs create", orgsCreate)).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		required(cc, "name", "the organization's name")
		optional(cc, "slug", "a unique short name: lowercase letters, digits and '-'")
		required(cc, "owner", "the email of the user who becomes its owner")
	})
	og.SubCommand("members").ShortHelp("list an organization's members with their roles").
		Func(act("orgs members", orgsMembers)).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		required(cc, "id", "the organization's id")
	})
	og.SubCommand("add-member").ShortHelp("add a user to an organization without an invitation (audited)").
		Func(act("orgs add-member", orgsAddMember)).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		required(cc, "id", "the organization's id")
		required(cc, "email", "the user's email")
		optional(cc, "roles", "organization roles, comma-separated")
	})
	og.SubCommand("assign-owner").ShortHelp("make a user a manager of an organization (for an orphaned one)").
		Func(act("orgs assign-owner", orgsAssignOwner)).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		required(cc, "id", "the organization's id")
		required(cc, "email", "the user's email")
	})
	og.SubCommand("invite").ShortHelp("invite to an organization: by address, to a user, or with an open link").
		LongHelp("Prints the link and token once. --send also emails the link (needs email and the secrets key on the server).").
		Func(act("orgs invite", orgsInvite)).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		required(cc, "id", "the organization's id")
		optional(cc, "email", "invite whoever signs in with this verified address")
		optional(cc, "user", "invite this existing user (their email)")
		boolean(cc, "open", "an open link: anyone signed in who holds it (see --uses)")
		optional(cc, "roles", "organization roles to give, comma-separated")
		optional(cc, "expires", "how long it works, such as 2d (default 7d, at most 30d)")
		cc.Define("uses").Int64().Flag("uses").Default(0).Min(0).Max(1000).Description("how many times an open link may be used (default 1)")
		boolean(cc, "send", "also email it (with --email)")
	})
	og.SubCommand("invitations").ShortHelp("list an organization's invitations (never tokens)").
		Func(act("orgs invitations", orgsInvitations)).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		required(cc, "id", "the organization's id")
	})
	og.SubCommand("revoke-invitation").ShortHelp("revoke an invitation").
		Func(act("orgs revoke-invitation", orgsRevokeInvitation)).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		required(cc, "id", "the organization's id")
		required(cc, "invitation", "the invitation's id")
	})
	og.SubCommand("owned").ShortHelp("report what deleting an organization would do").
		Func(act("orgs owned", orgsOwned)).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		required(cc, "id", "the organization's id")
	})
	og.SubCommand("delete").ShortHelp("delete an organization: access ends at once, its documents follow").
		LongHelp("Ends everyone's access at once (the record stays, as deleted), then a job applies each collection's on_org_delete: `delete` removes the documents and queues their files for deletion, `keep` leaves them for realm administrators. See `backd orgs owned` first. Needs --yes. " + adminAPINote).
		Func(act("orgs delete", orgsDelete)).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		required(cc, "id", "the organization's id")
		boolean(cc, "yes", "confirm")
	})
	og.SubCommand("reactivate").ShortHelp("bring a deleted organization back, with no members").
		Func(act("orgs reactivate", orgsReactivate)).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		required(cc, "id", "the organization's id")
		optional(cc, "slug", "the slug it takes (its former one may be in use)")
	})
	og.SubCommand("claims-set").ShortHelp("replace an organization's claims (at most 1 KiB of JSON)").
		Func(act("orgs claims-set", orgsClaimsSet)).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		required(cc, "id", "the organization's id")
		required(cc, "claims", "a JSON object, for example '{\"plan\": \"pro\"}'")
	})

	k := cfg.Command("apikey").ShortHelp("manage a realm's API keys").
		LongHelp(adminAPINote + "\n\nAPI keys give server-side services full access to their realm's data.\nStore them as secrets.")
	k.SubCommand("create").ShortHelp("create an API key and print it once").
		LongHelp("--role data (default) reaches data routes; admin also reaches the admin API.\n--expires takes days (\"90d\") or Go durations (\"12h\"); default: never (not\nrecommended: startup warns about such keys). --networks limits where the key\nmay be used from. --scopes limits what a data key reaches, so a leaked key exposes\nonly that: comma-separated grants read, write or call, each optionally followed by\n:<database> or :<database>/<collection or function>, for example\n--scopes read:blog/posts,call:main/contact. Without scopes the key reaches everything.").
		Func(act("apikey create", apikeyCreate)).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		required(cc, "name", "the key's name")
		cc.Define("role").String().Flag("role").Default("data").OneOf("data", "admin").Description("data (reaches data routes) or admin (also reaches the admin API)")
		optional(cc, "expires", "how long the key is valid, such as 90d or 12h")
		optional(cc, "networks", "where the key may be used from: comma-separated IP addresses or CIDR networks")
		optional(cc, "scopes", "what a data key reaches: comma-separated grants such as read:blog/posts,write:blog,call:main/export (default: everything)")
	})
	k.SubCommand("list").ShortHelp("list the realm's API keys (never the keys themselves)").
		Func(act("apikey list", apikeyList)).Config(remote)
	k.SubCommand("revoke").ShortHelp("delete the key; it stops working at once").
		Func(act("apikey revoke", apikeyRevoke)).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		required(cc, "name", "the key's name")
	})

	sec := cfg.Command("secret").ShortHelp("manage functions' secrets").
		LongHelp("set, list and delete use the realm's admin API on a running backd, as the\nsession stored by `backd login` or with the admin API key in BACKD_API_KEY.")
	sec.SubCommand("set").ShortHelp("create or replace a secret's value").
		LongHelp("The value is read from the terminal without echo, or the first line of\nstandard input when it isn't a terminal. Without --database it's the realm\nscope (realm.NAME in function.yaml); with it, that database's scope (NAME, for\nfunctions of that database only, never another's).").
		Func(act("secret set", secretSet)).Config(secretFlags)
	sec.SubCommand("create").ShortHelp("create a field key: generated and shown once, or imported").
		LongHelp("Creates a key for field encryption (design: a `key` secret). With --generate the server makes 32\nrandom bytes and prints them, base64url, on standard output ONCE, with the fingerprint on standard error:\nsave the key outside backd, because nothing ever shows it again and a lost key means the values encrypted\nwith it are lost. With --value it imports a key you already hold (key:<base64url>, env:VAR or file:PATH),\nfor a restore or a staging copy. Needs a full administrator and a fresh proof that it is you. Audited with the\nname and fingerprint, never the key. " + adminAPINote).
		Func(act("secret create", secretCreate)).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		required(cc, "name", "the key's name, such as HEALTH")
		required(cc, "type", "the kind of secret to create: key")
		boolean(cc, "generate", "make a new random key and show it once")
		optional(cc, "value", "import this key: key:<base64url>, env:VAR or file:PATH")
		boolean(cc, "json", "print the answer as JSON")
	})
	sec.SubCommand("list").ShortHelp("list the realm's secrets (never their values)").
		Func(act("secret list", secretList)).Config(remote)
	sec.SubCommand("delete").ShortHelp("delete a secret; a function that declares it answers secret_missing again").
		Func(act("secret delete", secretDelete)).Config(secretFlags)
	sec.SubCommand("rotate-key").ShortHelp("re-encrypt every realm's secrets under a new master key").
		LongHelp("Re-encrypts every realm's secrets from BACKD_SECRETS_KEY (the currently\nconfigured, old key) to BACKD_SECRETS_NEW_KEY (the new one; BACKD_SECRETS_NEW_KEY_FILE\nalso works). Unlike the commands above, this writes to MongoDB directly, so it\nneeds the server's CONFIG_DIR and MONGO_URI: run it where backd runs, for example\n  docker compose exec backd /backd secret rotate-key\nRestart every backd instance with BACKD_SECRETS_KEY set to the new key afterward.").
		Func(act("secret rotate-key", secretRotateKey)).Config(func(*kli.CommandConfig) {})

	cfg.Command("audit").ShortHelp("print the realm's audit trail, newest first").
		LongHelp("Prints who created, changed or deleted users, roles, network restrictions,\nAPI keys and invitations, administrators' logins, and admin requests refused\nby network. " + adminAPINote).
		Func(act("audit", auditAction)).Config(func(cc *kli.CommandConfig) {
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

	st := cfg.Command("storage").ShortHelp("check, inspect and clean up a realm's object storage")
	st.SubCommand("check").ShortHelp("verify the realm's storage the way files would use it").
		LongHelp("Asks the running backd, which holds the realm's access keys, to check its storage:\nthe bucket and credentials, put, head, get, a range request and a listing on the realm's\nprefix, the signed SHA-256 (a wrong one must be rejected), signed download and upload\nlinks, and, where the provider exposes them, the bucket's CORS rules and default\nencryption. It stores a few small objects under <prefix>/<realm>/_check/ and deletes them\nagain. Exits 1 when a step failed (warnings don't fail it). Audited as storage.check. " + adminAPINote).
		Func(act("storage check", storageCheck)).Config(func(cc *kli.CommandConfig) {
		required(cc, "realm", "the realm whose storage to check")
		optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		boolean(cc, "json", "print the report as JSON")
	})
	st.SubCommand("usage").ShortHelp("show what the realm's storage holds and whether it works").
		LongHelp("Asks the running backd for the realm's storage: how it is configured (never the keys), whether the keys\nare set and the bucket answers, the running totals of bytes and files the documents reference for the realm\nand for the users holding the most, and what waits to be cleaned up (objects queued for deletion, uploads left\nunfinished). " + adminAPINote).
		Func(act("storage usage", storageUsage)).Config(func(cc *kli.CommandConfig) {
		required(cc, "realm", "the realm whose storage to show")
		optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		boolean(cc, "json", "print it as JSON")
	})
	st.SubCommand("reconcile").ShortHelp("find (and with --delete remove) objects no document references").
		LongHelp("Lists the realm's file objects (the only time backd lists the bucket) and reports the ones no declared file\nfield of any document references. Use it after restoring a database backup or removing a file field. It always\nskips objects younger than 24 hours, objects whose upload record is still open (uploads in flight, pending\nuploads not yet expired, files a document holds) and anything that isn't <prefix>/<realm>/<database>/<collection>/<file id>.\nWithout --delete it only reports; with it, it removes exactly the objects the report lists. Needs the config\narea with write access. Audited as storage.reconcile. " + adminAPINote).
		Func(act("storage reconcile", storageReconcile)).Config(func(cc *kli.CommandConfig) {
		required(cc, "realm", "the realm whose storage to reconcile")
		optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		boolean(cc, "delete", "remove the unreferenced objects instead of only reporting them")
		boolean(cc, "json", "print the report as JSON")
	})
	cfg.Command("export").ShortHelp("write a realm, database or collection as a package").
		LongHelp("Reads the realm's documents and files from a running backd into a package: a directory with manifest.json,\n<database>/<collection>.jsonl (the API representation of every document, soft-deleted ones included, with dates and\nbinary values in Extended JSON) and <database>/_files/<file id>, or one .tar.gz with --archive. The package is lossless;\nbackd import reads it. Each part is verified as it arrives and recorded in <out>/.export-progress.json, so running the\nsame command again after an interruption continues from the last complete part. Only one export of a realm runs at a time.\n--fields and --where narrow it to a reporting extract (a --fields package is partial and can't be imported) and --format csv writes one flat, lossy table per collection for spreadsheets, without files. --include auth adds the realm's authentication data, audited as export.auth. --anonymize makes a copy for staging (see its flags); the manifest says so, and import warns about a package that doesn't fit the instance's BACKD_ENV. Never exported: sessions, tokens, counters, jobs, history, secrets and the audit trail. Needs full admin access and a fresh\nproof that it is you. Audited as export and export.done. " + adminAPINote).
		Func(act("export", exportAction)).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		required(cc, "out", "the package directory to write (with --archive, the archive is <out>.tar.gz)")
		optional(cc, "db", "only this database")
		optional(cc, "collection", "only these collections of --db: comma-separated names")
		optional(cc, "fields", "only these fields: comma-separated paths such as name,address.city (id is always there); the package is marked partial and can't be imported")
		optional(cc, "where", "only the documents matching this filter (the API's where syntax), for one --collection")
		optional(cc, "format", "jsonl (a package, the default) or csv (a flat table per collection for spreadsheets: lossy, no files, can't be imported)")
		optional(cc, "include", "auth: also export the realm's users with their password hashes, organizations, memberships, teams, second factors and push devices (whole realm only); a one-time export key is shown once")
		boolean(cc, "anonymize", "make a staging copy: each collection's on_owner_delete remove/replace rules are applied to every document, files are left out, and with --include auth emails become user-<id>@example.invalid and password hashes, second factors and devices are dropped")
		optional(cc, "anonymize-config", "with --anonymize: a YAML file adding or overriding rules per collection (collections: {<database>/<collection>: {remove: [...], replace: {...}}}), checked against the schemas")
		optional(cc, "staging-password", "with --anonymize --include auth: one password every user gets (env:<VAR>, file:<path> or key:<value>)")
		optional(cc, "anonymize-files", "with --anonymize: keep, to export the files too (they often hold personal data)")
		optional(cc, "auth-key-out", "write the export key of --include auth to this file (readable only by you) instead of showing it")
		directFlags(cc)
		boolean(cc, "archive", "write one <out>.tar.gz instead of a directory")
		boolean(cc, "no-files", "leave the files out: only the documents")
	})

	cfg.Command("import").ShortHelp("read an export package into a realm").
		LongHelp("Reads a package made by backd export (--from: its directory or .tar.gz) into the realm. First every checksum of the package\nis verified, then the running backd validates every document against the target's own configuration (ids, schemas,\nfile limits, unique values, the documents already there) and nothing is written until all of it is known; --dry-run stops\nthere and prints what would happen. Then the files are uploaded and the documents written with their ids and _meta kept, so\nreferences and file links stay valid. --mode create (the default) refuses a different document with the same id, skip leaves\nexisting documents alone and replace overwrites them. Invalid documents stop the import unless --skip-invalid, which writes\nthem to --report (default import-report.json). Running the same command again after an interruption finishes the job: what\nwas written is found identical and left alone. Partial packages and CSV extracts are refused. Needs full admin access and a\nfresh proof that it is you. Audited as import and import.done. " + adminAPINote).
		Func(act("import", importAction)).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		required(cc, "from", "the package: its directory, or the .tar.gz that export --archive wrote")
		cc.Define("mode").String().Flag("mode").Default("create").Description("create (fail on an existing id), skip (keep existing documents) or replace (overwrite them)")
		boolean(cc, "dry-run", "validate everything and write nothing")
		boolean(cc, "skip-invalid", "leave out the documents that are invalid for the target instead of stopping")
		optional(cc, "report", "write the problems found to this JSON file")
		optional(cc, "auth-key", "the export key of a package exported with --include auth: key:<value>, env:<VAR> or file:<path>; without it users arrive without second factors and push devices")
		directFlags(cc)
	})

	cons := cfg.Command("consents").ShortHelp("manage the consents users accept")
	cons.SubCommand("notify").ShortHelp("email the users who hold an older version of a consent").
		LongHelp("Queues the `consent-updated` email for every user who accepted an older version of the consent than the\ncurrent one (and, for a required consent, who accepted none). Publishing a version never sends anything by itself:\nthis is the only way, and the email limits apply, so a large realm may need to run it again later. Needs the\nusers area with write access. Audited as consents.notify. " + adminAPINote).
		Func(act("consents notify", consentsNotify)).Config(func(cc *kli.CommandConfig) {
		required(cc, "realm", "the realm whose users to notify")
		required(cc, "consent", "the consent whose current version to ask them to review")
		optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		boolean(cc, "json", "print the result as JSON")
	})
	cons.SubCommand("record").ShortHelp("record a consent given or withdrawn outside the app").
		LongHelp("Records, for one user, a consent they gave or withdrew on paper, by phone or in a way the app can't see.\nThe record has the method `admin` and the evidence, which is required: a ticket, a form number, anything that\nproves it. Use --action accepted with --version, or --action withdrawn. Needs the users area with write access.\nAudited as user.consent_recorded. " + adminAPINote).
		Func(act("consents record", consentsRecord)).Config(func(cc *kli.CommandConfig) {
		required(cc, "realm", "the realm of the user")
		required(cc, "email", "the user's email address")
		required(cc, "consent", "the consent's name")
		required(cc, "action", "accepted or withdrawn")
		required(cc, "evidence", "the reference that proves it (a ticket, a form number)")
		optional(cc, "version", "the version accepted (for accepted)")
		optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
	})
	comp := cfg.Command("compliance").ShortHelp("privacy: what a user's data export covers")
	comp.SubCommand("scope").ShortHelp("show what a user's data export takes from each collection").
		LongHelp("Asks the running backd for the realm's personal data scope: per collection whether the documents a user owns\n(and those where they appear in an erase policy's `pull` field, and the files they hold) are exported, which fields\nwhen only some are, and the reason for every exclusion (`personal_data` in collection.yaml), then the system data\nan export adds and what it never holds. Needs the config area (read is enough). " + adminAPINote).
		Func(act("compliance scope", complianceScopeCmd)).Config(func(cc *kli.CommandConfig) {
		required(cc, "realm", "the realm whose scope to show")
		optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		boolean(cc, "json", "print it as JSON")
	})
	cfg.Command("openapi").ShortHelp("generate a realm's OpenAPI document").
		LongHelp("Generates the OpenAPI 3.1 document of a realm's client API from CONFIG_DIR: the collections clients can reach\nwith their schemas and the features they enable, the sign-in methods the realm enables, its non-internal functions\nwith their input and output schemas, and its app configuration. It is what generators of typed clients for other\nplatforms (Dart, Kotlin, Swift) read; a running backd serves the same document at /v1/<realm>/_openapi.json.\n\nPrints it, or writes --out. --check writes nothing and exits non-zero when --out is missing or stale, for CI.\n\n--admin generates the admin API's document instead (served at /v1/<realm>/_admin/openapi.json to administrators):\nusers, API keys, secrets, audit, jobs, configuration, the data routes past the collections' rules typed per\ncollection, and every function an administrator may run by hand, internal ones included, with their input and\noutput. For internal tooling.").
		Func(act("openapi", openapiCommand)).Config(func(cc *kli.CommandConfig) {
		required(cc, "realm", "the realm")
		optional(cc, "out", "write the document to this file instead of standard output")
		boolean(cc, "check", "write nothing; fail when --out is missing or differs from what the config produces")
		boolean(cc, "admin", "the admin API's document instead: administration routes, data routes past the rules, and internal functions")
	})
	cfg.Command("types").ShortHelp("generate a realm's TypeScript or Dart types").
		LongHelp("Generates TypeScript types from the realm's config: documents, where and order_by per collection, functions,\napp configuration, email and push templates and roles. They come from the same config the server enforces; nothing\nis checked at runtime from them. Reads CONFIG_DIR, or a running backd with --from (an admin of the realm, the config\narea; " + adminAPINote + ") and writes types.d.ts (types.dart with --lang dart) and realm.json into --out.\n\n--check writes nothing and exits non-zero when the files in --out are missing or stale, for CI. --diff writes\nnothing and prints what changed at the type level since they were generated (a field added, a type changed, a\ncollection removed, a function's input changed), for pull request reviews. Things TypeScript can't express are\ntyped unknown and listed as warnings on standard error.\n\n--functions writes each functions project's backd.d.ts instead (in <realm>/<database>/_functions/, from CONFIG_DIR only):\nthe registry defineFunction(\"<database>/<name>\", handler) takes its types from. In dev mode backd rewrites it after\nevery config reload.").
		Func(act("types", typesCommand)).Config(func(cc *kli.CommandConfig) {
		required(cc, "realm", "the realm")
		optional(cc, "db", "only this database of the realm")
		cc.Define("lang").String().Flag("lang").Default("ts").Description("the language to emit: ts (types.d.ts) or dart (types.dart)")
		cc.Define("out").String().Flag("out").Default("backd").Description("the directory for the types file and realm.json")
		optional(cc, "from", "read the config from this running backd instead of CONFIG_DIR")
		boolean(cc, "check", "write nothing; fail when the generated files are missing or stale")
		boolean(cc, "diff", "write nothing; print the type-level changes since the files were generated")
		boolean(cc, "functions", "write each functions project's backd.d.ts (for defineFunction) instead of --out; with --check, only check")
	})
	appcfg := cfg.Command("app-config").ShortHelp("the realm's app configuration: values, history, rollback")
	appcfg.SubCommand("get").ShortHelp("show the configuration with every value").
		LongHelp("Asks the running backd for the realm's app configuration: every key with its effective value (the keys clients\ncan't read too), marking the ones that differ from their defaults, and the stored values that stopped validating\nafter a deploy. --key limits it to one key or group. Needs the config area (read is enough). " + adminAPINote).
		Func(act("app-config get", appConfigGet)).Config(func(cc *kli.CommandConfig) {
		required(cc, "realm", "the realm")
		optional(cc, "key", "only this key or group, as a dot path")
		optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		boolean(cc, "json", "print it as JSON")
	})
	appcfg.SubCommand("set").ShortHelp("change values").
		LongHelp("Sets one value with --key and --value (JSON when it parses: true, 10, [\"a\"]; text otherwise), or several with\n--values '{\"a.b\": 1, \"c.d\": true}'. The result is validated as a whole against app-config/schema.json and applied\nor refused. The change is based on the version read just before (or --if-match). Needs the config area with write\naccess. Audited as appconfig.change. " + adminAPINote).
		Func(act("app-config set", appConfigSet)).Config(func(cc *kli.CommandConfig) {
		required(cc, "realm", "the realm")
		optional(cc, "key", "the key to set, as a dot path")
		optional(cc, "value", "its value")
		optional(cc, "values", "several values, as a JSON object of key path to value")
		optional(cc, "if-match", "the version the change is based on (default: the current one)")
		optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		boolean(cc, "json", "print the result as JSON")
	})
	appcfg.SubCommand("reset").ShortHelp("return keys to their defaults").
		LongHelp("Returns keys, or whole groups of keys, to the defaults declared in schema.json. Needs the config area with write\naccess. Audited as appconfig.change. " + adminAPINote).
		Func(act("app-config reset", appConfigReset)).Config(func(cc *kli.CommandConfig) {
		required(cc, "realm", "the realm")
		required(cc, "keys", "the keys or groups to reset, comma separated")
		optional(cc, "if-match", "the version the change is based on (default: the current one)")
		optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		boolean(cc, "json", "print the result as JSON")
	})
	appcfg.SubCommand("history").ShortHelp("show who changed what").
		LongHelp("The versions of the configuration, newest first, with the actor and every path's old and new value. The last 100\nversions or 90 days are kept, whichever is longer. Needs the config area (read is enough). " + adminAPINote).
		Func(act("app-config history", appConfigHistory)).Config(func(cc *kli.CommandConfig) {
		required(cc, "realm", "the realm")
		optional(cc, "limit", "how many versions (default and most: 100)")
		optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		boolean(cc, "json", "print it as JSON")
	})
	appcfg.SubCommand("rollback").ShortHelp("restore an earlier version").
		LongHelp("Restores the values a version had (c0 is the defaults) as a new version, so the rollback is in the history too.\nValues that no longer fit the schema are refused. Needs the config area with write access. Audited as\nappconfig.change. " + adminAPINote).
		Func(act("app-config rollback", appConfigRollback)).Config(func(cc *kli.CommandConfig) {
		required(cc, "realm", "the realm")
		required(cc, "version", "the version to restore, such as c2")
		optional(cc, "if-match", "the version the change is based on (default: the current one)")
		optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		boolean(cc, "json", "print the result as JSON")
	})
	files := cfg.Command("files").ShortHelp("work on the image files documents hold")
	files.SubCommand("regenerate").ShortHelp("make the image versions of a file field again when they no longer match its declaration").
		LongHelp("Starts a job that goes through every document of a file field and makes again, on a worker, the image\nversions whose copies no longer match the field's `versions:` in collection.yaml: copies made with other\nparameters (after you changed the declaration), copies that failed or never finished, and versions added later\nthat a file has no copy of. Copies a function generated with its own parameters, versions only functions\nmake, and files that are not pictures are left alone. --missing-only makes only the copies that do not exist.\nThe job runs in the background (one per field at a time), makes at most --rate files a second, keeps serving\nthe old copy of a file until its new one is ready, and can be cancelled and resumed: running it again skips\nwhat matches already. This command follows its progress on stderr; --no-wait only starts it. Needs the config\narea with write access. Audited as files.versions.regenerate. " + adminAPINote).
		Func(act("files regenerate", filesRegenerate)).Config(func(cc *kli.CommandConfig) {
		required(cc, "realm", "the realm")
		required(cc, "database", "the file field's database")
		required(cc, "collection", "the file field's collection")
		required(cc, "field", "the file field")
		optional(cc, "version", "only this version (default: every version the field declares with parameters)")
		optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		boolean(cc, "missing-only", "make only the copies that do not exist, not the ones made with other parameters")
		cc.Define("rate").Int64().Flag("rate").Default(0).Min(0).Max(1000).Description("at most this many files a second (default 10)")
		boolean(cc, "no-wait", "start the job and print its id, without following it")
		cc.Define("interval").String().Flag("interval").Default("2s").Description("how often to ask the server for progress")
	})
	data := cfg.Command("data").ShortHelp("check the stored documents against their schemas")
	checkFlags := func(cc *kli.CommandConfig) {
		required(cc, "realm", "the realm")
		optional(cc, "database", "only this database (default: every database of the realm)")
		optional(cc, "collection", "only this collection (needs --database)")
		optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		boolean(cc, "json", "one JSON object per line")
	}
	data.SubCommand("check").ShortHelp("report the documents that no longer match their collection's schema").
		LongHelp("Starts a schema check and follows it: backd reads every stored document of the\ncollections (soft-deleted ones too), validates it against the collection's current\nschema as the API validates a write, and lists the invalid ones with the path and the\nrule that failed, never the values. It never writes. **It reads every document, so on a\nbig collection it takes a long time and loads MongoDB**: it runs in the background on a\nworker (one check per realm at a time) and this command follows its progress, in\nsteps of 5 percent, on stderr. Exits non-zero when drift is found or a collection was not\nchecked to the end. --no-wait only starts it and prints the job id; the latest reports\nstay readable with `backd data checks`. Audited as data.check. " + adminAPINote).
		Func(act("data check", dataCheck)).Config(func(cc *kli.CommandConfig) {
		checkFlags(cc)
		cc.Define("limit").Int64().Flag("limit").Default(0).Min(0).Max(1000).Description("list at most this many invalid documents per collection (default 100, at most 1000)")
		boolean(cc, "no-wait", "start the check and print its job id, without following it")
		cc.Define("interval").String().Flag("interval").Default("2s").Description("how often to ask the server for progress")
	})
	data.SubCommand("checks").ShortHelp("print the latest schema check reports").
		LongHelp("Lists every collection of the realm with its latest schema check (documents read,\ninvalid, whether it was complete, when it finished), and notes a check running now. With\n--database and --collection prints that collection's report in full. " + adminAPINote).
		Func(act("data checks", dataChecks)).Config(checkFlags)
	docFlags := func(cc *kli.CommandConfig) {
		required(cc, "realm", "the realm")
		required(cc, "database", "the database")
		required(cc, "collection", "the collection")
		optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
	}
	data.SubCommand("transfer").ShortHelp("move one document to another organization or team (audited)").
		LongHelp("Moves a document to another organization, or out of its organization with --org none when the collection's documents may have none. --team names a team of the organization the document ends up in (none takes it out of its team): moving to another organization leaves the team. It is a deliberate act of a realm administrator, not an update: the version goes up and the audit trail names both organizations. Needs the data area with write access. " + adminAPINote).
		Func(act("data transfer", dataTransfer)).Config(func(cc *kli.CommandConfig) {
		docFlags(cc)
		required(cc, "id", "the document's id")
		required(cc, "org", "the organization's id, or none")
		optional(cc, "team", "the team's id in that organization, or none")
	})
	data.SubCommand("assign-org").ShortHelp("give the documents that have no organization one").
		LongHelp("For a collection that adopted organizations after it had documents: they have none, and members can't see them. Queues a job that gives the organization to the documents that have none and match --where (a JSON filter, as in a list query), and never moves one that has an organization. --dry-run only counts them. Needs the data area with write access. " + adminAPINote).
		Func(act("data assign-org", dataAssignOrg)).Config(func(cc *kli.CommandConfig) {
		docFlags(cc)
		required(cc, "org", "the organization's id")
		optional(cc, "where", "only documents matching this JSON filter")
		boolean(cc, "dry-run", "count the documents without changing anything")
	})
	fn := cfg.Command("functions").ShortHelp("build, call and inspect server-side functions")
	fn.SubCommand("build").ShortHelp("bundle every function in CONFIG_DIR with deno").
		LongHelp("Bundles every function in CONFIG_DIR with `deno bundle` into\n<realm>/<database>/_functions/.build/, one file per function named by its\ncontent hash, and records them in .build/manifest.json. Needs only CONFIG_DIR\nand Deno " + registry.DenoVersion + " (DENO, or deno on the PATH). --check also type-checks\nTypeScript. backd refuses to start while any bundle is missing or stale, so\nbuild after every change to a functions project, before provisioning or\ndeploying.").
		Func(act("functions build", functionsBuild)).Config(func(cc *kli.CommandConfig) {
		boolean(cc, "check", "also type-check TypeScript")
	})
	fn.SubCommand("types").ShortHelp("print JSDoc types generated from the functions' schemas").
		LongHelp("Prints JSDoc types generated from every function's input and output JSON\nSchemas, for editor autocomplete in ctx.input and the return value. Needs only\nCONFIG_DIR.").
		Func(act("functions types", functionsTypes)).Config(func(*kli.CommandConfig) {})
	fnFlag := func(cc *kli.CommandConfig) {
		required(cc, "function", "the function, as <realm>/<database>/<name>")
		optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
	}
	fn.SubCommand("invoke").ShortHelp("call the function through a running backd").
		LongHelp("Calls the function through a running backd's real HTTP path: the same invoke\nrule, schemas and secrets apply as for any other caller. An internal function has\nno such route: it is run through the admin API instead (as --as, if given). --input reads the\nrequest body from a JSON file (\"-\" for standard input); without it, the body\nis null. --as <email> calls on the user's behalf (needs an admin API key,\nBACKD_API_KEY); without it, calls as whatever credential is active. When the\nserver runs with BACKD_DEV=true, also prints the function's console logs and\nits time inside the executor.").
		Func(act("functions invoke", functionsInvoke)).Config(func(cc *kli.CommandConfig) {
		fnFlag(cc)
		optional(cc, "input", "a JSON file with the request body, or - for standard input")
		optional(cc, "as", "call on behalf of this user's email (needs an admin API key)")
	})
	fn.SubCommand("history").ShortHelp("print the function's recent calls, newest first").
		LongHelp("Prints time, status, error code, duration, actor and request id of the\nfunction's recent calls (never the input or output). Records expire after the\nrealm's functions.log_retention (default 7d): a short debugging window, not a\nlog archive. " + adminAPINote).
		Func(act("functions history", functionsHistory)).Config(func(cc *kli.CommandConfig) {
		fnFlag(cc)
		optional(cc, "since", "from this time: RFC 3339, or a duration back from now such as 24h or 7d")
		limit(cc)
		boolean(cc, "json", "one JSON object per line")
	})
	fn.SubCommand("jobs").ShortHelp("list async and scheduled jobs, newest first").
		LongHelp("Lists the realm's async jobs and cron runs with their state and how they ended\n(never their input or output): for one function with --function, or every\nfunction of a realm with --realm. --status narrows to queued, running or done,\n--scheduled to cron runs; PROGRESS is the step a function reported with ctx.step\n(--job <id> with --realm shows every step of one job). A cron run's id is cron_<database>_<function>_<yyyymmddhhmm>\n(UTC). " + adminAPINote).
		Func(act("functions jobs", functionsJobs)).Config(func(cc *kli.CommandConfig) {
		optional(cc, "function", "the function, as <realm>/<database>/<name>")
		optional(cc, "realm", "list every function's jobs in this realm instead")
		optional(cc, "job", "show one job (with --realm) with every step it reported, instead of listing")
		optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		cc.Define("status").String().Flag("status").Default("").OneOf("", "queued", "running", "done").Description("only jobs in this state")
		boolean(cc, "scheduled", "only cron runs")
		optional(cc, "since", "from this time: RFC 3339, or a duration back from now such as 24h or 7d")
		limit(cc)
		boolean(cc, "json", "one JSON object per line")
	})
	scheduleFlags := func(cc *kli.CommandConfig) {
		fnFlag(cc)
		boolean(cc, "json", "print the schedule as JSON")
	}
	jobFlags := func(cc *kli.CommandConfig) {
		required(cc, "realm", "the realm the job belongs to")
		required(cc, "job", "the job's id (see `backd functions jobs`)")
		optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		boolean(cc, "json", "print the job as JSON")
	}
	fn.SubCommand("cancel").ShortHelp("cancel a queued or running job").
		LongHelp("Ends a queued, retry-waiting or running function job at once: it is done with the\nresult \"cancelled\". A worker that is running it stops the run within a few seconds\n(what the function already did stays done). The job keeps its input, so it can be\nre-run. Refused when it has already finished. Audited as job.cancel. " + adminAPINote).
		Func(act("functions cancel", functionsCancel)).Config(jobFlags)
	fn.SubCommand("rerun").ShortHelp("queue a finished job again").
		LongHelp("Queues a new job for a finished function job (any result, a cancelled one included):\nthe same function, input and caller, as a job of its own (the original keeps its\nresult). Prints the new job's id; follow it with `backd functions jobs`. A worker must\nbe running for it to run. Refused while the job is queued or running. Audited as\njob.rerun. " + adminAPINote).
		Func(act("functions rerun", functionsRerun)).Config(jobFlags)
	fn.SubCommand("schedules").ShortHelp("list the realm's schedules and whether each is paused").
		LongHelp("Lists every scheduled function of a realm: its cron expression, time zone,\noverlap setting and whether an administrator paused it (see `backd functions pause`).\n" + adminAPINote).
		Func(act("functions schedules", functionsSchedules)).Config(func(cc *kli.CommandConfig) {
		required(cc, "realm", "the realm to list")
		optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		boolean(cc, "json", "one JSON object per line")
	})
	fn.SubCommand("pause").ShortHelp("stop a function's schedule creating runs").
		LongHelp("Pauses the function's cron schedule: no run is created until it is resumed. Runs\nalready queued or running are not touched, and the function can still be called by\nhand. The state is stored in the realm's database, so it survives restarts and\napplies to every instance. Pausing a paused schedule changes nothing. Audited as\nschedule.pause. " + adminAPINote).
		Func(act("functions pause", functionsPause)).Config(scheduleFlags)
	fn.SubCommand("resume").ShortHelp("let a paused schedule create runs again").
		LongHelp("Resumes a paused cron schedule. The runs that fell due while it was paused are not\nmade up: the next scheduled time runs. Audited as schedule.resume. " + adminAPINote).
		Func(act("functions resume", functionsResume)).Config(scheduleFlags)
	fn.SubCommand("logs").ShortHelp("print the function's own console output for its recent calls").
		LongHelp("Prints the function's own console output for its recent calls (already masked of\nany declared secrets), oldest first within each call. " + adminAPINote).
		Func(act("functions logs", functionsLogs)).Config(func(cc *kli.CommandConfig) {
		fnFlag(cc)
		optional(cc, "request-id", "only this call")
		optional(cc, "since", "from this time: RFC 3339, or a duration back from now such as 24h or 7d")
		limit(cc)
		boolean(cc, "json", "one JSON object per line")
	})
}

func secretFlags(cc *kli.CommandConfig) {
	remote(cc)
	required(cc, "name", "the secret's name")
	optional(cc, "database", "the database scope; without it, the realm scope")
}

func limit(cc *kli.CommandConfig) {
	cc.Define("limit").Int64().Flag("limit").Default(50).Min(1).Description("at most this many records")
}

// userAction runs a user subcommand against the realm's admin API.
func userAction(withEmail bool, run func(*userCtx) error) kli.CommandFunc {
	return func(c *kli.CommandContext) error {
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

// registerRoles adds the commands that run backd's server roles. Plain `backd` starts
// none of them: it prints the help (see isGlobalHelp).
func registerRoles(cfg *kli.Config) {
	serveHelp := "Loads CONFIG_DIR, provisions or verifies MongoDB (PROVISION_MODE), and serves\nHTTP. --with-worker also runs the worker role (async jobs) in this process; it\nneeds BACKD_EXECUTOR_URL."
	withWorker := func(cc *kli.CommandConfig) {
		boolean(cc, "with-worker", "also run the worker role (async jobs) in this process")
	}
	cfg.Command("serve").ShortHelp("load config, provision or verify MongoDB, serve HTTP").LongHelp(serveHelp).
		Func(role("serve", serve)).Config(withWorker)
	cfg.Command("worker").ShortHelp("run the worker role alone: claims and runs async jobs").
		LongHelp("Claims and runs async jobs (mode: async) and scheduled functions. Needs\nBACKD_EXECUTOR_URL; WORKER_CONCURRENCY caps jobs running at once (default 10).").
		Func(role("worker", worker)).Config(func(*kli.CommandConfig) {})
	cfg.Command("provision").ShortHelp("load config, apply provisioning once and exit").
		LongHelp("Creates missing collections, validators and indexes, and enables pre-change images on\nrealtime collections. Nothing is ever removed, except with --disable-preimages --yes:\nthat switches the images off on collections that no longer need them, and deletes the\nimages stored so far for good.").
		Func(role("provision", provision)).Config(func(cc *kli.CommandConfig) {
		boolean(cc, "disable-preimages", "switch off pre-change images on collections that no longer need them (irreversible)")
		boolean(cc, "yes", "confirm --disable-preimages")
	})
	cfg.Command("executor").ShortHelp("run server-side functions for backd").LongHelp(executorHelp).
		Func(act("executor", func(c *kli.CommandContext) error { return serveExecutor(c.Getenv, c.Stderr()) })).
		Config(func(*kli.CommandConfig) {})
	cfg.Command("egress").ShortHelp("run the egress role, the executor's only way out").LongHelp(egressHelp).
		Func(act("egress", func(c *kli.CommandContext) error { return serveEgress(c.Getenv, c.Stderr()) })).
		Config(func(*kli.CommandConfig) {})
}

// registerTriggers adds `backd triggers`: what the dispatcher is doing and has not been able to
// do (status, gaps), and a way to re-emit the changes of a collection after a gap (rescan).
func registerTriggers(cfg *kli.Config) {
	tr := cfg.Command("triggers").ShortHelp("see and repair what the trigger dispatcher did")
	remote := func(cc *kli.CommandConfig) {
		required(cc, "realm", "the realm")
		optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		boolean(cc, "json", "one JSON object per line")
	}
	tr.SubCommand("status").ShortHelp("the dispatcher, the realm's triggers, the backlog and the open gaps").
		LongHelp("Shows whether an instance holds the dispatcher's lease and how old its stored position is,\nthe realm's triggers (each runs with admin data access), the unfinished trigger jobs and the\ngaps not yet resolved. " + adminAPINote).
		Func(act("triggers status", triggersStatus)).Config(remote)
	tr.SubCommand("gaps").ShortHelp("list what the dispatcher could not turn into jobs").
		LongHelp("Lists the gaps the dispatcher recorded: a stretch of changes that fell out of MongoDB's\nhistory while no dispatcher read them (window), changes whose condition needed an image that\nwas not there (preimage_missing) and events refused because the realm was at its backlog\nceiling (backlog). Open ones by default. " + adminAPINote).
		Func(act("triggers gaps", triggersGaps)).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		boolean(cc, "all", "include the resolved gaps")
	})
	tr.SubCommand("resolve").ShortHelp("mark a gap as dealt with").
		LongHelp("Closes a gap: use it once you have run `backd triggers rescan` for it, or decided that\nnothing that matters was lost. Audited as triggers.gap_resolve. " + adminAPINote).
		Func(act("triggers resolve", triggersResolve)).Config(func(cc *kli.CommandConfig) {
		required(cc, "realm", "the realm")
		required(cc, "id", "the gap's id (see `backd triggers gaps`)")
		optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
	})
	tr.SubCommand("rescan").ShortHelp("queue a collection's recently written documents again").
		LongHelp("Re-emits the documents of a collection written in a time range as synthetic `updated`\nevents, to the triggers that can be evaluated from a document alone, after a gap. It is not a\nreplay of history: a document changed twice is read once, as it is now; deletions can't be\nfound; and a trigger whose condition needs `before` or `changed()` is reported and skipped.\nThe jobs carry `rescan` in their input and have ids of their own, so handlers (which must be\nidempotent anyway) see them as new work.\n\nIt runs here, not through the API: it reads CONFIG_DIR and MONGO_URI like `backd provision`,\nand may read a whole collection (over 10,000 documents it asks for --confirm). Without an\nindex on _meta.updated_at, that is a scan of the collection. Audited as triggers.rescan.").
		Func(act("triggers rescan", triggersRescan)).Config(func(cc *kli.CommandConfig) {
		required(cc, "realm", "the realm")
		required(cc, "database", "the database")
		required(cc, "collection", "the collection")
		optional(cc, "trigger", "only this trigger (default: every one that can be rescanned)")
		optional(cc, "updated-since", "documents written from this time: RFC 3339, or a duration back from now such as 24h or 7d")
		optional(cc, "until", "...before this time (default: now)")
		boolean(cc, "confirm", "allow reading more than 10,000 documents")
	})
}
