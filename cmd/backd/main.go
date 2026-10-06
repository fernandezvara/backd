// Command backd serves a config-driven REST API over MongoDB.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/fernandezvara/cli"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/fernandezvara/backd/internal/adminui"
	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/executor"
	"github.com/fernandezvara/backd/internal/functions"
	"github.com/fernandezvara/backd/internal/httpapi"
	"github.com/fernandezvara/backd/internal/metrics"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/rules"
	"github.com/fernandezvara/backd/internal/settings"
	"github.com/fernandezvara/backd/internal/templates"
)

// version is set at build time (-ldflags "-X main.version=v1.2.3") by
// releases and the Dockerfile; "dev" for local builds.
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr))
}

// run executes one backd command line and returns the process exit code.
func run(args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		args = []string{"version"}
	}
	return execute(newCLI(getenv, stdin, stdout, stderr), args, stderr)
}

// role runs one of backd's server roles: it loads the settings, the config
// and the MongoDB client, then hands the app to action.
func role(name string, action func(context.Context, *app) error) cli.CommandFunc {
	return act(name, func(c *cli.CommandContext) error {
		ctx := context.Background()
		a, err := setup(ctx, c.Getenv, c.Stderr())
		if err != nil {
			return err
		}
		defer a.close()
		a.withWorker = name == "serve" && flag(c, "with-worker")
		return action(ctx, a)
	})
}

// app holds what every command needs: settings, logger, registry, MongoDB client.
type app struct {
	cfg    settings.Settings
	log    *slog.Logger
	reg    *registry.Registry
	client *mongo.Client

	userServices func(string) *auth.Users // built once by realmUsers
	fingerprint  string                   // of CONFIG_DIR (registry.Fingerprint)
	// withWorker runs the worker role in this process too (serve --with-worker).
	withWorker bool
	// metrics records what this process does; nil when METRICS_ADDR is unset.
	metrics *metrics.Metrics
}

func setup(ctx context.Context, getenv func(string) string, logOut io.Writer) (*app, error) {
	cfg, err := settings.Load(getenv)
	if err != nil {
		return nil, err
	}
	log := slog.New(slog.NewJSONHandler(logOut, &slog.HandlerOptions{Level: cfg.LogLevel}))

	reg, err := registry.Load(cfg.ConfigDir)
	if err != nil {
		return nil, fmt.Errorf("invalid config:\n%w", err)
	}
	if err := checkFunctions(reg, cfg, log); err != nil {
		return nil, err
	}
	fingerprint, err := reg.Fingerprint()
	if err != nil {
		return nil, err
	}
	log.Info("config loaded", "version", version, "config_dir", cfg.ConfigDir, "fingerprint", fingerprint, "realms", len(reg.Realms), "databases", len(reg.Databases()))

	var m *metrics.Metrics
	if cfg.MetricsAddr != "" {
		m = metrics.New(version, commit())
	}
	client, err := mongodb.Connect(ctx, cfg.MongoURI, m.MongoMonitor())
	if err != nil {
		return nil, err
	}
	return &app{cfg: cfg, log: log, reg: reg, client: client, fingerprint: fingerprint, metrics: m}, nil
}

func (a *app) close() { _ = a.client.Disconnect(context.Background()) }

func (a *app) provisioner() *mongodb.Provisioner {
	return &mongodb.Provisioner{Client: a.client, Registry: a.reg, Log: a.log, Fingerprint: a.fingerprint, Version: version}
}

func provision(ctx context.Context, a *app) error {
	if err := a.provisioner().Apply(ctx); err != nil {
		return err
	}
	if err := a.syncRoles(ctx); err != nil {
		return err
	}
	a.log.Info("provisioning complete")
	return nil
}

// devWatchInterval is how often BACKD_DEV checks function sources for changes.
const devWatchInterval = time.Second

