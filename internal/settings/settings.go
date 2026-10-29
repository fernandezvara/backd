// Package settings reads backd's runtime configuration from environment variables.
package settings

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/realtime"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/strutil"
)

// ProvisionMode controls what startup provisioning does to MongoDB.
type ProvisionMode string

const (
	// ProvisionApply creates missing collections, validators and indexes.
	ProvisionApply ProvisionMode = "apply"
	// ProvisionVerify only compares MongoDB against the config and fails on differences.
	ProvisionVerify ProvisionMode = "verify"
)

// Settings is the complete runtime configuration.
type Settings struct {
	ConfigDir     string
	MongoURI      string
	HTTPAddr      string
	ProvisionMode ProvisionMode
	LogLevel      slog.Level
	MaxBodyBytes  int64
	// MaxUploadBytes caps one file of a proxy upload (BACKD_MAX_UPLOAD_BYTES), apart from
	// MaxBodyBytes, which is for JSON.
	MaxUploadBytes int64
	// ImageMaxPixels is the most pixels (width × height) an image may have to get
	// versions (BACKD_IMAGE_MAX_PIXELS); a file field may only lower it.
	ImageMaxPixels int64
	// ImageTimeout bounds the making of one image's versions (BACKD_IMAGE_TIMEOUT).
	ImageTimeout time.Duration
	// Expand are the instance's limits for reference expansion (BACKD_EXPAND_MAX_*).
	Expand registry.ExpandLimits
	// Aggregate are the instance's limits for aggregations (BACKD_AGGREGATE_*).
	Aggregate registry.AggregateLimits
	// DeploymentID is BACKD_DEPLOYMENT_ID (default "default"): what keeps the trigger dispatcher
	// of this backd deployment apart from another one's when both share a MongoDB cluster.
	DeploymentID string
	// Env is BACKD_ENV (default empty): what this instance is for, such as development, staging
	// or production. Only imports use it, to warn about data that doesn't belong in it.
	Env string
	// TriggerLimits are the instance's trigger limits (BACKD_TRIGGER_MAX_CHAIN).
	TriggerLimits registry.TriggerLimits
	// Realtime are the instance's realtime limits (BACKD_REALTIME_*); zero fields mean the defaults.
	Realtime realtime.Limits
	// ImageConcurrency is how many images one worker process makes versions of at once
	// (BACKD_IMAGE_CONCURRENCY); 0 derives it from the container's CPU and memory limits.
	ImageConcurrency int
	MongoOpTimeout   time.Duration
	ShutdownTimeout  time.Duration
	// PasswordHashConcurrency caps concurrent password hashes; 0 means
	// auth.DefaultHashConcurrency (CPU and memory limits).
	PasswordHashConcurrency int
	// TrustedProxies are networks whose X-Forwarded-For header is believed.
	TrustedProxies []netip.Prefix
	// Functions: the executor (BACKD_EXECUTOR_URL, BACKD_EXECUTOR_TOKEN),
	// the internal listener it and the functions reach backd on
	// (BACKD_INTERNAL_ADDR, as BACKD_CALLBACK_URL from their side), and the
	// key callback tokens are signed with (BACKD_CALLBACK_KEY).
	ExecutorURL   string
	ExecutorToken string
	// EgressProxy (EXECUTOR_PROXY) and EgressKey (BACKD_EGRESS_KEY) are how push providers are
	// reached: through the egress proxy, with a token the worker signs for each delivery unit.
	// Without a proxy the senders dial the providers directly, and only public addresses.
	EgressProxy  string
	EgressKey    string
	InternalAddr string
	CallbackURL  string
	CallbackKey  string
	// BackdURL is backd's public address (BACKD_URL), the base of the links
	// in emails; a realm's email.public_url overrides it. No trailing slash.
	BackdURL string
	// SecretsKey is the master key functions' secrets (roadmap F5) are
	// encrypted under (BACKD_SECRETS_KEY, or BACKD_SECRETS_KEY_FILE for a
	// file holding it). Empty when neither is set: functions that declare
	// secrets are refused at startup (checked against the config, so it
	// isn't here).
	SecretsKey []byte
	// Dev enables BACKD_DEV: functions rebuild automatically when their
	// sources change. Local development only; serve refuses it unless
	// HTTPAddr is bound to localhost.
	Dev bool
	// DisableAdminAPI is BACKD_ADMIN_API=false: this instance serves no
	// /_admin route at all (they answer 404 like any unknown route), for public
	// instances when an internal one has the admin API.
	DisableAdminAPI bool
	// AdminUI is BACKD_ADMIN_UI=true: this instance serves the admin web
	// interface. It needs the admin API on the same instance.
	AdminUI bool
	// AdminUIIdle is BACKD_ADMIN_UI_IDLE: how long the admin UI may sit
	// untouched before it signs out and revokes its session (default 30m).
	AdminUIIdle time.Duration
	// DevAnyAddr (BACKD_DEV_ANY_ADDR) lets dev mode run on an address that
	// isn't localhost, for a container whose ports are published on the
	// host's localhost only.
	DevAnyAddr bool
	// DevLinks is BACKD_DEV_LINKS: extra links the dev landing page at /
	// shows, "Label=URL;Label=URL" — the local tools next to the API,
	// like a MinIO console. Dev mode only.
	DevLinks string
	// Deno is the deno binary dev mode bundles with; "" means "deno" on
	// the PATH (BACKD_DEV only; `functions build` reads DENO itself).
	Deno string
	// WorkerConcurrency caps how many async jobs one worker process runs
	// at once (WORKER_CONCURRENCY, roadmap F11); default 10.
	WorkerConcurrency int
	// Events is BACKD_EVENTS (default true): this instance may read the change stream for
	// realtime subscribers and serve /_realtime. Triggers is BACKD_TRIGGERS (default true): it may
	// hold the trigger dispatcher's lease. Neither reads anything until there is something to
	// serve (design/triggers-and-realtime.md §3).
	Events   bool
	Triggers bool
	// MetricsAddr (METRICS_ADDR) is where a process serves /metrics; empty
	// means it doesn't. MetricsToken (METRICS_TOKEN) makes it ask for that
	// bearer token. The port must be private (design/metrics.md).
	MetricsAddr  string
	MetricsToken string
}

