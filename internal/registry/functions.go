package registry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	"github.com/fernandezvara/backd/internal/cron"
	"github.com/fernandezvara/backd/internal/rules"
)

// Function layout inside a database directory.
const (
	FunctionsDir     = "_functions" // one Deno project per database
	BuildDir         = ".build"     // bundles and their manifest, written by `backd functions build`
	ManifestFile     = "manifest.json"
	FunctionFile     = "function.yaml"
	libDir           = "lib" // shared code of the project; not a function
	InputSchemaFile  = "input.schema.json"
	OutputSchemaFile = "output.schema.json"
	sourceHashSalt   = "backd-functions-v1\x00"
)

// DenoVersion is the Deno version functions are bundled and run with.
// `backd functions build` requires exactly this version, and the executor
// image is built from it.
const DenoVersion = "2.9.7"

// Function modes.
const (
	ModeSync    = "sync"
	ModeAsync   = "async"
	ModeWebhook = "webhook"
)

// Defaults and bounds of function.yaml values.
const (
	DefaultSyncTimeout  = 10 * time.Second
	DefaultAsyncTimeout = 15 * time.Minute
	MaxSyncTimeout      = 60 * time.Second
	MaxAsyncTimeout     = 24 * time.Hour
	minTimeout          = time.Second
	DefaultMemory       = 128 << 20
	minMemory           = 32 << 20
	maxMemory           = 4 << 30
	DefaultMaxOutput    = 1 << 20
	minMaxOutput        = 1 << 10
	MaxSyncOutput       = 64 << 20
	// MaxAsyncOutput keeps an async job's result within one MongoDB
	// document (16 MiB, minus room for the job's other fields).
	MaxAsyncOutput     = 15 << 20
	DefaultConcurrency = 10
	maxConcurrency     = 1000
	minRateWindow      = time.Second
	maxRetryAttempts   = 20
	defaultBackoff     = time.Minute
	defaultMaxBackoff  = time.Hour
	maxRetryWindow     = 24 * time.Hour // all the waits of one job together
	maxRateWindow      = 24 * time.Hour
)

// Functions is a database's Deno project of functions (`_functions/`).
type Functions struct {
	Dir       string               // the _functions directory
	Functions map[string]*Function // by name
}

// Function is one function, from its folder in _functions/.
type Function struct {
	Realm, Database, Name string
	Dir                   string // the function's folder
	Entry                 string // index.js or index.ts, relative to the project (slash-separated)
	Mode                  string
	Timeout               time.Duration
	Memory                int64 // bytes
	MaxOutput             int64 // bytes
	Concurrency           int
	RateLimit             *RateLimit     // nil: none
	Retry                 *Retry         // nil: a single attempt
	Idempotency           string         // optional or required
	Invoke                *rules.Rule    // nil: only API keys may invoke
	Admin                 bool           // ctx.admin.db with full access
	Internal              bool           // no HTTP route: only backd, scheduled runs and other functions call it
	Email                 bool           // ctx.email.send: may send custom emails through the realm's templates and delivery function
	DevOnly               bool           // backd refuses to start with it unless BACKD_DEV=true (development helpers)
	Calls                 []string       // functions of the same database this one may ctx.call
	Schedule              *cron.Schedule // nil: not scheduled
	ScheduleExpr          string
	OnComplete            *OnComplete // nil: nothing is told when a job ends
	Timezone              string      // IANA name the schedule is read in; "UTC" unless set. Empty when not scheduled
	Overlap               string      // allow (default) or skip: what a scheduled time does while the previous run hasn't finished
	Secrets               []SecretRef
	Network               []string // hosts (host or host:port) the function may reach
	InputSchema           *jsonschema.Schema
	OutputSchema          *jsonschema.Schema
}

// Retry is an async function's retry policy: how often a failed attempt
// is repeated and how long to wait between attempts.
type Retry struct {
	Attempts   int           // total attempts, the first included
	Backoff    time.Duration // the wait after the first failure; doubles after each one
	MaxBackoff time.Duration // the longest single wait
}

// Wait is how long to wait after the failures-th failed attempt (1 for the
// first failure): Backoff, doubled for each further failure, up to MaxBackoff.
func (r Retry) Wait(failures int) time.Duration {
	d := r.Backoff
	for i := 1; i < failures && d < r.MaxBackoff; i++ {
		d *= 2
	}
	return min(d, r.MaxBackoff)
}

