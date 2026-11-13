package cli

import (
	"fmt"
	"io"

	"github.com/fernandezvara/backd/internal/cli/account"
	"github.com/fernandezvara/backd/internal/cli/admin"
	"github.com/fernandezvara/backd/internal/cli/clicore"
	"github.com/fernandezvara/backd/internal/cli/project"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/buildinfo"
	"github.com/fernandezvara/backd/internal/cmdutil"
)

// newCLI builds backd's command tree. Commands read their flags with str,
// flag and num, and never touch the process environment or standard
// streams directly, so tests run them in-process.
func newCLI(getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) *kli.Config {
	cfg := kli.New().SetName("backd").SetEnv(getenv).SetIO(stdin, stdout, stderr)

	cfg.Command("version").ShortHelp("print the version").
		Func(cmdutil.Act("backd version", func(c *kli.CommandContext) error {
			fmt.Fprintln(c.Stdout(), "backd", buildinfo.Get())
			return nil
		}))

	tpl := cfg.Command("template").ShortHelp("create config files under CONFIG_DIR").
		LongHelp("Creates configuration files for realms, databases and functions under\nCONFIG_DIR, leaving existing files unchanged, and lists what it created.")
	tpl.SubCommand("realm").ShortHelp("create <realm>/realm.yaml; --sample adds the blog database").
		Func(cmdutil.Act("backd template realm", project.TemplateRealm)).
		Config(func(cc *kli.CommandConfig) {
			cmdutil.Required(cc, "realm", "the realm to create")
			cmdutil.Boolean(cc, "sample", "also add the blog sample database \"main\"")
		})
	tpl.SubCommand("database").ShortHelp("create a database directory; --sample adds the posts collection").
		Func(cmdutil.Act("backd template database", project.TemplateDatabase)).
		Config(func(cc *kli.CommandConfig) {
			cmdutil.Required(cc, "realm", "the realm of the database")
			cmdutil.Required(cc, "database", "the database to create")
			cmdutil.Boolean(cc, "sample", "also add the blog sample collection \"posts\"")
		})
	tpl.SubCommand("function").ShortHelp("create a function in the database's _functions project").
		Func(cmdutil.Act("backd template function", project.TemplateFunction)).
		Config(func(cc *kli.CommandConfig) {
			cmdutil.Required(cc, "realm", "the realm of the function")
			cmdutil.Required(cc, "database", "the database of the function")
			cmdutil.Required(cc, "name", "the function to create")
		})
	tpl.SubCommand("email-capture").ShortHelp("add a development delivery function that stores emails in an outbox").
		LongHelp("Adds the function email-capture to the database's _functions project, and the\ncollection outbox it writes to: instead of sending an email, it stores it (links\nincluded) so you can read it. For local development only: the function is\ndev_only, and backd refuses to start with it unless BACKD_DEV=true.").
		Func(cmdutil.Act("backd template email-capture", project.TemplateEmailCapture)).
		Config(func(cc *kli.CommandConfig) {
			cmdutil.Required(cc, "realm", "the realm of the database")
			cmdutil.Required(cc, "database", "the database to add the function and the outbox to")
		})
	tpl.SubCommand("collection-policy").ShortHelp("add a commented collection.yaml: rules, soft delete and what an erase does").
		LongHelp("Creates <realm>/<database>/<collection>/collection.yaml, with everything commented\nout: without a policy an erase leaves the collection alone. Uncomment what you need\n(delete or anonymize the documents a user owns, remove the user from other\ndocuments' arrays and fields). See the docs, Auth -> Erasing users.").
		Func(cmdutil.Act("backd template collection-policy", project.TemplateCollectionPolicy)).
		Config(func(cc *kli.CommandConfig) {
			cmdutil.Required(cc, "realm", "the realm of the collection")
			cmdutil.Required(cc, "database", "the database of the collection")
			cmdutil.Required(cc, "collection", "the collection to add the file to")
		})
	tpl.SubCommand("project").ShortHelp("create a complete config repository").
		LongHelp("Creates a complete config repository at --dir (which must not exist yet): the\nrealm and sample database, a sample function with tests, a compose stack on the\nreleased images for local development (nothing is built), the check-only\nBackd config check workflow, an AGENTS.md and a README.\nSee the generated README.md.").
		Func(cmdutil.Act("backd template project", project.TemplateProject)).
		Config(func(cc *kli.CommandConfig) {
			cmdutil.Required(cc, "dir", "the directory to create")
			cmdutil.Required(cc, "realm", "the realm of the project")
		})

	cfg.Command("databases").
		ShortHelp("print the MongoDB databases (or collections) the config uses").
		LongHelp("Prints the MongoDB databases the config uses, one per line, for granting a\nleast-privilege user access to exactly these. With --collections, every\ncollection as <database>.<collection>. Needs only CONFIG_DIR.").
		Func(cmdutil.Act("backd databases", project.ListDatabases)).
		Config(func(cc *kli.CommandConfig) {
			cmdutil.Boolean(cc, "collections", "print <database>.<collection> instead of databases")
		})

	conf := cfg.Command("config").ShortHelp("check or fingerprint CONFIG_DIR").
		LongHelp("Both need only CONFIG_DIR.")
	conf.SubCommand("check").ShortHelp("validate CONFIG_DIR as startup would").
		LongHelp("Runs every startup check that needs only CONFIG_DIR: names, realm.yaml,\nschemas, indexes, rules, functions and their bundles. Exits non-zero, with the\nsame messages as startup, when backd would refuse to start. For CI, before\npackaging.").
		Func(cmdutil.Act("backd config check", project.CheckConfig)).
		Config(func(*kli.CommandConfig) {})
	conf.SubCommand("fingerprint").ShortHelp("print the fingerprint of CONFIG_DIR").
		LongHelp("Prints one hash of every file backd reads, function bundles included.\n`backd-server provision` records it; instances in PROVISION_MODE=verify refuse to\nstart with a different one.").
		Func(cmdutil.Act("backd config fingerprint", project.ConfigFingerprint)).
		Config(func(*kli.CommandConfig) {})

	rl := cfg.Command("rules").ShortHelp("test the access rules of CONFIG_DIR").
		LongHelp("Needs only CONFIG_DIR, no database.")
	rl.SubCommand("test").ShortHelp("run every collection's rules.test.yaml against its rules").
		LongHelp("Runs the fixture next to each collection's collection.yaml (rules.test.yaml): named\ncallers and stored documents, and per operation which of them are allowed or\ndenied. The decisions are the server's: read rules become the database filter,\nevaluated the way MongoDB does; an update or delete first needs the document to\nbe readable. Prints the failures and exits non-zero when an assertion fails or\na fixture is wrong, for CI. Collections with rules and no fixture are listed;\n--strict makes them fail.").
		Func(cmdutil.Act("backd rules test", project.RulesTest)).
		Config(func(cc *kli.CommandConfig) {
			cmdutil.Optional(cc, "collection", "only this collection, or a prefix: realm, realm/database or realm/database/collection")
			cmdutil.Boolean(cc, "verbose", "also print the assertions that pass")
			cmdutil.Boolean(cc, "strict", "fail when a collection with rules has no rules.test.yaml")
		})

	registerRemote(cfg)
	registerTriggers(cfg)
	return cfg
}