// LoadMetrics reads METRICS_ADDR and METRICS_TOKEN, shared by every backd
// command that can serve /metrics. reserved are the addresses the process
// serves other things on: metrics must have a listener of their own.
func LoadMetrics(getenv func(string) string, reserved ...string) (addr, token string, err error) {
	addr = strings.TrimSpace(getenv("METRICS_ADDR"))
	token = getenv("METRICS_TOKEN")
	switch {
	case addr == "" && token != "":
		return "", "", errors.New("METRICS_TOKEN needs METRICS_ADDR: there is no metrics listener to protect")
	case addr == "":
		return "", "", nil
	}
	if _, port, perr := net.SplitHostPort(addr); perr != nil || port == "" || port == "0" {
		return "", "", fmt.Errorf("METRICS_ADDR must be host:port or :port, got %q", addr)
	}
	if slices.Contains(reserved, addr) {
		return "", "", fmt.Errorf("METRICS_ADDR %q is an address this process serves something else on: metrics have their own listener, which must stay private", addr)
	}
	if token != "" && len(token) < minSecretLen {
		return "", "", fmt.Errorf("METRICS_TOKEN must be at least %d characters", minSecretLen)
	}
	return addr, token, nil
}

var deploymentIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// minSecretLen is the minimum length of the executor token and callback key.
const minSecretLen = 32

