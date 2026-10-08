package registry

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/fernandezvara/backd/internal/email"
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

// CookieSettings are the realm's session cookies.
type CookieSettings struct {
	Enabled bool
	// SameSite is lax (default), strict or none. none is for apps on another
	// site than the API (another registrable domain); lax and strict also work
	// for an app on a sibling subdomain.
	SameSite string
}

// RealmSettings are the realm-level settings from realm.yaml, with
// defaults applied.
type RealmSettings struct {
	AuthEnabled bool
	Signup      string
	IdleTimeout time.Duration
	MaxLifetime time.Duration
	// AdminIdleTimeout and AdminMaxLifetime are the limits of the sessions of
	// users who hold an admin role (sessions.admin_idle_timeout and
	// sessions.admin_max_lifetime). 0 means the regular limit; set, they never
	// exceed it.
	AdminIdleTimeout time.Duration
	AdminMaxLifetime time.Duration
	// Cookie lets browser apps keep a session in an HttpOnly cookie, when
	// their login asks for it (sessions.cookie).
	Cookie            CookieSettings
	PasswordMinLength int
	// LoginThrottle is login_throttle; read it through Throttle(), which
	// fills in the defaults.
	LoginThrottle LoginThrottle
	CORSOrigins   []string
	Roles         map[string]Role
	// AdminNetworks restricts every admin API request (admin.allowed_networks);
	// empty means unrestricted.
	AdminNetworks Networks
	// ReadAccess is what read-only administrators (`admin: read`) may see
	// beyond operations (admin.read_access).
	ReadAccess ReadAccess
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
	// Email is how the realm sends email (email in realm.yaml); nil when it
	// doesn't, and then no email is ever sent.
	Email *EmailSettings
	// Storage is where the realm's files live (storage in realm.yaml); nil
	// when it has none, and then it can't declare file fields.
	Storage *StorageSettings
	// Account is how the realm's accounts behave (account in realm.yaml).
	Account AccountSettings
	// Providers are the external identity providers (providers in realm.yaml),
	// by name; SignInRedirects are where a sign-in may send the user back to
	// (sign_in.allowed_redirects, normalized like email.allowed_redirects).
	Providers       map[string]*Provider
	SignInRedirects []string
}

// AccountSettings are the account lifecycle settings of a realm.
type AccountSettings struct {
	// RequireVerifiedEmail: no session until the address is verified.
	RequireVerifiedEmail bool
	// WelcomeEmail sends the welcome email once the address is verified.
	WelcomeEmail bool
	// VerifyEmailTTL is how long a verification link works (account.tokens.verify_email).
	VerifyEmailTTL time.Duration
	// AllowEmailChange lets users change their own address (`POST /_auth/email`).
	AllowEmailChange bool
	// ChangeEmailTTL and RevertEmailChangeTTL are how long the confirmation
	// and the undo link of an email change work.
	ChangeEmailTTL, RevertEmailChangeTTL time.Duration
	// ResetPasswordTTL is how long a reset link works (account.tokens.reset_password).
	ResetPasswordTTL time.Duration
	// PurgeUnverifiedAfter deletes accounts that never verified; 0 is off.
	PurgeUnverifiedAfter time.Duration
	// OnSignup is the internal, async function (<database>/<function>) called for every
	// new user (account.on_signup), to create the app's profile document; OnSignupTimeout
	// is its timeout.
	OnSignup        string
	OnSignupTimeout time.Duration
}

// OnSignupDatabaseAndName splits OnSignup.
func (a AccountSettings) OnSignupDatabaseAndName() (string, string) {
	db, name, _ := strings.Cut(a.OnSignup, "/")
	return db, name
}

// TokenLifetime is how long a token of this purpose works in the realm.
func (a AccountSettings) TokenLifetime(p email.Purpose) time.Duration {
	switch {
	case p == email.TokenPurpose(email.VerifyEmail) && a.VerifyEmailTTL > 0:
		return a.VerifyEmailTTL
	case p == email.TokenPurpose(email.ResetPassword) && a.ResetPasswordTTL > 0:
		return a.ResetPasswordTTL
	case p == email.TokenPurpose(email.ChangeEmail) && a.ChangeEmailTTL > 0:
		return a.ChangeEmailTTL
	case p == email.TokenPurpose(email.EmailChanged) && a.RevertEmailChangeTTL > 0:
		return a.RevertEmailChangeTTL
	}
	return p.DefaultLifetime()
}

