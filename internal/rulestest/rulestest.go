// Package rulestest runs the fixtures of `backd rules test`: it decides who
// may read, create, update and delete which documents of a collection, with
// the collection's real rules (the `rules:` of its collection.yaml) and schema.json and no database, and
// compares the decisions with what the fixture expects.
//
// A fixture (rules.test.yaml, next to collection.yaml) names callers and stored
// documents and asserts allow or deny per operation:
//
//	now: 2026-10-04T12:00:00Z       # optional: what `now` is in the rules
//	users:                          # "anonymous" always exists
//	  ada: { id: u1, email: ada@example.com, roles: [] }
//	documents:                      # stored documents, by name
//	  draft: { title: Draft, published: false, _meta: { owner: u1 } }
//	tests:
//	  - as: ada
//	    read:   { allow: [draft], deny: [] }
//	    create: [ { data: { title: New }, expect: allow } ]
//	    update: [ { document: draft, patch: { published: true }, expect: allow } ]
//	    delete: { allow: [], deny: [draft] }
//
// The decisions follow the server's: read rules become a filter evaluated the
// way MongoDB would (storage.Match), an update or delete first needs the
// document to be readable, and a rule that fails while evaluating denies.
package rulestest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/fernandezvara/backd/internal/jsonnum"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/rules"
	"github.com/fernandezvara/backd/internal/storage"
)

// FileName is the fixture file, next to collection.yaml.
const FileName = "rules.test.yaml"

var printer = message.NewPrinter(language.English)

// defaultNow is `now` when the fixture doesn't set it, so results don't depend
// on the day the tests run.
var defaultNow = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// File is a fixture.
type File struct {
	Now       string                    `yaml:"now"`
	Users     map[string]UserDef        `yaml:"users"`
	Documents map[string]map[string]any `yaml:"documents"`
	Tests     []Test                    `yaml:"tests"`
}

// UserDef is a caller.
type UserDef struct {
	ID            string   `yaml:"id"`
	Email         string   `yaml:"email"`
	EmailVerified *bool    `yaml:"email_verified"` // default true
	Roles         []string `yaml:"roles"`
}

// Test groups the assertions of one caller.
type Test struct {
	Name   string `yaml:"name"`
	As     string `yaml:"as"` // a user of the fixture; default anonymous
	Now    string `yaml:"now"`
	Read   *Docs  `yaml:"read"`
	Delete *Docs  `yaml:"delete"`
	// Restore and Purge are for collections that soft-delete: which deleted
	// (_meta.deleted_at) documents the caller may see in the trash and
	// restore, and which they may purge.
	Restore *Docs    `yaml:"restore"`
	Purge   *Docs    `yaml:"purge"`
	Create  []Create `yaml:"create"`
	Update  []Update `yaml:"update"`
}

// Docs names stored documents the caller may or may not act on.
type Docs struct {
	Allow []string `yaml:"allow"`
	Deny  []string `yaml:"deny"`
}

// Create asserts the decision on creating a document.
type Create struct {
	Data   map[string]any `yaml:"data"`
	Expect string         `yaml:"expect"`
}

// Update asserts the decision on updating a stored document: Data is the
// complete new document (as PUT sends it), Patch a JSON merge patch onto the
// stored one (as PATCH sends it).
type Update struct {
	Document string         `yaml:"document"`
	Data     map[string]any `yaml:"data"`
	Patch    map[string]any `yaml:"patch"`
	Expect   string         `yaml:"expect"`
}

// Check is the outcome of one assertion.
type Check struct {
	Test   string // the test's name, or its position
	What   string // for example "ada updates draft"
	Pass   bool
	Detail string // on failure: what was expected and what the rules decided
}