// RateLimit limits calls per caller (user or client address).
type RateLimit struct {
	Per    string // user or ip
	Limit  int
	Window time.Duration
}

// SecretRef names a secret a function may read: its own database's
// (`NAME`) or the realm's (`realm.NAME`).
type SecretRef struct {
	Realm bool
	Name  string
}

func (s SecretRef) String() string {
	if s.Realm {
		return "realm." + s.Name
	}
	return s.Name
}

// ValidSecretName reports whether name is a valid secret NAME (upper-case
// letters, digits and _, starting with a letter) — the same rule
// function.yaml's secrets list is checked against, without the leading
// "realm." a reference may carry.
func ValidSecretName(name string) bool { return secretPattern.MatchString(name) }

// functionDoc mirrors function.yaml. Pointers tell "left out" from "set".
type functionDoc struct {
	Mode        *string `yaml:"mode"`
	Timeout     *string `yaml:"timeout"`
	Memory      *string `yaml:"memory"`
	MaxOutput   *string `yaml:"max_output"`
	Concurrency *int    `yaml:"concurrency"`
	RateLimit   *struct {
		Per    string `yaml:"per"`
		Limit  int    `yaml:"limit"`
		Window string `yaml:"window"`
	} `yaml:"rate_limit"`
	Retry *struct {
		Attempts   int    `yaml:"attempts"`
		Backoff    string `yaml:"backoff"`
		MaxBackoff string `yaml:"max_backoff"`
	} `yaml:"retry"`
	Idempotency *string  `yaml:"idempotency"`
	Invoke      *string  `yaml:"invoke"`
	Admin       bool     `yaml:"admin"`
	Internal    bool     `yaml:"internal"`
	DevOnly     bool     `yaml:"dev_only"`
	Email       bool     `yaml:"email"`
	Calls       []string `yaml:"calls"`
	Schedule    *string  `yaml:"schedule"`
	Timezone    *string  `yaml:"timezone"`
	OnComplete  *struct {
		Function string   `yaml:"function"`
		On       []string `yaml:"on"`
	} `yaml:"on_complete"`
	Overlap *string  `yaml:"overlap"`
	Secrets []string `yaml:"secrets"`
	Network []string `yaml:"network"`
}

// The endings a job's on_complete notification can be limited to (`on`).
const (
	CompleteOK        = "ok"        // the function returned
	CompleteFailed    = "failed"    // any other result: an error, a timeout, a crash
	CompleteCancelled = "cancelled" // an administrator cancelled the job
)

// OnComplete says which async function is queued, as a job of its own, when a
// job of this function ends.
type OnComplete struct {
	Function string   // a function of the same database
	On       []string // the endings that notify; every one when empty
}

// Notifies reports whether an ending (CompleteOK, CompleteFailed or
// CompleteCancelled) is told.
func (o *OnComplete) Notifies(ending string) bool {
	return o != nil && (len(o.On) == 0 || slices.Contains(o.On, ending))
}

// What a scheduled time does while the function's previous run hasn't finished.
const (
	OverlapAllow = "allow"
	OverlapSkip  = "skip"
)

var (
	secretPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	hostPattern   = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	sizePattern   = regexp.MustCompile(`^([0-9]+)(B|KB|MB|GB|KiB|MiB|GiB)$`)
)

var sizeUnits = map[string]int64{"B": 1, "KB": 1e3, "MB": 1e6, "GB": 1e9, "KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30}

// ParseSize parses a size such as 128MB (decimal) or 1MiB (binary).
func ParseSize(v string) (int64, error) {
	m := sizePattern.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return 0, fmt.Errorf("invalid size %q: want a whole number and a unit (B, KB, MB, GB, KiB, MiB, GiB), such as 128MB or 1MiB", v)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	unit := sizeUnits[m[2]]
	if err != nil || n > math.MaxInt64/unit {
		return 0, fmt.Errorf("invalid size %q: too large", v)
	}
	return n * unit, nil
}

// loadFunctions reads a database's _functions project, if it has one.
func loadFunctions(db *Database, settings RealmSettings, dbPath string) (*Functions, []error) {
	dir := filepath.Join(dbPath, FunctionsDir)
	if fi, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, []error{err}
	} else if !fi.IsDir() {
		return nil, []error{fmt.Errorf("%s: must be a directory", dir)}
	}
	names, err := subdirs(dir)
	if err != nil {
		return nil, []error{err}
	}
	fns := &Functions{Dir: dir, Functions: map[string]*Function{}}
	var errs []error
	for _, name := range names {
		if name == libDir {
			continue
		}
		path := filepath.Join(dir, name)
		if !namePattern.MatchString(name) {
			errs = append(errs, nameError(path, "function"))
			continue
		}
		fn, fnErrs := loadFunction(db, settings, name, path)
		errs = append(errs, fnErrs...)
		if fn != nil {
			fns.Functions[name] = fn
		}
	}
	errs = append(errs, checkCalls(fns)...)
	if len(fns.Functions) == 0 && len(errs) == 0 {
		errs = append(errs, fmt.Errorf("%s: no functions (each function is a folder with %s and index.js or index.ts)", dir, FunctionFile))
	}
	return fns, errs
}

