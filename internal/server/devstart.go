package server

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/fernandezvara/backd/internal/adminui"
	"github.com/fernandezvara/backd/internal/devreport"
	"github.com/fernandezvara/backd/internal/httpapi"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/settings"
	"github.com/fernandezvara/backd/internal/typegen"
)

// Run runs a server role (serve, worker or provision) with its flags. Outside dev mode, or
// for provision, any startup error is final. In dev mode (BACKD_DEV), serve and worker don't
// exit on what the developer edits (see runDev).
func Run(ctx context.Context, role string, getenv func(string) string, logOut io.Writer, o Options) error {
	run, ok := roles[role]
	if !ok {
		return fmt.Errorf("unknown role %q", role)
	}
	cfg, err := settings.Load(getenv)
	if err != nil {
		return err
	}
	var a *App
	if cfg.Dev && (role == "serve" || role == "worker") {
		a, err = startDev(ctx, role, cfg, logOut, o)
		if a == nil {
			return err // an error that exits, or a stop while waiting (nil)
		}
	} else if a, err = setupWith(ctx, cfg, logOut); err != nil {
		return err
	}
	defer a.Close()
	a.SetOptions(o)
	return run(ctx, a)
}

var roles = map[string]func(context.Context, *App) error{"serve": Serve, "worker": Worker, "provision": Provision}

// devPoll is how often a dev-mode start that is waiting looks at CONFIG_DIR again.
var devPoll = time.Second

// startDev is the dev-mode start of serve and worker: it builds stale function bundles (serve
// only: two processes never build the same project), and when the config, its rules or a
// build fails it doesn't exit. It logs what failed, one record per problem, and waits for
// CONFIG_DIR to change; the first change that loads is the config it starts with. While it
// waits, serve answers /healthz and nothing else (/readyz is not ready). What the developer
// doesn't edit still exits: the settings, an address dev mode refuses, MongoDB, the port, and
// no Deno to build with. It returns nil without an error when ctx ends while waiting.
func startDev(ctx context.Context, role string, cfg settings.Settings, logOut io.Writer, o Options) (*App, error) {
	log := newLogger(cfg, logOut)
	serve := role == "serve"
	if err := devPreflight(cfg, role, o); err != nil {
		return nil, err
	}
	if serve {
		warning, err := devDeno(cfg)
		if err != nil {
			return nil, err
		}
		if warning != "" {
			log.Warn(warning)
		}
	}
	client, m, err := connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	load := func() (*registry.Registry, string, error) { return devLoad(ctx, cfg, log, serve) }
	reg, fp, err := load()
	if err != nil {
		report := &problemBoard{}
		report.set(devreport.Of(err, cfg.ConfigDir))
		var waiting *http.Server
		if serve {
			ln, lerr := net.Listen("tcp", cfg.HTTPAddr)
			if lerr != nil {
				_ = client.Disconnect(context.Background())
				return nil, lerr
			}
			var ui http.Handler
			if cfg.AdminUI {
				ui = adminui.Handler(cfg.AdminUIIdle)
			}
			h := httpapi.NewWaitingHandler(httpapi.WaitingConfig{Problems: report.get, UI: ui, Links: devLinks(cfg.DevLinks), Log: log})
			waiting = &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
			go func() { _ = waiting.Serve(ln) }()
		}
		sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		reg, fp, err = waitForConfig(sigCtx, cfg.ConfigDir, log, report, load, !serve)
		stop()
		if waiting != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = waiting.Shutdown(shutdownCtx)
			cancel()
		}
		if err != nil {
			_ = client.Disconnect(context.Background())
			return nil, nil
		}
	}
	return newApp(cfg, log, reg, fp, client, m), nil
}

// devPreflight is what exits in dev mode before anything waits.
func devPreflight(cfg settings.Settings, role string, o Options) error {
	if role == "worker" {
		if cfg.ExecutorURL == "" {
			return errors.New("the worker role requires BACKD_EXECUTOR_URL")
		}
		return nil
	}
	if !isLoopbackAddr(cfg.HTTPAddr) && !cfg.DevAnyAddr {
		return devAddrError(cfg.HTTPAddr)
	}
	if o.WithWorker && cfg.ExecutorURL == "" {
		return errors.New("--with-worker requires BACKD_EXECUTOR_URL")
	}
	return nil
}

// devDeno is what serve does in dev mode without Deno. With no function, or every bundle
// fresh (built elsewhere, as a stack with its own build step does), it starts and says that a
// changed function can't be built again (warning); with a bundle to build, it exits.
func devDeno(cfg settings.Settings) (warning string, err error) {
	if !hasFunctionDirs(cfg.ConfigDir) {
		return "", nil
	}
	deno := cmp.Or(cfg.Deno, "deno")
	if _, err := exec.LookPath(deno); err == nil {
		return "", nil
	}
	if reg, err := registry.Load(cfg.ConfigDir); err == nil {
		if stale, err := staleFunctions(reg); err == nil && len(stale) == 0 {
			return fmt.Sprintf("BACKD_DEV=true without Deno (%s was not found, DENO): the functions' bundles are used as built, but a changed function can't be built again; run dev mode on the backd-executor image (ghcr.io/fernandezvara/backd-executor) to build on change", deno), nil
		}
	}
	return "", fmt.Errorf("BACKD_DEV=true builds the functions at startup, but %s was not found (DENO): run dev mode on the backd-executor image (ghcr.io/fernandezvara/backd-executor), which carries the Deno backd bundles with", deno)
}