// registerRemote adds the commands that manage a running backd through its
// admin API.
func registerRemote(cfg *kli.Config) {
	loginHelp := "Signs in to a realm of a running backd and keeps the session for later\ncommands, per server and realm, in ~/.config/backd/credentials (or\n$XDG_CONFIG_HOME/backd/credentials, or the file BACKD_CREDENTIALS names),\nreadable only by you. The password is read from the terminal without echo,\nor as the first line of standard input when it isn't a terminal (then\n--email is required).\n\nThe server is --url, else BACKD_URL, else the last one logged in to.\nCommands use BACKD_API_KEY (an admin API key) instead of a stored session\nwhen it is set. BACKD_CA_CERT names a PEM file of extra CAs to trust, for\nservers with a private CA."
	cfg.Command("login").ShortHelp("sign in to a realm and keep the session").LongHelp(loginHelp).
		Func(cmdutil.Act("backd login", account.LoginAction)).
		Config(func(cc *kli.CommandConfig) {
			clicore.Remote(cc)
			cmdutil.Optional(cc, "email", "the email to sign in with (asked for on a terminal)")
			cmdutil.Optional(cc, "strategy", "the strategy to sign in with (needed when the realm has more than one that starts with the password)")
			cmdutil.Boolean(cc, "magic-link", "sign in with a code sent to the email instead of a password (the realm needs magic_link)")
		})
	cfg.Command("logout").ShortHelp("end the stored session and forget it").
		Func(cmdutil.Act("backd logout", account.LogoutAction)).Config(clicore.Remote)
	cfg.Command("whoami").ShortHelp("show who the stored session belongs to").
		Func(cmdutil.Act("backd whoami", account.WhoamiAction)).Config(clicore.Remote)

	acc := cfg.Command("account").ShortHelp("manage your own sign-in security: the second factor and its recovery codes").
		LongHelp("Acts on the signed-in user with the session `backd login` stored (not an API key). Each\nchange asks for a fresh proof that it is you: your authenticator code if you have one,\nelse your password. Codes and passwords are read from the terminal, or from the lines of\nstandard input when it isn't one.")
	acc.SubCommand("strategies").ShortHelp("list the realm's sign-in strategies and their methods (no sign-in needed)").
		Func(cmdutil.Act("backd account strategies", account.StrategiesAction)).Config(clicore.Remote)
	acc.SubCommand("mfa-status").ShortHelp("show whether two-step verification is on").
		Func(cmdutil.Act("backd account mfa-status", account.AccountMFAStatus)).Config(clicore.Remote)
	acc.SubCommand("mfa-enable").ShortHelp("set an authenticator app up: shows a QR code and the recovery codes once").
		Func(cmdutil.Act("backd account mfa-enable", account.AccountMFAEnable)).Config(clicore.Remote)
	acc.SubCommand("mfa-disable").ShortHelp("turn two-step verification off (not for roles that require it)").
		Func(cmdutil.Act("backd account mfa-disable", account.AccountMFADisable)).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Boolean(cc, "yes", "confirm that you want it off")
	})
	acc.SubCommand("recovery-codes").ShortHelp("replace the recovery codes with new ones, shown once").
		Func(cmdutil.Act("backd account recovery-codes", account.AccountRecoveryCodes)).Config(clicore.Remote)

	u := cfg.Command("user").ShortHelp("manage a realm's users").
		LongHelp(clicore.AdminAPINote + " The first administrator of a realm is\ncreated with `backd-server bootstrap`. Passwords are read from the terminal without\necho, or as the first line of standard input when it isn't a terminal. Emails\ncan't be changed.")
	user := func(name, short string, withEmail bool, run func(*admin.UserCtx) error, extra func(*kli.CommandConfig)) {
		u.SubCommand(name).ShortHelp(short).Func(cmdutil.Act("backd user "+name, userAction(withEmail, run))).
			Config(func(cc *kli.CommandConfig) {
				clicore.Remote(cc)
				if withEmail {
					cmdutil.Required(cc, "email", "the user's email")
				}
				if extra != nil {
					extra(cc)
				}
			})
	}
	user("create", "create a user; asks for a password unless --no-password", true, admin.UserCreate, func(cc *kli.CommandConfig) {
		cmdutil.Boolean(cc, "no-password", "create the user without a password")
	})
	user("list", "list the realm's users", false, admin.UserList, nil)
	user("set-password", "set or replace the password; revokes all sessions", true, admin.UserSetPassword, nil)
	user("expire-password", "expire the user's password: it stops opening the account until they reset it; sessions are kept", true, admin.UserExpirePassword, func(cc *kli.CommandConfig) {
		cmdutil.Optional(cc, "reason", "why, for the audit trail")
	})
	user("expire-passwords", "expire every password of the realm and end every session (the first minutes of a leak)", false, admin.UserExpirePasswords, func(cc *kli.CommandConfig) {
		cmdutil.Optional(cc, "reason", "why, for the audit trail")
		cmdutil.Boolean(cc, "keep-sessions", "keep the sessions (a planned rotation, not a breach)")
		cmdutil.Boolean(cc, "notify", "also email every user a link to choose a new password (needs email in realm.yaml)")
		cmdutil.Boolean(cc, "yes", "confirm: every user will have to choose a new password")
	})
	user("notify-expired", "email every user whose password was expired a link to choose a new one; starts again after a cancel or failure", false, admin.UserNotifyExpired, nil)
	user("revoke-sessions", "end every session of the realm, leaving passwords alone", false, admin.UserRevokeSessions, func(cc *kli.CommandConfig) {
		cmdutil.Boolean(cc, "all", "end the sessions of every user")
		cmdutil.Boolean(cc, "yes", "confirm")
	})
	user("reset-link", "print a one-time link for the user to choose a new password, for a user the email can't reach (nothing is sent)", true, admin.UserResetLink, func(cc *kli.CommandConfig) {
		cmdutil.Optional(cc, "redirect-to", "where the page sends the user afterwards (must be in email.allowed_redirects)")
	})
	user("change-email", "change the user's email at once (needs email in realm.yaml); revokes all sessions", true, admin.UserChangeEmail, func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "new-email", "the new email address")
	})
	user("export", "export the user's personal data (a zip) for a formal request; the user is told unless --no-notify", true, admin.UserExport, func(cc *kli.CommandConfig) {
		cmdutil.Optional(cc, "out", "where to write the zip (default export-<id>.zip; an existing file is never overwritten)")
		cmdutil.Optional(cc, "ticket", "the reference of the request (a support ticket), kept with the export")
		cmdutil.Boolean(cc, "no-notify", "do not tell the user an export was made (needs --reason)")
		cmdutil.Optional(cc, "reason", "with --no-notify: why, kept in the audit record")
		cmdutil.Boolean(cc, "no-wait", "print the export's id and return; fetch it later with export-download")
		cmdutil.Optional(cc, "timeout", "how long to wait for the export (default 30m)")
	})
	user("export-download", "download an export made earlier with `user export --no-wait`", true, admin.UserExportDownload, func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "export", "the export's id")
		cmdutil.Optional(cc, "out", "where to write the zip (default export-<id>.zip; an existing file is never overwritten)")
	})
	user("identities", "list the user's ways to sign in: a password and the providers they use", true, admin.UserIdentities, nil)
	user("unlink-identity", "remove one of the user's ways to sign in, never the last (password included)", true, admin.UserUnlinkIdentity, func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "provider", "the sign-in method to remove: password, google, microsoft, apple…")
	})
	user("mfa-status", "show a user's second factor; without --email, who must set one up", false, admin.UserMFAStatus, func(cc *kli.CommandConfig) {
		cmdutil.Optional(cc, "email", "the user to show (default: the users of required roles who have no second factor yet)")
	})
	u.SubCommand("mfa-reset").ShortHelp("remove a user's second factor and recovery codes and end their sessions").
		LongHelp(clicore.AdminAPINote + " An administrator can't reset their own second factor; for the only administrator of a\nrealm who lost it, `backd-server user mfa-reset` acts on the instance's database directly.").
		Func(cmdutil.Act("backd user mfa-reset", userAction(true, admin.UserMFAReset))).
		Config(func(cc *kli.CommandConfig) {
			clicore.Remote(cc)
			cmdutil.Required(cc, "email", "the user's email")
			cmdutil.Boolean(cc, "yes", "confirm the reset, which ends all of the user's sessions")
		})
	user("owned", "show what erasing the user would do, per collection that declares a policy", true, admin.UserOwned, nil)
	user("verify-email", "mark the email as verified", true, admin.UserPatch(map[string]bool{"email_verified": true}, "email of %s marked as verified\n"), nil)
	user("disable", "block sign-in and revoke all sessions", true, admin.UserPatch(map[string]bool{"disabled": true}, "%s disabled; all of their sessions were revoked\n"), nil)
	user("enable", "allow a disabled user to sign in again", true, admin.UserPatch(map[string]bool{"disabled": false}, "%s enabled\n"), nil)
	user("delete", "erase the user: a tombstone, and the collections' policies applied", true, admin.UserDelete, func(cc *kli.CommandConfig) {
		cmdutil.Boolean(cc, "yes", "confirm the erase, which can't be undone")
		cc.Define("wait").String().Flag("wait").Default("2m").Description("how long to wait for the erase to finish (such as 30s; 0s: don't wait)")
	})
	user("add-role", "assign a role declared in realm.yaml", true, func(c *admin.UserCtx) error {
		return admin.UserRole(c, "add", cmdutil.Str(c.Cmd, "role"))
	}, func(cc *kli.CommandConfig) { cmdutil.Required(cc, "role", "the role") })
	user("remove-role", "take a role away", true, func(c *admin.UserCtx) error {
		return admin.UserRole(c, "remove", cmdutil.Str(c.Cmd, "role"))
	}, func(cc *kli.CommandConfig) { cmdutil.Required(cc, "role", "the role") })
	user("networks", "show or limit where the user may use the admin API and log in from", true, admin.UserNetworks, func(cc *kli.CommandConfig) {
		cmdutil.Optional(cc, "admin", "where the admin API may be used from: comma-separated IP addresses or CIDR networks, or \"none\" to lift the limit")
		cmdutil.Optional(cc, "login", "where the user may log in and use their session from (same format)")
	})

	pu := cfg.Command("push").ShortHelp("see a realm's push notification devices").
		LongHelp(clicore.AdminAPINote + " Devices are registered by the users' apps (POST /_auth/devices); tokens are never shown.")
	pu.SubCommand("devices").ShortHelp("list a user's registered devices").
		Func(cmdutil.Act("backd push devices", userAction(true, admin.PushDevices))).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Required(cc, "email", "the user's email")
	})
	pu.SubCommand("sends").ShortHelp("list the realm's push sends, or show one with --id").
		LongHelp("A send's record holds counts, never a message, a user or a token. Needs the functions area.").
		Func(cmdutil.Act("backd push sends", admin.PushSends)).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Optional(cc, "id", "show this send (ps_…) with its progress")
		cmdutil.Optional(cc, "kind", "list only sends of this template kind (raw for raw messages)")
		cmdutil.Optional(cc, "function", "list only the sends of this function: <database>/<name>")
		cmdutil.Optional(cc, "limit", "how many sends to list (1 to 200, default 50)")
	})
	pu.SubCommand("test").ShortHelp("send one user a test push (needs the users area; audited; counts toward their limits)").
		Func(cmdutil.Act("backd push test", userAction(true, admin.PushTest))).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Required(cc, "email", "the user's email")
		cmdutil.Optional(cc, "kind", "a template kind of the realm")
		cmdutil.Optional(cc, "data", "the data for the template, as a JSON object of strings")
		cmdutil.Optional(cc, "title", "the title of a raw push (with --body)")
		cmdutil.Optional(cc, "body", "the body of a raw push")
	})
	pu.SubCommand("web-keys").ShortHelp("make the realm's VAPID key pair for web push").
		LongHelp("Stores the private key as the realm secret VAPID_PRIVATE_KEY and prints the public key with the\npush.web snippet for realm.yaml. It refuses to replace an existing key without --rotate; rotating\nremoves every web device of the realm, because browsers made their subscriptions under the old\npublic key (apps subscribe again at their next launch).").
		Func(cmdutil.Act("backd push web-keys", admin.PushWebKeys)).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Optional(cc, "name", "the secret that holds the private key (default VAPID_PRIVATE_KEY)")
		cmdutil.Boolean(cc, "rotate", "replace an existing key pair and remove the realm's web devices")
	})
	pu.SubCommand("remove-device").ShortHelp("remove one of a user's devices (audited)").
		Func(cmdutil.Act("backd push remove-device", userAction(true, admin.PushRemoveDevice))).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Required(cc, "email", "the user's email")
		cmdutil.Required(cc, "id", "the device's id, from `backd push devices`")
	})

	og := cfg.Command("orgs").ShortHelp("manage a realm's organizations").
		LongHelp(clicore.AdminAPINote + " Needs the users area. People create and manage organizations through the realm's /_orgs API; this is for administrators.")
	og.SubCommand("list").ShortHelp("list the realm's organizations").
		Func(cmdutil.Act("backd orgs list", admin.OrgsList)).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cc.Define("status").String().Flag("status").Default("").OneOf("", "active", "orphaned", "deleted").Description("only organizations with this status (orphaned: no manager left)")
	})
	og.SubCommand("show").ShortHelp("show an organization with its claims").
		Func(cmdutil.Act("backd orgs show", admin.OrgsShow)).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Required(cc, "id", "the organization's id")
	})
	og.SubCommand("create").ShortHelp("create an organization owned by a user").
		Func(cmdutil.Act("backd orgs create", admin.OrgsCreate)).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Required(cc, "name", "the organization's name")
		cmdutil.Required(cc, "slug", "a unique short name people sign in with: lowercase letters, digits and '-'")
		cmdutil.Required(cc, "owner", "the email of the user who becomes its owner")
	})
	og.SubCommand("set-slug").ShortHelp("give an organization its slug, or change it").
		Func(cmdutil.Act("backd orgs set-slug", admin.OrgsSetSlug)).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Required(cc, "id", "the organization's id")
		cmdutil.Required(cc, "slug", "a unique short name people sign in with: lowercase letters, digits and '-'")
	})
	og.SubCommand("members").ShortHelp("list an organization's members with their roles").
		Func(cmdutil.Act("backd orgs members", admin.OrgsMembers)).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Required(cc, "id", "the organization's id")
	})
	og.SubCommand("add-member").ShortHelp("add a user to an organization without an invitation (audited)").
		Func(cmdutil.Act("backd orgs add-member", admin.OrgsAddMember)).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Required(cc, "id", "the organization's id")
		cmdutil.Required(cc, "email", "the user's email")
		cmdutil.Optional(cc, "roles", "organization roles, comma-separated")
	})
	og.SubCommand("assign-owner").ShortHelp("make a user a manager of an organization (for an orphaned one)").
		Func(cmdutil.Act("backd orgs assign-owner", admin.OrgsAssignOwner)).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Required(cc, "id", "the organization's id")
		cmdutil.Required(cc, "email", "the user's email")
	})
	og.SubCommand("invite").ShortHelp("invite to an organization: by address, to a user, or with an open link").
		LongHelp("Prints the link and token once. --send also emails the link (needs email and the secrets key on the server).").
		Func(cmdutil.Act("backd orgs invite", admin.OrgsInvite)).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Required(cc, "id", "the organization's id")
		cmdutil.Optional(cc, "email", "invite whoever signs in with this verified address")
		cmdutil.Optional(cc, "user", "invite this existing user (their email)")
		cmdutil.Boolean(cc, "open", "an open link: anyone signed in who holds it (see --uses)")
		cmdutil.Optional(cc, "roles", "organization roles to give, comma-separated")
		cmdutil.Optional(cc, "expires", "how long it works, such as 2d (default 7d, at most 30d)")
		cc.Define("uses").Int64().Flag("uses").Default(0).Min(0).Max(1000).Description("how many times an open link may be used (default 1)")
		cmdutil.Boolean(cc, "send", "also email it (with --email)")
	})
	og.SubCommand("invitations").ShortHelp("list an organization's invitations (never tokens)").
		Func(cmdutil.Act("backd orgs invitations", admin.OrgsInvitations)).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Required(cc, "id", "the organization's id")
	})
	og.SubCommand("revoke-invitation").ShortHelp("revoke an invitation").
		Func(cmdutil.Act("backd orgs revoke-invitation", admin.OrgsRevokeInvitation)).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Required(cc, "id", "the organization's id")
		cmdutil.Required(cc, "invitation", "the invitation's id")
	})
	og.SubCommand("owned").ShortHelp("report what deleting an organization would do").
		Func(cmdutil.Act("backd orgs owned", admin.OrgsOwned)).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Required(cc, "id", "the organization's id")
	})
	og.SubCommand("delete").ShortHelp("delete an organization: access ends at once, its documents follow").
		LongHelp("Ends everyone's access at once (the record stays, as deleted), then a job applies each collection's on_org_delete: `delete` removes the documents and queues their files for deletion, `keep` leaves them for realm administrators. See `backd orgs owned` first. Needs --yes. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd orgs delete", admin.OrgsDelete)).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Required(cc, "id", "the organization's id")
		cmdutil.Boolean(cc, "yes", "confirm")
	})
	og.SubCommand("reactivate").ShortHelp("bring a deleted organization back, with no members").
		Func(cmdutil.Act("backd orgs reactivate", admin.OrgsReactivate)).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Required(cc, "id", "the organization's id")
		cmdutil.Optional(cc, "slug", "the slug it takes (its former one may be in use)")
	})
	og.SubCommand("claims-set").ShortHelp("replace an organization's claims (at most 1 KiB of JSON)").
		Func(cmdutil.Act("backd orgs claims-set", admin.OrgsClaimsSet)).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Required(cc, "id", "the organization's id")
		cmdutil.Required(cc, "claims", "a JSON object, for example '{\"plan\": \"pro\"}'")
	})

	og.SubCommand("sso").ShortHelp("show an organization's sign-in connections (never their client secrets)").
		LongHelp("Lists the connections an organization set up (design: organization sign-in), with the address to register\nwith each provider. Needs organizations.sso in realm.yaml. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd orgs sso", admin.OrgsSSO)).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Required(cc, "id", "the organization's id")
	})
	og.SubCommand("sso-set").ShortHelp("create or replace an organization's sign-in connection").
		LongHelp("Connects the organization to its own Google Workspace (--provider google --hosted-domain acme.example) or\nMicrosoft Entra tenant (--provider microsoft --tenant <tenant id>). The client secret is read from the terminal\nwithout echo, or the first line of standard input; --keep-secret leaves the stored one. It is stored as an\norganization secret nobody can read back. --join-roles are given to people who join through it. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd orgs sso-set", admin.OrgsSSOSet)).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Required(cc, "id", "the organization's id")
		cc.Define("provider").String().Flag("provider").Required().OneOf("google", "microsoft").Description("google or microsoft")
		cmdutil.Required(cc, "client-id", "the organization's application (client) id with the provider")
		cmdutil.Optional(cc, "tenant", "Microsoft: the tenant id (a GUID); common, organizations and consumers are refused")
		cmdutil.Optional(cc, "hosted-domain", "Google: the Workspace domain whose people may sign in")
		cmdutil.Optional(cc, "email-domains", "comma-separated: also require the verified email to be in one of these domains")
		cmdutil.Optional(cc, "native-client-ids", "comma-separated: extra client ids the id-token endpoint accepts (mobile apps)")
		cmdutil.Optional(cc, "join-roles", "comma-separated organization roles given to people who join through it")
		cmdutil.Boolean(cc, "disabled", "keep the connection but refuse sign-ins through it")
		cmdutil.Boolean(cc, "keep-secret", "do not ask for the client secret: keep the stored one")
	})
	og.SubCommand("sso-delete").ShortHelp("remove an organization's sign-in connection and its client secret").
		Func(cmdutil.Act("backd orgs sso-delete", admin.OrgsSSODelete)).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Required(cc, "id", "the organization's id")
		cc.Define("provider").String().Flag("provider").Required().OneOf("google", "microsoft").Description("google or microsoft")
	})

	og.SubCommand("sso-required-off").ShortHelp("let an organization's people sign in the other ways again (break glass, audited)").
		LongHelp("For an organization that requires its connection (sso_required) and whose people can't reach their provider.").
		Func(cmdutil.Act("backd orgs sso-required-off", admin.OrgsSSORequiredOff)).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Required(cc, "id", "the organization's id")
	})

	k := cfg.Command("apikey").ShortHelp("manage a realm's API keys").
		LongHelp(clicore.AdminAPINote + "\n\nAPI keys give server-side services full access to their realm's data.\nStore them as secrets.")
	k.SubCommand("create").ShortHelp("create an API key and print it once").
		LongHelp("--role data (default) reaches data routes; admin also reaches the admin API.\n--expires takes days (\"90d\") or Go durations (\"12h\"); default: never (not\nrecommended: startup warns about such keys). --networks limits where the key\nmay be used from. --scopes limits what a data key reaches, so a leaked key exposes\nonly that: comma-separated grants read, write or call, each optionally followed by\n:<database> or :<database>/<collection or function>, for example\n--scopes read:blog/posts,call:main/contact. Without scopes the key reaches everything.").
		Func(cmdutil.Act("backd apikey create", admin.ApikeyCreate)).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Required(cc, "name", "the key's name")
		cc.Define("role").String().Flag("role").Default("data").OneOf("data", "admin").Description("data (reaches data routes) or admin (also reaches the admin API)")
		cmdutil.Optional(cc, "expires", "how long the key is valid, such as 90d or 12h")
		cmdutil.Optional(cc, "networks", "where the key may be used from: comma-separated IP addresses or CIDR networks")
		cmdutil.Optional(cc, "scopes", "what a data key reaches: comma-separated grants such as read:blog/posts,write:blog,call:main/export (default: everything)")
		cmdutil.Optional(cc, "org", "in a realm with organizations, the organization (id or slug) a data key acting on behalf of users works in")
	})
	k.SubCommand("list").ShortHelp("list the realm's API keys (never the keys themselves)").
		Func(cmdutil.Act("backd apikey list", admin.ApikeyList)).Config(clicore.Remote)
	k.SubCommand("revoke").ShortHelp("delete the key; it stops working at once").
		Func(cmdutil.Act("backd apikey revoke", admin.ApikeyRevoke)).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Required(cc, "name", "the key's name")
	})

	sec := cfg.Command("secret").ShortHelp("manage functions' secrets").
		LongHelp("set, list and delete use the realm's admin API on a running backd, as the\nsession stored by `backd login` or with the admin API key in BACKD_API_KEY.")
	sec.SubCommand("set").ShortHelp("create or replace a secret's value").
		LongHelp("The value is read from the terminal without echo, or the first line of\nstandard input when it isn't a terminal. Without --database it's the realm\nscope (realm.NAME in function.yaml); with it, that database's scope (NAME, for\nfunctions of that database only, never another's). With --org it is an organization's\nsecret (its sign-in client secrets): sealed like the others, but nobody can read it back, functions included.").
		Func(cmdutil.Act("backd secret set", admin.SecretSet)).Config(secretFlags)
	admin.RegisterFieldKeys(sec, admin.Remote)
	sec.SubCommand("delete").ShortHelp("delete a secret; a function that declares it answers secret_missing again").
		Func(cmdutil.Act("backd secret delete", admin.SecretDelete)).Config(secretFlags)

	cfg.Command("audit").ShortHelp("print the realm's audit trail, newest first").
		LongHelp("Prints who created, changed or deleted users, roles, network restrictions,\nAPI keys and invitations, administrators' logins, and admin requests refused\nby network. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd audit", admin.AuditAction)).Config(func(cc *kli.CommandConfig) {
		clicore.Remote(cc)
		cmdutil.Optional(cc, "action", "only this action, such as role.add or apikey.create")
		cmdutil.Optional(cc, "actor", "only this actor: user:<id>, key:<name>, anonymous, config:realm.yaml or cli:bootstrap")
		cmdutil.Optional(cc, "user", "only records about this (existing) user's email")
		cmdutil.Optional(cc, "target", "only this target: user:<id>, key:<name> or invitation:<id>")
		cmdutil.Optional(cc, "since", "from this time: RFC 3339, or a duration back from now such as 24h or 7d")
		cmdutil.Optional(cc, "until", "before this time (RFC 3339)")
		limit(cc)
		cmdutil.Boolean(cc, "json", "one JSON object per line, for other tools")
	})

	st := cfg.Command("storage").ShortHelp("check, inspect and clean up a realm's object storage")
	st.SubCommand("check").ShortHelp("verify the realm's storage the way files would use it").
		LongHelp("Asks the running backd, which holds the realm's access keys, to check its storage:\nthe bucket and credentials, put, head, get, a range request and a listing on the realm's\nprefix, the signed SHA-256 (a wrong one must be rejected), signed download and upload\nlinks, and, where the provider exposes them, the bucket's CORS rules and default\nencryption. It stores a few small objects under <prefix>/<realm>/_check/ and deletes them\nagain. A realm with a public_storage has that one checked too (or only that with --storage public): the\nheaders public files are stored with, and a small image it leaves in the bucket and gives the address of,\nbecause backd never connects to the public url; open it to test the bucket or CDN. Exits 1 when a step failed (warnings don't fail it). Audited as storage.check. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd storage check", admin.StorageCheck)).Config(func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "realm", "the realm whose storage to check")
		cmdutil.Optional(cc, "storage", "private or public (default: every storage the realm has)")
		cmdutil.Optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		cmdutil.Boolean(cc, "json", "print the report as JSON")
	})
	st.SubCommand("usage").ShortHelp("show what the realm's storage holds and whether it works").
		LongHelp("Asks the running backd for the realm's storage: how it is configured (never the keys), whether the keys\nare set and the bucket answers, the running totals of bytes and files the documents reference for the realm\nand for the users holding the most, and what waits to be cleaned up (objects queued for deletion, uploads left\nunfinished). " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd storage usage", admin.StorageUsage)).Config(func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "realm", "the realm whose storage to show")
		cmdutil.Optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		cmdutil.Boolean(cc, "json", "print it as JSON")
	})
	st.SubCommand("reconcile").ShortHelp("find (and with --delete remove) objects no document references").
		LongHelp("Lists the realm's file objects (the only time backd lists the bucket) and reports the ones no declared file\nfield of any document references. Use it after restoring a database backup or removing a file field. It always\nskips objects younger than 24 hours, objects whose upload record is still open (uploads in flight, pending\nuploads not yet expired, files a document holds) and anything that isn't <prefix>/<realm>/<database>/<collection>/<file id>.\nA realm with a public_storage has both storages reconciled (--storage picks one). Without --delete it only reports; with it, it removes exactly the objects the report lists. Needs the config\narea with write access. Audited as storage.reconcile. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd storage reconcile", admin.StorageReconcile)).Config(func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "realm", "the realm whose storage to reconcile")
		cmdutil.Optional(cc, "storage", "private or public (default: every storage the realm has)")
		cmdutil.Optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		cmdutil.Boolean(cc, "delete", "remove the unreferenced objects instead of only reporting them")
		cmdutil.Boolean(cc, "json", "print the report as JSON")
	})
	admin.RegisterTransfer(cfg, admin.Remote)

	cfg.Command("export-decrypt").ShortHelp("open encrypted fields of an export package, offline").
		LongHelp("Copies a package (--from: its directory or .tar.gz) to --out with the encrypted fields you give a key for opened (--field, as in backd export): no server, no database and no master key are involved, only the package's wrapped data keys and your field keys. Every key is checked against the fingerprint the manifest records, and a wrong one is reported by field and key name. Fields without a key stay ciphertext, and the manifest says which are which. The result holds plaintext: keep it as carefully as the keys.").
		Func(cmdutil.Act("backd export-decrypt", project.ExportDecrypt)).Config(func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "from", "the package: its directory, or the .tar.gz that export --archive wrote")
		cmdutil.Required(cc, "out", "the directory to write the opened package to")
		cmdutil.Required(cc, "field", "the keys: <path>=<key:|env:|file:…> entries separated by ';'")
	})

	cons := cfg.Command("consents").ShortHelp("manage the consents users accept")
	cons.SubCommand("notify").ShortHelp("email the users who hold an older version of a consent").
		LongHelp("Queues the `consent-updated` email for every user who accepted an older version of the consent than the\ncurrent one (and, for a required consent, who accepted none). Publishing a version never sends anything by itself:\nthis is the only way, and the email limits apply, so a large realm may need to run it again later. Needs the\nusers area with write access. Audited as consents.notify. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd consents notify", admin.ConsentsNotify)).Config(func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "realm", "the realm whose users to notify")
		cmdutil.Required(cc, "consent", "the consent whose current version to ask them to review")
		cmdutil.Optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		cmdutil.Boolean(cc, "json", "print the result as JSON")
	})
	cons.SubCommand("record").ShortHelp("record a consent given or withdrawn outside the app").
		LongHelp("Records, for one user, a consent they gave or withdrew on paper, by phone or in a way the app can't see.\nThe record has the method `admin` and the evidence, which is required: a ticket, a form number, anything that\nproves it. Use --action accepted with --version, or --action withdrawn. Needs the users area with write access.\nAudited as user.consent_recorded. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd consents record", admin.ConsentsRecord)).Config(func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "realm", "the realm of the user")
		cmdutil.Required(cc, "email", "the user's email address")
		cmdutil.Required(cc, "consent", "the consent's name")
		cmdutil.Required(cc, "action", "accepted or withdrawn")
		cmdutil.Required(cc, "evidence", "the reference that proves it (a ticket, a form number)")
		cmdutil.Optional(cc, "version", "the version accepted (for accepted)")
		cmdutil.Optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
	})
	comp := cfg.Command("compliance").ShortHelp("privacy: what a user's data export covers")
	comp.SubCommand("scope").ShortHelp("show what a user's data export takes from each collection").
		LongHelp("Asks the running backd for the realm's personal data scope: per collection whether the documents a user owns\n(and those where they appear in an erase policy's `pull` field, and the files they hold) are exported, which fields\nwhen only some are, and the reason for every exclusion (`personal_data` in collection.yaml), then the system data\nan export adds and what it never holds. Needs the config area (read is enough). " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd compliance scope", admin.ComplianceScopeCmd)).Config(func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "realm", "the realm whose scope to show")
		cmdutil.Optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		cmdutil.Boolean(cc, "json", "print it as JSON")
	})
	cfg.Command("openapi").ShortHelp("generate a realm's OpenAPI document").
		LongHelp("Generates the OpenAPI 3.1 document of a realm's client API from CONFIG_DIR: the collections clients can reach\nwith their schemas and the features they enable, the sign-in methods the realm enables, its non-internal functions\nwith their input and output schemas, and its app configuration. It is what generators of typed clients for other\nplatforms (Dart, Kotlin, Swift) read; a running backd serves the same document at /v1/<realm>/_openapi.json.\n\nPrints it, or writes --out. --check writes nothing and exits non-zero when --out is missing or stale, for CI.\n\n--admin generates the admin API's document instead (served at /v1/<realm>/_admin/openapi.json to administrators):\nusers, API keys, secrets, audit, jobs, configuration, the data routes past the collections' rules typed per\ncollection, and every function an administrator may run by hand, internal ones included, with their input and\noutput. For internal tooling.").
		Func(cmdutil.Act("backd openapi", project.OpenAPICommand)).Config(func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "realm", "the realm")
		cmdutil.Optional(cc, "out", "write the document to this file instead of standard output")
		cmdutil.Boolean(cc, "check", "write nothing; fail when --out is missing or differs from what the config produces")
		cmdutil.Boolean(cc, "admin", "the admin API's document instead: administration routes, data routes past the rules, and internal functions")
	})
	cfg.Command("types").ShortHelp("generate a realm's TypeScript or Dart types").
		LongHelp("Generates TypeScript types from the realm's config: documents, where and order_by per collection, functions,\napp configuration, email and push templates and roles. They come from the same config the server enforces; nothing\nis checked at runtime from them. Reads CONFIG_DIR, or a running backd with --from (an admin of the realm, the config\narea; " + clicore.AdminAPINote + ") and writes types.d.ts (types.dart with --lang dart, types.go with --lang go, types.py with --lang python) and realm.json into --out.\n\n--check writes nothing and exits non-zero when the files in --out are missing or stale, for CI. --diff writes\nnothing and prints what changed at the type level since they were generated (a field added, a type changed, a\ncollection removed, a function's input changed), for pull request reviews. Things TypeScript can't express are\ntyped unknown (Any in Python, any in Go, Object? in Dart) and listed as warnings on standard error.\n\n--functions writes each functions project's backd.d.ts instead (in <realm>/<database>/_functions/, from CONFIG_DIR only):\nthe registry defineFunction(\"<database>/<name>\", handler) takes its types from. In dev mode backd rewrites it after\nevery config reload.").
		Func(cmdutil.Act("backd types", project.TypesCommand)).Config(func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "realm", "the realm")
		cmdutil.Optional(cc, "db", "only this database of the realm")
		cc.Define("lang").String().Flag("lang").Default("ts").Description("the language to emit: ts (types.d.ts), dart (types.dart), go (types.go) or python (types.py)")
		cmdutil.Optional(cc, "package", "with --lang go: the package name of types.go (backdtypes by default)")
		cc.Define("out").String().Flag("out").Default("backd").Description("the directory for the types file and realm.json")
		cmdutil.Optional(cc, "from", "read the config from this running backd instead of CONFIG_DIR")
		cmdutil.Boolean(cc, "check", "write nothing; fail when the generated files are missing or stale")
		cmdutil.Boolean(cc, "diff", "write nothing; print the type-level changes since the files were generated")
		cmdutil.Boolean(cc, "functions", "write each functions project's backd.d.ts (for defineFunction) instead of --out; with --check, only check")
	})
	appcfg := cfg.Command("app-config").ShortHelp("the realm's app configuration: values, history, rollback")
	appcfg.SubCommand("get").ShortHelp("show the configuration with every value").
		LongHelp("Asks the running backd for the realm's app configuration: every key with its effective value (the keys clients\ncan't read too), marking the ones that differ from their defaults, and the stored values that stopped validating\nafter a deploy. --key limits it to one key or group. Needs the config area (read is enough). " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd app-config get", admin.AppConfigGet)).Config(func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "realm", "the realm")
		cmdutil.Optional(cc, "key", "only this key or group, as a dot path")
		cmdutil.Optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		cmdutil.Boolean(cc, "json", "print it as JSON")
	})
	appcfg.SubCommand("set").ShortHelp("change values").
		LongHelp("Sets one value with --key and --value (JSON when it parses: true, 10, [\"a\"]; text otherwise), or several with\n--values '{\"a.b\": 1, \"c.d\": true}'. The result is validated as a whole against _realm/config/schema.json and applied\nor refused. The change is based on the version read just before (or --if-match). Needs the config area with write\naccess. Audited as appconfig.change. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd app-config set", admin.AppConfigSet)).Config(func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "realm", "the realm")
		cmdutil.Optional(cc, "key", "the key to set, as a dot path")
		cmdutil.Optional(cc, "value", "its value")
		cmdutil.Optional(cc, "values", "several values, as a JSON object of key path to value")
		cmdutil.Optional(cc, "if-match", "the version the change is based on (default: the current one)")
		cmdutil.Optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		cmdutil.Boolean(cc, "json", "print the result as JSON")
	})
	appcfg.SubCommand("reset").ShortHelp("return keys to their defaults").
		LongHelp("Returns keys, or whole groups of keys, to the defaults declared in schema.json. Needs the config area with write\naccess. Audited as appconfig.change. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd app-config reset", admin.AppConfigReset)).Config(func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "realm", "the realm")
		cmdutil.Required(cc, "keys", "the keys or groups to reset, comma separated")
		cmdutil.Optional(cc, "if-match", "the version the change is based on (default: the current one)")
		cmdutil.Optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		cmdutil.Boolean(cc, "json", "print the result as JSON")
	})
	appcfg.SubCommand("history").ShortHelp("show who changed what").
		LongHelp("The versions of the configuration, newest first, with the actor and every path's old and new value. The last 100\nversions or 90 days are kept, whichever is longer. Needs the config area (read is enough). " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd app-config history", admin.AppConfigHistory)).Config(func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "realm", "the realm")
		cmdutil.Optional(cc, "limit", "how many versions (default and most: 100)")
		cmdutil.Optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		cmdutil.Boolean(cc, "json", "print it as JSON")
	})
	appcfg.SubCommand("rollback").ShortHelp("restore an earlier version").
		LongHelp("Restores the values a version had (c0 is the defaults) as a new version, so the rollback is in the history too.\nValues that no longer fit the schema are refused. Needs the config area with write access. Audited as\nappconfig.change. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd app-config rollback", admin.AppConfigRollback)).Config(func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "realm", "the realm")
		cmdutil.Required(cc, "version", "the version to restore, such as c2")
		cmdutil.Optional(cc, "if-match", "the version the change is based on (default: the current one)")
		cmdutil.Optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		cmdutil.Boolean(cc, "json", "print the result as JSON")
	})
	files := cfg.Command("files").ShortHelp("work on the image files documents hold")
	files.SubCommand("regenerate").ShortHelp("make the image versions of a file field again when they no longer match its declaration").
		LongHelp("Starts a job that goes through every document of a file field and makes again, on a worker, the image\nversions whose copies no longer match the field's `versions:` in collection.yaml: copies made with other\nparameters (after you changed the declaration), copies that failed or never finished, and versions added later\nthat a file has no copy of. Copies a function generated with its own parameters, versions only functions\nmake, and files that are not pictures are left alone. --missing-only makes only the copies that do not exist.\nThe job runs in the background (one per field at a time), makes at most --rate files a second, keeps serving\nthe old copy of a file until its new one is ready, and can be cancelled and resumed: running it again skips\nwhat matches already. This command follows its progress on stderr; --no-wait only starts it. Needs the config\narea with write access. Audited as files.versions.regenerate. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd files regenerate", admin.FilesRegenerate)).Config(func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "realm", "the realm")
		cmdutil.Required(cc, "database", "the file field's database")
		cmdutil.Required(cc, "collection", "the file field's collection")
		cmdutil.Required(cc, "field", "the file field")
		cmdutil.Optional(cc, "version", "only this version (default: every version the field declares with parameters)")
		cmdutil.Optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		cmdutil.Boolean(cc, "missing-only", "make only the copies that do not exist, not the ones made with other parameters")
		cc.Define("rate").Int64().Flag("rate").Default(0).Min(0).Max(1000).Description("at most this many files a second (default 10)")
		cmdutil.Boolean(cc, "no-wait", "start the job and print its id, without following it")
		cc.Define("interval").String().Flag("interval").Default("2s").Description("how often to ask the server for progress")
	})
	data := cfg.Command("data").ShortHelp("check the stored documents against their schemas")
	checkFlags := func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "realm", "the realm")
		cmdutil.Optional(cc, "database", "only this database (default: every database of the realm)")
		cmdutil.Optional(cc, "collection", "only this collection (needs --database)")
		cmdutil.Optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		cmdutil.Boolean(cc, "json", "one JSON object per line")
	}
	data.SubCommand("check").ShortHelp("report the documents that no longer match their collection's schema").
		LongHelp("Starts a schema check and follows it: backd reads every stored document of the\ncollections (soft-deleted ones too), validates it against the collection's current\nschema as the API validates a write, and lists the invalid ones with the path and the\nrule that failed, never the values. It never writes. **It reads every document, so on a\nbig collection it takes a long time and loads MongoDB**: it runs in the background on a\nworker (one check per realm at a time) and this command follows its progress, in\nsteps of 5 percent, on stderr. Exits non-zero when drift is found or a collection was not\nchecked to the end. --no-wait only starts it and prints the job id; the latest reports\nstay readable with `backd data checks`. Audited as data.check. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd data check", admin.DataCheck)).Config(func(cc *kli.CommandConfig) {
		checkFlags(cc)
		cc.Define("limit").Int64().Flag("limit").Default(0).Min(0).Max(1000).Description("list at most this many invalid documents per collection (default 100, at most 1000)")
		cmdutil.Boolean(cc, "no-wait", "start the check and print its job id, without following it")
		cc.Define("interval").String().Flag("interval").Default("2s").Description("how often to ask the server for progress")
	})
	data.SubCommand("checks").ShortHelp("print the latest schema check reports").
		LongHelp("Lists every collection of the realm with its latest schema check (documents read,\ninvalid, whether it was complete, when it finished), and notes a check running now. With\n--database and --collection prints that collection's report in full. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd data checks", admin.DataChecks)).Config(checkFlags)
	docFlags := func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "realm", "the realm")
		cmdutil.Required(cc, "database", "the database")
		cmdutil.Required(cc, "collection", "the collection")
		cmdutil.Optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
	}
	data.SubCommand("transfer").ShortHelp("move one document to another organization or team (audited)").
		LongHelp("Moves a document to another organization, or out of its organization with --org none when the collection's documents may have none. --team names a team of the organization the document ends up in (none takes it out of its team): moving to another organization leaves the team. It is a deliberate act of a realm administrator, not an update: the version goes up and the audit trail names both organizations. Needs the data area with write access. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd data transfer", admin.DataTransfer)).Config(func(cc *kli.CommandConfig) {
		docFlags(cc)
		cmdutil.Required(cc, "id", "the document's id")
		cmdutil.Required(cc, "org", "the organization's id, or none")
		cmdutil.Optional(cc, "team", "the team's id in that organization, or none")
	})
	data.SubCommand("assign-org").ShortHelp("give the documents that have no organization one").
		LongHelp("For a collection that adopted organizations after it had documents: they have none, and members can't see them. Queues a job that gives the organization to the documents that have none and match --where (a JSON filter, as in a list query), and never moves one that has an organization. --dry-run only counts them. Needs the data area with write access. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd data assign-org", admin.DataAssignOrg)).Config(func(cc *kli.CommandConfig) {
		docFlags(cc)
		cmdutil.Required(cc, "org", "the organization's id")
		cmdutil.Optional(cc, "where", "only documents matching this JSON filter")
		cmdutil.Boolean(cc, "dry-run", "count the documents without changing anything")
	})
	enc := cfg.Command("encrypt").ShortHelp("see and change how encrypted fields are stored")
	encFlags := func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "realm", "the realm")
		cmdutil.Optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
	}
	enc.SubCommand("status").ShortHelp("count how the values of each encrypted field are stored").
		LongHelp("For every collection that encrypts fields (or once did), per field: documents holding the value in plaintext, encrypted, sealed with the current data key and with an older one, how many have their blind index, the key with its fingerprint and the generations of its data keys. A collection is complete when nothing is left to migrate. It counts documents, so on a big collection it takes a moment. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd encrypt status", admin.EncryptStatus)).Config(func(cc *kli.CommandConfig) {
		encFlags(cc)
		cmdutil.Optional(cc, "database", "only this database")
		cmdutil.Optional(cc, "collection", "only this collection (needs --database)")
		cmdutil.Boolean(cc, "json", "print the answer as JSON")
	})
	enc.SubCommand("rekey").ShortHelp("start a new generation of a field's data key").
		LongHelp("Gives an encrypted field a fresh data key: new values are sealed with it, and the existing ones stay readable until `backd encrypt migrate` seals them again. For data keys that may have leaked (a server's memory, a decrypted dump); replacing the field key (`backd secret rotate`) never needs it. Needs a full administrator and a fresh proof. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd encrypt rekey", admin.EncryptRekey)).Config(func(cc *kli.CommandConfig) {
		encFlags(cc)
		cmdutil.Required(cc, "database", "the database")
		cmdutil.Required(cc, "collection", "the collection")
		cmdutil.Required(cc, "field", "the encrypted field's path")
	})
	enc.SubCommand("migrate").ShortHelp("bring a collection's stored documents in line with its encryption configuration").
		LongHelp("Runs a background job on a worker that reads the collection in id order, at most 1000 documents a second, and rewrites only what differs: plaintext values are encrypted, values under an older data key or generation sealed again, blind indexes built or rebuilt, and values of fields that are no longer encrypted opened. It goes on from where it stopped if a worker dies, never changes `_meta.version`, and at the end retires the data keys nothing uses. --dry-run only counts. Equality searches can miss documents whose index is not built yet until it finishes. Needs a full administrator and a fresh proof. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd encrypt migrate", admin.EncryptMigrate)).Config(func(cc *kli.CommandConfig) {
		encFlags(cc)
		cmdutil.Required(cc, "database", "the database")
		cmdutil.Required(cc, "collection", "the collection")
		cmdutil.Optional(cc, "field", "only these encrypted fields: comma-separated paths (fields no longer encrypted are always opened)")
		cmdutil.Boolean(cc, "dry-run", "count what would change without changing it")
		cmdutil.Boolean(cc, "no-wait", "start the job and print its id, without following it")
		cc.Define("interval").String().Flag("interval").Default("2s").Description("how often to ask the server for progress")
	})
	fn := cfg.Command("functions").ShortHelp("call and inspect server-side functions")
	fn.SubCommand("types").ShortHelp("print JSDoc types generated from the functions' schemas [local]").
		LongHelp("Prints JSDoc types generated from every function's input and output JSON\nSchemas, for editor autocomplete in ctx.input and the return value. Needs only\nCONFIG_DIR.").
		Func(cmdutil.Act("backd functions types", project.FunctionsTypes)).Config(func(*kli.CommandConfig) {})
	fnFlag := func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "function", "the function, as <realm>/<database>/<name>")
		cmdutil.Optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
	}
	fn.SubCommand("invoke").ShortHelp("call the function through a running backd [server]").
		LongHelp("Calls the function through a running backd's real HTTP path: the same invoke\nrule, schemas and secrets apply as for any other caller. An internal function has\nno such route: it is run through the admin API instead (as --as, if given). --input reads the\nrequest body from a JSON file (\"-\" for standard input); without it, the body\nis null. --as <email> calls on the user's behalf (needs an admin API key,\nBACKD_API_KEY); without it, calls as whatever credential is active. When the\nserver runs with BACKD_DEV=true, also prints the function's console logs and\nits time inside the executor.").
		Func(cmdutil.Act("backd functions invoke", admin.FunctionsInvoke)).Config(func(cc *kli.CommandConfig) {
		fnFlag(cc)
		cmdutil.Optional(cc, "input", "a JSON file with the request body, or - for standard input")
		cmdutil.Optional(cc, "as", "call on behalf of this user's email (needs an admin API key)")
	})
	fn.SubCommand("history").ShortHelp("print the function's recent calls, newest first [server]").
		LongHelp("Prints time, status, error code, duration, actor and request id of the\nfunction's recent calls (never the input or output). Records expire after the\nrealm's functions.log_retention (default 7d): a short debugging window, not a\nlog archive. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd functions history", admin.FunctionsHistory)).Config(func(cc *kli.CommandConfig) {
		fnFlag(cc)
		cmdutil.Optional(cc, "since", "from this time: RFC 3339, or a duration back from now such as 24h or 7d")
		limit(cc)
		cmdutil.Boolean(cc, "json", "one JSON object per line")
	})
	fn.SubCommand("jobs").ShortHelp("list async and scheduled jobs, newest first [server]").
		LongHelp("Lists the realm's async jobs and cron runs with their state and how they ended\n(never their input or output): for one function with --function, or every\nfunction of a realm with --realm. --status narrows to queued, running or done,\n--scheduled to cron runs; PROGRESS is the step a function reported with ctx.step\n(--job <id> with --realm shows every step of one job). A cron run's id is cron_<database>_<function>_<yyyymmddhhmm>\n(UTC). " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd functions jobs", admin.FunctionsJobs)).Config(func(cc *kli.CommandConfig) {
		cmdutil.Optional(cc, "function", "the function, as <realm>/<database>/<name>")
		cmdutil.Optional(cc, "realm", "list every function's jobs in this realm instead")
		cmdutil.Optional(cc, "job", "show one job (with --realm) with every step it reported, instead of listing")
		cmdutil.Optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		cc.Define("status").String().Flag("status").Default("").OneOf("", "queued", "running", "done").Description("only jobs in this state")
		cmdutil.Boolean(cc, "scheduled", "only cron runs")
		cmdutil.Optional(cc, "since", "from this time: RFC 3339, or a duration back from now such as 24h or 7d")
		limit(cc)
		cmdutil.Boolean(cc, "json", "one JSON object per line")
	})
	scheduleFlags := func(cc *kli.CommandConfig) {
		fnFlag(cc)
		cmdutil.Boolean(cc, "json", "print the schedule as JSON")
	}
	jobFlags := func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "realm", "the realm the job belongs to")
		cmdutil.Required(cc, "job", "the job's id (see `backd functions jobs`)")
		cmdutil.Optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		cmdutil.Boolean(cc, "json", "print the job as JSON")
	}
	fn.SubCommand("cancel").ShortHelp("cancel a queued or running job [server]").
		LongHelp("Ends a queued, retry-waiting or running function job at once: it is done with the\nresult \"cancelled\". A worker that is running it stops the run within a few seconds\n(what the function already did stays done). The job keeps its input, so it can be\nre-run. Refused when it has already finished. Audited as job.cancel. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd functions cancel", admin.FunctionsCancel)).Config(jobFlags)
	fn.SubCommand("rerun").ShortHelp("queue a finished job again [server]").
		LongHelp("Queues a new job for a finished function job (any result, a cancelled one included):\nthe same function, input and caller, as a job of its own (the original keeps its\nresult). Prints the new job's id; follow it with `backd functions jobs`. A worker must\nbe running for it to run. Refused while the job is queued or running. Audited as\njob.rerun. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd functions rerun", admin.FunctionsRerun)).Config(jobFlags)
	fn.SubCommand("schedules").ShortHelp("list the realm's schedules and whether each is paused [server]").
		LongHelp("Lists every scheduled function of a realm: its cron expression, time zone,\noverlap setting and whether an administrator paused it (see `backd functions pause`).\n" + clicore.AdminAPINote).
		Func(cmdutil.Act("backd functions schedules", admin.FunctionsSchedules)).Config(func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "realm", "the realm to list")
		cmdutil.Optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		cmdutil.Boolean(cc, "json", "one JSON object per line")
	})
	fn.SubCommand("pause").ShortHelp("stop a function's schedule creating runs [server]").
		LongHelp("Pauses the function's cron schedule: no run is created until it is resumed. Runs\nalready queued or running are not touched, and the function can still be called by\nhand. The state is stored in the realm's database, so it survives restarts and\napplies to every instance. Pausing a paused schedule changes nothing. Audited as\nschedule.pause. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd functions pause", admin.FunctionsPause)).Config(scheduleFlags)
	fn.SubCommand("resume").ShortHelp("let a paused schedule create runs again [server]").
		LongHelp("Resumes a paused cron schedule. The runs that fell due while it was paused are not\nmade up: the next scheduled time runs. Audited as schedule.resume. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd functions resume", admin.FunctionsResume)).Config(scheduleFlags)
	fn.SubCommand("logs").ShortHelp("print the function's own console output for its recent calls [server]").
		LongHelp("Prints the function's own console output for its recent calls (already masked of\nany declared secrets), oldest first within each call. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd functions logs", admin.FunctionsLogs)).Config(func(cc *kli.CommandConfig) {
		fnFlag(cc)
		cmdutil.Optional(cc, "request-id", "only this call")
		cmdutil.Optional(cc, "since", "from this time: RFC 3339, or a duration back from now such as 24h or 7d")
		limit(cc)
		cmdutil.Boolean(cc, "json", "one JSON object per line")
	})
}

