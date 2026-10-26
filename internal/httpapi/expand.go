package httpapi

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/docutil"
	"github.com/fernandezvara/backd/internal/query"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

const (
	codeInvalidExpand  = "invalid_expand"
	codeExpandTooLarge = "expand_too_large"

	expandQueriesHeader = "X-Backd-Expand-Queries"
	expandDocsHeader    = "X-Backd-Expand-Docs"
)

// expandNode is one reference to follow: the target collection, the fields to return of its
// documents, and the references followed from them.
type expandNode struct {
	ref      *registry.Reference
	target   *registry.Collection
	fields   []string // nil: the whole document
	explicit bool     // named in the request, not only a way to a second level
	children []*expandNode
}

// expandPlan is a request's expansion, checked against the collection's references.
type expandPlan struct {
	nodes []*expandNode
	paths []string // as requested, for the query log
}

// planExpand reads the `expand` parameter of a request to collection c and checks it against
// c's references and the instance's limits. A nil plan with no details means no expansion.
func (d *documents) planExpand(c *registry.Collection, raw string) (*expandPlan, []Detail) {
	entries, err := query.ParseExpand(raw)
	if err != nil {
		var details []Detail
		if qe, ok := errors.AsType[query.Errors](err); ok {
			for _, e := range qe {
				details = append(details, Detail{Path: e.Path, Reason: e.Reason})
			}
		}
		return nil, details
	}
	if len(entries) == 0 {
		return nil, nil
	}
	lim := d.expand.OrDefault()
	bad := func(e query.ExpandPath, format string, args ...any) []Detail {
		return []Detail{{Path: "expand", Reason: e.Text + ": " + fmt.Sprintf(format, args...)}}
	}
	if len(entries) > lim.MaxPaths {
		return nil, []Detail{{Path: "expand", Reason: fmt.Sprintf("%d entries, at most %d per request", len(entries), lim.MaxPaths)}}
	}
	plan := &expandPlan{}
	var roots []*expandNode
	for _, e := range entries {
		if len(e.Names) > lim.MaxDepth {
			return nil, bad(e, "goes %d levels deep, at most %d are followed", len(e.Names), lim.MaxDepth)
		}
		plan.paths = append(plan.paths, e.Text)
		level, from := &roots, c
		var node *expandNode
		for i, name := range e.Names {
			ref := from.References[name]
			if ref == nil {
				return nil, bad(e, "%q is not a reference of %s; %s", name, from.Name, declaredReferences(from))
			}
			if node = findNode(*level, name); node == nil {
				target, ok := d.reg.Collection(from.Realm, ref.Database, ref.Collection)
				if !ok {
					return nil, bad(e, "the target of %q is not configured", name)
				}
				node = &expandNode{ref: ref, target: target}
				*level = append(*level, node)
			}
			if i == len(e.Names)-1 {
				if node.explicit {
					return nil, bad(e, "%q is listed twice", name)
				}
				node.explicit = true
				for _, f := range e.Fields {
					if !node.target.IsKnownField(f) && f != "_meta" {
						return nil, bad(e, "%q is not a field of %s", f, node.target.Name)
					}
				}
				node.fields = e.Fields
			}
			level, from = &node.children, node.target
		}
	}
	plan.nodes = roots
	return plan, nil
}

func findNode(nodes []*expandNode, name string) *expandNode {
	for _, n := range nodes {
		if n.ref.Name == name {
			return n
		}
	}
	return nil
}

func declaredReferences(c *registry.Collection) string {
	if len(c.References) == 0 {
		return c.Name + " declares no references"
	}
	return "declared: " + strings.Join(slices.Sorted(maps.Keys(c.References)), ", ")
}

// expandTooLarge is an expansion that would return more documents than the instance allows.
type expandTooLarge struct{ would, max int }

func (e *expandTooLarge) Error() string {
	return fmt.Sprintf("this expansion would return %d documents, at most %d per response", e.would, e.max)
}

// expandRun executes a plan for one request.
type expandRun struct {
	d       *documents
	r       *http.Request
	limits  registry.ExpandLimits
	queries int
	docs    int
	targets map[*registry.Collection]targetAccess
	reveals map[*registry.Collection]*revealer
}

