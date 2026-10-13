package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// ConsentsDir is the folder of a realm that holds the consent texts:
// <realm>/consents/<name>/<version>/<locale>.md. It is never read as a database.
const ConsentsDir = "consents"

// DefaultConsentRetention is how long consent records are kept after their
// user was erased, when the realm doesn't say (compliance.consent_retention).
const DefaultConsentRetention = 3 * 365 * 24 * time.Hour

// ConsentProof names what a consent record stores besides time, method and text hash.
const (
	ProofIP        = "ip"
	ProofUserAgent = "user_agent"
)

var (
	consentNamePattern    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	consentVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)
)

// Consent is one consent a realm asks for (consents in realm.yaml): a name, the
// version in force, and the texts of every version, per language.
type Consent struct {
	Name string
	// Required consents must be accepted to have an account; optional ones can be
	// given and withdrawn at will.
	Required bool
	// Current is the version new accounts accept.
	Current string
	// Versions are the versions realm.yaml says something about, plus the current one.
	Versions map[string]*ConsentVersion
	// Texts holds the loaded texts by version and locale.
	Texts map[string]map[string]ConsentText
}

// ConsentVersion is what realm.yaml says about one version of a consent.
type ConsentVersion struct {
	// Reaccept asks users who accepted an earlier version to accept this one.
	Reaccept bool
	// ReacceptBy is the date after which open sessions of users who haven't accepted are
	// refused; the zero time means sessions continue until the user signs in again.
	ReacceptBy time.Time
}

// ConsentText is the exact text of a version in one language, with its SHA-256.
type ConsentText struct {
	Body string
	Hash string // hex SHA-256 of Body
}

// ComplianceSettings are the realm's compliance settings (compliance in realm.yaml).
type ComplianceSettings struct {
	// ConsentProof lists what each consent record stores besides time, method and
	// text hash: ProofIP and ProofUserAgent. Both by default.
	ConsentProof []string
	// ConsentRetention is how long records are kept after erasure; 0 deletes them at erasure.
	ConsentRetention time.Duration
	// ConsentRetentionSet says whether realm.yaml chose a retention (0 is a choice).
	ConsentRetentionSet bool
}

// Retention is how long consent records outlive their user: the realm's choice, or the default.
func (c ComplianceSettings) Retention() time.Duration {
	if c.ConsentRetentionSet {
		return c.ConsentRetention
	}
	return DefaultConsentRetention
}

// Proof reports whether records store the given proof ("ip" or "user_agent").
func (c ComplianceSettings) Proof(what string) bool {
	if c.ConsentProof == nil {
		return true
	}
	return slices.Contains(c.ConsentProof, what)
}

type consentDoc struct {
	Required bool                      `yaml:"required"`
	Current  string                    `yaml:"current"`
	Versions map[string]*consentVerDoc `yaml:"versions"`
}

type consentVerDoc struct {
	Reaccept   bool   `yaml:"reaccept"`
	ReacceptBy string `yaml:"reaccept_by"`
}

type complianceDoc struct {
	ConsentProof     []string `yaml:"consent_proof"`
	ConsentRetention *string  `yaml:"consent_retention"`
}

// parseConsents reads the consents section of realm.yaml.
func parseConsents(n yaml.Node) (map[string]*Consent, []error) {
	if n.Kind == 0 {
		return nil, nil
	}
	var doc map[string]*consentDoc
	if err := n.Decode(&doc); err != nil {
		return nil, []error{fmt.Errorf("consents: want a mapping of consent name to its settings: %w", err)}
	}
	var errs []error
	out := map[string]*Consent{}
	for _, name := range slices.Sorted(mapKeys(doc)) {
		d := doc[name]
		if d == nil {
			errs = append(errs, fmt.Errorf("consents.%s: needs at least `current`", name))
			continue
		}
		if !consentNamePattern.MatchString(name) {
			errs = append(errs, fmt.Errorf("consents.%s: the name must be lower-case letters, digits and underscores, starting with a letter (at most 32 characters)", name))
			continue
		}
		c := &Consent{Name: name, Required: d.Required, Current: d.Current, Versions: map[string]*ConsentVersion{}, Texts: map[string]map[string]ConsentText{}}
		if !consentVersionPattern.MatchString(c.Current) {
			errs = append(errs, fmt.Errorf("consents.%s.current: %q must be a version such as 2026-10 or 3 (letters, digits, dots, dashes and underscores; at most 32 characters)", name, c.Current))
			continue
		}
		c.Versions[c.Current] = &ConsentVersion{}
		for _, v := range slices.Sorted(mapKeys(d.Versions)) {
			if !consentVersionPattern.MatchString(v) {
				errs = append(errs, fmt.Errorf("consents.%s.versions: %q is not a valid version (letters, digits, dots, dashes and underscores; at most 32 characters)", name, v))
				continue
			}
			cv := &ConsentVersion{}
			if vd := d.Versions[v]; vd != nil {
				cv.Reaccept = vd.Reaccept
				if vd.ReacceptBy != "" {
					t, err := time.Parse("2006-01-02", vd.ReacceptBy)
					if err != nil {
						errs = append(errs, fmt.Errorf("consents.%s.versions.%s.reaccept_by: %q must be a date such as 2026-11-15", name, v, vd.ReacceptBy))
						continue
					}
					cv.ReacceptBy = t.UTC()
					if !cv.Reaccept {
						errs = append(errs, fmt.Errorf("consents.%s.versions.%s.reaccept_by: has no effect without `reaccept: true`", name, v))
					}
				}
			}
			c.Versions[v] = cv
		}
		out[name] = c
	}
	return out, errs
}