func devAddrError(addr string) error {
	return fmt.Errorf("BACKD_DEV=true requires HTTP_ADDR bound to localhost (e.g. 127.0.0.1:8080), got %q: dev mode must never be reachable off the machine (a container whose ports are published on 127.0.0.1 only can set BACKD_DEV_ANY_ADDR=true)", addr)
}

// hasFunctionDirs tells whether CONFIG_DIR holds any function or migration, without loading it (it may
// not load).
func hasFunctionDirs(dir string) bool {
	m, _ := filepath.Glob(filepath.Join(dir, "*", "*", "_functions", "*", "function.yaml"))
	t, _ := filepath.Glob(filepath.Join(dir, "*", "*", "*", "schema.v*.ts")) // a collection's migrations are built with Deno too
	return len(m)+len(t) > 0
}

// devLoad loads and checks the config as startup does, building stale bundles first when
// build is set.
func devLoad(ctx context.Context, cfg settings.Settings, log *slog.Logger, build bool) (*registry.Registry, string, error) {
	reg, err := registry.Load(cfg.ConfigDir)
	if err != nil {
		return nil, "", fmt.Errorf(devreport.ConfigPrefix+"%w", err)
	}
	if build {
		if err := buildStaleFunctions(ctx, reg, cfg.Deno, log); err != nil {
			return nil, "", devreport.BuildError{Err: err}
		}
	}
	for _, w := range reg.AggregateWarnings() {
		log.Warn(w)
	}
	writeDevSchemaFiles(reg, log)
	logCompatibility(reg, log)
	fp, err := checkLoaded(reg, cfg, log)
	if err != nil {
		return nil, "", err
	}
	return reg, fp, nil
}

// waitForConfig logs why the config didn't load and tries again when CONFIG_DIR changes,
// until it loads or ctx ends. With retry it also tries again on every tick without a change:
// the worker waits for bundles the API builds (in hidden directories, not in the snapshot).
func waitForConfig(ctx context.Context, dir string, log *slog.Logger, report *problemBoard, load func() (*registry.Registry, string, error), retry bool) (*registry.Registry, string, error) {
	const msg = "dev mode: the config does not load; backd waits and loads it again when CONFIG_DIR changes"
	seen := map[string]bool{}
	devreport.Log(log, msg, report.get(), seen)
	files, _ := snapshot(dir)
	t := time.NewTicker(devPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-t.C:
		}
		now, err := snapshot(dir)
		if err != nil {
			continue
		}
		changed := len(diffSnapshots(files, now)) > 0
		if !changed && !retry {
			continue
		}
		files = now
		reg, fp, err := load()
		if err == nil {
			log.Info("dev mode: the config loads now")
			return reg, fp, nil
		}
		if changed {
			seen = map[string]bool{} // a new attempt: report what is still wrong again
		}
		report.set(devreport.Of(err, dir))
		devreport.Log(log, msg, report.get(), seen)
	}
}

// problemBoard holds the report of a dev-mode start that is waiting: the waiting handler
// reads it on every request while waitForConfig replaces it.
type problemBoard struct {
	mu       sync.Mutex
	problems []devreport.Problem
}

func (b *problemBoard) set(ps []devreport.Problem) {
	b.mu.Lock()
	b.problems = ps
	b.mu.Unlock()
}

func (b *problemBoard) get() []devreport.Problem {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.problems)
}

// writeDevSchemaFiles keeps the schema.d.ts of every versioned collection up to date while the
// dev server runs, so the migrations' types follow the schema files (a read-only config is left alone).
func writeDevSchemaFiles(reg *registry.Registry, log *slog.Logger) {
	for _, realm := range reg.SortedRealms() {
		written, warnings, err := typegen.WriteSchemaFiles(reg, realm.Name, "")
		for _, w := range warnings {
			log.Warn(w)
		}
		if err != nil {
			log.Warn("dev mode: schema.d.ts could not be written", "realm", realm.Name, "error", err)
		}
		for _, p := range written {
			log.Info("dev mode: schema types written", "file", p)
		}
	}
}

// logCompatibility warns about what each versioned schema breaks for the version below it, at
// every load of the dev server (a build that was fresh does not run the check itself).
func logCompatibility(reg *registry.Registry, log *slog.Logger) {
	findings, err := reg.CompatibilityFindings()
	if err != nil {
		log.Warn("dev mode: the schema compatibility check could not run", "error", err)
		return
	}
	for _, f := range findings {
		log.Warn(f.String())
	}
}