// isLoopbackAddr reports whether addr's host is localhost or a loopback
// IP, so BACKD_DEV can refuse to bind anywhere reachable off the machine.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func serve(ctx context.Context, a *app) error {
	if a.cfg.Dev && !isLoopbackAddr(a.cfg.HTTPAddr) {
		if !a.cfg.DevAnyAddr {
			return fmt.Errorf("BACKD_DEV=true requires HTTP_ADDR bound to localhost (e.g. 127.0.0.1:8080), got %q: dev mode must never be reachable off the machine (a container whose ports are published on 127.0.0.1 only can set BACKD_DEV_ANY_ADDR=true)", a.cfg.HTTPAddr)
		}
		a.log.Warn("BACKD_DEV_ANY_ADDR=true: dev mode on an address that isn't localhost; make sure nothing but this machine can reach it", "addr", a.cfg.HTTPAddr)
	}
	if a.cfg.DisableAdminAPI {
		a.log.Info("BACKD_ADMIN_API=false: this instance serves no admin API; use the instance that does (the CLI's BACKD_URL, the admin UI)")
	}
	if a.cfg.AdminUI && adminui.Handler(a.cfg.AdminUIIdle) == nil {
		a.log.Warn("BACKD_ADMIN_UI=true, but this build has no admin UI assets: nothing is served at /_ui/ (build ui/admin before the Go build)")
	}
	if a.withWorker && a.cfg.ExecutorURL == "" {
		return errors.New("--with-worker requires BACKD_EXECUTOR_URL")
	}
	p := a.provisioner()
	var err error
	if a.cfg.ProvisionMode == settings.ProvisionVerify {
		err = p.Verify(ctx)
	} else {
		err = p.Apply(ctx)
	}
	if err != nil {
		return err
	}

	if err := a.syncRoles(ctx); err != nil {
		return err
	}
	a.logSecurityWarnings()
	a.logStorageSecrets(ctx, a.realmUsers())
	if err := a.logAPIKeyExpiry(ctx, a.realmUsers(), time.Now()); err != nil {
		return err
	}

	a.checkExecutor(ctx)
	if err := a.checkSecrets(ctx); err != nil {
		return err
	}

	srv := &http.Server{Handler: a.handler()}
	httpapi.ServerTimeouts(srv, a.cfg.MongoOpTimeout)

	ln, err := net.Listen("tcp", a.cfg.HTTPAddr)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if a.cfg.Dev && a.hasFunctions() {
		a.log.Warn("BACKD_DEV=true: function sources rebuild automatically on change; schemas, rules and realm.yaml still need a restart — local development only, never enable this in production")
		go functions.Watch(ctx, a.reg, functions.Options{
			Deno: a.cfg.Deno,
			Log:  func(format string, args ...any) { a.log.Info(fmt.Sprintf(format, args...)) },
		}, devWatchInterval)
	}
	needsInternal := a.hasFunctions() && a.cfg.ExecutorURL != ""
	if !needsInternal && !a.withWorker && a.metrics == nil {
		return httpapi.Run(ctx, srv, ln, a.cfg.ShutdownTimeout, a.log)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	tasks := []func() error{
		func() error { return httpapi.Run(ctx, srv, ln, a.cfg.ShutdownTimeout, a.log) },
	}
	if a.metrics != nil {
		go a.metrics.WatchMongo(ctx, func(c context.Context) error { return a.client.Ping(c, nil) }, 15*time.Second)
		go a.metrics.WatchJobs(ctx, 15*time.Second, a.jobStats)
		tasks = append(tasks, func() error {
			return a.metrics.Serve(ctx, a.cfg.MetricsAddr, a.cfg.MetricsToken, a.cfg.ShutdownTimeout, a.log.With("listener", "metrics"))
		})
	}
	if needsInternal {
		// The internal listener, for the executor and functions calling back.
		internal := &http.Server{Handler: a.internalHandler()}
		httpapi.ServerTimeouts(internal, a.cfg.MongoOpTimeout)
		iln, err := net.Listen("tcp", a.cfg.InternalAddr)
		if err != nil {
			ln.Close()
			return fmt.Errorf("internal listener: %w", err)
		}
		tasks = append(tasks, func() error {
			return httpapi.Run(ctx, internal, iln, a.cfg.ShutdownTimeout, a.log.With("listener", "internal"))
		})
	}
	if !a.withWorker {
		for _, realm := range a.reg.FunctionRealms(true) {
			for dbName, db := range a.reg.Realms[realm].Databases {
				if db.Functions == nil {
					continue
				}
				for name, fn := range db.Functions.Functions {
					if fn.Schedule != nil {
						a.log.Info("scheduled function runs only while a worker is running (backd worker, or serve --with-worker)", "function", realm+"/"+dbName+"/"+name, "schedule", fn.ScheduleExpr)
					}
				}
			}
		}
	}
	if a.withWorker {
		w := httpapi.NewWorker(a.handlerConfig(), workerID())
		a.log.Info("worker ready", "concurrency", a.cfg.WorkerConcurrency)
		tasks = append(tasks, func() error { w.Run(ctx, a.cfg.WorkerConcurrency); return nil })
	}
	errc := make(chan error, len(tasks))
	for _, t := range tasks {
		go func(t func() error) {
			err := t()
			cancel() // one ending stops the others
			errc <- err
		}(t)
	}
	var errs []error
	for range tasks {
		errs = append(errs, <-errc)
	}
	return errors.Join(errs...)
}