// MaxCallChain is the longest chain of functions calling each other, the
// entry function included.
const MaxCallChain = 4

// checkCalls checks the call graph of a database's functions: every name
// in `calls` exists, there are no cycles and no chain is longer than
// MaxCallChain.
func checkCalls(fns *Functions) []error {
	var errs []error
	names := make([]string, 0, len(fns.Functions))
	for n := range fns.Functions {
		names = append(names, n)
	}
	slices.Sort(names)
	file := func(n string) string { return filepath.Join(fns.Functions[n].Dir, FunctionFile) }
	for _, n := range names {
		for _, c := range fns.Functions[n].Calls {
			if _, ok := fns.Functions[c]; !ok {
				errs = append(errs, fmt.Errorf("%s: calls: %q is not a function of this database", file(n), c))
			}
		}
	}
	for _, n := range names {
		oc := fns.Functions[n].OnComplete
		if oc == nil {
			continue
		}
		switch t := fns.Functions[oc.Function]; {
		case t == nil:
			errs = append(errs, fmt.Errorf("%s: on_complete.function: %q is not a function of this database", file(n), oc.Function))
		case t.Mode != ModeAsync:
			errs = append(errs, fmt.Errorf("%s: on_complete.function: %q must be mode: async (the notification is a job, so it can be retried)", file(n), oc.Function))
		case t.OnComplete != nil:
			errs = append(errs, fmt.Errorf("%s: on_complete.function: %q has an on_complete of its own (notifications don't chain)", file(n), oc.Function))
		}
	}
	for _, n := range names {
		for _, c := range fns.Functions[n].Calls {
			if t := fns.Functions[c]; t != nil && t.Mode == ModeWebhook {
				errs = append(errs, fmt.Errorf("%s: calls: %q is a webhook function, which only its sender calls", file(n), c))
			}
		}
	}
	if len(errs) > 0 {
		return errs
	}
	// Depth-first search: state 1 = on the current path, 2 = done; depth[n]
	// is the longest chain starting at n.
	state := map[string]int{}
	depth := map[string]int{}
	var path []string
	var visit func(n string)
	visit = func(n string) {
		state[n] = 1
		path = append(path, n)
		longest := 0
		for _, c := range fns.Functions[n].Calls {
			switch state[c] {
			case 1:
				i := slices.Index(path, c)
				cycle := append(slices.Clone(path[i:]), c)
				errs = append(errs, fmt.Errorf("%s: calls: cycle %s", file(n), strings.Join(cycle, " -> ")))
			case 0:
				visit(c)
			}
			longest = max(longest, depth[c])
		}
		path = path[:len(path)-1]
		state[n] = 2
		depth[n] = longest + 1
	}
	for _, n := range names {
		if state[n] == 0 {
			visit(n)
		}
	}
	if len(errs) > 0 {
		return errs
	}
	for _, n := range names {
		if depth[n] > MaxCallChain {
			errs = append(errs, fmt.Errorf("%s: calls: a chain of %d functions starts here, the longest allowed is %d", file(n), depth[n], MaxCallChain))
		}
	}
	return errs
}

