package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/executor"
)

func TestParseSchemaVersions(t *testing.T) {
	got, err := parseSchemaVersions([]string{"main/assets=1, main/pins=3", "other/notes=12"})
	if err != nil || len(got) != 3 || got["main/assets"] != 1 || got["main/pins"] != 3 || got["other/notes"] != 12 {
		t.Fatalf("parsed %v, %v", got, err)
	}
	for _, bad := range []string{"", "main/assets", "main/assets=", "main/assets=0", "main/assets=-1", "main/assets=01", "main/assets=x", "assets=1", "main/=1", "/assets=1", "Main/assets=1", "main/assets=1,", "main/assets=1,,main/pins=2", "main/assets=1, main/assets=2", "main/assets=1.5", "a/b/c=1"} {
		if _, err := parseSchemaVersions([]string{bad}); err == nil {
			t.Errorf("%q should be malformed", bad)
		}
	}
	if got, err := parseSchemaVersions([]string{"main/assets=2, main/assets=2"}); err != nil || got["main/assets"] != 2 {
		t.Errorf("the same version twice is fine: %v %v", got, err)
	}
}

// The version an app declares: writes are stamped with it, entries for other or unversioned
// collections are ignored, one above the current version or a malformed header is a 400, and the
// write is validated against the current schema either way.
func TestDeclaredSchemaVersionStampsWrites(t *testing.T) {
	f := newRulesFixture(t)
	const revisions, notes = "/v1/acme/app/revisions", "/v1/acme/app/notes"
	as := func(extra ...string) map[string]string {
		h := map[string]string{"Authorization": "Bearer " + f.ada, "Content-Type": "application/json"}
		for i := 0; i+1 < len(extra); i += 2 {
			h[extra[i]] = extra[i+1]
		}
		return h
	}
	stamp := func(id string) any {
		f.store.mu.Lock()
		defer f.store.mu.Unlock()
		return f.store.docs[revisionsKey][id]["_meta"].(map[string]any)["schema_version"]
	}

	// An app built for version 1 stores its create as version 1; one for version 2, and one that says nothing, as 2.
	rec, body := f.doH(t, "POST", revisions, `{"title": "from v1"}`, as(schemaVersionHeader, "app/revisions=1"))
	if rec.Code != http.StatusCreated || schemaVersionOf(body) != float64(1) {
		t.Fatalf("v1 create = %d %s", rec.Code, rec.Body)
	}
	id := body["id"].(string)
	if rec, body := f.doH(t, "POST", revisions, `{"title": "from v2"}`, as(schemaVersionHeader, "app/revisions=2")); rec.Code != http.StatusCreated || schemaVersionOf(body) != float64(2) {
		t.Fatalf("v2 create = %d %s", rec.Code, rec.Body)
	}
	if rec, body := f.doH(t, "POST", revisions, `{"title": "untyped"}`, as()); rec.Code != http.StatusCreated || schemaVersionOf(body) != float64(2) {
		t.Fatalf("create without the header = %d %s, want the current version", rec.Code, rec.Body)
	}
	// Entries for collections the request doesn't touch are ignored.
	if rec, body := f.doH(t, "POST", revisions, `{"title": "elsewhere"}`, as(schemaVersionHeader, "app/strict=2, app/ghost=9")); rec.Code != http.StatusCreated || schemaVersionOf(body) != float64(2) {
		t.Fatalf("create declaring other collections = %d %s", rec.Code, rec.Body)
	}

	// Every write kind follows the declared version; the write is still validated against the current schema.
	for name, do := range map[string]func(h map[string]string) int{
		"PUT": func(h map[string]string) int {
			rec, _ := f.doH(t, "PUT", revisions+"/"+id, `{"title": "put"}`, h)
			return rec.Code
		},
		"PATCH": func(h map[string]string) int {
			h["Content-Type"] = "application/merge-patch+json"
			rec, _ := f.doH(t, "PATCH", revisions+"/"+id, `{"title": "patched"}`, h)
			return rec.Code
		},
		"batch": func(h map[string]string) int {
			rec, _ := f.doH(t, "POST", "/v1/acme/app/_batch", `{"operations": [{"op": "patch", "collection": "revisions", "id": "`+id+`", "patch": {"title": "batched"}}]}`, h)
			return rec.Code
		},
	} {
		if code := do(as(schemaVersionHeader, "app/revisions=2")); code != http.StatusOK || stamp(id) != int64(2) {
			t.Fatalf("%s as v2 = %d, stamp %v", name, code, stamp(id))
		}
		if code := do(as(schemaVersionHeader, "app/revisions=1")); code != http.StatusOK || stamp(id) != int64(1) {
			t.Fatalf("%s as v1 = %d, stamp %v, want 1", name, code, stamp(id))
		}
	}
	if rec, _ := f.doH(t, "PUT", revisions+"/"+id, `{"title": 5}`, as(schemaVersionHeader, "app/revisions=1")); rec.Code != http.StatusBadRequest {
		t.Errorf("a v1 write is still validated against the current schema: %d", rec.Code)
	}

	// Refusals.
	for header, want := range map[string]string{
		"app/revisions=3":    codeSchemaVersionUnknown,
		"app/revisions=abc":  codeInvalidSchemaVersion,
		"app/revisions":      codeInvalidSchemaVersion,
		"app/revisions=1, ,": codeInvalidSchemaVersion,
	} {
		rec, body := f.doH(t, "POST", revisions, `{"title": "x"}`, as(schemaVersionHeader, header))
		if code, _ := errorOf(body); rec.Code != http.StatusBadRequest || code != want {
			t.Errorf("header %q = %d %s, want 400 %s", header, rec.Code, rec.Body, want)
		}
	}
	// A malformed header is refused on an unversioned collection too; a well-formed entry for it is ignored.
	if rec, body := f.doH(t, "GET", notes, "", as(schemaVersionHeader, "app/notes=")); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed header on an unversioned collection = %d %v", rec.Code, body)
	}
	if rec, _ := f.doH(t, "GET", notes, "", as(schemaVersionHeader, "app/notes=7")); rec.Code != http.StatusOK {
		t.Errorf("an entry for an unversioned collection should be ignored: %d", rec.Code)
	}
	// Reads declare it too: above the current version is refused there as well.
	if rec, _ := f.doH(t, "GET", revisions, "", as(schemaVersionHeader, "app/revisions=3")); rec.Code != http.StatusBadRequest {
		t.Errorf("a read declaring a future version = %d", rec.Code)
	}

	// `where` and `order_by` accept the stamp, so an app can find what is still old.
	rec, list := f.doH(t, "GET", revisions+`?where={"_meta.schema_version":1}&order_by=_meta.schema_version`, "", as())
	items, _ := list["items"].([]any)
	if rec.Code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["id"] != id {
		t.Errorf("where on the stamp = %d %s", rec.Code, rec.Body)
	}
}

