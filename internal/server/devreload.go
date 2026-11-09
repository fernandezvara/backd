package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fernandezvara/backd/internal/devreport"
	"github.com/fernandezvara/backd/internal/functions"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/registry"
)

// swapHandler serves whatever handler was stored last: a request in flight finishes on the
// handler it started with, new ones get the new one.
type swapHandler struct{ h atomic.Pointer[http.Handler] }

func (s *swapHandler) Store(h http.Handler) { s.h.Store(&h) }

func (s *swapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	(*s.h.Load()).ServeHTTP(w, r)
}

// servedBy is the handler a listener serves: the reloader's swappable one in dev mode (swap is
// not nil), else the handler itself. A nil *swapHandler must never be wrapped in the interface.
func servedBy(h http.Handler, swap *swapHandler) http.Handler {
	if swap != nil {
		return swap
	}
	return h
}

// reloadEvent describes one successful live reload.
type reloadEvent struct {
	Fingerprint string
	Realms      []string // realms with a changed file, sorted
	Files       []string // changed files relative to CONFIG_DIR, sorted
}

// reloader follows CONFIG_DIR in dev mode (BACKD_DEV): when a file changes it loads and
// validates the config exactly as startup does, and only then swaps the handlers. On any
// failure the previous config keeps serving and the reason is kept for the admin warnings.
type reloader struct {
	app      *App // what is serving now; replaced on each successful reload
	public   *swapHandler
	internal *swapHandler // nil without an internal listener
	// apply provisions the new config (indexes and the like), like startup does.
	apply func(ctx context.Context, next *App) error
	// build bundles stale function projects of the new config before it is checked.
	build func(ctx context.Context, reg *registry.Registry) error
	// onReload receives every successful reload, in order, from the reload goroutine.
	onReload []func(reloadEvent)

	mu     sync.Mutex
	errors []devreport.Problem
	files  map[string]string // snapshot of CONFIG_DIR at the last attempt
}

func newReloader(a *App, public, internal *swapHandler) *reloader {
	r := &reloader{app: a, public: public, internal: internal}
	r.apply = func(ctx context.Context, next *App) error {
		if err := next.Provisioner().Apply(ctx); err != nil {
			return err
		}
		return next.syncRoles(ctx)
	}
	r.build = func(ctx context.Context, reg *registry.Registry) error {
		return buildStaleFunctions(ctx, reg, a.cfg.Deno, a.log)
	}
	r.files, _ = snapshot(a.cfg.ConfigDir)
	return r
}

// Errors are why the last reload failed; empty when the running config is the files' config.
func (r *reloader) Errors() []devreport.Problem {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.errors)
}

func (r *reloader) setErrors(errs []devreport.Problem) {
	r.mu.Lock()
	r.errors = errs
	r.mu.Unlock()
}

// Watch checks the files every interval until ctx is done.
func (r *reloader) Watch(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		r.Poll(ctx)
	}
}

// Poll reloads once if the files changed since the last attempt. A failed attempt is not
// retried until a file changes again.
func (r *reloader) Poll(ctx context.Context) {
	now, err := snapshot(r.app.cfg.ConfigDir)
	if err != nil {
		return
	}
	changed := diffSnapshots(r.files, now)
	if len(changed) == 0 {
		return
	}
	r.files = now
	ev, err := r.reload(ctx, changed)
	if err != nil {
		problems := devreport.Of(err, r.app.cfg.ConfigDir)
		devreport.Log(r.app.log, "dev mode: the config change was not applied; the previous config keeps running", problems, nil)
		r.setErrors(problems)
		return
	}
	r.setErrors(nil)
	if ev == nil {
		return // changed files, same config (a comment, a stray file)
	}
	r.app.log.Info("dev mode: config reloaded", "fingerprint", ev.Fingerprint, "realms", ev.Realms, "files", ev.Files)
	for _, f := range r.onReload {
		f(*ev)
	}
}

