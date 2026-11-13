package registry

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// The methods a strategy is made of besides the names of providers
// (design/auth-strategies.md §2).
const (
	MethodPassword  = "password"
	MethodPasskey   = "passkey"
	MethodTOTP      = "totp"
	MethodMagicLink = "magic_link"
)

// Steps says where a method may stand in a strategy.
type Steps struct{ First, Later bool }

// methodSteps classifies every method: a new Method constant is not usable until it is
// listed here, and TestEveryMethodIsClassified fails until it is.
var methodSteps = map[string]Steps{
	MethodPassword:  {First: true, Later: false}, // as a later step it needs the engine's password step (not built yet)
	MethodPasskey:   {First: true, Later: true},
	MethodTOTP:      {First: false, Later: true},
	MethodMagicLink: {First: true},
}

// AllMethods lists the Method constants.
var AllMethods = []string{MethodPassword, MethodPasskey, MethodTOTP, MethodMagicLink}

// MethodSteps says where method may stand: a provider's name (anything not built in) can only
// be first.
func MethodSteps(method string) Steps {
	if st, ok := methodSteps[method]; ok {
		return st
	}
	return Steps{First: true}
}

// Strategy is a named, ordered list of methods that a person completes to sign in. Every
// method must succeed, in order; the first one identifies the person.
type Strategy struct {
	Name    string
	Methods []string
}

// HasLaterStep says whether method is one of the steps after the first.
func (st Strategy) HasLaterStep(method string) bool {
	return len(st.Methods) > 1 && slices.Contains(st.Methods[1:], method)
}

// PasskeySettings are the realm's passkey settings (auth.passkeys in realm.yaml,
// design/passkeys.md §3). RPID and Origins are filled from sign_in.app_url when left out.
type PasskeySettings struct {
	Enabled bool
	RPID    string
	RPName  string
	Origins []string
}

// StrategyByName returns the strategy with that name.
func (s RealmSettings) StrategyByName(name string) (Strategy, bool) {
	for _, st := range s.Strategies {
		if st.Name == name {
			return st, true
		}
	}
	return Strategy{}, false
}

type sessionsDoc struct {
	IdleTimeout      *string `yaml:"idle_timeout"`
	MaxLifetime      *string `yaml:"max_lifetime"`
	AdminIdleTimeout *string `yaml:"admin_idle_timeout"`
	AdminMaxLifetime *string `yaml:"admin_max_lifetime"`
	Cookie           *struct {
		Enabled  *bool   `yaml:"enabled"`
		SameSite *string `yaml:"same_site"`
	} `yaml:"cookie"`
}

type passwordDoc struct {
	MinLength *int `yaml:"min_length"`
}

// totpDoc is auth.totp, which replaces the old mfa section.
type totpDoc struct {
	Enabled *bool  `yaml:"enabled"`
	Issuer  string `yaml:"issuer"`
}

type passkeysDoc struct {
	Enabled bool     `yaml:"enabled"`
	RPID    string   `yaml:"rp_id"`
	RPName  string   `yaml:"rp_name"`
	Origins []string `yaml:"origins"`
}

// magicLinkDoc lets users sign in with a link or a code sent to their address.
type magicLinkDoc struct {
	Enabled bool `yaml:"enabled"`
	Signup  bool `yaml:"signup"`
}

// reauthDoc is how long a fresh proof of identity, asked for before a sensitive action,
// stays good.
type reauthDoc struct {
	TTL string `yaml:"ttl"`
}

// signInDoc are the settings that go with signing in, shared by magic links, providers and
// the hosted pages.
type signInDoc struct {
	AllowedRedirects []string `yaml:"allowed_redirects"`
	AppURL           string   `yaml:"app_url"`
}

// authDoc is the auth section of realm.yaml when it is a map: the strategies and every
// setting about signing in.
type authDoc struct {
	Strategies    yaml.Node               `yaml:"strategies"`
	Signup        *string                 `yaml:"signup"`
	Sessions      *sessionsDoc            `yaml:"sessions"`
	Password      *passwordDoc            `yaml:"password"`
	LoginThrottle *loginThrottleDoc       `yaml:"login_throttle"`
	TOTP          *totpDoc                `yaml:"totp"`
	Passkeys      *passkeysDoc            `yaml:"passkeys"`
	MagicLink     *magicLinkDoc           `yaml:"magic_link"`
	Reauth        *reauthDoc              `yaml:"reauth"`
	SignIn        *signInDoc              `yaml:"sign_in"`
	Providers     map[string]*providerDoc `yaml:"providers"`
}