func loadFunction(db *Database, settings RealmSettings, name, dir string) (*Function, []error) {
	path := filepath.Join(dir, FunctionFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, []error{fmt.Errorf("%s: function folder has no %s", dir, FunctionFile)}
	}
	if err != nil {
		return nil, []error{err}
	}
	fn := &Function{Realm: db.Realm, Database: db.Name, Name: name, Dir: dir}
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: "+format, append([]any{path}, args...)...))
	}

	// Exactly one entry point.
	var entries []string
	for _, e := range []string{"index.js", "index.ts"} {
		if fi, err := os.Stat(filepath.Join(dir, e)); err == nil && !fi.IsDir() {
			entries = append(entries, e)
		}
	}
	switch len(entries) {
	case 0:
		errs = append(errs, fmt.Errorf("%s: function folder has no index.js or index.ts", dir))
	case 1:
		fn.Entry = name + "/" + entries[0]
	default:
		errs = append(errs, fmt.Errorf("%s: function folder has both index.js and index.ts; keep one", dir))
	}

	var doc functionDoc
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, append(errs, fmt.Errorf("%s: invalid YAML: %w", path, err))
	}

	fn.Mode = ModeSync
	if doc.Mode != nil {
		switch *doc.Mode {
		case ModeSync, ModeAsync, ModeWebhook:
			fn.Mode = *doc.Mode
		default:
			add("mode: must be sync, async or webhook, got %q", *doc.Mode)
		}
	}
	if fn.Mode == ModeAsync && !settings.AuthEnabled {
		add("mode: async needs auth enabled (its jobs are stored in the realm's system database)")
	}

	fn.Timeout = DefaultSyncTimeout
	maxTimeout := MaxSyncTimeout
	if fn.Mode == ModeAsync {
		fn.Timeout, maxTimeout = DefaultAsyncTimeout, MaxAsyncTimeout
	}
	if doc.Timeout != nil {
		d, err := ParseDuration(*doc.Timeout)
		switch {
		case err != nil:
			add("timeout: %v", err)
		case d < minTimeout || d > maxTimeout:
			add("timeout: must be between %s and %s for %s functions, got %s", minTimeout, maxTimeout, fn.Mode, *doc.Timeout)
		default:
			fn.Timeout = d
		}
	}

	fn.Memory = DefaultMemory
	if doc.Memory != nil {
		n, err := ParseSize(*doc.Memory)
		switch {
		case err != nil:
			add("memory: %v", err)
		case n < minMemory || n > maxMemory:
			add("memory: must be between 32MiB and 4GiB, got %s", *doc.Memory)
		default:
			fn.Memory = n
		}
	}

	fn.MaxOutput = DefaultMaxOutput
	maxOutput := int64(MaxSyncOutput)
	if fn.Mode == ModeAsync {
		maxOutput = MaxAsyncOutput
	}
	if doc.MaxOutput != nil {
		n, err := ParseSize(*doc.MaxOutput)
		switch {
		case err != nil:
			add("max_output: %v", err)
		case n < minMaxOutput || n > maxOutput:
			add("max_output: must be between 1KiB and %dMiB for %s functions, got %s", maxOutput>>20, fn.Mode, *doc.MaxOutput)
		default:
			fn.MaxOutput = n
		}
	}

	fn.Concurrency = DefaultConcurrency
	if doc.Concurrency != nil {
		if *doc.Concurrency < 1 || *doc.Concurrency > maxConcurrency {
			add("concurrency: must be between 1 and %d, got %d", maxConcurrency, *doc.Concurrency)
		} else {
			fn.Concurrency = *doc.Concurrency
		}
	}

	if rl := doc.RateLimit; rl != nil {
		r := &RateLimit{Per: rl.Per, Limit: rl.Limit}
		if rl.Per != "user" && rl.Per != "ip" {
			add("rate_limit.per: must be user or ip, got %q", rl.Per)
		}
		if rl.Limit < 1 {
			add("rate_limit.limit: must be at least 1")
		}
		d, err := ParseDuration(rl.Window)
		switch {
		case err != nil:
			add("rate_limit.window: %v", err)
		case d < minRateWindow || d > maxRateWindow:
			add("rate_limit.window: must be between %s and %s, got %s", minRateWindow, maxRateWindow, rl.Window)
		}
		r.Window = d
		fn.RateLimit = r
	}

	if rt := doc.Retry; rt != nil {
		r := &Retry{Attempts: rt.Attempts, Backoff: defaultBackoff, MaxBackoff: defaultMaxBackoff}
		ok := true
		if fn.Mode != ModeAsync {
			add("retry: only async functions can be retried (a sync caller is waiting, and a webhook sender retries by itself)")
			ok = false
		}
		if rt.Attempts < 1 || rt.Attempts > maxRetryAttempts {
			add("retry.attempts: must be between 1 and %d (the first attempt included), got %d", maxRetryAttempts, rt.Attempts)
			ok = false
		}
		for _, d := range []struct {
			key string
			v   string
			dst *time.Duration
		}{{"backoff", rt.Backoff, &r.Backoff}, {"max_backoff", rt.MaxBackoff, &r.MaxBackoff}} {
			if d.v == "" {
				continue
			}
			v, err := ParseDuration(d.v)
			switch {
			case err != nil:
				add("retry.%s: %v", d.key, err)
				ok = false
			case v < time.Second || v > maxRetryWindow:
				add("retry.%s: must be between 1s and %s, got %s", d.key, maxRetryWindow, d.v)
				ok = false
			default:
				*d.dst = v
			}
		}
		if ok && rt.MaxBackoff == "" && r.MaxBackoff < r.Backoff {
			r.MaxBackoff = r.Backoff
		}
		if ok && r.MaxBackoff < r.Backoff {
			add("retry.max_backoff: must not be shorter than backoff (%s)", r.Backoff)
			ok = false
		}
		if ok && r.Attempts > 1 {
			var total time.Duration
			for i := 1; i < r.Attempts; i++ {
				total += r.Wait(i)
			}
			if total > maxRetryWindow {
				add("retry: the waits between %d attempts add up to %s, more than the %s a job may keep retrying", r.Attempts, total, maxRetryWindow)
				ok = false
			}
		}
		if ok && r.Attempts > 1 {
			fn.Retry = r
		}
	}

	fn.Idempotency = "optional"
	if doc.Idempotency != nil {
		if *doc.Idempotency != "optional" && *doc.Idempotency != "required" {
			add("idempotency: must be optional or required, got %q", *doc.Idempotency)
		} else {
			fn.Idempotency = *doc.Idempotency
		}
	}

	if doc.Invoke != nil {
		r, err := rules.CompileInvoke(*doc.Invoke, roleNames(settings))
		if err != nil {
			add("%v", err)
		}
		fn.Invoke = r
	}
	if fn.Invoke != nil && !settings.AuthEnabled {
		add("invoke: only applies when auth is enabled (in this realm anyone may call the function)")
	}
	if fn.Mode == ModeWebhook && settings.AuthEnabled {
		switch fn.Invoke {
		case nil:
			add("mode: webhook requires an invoke rule that allows anonymous callers (without one, only API keys may call it, but a webhook sender never has one)")
		default:
			allowed, err := fn.Invoke.Allow(rules.Values{Now: time.Now()})
			if err != nil || !allowed {
				add(`mode: webhook requires an invoke rule that allows anonymous callers (e.g. "true"); this one refuses them`)
			}
		}
	}

	fn.Admin = doc.Admin

	fn.Internal = doc.Internal
	fn.DevOnly = doc.DevOnly
	fn.Email = doc.Email
	if fn.Internal && doc.Invoke != nil {
		add("internal: a function with no HTTP route has no use for an invoke rule; remove one of them")
	}
	if fn.Internal && fn.Mode == ModeWebhook {
		add("internal: a webhook function is called over HTTP by its sender, so it can't be internal")
	}
	for i, c := range doc.Calls {
		switch {
		case !namePattern.MatchString(c):
			add("calls[%d]: %q is not a valid function name", i, c)
		case c == name:
			add("calls[%d]: %q calls itself", i, c)
		case slices.Contains(fn.Calls, c):
			add("calls[%d]: %q is listed twice", i, c)
		default:
			fn.Calls = append(fn.Calls, c)
		}
	}

	if doc.Schedule != nil {
		switch {
		case fn.Mode != ModeAsync:
			add("schedule: needs mode: async (scheduled runs go through the async queue)")
		default:
			sch, err := cron.Parse(*doc.Schedule)
			if err != nil {
				add("schedule: %v", err)
			} else {
				fn.Schedule, fn.ScheduleExpr, fn.Timezone = sch, strings.TrimSpace(*doc.Schedule), "UTC"
			}
		}
	}
	if doc.Timezone != nil {
		switch loc, err := time.LoadLocation(*doc.Timezone); {
		case doc.Schedule == nil:
			add("timezone: only applies to a scheduled function (add schedule)")
		case *doc.Timezone == "" || *doc.Timezone == "Local" || err != nil:
			add("timezone: %q is not an IANA time zone name such as Europe/Madrid or UTC", *doc.Timezone)
		case fn.Schedule != nil:
			fn.Schedule, fn.Timezone = fn.Schedule.In(loc), *doc.Timezone
		}
	}

	if oc := doc.OnComplete; oc != nil {
		switch {
		case fn.Mode != ModeAsync:
			add("on_complete: needs mode: async (only async functions have jobs)")
		case !namePattern.MatchString(oc.Function):
			add("on_complete.function: %q is not a valid function name", oc.Function)
		case oc.Function == name:
			add("on_complete.function: %q is the function itself", oc.Function)
		default:
			on := &OnComplete{Function: oc.Function}
			for i, e := range oc.On {
				switch {
				case e != CompleteOK && e != CompleteFailed && e != CompleteCancelled:
					add("on_complete.on[%d]: %q must be %s, %s or %s", i, e, CompleteOK, CompleteFailed, CompleteCancelled)
				case slices.Contains(on.On, e):
					add("on_complete.on[%d]: %q is listed twice", i, e)
				default:
					on.On = append(on.On, e)
				}
			}
			fn.OnComplete = on
		}
	}

	if doc.Schedule != nil {
		fn.Overlap = OverlapAllow
	}
	if doc.Overlap != nil {
		switch {
		case *doc.Overlap != OverlapAllow && *doc.Overlap != OverlapSkip:
			add("overlap: must be %s or %s, got %q", OverlapAllow, OverlapSkip, *doc.Overlap)
		case doc.Schedule == nil:
			add("overlap: only applies to a scheduled function (add schedule)")
		default:
			fn.Overlap = *doc.Overlap
		}
	}

	seen := map[string]bool{}
	for i, s := range doc.Secrets {
		ref := SecretRef{Name: s}
		if name, ok := strings.CutPrefix(s, "realm."); ok {
			ref = SecretRef{Realm: true, Name: name}
		}
		switch {
		case ref.Realm && reservedRealmSecret(settings, ref.Name):
			add("secrets[%d]: %q is a secret backd keeps for itself (the realm's storage keys and the files link key): a function can't read it", i, s)
		case !secretPattern.MatchString(ref.Name):
			add("secrets[%d]: %q must be NAME or realm.NAME, with NAME in upper case letters, digits and _ (starting with a letter)", i, s)
		case seen[s]:
			add("secrets[%d]: %q is listed twice", i, s)
		default:
			seen[s] = true
			fn.Secrets = append(fn.Secrets, ref)
		}
	}
	if len(fn.Secrets) > 0 && !settings.AuthEnabled {
		add("secrets: only applies when auth is enabled (this realm has no system database to store them in)")
	}
	if fn.RateLimit != nil && !settings.AuthEnabled {
		add("rate_limit: only applies when auth is enabled (this realm has no system database to store counters in)")
	}

	for i, h := range doc.Network {
		if err := checkHost(h); err != nil {
			add("network[%d]: %v", i, err)
			continue
		}
		if slices.Contains(fn.Network, h) {
			add("network[%d]: %q is listed twice", i, h)
			continue
		}
		fn.Network = append(fn.Network, h)
	}

	for _, s := range []struct {
		file string
		dst  **jsonschema.Schema
	}{{InputSchemaFile, &fn.InputSchema}, {OutputSchemaFile, &fn.OutputSchema}} {
		sch, err := loadFunctionSchema(filepath.Join(dir, s.file))
		if err != nil {
			errs = append(errs, err)
		}
		*s.dst = sch
	}
	return fn, errs
}