// Run parses the fixture and runs it against the collection's rules.
// problems are mistakes in the fixture itself (unknown names, documents that
// break the schema): the assertions are not run when there are any.
func Run(c *registry.Collection, declaredRoles []string, fixture []byte) (checks []Check, problems []error) {
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(fixture))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, []error{fmt.Errorf("invalid YAML: %w", err)}
	}
	r := &runner{c: c, roles: declaredRoles, now: defaultNow, users: map[string]*rules.User{"anonymous": nil}, docs: map[string]map[string]any{}}
	if f.Now != "" {
		t, err := time.Parse(time.RFC3339, f.Now)
		if err != nil {
			return nil, []error{fmt.Errorf("now: %q is not an RFC 3339 timestamp", f.Now)}
		}
		r.now = t.UTC()
	}
	r.load(f)
	if len(r.problems) > 0 {
		return nil, r.problems
	}
	for i, t := range f.Tests {
		r.run(i, t)
	}
	if len(r.problems) > 0 {
		return nil, r.problems
	}
	return r.checks, nil
}

type runner struct {
	c        *registry.Collection
	roles    []string
	now      time.Time
	users    map[string]*rules.User
	docs     map[string]map[string]any // stored documents, as the server reads them
	checks   []Check
	problems []error
}

func (r *runner) problem(format string, args ...any) {
	r.problems = append(r.problems, fmt.Errorf(format, args...))
}

func (r *runner) load(f File) {
	for _, name := range slices.Sorted(maps.Keys(f.Users)) {
		u := f.Users[name]
		if name == "anonymous" {
			r.problem("users.anonymous: the anonymous caller always exists; give a user another name")
			continue
		}
		if u.ID == "" {
			r.problem("users.%s: id is required", name)
		}
		for _, role := range u.Roles {
			if !slices.Contains(r.roles, role) {
				r.problem("users.%s: role %q is not declared in realm.yaml (declared: %s)", name, role, strings.Join(r.roles, ", "))
			}
		}
		verified := u.EmailVerified == nil || *u.EmailVerified
		r.users[name] = &rules.User{ID: u.ID, Email: u.Email, EmailVerified: verified, Roles: append([]string{}, u.Roles...)}
	}
	for _, name := range slices.Sorted(maps.Keys(f.Documents)) {
		if doc, err := r.stored(name, f.Documents[name]); err != nil {
			r.problem("documents.%s: %v", name, err)
		} else {
			r.docs[name] = doc
		}
	}
	if len(f.Tests) == 0 {
		r.problem("tests: none; add at least one")
	}
}

var metaKeys = []string{"owner", "created_by", "updated_by", "version", "created_at", "updated_at", "deleted_at", "deleted_by", "purge_at"}

// stored builds a stored document: the user fields (checked against the
// schema, as the server does when it writes them) and the system fields.
func (r *runner) stored(name string, def map[string]any) (map[string]any, error) {
	fields := maps.Clone(def)
	id := name
	if v, ok := fields["id"]; ok {
		s, isString := v.(string)
		if !isString || s == "" {
			return nil, errors.New("id must be a non-empty string")
		}
		id = s
	}
	delete(fields, "id")
	metaIn, _ := fields["_meta"].(map[string]any)
	if _, has := fields["_meta"]; has && metaIn == nil {
		return nil, errors.New("_meta must be a map")
	}
	delete(fields, "_meta")
	body, err := r.userFields(fields)
	if err != nil {
		return nil, err
	}
	meta := map[string]any{"created_at": r.now, "updated_at": r.now, "version": int64(1), "owner": nil, "created_by": "key:fixture", "updated_by": "key:fixture"}
	for k, v := range metaIn {
		if !slices.Contains(metaKeys, k) {
			return nil, fmt.Errorf("_meta.%s is not a field (use %s)", k, strings.Join(metaKeys, ", "))
		}
		switch k {
		case "created_at", "updated_at", "deleted_at", "purge_at":
			s, _ := v.(string)
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				v = t.UTC()
			} else if t, ok := v.(time.Time); ok {
				v = t.UTC()
			} else {
				return nil, fmt.Errorf("_meta.%s must be an RFC 3339 timestamp", k)
			}
		case "version":
			n, ok := v.(int)
			if !ok {
				return nil, errors.New("_meta.version must be an integer")
			}
			v = int64(n)
		case "owner":
			if s, isString := v.(string); v != nil && !isString || s == "" && v != nil {
				return nil, errors.New("_meta.owner must be a user id or null")
			}
		}
		meta[k] = v
	}
	doc := body
	doc["id"] = id
	doc["_meta"] = meta
	return doc, nil
}