// worker runs `backd worker`: the same async-job loop as
// `serve --with-worker`, in its own process with no HTTP listener.
func worker(ctx context.Context, a *app) error {
	if a.cfg.ExecutorURL == "" {
		return errors.New("the worker role requires BACKD_EXECUTOR_URL")
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	w := httpapi.NewWorker(a.handlerConfig(), workerID())
	a.log.Info("worker ready", "version", version, "concurrency", a.cfg.WorkerConcurrency)
	if a.metrics != nil {
		go a.metrics.WatchMongo(ctx, func(c context.Context) error { return a.client.Ping(c, nil) }, 15*time.Second)
		go a.metrics.WatchJobs(ctx, 15*time.Second, a.jobStats)
		go func() {
			if err := a.metrics.Serve(ctx, a.cfg.MetricsAddr, a.cfg.MetricsToken, a.cfg.ShutdownTimeout, a.log.With("listener", "metrics")); err != nil {
				a.log.Error("metrics listener", "error", err)
			}
		}()
	}
	w.Run(ctx, a.cfg.WorkerConcurrency)
	return nil
}

// workerID identifies this process in job leases, so an operator can
// tell which instance claimed a stuck job.
func workerID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "worker"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}

// templateRealm, templateDatabase, templateFunction and templateProject
// handle `backd template ...`: they write files under CONFIG_DIR (the
// project template, a whole directory) and list what they created.
func templateRealm(c *cli.CommandContext) error {
	dir, err := configDir(c)
	if err != nil {
		return err
	}
	files, err := templates.Realm(dir, str(c, "realm"), flag(c, "sample"))
	printTemplateFiles(c.Stdout(), files)
	return err
}

func templateDatabase(c *cli.CommandContext) error {
	dir, err := configDir(c)
	if err != nil {
		return err
	}
	realm, database := str(c, "realm"), str(c, "database")
	files, err := templates.Database(dir, realm, database, flag(c, "sample"))
	if err == nil {
		fmt.Fprintf(c.Stdout(), "database  %s/%s/\n", realm, database)
	}
	printTemplateFiles(c.Stdout(), files)
	return err
}

func templateFunction(c *cli.CommandContext) error {
	dir, err := configDir(c)
	if err != nil {
		return err
	}
	files, err := templates.Function(dir, str(c, "realm"), str(c, "database"), str(c, "name"))
	printTemplateFiles(c.Stdout(), files)
	return err
}

func templateEmailCapture(c *cli.CommandContext) error {
	dir, err := configDir(c)
	if err != nil {
		return err
	}
	files, err := templates.EmailCapture(dir, str(c, "realm"), str(c, "database"))
	printTemplateFiles(c.Stdout(), files)
	if err == nil {
		fmt.Fprintf(c.Stdout(), "\nnext: set `email.function: %s/email-capture` in %s/realm.yaml (see the file's comments), then run backd with BACKD_DEV=true\n", str(c, "database"), str(c, "realm"))
	}
	return err
}