// Load reads settings through getenv (usually os.Getenv). It reports every
// problem at once rather than stopping at the first.
func Load(getenv func(string) string) (Settings, error) {
	s := Settings{
		ConfigDir:     getenv("CONFIG_DIR"),
		MongoURI:      getenv("MONGO_URI"),
		HTTPAddr:      orDefault(getenv("HTTP_ADDR"), ":8080"),
		ProvisionMode: ProvisionMode(orDefault(getenv("PROVISION_MODE"), string(ProvisionApply))),
	}

	var errs []error
	if s.ConfigDir == "" {
		errs = append(errs, errors.New("CONFIG_DIR is required"))
	}
	if s.MongoURI == "" {
		errs = append(errs, errors.New("MONGO_URI is required"))
	}
	if s.ProvisionMode != ProvisionApply && s.ProvisionMode != ProvisionVerify {
		errs = append(errs, fmt.Errorf("PROVISION_MODE must be %q or %q, got %q", ProvisionApply, ProvisionVerify, s.ProvisionMode))
	}

	maxBody := orDefault(getenv("MAX_BODY_BYTES"), "1048576")
	if n, err := envSize(maxBody); err != nil {
		errs = append(errs, fmt.Errorf("MAX_BODY_BYTES: %w", err))
	} else {
		s.MaxBodyBytes = n
	}

	maxUpload := orDefault(getenv("BACKD_MAX_UPLOAD_BYTES"), "104857600")
	if n, err := envSize(maxUpload); err != nil {
		errs = append(errs, fmt.Errorf("BACKD_MAX_UPLOAD_BYTES: %w", err))
	} else {
		s.MaxUploadBytes = n
	}

	if n, err := ImageMaxPixels(getenv); err != nil {
		errs = append(errs, err)
	} else {
		s.ImageMaxPixels = n
	}

	if l, err := ExpandLimitsFrom(getenv); err != nil {
		errs = append(errs, err)
	} else {
		s.Expand = l
	}

	if l, err := AggregateLimitsFrom(getenv); err != nil {
		errs = append(errs, err)
	} else {
		s.Aggregate = l
	}

	if l, err := RealtimeLimitsFrom(getenv); err != nil {
		errs = append(errs, err)
	} else {
		s.Realtime = l
	}
	if l, err := TriggerLimitsFrom(getenv); err != nil {
		errs = append(errs, err)
	} else {
		s.TriggerLimits = l
	}

	if d, err := duration(getenv, "BACKD_IMAGE_TIMEOUT", "30s", false); err != nil {
		errs = append(errs, err)
	} else {
		s.ImageTimeout = d
	}
	if v := getenv("BACKD_IMAGE_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 1 {
			errs = append(errs, fmt.Errorf("BACKD_IMAGE_CONCURRENCY must be a positive integer, got %q", v))
		} else {
			s.ImageConcurrency = n
		}
	}

	if v := getenv("PASSWORD_HASH_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 1 {
			errs = append(errs, fmt.Errorf("PASSWORD_HASH_CONCURRENCY must be a positive integer, got %q", v))
		} else {
			s.PasswordHashConcurrency = n
		}
	}

	for v := range strings.SplitSeq(getenv("TRUSTED_PROXIES"), ",") {
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		p, err := netip.ParsePrefix(v)
		if err != nil {
			a, aerr := netip.ParseAddr(v)
			if aerr != nil {
				errs = append(errs, fmt.Errorf("TRUSTED_PROXIES: %q is not an IP address or CIDR network", v))
				continue
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		s.TrustedProxies = append(s.TrustedProxies, p.Masked())
	}

	switch v := strings.ToLower(getenv("BACKD_DEV")); v {
	case "", "false":
	case "true":
		s.Dev = true
	default:
		errs = append(errs, fmt.Errorf("BACKD_DEV must be true or false, got %q", v))
	}
	s.DeploymentID = orDefault(getenv("BACKD_DEPLOYMENT_ID"), "default")
	if !deploymentIDPattern.MatchString(s.DeploymentID) {
		errs = append(errs, fmt.Errorf("BACKD_DEPLOYMENT_ID must be 1 to 32 characters of a-z, 0-9, - and _, got %q", s.DeploymentID))
	}
	s.Env = strings.ToLower(strings.TrimSpace(getenv("BACKD_ENV")))
	if s.Env != "" && !deploymentIDPattern.MatchString(s.Env) {
		errs = append(errs, fmt.Errorf("BACKD_ENV must be 1 to 32 characters of a-z, 0-9, - and _ (for example production or staging), got %q", s.Env))
	}
	s.Events, s.Triggers = true, true
	for _, flag := range []struct {
		name string
		to   *bool
	}{{"BACKD_EVENTS", &s.Events}, {"BACKD_TRIGGERS", &s.Triggers}} {
		switch v := strings.ToLower(getenv(flag.name)); v {
		case "", "true":
		case "false":
			*flag.to = false
		default:
			errs = append(errs, fmt.Errorf("%s must be true or false, got %q", flag.name, v))
		}
	}
	switch v := strings.ToLower(getenv("BACKD_ADMIN_API")); v {
	case "", "true":
	case "false":
		s.DisableAdminAPI = true
	default:
		errs = append(errs, fmt.Errorf("BACKD_ADMIN_API must be true or false, got %q", v))
	}
	switch v := strings.ToLower(getenv("BACKD_ADMIN_UI")); v {
	case "", "false":
	case "true":
		s.AdminUI = true
	default:
		errs = append(errs, fmt.Errorf("BACKD_ADMIN_UI must be true or false, got %q", v))
	}
	if idle, derr := duration(getenv, "BACKD_ADMIN_UI_IDLE", "30m", false); derr != nil {
		errs = append(errs, derr)
	} else {
		s.AdminUIIdle = idle
	}
	if s.AdminUI && s.DisableAdminAPI {
		errs = append(errs, errors.New("BACKD_ADMIN_UI=true needs the admin API on the same instance, but BACKD_ADMIN_API=false: turn the UI off here, or serve it from the internal instance that has the admin API"))
	}
	switch v := strings.ToLower(getenv("BACKD_DEV_ANY_ADDR")); v {
	case "", "false":
	case "true":
		s.DevAnyAddr = true
	default:
		errs = append(errs, fmt.Errorf("BACKD_DEV_ANY_ADDR must be true or false, got %q", v))
	}
	s.DevLinks = getenv("BACKD_DEV_LINKS")
	s.Deno = getenv("DENO")

	s.ExecutorURL = getenv("BACKD_EXECUTOR_URL")
	s.ExecutorToken = getenv("BACKD_EXECUTOR_TOKEN")
	s.EgressProxy = getenv("EXECUTOR_PROXY")
	s.EgressKey = getenv("BACKD_EGRESS_KEY")
	if s.EgressProxy != "" {
		if !httpURL(s.EgressProxy) {
			errs = append(errs, fmt.Errorf("EXECUTOR_PROXY must be an http(s) URL, got %q", s.EgressProxy))
		}
		if len(s.EgressKey) < minSecretLen {
			errs = append(errs, fmt.Errorf("BACKD_EGRESS_KEY must be at least %d characters when EXECUTOR_PROXY is set", minSecretLen))
		}
	}
	s.InternalAddr = orDefault(getenv("BACKD_INTERNAL_ADDR"), ":8081")
	s.CallbackURL = getenv("BACKD_CALLBACK_URL")
	s.CallbackKey = getenv("BACKD_CALLBACK_KEY")
	if v := strings.TrimSuffix(getenv("BACKD_URL"), "/"); v != "" {
		if !httpURL(v) {
			errs = append(errs, fmt.Errorf("BACKD_URL must be backd's public http(s) address, e.g. https://api.example.com, got %q", v))
		}
		s.BackdURL = v
	}
	if s.ExecutorURL != "" {
		if !httpURL(s.ExecutorURL) {
			errs = append(errs, fmt.Errorf("BACKD_EXECUTOR_URL must be an http(s) URL, got %q", s.ExecutorURL))
		}
		if len(s.ExecutorToken) < minSecretLen {
			errs = append(errs, fmt.Errorf("BACKD_EXECUTOR_TOKEN must be at least %d characters when BACKD_EXECUTOR_URL is set", minSecretLen))
		}
		if !httpURL(s.CallbackURL) {
			errs = append(errs, errors.New("BACKD_CALLBACK_URL must be the http(s) URL functions reach backd's internal listener at (e.g. http://backd:8081) when BACKD_EXECUTOR_URL is set"))
		}
		if len(s.CallbackKey) < minSecretLen {
			errs = append(errs, fmt.Errorf("BACKD_CALLBACK_KEY must be at least %d characters when BACKD_EXECUTOR_URL is set", minSecretLen))
		}
	}

	s.WorkerConcurrency = 10
	if v := getenv("WORKER_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 1 {
			errs = append(errs, fmt.Errorf("WORKER_CONCURRENCY must be a positive integer, got %q", v))
		} else {
			s.WorkerConcurrency = n
		}
	}

	var merr error
	s.MetricsAddr, s.MetricsToken, merr = LoadMetrics(getenv, s.HTTPAddr, s.InternalAddr)
	if merr != nil {
		errs = append(errs, merr)
	}

	secretsKey, err := LoadKeyMaterial(getenv, "BACKD_SECRETS_KEY")
	if err != nil {
		errs = append(errs, err)
	} else if secretsKey != "" {
		if len(secretsKey) < minSecretLen {
			errs = append(errs, fmt.Errorf("BACKD_SECRETS_KEY must be at least %d characters, got %d", minSecretLen, len(secretsKey)))
		} else {
			s.SecretsKey = []byte(secretsKey)
		}
	}

	if s.MongoOpTimeout, err = duration(getenv, "MONGO_OP_TIMEOUT", "10s", false); err != nil {
		errs = append(errs, err)
	}
	if s.ShutdownTimeout, err = duration(getenv, "SHUTDOWN_TIMEOUT", "15s", true); err != nil {
		errs = append(errs, err)
	}

	level := orDefault(getenv("LOG_LEVEL"), "info")
	switch strings.ToLower(level) {
	case "debug":
		s.LogLevel = slog.LevelDebug
	case "info":
		s.LogLevel = slog.LevelInfo
	case "warn":
		s.LogLevel = slog.LevelWarn
	case "error":
		s.LogLevel = slog.LevelError
	default:
		errs = append(errs, fmt.Errorf("LOG_LEVEL must be debug, info, warn or error, got %q", level))
	}

	return s, errors.Join(errs...)
}

