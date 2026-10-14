package registry

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	// MaxIDLength is the longest id any collection accepts.
	MaxIDLength = 128
	// maxCustomIDPattern is the longest pattern a collection may declare, in characters.
	maxCustomIDPattern = 512
	// DefaultDuplicateLimit is how many duplicate creates a caller may attempt per minute when
	// the collection doesn't say.
	DefaultDuplicateLimit = 20
	// MaxDuplicateLimit is the largest duplicate_limit.
	MaxDuplicateLimit = 1000
)

// CustomID is collection.yaml's custom_id: the collection takes the id of a new document from
// the client, which must match Pattern (or be the creating user's id) and the safety envelope.
type CustomID struct {
	// User means the id must equal the id of the user creating the document.
	User bool
	// Pattern is the declared pattern as written; empty with User.
	Pattern string
	// DuplicateLimit is how many creates of an id that exists a caller may attempt per minute.
	DuplicateLimit int
	re             *regexp.Regexp
}

// customIDDoc is custom_id in collection.yaml: `user`, or a mapping of pattern and
// duplicate_limit.
type customIDDoc struct {
	User           bool
	Pattern        *string
	DuplicateLimit *int
}

func (d *customIDDoc) UnmarshalYAML(n *yaml.Node) error {
	const want = "custom_id must be `user` or a mapping (pattern, duplicate_limit)"
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Value != "user" {
			return fmt.Errorf("line %d: %s, got %q", n.Line, want, n.Value)
		}
		d.User = true
		return nil
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			v := n.Content[i+1]
			switch key := n.Content[i].Value; key {
			case "pattern":
				var s string
				if err := v.Decode(&s); err != nil {
					return fmt.Errorf("line %d: custom_id.pattern must be a string", v.Line)
				}
				d.Pattern = &s
			case "duplicate_limit":
				var k int
				if err := v.Decode(&k); err != nil {
					return fmt.Errorf("line %d: custom_id.duplicate_limit must be a whole number", v.Line)
				}
				d.DuplicateLimit = &k
			default:
				return fmt.Errorf("line %d: unknown key %q in custom_id (want pattern, duplicate_limit)", n.Content[i].Line, key)
			}
		}
		return nil
	}
	return fmt.Errorf("line %d: %s", n.Line, want)
}

// settings validates the section; nil means the server generates the ids.
func (d *customIDDoc) settings(path string, authEnabled bool) (*CustomID, error) {
	if d == nil {
		return nil, nil
	}
	limit := DefaultDuplicateLimit
	if d.DuplicateLimit != nil {
		limit = *d.DuplicateLimit
		if limit < 1 || limit > MaxDuplicateLimit {
			return nil, fmt.Errorf("%s: custom_id.duplicate_limit must be between 1 and %d (got %d)", path, MaxDuplicateLimit, limit)
		}
	}
	if d.User {
		if !authEnabled {
			return nil, fmt.Errorf("%s: custom_id: user only applies when the realm has auth enabled (realm.yaml has `auth: disabled`: it has no users)", path)
		}
		return &CustomID{User: true, DuplicateLimit: limit}, nil
	}
	if d.Pattern == nil || strings.TrimSpace(*d.Pattern) == "" {
		return nil, fmt.Errorf("%s: custom_id needs a pattern (or `custom_id: user`)", path)
	}
	if len(*d.Pattern) > maxCustomIDPattern {
		return nil, fmt.Errorf("%s: custom_id.pattern is %d characters long, at most %d", path, len(*d.Pattern), maxCustomIDPattern)
	}
	re, err := regexp.Compile(`^(?:` + *d.Pattern + `)$`)
	if err != nil {
		return nil, fmt.Errorf("%s: custom_id.pattern isn't a valid regular expression (RE2 syntax): %w", path, err)
	}
	return &CustomID{Pattern: *d.Pattern, DuplicateLimit: limit, re: re}, nil
}

// Anchored is the pattern as it is matched, for JSON Schema: the whole id must match.
func (c *CustomID) Anchored() string {
	if c.User {
		return ""
	}
	return "^(?:" + c.Pattern + ")$"
}

// Check reports why id is not acceptable for the collection, or "" when it is. It checks the
// safety envelope first, then the pattern; the `user` form is checked by the caller, which
// knows the user.
func (c *CustomID) Check(id string) string {
	if reason := EnvelopeProblem(id); reason != "" {
		return reason
	}
	if c.re != nil && !c.re.MatchString(id) {
		return "the id doesn't match the collection's pattern"
	}
	return ""
}

// EnvelopeProblem checks what every custom id must satisfy whatever the collection's pattern:
// 1 to 128 characters, only letters, digits and - . _ ~ : @, not starting with _ or . and
// without "..". It returns "" when id is inside the envelope.
func EnvelopeProblem(id string) string {
	switch {
	case id == "":
		return "the id is empty"
	case len(id) > MaxIDLength:
		return fmt.Sprintf("the id is longer than %d characters", MaxIDLength)
	case id[0] == '_' || id[0] == '.':
		return "the id must not start with _ or ."
	case strings.Contains(id, ".."):
		return "the id must not contain .."
	}
	for i := 0; i < len(id); i++ {
		b := id[i]
		switch {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		case strings.IndexByte("-._~:@", b) >= 0:
		default:
			return "the id may only have letters, digits and - . _ ~ : @"
		}
	}
	return ""
}

// loadCustomID reads collection.yaml's custom_id into c. It runs before the rules are
// compiled, which can then name data.id. The other sections are read elsewhere.
func loadCustomID(c *Collection, settings RealmSettings, path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var doc struct {
		CustomID *customIDDoc `yaml:"custom_id"`
	}
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		// Another section's mistake is reported by the loader that reads it; this one only
		// speaks of its own.
		if strings.Contains(err.Error(), "custom_id") {
			return fmt.Errorf("%s: %w", path, err)
		}
		return nil
	}
	cid, err := doc.CustomID.settings(path, settings.AuthEnabled)
	if err != nil {
		return err
	}
	c.CustomID = cid
	return nil
}
