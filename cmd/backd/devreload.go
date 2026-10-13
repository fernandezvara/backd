package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fernandezvara/backd/internal/functions"
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
	app      *app // what is serving now; replaced on each successful reload
	public   *swapHandler
	internal *swapHandler // nil without an internal listener
	// apply provisions the new config (indexes and the like), like startup does.
	apply func(ctx context.Context, next *app) error
	// build bundles stale function projects of the new config before it is checked.
	build func(ctx context.Context, reg *registry.Registry) error
	// onReload receives every successful reload, in order, from the reload goroutine.
	onReload []func(reloadEvent)

	mu     sync.Mutex
	errors []string
	files  map[string]string // snapshot of CONFIG_DIR at the last attempt
}

func newReloader(a *app, public, internal *swapHandler) *reloader {
	r := &reloader{app: a, public: public, internal: internal}
	r.apply = func(ctx context.Context, next *app) error {
		if err := next.provisioner().Apply(ctx); err != nil {
			return err
		}
		return next.syncRoles(ctx)
	}
	r.build = func(ctx context.Context, reg *registry.Registry) error {
		stale := false
		for _, db := range reg.Databases() {
			if db.Functions == nil || len(db.Functions.Functions) == 0 {
				continue
			}
			src, err := registry.SourceHash(db.Functions.Dir)
			if err != nil {
				return err
			}
			if m, err := registry.ReadManifest(db.Functions.Dir); err != nil || m.Source != src {
				stale = true
			}
		}
		if !stale {
			return nil
		}
		return functions.Build(ctx, reg, functions.Options{
			Deno: a.cfg.Deno,
			Log:  func(format string, args ...any) { a.log.Info(fmt.Sprintf(format, args...)) },
		})
	}
	r.files, _ = snapshot(a.cfg.ConfigDir)
	return r
}

// Errors are why the last reload failed; empty when the running config is the files' config.
func (r *reloader) Errors() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.errors)
}

func (r *reloader) setErrors(errs []string) {
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
		r.app.log.Error("dev mode: the config change was not applied; the previous config keeps running", "error", err)
		r.setErrors(strings.Split(strings.TrimSpace(err.Error()), "\n"))
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
		return nil, fmt.Errorf("invalid config:\n%w", err)
	}
	if err := reg.CheckImageLimits(cur.cfg.ImageMaxPixels); err != nil {
		return nil, fmt.Errorf("invalid config:\n%w", err)
	}
	if err := r.build(ctx, reg); err != nil {
		return nil, err
	}
	if err := checkFunctions(reg, cur.cfg, cur.log); err != nil {
		return nil, err
	}
	fp, err := reg.Fingerprint()
	if err != nil {
		return nil, err
	}
	if fp == cur.fingerprint {
		return nil, nil
	}
	next := *cur
	next.reg, next.fingerprint, next.userServices = reg, fp, nil
	if err := r.apply(ctx, &next); err != nil {
		return nil, fmt.Errorf("applying the new config: %w", err)
	}
	r.public.Store(next.handler())
	if r.internal != nil {
		r.internal.Store(next.internalHandler())
	}
	r.app = &next
	r.writeTypes(&next)
	realms := map[string]bool{}
	for _, f := range changed {
		if i := strings.IndexByte(f, '/'); i > 0 {
			realms[f[:i]] = true
		}
	}
	return &reloadEvent{Fingerprint: fp, Realms: slices.Sorted(mapKeysBool(realms)), Files: changed}, nil
}

// writeTypes regenerates every functions project's backd.d.ts for the config now running.
func (r *reloader) writeTypes(a *app) {
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

func mapKeysBool(m map[string]bool) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
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
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d/%d", fi.Size(), fi.ModTime().UnixNano())))
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