// targetAccess is what the caller may read of a target collection.
type targetAccess struct {
	filter storage.Filter // nil: everything
	ok     bool           // false: nothing
}

// expandDocuments adds `_expanded` to out, the rendered form of docs (index for index),
// following the plan. It returns what the expansion cost.
func (d *documents) expandDocuments(ctx context.Context, r *http.Request, plan *expandPlan, docs []storage.Document, out []map[string]any) (queries, count int, err error) {
	run := &expandRun{d: d, r: r, limits: d.expand.OrDefault(), targets: map[*registry.Collection]targetAccess{}}
	err = run.expand(ctx, docs, out, plan.nodes)
	return run.queries, run.docs, err
}

func (run *expandRun) expand(ctx context.Context, parents []storage.Document, outs []map[string]any, nodes []*expandNode) error {
	for _, n := range nodes {
		if err := run.follow(ctx, parents, outs, n); err != nil {
			return err
		}
	}
	return nil
}

// follow resolves one reference for every parent with one query.
func (run *expandRun) follow(ctx context.Context, parents []storage.Document, outs []map[string]any, n *expandNode) error {
	perParent := make([][]string, len(parents))
	truncated := make([]bool, len(parents))
	maxArray := run.limits.MaxArray
	if n.ref.MaxExpand > 0 {
		maxArray = min(maxArray, n.ref.MaxExpand)
	}
	total := 0
	var unique []any
	seen := map[string]bool{}
	for i, p := range parents {
		vals := sourceValues(p, n.ref.Field, n.ref.Many)
		if len(vals) > maxArray {
			vals, truncated[i] = vals[:maxArray], true
		}
		perParent[i] = vals
		total += len(vals)
		for _, v := range vals {
			if !seen[v] {
				seen[v] = true
				unique = append(unique, v)
			}
		}
	}
	// Before any query: what could be placed depends only on the request's own documents.
	if run.docs+total > run.limits.MaxDocs {
		return &expandTooLarge{would: run.docs + total, max: run.limits.MaxDocs}
	}
	found := map[string]storage.Document{}
	if len(unique) > 0 {
		run.queries++
		acc := run.access(n.target)
		if acc.ok {
			page, err := run.d.store.Repository(n.target).List(ctx, storage.Query{
				Filter: storage.Condition{Field: n.ref.Key, Op: storage.OpIn, Value: unique},
				Access: acc.filter,
				Limit:  len(unique),
			})
			if err != nil {
				return err
			}
			for _, doc := range page.Items {
				if k, ok := fieldString(doc, n.ref.Key); ok {
					if _, dup := found[k]; !dup {
						found[k] = doc
					}
				}
			}
		}
	}
	// One shaped document per target, shared by every parent that points at it.
	shaped := map[string]map[string]any{}
	var tDocs []storage.Document
	var tOuts []map[string]any
	for _, k := range slices.Sorted(maps.Keys(found)) {
		// The target's own reveal rules decide what of it the caller sees.
		shown, redacted := run.reveal(n.target).view(found[k])
		o := selectFields(shown, n.fields)
		markRedacted(o, n.fields, redacted)
		shaped[k] = o
		tDocs = append(tDocs, found[k])
		tOuts = append(tOuts, o)
	}
	for i, vals := range perParent {
		var v any
		switch {
		case n.ref.Many:
			items := make([]any, 0, len(vals))
			for _, key := range vals {
				if o, ok := shaped[key]; ok {
					items = append(items, o)
					run.docs++
				} else {
					items = append(items, nil)
				}
			}
			v = items
			if truncated[i] {
				v = map[string]any{"items": items, "truncated": true}
			}
		case len(vals) == 0:
			v = nil
		default:
			if o, ok := shaped[vals[0]]; ok {
				v, run.docs = o, run.docs+1
			}
		}
		exp, _ := outs[i]["_expanded"].(map[string]any)
		if exp == nil {
			exp = map[string]any{}
			outs[i]["_expanded"] = exp
		}
		exp[n.ref.Name] = v
	}
	if len(n.children) > 0 && len(tDocs) > 0 {
		return run.expand(ctx, tDocs, tOuts, n.children)
	}
	return nil
}