// userFields checks fields against schema.json and normalizes them the way the
// server does (json.Number into int64 and float64).
func (r *runner) userFields(fields map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("not JSON-compatible: %w", err)
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if err := r.c.Schema.Validate(v); err != nil {
		return nil, fmt.Errorf("breaks schema.json: %s", firstLine(err))
	}
	out, _ := jsonnum.Normalize(v).(map[string]any)
	if out == nil {
		out = map[string]any{}
	}
	return r.c.DatesToStorage(out), nil // fields stored as dates are times, as the rules see them
}

func firstLine(err error) string {
	var ve *jsonschema.ValidationError
	if errors.As(err, &ve) {
		var msgs []string
		var walk func(e *jsonschema.ValidationError)
		walk = func(e *jsonschema.ValidationError) {
			if len(e.Causes) == 0 {
				msgs = append(msgs, "/"+strings.Join(e.InstanceLocation, "/")+": "+e.ErrorKind.LocalizedString(printer))
			}
			for _, c := range e.Causes {
				walk(c)
			}
		}
		walk(ve)
		if len(msgs) > 0 {
			return strings.Join(msgs, "; ")
		}
	}
	return strings.SplitN(err.Error(), "\n", 2)[0]
}

// run executes one test: the assertions of one caller.
func (r *runner) run(i int, t Test) {
	name := t.Name
	if name == "" {
		name = fmt.Sprintf("tests[%d]", i)
	}
	where := fmt.Sprintf("tests[%d]", i)
	as := t.As
	if as == "" {
		as = "anonymous"
	}
	user, ok := r.users[as]
	if !ok {
		r.problem("%s.as: %q is not one of the fixture's users (%s)", where, as, strings.Join(slices.Sorted(maps.Keys(r.users)), ", "))
		return
	}
	now := r.now
	if t.Now != "" {
		parsed, err := time.Parse(time.RFC3339, t.Now)
		if err != nil {
			r.problem("%s.now: %q is not an RFC 3339 timestamp", where, t.Now)
			return
		}
		now = parsed.UTC()
	}
	if t.Read == nil && t.Delete == nil && t.Restore == nil && t.Purge == nil && len(t.Create) == 0 && len(t.Update) == 0 {
		r.problem("%s: no assertions: add read, create, update, delete, restore or purge", where)
	}
	add := func(what string, want string, d decision) {
		c := Check{Test: name, What: what, Pass: (want == "allow") == d.allow}
		if !c.Pass {
			c.Detail = fmt.Sprintf("expected %s, the rules %s (%s)", want, verdict(d.allow), d.why)
		}
		r.checks = append(r.checks, c)
	}
	doc := func(field, docName string) (map[string]any, bool) {
		d, ok := r.docs[docName]
		if !ok {
			r.problem("%s.%s: %q is not one of the fixture's documents (%s)", where, field, docName, strings.Join(slices.Sorted(maps.Keys(r.docs)), ", "))
		}
		return d, ok
	}
	for _, g := range []struct {
		op   rules.Op
		docs *Docs
	}{{rules.Read, t.Read}, {rules.Delete, t.Delete}, {rules.Restore, t.Restore}, {rules.Purge, t.Purge}} {
		if g.docs == nil {
			continue
		}
		for _, side := range []struct {
			want  string
			names []string
		}{{"allow", g.docs.Allow}, {"deny", g.docs.Deny}} {
			for _, n := range side.names {
				d, ok := doc(string(g.op), n)
				if !ok {
					continue
				}
				verb := map[rules.Op]string{rules.Read: "reads", rules.Delete: "deletes", rules.Restore: "restores", rules.Purge: "purges"}[g.op]
				switch g.op {
				case rules.Read:
					add(fmt.Sprintf("%s %s %s", as, verb, n), side.want, r.read(user, now, d))
				case rules.Restore:
					add(fmt.Sprintf("%s %s %s", as, verb, n), side.want, r.restore(user, now, d))
				case rules.Purge:
					add(fmt.Sprintf("%s %s %s", as, verb, n), side.want, r.purge(user, now, d))
				default:
					add(fmt.Sprintf("%s %s %s", as, verb, n), side.want, r.write(user, now, rules.Delete, d, nil))
				}
			}
		}
	}
	for j, c := range t.Create {
		if !validExpect(c.Expect) {
			r.problem("%s.create[%d].expect: must be allow or deny, got %q", where, j, c.Expect)
			continue
		}
		data, valid := r.checked(fmt.Sprintf("%s.create[%d].data", where, j), c.Data)
		if !valid {
			continue
		}
		add(fmt.Sprintf("%s creates %s", as, summary(c.Data)), c.Expect, r.write(user, now, rules.Create, nil, data))
	}
	for j, u := range t.Update {
		field := fmt.Sprintf("update[%d]", j)
		if !validExpect(u.Expect) {
			r.problem("%s.%s.expect: must be allow or deny, got %q", where, field, u.Expect)
			continue
		}
		if (u.Data == nil) == (u.Patch == nil) {
			r.problem("%s.%s: give exactly one of data (the complete new document) and patch (a merge patch)", where, field)
			continue
		}
		d, ok := doc(field+".document", u.Document)
		if !ok {
			continue
		}
		next := u.Data
		if u.Patch != nil {
			next = mergePatch(userOnly(d), u.Patch)
		}
		data, valid := r.checked(fmt.Sprintf("%s.%s result", where, field), next)
		if !valid {
			continue
		}
		change := summary(u.Data)
		if u.Patch != nil {
			change = "patch " + summary(u.Patch)
		}
		add(fmt.Sprintf("%s updates %s with %s", as, u.Document, change), u.Expect, r.write(user, now, rules.Update, d, data))
	}
}