// An app below min_schema_version is told to update on every kind of request, with the
// collection, the minimum and what it declared; requests that declare nothing are never refused.
func TestMinSchemaVersionAnswers426(t *testing.T) {
	f := newRulesFixture(t, func(cfg *Config) { cfg.Realtime = testHub(t) })
	const strict = "/v1/acme/app/strict"
	as := func(extra ...string) map[string]string {
		h := map[string]string{"Authorization": "Bearer " + f.ada, "Content-Type": "application/json"}
		for i := 0; i+1 < len(extra); i += 2 {
			h[extra[i]] = extra[i+1]
		}
		return h
	}
	rec, created := f.doH(t, "POST", strict, `{"title": "t"}`, as()) // declares nothing
	if rec.Code != http.StatusCreated {
		t.Fatalf("create without the header = %d %s", rec.Code, rec.Body)
	}
	id := created["id"].(string)

	old := schemaVersionHeader
	refused := func(name, method, path, body string, hdr map[string]string) {
		t.Helper()
		rec, out := f.doH(t, method, path, body, hdr)
		e, _ := out["error"].(map[string]any)
		if rec.Code != http.StatusUpgradeRequired || e["code"] != "upgrade_required" || e["collection"] != "app/strict" || e["min"] != float64(2) || e["declared"] != float64(1) {
			t.Errorf("%s = %d %s, want 426 upgrade_required for app/strict (min 2, declared 1)", name, rec.Code, rec.Body)
		}
	}
	v1 := as(old, "app/strict=1")
	refused("list", "GET", strict, "", v1)
	refused("get", "GET", strict+"/"+id, "", v1)
	refused("create", "POST", strict, `{"title": "x"}`, v1)
	refused("put", "PUT", strict+"/"+id, `{"title": "x"}`, v1)
	refused("delete", "DELETE", strict+"/"+id, "", v1)
	patch := as(old, "app/strict=1")
	patch["Content-Type"] = "application/merge-patch+json"
	refused("patch", "PATCH", strict+"/"+id, `{"title": "x"}`, patch)
	refused("batch", "POST", "/v1/acme/app/_batch", `{"operations": [{"op": "create", "collection": "strict", "document": {"title": "x"}}]}`, v1)
	// An expansion reads the collection too: the app declares the version it was built for.
	refused("expand", "GET", "/v1/acme/app/revisions?expand=gate", "", as(old, "app/strict=1"))
	refused("realtime", "POST", "/v1/acme/_realtime", `{"subscriptions":[{"id":"a","db":"app","collection":"strict"}]}`, as(old, "app/strict=1"))

	// At or above the minimum, or declaring nothing about it, is served.
	for _, h := range []map[string]string{as(old, "app/strict=2"), as(), as(old, "app/revisions=1")} {
		if rec, _ := f.doH(t, "GET", strict+"/"+id, "", h); rec.Code != http.StatusOK {
			t.Errorf("get with %q = %d", h[old], rec.Code)
		}
	}
	// A request for another collection that declares an old version of gated is not refused.
	if rec, _ := f.doH(t, "GET", "/v1/acme/app/revisions", "", as(old, "app/strict=1")); rec.Code != http.StatusOK {
		t.Errorf("another collection = %d", rec.Code)
	}
	// An API key declaring nothing (a script) is never refused.
	if rec, _ := f.doH(t, "GET", strict, "", map[string]string{"Authorization": "Bearer " + f.key}); rec.Code != http.StatusOK {
		t.Errorf("an API key without the header = %d", rec.Code)
	}
}