func templateCollectionPolicy(c *cli.CommandContext) error {
	dir, err := configDir(c)
	if err != nil {
		return err
	}
	files, err := templates.CollectionPolicy(dir, str(c, "realm"), str(c, "database"), str(c, "collection"))
	printTemplateFiles(c.Stdout(), files)
	return err
}

func templateProject(c *cli.CommandContext) error {
	dir := str(c, "dir")
	files, err := templates.Project(dir, str(c, "realm"))
	printTemplateFiles(c.Stdout(), files)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.Stdout(), "\nnext: cd %s && cat README.md\n", dir)
	return nil
}

func printTemplateFiles(stdout io.Writer, files []templates.File) {
	for _, f := range files {
		if f.Created {
			fmt.Fprintf(stdout, "created   %s\n", f.Path)
		} else {
			fmt.Fprintf(stdout, "exists    %s (left unchanged)\n", f.Path)
		}
	}
}

// listDatabases handles `backd databases [--collections]`: it prints the
// MongoDB databases the config uses, data databases and realm system
// databases, sorted; with --collections, every collection as
// <database>.<collection>. It needs only CONFIG_DIR, so deploy scripts can
// grant a least-privilege user access to exactly these.
func listDatabases(c *cli.CommandContext) error {
	collections := flag(c, "collections")
	dir, err := configDir(c)
	if err != nil {
		return err
	}
	reg, err := registry.Load(dir)
	if err != nil {
		return fmt.Errorf("invalid config:\n%w", err)
	}
	var names []string
	add := func(db string, colls []string) {
		if !collections {
			names = append(names, db)
			return
		}
		for _, c := range colls {
			names = append(names, db+"."+c)
		}
	}
	for _, db := range reg.Databases() {
		var colls []string
		for _, c := range db.SortedCollections() {
			colls = append(colls, c.Name)
		}
		add(db.MongoName, colls)
	}
	for _, rl := range reg.SortedRealms() {
		if rl.Settings.AuthEnabled {
			add(mongodb.SystemDatabaseName(rl.Name), mongodb.SystemCollectionNames())
		}
	}
	// Where provisioning records the config fingerprint (read by verify).
	add(mongodb.DeploymentDatabase, []string{mongodb.DeploymentCollection})
	slices.Sort(names)
	for _, n := range names {
		fmt.Fprintln(c.Stdout(), n)
	}
	return nil
}

// handler builds the HTTP handler that serve runs.
func (a *app) handler() http.Handler {
	return httpapi.NewHandler(a.handlerConfig())
}

// internalHandler serves the internal listener (functions only).
func (a *app) internalHandler() http.Handler {
	return httpapi.NewInternalHandler(a.handlerConfig())
}

func (a *app) handlerConfig() httpapi.Config {
	var ui http.Handler
	if a.cfg.AdminUI {
		ui = adminui.Handler(a.cfg.AdminUIIdle)
	}
	var runner httpapi.FunctionRunner
	if a.cfg.ExecutorURL != "" {
		runner = a.executor()
	}
	return httpapi.Config{
		Functions:         runner,
		CallbackKey:       []byte(a.cfg.CallbackKey),
		CallbackURL:       a.cfg.CallbackURL,
		BackdURL:          a.cfg.BackdURL,
		ExecutorToken:     a.cfg.ExecutorToken,
		Log:               a.log,
		Registry:          a.reg,
		Store:             &mongodb.Store{Client: a.client},
		Ready:             func(ctx context.Context) error { return a.client.Ping(ctx, nil) },
		MaxBodyBytes:      a.cfg.MaxBodyBytes,
		OpTimeout:         a.cfg.MongoOpTimeout,
		Users:             a.realmUsers(),
		TrustedProxies:    a.cfg.TrustedProxies,
		ConfigFingerprint: a.fingerprint,
		Dev:               a.cfg.Dev,
		DisableAdminAPI:   a.cfg.DisableAdminAPI,
		UI:                ui,
		Metrics:           a.metrics,
	}
}

func (a *app) executor() *executor.Client {
	return &executor.Client{URL: a.cfg.ExecutorURL, Token: a.cfg.ExecutorToken}
}