// movedKeys are the top-level keys of the old realm.yaml shape and where each one lives now.
var movedKeys = map[string]string{
	"signup":         "auth.signup",
	"sessions":       "auth.sessions",
	"password":       "auth.password",
	"login_throttle": "auth.login_throttle",
	"providers":      "auth.providers",
	"sign_in":        "auth.sign_in",
	"magic_link":     "auth.magic_link",
	"reauth":         "auth.reauth",
	"totp":           "auth.totp",
	"passkeys":       "auth.passkeys",
	"mfa":            "auth.totp (`mfa.required_roles` is replaced by `strategies` on the roles that need a second step, for example admin_password: [password, totp])",
}

// parseAuth reads the auth key and the keys that used to sit beside it. enabled is false for
// the explicit `auth: disabled`, which has no sections.
func parseAuth(n yaml.Node, extra map[string]yaml.Node) (a authDoc, enabled bool, errs []error) {
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return extra[keys[i]].Line < extra[keys[j]].Line })
	for _, k := range keys {
		if to, ok := movedKeys[k]; ok {
			errs = append(errs, fmt.Errorf("line %d: %s: has moved, it is now %s (see design/auth-strategies.md §3.4)", extra[k].Line, k, to))
			continue
		}
		errs = append(errs, fmt.Errorf("line %d: unknown key %q", extra[k].Line, k))
	}

	switch n.Kind {
	case 0:
		errs = append(errs, errors.New("auth: is missing: write `auth: disabled` for a realm without authentication, or an auth section with `strategies` (for example `auth: {strategies: {password: [password]}}`)"))
		return a, false, errs
	case yaml.ScalarNode:
		switch n.Value {
		case "disabled":
			return a, false, errs
		case "enabled":
			errs = append(errs, fmt.Errorf("line %d: auth: `enabled` is gone: write an auth section with `strategies` (for example `auth: {strategies: {password: [password]}}`); `auth: disabled` stays the way to run without authentication", n.Line))
		default:
			errs = append(errs, fmt.Errorf("line %d: auth: must be `disabled` or a section with `strategies`, got %q", n.Line, n.Value))
		}
		return a, false, errs
	case yaml.MappingNode:
	default:
		errs = append(errs, fmt.Errorf("line %d: auth: must be `disabled` or a section with `strategies`", n.Line))
		return a, false, errs
	}

	// Decoded again with unknown keys refused, as the whole file is.
	b, err := yaml.Marshal(&n)
	if err != nil {
		return a, true, append(errs, fmt.Errorf("auth: %w", err))
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&a); err != nil && !errors.Is(err, io.EOF) {
		return a, true, append(errs, fmt.Errorf("auth: %w", err))
	}
	if a.Strategies.Kind != yaml.MappingNode || len(a.Strategies.Content) == 0 {
		errs = append(errs, errors.New("auth: needs `strategies`: at least one named list of methods, for example `strategies: {password: [password]}` (`auth: disabled` runs without authentication)"))
	}
	return a, true, errs
}

// parseStrategies reads auth.strategies and checks it against what is configured: the
// providers, the email section, totp and passkeys. It runs after those and the roles are read.
func (s *RealmSettings) parseStrategies(a authDoc) []error {
	n := a.Strategies
	if n.Kind != yaml.MappingNode {
		return nil
	}
	var errs []error
	for i := 0; i+1 < len(n.Content); i += 2 {
		name, list := n.Content[i].Value, n.Content[i+1]
		at := "auth.strategies." + name
		if !namePattern.MatchString(name) || len(name) > 64 {
			errs = append(errs, fmt.Errorf("line %d: auth.strategies: %q is not a valid name (lowercase letters, digits, - and _, up to 64 characters)", n.Content[i].Line, name))
			continue
		}
		if _, dup := s.StrategyByName(name); dup {
			errs = append(errs, fmt.Errorf("line %d: auth.strategies: %q is declared twice", n.Content[i].Line, name))
			continue
		}
		if list.Kind != yaml.SequenceNode || len(list.Content) == 0 {
			errs = append(errs, fmt.Errorf("line %d: %s: must be a non-empty list of methods, for example [password, totp]", list.Line, at))
			continue
		}
		st := Strategy{Name: name}
		ok := true
		for j, m := range list.Content {
			method := m.Value
			if m.Kind != yaml.ScalarNode || method == "" {
				errs = append(errs, fmt.Errorf("line %d: %s[%d]: must be a method name", m.Line, at, j))
				ok = false
				continue
			}
			if msg := s.methodProblem(method, j); msg != "" {
				errs = append(errs, fmt.Errorf("line %d: %s[%d]: %s", m.Line, at, j, msg))
				ok = false
				continue
			}
			if slices.Contains(st.Methods, method) {
				errs = append(errs, fmt.Errorf("line %d: %s: lists %q twice", m.Line, at, method))
				ok = false
				continue
			}
			st.Methods = append(st.Methods, method)
		}
		if ok {
			s.Strategies = append(s.Strategies, st)
		}
	}
	return errs
}