func secretFlags(cc *kli.CommandConfig) {
	clicore.Remote(cc)
	cmdutil.Required(cc, "name", "the secret's name")
	cmdutil.Optional(cc, "database", "the database scope; without it, the realm scope")
	cmdutil.Optional(cc, "org", "the organization scope (an organization id), instead of --database")
}

func limit(cc *kli.CommandConfig) {
	cc.Define("limit").Int64().Flag("limit").Default(50).Min(1).Description("at most this many records")
}

// userAction runs a user subcommand against the realm's admin API.
func userAction(withEmail bool, run func(*admin.UserCtx) error) kli.CommandFunc {
	return func(c *kli.CommandContext) error {
		uc := &admin.UserCtx{Cmd: c, IO: cmdutil.IOOf(c), Local: admin.LocalSettings(cmdutil.Str(c, "realm"), c.Getenv)}
		if withEmail {
			uc.Email = cmdutil.Str(c, "email")
		}
		t, err := clicore.TargetOf(c, false)
		if err != nil {
			return err
		}
		uc.Target = t
		return run(uc)
	}
}

// registerTriggers adds `backd triggers`: what the dispatcher is doing and has not been able to
// do (status, gaps), and a way to re-emit the changes of a collection after a gap (rescan).
func registerTriggers(cfg *kli.Config) {
	tr := cfg.Command("triggers").ShortHelp("see and repair what the trigger dispatcher did")
	remote := func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "realm", "the realm")
		cmdutil.Optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
		cmdutil.Boolean(cc, "json", "one JSON object per line")
	}
	tr.SubCommand("status").ShortHelp("the dispatcher, the realm's triggers, the backlog and the open gaps").
		LongHelp("Shows whether an instance holds the dispatcher's lease and how old its stored position is,\nthe realm's triggers (each runs with admin data access), the unfinished trigger jobs and the\ngaps not yet resolved. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd triggers status", admin.TriggersStatus)).Config(remote)
	tr.SubCommand("gaps").ShortHelp("list what the dispatcher could not turn into jobs").
		LongHelp("Lists the gaps the dispatcher recorded: a stretch of changes that fell out of MongoDB's\nhistory while no dispatcher read them (window), changes whose condition needed an image that\nwas not there (preimage_missing) and events refused because the realm was at its backlog\nceiling (backlog). Open ones by default. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd triggers gaps", admin.TriggersGaps)).Config(func(cc *kli.CommandConfig) {
		remote(cc)
		cmdutil.Boolean(cc, "all", "include the resolved gaps")
	})
	tr.SubCommand("resolve").ShortHelp("mark a gap as dealt with").
		LongHelp("Closes a gap: use it once you have run `backd-server triggers rescan` for it, or decided that\nnothing that matters was lost. Audited as triggers.gap_resolve. " + clicore.AdminAPINote).
		Func(cmdutil.Act("backd triggers resolve", admin.TriggersResolve)).Config(func(cc *kli.CommandConfig) {
		cmdutil.Required(cc, "realm", "the realm")
		cmdutil.Required(cc, "id", "the gap's id (see `backd triggers gaps`)")
		cmdutil.Optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
	})
}