// hasFunctions reports whether any database has functions.
func (a *app) hasFunctions() bool {
	return len(a.reg.FunctionRealms(true))+len(a.reg.FunctionRealms(false)) > 0
}

// checkExecutor warns when functions are configured but can't run: no
// executor configured, or it doesn't answer (calls then get 503).
func (a *app) checkExecutor(ctx context.Context) {
	if !a.hasFunctions() {
		return
	}
	if a.cfg.ExecutorURL == "" {
		a.log.Warn("functions are configured but BACKD_EXECUTOR_URL is not set: calls to them answer 503")
		return
	}
	hctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := a.executor().Health(hctx); err != nil {
		a.log.Warn("the functions executor doesn't answer: calls to functions answer 503 until it does", "executor", a.cfg.ExecutorURL, "error", err)
	}
}

// checkSecrets warns about functions that declare secrets with no stored
// value yet: they answer 500 secret_missing until one is set with
// `backd secret set`. A missing master key entirely is checkFunctions'
// job (a startup error, not a warning): nothing declared could resolve.
func (a *app) checkSecrets(ctx context.Context) error {
	users := a.realmUsers()
	for _, db := range a.reg.Databases() {
		if db.Functions == nil {
			continue
		}
		u := users(db.Realm)
		if u == nil {
			continue // auth disabled: registry refuses secrets there
		}
		for _, name := range slices.Sorted(maps.Keys(db.Functions.Functions)) {
			fn := db.Functions.Functions[name]
			if len(fn.Secrets) == 0 {
				continue
			}
			_, missing, err := u.ResolveSecrets(ctx, fn.Secrets, db.Name)
			if err != nil {
				return fmt.Errorf("checking %s/%s/%s's secrets: %w", db.Realm, db.Name, name, err)
			}
			if len(missing) > 0 {
				a.log.Warn("function declares secrets that aren't set yet; it answers secret_missing until they are",
					"function", db.Realm+"/"+db.Name+"/"+name, "secrets", missing)
			}
		}
	}
	return nil
}

// realmUsers returns the user service of every auth-enabled realm, built
// on first use. They share one Hasher, so the hashing concurrency cap
// applies per process.
func (a *app) realmUsers() func(string) *auth.Users {
	if a.userServices != nil {
		return a.userServices
	}
	hasher := auth.NewHasher(a.cfg.PasswordHashConcurrency, auth.DefaultArgon2Params)
	a.log.Info("password hashing", "concurrency", hasher.Concurrency(),
		"memory_per_hash_mib", auth.DefaultArgon2Params.Memory/1024)
	// One cipher and one cache, shared by every realm: the master key is
	// process-wide, and SecretCache keys include the realm.
	var cipher *auth.SecretCipher
	var cache *auth.SecretCache
	if len(a.cfg.SecretsKey) > 0 {
		var err error
		if cipher, err = auth.NewSecretCipher(a.cfg.SecretsKey); err != nil {
			// settings.Load already enforces the minimum length; this
			// would only fail on an internal bug.
			a.log.Error("secrets master key rejected; secrets are unavailable", "error", err)
			cipher = nil
		} else {
			cache = auth.NewSecretCache()
		}
	}
	services := map[string]*auth.Users{}
	for name, rl := range a.reg.Realms {
		if rl.Settings.AuthEnabled {
			services[name] = &auth.Users{Store: mongodb.NewAuthStore(a.client, name), Hasher: hasher, Settings: rl.Settings, Realm: name, Log: a.log, Cipher: cipher, Cache: cache, Metrics: a.metrics}
		}
	}
	a.userServices = func(realm string) *auth.Users { return services[realm] }
	return a.userServices
}

