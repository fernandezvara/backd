package registry

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// RealmFile is the name of the required realm settings file.
const RealmFile = "realm.yaml"

// Signup modes.
const (
	SignupOpen   = "open"
	SignupInvite = "invite"
	SignupClosed = "closed"
)

// Defaults for realm settings left out of realm.yaml.
const (
	DefaultIdleTimeout    = 30 * 24 * time.Hour
	DefaultMaxLifetime    = 90 * 24 * time.Hour
	DefaultAuditRetention = 365 * 24 * time.Hour
	minAuditRetention     = 24 * time.Hour
	// DefaultLogRetention is functions.log_retention's default: a short
	// debugging window, not a log archive (see the docs).
	DefaultLogRetention = 7 * 24 * time.Hour
	minLogRetention     = time.Hour
	// DefaultJobRetention is functions.job_retention's default: how long
	// an async job's result is kept once done (roadmap F11).
	DefaultJobRetention      = 24 * time.Hour
	minJobRetention          = time.Hour
	DefaultPasswordMinLength = 12

	minPasswordLength = 8
	maxPasswordLength = 128
	minSessionTimeout = time.Minute
)

// RealmSettings are the realm-level settings from realm.yaml, with
// defaults applied.
type RealmSettings struct {
	AuthEnabled       bool
	Signup            string
	IdleTimeout       time.Duration
	MaxLifetime       time.Duration
	PasswordMinLength int
	CORSOrigins       []string
	Roles             map[string]Role
	// AdminNetworks restricts every admin API request (admin.allowed_networks);
	// empty means unrestricted.
	AdminNetworks Networks
	// UserNetworks are the network restrictions realm.yaml sets per user
	// email (in a role's user entries).
	UserNetworks map[string]UserNetworks
	// AuditRetention is how long audit records are kept (audit.retention);
	// DefaultAuditRetention when not set.
	AuditRetention time.Duration
	// FunctionsMaxConcurrency caps how many function calls may run at once
	// on this backd instance (functions.max_concurrency); 0 means
	// unlimited. Applies whether or not auth is enabled.
	FunctionsMaxConcurrency int
	// FunctionsLogRetention is how long invocation records (roadmap F10)
	// are kept (functions.log_retention); DefaultLogRetention when not
	// set. Needs auth enabled, like the rest of functions.* except
	// max_concurrency: there's no system database to store them in
	// otherwise.
	FunctionsLogRetention time.Duration
	// FunctionsJobRetention is how long an async job's result (roadmap
	// F11) is kept once done (functions.job_retention); DefaultJobRetention
	// when not set. Needs auth enabled, like FunctionsLogRetention.
	FunctionsJobRetention time.Duration
}

// UserNetworks restricts where one user may act from.
type UserNetworks struct {
	Admin Networks // the user's admin API requests; within AdminNetworks
	Login Networks // login and every request with the user's session
}

// Role is a role declared in realm.yaml.
type Role struct {
	Description string
	// Admin roles let the sessions of users holding them use the admin API.
	Admin bool
	// Users are seed assignments: normalized (trimmed, lowercased) emails.
	Users []string
}

// realmDoc mirrors realm.yaml. Pointers tell "left out" from "set".
type realmDoc struct {
	Auth     *string `yaml:"auth"`
	Signup   *string `yaml:"signup"`
	Sessions *struct {
		IdleTimeout *string `yaml:"idle_timeout"`
		MaxLifetime *string `yaml:"max_lifetime"`
	} `yaml:"sessions"`
	Password *struct {
		MinLength *int `yaml:"min_length"`
	} `yaml:"password"`
	CORS *struct {
		Origins []string `yaml:"origins"`
	} `yaml:"cors"`
	Admin *struct {
		AllowedNetworks []string `yaml:"allowed_networks"`
	} `yaml:"admin"`
	Audit *struct {
		Retention *string `yaml:"retention"`
	} `yaml:"audit"`
	Roles map[string]*struct {
		Description string      `yaml:"description"`
		Admin       bool        `yaml:"admin"`
		Users       []roleEntry `yaml:"users"`
	} `yaml:"roles"`
	Functions *struct {
		MaxConcurrency *int    `yaml:"max_concurrency"`
		LogRetention   *string `yaml:"log_retention"`
		JobRetention   *string `yaml:"job_retention"`
	} `yaml:"functions"`
}