func (r *reloader) reload(ctx context.Context, changed []string) (*reloadEvent, error) {
	cur := r.app
	reg, err := registry.Load(cur.cfg.ConfigDir)
	if err != nil {
		return nil, fmt.Errorf(devreport.ConfigPrefix+"%w", err)
	}
	if err := r.build(ctx, reg); err != nil {
		return nil, devreport.BuildError{Err: err}
	}
	fp, err := checkLoaded(reg, cur.cfg, cur.log)
	if err != nil {
		return nil, err
	}
	if fp == cur.fingerprint {
		return nil, nil
	}
	next := *cur
	next.reg, next.fingerprint, next.userServices, next.rings = reg, fp, nil, nil
	if err := r.apply(ctx, &next); err != nil {
		return nil, fmt.Errorf("applying the new config: %w", err)
	}
	if next.events != nil {
		next.events.SetWatched(mongodb.RealtimeWatchSet(reg))
	}
	if next.hub != nil {
		// Subscriptions made under the old configuration end with a reset: the client
		// subscribes again under the new one.
		next.hub.ConfigChanged(registry.RealtimeChanged(cur.reg, reg))
	}
	if next.triggers != nil {
		next.triggers.SetRegistry(reg)
	}
	r.public.Store(next.Handler())
	if r.internal != nil {
		r.internal.Store(next.InternalHandler())
	}
	r.app = &next
	r.writeTypes(&next)
	realms := map[string]bool{}
	for _, f := range changed {
		if i := strings.IndexByte(f, '/'); i > 0 {
			realms[f[:i]] = true
		}
	}
	return &reloadEvent{Fingerprint: fp, Realms: slices.Sorted(maps.Keys(realms)), Files: changed}, nil
}

// writeTypes regenerates every functions project's backd.d.ts for the config now running.
func (r *reloader) writeTypes(a *App) {
	changed, warnings, err := functions.WriteTypes(a.reg, "", a.fingerprint, false)
	if err != nil {
		a.log.Warn("dev mode: writing function types failed", "error", err)
		return
	}
	for _, w := range warnings {
		a.log.Warn("function types: " + w)
	}
	for _, p := range changed {
		a.log.Info("dev mode: function types updated", "file", p)
	}
}

// snapshot lists CONFIG_DIR's files with their size and modification time, except hidden
// entries (.git, .build) and node_modules.
func snapshot(root string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // a file removed while walking
		}
		if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || d.Name() == registry.TypesFile {
			return nil // generated by this process: not a change
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		sum := sha256.Sum256(fmt.Appendf(nil, "%d/%d", fi.Size(), fi.ModTime().UnixNano()))
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:8])
		return nil
	})
	if _, serr := os.Stat(root); serr != nil {
		return nil, serr
	}
	return out, err
}

// diffSnapshots lists the files added, removed or modified between two snapshots, sorted.
func diffSnapshots(before, after map[string]string) []string {
	var out []string
	for f, v := range after {
		if before[f] != v {
			out = append(out, f)
		}
	}
	for f := range before {
		if _, ok := after[f]; !ok {
			out = append(out, f)
		}
	}
	slices.Sort(out)
	return out
}

// followConfig is the worker's counterpart of the reloader in dev mode: it returns the app for the
// new config once CONFIG_DIR has changed and the change loads and validates, or nil when ctx is
// done. It keeps trying while the new config doesn't load: the API builds the functions' bundles
// after a change, and until it has, the config is stale. Each distinct reason is logged once.
func followConfig(ctx context.Context, cur *App, interval time.Duration) *App {
	files, _ := snapshot(cur.cfg.ConfigDir)
	pending := false
	seen := map[string]bool{}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		now, err := snapshot(cur.cfg.ConfigDir)
		if err != nil {
			continue
		}
		if len(diffSnapshots(files, now)) > 0 {
			files, pending = now, true
		}
		if !pending {
			continue
		}
		reg, fp, err := loadConfig(cur.cfg, cur.log)
		if err != nil {
			devreport.Log(cur.log, "dev mode: the config change is not applied yet; the worker keeps running the previous config (it tries again until it loads)", devreport.Of(err, cur.cfg.ConfigDir), seen)
			continue
		}
		pending = false
		clear(seen)
		if fp == cur.fingerprint {
			continue // changed files, same config (a comment, a stray file)
		}
		cur.log.Info("dev mode: worker config reloaded", "fingerprint", fp)
		next := *cur
		next.reg, next.fingerprint, next.userServices, next.rings = reg, fp, nil, nil
		return &next
	}
}