// checkHost accepts a lower-case host name or IP address, with an optional port.
func checkHost(h string) error {
	host, port := h, ""
	if strings.HasPrefix(h, "[") || strings.Count(h, ":") == 1 {
		var err error
		if host, port, err = net.SplitHostPort(h); err != nil {
			return fmt.Errorf("%q: want host or host:port", h)
		}
	}
	if port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("%q: invalid port", h)
		}
	}
	if net.ParseIP(host) == nil && !hostPattern.MatchString(host) {
		return fmt.Errorf("%q: want a lower-case host name (no scheme, path or wildcard) or an IP address, optionally with :port", h)
	}
	return nil
}

func loadFunctionSchema(path string) (*jsonschema.Schema, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: invalid JSON: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	url := "file://" + filepath.ToSlash(abs)
	if err := c.AddResource(url, doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s, err := c.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid schema: %w", path, err)
	}
	return s, nil
}

func roleNames(s RealmSettings) []string {
	out := make([]string, 0, len(s.Roles))
	for name := range s.Roles {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// SourceHash hashes every file of a functions project, except hidden
// entries (such as .build), so any change to code, shared lib/, deno.json,
// deno.lock or schemas changes it.
func SourceHash(dir string) (string, error) {
	h := sha256.New()
	h.Write([]byte(sourceHashSalt))
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != dir && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(data))
		h.Write(data)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Manifest describes the bundles `backd functions build` wrote for one
// functions project.
type Manifest struct {
	Deno      string                    `json:"deno"`
	Source    string                    `json:"source"` // SourceHash of the project when built
	Functions map[string]ManifestBundle `json:"functions"`
}

// ManifestBundle is one function's bundle.
type ManifestBundle struct {
	Bundle string `json:"bundle"` // file name in .build
	SHA256 string `json:"sha256"`
}

// ReadManifest reads a project's .build/manifest.json.
func ReadManifest(functionsDir string) (Manifest, error) {
	var m Manifest
	data, err := os.ReadFile(filepath.Join(functionsDir, BuildDir, ManifestFile))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("%s: %w", filepath.Join(functionsDir, BuildDir, ManifestFile), err)
	}
	return m, nil
}

// BundlePath returns the file of fn's bundle, as recorded in the manifest.
func (f *Functions) BundlePath(m Manifest, fn string) string {
	return filepath.Join(f.Dir, BuildDir, m.Functions[fn].Bundle)
}

// CheckBundles reports functions projects whose bundles are missing, were
// built by another Deno version, don't match the sources, or were changed
// since the build. Startup refuses to run with any of them.
func (r *Registry) CheckBundles() error {
	var errs []error
	for _, db := range r.Databases() {
		fns := db.Functions
		if fns == nil {
			continue
		}
		fix := fmt.Sprintf("run `backd functions build` (for %s/%s)", db.Realm, db.Name)
		m, err := ReadManifest(fns.Dir)
		if errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("%s: functions are not built: %s", fns.Dir, fix))
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if m.Deno != DenoVersion {
			errs = append(errs, fmt.Errorf("%s: bundles were built with Deno %s, this backd uses %s: %s", fns.Dir, m.Deno, DenoVersion, fix))
			continue
		}
		src, err := SourceHash(fns.Dir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if src != m.Source {
			errs = append(errs, fmt.Errorf("%s: bundles are stale (sources changed since the last build): %s", fns.Dir, fix))
			continue
		}
		for _, name := range sortedKeys(fns.Functions) {
			b, ok := m.Functions[name]
			if !ok {
				errs = append(errs, fmt.Errorf("%s: function %q has no bundle: %s", fns.Dir, name, fix))
				continue
			}
			data, err := os.ReadFile(fns.BundlePath(m, name))
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: bundle of %q: %v: %s", fns.Dir, name, err, fix))
				continue
			}
			if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != b.SHA256 {
				errs = append(errs, fmt.Errorf("%s: bundle of %q was modified after the build: %s", fns.Dir, name, fix))
			}
		}
	}
	return errors.Join(errs...)
}