// roleEntry is one user of a role in realm.yaml: an email, or an object
// with an email and settings for that user.
type roleEntry struct {
	Email         string
	AdminNetworks []string
	LoginNetworks []string
}

func (e *roleEntry) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		return n.Decode(&e.Email)
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i].Value, n.Content[i+1]
			switch key {
			case "email":
				if err := val.Decode(&e.Email); err != nil {
					return err
				}
			case "admin_networks":
				if err := val.Decode(&e.AdminNetworks); err != nil {
					return err
				}
			case "login_networks":
				if err := val.Decode(&e.LoginNetworks); err != nil {
					return err
				}
			default:
				return fmt.Errorf("line %d: unknown key %q in a role's user entry (want email, admin_networks or login_networks)", n.Content[i].Line, key)
			}
		}
		if e.Email == "" {
			return fmt.Errorf("line %d: a role's user entry needs an email", n.Line)
		}
		return nil
	}
	return fmt.Errorf("line %d: a role's user entry must be an email or an object with an email", n.Line)
}

// AdminRoles returns the names of the roles declared with admin: true,
// sorted.
func (s RealmSettings) AdminRoles() []string {
	var out []string
	for name, r := range s.Roles {
		if r.Admin {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// IsAdmin reports whether roles include an admin role of the realm.
func (s RealmSettings) IsAdmin(roles []string) bool {
	for _, r := range roles {
		if s.Roles[r].Admin {
			return true
		}
	}
	return false
}

// loadRealmSettings reads and validates <realm>/realm.yaml.
func loadRealmSettings(realmPath string) (RealmSettings, error) {
	path := filepath.Join(realmPath, RealmFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return RealmSettings{}, fmt.Errorf("%s: realm has no %s (create one with `backd template realm --realm %s`)", realmPath, RealmFile, filepath.Base(realmPath))
	}
	if err != nil {
		return RealmSettings{}, fmt.Errorf("%s: %w", path, err)
	}
	s, errs := parseRealmSettings(data)
	for i, e := range errs {
		errs[i] = fmt.Errorf("%s: %w", path, e)
	}
	return s, errors.Join(errs...)
}

func parseRealmSettings(data []byte) (RealmSettings, []error) {
	s := RealmSettings{
		AuthEnabled:       true,
		Signup:            SignupClosed,
		IdleTimeout:       DefaultIdleTimeout,
		MaxLifetime:       DefaultMaxLifetime,
		PasswordMinLength: DefaultPasswordMinLength,
		Roles:             map[string]Role{},
		AuditRetention:    DefaultAuditRetention,
	}

	var doc realmDoc
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return s, []error{fmt.Errorf("invalid YAML: %w", err)}
	}

	var errs []error
	if doc.Auth != nil {
		switch *doc.Auth {
		case "enabled":
		case "disabled":
			s.AuthEnabled = false
		default:
			errs = append(errs, fmt.Errorf("auth: must be enabled or disabled, got %q", *doc.Auth))
		}
	}
	if !s.AuthEnabled {
		// Everything but CORS configures users; reject it rather than ignore it.
		for _, k := range []struct {
			key string
			set bool
		}{
			{"signup", doc.Signup != nil}, {"sessions", doc.Sessions != nil},
			{"password", doc.Password != nil}, {"roles", len(doc.Roles) > 0}, {"admin", doc.Admin != nil},
			{"audit", doc.Audit != nil},
		} {
			if k.set {
				errs = append(errs, fmt.Errorf("%s: only applies when auth is enabled", k.key))
			}
		}
	}

	if doc.Signup != nil {
		switch *doc.Signup {
		case SignupOpen, SignupInvite, SignupClosed:
			s.Signup = *doc.Signup
		default:
			errs = append(errs, fmt.Errorf("signup: must be open, invite or closed, got %q", *doc.Signup))
		}
	}

	if ss := doc.Sessions; ss != nil {
		errs = appendDuration(errs, "sessions.idle_timeout", ss.IdleTimeout, &s.IdleTimeout)
		errs = appendDuration(errs, "sessions.max_lifetime", ss.MaxLifetime, &s.MaxLifetime)
	}
	if s.IdleTimeout > s.MaxLifetime {
		errs = append(errs, fmt.Errorf("sessions: idle_timeout (%s) must not exceed max_lifetime (%s)", s.IdleTimeout, s.MaxLifetime))
	}

	if a := doc.Audit; a != nil && a.Retention != nil {
		d, err := ParseDuration(*a.Retention)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("audit.retention: %w", err))
		case d < minAuditRetention:
			errs = append(errs, fmt.Errorf("audit.retention: must be at least 1d, got %s", *a.Retention))
		default:
			s.AuditRetention = d
		}
	}

	if p := doc.Password; p != nil && p.MinLength != nil {
		n := *p.MinLength
		if n < minPasswordLength || n > maxPasswordLength {
			errs = append(errs, fmt.Errorf("password.min_length: must be between %d and %d, got %d", minPasswordLength, maxPasswordLength, n))
		} else {
			s.PasswordMinLength = n
		}
	}

	if c := doc.CORS; c != nil {
		for i, o := range c.Origins {
			if err := checkOrigin(o, len(c.Origins)); err != nil {
				errs = append(errs, fmt.Errorf("cors.origins[%d]: %w", i, err))
				continue
			}
			s.CORSOrigins = append(s.CORSOrigins, strings.TrimSuffix(o, "/"))
		}
	}

	if fn := doc.Functions; fn != nil && fn.MaxConcurrency != nil {
		n := *fn.MaxConcurrency
		if n < 1 {
			errs = append(errs, fmt.Errorf("functions.max_concurrency: must be at least 1, got %d", n))
		} else {
			s.FunctionsMaxConcurrency = n
		}
	}
	if fn := doc.Functions; fn != nil && fn.LogRetention != nil {
		if !s.AuthEnabled {
			errs = append(errs, errors.New("functions.log_retention: only applies when auth is enabled (this realm has no system database to store invocation records in)"))
		} else {
			d, err := ParseDuration(*fn.LogRetention)
			switch {
			case err != nil:
				errs = append(errs, fmt.Errorf("functions.log_retention: %w", err))
			case d < minLogRetention:
				errs = append(errs, fmt.Errorf("functions.log_retention: must be at least %s, got %s", minLogRetention, *fn.LogRetention))
			default:
				s.FunctionsLogRetention = d
			}
		}
	}
	if fn := doc.Functions; fn != nil && fn.JobRetention != nil {
		if !s.AuthEnabled {
			errs = append(errs, errors.New("functions.job_retention: only applies when auth is enabled (this realm has no system database to store jobs in)"))
		} else {
			d, err := ParseDuration(*fn.JobRetention)
			switch {
			case err != nil:
				errs = append(errs, fmt.Errorf("functions.job_retention: %w", err))
			case d < minJobRetention:
				errs = append(errs, fmt.Errorf("functions.job_retention: must be at least %s, got %s", minJobRetention, *fn.JobRetention))
			default:
				s.FunctionsJobRetention = d
			}
		}
	}

	if a := doc.Admin; a != nil {
		nets, err := ParseNetworks(a.AllowedNetworks)
		if err != nil {
			errs = append(errs, fmt.Errorf("admin.allowed_networks: %w", err))
		}
		s.AdminNetworks = nets
	}

	s.UserNetworks = map[string]UserNetworks{}
	for _, name := range slices.Sorted(maps.Keys(doc.Roles)) {
		r := doc.Roles[name]
		if !namePattern.MatchString(name) {
			errs = append(errs, fmt.Errorf("roles: invalid role name %q: must be lowercase letters and digits, optionally separated by single '-' or '_'", name))
			continue
		}
		role := Role{}
		if r != nil {
			role.Description = r.Description
			role.Admin = r.Admin
			for i, u := range r.Users {
				email, err := NormalizeEmail(u.Email)
				if err != nil {
					errs = append(errs, fmt.Errorf("roles.%s.users[%d]: %w", name, i, err))
					continue
				}
				if slices.Contains(role.Users, email) {
					errs = append(errs, fmt.Errorf("roles.%s.users[%d]: %q is listed twice", name, i, email))
					continue
				}
				role.Users = append(role.Users, email)
				if u.AdminNetworks == nil && u.LoginNetworks == nil {
					continue
				}
				at := fmt.Sprintf("roles.%s.users[%d]", name, i)
				var un UserNetworks
				if un.Admin, err = ParseNetworks(u.AdminNetworks); err != nil {
					errs = append(errs, fmt.Errorf("%s.admin_networks: %w", at, err))
					continue
				}
				if un.Login, err = ParseNetworks(u.LoginNetworks); err != nil {
					errs = append(errs, fmt.Errorf("%s.login_networks: %w", at, err))
					continue
				}
				if !un.Admin.Within(s.AdminNetworks) {
					errs = append(errs, fmt.Errorf("%s.admin_networks: must lie within admin.allowed_networks %v", at, s.AdminNetworks.Strings()))
					continue
				}
				if prev, ok := s.UserNetworks[email]; ok && (!prev.Admin.Equal(un.Admin) || !prev.Login.Equal(un.Login)) {
					errs = append(errs, fmt.Errorf("%s: %q has different networks in another role; set them the same everywhere", at, email))
					continue
				}
				s.UserNetworks[email] = un
			}
		}
		s.Roles[name] = role
	}
	return s, errs
}