// logSecurityWarnings warns about realms without authentication, and
// realms whose data only API keys can reach.
func (a *app) logSecurityWarnings() {
	for _, rl := range a.reg.SortedRealms() {
		if !rl.Settings.AuthEnabled {
			a.log.Warn("REALM HAS AUTH DISABLED: anyone who can reach this service can read and modify its data", "realm", rl.Name)
		} else if !hasRules(rl) {
			a.log.Warn("realm has no rules.yaml in any collection: only API keys can access its data", "realm", rl.Name)
		}
		if rl.Settings.AuthEnabled && len(rl.Settings.AdminNetworks) == 0 {
			a.log.Info("admin API reachable from any network (no admin.allowed_networks in realm.yaml)", "realm", rl.Name)
		}
	}
}

// keyExpiryNotice is how long before an API key expires startup warns about it.
const keyExpiryNotice = 14 * 24 * time.Hour

// logStorageSecrets warns about a realm whose storage keys are not set: its file
// endpoints answer 503 storage_unavailable until they are. It never stops startup
// (the keys are set after the instance is up, with backd secret set).
func (a *app) logStorageSecrets(ctx context.Context, users func(string) *auth.Users) {
	for _, rl := range a.reg.SortedRealms() {
		st := rl.Settings.Storage
		svc := users(rl.Name)
		if st == nil || svc == nil {
			continue
		}
		refs := []registry.SecretRef{{Realm: true, Name: st.AccessKey}, {Realm: true, Name: st.SecretKey}}
		_, missing, err := svc.ResolveSecrets(ctx, refs, "")
		switch {
		case err != nil:
			a.log.Warn("could not read the realm's storage keys", "realm", rl.Name, "error", err)
		case len(missing) > 0:
			a.log.Warn("storage keys are not set: the realm's file endpoints answer 503 storage_unavailable until they are (backd secret set, then backd storage check)", "realm", rl.Name, "missing", missing)
		default:
			a.log.Info("realm storage", "realm", rl.Name, "provider", st.Provider, "bucket", st.Bucket, "prefix", st.Prefix)
		}
	}
}

// logAPIKeyExpiry reports API keys that never expire, that expire within
// keyExpiryNotice, and that have expired but are still stored. It logs key
// names, never keys or hashes.
func (a *app) logAPIKeyExpiry(ctx context.Context, users func(string) *auth.Users, now time.Time) error {
	for _, rl := range a.reg.SortedRealms() {
		svc := users(rl.Name)
		if svc == nil {
			continue
		}
		keys, err := svc.ListAPIKeys(ctx)
		if err != nil {
			return fmt.Errorf("realm %s: list API keys: %w", rl.Name, err)
		}
		var never, soon, expired []string
		for _, k := range keys {
			switch {
			case k.ExpiresAt == nil:
				never = append(never, k.Name)
			case k.Expired(now):
				expired = append(expired, k.Name)
			case k.ExpiresAt.Sub(now) < keyExpiryNotice:
				soon = append(soon, k.Name)
			}
		}
		if len(never) > 0 {
			a.log.Warn("API keys that never expire: replace them with keys created with --expires", "realm", rl.Name, "keys", never)
		}
		if len(soon) > 0 {
			a.log.Warn("API keys expire within 14 days: create replacements and roll them out", "realm", rl.Name, "keys", soon)
		}
		if len(expired) > 0 {
			a.log.Info("expired API keys are refused but still listed: revoke them", "realm", rl.Name, "keys", expired)
		}
	}
	return nil
}

// hasRules reports whether any collection of the realm has access rules.
func hasRules(rl *registry.Realm) bool {
	for _, db := range rl.Databases {
		for _, c := range db.Collections {
			if c.Rules != nil {
				return true
			}
		}
	}
	return false
}

