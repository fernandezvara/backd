package httpapi

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/triggers"
)

// triggersStatus answers GET /v1/{realm}/_admin/triggers/status: whether a dispatcher holds the
// lease, how fresh its position is, the realm's triggers (and that each of their functions has
// admin data access), the unfinished trigger jobs and the gaps not yet resolved. The lease
// holder's name is left out: it is an instance of the deployment, not of the realm.
func (a *adminAPI) triggersStatus(w http.ResponseWriter, r *http.Request) {
	realm := chi.URLParam(r, "realm")
	out := map[string]any{"dispatcher": nil, "triggers": a.realmTriggers(realm), "backlog": nil, "open_gaps": nil}
	if a.triggerAdmin != nil {
		st, err := a.triggerAdmin.Status(r.Context())
		if err != nil {
			adminError(w, r, err)
			return
		}
		d := map[string]any{"started": st.Found, "held": st.Held, "epoch": st.Epoch, "watermark_at": nil, "watermark_age_seconds": nil, "lease_expires_at": nil}
		if !st.WatermarkAt.IsZero() {
			d["watermark_at"] = formatTime(st.WatermarkAt)
			d["watermark_age_seconds"] = max(0, time.Since(st.WatermarkAt).Seconds())
		}
		if !st.LeaseExpires.IsZero() {
			d["lease_expires_at"] = formatTime(st.LeaseExpires)
		}
		out["dispatcher"] = d
		if open, ok := a.openGaps(r.Context(), realm); ok {
			out["open_gaps"] = open
		}
	}
	if c, ok := usersOf(r).Store.(interface {
		CountTriggerJobs(context.Context) (int, error)
	}); ok {
		if n, err := c.CountTriggerJobs(r.Context()); err == nil {
			out["backlog"] = n
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *adminAPI) openGaps(ctx context.Context, realm string) (int, bool) {
	gaps, err := a.triggerAdmin.Gaps(ctx, realm, false, 1000)
	if err != nil {
		return 0, false
	}
	return len(gaps), true
}

// realmTriggers lists the realm's triggers, by database, collection and name.
func (a *adminAPI) realmTriggers(realm string) []map[string]any {
	out := []map[string]any{}
	rl := a.reg.Realms[realm]
	if rl == nil {
		return out
	}
	for _, dbName := range slices.Sorted(maps.Keys(rl.Databases)) {
		for _, c := range rl.Databases[dbName].SortedCollections() {
			ts := slices.Clone(c.Triggers)
			slices.SortFunc(ts, func(x, y *registry.Trigger) int {
				if x.Name < y.Name {
					return -1
				}
				if x.Name > y.Name {
					return 1
				}
				return 0
			})
			for _, t := range ts {
				var when any
				if t.When != nil {
					when = t.When.Source
				}
				var fields any
				if len(t.Fields) > 0 {
					fields = t.Fields
				}
				out = append(out, map[string]any{
					"database": c.Database, "collection": c.Name, "name": t.Name, "events": t.Events,
					"when": when, "function": t.Function, "fields": fields, "session": t.Session, "ordered": t.Ordered,
					// A trigger's function always has ctx.admin.db: the realm declared the trigger.
					"runs_with_admin_access": true,
				})
			}
		}
	}
	return out
}

func gapJSON(g triggers.GapRecord) map[string]any {
	out := map[string]any{
		"id": g.ID, "reason": g.Reason, "realm": nil, "collection": nil, "trigger": nil,
		"from": formatTime(g.From), "to": formatTime(g.To), "count": g.Count, "keys": g.Keys,
		"recorded_at": formatTime(g.RecordedAt), "resolved_at": nil,
	}
	if out["keys"] == nil || len(g.Keys) == 0 {
		out["keys"] = []string{}
	}
	for k, v := range map[string]string{"realm": g.Realm, "collection": g.Collection, "trigger": g.Trigger} {
		if v != "" {
			out[k] = v
		}
	}
	if !g.ResolvedAt.IsZero() {
		out["resolved_at"] = formatTime(g.ResolvedAt)
	}
	return out
}

// listTriggerGaps answers GET /v1/{realm}/_admin/triggers/gaps: what the dispatcher could not
// turn into jobs, newest first; `all=true` includes the resolved ones.
func (a *adminAPI) listTriggerGaps(w http.ResponseWriter, r *http.Request) {
	if a.triggerAdmin == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		return
	}
	q := r.URL.Query()
	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeError(w, r, http.StatusBadRequest, codeInvalidQuery, "limit must be between 1 and 500")
			return
		}
		limit = n
	}
	gaps, err := a.triggerAdmin.Gaps(r.Context(), chi.URLParam(r, "realm"), q.Get("all") == "true", limit)
	if err != nil {
		adminError(w, r, err)
		return
	}
	items := make([]map[string]any, len(gaps))
	for i, g := range gaps {
		items[i] = gapJSON(g)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// resolveTriggerGap answers POST /v1/{realm}/_admin/triggers/gaps/{id}/resolve: the operator
// says the gap has been dealt with (a rescan, or nothing was lost that matters). It is audited.
func (a *adminAPI) resolveTriggerGap(w http.ResponseWriter, r *http.Request) {
	if a.triggerAdmin == nil {
		notFound(w, r)
		return
	}
	id := chi.URLParam(r, "id")
	now := time.Now().UTC()
	ok, err := a.triggerAdmin.ResolveGap(r.Context(), chi.URLParam(r, "realm"), id, now)
	if err != nil {
		adminError(w, r, err)
		return
	}
	if !ok {
		adminError(w, r, errors.Join(auth.ErrNotFound))
		return
	}
	usersOf(r).Audit(r.Context(), auth.AuditTriggerGapResolve, "trigger-gap:"+id, nil)
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "resolved_at": formatTime(now)})
}
