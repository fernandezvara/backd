// Package settings reads backd's runtime configuration from environment variables.
package settings

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
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
	ConfigDir       string
	MongoURI        string
	HTTPAddr        string
	ProvisionMode   ProvisionMode
	LogLevel        slog.Level
	MaxBodyBytes    int64
	MongoOpTimeout  time.Duration
	ShutdownTimeout time.Duration
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
	InternalAddr  string
	CallbackURL   string
	CallbackKey   string
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
	// DevAnyAddr (BACKD_DEV_ANY_ADDR) lets dev mode run on an address that
	// isn't localhost, for a container whose ports are published on the
	// host's localhost only.
	DevAnyAddr bool
	// Deno is the deno binary dev mode bundles with; "" means "deno" on
	// the PATH (BACKD_DEV only; `functions build` reads DENO itself).
	Deno string
	// WorkerConcurrency caps how many async jobs one worker process runs
	// at once (WORKER_CONCURRENCY, roadmap F11); default 10.
	WorkerConcurrency int
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
	if n, err := strconv.ParseInt(maxBody, 10, 64); err != nil || n < 1 {
		errs = append(errs, fmt.Errorf("MAX_BODY_BYTES must be a positive integer, got %q", maxBody))
	} else {
		s.MaxBodyBytes = n
	}

	if v := getenv("PASSWORD_HASH_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 1 {
			errs = append(errs, fmt.Errorf("PASSWORD_HASH_CONCURRENCY must be a positive integer, got %q", v))
		} else {
			s.PasswordHashConcurrency = n
		}
	}

	for _, v := range strings.Split(getenv("TRUSTED_PROXIES"), ",") {
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
	s.Deno = getenv("DENO")

	s.ExecutorURL = getenv("BACKD_EXECUTOR_URL")
	s.ExecutorToken = getenv("BACKD_EXECUTOR_TOKEN")
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
