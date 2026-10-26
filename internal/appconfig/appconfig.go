// Package appconfig keeps a realm's app configuration: the values admins and
// functions set over the declared defaults, with optimistic concurrency, an
// audited history and rollback. It knows the declaration (registry.AppConfig)
// and a Store, and nothing about HTTP.
package appconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/docutil"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/fernandezvara/backd/internal/registry"
)

// History is kept for the last HistoryVersions versions or HistoryAge,
// whichever is longer.
const (
	HistoryVersions = 100
	HistoryAge      = 90 * 24 * time.Hour
)

// Kinds of a history entry.
const (
	KindSet      = "set"
	KindRollback = "rollback"
)

var (
	// ErrConflict: the version the change was based on isn't the current one.
	ErrConflict = errors.New("the configuration changed since it was read")
	// ErrNoVersion: the history has no such version (it may have been pruned).
	ErrNoVersion = errors.New("no such configuration version")
)

// InvalidError is a change that doesn't satisfy the declaration; it applied nothing.
type InvalidError struct{ Problems []Problem }

func (e *InvalidError) Error() string {
	parts := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		parts[i] = p.Path + " " + p.Reason
	}
	return "invalid configuration: " + strings.Join(parts, "; ")
}

// Problem is one path that can't take a value, and why.
type Problem struct{ Path, Reason string }

// State is what is stored: the values that differ from the defaults, nested like
// the configuration, with the version they have.
type State struct {
	Version   int64
	Values    map[string]any
	UpdatedAt time.Time
	UpdatedBy string
}

// Change is one path whose effective value changed; Old and New are effective
// values, so a path at its default shows the default.
type Change struct {
	Path string
	Old  any
	New  any
}

// Entry is one version of the history.
type Entry struct {
	Version int64
	At      time.Time
	Actor   string // "user:<id>", "key:<name>", "function:<db>/<name>", …
	Kind    string // KindSet or KindRollback
	// RolledBackTo is the version a rollback restored.
	RolledBackTo int64
	Changes      []Change
	// Values are the stored values after the change: what a rollback to this version restores.
	Values map[string]any
}

// Store is where the state and its history live (a realm's system database).
type Store interface {
	// AppConfigState returns the current state; version 0 with no values when none was saved.
	AppConfigState(ctx context.Context) (State, error)
	// SaveAppConfig replaces the state with next when the stored version is expected
	// (ErrConflict otherwise), records the entry, and removes entries of versions up to
	// pruneTo that are older than pruneBefore.
	SaveAppConfig(ctx context.Context, expected int64, next State, entry Entry, pruneTo int64, pruneBefore time.Time) error
	// AppConfigHistory returns entries, newest first.
	AppConfigHistory(ctx context.Context, limit int) ([]Entry, error)
	// AppConfigEntry returns one version's entry, or ErrNoVersion.
	AppConfigEntry(ctx context.Context, version int64) (Entry, error)
}

