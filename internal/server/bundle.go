package server

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/fernandezvara/backd/internal/functions"
	"github.com/fernandezvara/backd/internal/registry"
)

// staleFunctions lists, as realm/database, the databases whose functions project has no
// built bundles or bundles older than its source (the manifest's source hash against the
// project's). Databases without functions are never stale.
func staleFunctions(reg *registry.Registry) ([]string, error) {
	var stale []string
	for _, db := range reg.Databases() {
		if db.Functions == nil || len(db.Functions.Functions) == 0 {
			continue
		}
		src, err := registry.SourceHash(db.Functions.Dir)
		if err != nil {
			return nil, err
		}
		if m, err := registry.ReadManifest(db.Functions.Dir); err != nil || m.Source != src {
			stale = append(stale, db.Realm+"/"+db.Name)
		}
	}
	return stale, nil
}

// buildStaleFunctions bundles the functions of reg with deno (the DENO setting; "" finds it
// on PATH) when any project is stale, as `backd-server functions build` would, and does nothing
// when every bundle is fresh. The dev reloader runs it before checking a new config.
func buildStaleFunctions(ctx context.Context, reg *registry.Registry, deno string, log *slog.Logger) error {
	stale, err := staleFunctions(reg)
	if err != nil || len(stale) == 0 {
		return err
	}
	return functions.Build(ctx, reg, functions.Options{
		Deno: deno,
		Log:  func(format string, args ...any) { log.Info(fmt.Sprintf(format, args...)) },
	})
}
