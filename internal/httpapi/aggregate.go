package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/fernandezvara/backd/internal/query"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

const (
	codeAggregateNotEnabled = "aggregate_not_enabled"
	codeInvalidAggregate    = "invalid_aggregate"
	codeAggregateTooCostly  = "aggregate_too_expensive"

	// maxDistinct is the most distinct values count_distinct counts exactly.
	maxDistinct = 10000
)

var aggregateParams = map[string]bool{"language": true, "where": true, "group_by": true, "metrics": true, "order_by": true, "limit": true, "tz": true, "org": true, "team": true}

// aggregate handles GET …/{collection}/_aggregate: a summary of the documents the caller may
// read, grouped by declared fields, with a fixed pipeline the request only parameterizes.
func (d *documents) aggregate(w http.ResponseWriter, r *http.Request) {
	c, repo := d.collection(r)
	outcome := "error"
	var took time.Duration // how long the database took, when it was asked
	defer func() { d.metrics.AggregateRan(c.Realm, c.Database, c.Name, outcome, took) }()

	// The rules come first: someone who may read nothing learns nothing, not even whether the
	// collection can be aggregated.
	access, ok := readFilter(w, r, d.accessFor(r), c)
	if !ok {
		outcome = "denied"
		return
	}
	if c.Aggregate == nil {
		outcome = "invalid"
		writeError(w, r, http.StatusBadRequest, codeAggregateNotEnabled, "this collection doesn't declare `aggregate:` in its collection.yaml, so it can't be aggregated")
		return
	}
	v := r.URL.Query()
	var details []Detail
	for key, vals := range v {
		switch {
		case !aggregateParams[key]:
			details = append(details, Detail{Path: key, Reason: "unknown query parameter"})
		case len(vals) > 1:
			details = append(details, Detail{Path: key, Reason: "must be given at most once"})
		}
	}
	where, text, err := query.ParseWhereSearch(v.Get("where"), c.Fields)
	details = append(details, queryDetails(err)...)
	lim := d.aggregateLim
	maxGroups := c.Aggregate.EffectiveMaxGroups(lim)
	q, err := query.ParseAggregate(query.AggregateParams{
		GroupBy: v.Get("group_by"), Metrics: v.Get("metrics"), OrderBy: v.Get("order_by"), TZ: v.Get("tz"), Limit: v.Get("limit"),
	}, query.AggregateFields{
		GroupBy: c.Aggregate.GroupBy, Metrics: c.Aggregate.Metrics, MaxGroups: maxGroups,
		Date:    func(f string) bool { return aggregateDate(c, f) },
		Numeric: func(f string) bool { return aggregateNumeric(c, f) },
	})
	details = append(details, queryDetails(err)...)
	if len(details) > 0 {
		outcome = "invalid"
		writeError(w, r, http.StatusBadRequest, codeInvalidAggregate, "invalid aggregate parameters", details...)
		return
	}
	filter, ok := scopeFilter(w, r, c, where)
	if !ok {
		outcome = "invalid"
		return
	}
	agg, ok := repo.(storage.Aggregator)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, codeInvalidAggregate, "this storage can't aggregate")
		return
	}

	// One at a time per caller and collection: an aggregation is the most expensive read there
	// is, and a client that opens many at once is asking for its neighbours' time.
	key := "aggregate/" + c.Realm + "/" + c.Database + "/" + c.Name + "/" + aggregateCaller(r)
	limiter := limiterFor(d.reg)
	if !limiter.tryAcquire(key, 1) {
		outcome = "limited"
		retryAfter(w, time.Second)
		writeError(w, r, http.StatusTooManyRequests, codeTooManyRequests, "another aggregation of this collection is running for you: wait for it to finish")
		return
	}
	defer limiter.release(key, 1)

	if text != "" {
		q.Search = &storage.Search{Text: text}
		if !d.prepareSearch(w, r, c, q.Search) {
			outcome = "invalid"
			return
		}
	}
	q.Filter, q.Access = filter, access
	q.MaxTime, q.MaxDistinct = c.Aggregate.EffectiveTime(lim), maxDistinct
	began := time.Now()
	res, err := agg.Aggregate(r.Context(), q)
	took = time.Since(began)
	if errors.Is(err, storage.ErrAggregateTooExpensive) {
		outcome = "too_expensive"
		writeError(w, r, http.StatusUnprocessableEntity, codeAggregateTooCostly,
			"this aggregation needs more time, memory or distinct values than allowed: narrow `where`, group by fewer fields, or add an index that starts with a group_by field")
		return
	}
	if err != nil {
		storageError(w, r, err)
		return
	}
	outcome = "ok"

	groups := make([]map[string]any, 0, len(res.Groups))
	for _, g := range res.Groups {
		key := make(map[string]any, len(q.GroupBy))
		for i, f := range q.GroupBy {
			key[f.Name()] = timesToStrings(g.Key[i])
		}
		out := map[string]any{"key": key}
		for _, m := range q.Metrics {
			out[m.Name()] = timesToStrings(g.Values[m.Name()])
		}
		groups = append(groups, out)
	}
	body := map[string]any{"groups": groups, "total_groups": res.TotalGroups}
	raw, _ := json.Marshal(body)
	sum := sha256.Sum256(raw)
	tag := `W/"` + hex.EncodeToString(sum[:])[:32] + `"`
	readHeaders(w, tag)
	if notModified(w, r, tag) {
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// aggregateCaller identifies who an aggregation counts against.
func aggregateCaller(r *http.Request) string {
	if c, ok := callerOf(r); ok {
		return c.Subject()
	}
	return "ip:" + clientIP(r)
}

// aggregateDate says whether a field holds dates (stored as BSON dates), so it can be bucketed.
func aggregateDate(c *registry.Collection, f string) bool {
	if f == "_meta.created_at" || f == "_meta.updated_at" {
		return true
	}
	return c.Fields[f].Date
}

// aggregateNumeric says whether a field holds numbers, so it can be summed and averaged.
func aggregateNumeric(c *registry.Collection, f string) bool {
	if f == "_meta.version" {
		return true
	}
	t := c.Fields[f].Types
	return !c.Fields[f].Date && (slices.Contains(t, "number") || slices.Contains(t, "integer"))
}

// queryDetails turns a query's errors into response details.
func queryDetails(err error) []Detail {
	var qe query.Errors
	if !errors.As(err, &qe) {
		return nil
	}
	out := make([]Detail, 0, len(qe))
	for _, e := range qe {
		out = append(out, Detail{Path: e.Path, Reason: e.Reason})
	}
	return out
}