// methodProblem says what is wrong with method at position pos of a strategy, or "".
func (s RealmSettings) methodProblem(method string, pos int) string {
	switch method {
	case MethodPassword:
	case MethodTOTP:
		if pos == 0 {
			return "totp cannot be the first method: it needs a method before it that says who is signing in"
		}
		if s.MFA.Off {
			return "totp is disabled (auth.totp.enabled: false)"
		}
	case MethodPasskey:
		if !s.Passkeys.Enabled {
			return "passkey needs auth.passkeys.enabled: true (and its rp_id and origins)"
		}
	case MethodMagicLink:
		if !s.MagicLink.Enabled {
			return "magic_link needs auth.magic_link.enabled: true (and the email section)"
		}
	case "recovery_code":
		return "recovery codes are not a method: they stand in for totp wherever a strategy lists totp"
	default:
		if _, ok := s.Providers[method]; !ok {
			return fmt.Sprintf("%q is not a method (password, passkey, totp, magic_link) or a provider declared in auth.providers", method)
		}
	}
	if pos > 0 && !MethodSteps(method).Later {
		return fmt.Sprintf("%s can only be the first method: a later step is totp or passkey", method)
	}
	return ""
}

// checkRoleStrategies validates the strategies the roles list.
func (s *RealmSettings) checkRoleStrategies() []error {
	var errs []error
	for _, name := range sortedRoleNames(s.Roles) {
		role := s.Roles[name]
		if role.Strategies == nil {
			continue
		}
		if len(role.Strategies) == 0 {
			errs = append(errs, fmt.Errorf("roles.%s.strategies: is empty: leave the key out to allow every strategy", name))
			continue
		}
		seen := map[string]bool{}
		for i, sn := range role.Strategies {
			switch _, ok := s.StrategyByName(sn); {
			case !ok:
				errs = append(errs, fmt.Errorf("roles.%s.strategies[%d]: no strategy named %q in auth.strategies", name, i, sn))
			case seen[sn]:
				errs = append(errs, fmt.Errorf("roles.%s.strategies[%d]: %q is listed twice", name, i, sn))
			}
			seen[sn] = true
		}
	}
	return errs
}