// syncRoles applies the role assignments seeded in realm.yaml and reports
// assignments that realm.yaml doesn't account for. It logs counts only:
// `backd user list` shows who has which role.
func (a *app) syncRoles(ctx context.Context) error {
	users := a.realmUsers()
	for _, rl := range a.reg.SortedRealms() {
		svc := users(rl.Name)
		if svc == nil {
			continue
		}
		n, err := svc.ApplyRoleSeeds(ctx)
		if err != nil {
			return fmt.Errorf("realm %s: apply role seeds: %w", rl.Name, err)
		}
		if n > 0 {
			a.log.Info("role seeds from realm.yaml applied", "realm", rl.Name, "users", n)
		}
		rep, err := svc.ReportRoles(ctx)
		if err != nil {
			return fmt.Errorf("realm %s: check roles: %w", rl.Name, err)
		}
		if len(rep.DBOnly) > 0 {
			a.log.Info("role assignments stored only in the database (not in realm.yaml); they won't come back if the realm is rebuilt from config",
				"realm", rl.Name, "users", len(rep.DBOnly))
		}
		if len(rep.NetworksDBOnly) > 0 {
			a.log.Info("network restrictions stored only in the database (not in realm.yaml); they won't come back if the realm is rebuilt from config",
				"realm", rl.Name, "users", len(rep.NetworksDBOnly))
		}
		switch {
		case len(rl.Settings.AdminRoles()) == 0:
			a.log.Warn("realm declares no admin role (admin: true in realm.yaml): only existing admin API keys can manage it, and `backd bootstrap` can't create an administrator", "realm", rl.Name)
		case len(rl.Settings.FullAdminRoles()) == 0:
			a.log.Warn("no role of the realm has admin: true: nobody can manage roles and API keys beyond the areas their own role opens, and `backd bootstrap` can't create an administrator", "realm", rl.Name)
		case rep.Admins == 0:
			a.log.Warn("no user holds an admin role of the realm, in realm.yaml or the database: create the first administrator with `backd bootstrap`", "realm", rl.Name)
		}
		if len(rep.Undeclared) > 0 {
			a.log.Warn("users have roles that realm.yaml no longer declares; rules can't use them",
				"realm", rl.Name, "users", len(rep.Undeclared))
		}
	}
	return nil
}

// checkFunctions refuses stale or missing function bundles and functions
// declaring secrets when no master key is configured (roadmap F5):
// unlike a specific secret not being set yet (a startup warning,
// checkSecrets), nothing declared could ever resolve without one.
func checkFunctions(reg *registry.Registry, cfg settings.Settings, log *slog.Logger) error {
	if err := reg.CheckBundles(); err != nil {
		return fmt.Errorf("invalid config:\n%w", err)
	}
	if len(cfg.SecretsKey) == 0 {
		if fn := firstFunctionWithSecrets(reg); fn != nil {
			return fmt.Errorf("%s/%s/%s declares secrets, but BACKD_SECRETS_KEY (or BACKD_SECRETS_KEY_FILE) is not set", fn.Realm, fn.Database, fn.Name)
		}
	}
	if !cfg.Dev {
		for _, db := range reg.Databases() {
			if db.Functions == nil {
				continue
			}
			for _, name := range slices.Sorted(maps.Keys(db.Functions.Functions)) {
				if fn := db.Functions.Functions[name]; fn.DevOnly {
					return fmt.Errorf("%s/%s/%s declares dev_only: true, so it only runs with BACKD_DEV=true (local development): remove the function from this deployment's config", fn.Realm, fn.Database, fn.Name)
				}
			}
		}
	}
	for _, name := range reg.RealmNames() {
		st := reg.Realms[name].Settings.Storage
		if st == nil {
			continue
		}
		if len(cfg.SecretsKey) == 0 {
			return fmt.Errorf("realm %s configures storage, whose access keys are secrets, but BACKD_SECRETS_KEY (or BACKD_SECRETS_KEY_FILE) is not set", name)
		}
		if st.HTTP && !cfg.Dev {
			return fmt.Errorf("realm %s: storage uses a plain http address (%s): only BACKD_DEV=true (local development) allows it; use https", name, plainHTTP(st))
		}
	}
	for _, name := range reg.RealmNames() {
		if e := reg.Realms[name].Settings.Email; e != nil && e.PublicURL == "" && cfg.BackdURL == "" {
			return fmt.Errorf("realm %s configures email, but backd's public address is unknown: set BACKD_URL (or email.public_url in its realm.yaml), the base of the links in its emails", name)
		}
	}
	warnAnonymousFunctionsWithoutRateLimit(reg, log)
	logEmailSenders(reg, log)
	return nil
}

