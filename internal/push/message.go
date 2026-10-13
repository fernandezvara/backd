package push

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// The portable message of design §6.3: the same fields for every platform, with a
// passthrough for what only one of them has.

// Limits of the portable fields.
const (
	// MaxDataBytes is the serialized size of Message.Data.
	MaxDataBytes = 2048
	// MaxTTL is the longest time providers keep a push (FCM's four weeks).
	MaxTTL = 28 * 24 * time.Hour
	// DefaultTTL is how long a push is kept when the message doesn't say.
	DefaultTTL = 24 * time.Hour
	// maxGroupLength is Web Push's Topic limit, the smallest of the three providers'.
	maxGroupLength = 32
	maxLinkLength  = 2048
	maxSoundLength = 64
	maxKeyLength   = 64
)

// The priorities.
const (
	PriorityNormal = "normal"
	PriorityHigh   = "high"
)

// Message is what is sent to a user's devices, in their language.
type Message struct {
	// Kind is the template the text came from, or RawKind.
	Kind        string
	Title, Body string
	Link        string
	// Data is delivered to the app as it is: strings only, at most MaxDataBytes serialized.
	Data  map[string]string
	Badge *int
	Sound string
	// Group replaces an earlier push with the same key (collapse key, collapse id, topic).
	Group string
	// TTL is how long providers keep trying; nil is DefaultTTL, zero is now or never.
	TTL      *time.Duration
	Priority string
	// Silent is data only: no visible notification.
	Silent bool
	// Platform holds keys android, ios and web, merged into that platform's payload.
	Platform map[string]map[string]any
}

// ValidationError is a message field that can't be sent.
type ValidationError struct{ Field, Reason string }

func (e *ValidationError) Error() string { return e.Field + ": " + e.Reason }

func invalid(field, format string, args ...any) error {
	return &ValidationError{Field: field, Reason: fmt.Sprintf(format, args...)}
}

// Validate checks the portable fields strictly. It expects the text to be in place (rendered
// from a template, or written by the function).
func (m Message) Validate() error {
	if m.Silent {
		switch {
		case len(m.Data) == 0:
			return invalid("data", "a silent push carries only data: it is required")
		case m.Title != "" || m.Body != "":
			return invalid("title", "a silent push shows nothing: it takes no title or body")
		case m.Kind != "" && m.Kind != RawKind:
			return invalid("kind", "a silent push has no template: it takes no kind")
		case m.Badge != nil:
			return invalid("badge", "a silent push shows nothing: it takes no badge")
		case m.Sound != "":
			return invalid("sound", "a silent push makes no sound")
		}
	} else {
		if strings.TrimSpace(m.Title) == "" {
			return invalid("title", "is required")
		}
		if strings.TrimSpace(m.Body) == "" {
			return invalid("body", "is required")
		}
	}
	for _, f := range []struct{ name, v string }{{"title", m.Title}, {"body", m.Body}, {"link", m.Link}, {"sound", m.Sound}, {"group", m.Group}} {
		if !utf8.ValidString(f.v) || strings.ContainsFunc(f.v, func(r rune) bool { return r < ' ' && r != '\n' && r != '\t' }) {
			return invalid(f.name, "must be text without control characters")
		}
	}
	if m.Link != "" {
		u, err := url.Parse(m.Link)
		if err != nil || len(m.Link) > maxLinkLength || strings.ContainsAny(m.Link, " \n\t") || (u.Scheme == "" && !strings.HasPrefix(m.Link, "/")) {
			return invalid("link", "must be a URL (https://…, an app scheme such as acme://orders/1) or a path starting with /, at most %d characters", maxLinkLength)
		}
	}
	if len(m.Sound) > maxSoundLength {
		return invalid("sound", "at most %d characters", maxSoundLength)
	}
	if m.Group != "" {
		if len(m.Group) > maxGroupLength || strings.Trim(m.Group, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" {
			return invalid("group", "must be 1 to %d letters, digits, _ or - (the smallest key of the three providers)", maxGroupLength)
		}
	}
	if m.Badge != nil && *m.Badge < 0 {
		return invalid("badge", "must be 0 or more")
	}
	if m.TTL != nil && (*m.TTL < 0 || *m.TTL > MaxTTL) {
		return invalid("ttl", "must be between 0 and 28 days")
	}
	if m.Priority != "" && m.Priority != PriorityNormal && m.Priority != PriorityHigh {
		return invalid("priority", "must be normal or high")
	}
	for k, v := range m.Data {
		switch {
		case k == "" || len(k) > maxKeyLength:
			return invalid("data", "keys are 1 to %d characters", maxKeyLength)
		case reservedDataKey(k):
			return invalid("data."+k, "is a name FCM reserves: choose another key")
		case !utf8.ValidString(v):
			return invalid("data."+k, "must be text")
		}
	}
	if len(m.Data) > 0 {
		b, _ := json.Marshal(m.Data)
		if len(b) > MaxDataBytes {
			return invalid("data", "is %d bytes serialized; at most %d", len(b), MaxDataBytes)
		}
	}
	for k, v := range m.Platform {
		if !slices.Contains([]string{"android", "ios", "web"}, k) {
			return invalid("platform."+k, "unknown platform: use android, ios or web")
		}
		if _, err := json.Marshal(v); err != nil {
			return invalid("platform."+k, "must be a JSON object")
		}
	}
	return nil
}

// reservedDataKey says whether FCM refuses the key in a data message.
func reservedDataKey(k string) bool {
	if slices.Contains([]string{"from", "message_type", "collapse_key", "link"}, k) {
		return true
	}
	return strings.HasPrefix(k, "google") || strings.HasPrefix(k, "gcm")
}

// TTLOrDefault is the message's time to live.
func (m Message) TTLOrDefault() time.Duration {
	if m.TTL == nil {
		return DefaultTTL
	}
	return *m.TTL
}