func appendDuration(errs []error, key string, v *string, dst *time.Duration) []error {
	if v == nil {
		return errs
	}
	d, err := ParseDuration(*v)
	if err != nil {
		return append(errs, fmt.Errorf("%s: %w", key, err))
	}
	if d < minSessionTimeout {
		return append(errs, fmt.Errorf("%s: must be at least %s, got %s", key, minSessionTimeout, *v))
	}
	*dst = d
	return errs
}

// ParseDuration parses a Go duration ("12h", "90m") or a whole number of
// days ("30d").
func ParseDuration(v string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid duration %q: use a whole number of days (\"30d\") or Go syntax (\"12h\")", v)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: use a whole number of days (\"30d\") or Go syntax (\"12h\")", v)
	}
	return d, nil
}

// checkOrigin accepts "*" (as the only entry) or a scheme://host[:port]
// origin with an http or https scheme.
func checkOrigin(o string, total int) error {
	if o == "*" {
		if total > 1 {
			return errors.New(`"*" must be the only origin`)
		}
		return nil
	}
	u, err := url.Parse(o)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		strings.TrimSuffix(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("invalid origin %q: want scheme://host[:port], e.g. https://app.example.com", o)
	}
	return nil
}

// NormalizeEmail trims and lowercases an email address and checks its
// basic shape (one "@" with text on both sides, no spaces).
func NormalizeEmail(v string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(v))
	local, domain, ok := strings.Cut(e, "@")
	if !ok || local == "" || domain == "" || strings.Contains(domain, "@") || strings.ContainsAny(e, " \t\r\n") {
		return "", fmt.Errorf("invalid email address %q", v)
	}
	return e, nil
}
