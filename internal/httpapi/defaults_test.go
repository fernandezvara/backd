package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/fernandezvara/backd/internal/registry"
)

func defaultsFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "shop", "orders", "items")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	schema := `{"type":"object","required":["status"],"additionalProperties":false,"properties":{
	  "name":{"type":"string"},
	  "status":{"enum":["draft","sent"],"default":"draft"},
	  "qty":{"type":"integer","default":1}}}`
	if err := os.WriteFile(filepath.Join(dir, "schema.json"), []byte(schema), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "shop", registry.RealmFile), []byte("auth: disabled\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return newFixtureWith(t, reg, &memStore{})
}

func TestSchemaDefaultsOnWrite(t *testing.T) {
	f := defaultsFixture(t)

	// A create fills what the request leaves out, even a required field.
	rec, body := f.do(t, "POST", items, `{"name":"a"}`)
	if rec.Code != http.StatusCreated || body["status"] != "draft" || body["qty"] != float64(1) {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	id := body["id"].(string)
	if rec, body = f.do(t, "POST", items, `{"status":"sent","qty":7}`); rec.Code != http.StatusCreated || body["status"] != "sent" || body["qty"] != float64(7) {
		t.Errorf("explicit values = %d %s", rec.Code, rec.Body)
	}

	// A PATCH changes only what it names: it never refills a field.
	if rec, body = f.do(t, "PATCH", items+"/"+id, `{"qty":null}`); rec.Code == http.StatusOK && body["qty"] == float64(1) {
		t.Errorf("patch refilled a default: %s", rec.Body)
	}

	// A PUT replaces the whole document, so the defaults come back.
	rec, body = f.do(t, "PUT", items+"/"+id, `{"name":"b"}`)
	if rec.Code != http.StatusOK || body["status"] != "draft" || body["qty"] != float64(1) {
		t.Errorf("put = %d %s", rec.Code, rec.Body)
	}

	// _batch creates fill them too.
	rec, _ = f.do(t, "POST", "/v1/shop/orders/_batch", `{"operations":[{"op":"create","collection":"items","document":{"name":"c"}}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("batch = %d %s", rec.Code, rec.Body)
	}
	found := false
	for _, d := range f.store.docs["shop__orders.items"] {
		if d["name"] == "c" {
			found = d["status"] == "draft" && d["qty"] == int64(1)
		}
	}
	if !found {
		t.Errorf("batch create didn't get the defaults: %s", rec.Body)
	}
}