// CORS: the browser may send the header.
func TestSchemaVersionHeaderIsCORSAllowed(t *testing.T) {
	if !strings.Contains(corsAllowHeaders, schemaVersionHeader) {
		t.Errorf("%s is not in the allowed request headers: %s", schemaVersionHeader, corsAllowHeaders)
	}
}

// An old app's write to a document a newer app moved up stores it as the old version; the job of
// the next start migrates it again.
func TestOldAppWriteIsMigratedAgain(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	w := newTestWorker(t, f)
	w.migrateDocsPerSecond = 1_000_000
	ctx := context.Background()
	as := map[string]string{"Authorization": "Bearer " + f.ada, "Content-Type": "application/json", schemaVersionHeader: "app/revisions=1"}

	rec, body := f.doH(t, "POST", "/v1/acme/app/revisions", `{"title": "made by a v2 app"}`, map[string]string{"Authorization": "Bearer " + f.ada, "Content-Type": "application/json"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	id := body["id"].(string)
	if rec, _ := f.doH(t, "PUT", "/v1/acme/app/revisions/"+id, `{"title": "edited by a v1 app"}`, as); rec.Code != http.StatusOK {
		t.Fatalf("v1 put: %d %s", rec.Code, rec.Body)
	}
	if d := f.revision(id); d["_meta"].(map[string]any)["schema_version"] != int64(1) {
		t.Fatalf("the v1 write should be stored as v1: %+v", d)
	}
	f.runner.migrate = func(req executor.MigrateRequest) (executor.MigrateResult, error) {
		res := executor.MigrateResult{Status: executor.StatusOK}
		for _, d := range req.Docs {
			res.Outcomes = append(res.Outcomes, executor.MigrateOutcome{ID: d.ID, Fields: d.Fields})
		}
		return res, nil
	}
	w.QueueSchemaMigrations(ctx) // the next start
	w.RunOnce(ctx)
	d := f.revision(id)
	if d["_meta"].(map[string]any)["schema_version"] != int64(2) || d["title"] != "edited by a v1 app" {
		t.Errorf("after the job: %+v", d)
	}
}

// An imported document may carry the schema version it was written under.
func TestImportedMetaMayCarryTheSchemaVersion(t *testing.T) {
	if p := checkImportedMeta(map[string]any{"version": int64(1), "schema_version": int64(2)}); len(p) != 0 {
		t.Errorf("problems = %v", p)
	}
	for _, bad := range []any{int64(0), "2", 1.5} {
		if p := checkImportedMeta(map[string]any{"schema_version": bad}); len(p) != 1 || p[0].Path != "_meta.schema_version" {
			t.Errorf("%v: problems = %v", bad, p)
		}
	}
}
