package cli

import (
	"net/http"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/cmdutil"
)

// An Opener is where the commands that work on a realm's data through its admin API find it.
// The CLI's is a running backd (Remote); backd-server's is that same admin handler run in its
// own process over MongoDB and CONFIG_DIR, with no server (see internal/servercli). Export,
// import and the field key commands are written once and registered in both trees with their
// opener.
type Opener struct {
	Prog  string                      // the program: the commands are named "<Prog> export"
	Flags func(cc *kli.CommandConfig) // the flags that say where the realm is
	Open  func(c *kli.CommandContext) (*Session, error)
	Note  string // how the commands reach the realm, ending their help
}

// A Session is an opened realm.
type Session struct {
	t     *target
	Close func()
	// BeforeWrite, when set, runs before an import writes anything (backd-server refuses
	// while an instance serves the realm).
	BeforeWrite func(c *kli.CommandContext) error
}

// Remote is the CLI's opener: the realm's admin API on a running backd.
var Remote = Opener{
	Prog:  "backd",
	Flags: remote,
	Open: func(c *kli.CommandContext) (*Session, error) {
		t, err := targetOf(c, false)
		if err != nil {
			return nil, err
		}
		return &Session{t: t, Close: func() {}}, nil
	},
	Note: adminAPINote,
}

// InProcess is a session on an admin handler in this process: requests never touch a socket,
// they are handed to h (see inProcess).
func InProcess(realm string, h http.Handler, closeFn func(), beforeWrite func(c *kli.CommandContext) error) *Session {
	return &Session{t: offlineTarget("http://backd.offline", realm, &http.Client{Transport: inProcess{h}}), Close: closeFn, BeforeWrite: beforeWrite}
}

// RegisterFieldKeys adds secret create, rotate and list (the field keys) to sec, through o.
func RegisterFieldKeys(sec *kli.CommandBuilder, o Opener) {
	sec.SubCommand("create").ShortHelp("create a field key: generated and shown once, or imported").
		LongHelp("Creates a key for field encryption (design: a `key` secret). With --generate the server makes 32\nrandom bytes and prints them, base64url, on standard output ONCE, with the fingerprint on standard error:\nsave the key outside backd, because nothing ever shows it again and a lost key means the values encrypted\nwith it are lost. With --value it imports a key you already hold (key:<base64url>, env:VAR or file:PATH),\nfor a restore or a staging copy. Needs a full administrator and a fresh proof that it is you. Audited with the\nname and fingerprint, never the key. " + o.Note).
		Func(cmdutil.Act(o.Prog+" secret create", func(c *kli.CommandContext) error { return secretCreate(c, o) })).Config(func(cc *kli.CommandConfig) {
		o.Flags(cc)
		cmdutil.Required(cc, "name", "the key's name, such as HEALTH")
		cmdutil.Required(cc, "type", "the kind of secret to create: key")
		cmdutil.Boolean(cc, "generate", "make a new random key and show it once")
		cmdutil.Optional(cc, "value", "import this key: key:<base64url>, env:VAR or file:PATH")
		cmdutil.Boolean(cc, "skip-existing", "succeed, changing nothing, when a secret with that name exists (for scripts)")
		cmdutil.Boolean(cc, "json", "print the answer as JSON")
	})
	sec.SubCommand("rotate").ShortHelp("give a field key a new value, without rewriting any document").
		LongHelp("Replaces the value of a field key and wraps every data key it protected again, so nothing in the database\nis rewritten and the change is instant. With --generate the new key is printed once on standard output; with\n--value you bring your own (key:<base64url>, env:VAR or file:PATH). --resume finishes a rotation that stopped\nhalf way: until it finishes the previous value is kept and nothing is lost. Keep your old copy for backups made\nbefore the rotation. Needs a full administrator and a fresh proof. " + o.Note).
		Func(cmdutil.Act(o.Prog+" secret rotate", func(c *kli.CommandContext) error { return secretRotate(c, o) })).Config(func(cc *kli.CommandConfig) {
		o.Flags(cc)
		cmdutil.Required(cc, "name", "the key's name")
		cmdutil.Boolean(cc, "generate", "make a new random key and show it once")
		cmdutil.Optional(cc, "value", "the new key: key:<base64url>, env:VAR or file:PATH")
		cmdutil.Boolean(cc, "resume", "finish a rotation that stopped before it ended")
		cmdutil.Boolean(cc, "json", "print the answer as JSON")
	})
	sec.SubCommand("list").ShortHelp("list the realm's secrets (never their values)").
		Func(cmdutil.Act(o.Prog+" secret list", func(c *kli.CommandContext) error { return secretList(c, o) })).Config(func(cc *kli.CommandConfig) {
		o.Flags(cc)
	})
}