// DefaultResetPasswordTTL is account.tokens.reset_password when not set.
const DefaultResetPasswordTTL = time.Hour

// DefaultVerifyEmailTTL is account.tokens.verify_email when not set.
const DefaultVerifyEmailTTL = 48 * time.Hour

// Defaults of email.limits.
const (
	DefaultEmailPerKindPerHour = 3
	DefaultEmailPerDay         = 10
	DefaultEmailPerIPPerHour   = 20
	// The caps of custom emails from functions.
	DefaultEmailPerFunctionPerHour   = 200
	DefaultEmailPerInvocation        = 50
	DefaultEmailRecipientsPerMessage = 10
	// DefaultLocale is email.default_locale when not set.
	DefaultLocale = "en"
)

// EmailSettings are the email settings of a realm: which function delivers
// its messages and who they come from. backd renders the messages and never
// sends one itself.
type EmailSettings struct {
	Function      string // <database>/<function>: internal and async
	From          string // "Name <address>" or an address
	ReplyTo       string // optional
	PublicURL     string // overrides BACKD_URL for this realm's links; no trailing slash
	DefaultLocale string
	Locales       []string // every one needs every required template
	Limits        EmailLimits
	// RedirectDelay is how long a hosted result page waits before it sends the
	// user on (email.redirect_delay).
	RedirectDelay time.Duration
	// Redirects are where the hosted pages send users afterwards, by flow
	// (verify_email, reset_password, change_email, invitation), when the
	// request that caused the email named no other place.
	Redirects map[string]string
	// AllowedRedirects are the origins (https://app.example.com) and app
	// schemes (acme://) that redirect_to, redirects and links may point at.
	AllowedRedirects []string
	// Links replace the hosted page of a flow with a page of the app: the
	// link in the email is this URL, with {token} replaced.
	Links map[string]string
	// Timeout is the delivery function's own timeout, set when the config is
	// loaded; email jobs lease for it.
	Timeout time.Duration
}

// EmailLimits bound how many emails one address, and one client address,
// can cause.
type EmailLimits struct {
	PerKindPerHour int // emails of one kind to one recipient, per hour
	PerDay         int // emails to one recipient, per day
	PerIPPerHour   int // email-sending requests from one client address, per hour
	// The caps of custom emails from functions (ctx.email.send), whoever the
	// recipients are.
	PerFunctionPerHour   int // messages one function sends, per hour
	PerInvocation        int // messages one invocation sends
	RecipientsPerMessage int // to, cc and bcc of one message
}

// DatabaseAndName splits Function into the database and the function name.
func (e EmailSettings) DatabaseAndName() (string, string) {
	db, name, _ := strings.Cut(e.Function, "/")
	return db, name
}

// UserNetworks restricts where one user may act from.
type UserNetworks struct {
	Admin Networks // the user's admin API requests; within AdminNetworks
	Login Networks // login and every request with the user's session
}

// Role is a role declared in realm.yaml.
type Role struct {
	Description string
	// Admin is what the sessions of users holding the role may do in the
	// admin API: AllRights for `admin: true`, some areas for a list, none.
	Admin AdminRights
	// AdminRead is `admin: read` (alone or in a list): every area may be read.
	AdminRead bool
	// Users are seed assignments: normalized (trimmed, lowercased) emails.
	Users []string
}