// AuthRealms returns the realms with auth enabled (the ones with users, and
// so with jobs), sorted.
func (r *Registry) AuthRealms() []string {
	var out []string
	for _, rl := range r.SortedRealms() {
		if rl.Settings.AuthEnabled {
			out = append(out, rl.Name)
		}
	}
	return out
}

// FunctionRealms returns the realms with at least one function, sorted.
func (r *Registry) FunctionRealms(authEnabled bool) []string {
	var out []string
	for _, rl := range r.SortedRealms() {
		if rl.Settings.AuthEnabled != authEnabled {
			continue
		}
		for _, db := range rl.Databases {
			if db.Functions != nil && len(db.Functions.Functions) > 0 {
				out = append(out, rl.Name)
				break
			}
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// FilesLinkKeySecret is the realm secret that signs backd's own links to files.
const FilesLinkKeySecret = "BACKD_FILES_LINK_KEY"

// reservedRealmSecret reports whether a realm secret is one backd uses itself: with the
// storage keys a function could read the bucket, and with the link key it could sign a
// link to any file.
func reservedRealmSecret(settings RealmSettings, name string) bool {
	if name == FilesLinkKeySecret {
		return true
	}
	return settings.Storage != nil && slices.Contains(settings.Storage.SecretNames(), name)
}