func validExpect(s string) bool { return s == "allow" || s == "deny" }

func verdict(allow bool) string {
	if allow {
		return "allow it"
	}
	return "deny it"
}

// checked validates a document the caller would send, as the server does
// before it looks at the rules.
func (r *runner) checked(where string, fields map[string]any) (map[string]any, bool) {
	out, err := r.userFields(fields)
	if err != nil {
		r.problem("%s: %v (the server would answer 400 before the rules are asked)", where, err)
		return nil, false
	}
	return out, true
}

// userOnly is a stored document without its system fields.
func userOnly(doc map[string]any) map[string]any {
	out := maps.Clone(doc)
	delete(out, "id")
	delete(out, "_meta")
	return deepCopy(out).(map[string]any)
}

type decision struct {
	allow bool
	why   string
}

// read decides whether the caller may read the stored document: the read rule
// becomes a filter, and the document must match it.
func (r *runner) read(user *rules.User, now time.Time, doc map[string]any) decision {
	rule := r.c.Rules.For(rules.Read)
	if rule == nil {
		return decision{false, "no read rule"}
	}
	f, err := rule.Filter(rules.Values{User: user, Now: now})
	switch {
	case err != nil:
		return decision{false, "read rule error: " + err.Error()}
	case f == storage.Const(false):
		return decision{false, "rule " + rule.Key + " is false for this caller"}
	case f == storage.Const(true):
		return decision{true, "rule " + rule.Key + " is true for this caller"}
	}
	ok, err := storage.Match(doc, f)
	switch {
	case err != nil:
		return decision{false, "read rule error: " + err.Error()}
	case ok:
		return decision{true, "rule " + rule.Key + " matches the document"}
	}
	return decision{false, "rule " + rule.Key + " does not match the document: the server answers 404"}
}

// restore decides who sees a deleted document in the trash and may restore it:
// the read rule and the restore rule must both match it, and the document has
// to be in the trash (a live document can't be restored).
func (r *runner) restore(user *rules.User, now time.Time, doc map[string]any) decision {
	if !r.deleted(doc) {
		return decision{false, "the document is not deleted (give it _meta.deleted_at in the fixture)"}
	}
	if d := r.read(user, now, doc); !d.allow {
		return decision{false, "the caller can't read the document: " + d.why}
	}
	return r.filterRule(user, now, rules.Restore, doc)
}