// firstFunctionWithSecrets returns the first function (by realm, database,
// name) that declares secrets, or nil.
func firstFunctionWithSecrets(reg *registry.Registry) *registry.Function {
	dbs := reg.Databases()
	slices.SortFunc(dbs, func(a, b *registry.Database) int {
		if c := strings.Compare(a.Realm, b.Realm); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	for _, db := range dbs {
		if db.Functions == nil {
			continue
		}
		names := slices.Sorted(maps.Keys(db.Functions.Functions))
		for _, name := range names {
			if fn := db.Functions.Functions[name]; len(fn.Secrets) > 0 {
				return fn
			}
		}
	}
	return nil
}

// warnAnonymousFunctionsWithoutRateLimit warns, once per function, about
// any whose invoke rule allows anonymous callers and declares no
// rate_limit (roadmap F9): a function anyone can call for free, with no
// per-caller limit, can be abused as a relay (e.g. one that sends email).
// logEmailSenders lists the functions that can send email (`email: true`), one
// line each, and warns about those anonymous callers can invoke: a function
// like that is a way to send mail from the realm's sender address without
// any control (docs: Functions -> Email -> Sending email from functions).
func logEmailSenders(reg *registry.Registry, log *slog.Logger) {
	for _, db := range reg.Databases() {
		if db.Functions == nil {
			continue
		}
		for _, name := range slices.Sorted(maps.Keys(db.Functions.Functions)) {
			fn := db.Functions.Functions[name]
			if !fn.Email {
				continue
			}
			full := db.Realm + "/" + db.Name + "/" + name
			log.Info("function can send email", "function", full)
			if fn.Invoke == nil || fn.Internal {
				continue
			}
			if allowed, err := fn.Invoke.Allow(rules.Values{Now: time.Now()}); allowed && err == nil {
				log.Warn("function can send email and its invoke rule allows anonymous callers: anyone can make it send mail from this realm's sender address; require a verified user, and never take the recipient or the text from the input",
					"function", full)
			}
		}
	}
}

func warnAnonymousFunctionsWithoutRateLimit(reg *registry.Registry, log *slog.Logger) {
	dbs := reg.Databases()
	slices.SortFunc(dbs, func(a, b *registry.Database) int {
		if c := strings.Compare(a.Realm, b.Realm); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	for _, db := range dbs {
		if db.Functions == nil {
			continue
		}
		for _, name := range slices.Sorted(maps.Keys(db.Functions.Functions)) {
			fn := db.Functions.Functions[name]
			if fn.Invoke == nil || fn.RateLimit != nil {
				continue
			}
			allowed, err := fn.Invoke.Allow(rules.Values{Now: time.Now()})
			if allowed && err == nil {
				log.Warn("function's invoke rule allows anonymous callers and declares no rate_limit: it can be called for free, without limit, by anyone",
					"function", db.Realm+"/"+db.Name+"/"+name)
			}
		}
	}
}

// commit is the VCS revision the binary was built from ("unknown" when the
// build has none, such as a container build without .git).
func commit() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				return s.Value[:min(len(s.Value), 12)]
			}
		}
	}
	return "unknown"
}

// jobStats reads every auth realm's job queue, for the metrics refresher.
// A realm that fails doesn't hide the others.
func (a *app) jobStats(ctx context.Context) (map[string]metrics.RealmJobs, error) {
	out := map[string]metrics.RealmJobs{}
	var errs []error
	for _, realm := range a.reg.AuthRealms() {
		svc := a.realmUsers()(realm)
		src, ok := svc.Store.(metrics.JobSource)
		if !ok {
			continue
		}
		stats, err := src.JobStats(ctx, time.Now())
		if err != nil {
			errs = append(errs, err)
			continue
		}
		attention, err := src.EraseNeedsAttention(ctx)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out[realm] = metrics.RealmJobs{Stats: stats, NeedsAttention: attention}
	}
	return out, errors.Join(errs...)
}

// plainHTTP is the storage address that is not https.
func plainHTTP(st *registry.StorageSettings) string {
	if strings.HasPrefix(st.Endpoint, "http://") {
		return st.Endpoint
	}
	return st.PublicEndpoint
}