// LoadKeyMaterial reads secret key material from the environment
// variable name, or from the file named by name+"_FILE" (trimmed of
// surrounding whitespace); the two are mutually exclusive. Returns "" if
// neither is set. Exported for `backd secret rotate-key`, which reads
// BACKD_SECRETS_NEW_KEY the same way outside of Load.
func LoadKeyMaterial(getenv func(string) string, name string) (string, error) {
	v, file := getenv(name), getenv(name+"_FILE")
	switch {
	case v != "" && file != "":
		return "", fmt.Errorf("%s and %s_FILE can't both be set", name, name)
	case file != "":
		data, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("%s_FILE: %w", name, err)
		}
		return strings.TrimSpace(string(data)), nil
	default:
		return v, nil
	}
}

func httpURL(v string) bool {
	return (strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://")) && len(v) > len("https://")
}

// duration parses a Go duration such as "10s" or "1m30s".
func duration(getenv func(string) string, key, def string, allowZero bool) (time.Duration, error) {
	v := orDefault(getenv(key), def)
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 || (d == 0 && !allowZero) {
		return 0, fmt.Errorf("%s must be a positive duration such as %q, got %q", key, def, v)
	}
	return d, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// ExpandLimitsFrom reads the instance's reference expansion limits
// (BACKD_EXPAND_MAX_DEPTH, _PATHS, _ARRAY and _DOCS). A collection's reference may only lower
// the array limit.
func ExpandLimitsFrom(getenv func(string) string) (registry.ExpandLimits, error) {
	l := registry.DefaultExpandLimits
	var errs []error
	for _, v := range []struct {
		env  string
		dst  *int
		max  int
		hint string
	}{
		{"BACKD_EXPAND_MAX_DEPTH", &l.MaxDepth, 2, "1 or 2"},
		{"BACKD_EXPAND_MAX_PATHS", &l.MaxPaths, 0, "a positive integer"},
		{"BACKD_EXPAND_MAX_ARRAY", &l.MaxArray, 0, "a positive integer"},
		{"BACKD_EXPAND_MAX_DOCS", &l.MaxDocs, 0, "a positive integer"},
	} {
		s := getenv(v.env)
		if s == "" {
			continue
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || (v.max > 0 && n > v.max) {
			errs = append(errs, fmt.Errorf("%s must be %s, got %q", v.env, v.hint, s))
			continue
		}
		*v.dst = n
	}
	return l, errors.Join(errs...)
}

// AggregateLimitsFrom reads the instance's aggregation limits (BACKD_AGGREGATE_TIME and
// BACKD_AGGREGATE_MAX_GROUPS). A collection's aggregate.limits may only lower them.
func AggregateLimitsFrom(getenv func(string) string) (registry.AggregateLimits, error) {
	l := registry.DefaultAggregateLimits
	var errs []error
	if v := getenv("BACKD_AGGREGATE_TIME"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 100*time.Millisecond || d > time.Minute {
			errs = append(errs, fmt.Errorf("BACKD_AGGREGATE_TIME must be a duration between 100ms and 1m, got %q", v))
		} else {
			l.Time = d
		}
	}
	if v := getenv("BACKD_AGGREGATE_MAX_GROUPS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 10000 {
			errs = append(errs, fmt.Errorf("BACKD_AGGREGATE_MAX_GROUPS must be an integer between 1 and 10000, got %q", v))
		} else {
			l.MaxGroups = n
		}
	}
	return l, errors.Join(errs...)
}

// TriggerLimitsFrom reads the instance's trigger limits: BACKD_TRIGGER_MAX_CHAIN, how deep a
// chain of writes caused by trigger runs may go. A realm's `triggers:` may only lower it.
func TriggerLimitsFrom(getenv func(string) string) (registry.TriggerLimits, error) {
	l := registry.DefaultTriggerLimits
	if v := getenv("BACKD_TRIGGER_MAX_CHAIN"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > registry.MaxTriggerChain {
			return l, fmt.Errorf("BACKD_TRIGGER_MAX_CHAIN must be an integer between 1 and %d, got %q", registry.MaxTriggerChain, v)
		}
		l.MaxChain = n
	}
	return l, nil
}

// RealtimeLimitsFrom reads the instance's realtime limits: BACKD_REALTIME_MAX_SUBSCRIPTIONS_PER_STREAM,
// BACKD_REALTIME_MAX_STREAMS_PER_USER, BACKD_REALTIME_MAX_STREAMS, BACKD_REALTIME_BUFFER (bytes per
// stream), BACKD_REALTIME_MEMORY (bytes for all buffers) and BACKD_REALTIME_HEARTBEAT (a duration).
func RealtimeLimitsFrom(getenv func(string) string) (realtime.Limits, error) {
	var l realtime.Limits
	var errs []error
	num := func(name string, to *int) {
		v := getenv(name)
		if v == "" {
			return
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			errs = append(errs, fmt.Errorf("%s must be a positive integer, got %q", name, v))
			return
		}
		*to = n
	}
	num("BACKD_REALTIME_MAX_SUBSCRIPTIONS_PER_STREAM", &l.SubscriptionsPerStream)
	num("BACKD_REALTIME_MAX_STREAMS_PER_USER", &l.StreamsPerUser)
	num("BACKD_REALTIME_MAX_STREAMS", &l.Streams)
	num("BACKD_REALTIME_BUFFER", &l.BufferBytes)
	if v := getenv("BACKD_REALTIME_MEMORY"); v != "" {
		n, err := envSize(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("BACKD_REALTIME_MEMORY: %w", err))
		} else {
			l.Memory = n
		}
	}
	if v := getenv("BACKD_REALTIME_HEARTBEAT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second {
			errs = append(errs, fmt.Errorf("BACKD_REALTIME_HEARTBEAT must be a duration of at least 1s, got %q", v))
		} else {
			l.Heartbeat = d
		}
	}
	return l, errors.Join(errs...)
}

// DefaultImageMaxPixels is the pixel limit for image versions when none is set.
const DefaultImageMaxPixels = 40_000_000

// ImageMaxPixels reads BACKD_IMAGE_MAX_PIXELS: a positive number of pixels.
func ImageMaxPixels(getenv func(string) string) (int64, error) {
	v := getenv("BACKD_IMAGE_MAX_PIXELS")
	if v == "" {
		return DefaultImageMaxPixels, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("BACKD_IMAGE_MAX_PIXELS must be a positive integer, got %q", v)
	}
	return n, nil
}

// envSize reads a size from an environment variable: a whole number of bytes ("1048576") or a
// number with a unit ("1MiB", "1.5GB"), and at least 1 byte.
func envSize(v string) (int64, error) {
	n, err := strutil.ParseSize(v, strutil.SizeBoth, true)
	if err == nil && n < 1 {
		err = fmt.Errorf("invalid size %q: must be at least 1 byte", v)
	}
	return n, err
}
