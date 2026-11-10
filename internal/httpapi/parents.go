package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/fernandezvara/backd/internal/docutil"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/rules"
	"github.com/fernandezvara/backd/internal/storage"
)

// maxParentKeys bounds the parents a rule's filter may be resolved to (see design/parent-rules.md):
// a caller who qualifies through more parents gets an error, never a shorter list.
const maxParentKeys = 2000

// parentResolver lets the rules of collection c follow its references: it reads the parent
// documents, which must be live, and nothing else of them is exposed. Nothing is cached: a
// batch may change a parent between two operations.
type parentResolver struct {
	ctx   context.Context
	d     *documents
	child *registry.Collection
}

var _ rules.Parents = (*parentResolver)(nil)

func (d *documents) parentsOf(r *http.Request, c *registry.Collection) rules.Parents {
	return &parentResolver{ctx: r.Context(), d: d, child: c}
}

func (p *parentResolver) target(ref string) (*registry.Reference, *registry.Collection, error) {
	rf := p.child.References[ref]
	if rf == nil {
		return nil, nil, fmt.Errorf("parent(): %s has no reference %q", p.child.Name, ref)
	}
	t, ok := p.d.reg.Collection(p.child.Realm, rf.Database, rf.Collection)
	if !ok {
		return nil, nil, fmt.Errorf("parent(): reference %q points to a collection that doesn't exist", ref)
	}
	return rf, t, nil
}

func (p *parentResolver) Get(ref string, key any) (map[string]any, error) {
	rf, t, err := p.target(ref)
	if err != nil {
		return nil, err
	}
	page, err := p.d.store.Repository(t).List(p.ctx, storage.Query{
		Filter: hideDeleted(t, storage.Condition{Field: rf.Key, Op: storage.OpEq, Value: key}),
		Limit:  1,
	})
	if err != nil || len(page.Items) == 0 {
		return nil, err
	}
	return page.Items[0], nil
}

func (p *parentResolver) Keys(ref string, f storage.Filter) ([]any, error) {
	rf, t, err := p.target(ref)
	if err != nil {
		return nil, err
	}
	page, err := p.d.store.Repository(t).List(p.ctx, storage.Query{Filter: hideDeleted(t, f), Limit: maxParentKeys})
	if err != nil {
		return nil, err
	}
	if page.HasMore {
		return nil, fmt.Errorf("parent(): more than %d %s match the rule for this caller", maxParentKeys, t.Name)
	}
	keys := make([]any, 0, len(page.Items))
	for _, doc := range page.Items {
		if v, ok := docutil.GetPath(doc, rf.Key); ok && v != nil {
			keys = append(keys, v)
		}
	}
	// The same set is the same filter: a stream compares filters to know whether its access changed.
	slices.SortFunc(keys, func(a, b any) int { return strings.Compare(fmt.Sprint(a), fmt.Sprint(b)) })
	return keys, nil
}
