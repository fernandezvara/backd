package registry

import (
	"fmt"
	"time"
)

// Defaults of login_throttle in realm.yaml: failures are counted per account
// and per client address in a sliding window, and past a threshold each new
// attempt waits twice as long as the one before, up to MaxDelay.
const (
	DefaultAccountThreshold = 5
	DefaultIPThreshold      = 50
	DefaultThrottleWindow   = 15 * time.Minute
	DefaultMaxDelay         = 15 * time.Minute

	minThrottleWindow = time.Minute
	minMaxDelay       = time.Second
)

// LoginThrottle is how a realm slows down guessing of passwords
// (login_throttle). The zero value means the defaults.
type LoginThrottle struct {
	// AccountThreshold is how many failed attempts an account gets before
	// each new attempt must wait; IPThreshold is the same per client address
	// (IPv6 per /64), higher because a network can hide many people.
	AccountThreshold int
	IPThreshold      int
	// Window is how long a failure counts: the counter of an account or an
	// address expires this long after its last failure.
	Window time.Duration
	// MaxDelay is the longest anyone waits. It never exceeds Window.
	MaxDelay time.Duration
}

// Throttle is the realm's login throttling, with the defaults filled in
// for what is left out.
func (s RealmSettings) Throttle() LoginThrottle {
	t := s.LoginThrottle
	if t.AccountThreshold <= 0 {
		t.AccountThreshold = DefaultAccountThreshold
	}
	if t.IPThreshold <= 0 {
		t.IPThreshold = DefaultIPThreshold
	}
	if t.Window <= 0 {
		t.Window = DefaultThrottleWindow
	}
	if t.MaxDelay <= 0 {
		t.MaxDelay = DefaultMaxDelay
	}
	return t
}

// loginThrottleDoc is login_throttle in realm.yaml.
type loginThrottleDoc struct {
	AccountThreshold *int    `yaml:"account_threshold"`
	IPThreshold      *int    `yaml:"ip_threshold"`
	Window           *string `yaml:"window"`
	MaxDelay         *string `yaml:"max_delay"`
}

// apply validates the section and sets it on s (starting from the defaults).
func (d *loginThrottleDoc) apply(s *RealmSettings) []error {
	t := LoginThrottle{
		AccountThreshold: DefaultAccountThreshold, IPThreshold: DefaultIPThreshold,
		Window: DefaultThrottleWindow, MaxDelay: DefaultMaxDelay,
	}
	var errs []error
	count := func(key string, v *int, dst *int) {
		switch {
		case v == nil:
		case *v < 1:
			errs = append(errs, fmt.Errorf("login_throttle.%s: must be at least 1, got %d", key, *v))
		default:
			*dst = *v
		}
	}
	duration := func(key string, v *string, min time.Duration, dst *time.Duration) {
		if v == nil {
			return
		}
		got, err := ParseDuration(*v)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("login_throttle.%s: %w", key, err))
		case got < min:
			errs = append(errs, fmt.Errorf("login_throttle.%s: must be at least %s, got %s", key, min, *v))
		default:
			*dst = got
		}
	}
	count("account_threshold", d.AccountThreshold, &t.AccountThreshold)
	count("ip_threshold", d.IPThreshold, &t.IPThreshold)
	duration("window", d.Window, minThrottleWindow, &t.Window)
	duration("max_delay", d.MaxDelay, minMaxDelay, &t.MaxDelay)
	if t.MaxDelay > t.Window {
		errs = append(errs, fmt.Errorf("login_throttle: max_delay (%s) must not exceed window (%s): a counter expires after the window, so a longer wait could never be served", t.MaxDelay, t.Window))
	}
	s.LoginThrottle = t
	return errs
}
