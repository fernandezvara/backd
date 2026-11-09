package server

import (
	"context"
	"fmt"
	"log/slog"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/settings"
	"github.com/fernandezvara/backd/internal/triggers"
)

// Options are the flags of the role commands.
type Options struct {
	// WithWorker runs the worker role in this process too (serve --with-worker).
	WithWorker bool
	// DisablePreImages and Yes are provision's --disable-preimages and --yes.
	DisablePreImages, Yes bool
}

// SetOptions applies the role flags before the role runs.
func (a *App) SetOptions(o Options) {
	a.withWorker, a.disablePreImages, a.yes = o.WithWorker, o.DisablePreImages, o.Yes
}

// NewApp is an app on a config that is already loaded and a connected client, for the tools
// that work on the database directly without the startup checks (Setup does them).
func NewApp(cfg settings.Settings, log *slog.Logger, reg *registry.Registry, client *mongo.Client, fingerprint string) *App {
	return &App{cfg: cfg, log: log, reg: reg, client: client, fingerprint: fingerprint}
}

// Client is the app's MongoDB client.
func (a *App) Client() *mongo.Client { return a.client }

// Registry is the loaded config.
func (a *App) Registry() *registry.Registry { return a.reg }

// Log is the app's logger.
func (a *App) Log() *slog.Logger { return a.log }

// UsersOf returns the user service of an auth-enabled, provisioned realm.
func (a *App) UsersOf(ctx context.Context, realm string) (*auth.Users, error) {
	rl, ok := a.reg.Realms[realm]
	if !ok {
		return nil, fmt.Errorf("realm %q is not configured", realm)
	}
	if !rl.Settings.AuthEnabled {
		return nil, fmt.Errorf("realm %q has auth disabled, so it has no users", realm)
	}
	if err := a.Provisioner().VerifySystem(ctx, realm); err != nil {
		return nil, err
	}
	return &auth.Users{
		Store:    mongodb.NewAuthStore(a.client, realm),
		Hasher:   auth.NewHasher(a.cfg.PasswordHashConcurrency, auth.DefaultArgon2Params),
		Settings: rl.Settings,
		Realm:    realm,
		Log:      a.log,
	}, nil
}

// Rescan reads a collection's stored documents again and queues the trigger jobs their
// changes would have queued (`backd-server triggers rescan`).
func (a *App) Rescan(ctx context.Context, req triggers.RescanRequest) (triggers.RescanResult, error) {
	return triggers.Rescan(ctx, a.reg, req, &mongodb.DocScanner{Client: a.client, Decrypt: func(ctx context.Context, mongoDB, coll string, doc map[string]any) error {
		return a.decryptDocument(ctx, req.Realm, mongoDB, coll, doc)
	}}, jobQueue{a}, func(ctx context.Context, realm string) map[string]any {
		return a.triggerConfig(ctx, a.reg, realm)
	})
}

// Settings are the app's settings. Tests change them (the executor and callback addresses of
// servers they start) before the app serves.
func (a *App) Settings() *settings.Settings { return &a.cfg }