// realmDoc mirrors realm.yaml. Pointers tell "left out" from "set".
type realmDoc struct {
	Auth     *string `yaml:"auth"`
	Signup   *string `yaml:"signup"`
	Sessions *struct {
		IdleTimeout      *string `yaml:"idle_timeout"`
		MaxLifetime      *string `yaml:"max_lifetime"`
		AdminIdleTimeout *string `yaml:"admin_idle_timeout"`
		AdminMaxLifetime *string `yaml:"admin_max_lifetime"`
		Cookie           *struct {
			Enabled  *bool   `yaml:"enabled"`
			SameSite *string `yaml:"same_site"`
		} `yaml:"cookie"`
	} `yaml:"sessions"`
	Password *struct {
		MinLength *int `yaml:"min_length"`
	} `yaml:"password"`
	LoginThrottle *loginThrottleDoc `yaml:"login_throttle"`
	CORS          *struct {
		Origins []string `yaml:"origins"`
	} `yaml:"cors"`
	Admin *struct {
		AllowedNetworks []string `yaml:"allowed_networks"`
		ReadAccess      *struct {
			Users bool `yaml:"users"`
			Data  bool `yaml:"data"`
		} `yaml:"read_access"`
	} `yaml:"admin"`
	Audit *struct {
		Retention *string `yaml:"retention"`
	} `yaml:"audit"`
	Roles map[string]*struct {
		Description string      `yaml:"description"`
		Admin       adminDoc    `yaml:"admin"`
		Users       []roleEntry `yaml:"users"`
	} `yaml:"roles"`
	Email   *emailDoc   `yaml:"email"`
	Storage *storageDoc `yaml:"storage"`
	// Providers are the external identity providers users may sign in with, and
	// SignIn the settings that go with signing in.
	Providers map[string]*providerDoc `yaml:"providers"`
	SignIn    *struct {
		AllowedRedirects []string `yaml:"allowed_redirects"`
	} `yaml:"sign_in"`
	Account *struct {
		RequireVerifiedEmail bool   `yaml:"require_verified_email"`
		WelcomeEmail         bool   `yaml:"welcome_email"`
		AllowEmailChange     bool   `yaml:"allow_email_change"`
		PurgeUnverifiedAfter string `yaml:"purge_unverified_after"`
		OnSignup             string `yaml:"on_signup"`
		Tokens               *struct {
			VerifyEmail       string `yaml:"verify_email"`
			ResetPassword     string `yaml:"reset_password"`
			ChangeEmail       string `yaml:"change_email"`
			RevertEmailChange string `yaml:"revert_email_change"`
		} `yaml:"tokens"`
	} `yaml:"account"`
	Functions *struct {
		MaxConcurrency *int    `yaml:"max_concurrency"`
		LogRetention   *string `yaml:"log_retention"`
		JobRetention   *string `yaml:"job_retention"`
	} `yaml:"functions"`
}