// Service reads and changes one realm's configuration.
type Service struct {
	Store  Store
	Config *registry.AppConfig
	Now    func() time.Time // defaults to time.Now
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// View is the configuration as it is now.
type View struct {
	Version int64
	// Values is every key with its effective value: the default, or what was set.
	Values map[string]any
	// Overrides are the values that differ from their defaults, nested.
	Overrides map[string]any
	// Invalid are stored values that no longer satisfy the declaration (after a
	// deploy): they are ignored, so their keys read as defaults, until fixed or reset.
	Invalid   []Problem
	UpdatedAt time.Time
	UpdatedBy string
}

// Current returns the configuration with the defaults filled in.
func (s *Service) Current(ctx context.Context) (View, error) {
	st, err := s.Store.AppConfigState(ctx)
	if err != nil {
		return View{}, err
	}
	return s.view(st), nil
}

func (s *Service) view(st State) View {
	valid, bad := s.usable(st.Values)
	return View{
		Version: st.Version, Values: s.merge(valid), Overrides: nest(valid), Invalid: bad,
		UpdatedAt: st.UpdatedAt, UpdatedBy: st.UpdatedBy,
	}
}

// usable keeps the stored leaves that still satisfy the declaration, and reports the rest.
func (s *Service) usable(stored map[string]any) (map[string]any, []Problem) {
	flat := flatten(normalize(stored).(map[string]any), "")
	good := map[string]any{}
	var bad []Problem
	for _, p := range slices.Sorted(maps.Keys(flat)) {
		switch cp, ok := s.Config.Paths[p]; {
		case !ok:
			bad = append(bad, Problem{p, "is not declared in the schema any more"})
		case !cp.Leaf:
			bad = append(bad, Problem{p, "is a branch now"})
		default:
			one := map[string]any{p: flat[p]}
			if err := s.Config.Validate(s.merge(one)); err != nil {
				bad = append(bad, Problem{p, firstReason(err)})
				continue
			}
			good[p] = flat[p]
		}
	}
	if err := s.Config.Validate(s.merge(good)); err != nil && len(good) > 0 {
		// Valid one by one, invalid together (a rule across keys): fall back to the defaults.
		for _, p := range slices.Sorted(maps.Keys(good)) {
			bad = append(bad, Problem{p, firstReason(err)})
		}
		good = map[string]any{}
	}
	return good, bad
}

// merge lays flat overrides over the defaults.
func (s *Service) merge(flat map[string]any) map[string]any {
	out := normalize(s.Config.Defaults).(map[string]any)
	for _, p := range slices.Sorted(maps.Keys(flat)) {
		docutil.SetPath(out, p, flat[p])
	}
	return out
}

// Input is a change: values to set and paths to return to their defaults.
type Input struct {
	Set   map[string]any
	Reset []string
}

// Result is what a change did.
type Result struct {
	Version int64
	// Changed is false when the change left every value as it was; no version is made then.
	Changed bool
	Changes []Change
}

// Apply validates the change as a whole against the declaration and, when it is
// valid and based on the current version, stores it as a new version.
func (s *Service) Apply(ctx context.Context, expected int64, in Input, actor string) (Result, error) {
	st, err := s.Store.AppConfigState(ctx)
	if err != nil {
		return Result{}, err
	}
	if st.Version != expected {
		return Result{}, ErrConflict
	}
	current, _ := s.usable(st.Values)
	next := maps.Clone(current)
	var problems []Problem
	for _, p := range in.Reset {
		if !s.Config.HasPath(p) {
			problems = append(problems, Problem{p, "is not a configuration key"})
			continue
		}
		for k := range next {
			if k == p || strings.HasPrefix(k, p+".") {
				delete(next, k)
			}
		}
	}
	for _, p := range slices.Sorted(maps.Keys(in.Set)) {
		cp, ok := s.Config.Paths[p]
		switch {
		case !ok:
			problems = append(problems, Problem{p, "is not a configuration key"})
		case !cp.Leaf:
			problems = append(problems, Problem{p, "is a group of keys: set each of its values"})
		default:
			next[p] = normalize(in.Set[p])
		}
	}
	if len(problems) > 0 {
		return Result{}, &InvalidError{problems}
	}
	return s.save(ctx, st, current, next, Entry{Kind: KindSet}, actor)
}

// Rollback restores the values a version had, as a new version. Version 0 is
// the defaults.
func (s *Service) Rollback(ctx context.Context, expected, to int64, actor string) (Result, error) {
	st, err := s.Store.AppConfigState(ctx)
	if err != nil {
		return Result{}, err
	}
	if st.Version != expected {
		return Result{}, ErrConflict
	}
	var target map[string]any
	if to != 0 {
		e, err := s.Store.AppConfigEntry(ctx, to)
		if err != nil {
			return Result{}, err
		}
		target = flatten(normalize(e.Values).(map[string]any), "")
	}
	if target == nil {
		target = map[string]any{}
	}
	current, _ := s.usable(st.Values)
	return s.save(ctx, st, current, target, Entry{Kind: KindRollback, RolledBackTo: to}, actor)
}

func (s *Service) save(ctx context.Context, st State, current, next map[string]any, e Entry, actor string) (Result, error) {
	// A value equal to its default isn't stored.
	defaults := flatten(normalize(s.Config.Defaults).(map[string]any), "")
	for p, v := range next {
		if d, ok := defaults[p]; ok && equal(d, v) {
			delete(next, p)
		}
	}
	merged := s.merge(next)
	if err := s.Config.Validate(merged); err != nil {
		return Result{}, &InvalidError{problems(err)}
	}
	if b, err := json.Marshal(merged); err != nil || len(b) > registry.AppConfigMaxBytes {
		return Result{}, &InvalidError{[]Problem{{"", fmt.Sprintf("the configuration would take more than %d bytes", registry.AppConfigMaxBytes)}}}
	}
	before, after := flatten(s.merge(current), ""), flatten(merged, "")
	var changes []Change
	for _, p := range slices.Sorted(maps.Keys(after)) {
		if !equal(before[p], after[p]) {
			changes = append(changes, Change{Path: p, Old: before[p], New: after[p]})
		}
	}
	if len(changes) == 0 {
		return Result{Version: st.Version}, nil
	}
	now := s.now()
	version := st.Version + 1
	e.Version, e.At, e.Actor, e.Changes, e.Values = version, now, actor, changes, nest(next)
	nextState := State{Version: version, Values: nest(next), UpdatedAt: now, UpdatedBy: actor}
	err := s.Store.SaveAppConfig(ctx, st.Version, nextState, e, version-HistoryVersions, now.Add(-HistoryAge))
	if err != nil {
		return Result{}, err
	}
	return Result{Version: version, Changed: true, Changes: changes}, nil
}

// History returns the last versions, newest first.
func (s *Service) History(ctx context.Context, limit int) ([]Entry, error) {
	if limit <= 0 || limit > HistoryVersions {
		limit = HistoryVersions
	}
	return s.Store.AppConfigHistory(ctx, limit)
}

// problems lists the failing instances of a schema validation error, by path.
func problems(err error) []Problem {
	var verr *jsonschema.ValidationError
	if !errors.As(err, &verr) {
		return []Problem{{"", err.Error()}}
	}
	var out []Problem
	var walk func(u *jsonschema.OutputUnit)
	walk = func(u *jsonschema.OutputUnit) {
		if u.Error != nil {
			path := strings.ReplaceAll(strings.Trim(u.InstanceLocation, "/"), "/", ".")
			out = append(out, Problem{path, u.Error.String()})
		}
		for i := range u.Errors {
			walk(&u.Errors[i])
		}
	}
	walk(verr.BasicOutput())
	if len(out) == 0 {
		out = []Problem{{"", verr.Error()}}
	}
	return out
}

// firstReason is the first problem of a schema validation error, as a sentence.
func firstReason(err error) string { return problems(err)[0].Reason }

// normalize turns a value into plain JSON types (numbers as float64), copying it.
func normalize(v any) any {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	_ = json.Unmarshal(b, &out)
	if out == nil {
		if _, isMap := v.(map[string]any); isMap {
			return map[string]any{}
		}
	}
	return out
}

// flatten lists the leaves of a nested document by dot path. A map that is
// empty is skipped, and arrays are leaves.
func flatten(m map[string]any, prefix string) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		if sub, ok := v.(map[string]any); ok {
			maps.Copy(out, flatten(sub, p))
			continue
		}
		out[p] = v
	}
	return out
}

// nest is the inverse of flatten.
func nest(flat map[string]any) map[string]any {
	out := map[string]any{}
	for p, v := range flat {
		docutil.SetPath(out, p, v)
	}
	return out
}

// equal compares two plain JSON values.
func equal(a, b any) bool {
	switch a := a.(type) {
	case map[string]any:
		bm, ok := b.(map[string]any)
		if !ok || len(a) != len(bm) {
			return false
		}
		for k, v := range a {
			if !equal(v, bm[k]) {
				return false
			}
		}
		return true
	case []any:
		bs, ok := b.([]any)
		return ok && slices.EqualFunc(a, bs, equal)
	case float64:
		bf, ok := b.(float64)
		return ok && a == bf
	}
	return a == b
}

// ParseVersion reads a version in the form the API uses, "c41" or "41".
func ParseVersion(s string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "c"), 10, 64)
	return n, err == nil && n >= 0
}

// FormatVersion writes a version in the form the API uses.
func FormatVersion(v int64) string { return "c" + strconv.FormatInt(v, 10) }

// Flat lists the values of a nested configuration by dot path.
func Flat(values map[string]any) map[string]any { return flatten(values, "") }