// parseCompliance reads the compliance section of realm.yaml.
func parseCompliance(n yaml.Node) (ComplianceSettings, []error) {
	var out ComplianceSettings
	if n.Kind == 0 {
		return out, nil
	}
	var doc complianceDoc
	if err := n.Decode(&doc); err != nil {
		return out, []error{fmt.Errorf("compliance: %w", err)}
	}
	var errs []error
	if doc.ConsentProof != nil {
		out.ConsentProof = []string{}
		for _, p := range doc.ConsentProof {
			switch p {
			case ProofIP, ProofUserAgent:
				if !slices.Contains(out.ConsentProof, p) {
					out.ConsentProof = append(out.ConsentProof, p)
				}
			default:
				errs = append(errs, fmt.Errorf("compliance.consent_proof: %q must be ip or user_agent", p))
			}
		}
	}
	if doc.ConsentRetention != nil {
		v := *doc.ConsentRetention
		if v == "0" {
			out.ConsentRetentionSet = true
		} else if d, err := parseRetention(v); err != nil {
			errs = append(errs, fmt.Errorf("compliance.consent_retention: %w", err))
		} else {
			out.ConsentRetention, out.ConsentRetentionSet = d, true
		}
	}
	return out, errs
}

// parseRetention reads a retention: Go durations, whole days ("30d") and whole years ("3y").
func parseRetention(v string) (time.Duration, error) {
	if y, ok := strings.CutSuffix(v, "y"); ok {
		var n int
		if _, err := fmt.Sscanf(y, "%d", &n); err != nil || n < 0 || fmt.Sprint(n) != y {
			return 0, fmt.Errorf("invalid duration %q: use whole years (\"3y\"), whole days (\"90d\") or 0", v)
		}
		return time.Duration(n) * 365 * 24 * time.Hour, nil
	}
	return ParseDuration(v)
}

// loadConsents reads the texts of a realm's consents and checks them: the current
// version needs a text in every language of the realm, and every text found is hashed.
func loadConsents(rl *Realm, realmPath string) []error {
	if len(rl.Settings.Consents) == 0 {
		return nil
	}
	file := filepath.Join(realmPath, RealmFile)
	_, locales := rl.Settings.Languages()
	var errs []error
	for _, name := range slices.Sorted(mapKeys(rl.Settings.Consents)) {
		c := rl.Settings.Consents[name]
		dir := filepath.Join(realmPath, ConsentsDir, name)
		versions, _ := os.ReadDir(dir)
		for _, v := range versions {
			if !v.IsDir() || !consentVersionPattern.MatchString(v.Name()) {
				continue
			}
			for _, loc := range locales {
				data, err := os.ReadFile(filepath.Join(dir, v.Name(), loc+".md"))
				if err != nil {
					continue
				}
				sum := sha256.Sum256(data)
				if c.Texts[v.Name()] == nil {
					c.Texts[v.Name()] = map[string]ConsentText{}
				}
				c.Texts[v.Name()][loc] = ConsentText{Body: string(data), Hash: hex.EncodeToString(sum[:])}
			}
		}
		for _, loc := range locales {
			if _, ok := c.Texts[c.Current][loc]; !ok {
				errs = append(errs, fmt.Errorf("%s: consents.%s: the current version %q has no text for the language %s (%s)", file, name, c.Current, loc, filepath.Join(ConsentsDir, name, c.Current, loc+".md")))
			}
		}
		for v := range c.Versions {
			if v != c.Current && len(c.Texts[v]) == 0 {
				errs = append(errs, fmt.Errorf("%s: consents.%s.versions: %q has no text (%s): add its texts, or remove it from the list", file, name, v, filepath.Join(ConsentsDir, name, v, "<locale>.md")))
			}
		}
	}
	return errs
}

// ConsentNames returns the declared consents' names, sorted.
func (s RealmSettings) ConsentNames() []string {
	return slices.Sorted(mapKeys(s.Consents))
}

// ConsentRef names a consent and a version of it.
type ConsentRef struct{ Name, Version string }

// ConsentsToAccept lists the required consents a user must accept (again) to be
// let in: the ones they hold no version of, and the ones they hold an older
// version of when the current version asks for re-acceptance. held maps a consent
// to the version the user accepted; the list is sorted by name.
func (s RealmSettings) ConsentsToAccept(held map[string]string) []ConsentRef {
	var out []ConsentRef
	for _, name := range s.ConsentNames() {
		c := s.Consents[name]
		if !c.Required {
			continue
		}
		have := held[name]
		switch {
		case have == c.Current:
		case have == "" || c.Versions[c.Current].Reaccept:
			out = append(out, ConsentRef{Name: name, Version: c.Current})
		}
	}
	return out
}

// ConsentsPastDeadline is ConsentsToAccept restricted to the consents whose
// current version has a reaccept_by date that has passed: from then on a user
// who hasn't accepted them is refused until they do.
func (s RealmSettings) ConsentsPastDeadline(held map[string]string, now time.Time) []ConsentRef {
	var out []ConsentRef
	for _, ref := range s.ConsentsToAccept(held) {
		if by := s.Consents[ref.Name].Versions[ref.Version].ReacceptBy; !by.IsZero() && !now.Before(by.Add(24*time.Hour)) {
			out = append(out, ref)
		}
	}
	return out
}