func sortedRoleNames(roles map[string]Role) []string {
	names := make([]string, 0, len(roles))
	for n := range roles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// parsePasskeys validates auth.passkeys and fills rp_id and the origins from sign_in.app_url
// when they are left out.
func (s *RealmSettings) parsePasskeys(p *passkeysDoc) []error {
	if p == nil {
		return nil
	}
	s.Passkeys = PasskeySettings{Enabled: p.Enabled, RPID: strings.ToLower(strings.TrimSpace(p.RPID)), RPName: strings.TrimSpace(p.RPName), Origins: slices.Clone(p.Origins)}
	if !p.Enabled {
		return nil
	}
	var errs []error
	var appOrigin, appHost string
	if s.SignInAppURL != "" {
		if u, err := url.Parse(s.SignInAppURL); err == nil && u.Host != "" {
			appOrigin, appHost = u.Scheme+"://"+u.Host, u.Hostname()
		}
	}
	if s.Passkeys.RPID == "" {
		s.Passkeys.RPID = appHost
	}
	if s.Passkeys.RPID == "" {
		errs = append(errs, errors.New("auth.passkeys.rp_id: is needed (the domain passkeys are bound to; it cannot be derived because auth.sign_in.app_url is not set)"))
	}
	if len(s.Passkeys.Origins) == 0 && appOrigin != "" {
		s.Passkeys.Origins = []string{appOrigin}
	}
	if len(s.Passkeys.Origins) == 0 {
		errs = append(errs, errors.New("auth.passkeys.origins: lists no origin (where the ceremony may run; it cannot be derived because auth.sign_in.app_url is not set)"))
	}
	for i, o := range s.Passkeys.Origins {
		if strings.HasPrefix(o, "android:apk-key-hash:") && len(o) > len("android:apk-key-hash:") {
			continue
		}
		u, err := url.Parse(o)
		switch {
		case err != nil || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "":
			errs = append(errs, fmt.Errorf("auth.passkeys.origins[%d]: %q must be an origin such as https://app.example.com, or android:apk-key-hash:<hash>", i, o))
		case u.Scheme != "https" && (u.Scheme != "http" || !isLoopbackHost(u.Hostname())):
			errs = append(errs, fmt.Errorf("auth.passkeys.origins[%d]: %q must be https (http only for localhost)", i, o))
		case s.Passkeys.RPID != "" && u.Hostname() != s.Passkeys.RPID && !strings.HasSuffix(u.Hostname(), "."+s.Passkeys.RPID):
			errs = append(errs, fmt.Errorf("auth.passkeys.origins[%d]: the host of %q must be the rp_id (%s) or a subdomain of it: browsers refuse passkeys otherwise", i, o, s.Passkeys.RPID))
		}
	}
	return errs
}

func isLoopbackHost(h string) bool {
	return h == "localhost" || strings.HasSuffix(h, ".localhost") || h == "127.0.0.1" || h == "::1"
}

// The errors of choosing a strategy to sign in with.
var (
	// ErrStrategyRequired: the call named no strategy and more than one starts with its method.
	ErrStrategyRequired = errors.New("strategy is required: more than one strategy starts with this method")
	// ErrUnknownStrategy: no strategy of that name, or it does not start with the method of
	// the call. The same answer for both, so it tells nothing else.
	ErrUnknownStrategy = errors.New("unknown strategy")
)

// StrategyFor chooses the strategy a first-step call signs in with. name may be empty when
// exactly one strategy starts with first (the call's method: password, magic_link or a
// provider). Settings built by hand without strategies (tests) answer an unnamed strategy
// of that one method; a parsed realm always has strategies.
func (s RealmSettings) StrategyFor(name, first string) (Strategy, error) {
	if len(s.Strategies) == 0 {
		if name != "" {
			return Strategy{}, ErrUnknownStrategy
		}
		return Strategy{Methods: []string{first}}, nil
	}
	if name != "" {
		st, ok := s.StrategyByName(name)
		if !ok || st.Methods[0] != first {
			return Strategy{}, ErrUnknownStrategy
		}
		return st, nil
	}
	var found []Strategy
	for _, st := range s.Strategies {
		if st.Methods[0] == first {
			found = append(found, st)
		}
	}
	switch len(found) {
	case 0:
		return Strategy{}, ErrUnknownStrategy
	case 1:
		return found[0], nil
	}
	return Strategy{}, ErrStrategyRequired
}

// ActiveRoles splits the roles a user holds by a session made with strategy: a role is
// active when it lists no strategies or lists that one. A session without a strategy (made
// before strategies, or through an organization's connection) keeps every role.
func (s RealmSettings) ActiveRoles(held []string, strategy string) (active, inactive []string) {
	for _, r := range held {
		list := s.Roles[r].Strategies
		if strategy == "" || len(list) == 0 || slices.Contains(list, strategy) {
			active = append(active, r)
		} else {
			inactive = append(inactive, r)
		}
	}
	return active, inactive
}

// TOTPRequired says whether a user with these roles has to enrol an authenticator: every
// strategy of the realm ends in a totp step, or one of the roles accepts only strategies that do.
func (s RealmSettings) TOTPRequired(roles []string) bool {
	if len(s.Strategies) == 0 {
		return false
	}
	all := true
	for _, st := range s.Strategies {
		if !st.HasLaterStep(MethodTOTP) {
			all = false
		}
	}
	if all {
		return true
	}
	for _, r := range roles {
		list := s.Roles[r].Strategies
		if len(list) == 0 {
			continue
		}
		every := true
		for _, sn := range list {
			if st, ok := s.StrategyByName(sn); !ok || !st.HasLaterStep(MethodTOTP) {
				every = false
			}
		}
		if every {
			return true
		}
	}
	return false
}

// TOTPRoles lists the roles that need a totp step, and whether every user does.
func (s RealmSettings) TOTPRoles() (roles []string, everyone bool) {
	if s.TOTPRequired(nil) {
		return nil, true
	}
	for _, r := range sortedRoleNames(s.Roles) {
		if s.TOTPRequired([]string{r}) {
			roles = append(roles, r)
		}
	}
	return roles, false
}
