package servercli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/cli/admin"
	"github.com/fernandezvara/backd/internal/cmdutil"
	"github.com/fernandezvara/backd/internal/httpapi"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/server"
	"github.com/fernandezvara/backd/internal/settings"
)

// The commands that work on a realm's data (export, import, the field keys) run here the same
// admin API the CLI reaches over the network, in this process, over MongoDB and CONFIG_DIR: no
// server is needed. The requests never touch a socket: they are handed to the handler, which
// takes each of them as a full administrator's working offline (and says so in the records it
// writes).

// direct is the opener of those commands (admin.RegisterTransfer, admin.RegisterFieldKeys).
var direct = admin.Opener{
	Prog:  "backd-server",
	Flags: directFlags,
	Open:  openDirect,
	Note:  "Works straight on MongoDB and CONFIG_DIR, with no running backd (the realm needs auth enabled): how you create the key a field names when backd refuses to start without it, restore keys after losing the secrets, or move a realm's data while no instance serves it.",
}

// directFlags say which realm, and where its data and keys are.
func directFlags(cc *kli.CommandConfig) {
	cmdutil.Required(cc, "realm", "the realm")
	cmdutil.Optional(cc, "mongo-uri", "the MongoDB URI, or env:<VAR> / file:<path> to read it from (default: BACKD_MONGO_URI, then MONGO_URI)")
	cmdutil.Optional(cc, "master-key", "the secrets master key (env:<VAR> / file:<path>): needed for files kept with the realm's storage secrets and for authentication data sealed by the realm; default BACKD_SECRETS_KEY")
	cmdutil.Optional(cc, "storage-key", "the storage access key (env:<VAR> / file:<path>) instead of the realm's secrets")
	cmdutil.Optional(cc, "storage-secret", "the storage secret key (env:<VAR> / file:<path>) instead of the realm's secrets")
	cmdutil.Optional(cc, "public-storage-key", "the public storage's access key (env:<VAR> / file:<path>) instead of the realm's secrets")
	cmdutil.Optional(cc, "public-storage-secret", "the public storage's secret key (env:<VAR> / file:<path>) instead of the realm's secrets")
	cmdutil.Boolean(cc, "force", "import: write even while a backd instance serves the realm")
}

// valueOrSource reads a flag that holds a value or key:/env:/file:, returning "" when unset.
func valueOrSource(c *kli.CommandContext, name string) (string, error) {
	spec := cmdutil.Str(c, name)
	if spec == "" {
		return "", nil
	}
	v, err := cmdutil.ReadKeySource(spec, c.Getenv, c.Stderr())
	if err != nil {
		return "", cmdutil.UsageErr(fmt.Errorf("--%s: %w", name, err))
	}
	return v, nil
}

// openDirect connects to MongoDB and loads CONFIG_DIR for the realm named by --realm.
func openDirect(c *kli.CommandContext) (*admin.Session, error) {
	realm := cmdutil.Str(c, "realm")
	uri := ""
	if spec := cmdutil.Str(c, "mongo-uri"); spec != "" {
		if strings.HasPrefix(spec, "mongodb://") || strings.HasPrefix(spec, "mongodb+srv://") {
			uri = spec
		} else {
			v, err := valueOrSource(c, "mongo-uri")
			if err != nil {
				return nil, err
			}
			uri = v
		}
	}
	masterKey, err := valueOrSource(c, "master-key")
	if err != nil {
		return nil, err
	}
	storageKey, err := valueOrSource(c, "storage-key")
	if err != nil {
		return nil, err
	}
	storageSecret, err := valueOrSource(c, "storage-secret")
	if err != nil {
		return nil, err
	}
	if (storageKey == "") != (storageSecret == "") {
		return nil, cmdutil.UsageErr(errors.New("--storage-key and --storage-secret go together"))
	}
	publicKey, err := valueOrSource(c, "public-storage-key")
	if err != nil {
		return nil, err
	}
	publicSecret, err := valueOrSource(c, "public-storage-secret")
	if err != nil {
		return nil, err
	}
	if (publicKey == "") != (publicSecret == "") {
		return nil, cmdutil.UsageErr(errors.New("--public-storage-key and --public-storage-secret go together"))
	}
	env := func(k string) string {
		switch k {
		case "MONGO_URI":
			if uri != "" {
				return uri
			}
			return firstSet(c.Getenv("BACKD_MONGO_URI"), c.Getenv("MONGO_URI"))
		case "BACKD_SECRETS_KEY":
			if masterKey != "" {
				return masterKey
			}
		case "LOG_LEVEL":
			return "warn"
		}
		return c.Getenv(k)
	}
	ctx := context.Background()
	cfg, err := settings.Load(env)
	if err != nil {
		return nil, err
	}
	log := slog.New(slog.NewJSONHandler(c.Stderr(), &slog.HandlerOptions{Level: cfg.LogLevel}))
	// The functions' bundles are not needed to move data: the config is read, not built.
	reg, err := registry.Load(cfg.ConfigDir)
	if err != nil {
		return nil, fmt.Errorf("invalid config:\n%w", err)
	}
	fingerprint, err := reg.Fingerprint()
	if err != nil {
		return nil, err
	}
	rl := reg.Realms[realm]
	if rl == nil {
		return nil, fmt.Errorf("CONFIG_DIR (%s) has no realm %q", cfg.ConfigDir, realm)
	}
	if !rl.Settings.AuthEnabled {
		return nil, fmt.Errorf("realm %q has auth disabled, and the export and import tools work through the admin API of a realm with auth enabled", realm)
	}
	client, err := mongodb.Connect(ctx, cfg.MongoURI)
	if err != nil {
		return nil, err
	}
	a := server.NewApp(cfg, log, reg, client, fingerprint)
	hc := a.HandlerConfig()
	hc.Offline = true
	hc.StorageAccessKey, hc.StorageSecretKey = storageKey, storageSecret
	hc.PublicStorageAccessKey, hc.PublicStorageSecretKey = publicKey, publicSecret
	h := httpapi.NewHandler(hc)
	return admin.InProcess(realm, h, a.Close, func(c *kli.CommandContext) error {
		return refuseIfServed(context.Background(), a, realm, cmdutil.Flag(c, "force"), c.Stderr())
	}), nil
}

func firstSet(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// refuseIfServed fails while a backd instance that serves the realm is alive, unless force.
func refuseIfServed(ctx context.Context, a *server.App, realm string, force bool, stderr io.Writer) error {
	ids, err := mongodb.ActiveInstances(ctx, a.Client(), realm, time.Now())
	if err != nil {
		return fmt.Errorf("could not check whether the realm is served: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	if force {
		fmt.Fprintf(stderr, "warning: %d running backd instance(s) serve realm %q; --force writes anyway\n", len(ids), realm)
		return nil
	}
	return fmt.Errorf("realm %q is served by %d running backd instance(s): stop them (or --force, if you know they are not writing) before importing offline", realm, len(ids))
}