// purge decides who may remove a document for good: they must see it (a live
// one by the read rule, a deleted one by the read and restore rules too) and
// the purge rule must allow it.
func (r *runner) purge(user *rules.User, now time.Time, doc map[string]any) decision {
	seen := r.read(user, now, doc)
	if seen.allow && r.deleted(doc) {
		seen = r.filterRule(user, now, rules.Restore, doc)
	}
	if !seen.allow {
		return decision{false, "the caller can't see the document: " + seen.why}
	}
	rule := r.c.Rules.For(rules.Purge)
	if rule == nil {
		return decision{false, "no purge rule"}
	}
	ok, err := rule.Allow(rules.Values{User: user, Document: doc, Now: now})
	switch {
	case err != nil:
		return decision{false, "rule " + rule.Key + " failed: " + err.Error()}
	case ok:
		return decision{true, "rule " + rule.Key + " is true"}
	}
	return decision{false, "rule " + rule.Key + " is false"}
}

func (r *runner) deleted(doc map[string]any) bool {
	meta, _ := doc["_meta"].(map[string]any)
	_, ok := meta["deleted_at"]
	return ok
}

// filterRule evaluates a filter-style rule (read, restore) against a document.
func (r *runner) filterRule(user *rules.User, now time.Time, op rules.Op, doc map[string]any) decision {
	rule := r.c.Rules.For(op)
	if rule == nil {
		return decision{false, "no " + string(op) + " rule"}
	}
	f, err := rule.Filter(rules.Values{User: user, Now: now})
	switch {
	case err != nil:
		return decision{false, string(op) + " rule error: " + err.Error()}
	case f == storage.Const(false):
		return decision{false, "rule " + rule.Key + " is false for this caller"}
	case f == storage.Const(true):
		return decision{true, "rule " + rule.Key + " is true for this caller"}
	}
	ok, err := storage.Match(doc, f)
	switch {
	case err != nil:
		return decision{false, string(op) + " rule error: " + err.Error()}
	case ok:
		return decision{true, "rule " + rule.Key + " matches the document"}
	}
	return decision{false, "rule " + rule.Key + " does not match the document"}
}

// write decides create, update and delete. Update and delete need the stored
// document to be readable first, as on the server.
func (r *runner) write(user *rules.User, now time.Time, op rules.Op, doc, data map[string]any) decision {
	if doc != nil {
		if d := r.read(user, now, doc); !d.allow {
			return decision{false, "the caller can't read the document: " + d.why}
		}
	}
	rule := r.c.Rules.For(op)
	if rule == nil {
		return decision{false, "no " + string(op) + " rule"}
	}
	ok, err := rule.Allow(rules.Values{User: user, Document: doc, Data: data, Now: now})
	switch {
	case err != nil:
		return decision{false, "rule " + rule.Key + " failed: " + err.Error()}
	case ok:
		return decision{true, "rule " + rule.Key + " is true"}
	}
	return decision{false, "rule " + rule.Key + " is false"}
}

// summary shows a document briefly in a description.
func summary(m map[string]any) string {
	b, _ := json.Marshal(m)
	s := string(b)
	if len(s) > 70 {
		s = s[:67] + "..."
	}
	return s
}

// mergePatch applies an RFC 7396 merge patch to target, in place.
func mergePatch(target, patch map[string]any) map[string]any {
	if target == nil {
		target = map[string]any{}
	}
	for k, v := range patch {
		switch pv := v.(type) {
		case nil:
			delete(target, k)
		case map[string]any:
			tv, _ := target[k].(map[string]any)
			target[k] = mergePatch(tv, pv)
		default:
			target[k] = v
		}
	}
	return target
}

func deepCopy(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = deepCopy(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = deepCopy(e)
		}
		return out
	}
	return v
}