// access is what the caller may read of target: the same identity as the request, the
// target's own read rule, and an API key's scopes.
// reveal is the request's view of a target collection's encrypted fields, made once per target.
func (run *expandRun) reveal(target *registry.Collection) *revealer {
	if rv, ok := run.reveals[target]; ok {
		return rv
	}
	if run.reveals == nil {
		run.reveals = map[*registry.Collection]*revealer{}
	}
	rv := run.d.revealerFor(run.r, target)
	run.reveals[target] = rv
	return rv
}

func (run *expandRun) access(target *registry.Collection) targetAccess {
	if a, ok := run.targets[target]; ok {
		return a
	}
	a := targetAccess{}
	caller, hasCaller := callerOf(run.r)
	switch {
	case hasCaller && caller.Key != nil && !caller.Key.Scopes.Allows(auth.ScopeRead, target.Database, target.Name):
	default:
		f, why := readRuleFilter(run.d.accessIn(run.r, target), target)
		if why == "" {
			a = targetAccess{filter: hideDeleted(target, f), ok: true}
		}
	}
	run.targets[target] = a
	return a
}

// sourceValues are the reference values a document holds in path, in order and without
// duplicates; anything that isn't a non-empty string is ignored.
func sourceValues(doc storage.Document, path string, many bool) []string {
	v, ok := docutil.GetPath(doc, path)
	if !ok {
		return nil
	}
	var out []string
	add := func(x any) {
		if s, ok := x.(string); ok && s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	if many {
		switch list := v.(type) {
		case []any:
			for _, x := range list {
				add(x)
			}
		case []string:
			for _, x := range list {
				add(x)
			}
		}
		return out
	}
	add(v)
	return out
}

func fieldString(doc storage.Document, path string) (string, bool) {
	v, ok := docutil.GetPath(doc, path)
	s, isString := v.(string)
	return s, ok && isString && s != ""
}

// selectFields renders an expanded document: the whole of it, or its id and the selected
// fields. The selection is applied to the stored document after the rules have decided that
// the caller may read it, so it can't reveal anything a direct read wouldn't.
func selectFields(doc storage.Document, fields []string) map[string]any {
	if fields == nil {
		return render(doc)
	}
	out := map[string]any{"id": doc["id"]}
	for _, f := range fields {
		v, ok := docutil.GetPath(doc, f)
		if !ok {
			continue
		}
		docutil.SetPath(out, f, v)
	}
	return render(out)
}

// markRedacted lists, in an expanded document that was cut to some fields, which of those the
// caller may not see, as a full document does in _meta.redacted.
func markRedacted(out map[string]any, fields, redacted []string) {
	if fields == nil || len(redacted) == 0 {
		return
	}
	var hidden []any
	for _, p := range redacted {
		if slices.Contains(fields, p) {
			hidden = append(hidden, p)
		}
	}
	if len(hidden) > 0 {
		out["_meta"] = map[string]any{"redacted": hidden}
	}
}

// expandError answers a failed expansion: too large a response is the caller's to fix, the
// rest is storage's.
func expandError(w http.ResponseWriter, r *http.Request, err error) {
	if tooLarge, ok := errors.AsType[*expandTooLarge](err); ok {
		writeError(w, r, http.StatusBadRequest, codeExpandTooLarge, tooLarge.Error()+": ask for fewer entries in expand or a smaller page (limit)", Detail{Path: "expand", Reason: "too many documents"})
		return
	}
	storageError(w, r, err)
}

// setExpandHeaders reports the cost of an expansion and records it for the access log.
func setExpandHeaders(w http.ResponseWriter, r *http.Request, plan *expandPlan, queries, docs int, took time.Duration) {
	w.Header().Set(expandQueriesHeader, strconv.Itoa(queries))
	w.Header().Set(expandDocsHeader, strconv.Itoa(docs))
	if s, ok := r.Context().Value(expandLogKey).(*expandLog); ok {
		*s = expandLog{Paths: plan.paths, Queries: queries, Docs: docs, Took: took}
	}
}

// expandLog is what the access log says of an expansion.
type expandLog struct {
	Paths   []string
	Queries int
	Docs    int
	Took    time.Duration
}