// RegisterTransfer adds export and import to cfg, through o.
func RegisterTransfer(cfg *kli.Config, o Opener) {
	cfg.Command("export").ShortHelp("write a realm, database or collection as a package").
		LongHelp("Reads the realm's documents and files into a package: a directory with manifest.json,\n<database>/<collection>.jsonl (the API representation of every document, soft-deleted ones included, with dates and\nbinary values in Extended JSON) and <database>/_files/<file id>, or one .tar.gz with --archive. The package is lossless;\n`import` reads it. Each part is verified as it arrives and recorded in <out>/.export-progress.json, so running the\nsame command again after an interruption continues from the last complete part. Only one export of a realm runs at a time.\n--fields and --where narrow it to a reporting extract (a --fields package is partial and can't be imported) and --format csv writes one flat, lossy table per collection for spreadsheets, without files. --include auth adds the realm's authentication data, audited as export.auth. --anonymize makes a copy for staging (see its flags); the manifest says so, and import warns about a package that doesn't fit the instance's BACKD_ENV. Never exported: sessions, tokens, counters, jobs, history, secrets and the audit trail. Needs full admin access and a fresh\nproof that it is you. Audited as export and export.done. " + o.Note).
		Func(cmdutil.Act(o.Prog+" export", func(c *kli.CommandContext) error { return exportAction(c, o) })).Config(func(cc *kli.CommandConfig) {
		o.Flags(cc)
		cmdutil.Required(cc, "out", "the package directory to write (with --archive, the archive is <out>.tar.gz)")
		cmdutil.Optional(cc, "db", "only this database")
		cmdutil.Optional(cc, "collection", "only these collections of --db: comma-separated names")
		cmdutil.Optional(cc, "fields", "only these fields: comma-separated paths such as name,address.city (id is always there); the package is marked partial and can't be imported")
		cmdutil.Optional(cc, "where", "only the documents matching this filter (the API's where syntax), for one --collection")
		cmdutil.Optional(cc, "format", "jsonl (a package, the default) or csv (a flat table per collection for spreadsheets: lossy, no files, can't be imported)")
		cmdutil.Optional(cc, "include", "auth: also export the realm's users with their password hashes, organizations, memberships, teams, second factors and push devices (whole realm only); a one-time export key is shown once")
		cmdutil.Boolean(cc, "anonymize", "make a staging copy: each collection's on_owner_delete remove/replace rules are applied to every document, files are left out, and with --include auth emails become user-<id>@example.invalid and password hashes, second factors and devices are dropped")
		cmdutil.Optional(cc, "anonymize-config", "with --anonymize: a YAML file adding or overriding rules per collection (collections: {<database>/<collection>: {remove: [...], replace: {...}}}), checked against the schemas")
		cmdutil.Optional(cc, "staging-password", "with --anonymize --include auth: one password every user gets (env:<VAR>, file:<path> or key:<value>)")
		cmdutil.Optional(cc, "anonymize-files", "with --anonymize: keep, to export the files too (they often hold personal data)")
		cmdutil.Optional(cc, "auth-key-out", "write the export key of --include auth to this file (readable only by you) instead of showing it")
		cmdutil.Optional(cc, "field", "open encrypted fields with their keys: <path>=<key:|env:|file:…> entries separated by ';' (path as health_notes, or main/patients.health_notes); the others stay ciphertext")
		cmdutil.Boolean(cc, "archive", "write one <out>.tar.gz instead of a directory")
		cmdutil.Boolean(cc, "no-files", "leave the files out: only the documents")
	})

	cfg.Command("import").ShortHelp("read an export package into a realm").
		LongHelp("Reads a package made by `export` (--from: its directory or .tar.gz) into the realm. First every checksum of the package\nis verified, then backd validates every document against the target's own configuration (ids, schemas,\nfile limits, unique values, the documents already there) and nothing is written until all of it is known; --dry-run stops\nthere and prints what would happen. Then the files are uploaded and the documents written with their ids and _meta kept, so\nreferences and file links stay valid. --mode create (the default) refuses a different document with the same id, skip leaves\nexisting documents alone and replace overwrites them. Invalid documents stop the import unless --skip-invalid, which writes\nthem to --report (default import-report.json). Running the same command again after an interruption finishes the job: what\nwas written is found identical and left alone. Partial packages and CSV extracts are refused. Needs full admin access and a\nfresh proof that it is you. Audited as import and import.done. " + o.Note).
		Func(cmdutil.Act(o.Prog+" import", func(c *kli.CommandContext) error { return importAction(c, o) })).Config(func(cc *kli.CommandConfig) {
		o.Flags(cc)
		cmdutil.Required(cc, "from", "the package: its directory, or the .tar.gz that export --archive wrote")
		cc.Define("mode").String().Flag("mode").Default("create").Description("create (fail on an existing id), skip (keep existing documents) or replace (overwrite them)")
		cmdutil.Boolean(cc, "dry-run", "validate everything and write nothing")
		cmdutil.Boolean(cc, "skip-invalid", "leave out the documents that are invalid for the target instead of stopping")
		cmdutil.Optional(cc, "report", "write the problems found to this JSON file")
		cmdutil.Optional(cc, "auth-key", "the export key of a package exported with --include auth: key:<value>, env:<VAR> or file:<path>; without it users arrive without second factors and push devices")
		cmdutil.Optional(cc, "field", "keys for encrypted fields the package holds as ciphertext: <path>=<key:|env:|file:…> entries separated by ';'; needed when the target does not hold the same key (the dry run lists which fields need which)")
	})
}