// emailDoc mirrors the email section of realm.yaml.
type emailDoc struct {
	Function         string            `yaml:"function"`
	From             string            `yaml:"from"`
	ReplyTo          string            `yaml:"reply_to"`
	PublicURL        string            `yaml:"public_url"`
	DefaultLocale    string            `yaml:"default_locale"`
	Locales          []string          `yaml:"locales"`
	RedirectDelay    string            `yaml:"redirect_delay"`
	Redirects        map[string]string `yaml:"redirects"`
	AllowedRedirects []string          `yaml:"allowed_redirects"`
	Links            map[string]string `yaml:"links"`
	Limits           *struct {
		PerRecipient *struct {
			PerKindPerHour *int `yaml:"per_kind_per_hour"`
			PerDay         *int `yaml:"per_day"`
		} `yaml:"per_recipient"`
		PerIP *struct {
			PerHour *int `yaml:"per_hour"`
		} `yaml:"per_ip"`
		PerFunction *struct {
			PerHour       *int `yaml:"per_hour"`
			PerInvocation *int `yaml:"per_invocation"`
		} `yaml:"per_function"`
		RecipientsPerMessage *int `yaml:"recipients_per_message"`
	} `yaml:"limits"`
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

// AdminRoles returns the names of the roles that open any part of the admin
// API (admin: true, or a list of areas), sorted.
func (s RealmSettings) AdminRoles() []string {
	var out []string
	for name, r := range s.Roles {
		if r.Admin.Any() || r.AdminRead {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// cmpDuration is d, or fallback when d is unset (0).
func cmpDuration(d, fallback time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return fallback
}

// IsAdmin reports whether roles include an admin role of the realm.
func (s RealmSettings) IsAdmin(roles []string) bool {
	for _, r := range roles {
		if role := s.Roles[r]; role.Admin.Any() || role.AdminRead {
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
			{"password", doc.Password != nil}, {"login_throttle", doc.LoginThrottle != nil}, {"roles", len(doc.Roles) > 0}, {"admin", doc.Admin != nil},
			{"audit", doc.Audit != nil},
		} {
			if k.set {
				errs = append(errs, fmt.Errorf("%s: only applies when auth is enabled", k.key))
			}
		}
	}

	if doc.LoginThrottle != nil {
		errs = append(errs, doc.LoginThrottle.apply(&s)...)
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
	// Admin sessions: unset (0) means the regular limits; set, they only
	// shorten them.
	if ss := doc.Sessions; ss != nil && (ss.AdminIdleTimeout != nil || ss.AdminMaxLifetime != nil) {
		errs = appendDuration(errs, "sessions.admin_idle_timeout", ss.AdminIdleTimeout, &s.AdminIdleTimeout)
		errs = appendDuration(errs, "sessions.admin_max_lifetime", ss.AdminMaxLifetime, &s.AdminMaxLifetime)
		idle, max := cmpDuration(s.AdminIdleTimeout, s.IdleTimeout), cmpDuration(s.AdminMaxLifetime, s.MaxLifetime)
		if idle > s.IdleTimeout {
			errs = append(errs, fmt.Errorf("sessions.admin_idle_timeout (%s) must not exceed idle_timeout (%s): it is for shortening admin sessions", idle, s.IdleTimeout))
		}
		if max > s.MaxLifetime {
			errs = append(errs, fmt.Errorf("sessions.admin_max_lifetime (%s) must not exceed max_lifetime (%s): it is for shortening admin sessions", max, s.MaxLifetime))
		}
		if idle > max {
			errs = append(errs, fmt.Errorf("sessions: admin_idle_timeout (%s) must not exceed admin_max_lifetime (%s)", idle, max))
		}
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

	if ss := doc.Sessions; ss != nil && ss.Cookie != nil {
		c := ss.Cookie
		s.Cookie.SameSite = "lax"
		if c.SameSite != nil {
			switch v := strings.ToLower(*c.SameSite); v {
			case "lax", "strict", "none":
				s.Cookie.SameSite = v
			default:
				errs = append(errs, fmt.Errorf("sessions.cookie.same_site: must be lax, strict or none, got %q", *c.SameSite))
			}
		}
		if c.Enabled != nil && *c.Enabled {
			s.Cookie.Enabled = true
			// A cookie is sent by the browser on its own: which sites may use it
			// must be said, not guessed.
			if slices.Contains(s.CORSOrigins, "*") {
				errs = append(errs, errors.New("sessions.cookie: cookies can't be used with cors.origins: ['*']; list the origins of your apps"))
			}
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

	if e := doc.Email; e != nil {
		if !s.AuthEnabled {
			errs = append(errs, errors.New("email: only applies when auth is enabled"))
		} else {
			var es []error
			s.Email, es = parseEmail(e)
			errs = append(errs, es...)
		}
	}

	if st := doc.Storage; st != nil {
		if !s.AuthEnabled {
			errs = append(errs, errors.New("storage: only applies when auth is enabled (its access keys are secrets of the realm's system database)"))
		} else {
			var es []error
			s.Storage, es = parseStorage(st)
			errs = append(errs, es...)
		}
	}

	if len(doc.Providers) > 0 || doc.SignIn != nil {
		if !s.AuthEnabled {
			errs = append(errs, errors.New("providers: only apply when auth is enabled"))
		} else {
			var es []error
			s.Providers, es = parseProviders(doc.Providers)
			errs = append(errs, es...)
			if si := doc.SignIn; si != nil {
				for i, o := range si.AllowedRedirects {
					norm, ok := normalizeRedirectOrigin(o)
					switch {
					case !ok:
						errs = append(errs, fmt.Errorf("sign_in.allowed_redirects[%d]: %q must be an origin such as https://app.example.com (no path), or an app scheme such as acme://", i, o))
					case slices.Contains(s.SignInRedirects, norm):
						errs = append(errs, fmt.Errorf("sign_in.allowed_redirects[%d]: %q is listed twice", i, o))
					default:
						s.SignInRedirects = append(s.SignInRedirects, norm)
					}
				}
			}
			if len(s.Providers) > 0 && len(s.SignInRedirects) == 0 && (s.Email == nil || len(s.Email.AllowedRedirects) == 0) {
				errs = append(errs, errors.New("providers: need sign_in.allowed_redirects (or email.allowed_redirects): the origins and app schemes a sign-in may send the user back to"))
			}
		}
	}

	s.Account.VerifyEmailTTL = DefaultVerifyEmailTTL
	s.Account.ResetPasswordTTL = DefaultResetPasswordTTL
	s.Account.ChangeEmailTTL = 24 * time.Hour
	s.Account.RevertEmailChangeTTL = 7 * 24 * time.Hour
	if a := doc.Account; a != nil {
		if !s.AuthEnabled {
			errs = append(errs, errors.New("account: only applies when auth is enabled"))
		} else {
			s.Account.RequireVerifiedEmail, s.Account.WelcomeEmail, s.Account.AllowEmailChange = a.RequireVerifiedEmail, a.WelcomeEmail, a.AllowEmailChange
			for key, on := range map[string]bool{"require_verified_email": a.RequireVerifiedEmail, "welcome_email": a.WelcomeEmail, "allow_email_change": a.AllowEmailChange, "purge_unverified_after": a.PurgeUnverifiedAfter != ""} {
				if on && s.Email == nil {
					errs = append(errs, fmt.Errorf("account.%s: needs an email section (the emails it sends go through its delivery function)", key))
				}
			}
			dur := func(key, v string, dst *time.Duration) {
				if v == "" {
					return
				}
				d, err := ParseDuration(v)
				switch {
				case err != nil:
					errs = append(errs, fmt.Errorf("account.%s: %w", key, err))
				case d < time.Minute:
					errs = append(errs, fmt.Errorf("account.%s: must be at least 1m, got %s", key, v))
				default:
					*dst = d
				}
			}
			dur("purge_unverified_after", a.PurgeUnverifiedAfter, &s.Account.PurgeUnverifiedAfter)
			if a.OnSignup != "" {
				db, name, ok := strings.Cut(a.OnSignup, "/")
				if !ok || !namePattern.MatchString(db) || !namePattern.MatchString(name) {
					errs = append(errs, fmt.Errorf("account.on_signup: must be <database>/<function> (the internal, async function called for every new user), got %q", a.OnSignup))
				} else {
					s.Account.OnSignup = a.OnSignup
				}
			}
			if a.Tokens != nil {
				dur("tokens.verify_email", a.Tokens.VerifyEmail, &s.Account.VerifyEmailTTL)
				dur("tokens.reset_password", a.Tokens.ResetPassword, &s.Account.ResetPasswordTTL)
				dur("tokens.change_email", a.Tokens.ChangeEmail, &s.Account.ChangeEmailTTL)
				dur("tokens.revert_email_change", a.Tokens.RevertEmailChange, &s.Account.RevertEmailChangeTTL)
			}
		}
	}

	if a := doc.Admin; a != nil {
		nets, err := ParseNetworks(a.AllowedNetworks)
		if err != nil {
			errs = append(errs, fmt.Errorf("admin.allowed_networks: %w", err))
		}
		s.AdminNetworks = nets
		if ra := a.ReadAccess; ra != nil {
			s.ReadAccess = ReadAccess{Users: ra.Users, Data: ra.Data}
		}
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
			role.Admin = r.Admin.Rights
			role.AdminRead = r.Admin.Read
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
// basic shape (one "@" with text on both sides, no spaces). The reserved
// ".invalid" top-level domain (RFC 2606) is refused: it never receives mail,
// and erased users' placeholder addresses live there, so nobody may take one.
// Characters that delimit addresses or break a header (commas, angle
// brackets, quotes, control characters...) are refused as well: a delivery
// function that joins recipients with commas must never see one address
// that is really two.
func NormalizeEmail(v string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(v))
	local, domain, ok := strings.Cut(e, "@")
	if !ok || local == "" || domain == "" || strings.Contains(domain, "@") || strings.ContainsAny(e, " \t\r\n") {
		return "", fmt.Errorf("invalid email address %q", v)
	}
	if len(e) > 254 || strings.ContainsFunc(e, func(r rune) bool { return r < ' ' || r == 0x7f || strings.ContainsRune(`,;:<>()[]\"`, r) }) {
		return "", fmt.Errorf("invalid email address %q: it has characters that would split or break a message header", v)
	}
	if domain == "invalid" || strings.HasSuffix(domain, ".invalid") {
		return "", fmt.Errorf("invalid email address %q: the .invalid domain is reserved", v)
	}
	return e, nil
}

// parseEmail checks the email section of realm.yaml.
func parseEmail(d *emailDoc) (*EmailSettings, []error) {
	e := &EmailSettings{
		Function: d.Function, From: d.From, ReplyTo: d.ReplyTo, DefaultLocale: DefaultLocale,
		Limits: EmailLimits{PerKindPerHour: DefaultEmailPerKindPerHour, PerDay: DefaultEmailPerDay, PerIPPerHour: DefaultEmailPerIPPerHour,
			PerFunctionPerHour: DefaultEmailPerFunctionPerHour, PerInvocation: DefaultEmailPerInvocation, RecipientsPerMessage: DefaultEmailRecipientsPerMessage},
	}
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf("email."+format, args...)) }

	db, name, ok := strings.Cut(d.Function, "/")
	if !ok || !namePattern.MatchString(db) || !namePattern.MatchString(name) {
		add("function: must be <database>/<function> (the internal, async function that delivers the messages), got %q", d.Function)
	}
	if d.From == "" {
		add("from: is required: the sender of every message, as an address or \"Name <address>\"")
	} else if _, err := mail.ParseAddress(d.From); err != nil {
		add("from: %q isn't a valid address (want an address or \"Name <address>\")", d.From)
	}
	if d.ReplyTo != "" {
		if _, err := mail.ParseAddress(d.ReplyTo); err != nil {
			add("reply_to: %q isn't a valid address", d.ReplyTo)
		}
	}
	if d.PublicURL != "" {
		u, err := url.Parse(d.PublicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			add("public_url: %q isn't an absolute http(s) URL (backd's public address, e.g. https://api.example.com)", d.PublicURL)
		} else {
			e.PublicURL = strings.TrimSuffix(d.PublicURL, "/")
		}
	}
	if d.DefaultLocale != "" {
		e.DefaultLocale = d.DefaultLocale
	}
	if !localePattern.MatchString(e.DefaultLocale) {
		add("default_locale: %q isn't a language tag such as en or es-MX", e.DefaultLocale)
	}
	e.Locales = []string{e.DefaultLocale}
	if len(d.Locales) > 0 {
		e.Locales = nil
		for i, l := range d.Locales {
			switch {
			case !localePattern.MatchString(l):
				add("locales[%d]: %q isn't a language tag such as en or es-MX", i, l)
			case slices.Contains(e.Locales, l):
				add("locales[%d]: %q is listed twice", i, l)
			default:
				e.Locales = append(e.Locales, l)
			}
		}
		if !slices.Contains(e.Locales, e.DefaultLocale) {
			add("locales: must include default_locale (%s)", e.DefaultLocale)
		}
	}
	if l := d.Limits; l != nil {
		limit := func(key string, v *int, dst *int) {
			switch {
			case v == nil:
			case *v < 1:
				add("limits.%s: must be at least 1, got %d", key, *v)
			default:
				*dst = *v
			}
		}
		if r := l.PerRecipient; r != nil {
			limit("per_recipient.per_kind_per_hour", r.PerKindPerHour, &e.Limits.PerKindPerHour)
			limit("per_recipient.per_day", r.PerDay, &e.Limits.PerDay)
		}
		if ip := l.PerIP; ip != nil {
			limit("per_ip.per_hour", ip.PerHour, &e.Limits.PerIPPerHour)
		}
		if f := l.PerFunction; f != nil {
			limit("per_function.per_hour", f.PerHour, &e.Limits.PerFunctionPerHour)
			limit("per_function.per_invocation", f.PerInvocation, &e.Limits.PerInvocation)
		}
		limit("recipients_per_message", l.RecipientsPerMessage, &e.Limits.RecipientsPerMessage)
	}
	errs = append(errs, parseRedirects(e, d)...)
	return e, errs
}

// Flows the hosted pages and links cover, by realm.yaml key.
var redirectKeys = []string{"verify_email", "reset_password", "change_email", "invitation"}
var linkKeys = []string{"verify_email", "reset_password", "change_email", "revert_email_change", "invitation"}

// DefaultRedirectDelay is email.redirect_delay when not set.
const DefaultRedirectDelay = 3 * time.Second

func parseRedirects(e *EmailSettings, d *emailDoc) []error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf("email."+format, args...)) }
	e.RedirectDelay = DefaultRedirectDelay
	if d.RedirectDelay != "" {
		dur, err := ParseDuration(d.RedirectDelay)
		switch {
		case err != nil:
			add("redirect_delay: %v", err)
		case dur > 30*time.Second:
			add("redirect_delay: must be at most 30s, got %s", d.RedirectDelay)
		default:
			e.RedirectDelay = dur
		}
	}
	for i, o := range d.AllowedRedirects {
		norm, ok := normalizeRedirectOrigin(o)
		switch {
		case !ok:
			add("allowed_redirects[%d]: %q must be an origin such as https://app.example.com (no path), or an app scheme such as acme://", i, o)
		case slices.Contains(e.AllowedRedirects, norm):
			add("allowed_redirects[%d]: %q is listed twice", i, o)
		default:
			e.AllowedRedirects = append(e.AllowedRedirects, norm)
		}
	}
	e.Redirects = map[string]string{}
	for _, k := range slices.Sorted(maps.Keys(d.Redirects)) {
		v := d.Redirects[k]
		switch {
		case !slices.Contains(redirectKeys, k):
			add("redirects: unknown flow %q (want %s)", k, strings.Join(redirectKeys, ", "))
		case !e.AllowedRedirect(v):
			add("redirects.%s: %q must be an absolute URL within allowed_redirects", k, v)
		default:
			e.Redirects[k] = v
		}
	}
	e.Links = map[string]string{}
	for _, k := range slices.Sorted(maps.Keys(d.Links)) {
		v := d.Links[k]
		switch {
		case !slices.Contains(linkKeys, k):
			add("links: unknown flow %q (want %s)", k, strings.Join(linkKeys, ", "))
		case strings.Count(v, "{token}") != 1:
			add("links.%s: %q must contain {token} exactly once", k, v)
		case !e.AllowedRedirect(strings.Replace(v, "{token}", "x", 1)):
			add("links.%s: %q must be an absolute URL within allowed_redirects", k, v)
		default:
			e.Links[k] = v
		}
	}
	return errs
}

// normalizeRedirectOrigin accepts an http(s) origin without a path, or an app
// scheme with nothing after it (acme://), and returns it lower-cased.
func normalizeRedirectOrigin(o string) (string, bool) {
	o = strings.TrimSpace(o)
	if scheme, rest, ok := strings.Cut(o, "://"); ok && rest == "" && schemePattern.MatchString(scheme) {
		switch s := strings.ToLower(scheme); s {
		case "http", "https", "javascript", "data", "vbscript", "file", "blob":
			return "", false
		default:
			return s + "://", true
		}
	}
	o = strings.TrimSuffix(o, "/")
	u, err := url.Parse(o)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", false
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), true
}

var schemePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*$`)

// AllowedRedirect reports whether raw is an absolute URL on one of the
// allowed origins or app schemes: where a page may send a user.
func (e EmailSettings) AllowedRedirect(raw string) bool {
	return redirectAllowed(e.AllowedRedirects, raw)
}

func redirectAllowed(allowed []string, raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.User != nil || strings.ContainsAny(raw, " \t\r\n") {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "http" || scheme == "https" {
		if u.Host == "" {
			return false
		}
		return slices.Contains(allowed, scheme+"://"+strings.ToLower(u.Host))
	}
	return slices.Contains(allowed, scheme+"://")
}

var localePattern = regexp.MustCompile(`^[a-z]{2,3}(?:-[A-Za-z0-9]{2,8})*$`)
