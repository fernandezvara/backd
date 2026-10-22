package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rs/xid"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/fernandezvara/backd/internal/auth"
)

const clinicSchema = `{"type":"object","additionalProperties":false,"required":["name"],"properties":{
  "name":{"type":"string"},
  "tax_id":{"type":"string","pattern":"^[A-Z][0-9]+$"},
  "notes":{"type":["string","null"]},
  "history":{"type":"object"},
  "address":{"type":"object","properties":{"street":{"type":"string"},"city":{"type":"string"}}}}}`

// clinic provisions a realm whose patients encrypt some fields, with the keys already created.
func clinic(t *testing.T) (f *fixture, realm string, base string, keys map[string]string, logs *strings.Builder) {
	t.Helper()
	realm = "t" + xid.New().String()
	logs = &strings.Builder{}
	f = mongoFixture(t, map[string]string{
		realm + "/realm.yaml":                    "signup: open\nroles:\n  staff: {admin: true}\n",
		realm + "/main/patients/schema.json":     clinicSchema,
		realm + "/main/patients/collection.yaml": "fields:\n  tax_id:\n    encrypt: {key: TAX}\n  notes:\n    encrypt: {key: HEALTH}\n  history:\n    encrypt: {key: HEALTH}\n  address.street:\n    encrypt: {key: HEALTH}\nrules:\n  read: user != nil\n  write: user != nil\n",
	}, func(c *Config) { c.Log = slog.New(slog.NewJSONHandler(logs, nil)) })
	ctx := context.Background()
	keys = map[string]string{}
	for _, name := range []string{"TAX", "HEALTH"} {
		k, err := f.users[realm].CreateKey(ctx, name, "", "test")
		if err != nil {
			t.Fatal(err)
		}
		keys[name] = k.Value
	}
	for _, c := range f.reg.Realms[realm].Databases["main"].Collections {
		if err := f.rings[realm].Ensure(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	return f, realm, "/v1/" + realm + "/main/patients", keys, logs
}

func asMergePatch(h map[string]string) map[string]string {
	return map[string]string{"Authorization": h["Authorization"], "Content-Type": "application/merge-patch+json"}
}

func signedIn(t *testing.T, f *fixture, realm, email string) map[string]string {
	t.Helper()
	rec, out := f.do(t, "POST", "/v1/"+realm+"/_auth/signup", `{"email": "`+email+`", "password": "dev-p4ssw0rd!"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("signup: %d %v", rec.Code, out)
	}
	return map[string]string{"Authorization": "Bearer " + out["token"].(string), "Content-Type": "application/json"}
}

// stored reads a document as MongoDB holds it.
func stored(t *testing.T, f *fixture, realm, id string) bson.D {
	t.Helper()
	var d bson.D
	err := f.mongo.Database(realm+"__main").Collection("patients").FindOne(context.Background(), bson.D{{Key: "_id", Value: id}}).Decode(&d)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func binaryOf(d bson.D, path ...string) (bson.Binary, bool) {
	var cur any = d
	for _, p := range path {
		sub, ok := cur.(bson.D)
		if !ok {
			return bson.Binary{}, false
		}
		var next any
		for _, e := range sub {
			if e.Key == p {
				next = e.Value
			}
		}
		cur = next
	}
	b, ok := cur.(bson.Binary)
	return b, ok
}

func TestEncryptedFieldsAreCiphertextInMongoDBAndPlaintextThroughTheAPI(t *testing.T) {
	f, realm, base, keys, logs := clinic(t)
	ada := signedIn(t, f, realm, "ada@example.com")

	body := `{"name": "Ana", "tax_id": "B1234", "notes": "allergic to nuts", "history": {"visits": 3, "ok": true}, "address": {"street": "Main 1", "city": "Madrid"}}`
	rec, created := f.doH(t, "POST", base, body, ada)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	id := created["id"].(string)
	if created["tax_id"] != "B1234" || created["address"].(map[string]any)["street"] != "Main 1" {
		t.Errorf("the answer to a write is plaintext: %v", created)
	}

	// MongoDB holds binary subtype 6 where the encrypted fields were, and plaintext elsewhere.
	d := stored(t, f, realm, id)
	for _, p := range [][]string{{"tax_id"}, {"notes"}, {"history"}, {"address", "street"}} {
		b, ok := binaryOf(d, p...)
		if !ok || b.Subtype != 6 {
			t.Errorf("%v is %v", p, d)
		}
	}
	raw := d.String()
	for _, secret := range []string{"B1234", "allergic", "Main 1"} {
		if strings.Contains(raw, secret) {
			t.Errorf("the stored document shows %q: %s", secret, raw)
		}
	}
	if !strings.Contains(raw, "Madrid") || !strings.Contains(raw, "Ana") {
		t.Errorf("the other fields are stored as they are: %s", raw)
	}

	// The database refuses plaintext where a field is encrypted: nothing but binary gets in.
	_, err := f.mongo.Database(realm+"__main").Collection("patients").InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: "raw1"}, {Key: "_meta", Value: bson.D{{Key: "created_at", Value: time.Now()}, {Key: "updated_at", Value: time.Now()}, {Key: "version", Value: int64(1)}}},
		{Key: "name", Value: "x"}, {Key: "tax_id", Value: "B999"}})
	if err == nil {
		t.Error("the database accepted a plaintext in an encrypted field")
	}

	// Reads decrypt, in a single read and in a list.
	rec, got := f.doH(t, "GET", base+"/"+id, "", ada)
	if rec.Code != http.StatusOK || got["tax_id"] != "B1234" || got["notes"] != "allergic to nuts" || got["history"].(map[string]any)["visits"] != float64(3) || got["address"].(map[string]any)["street"] != "Main 1" {
		t.Errorf("get: %d %v", rec.Code, got)
	}
	rec, list := f.doH(t, "GET", base, "", ada)
	if items, _ := list["items"].([]any); rec.Code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["tax_id"] != "B1234" {
		t.Errorf("list: %d %v", rec.Code, list)
	}

	// PUT and PATCH re-encrypt; the schema still validates the plaintext.
	if rec, out := f.doH(t, "PATCH", base+"/"+id, `{"notes": "no allergies"}`, asMergePatch(ada)); rec.Code != http.StatusOK || out["notes"] != "no allergies" || out["tax_id"] != "B1234" {
		t.Errorf("patch: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "PATCH", base+"/"+id, `{"tax_id": "lowercase"}`, asMergePatch(ada)); rec.Code != http.StatusBadRequest {
		t.Errorf("a plaintext that fails the schema: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "PATCH", base+"/"+id, `{"notes": null}`, asMergePatch(ada)); rec.Code != http.StatusOK || out["notes"] != nil {
		t.Errorf("null: %d %v", rec.Code, out)
	}
	// A merge patch removes a field with null: nothing is left to encrypt.
	if _, ok := binaryOf(stored(t, f, realm, id), "notes"); ok {
		t.Error("a removed field is still stored")
	}
	// A null the client writes is a value, and is stored encrypted too.
	if rec, out := f.doH(t, "PUT", base+"/"+id, `{"name": "Ana", "tax_id": "C99", "notes": null}`, ada); rec.Code != http.StatusOK || out["tax_id"] != "C99" {
		t.Errorf("put: %d %v", rec.Code, out)
	}
	if b, ok := binaryOf(stored(t, f, realm, id), "notes"); !ok || b.Subtype != 6 {
		t.Error("null is stored encrypted")
	}
	if rec, got := f.doH(t, "GET", base+"/"+id, "", ada); got["tax_id"] != "C99" || got["notes"] != nil || rec.Code != 200 {
		t.Errorf("after put: %v", got)
	}

	// A batch (one transaction) encrypts what it creates and patches.
	rec, bout := f.doH(t, "POST", "/v1/"+realm+"/main/_batch", `{"operations": [
		{"op": "create", "collection": "patients", "document": {"name": "Cy", "tax_id": "E5", "notes": "batch note"}},
		{"op": "patch", "collection": "patients", "id": "`+id+`", "patch": {"notes": "patched in a batch"}}]}`, ada)
	if rec.Code != http.StatusOK {
		t.Fatalf("batch: %d %s", rec.Code, rec.Body)
	}
	results := bout["results"].([]any)
	batchID := results[0].(map[string]any)["id"].(string)
	if results[0].(map[string]any)["tax_id"] != "E5" || results[1].(map[string]any)["notes"] != "patched in a batch" {
		t.Errorf("batch results: %v", results)
	}
	for _, did := range []string{batchID, id} {
		if b, ok := binaryOf(stored(t, f, realm, did), "notes"); !ok || b.Subtype != 6 {
			t.Errorf("%s: notes is not stored encrypted", did)
		}
	}
	if rec, _ := f.doH(t, "DELETE", base+"/"+batchID, "", ada); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rec.Code)
	}

	// Queries and sorting on encrypted fields are refused, with a code of their own.
	for _, q := range []string{`?where={"tax_id":"C99"}`, `?where={"address.street":"x"}`, `?where={"notes":{"$contains":"a"}}`, `?order_by=tax_id`} {
		rec, out := f.doH(t, "GET", base+q, "", ada)
		if rec.Code != http.StatusBadRequest || errCode(out) != "field_encrypted" || !strings.Contains(rec.Body.String(), "encrypted") {
			t.Errorf("%s: %d %s", q, rec.Code, rec.Body)
		}
	}
	if rec, _ := f.doH(t, "GET", base+`?where={"name":"Ana"}`, "", ada); rec.Code != http.StatusOK {
		t.Errorf("an ordinary field: %d", rec.Code)
	}

	// A value moved to another document fails to decrypt instead of being accepted.
	rec, other := f.doH(t, "POST", base, `{"name": "Ben", "tax_id": "D5"}`, ada)
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body)
	}
	otherID := other["id"].(string)
	swap := stored(t, f, realm, id)
	b, _ := binaryOf(swap, "tax_id")
	if _, err := f.mongo.Database(realm+"__main").Collection("patients").UpdateOne(context.Background(), bson.D{{Key: "_id", Value: otherID}}, bson.D{{Key: "$set", Value: bson.D{{Key: "tax_id", Value: b}}}}); err != nil {
		t.Fatal(err)
	}
	rec, out := f.doH(t, "GET", base+"/"+otherID, "", ada)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "C99") || strings.Contains(rec.Body.String(), "D5") {
		t.Errorf("a moved value: %d %v", rec.Code, out)
	}
	if l := logs.String(); !strings.Contains(l, "encrypted field") || strings.Contains(l, "C99") || strings.Contains(l, "B1234") || strings.Contains(l, "D5") {
		t.Error("the log holds a value")
	}

	// Rotating a key re-wraps its data keys and touches no document; reading goes on.
	before := stored(t, f, realm, id)
	res, err := f.users[realm].RotateKey(context.Background(), "HEALTH", "", false, "test")
	if err != nil || res.Value == "" || res.Value == keys["HEALTH"] || res.Rewrapped < 3 {
		t.Fatalf("rotate: %+v %v", res, err)
	}
	if after := stored(t, f, realm, id); after.String() != before.String() {
		t.Error("a rotation rewrote a document")
	}
	if rec, got := f.doH(t, "GET", base+"/"+id, "", ada); rec.Code != 200 || got["tax_id"] != "C99" {
		t.Errorf("after rotation: %d %v", rec.Code, got)
	}
	// A process that has not seen the data keys yet opens them with the new key alone.
	f.rings[realm].Reset()
	if rec, got := f.doH(t, "GET", base+"/"+id, "", ada); rec.Code != 200 || got["tax_id"] != "C99" {
		t.Errorf("fresh cache: %d %v", rec.Code, got)
	}
}

func TestExportAndImportRefuseCollectionsWithEncryptedFields(t *testing.T) {
	f, realm, _, _, _ := clinic(t)
	_, adminKey, err := f.users[realm].CreateAPIKey(context.Background(), "ops", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	rec, out := f.doH(t, "GET", "/v1/"+realm+"/_admin/export", "", map[string]string{"Authorization": "Bearer " + adminKey})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "main/patients") {
		t.Errorf("export: %d %v", rec.Code, out)
	}
}
