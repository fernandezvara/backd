package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rs/xid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/auth"
)

const clinicSchema = `{"type":"object","additionalProperties":false,"required":["name"],"properties":{
  "name":{"type":"string"},
  "tax_id":{"type":"string","pattern":"^[A-Z][0-9]+$"},
  "handle":{"type":"string"},
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
		realm + "/main/lookups/schema.json":      `{"type":"object","properties":{"code":{"type":"string"},"label":{"type":"string"}}}`,
		realm + "/main/lookups/collection.yaml":  "fields:\n  code:\n    encrypt: {key: TAX, search: equality}\nrules:\n  read: user != nil && document.code in ['X1', 'X2']\n  write: user != nil\n",
		realm + "/main/patients/collection.yaml": "fields:\n  tax_id:\n    encrypt: {key: TAX, search: equality, unique: true}\n  handle:\n    encrypt: {key: TAX, search: equality, normalize: [trim, lower]}\n  notes:\n    encrypt: {key: HEALTH}\n  history:\n    encrypt: {key: HEALTH}\n  address.street:\n    encrypt: {key: HEALTH}\nrules:\n  read: user != nil\n  write: user != nil\n",
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
	for _, q := range []string{`?where={"tax_id":{"$ne":"C99"}}`, `?where={"tax_id":{"$gt":"A"}}`, `?where={"tax_id":{"$startsWith":"C"}}`, `?where={"tax_id":null}`, `?where={"tax_id":{"$isNull":true}}`, `?where={"address.street":"x"}`, `?where={"notes":{"$contains":"a"}}`, `?order_by=tax_id`, `?order_by=-handle`} {
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

func TestSearchableEncryptedFieldsAnswerEqualityThroughTheBlindIndex(t *testing.T) {
	f, realm, base, _, _ := clinic(t)
	ada := signedIn(t, f, realm, "ada@example.com")
	ids := map[string]string{}
	for _, p := range []struct{ name, tax, handle string }{{"Ana", "B1", "  Ana.Garcia "}, {"Ben", "B2", "ben"}, {"Cy", "B3", "ANA.GARCIA"}} {
		rec, out := f.doH(t, "POST", base, `{"name": "`+p.name+`", "tax_id": "`+p.tax+`", "handle": "`+p.handle+`"}`, ada)
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", p.name, rec.Code, rec.Body)
		}
		ids[p.name] = out["id"].(string)
	}
	names := func(q string) string {
		t.Helper()
		rec, out := f.doH(t, "GET", base+"?order_by=name&where="+url.QueryEscape(q), "", ada)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", q, rec.Code, rec.Body)
		}
		var got []string
		for _, it := range out["items"].([]any) {
			got = append(got, it.(map[string]any)["name"].(string))
		}
		return strings.Join(got, ",")
	}
	if got := names(`{"tax_id":"B2"}`); got != "Ben" {
		t.Errorf("= : %q", got)
	}
	if got := names(`{"tax_id":{"$in":["B1","B3","B9"]}}`); got != "Ana,Cy" {
		t.Errorf("in: %q", got)
	}
	if got := names(`{"tax_id":"B9"}`); got != "" {
		t.Errorf("no match: %q", got)
	}
	// Normalization applies to what is stored and to what is searched for.
	if got := names(`{"handle":"ana.garcia"}`); got != "Ana,Cy" {
		t.Errorf("normalized: %q", got)
	}
	if got := names(`{"handle":" ANA.garcia  "}`); got != "Ana,Cy" {
		t.Errorf("normalized query: %q", got)
	}
	if got := names(`{"$or":[{"tax_id":"B1"},{"handle":"ben"}]}`); got != "Ana,Ben" {
		t.Errorf("or: %q", got)
	}
	if got := names(`{"tax_id":"B1","name":"Cy"}`); got != "" {
		t.Errorf("combined with a plain field: %q", got)
	}
	// Counts follow the same filter.
	if _, out := f.doH(t, "GET", base+`?count=true&where={"handle":"ana.garcia"}`, "", ada); out["total"] != float64(2) {
		t.Errorf("total: %v", out["total"])
	}

	// The blind index is hidden: stored, indexed, and never returned or accepted.
	d := stored(t, f, realm, ids["Ana"])
	if !strings.Contains(d.String(), "_bidx") {
		t.Errorf("no blind index stored: %s", d)
	}
	_, got := f.doH(t, "GET", base+"/"+ids["Ana"], "", ada)
	if _, has := got["_bidx"]; has || strings.Contains(fmt.Sprint(got), "bidx") {
		t.Errorf("the blind index is returned: %v", got)
	}
	// A client-sent _bidx is dropped, not stored.
	rec, forged := f.doH(t, "POST", base, `{"name": "Dee", "tax_id": "B4", "_bidx": {"tax_id": "AAAA"}}`, ada)
	if rec.Code == http.StatusCreated {
		if raw := stored(t, f, realm, forged["id"].(string)).String(); strings.Contains(raw, "AAAA") {
			t.Errorf("a forged index was stored: %s", raw)
		}
	}

	// Equal values clash on a unique blind index, naming the field, not the index.
	rec, out := f.doH(t, "POST", base, `{"name": "Dup", "tax_id": "B1"}`, ada)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "tax_id") || strings.Contains(rec.Body.String(), "bidx") {
		t.Errorf("duplicate: %d %v", rec.Code, out)
	}
	// Documents without the field don't clash with each other on a unique blind index.
	for _, n := range []string{"NoTax1", "NoTax2"} {
		if rec, _ := f.doH(t, "POST", base, `{"name": "`+n+`"}`, ada); rec.Code != http.StatusCreated {
			t.Errorf("%s: %d %s", n, rec.Code, rec.Body)
		}
	}
	// Changing it away frees the value; patching to a taken one clashes.
	if rec, _ := f.doH(t, "PATCH", base+"/"+ids["Ana"], `{"tax_id": "B7"}`, asMergePatch(ada)); rec.Code != http.StatusOK {
		t.Errorf("change: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := f.doH(t, "POST", base, `{"name": "Reuse", "tax_id": "B1"}`, ada); rec.Code != http.StatusCreated {
		t.Errorf("reuse: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := f.doH(t, "PATCH", base+"/"+ids["Ben"], `{"tax_id": "B7"}`, asMergePatch(ada)); rec.Code != http.StatusConflict {
		t.Errorf("patch to a taken value: %d", rec.Code)
	}
	if got := names(`{"tax_id":"B7"}`); got != "Ana" {
		t.Errorf("after change: %q", got)
	}
}

func TestReadRulesPushEqualityOnEncryptedFieldsDown(t *testing.T) {
	f, realm, _, _, _ := clinic(t)
	ada := signedIn(t, f, realm, "ada@example.com")
	base := "/v1/" + realm + "/main/lookups"
	ids := map[string]string{}
	for _, code := range []string{"X1", "X2", "X3"} {
		rec, out := f.doH(t, "POST", base, `{"code": "`+code+`", "label": "`+code+`"}`, ada)
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", code, rec.Code, rec.Body)
		}
		ids[code] = out["id"].(string)
	}
	rec, out := f.doH(t, "GET", base+"?order_by=label", "", ada)
	var labels []string
	for _, it := range out["items"].([]any) {
		labels = append(labels, it.(map[string]any)["label"].(string))
	}
	if rec.Code != http.StatusOK || strings.Join(labels, ",") != "X1,X2" {
		t.Errorf("list through the rule: %d %v", rec.Code, labels)
	}
	if rec, _ := f.doH(t, "GET", base+"/"+ids["X1"], "", ada); rec.Code != http.StatusOK {
		t.Errorf("an allowed document: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", base+"/"+ids["X3"], "", ada); rec.Code != http.StatusNotFound {
		t.Errorf("a document the rule forbids: %d", rec.Code)
	}
}

// revealClinic is clinic with a reveal rule on the notes, a doctor role, a read-only
// administrator who may read data, and visits that reference their patient.
func revealClinic(t *testing.T) (f *fixture, realm, base string, logs *strings.Builder) {
	t.Helper()
	realm = "t" + xid.New().String()
	logs = &strings.Builder{}
	f = mongoFixture(t, map[string]string{
		realm + "/realm.yaml":                    "signup: open\nroles:\n  doctor: {}\n  staff: {admin: true}\n  auditor: {admin: read}\nadmin:\n  read_access: {data: true}\n",
		realm + "/main/patients/schema.json":     clinicSchema,
		realm + "/main/patients/collection.yaml": "fields:\n  notes:\n    encrypt: {key: HEALTH}\n    reveal: user != nil && (hasRole(user, 'doctor') || document._meta.owner == user.id)\n  history:\n    encrypt: {key: HEALTH}\n  address.street:\n    encrypt: {key: HEALTH}\n    reveal: hasRole(user, 'doctor')\nrules:\n  read: user != nil\n  write: user != nil\n",
		realm + "/main/visits/schema.json":       `{"type":"object","properties":{"patient":{"type":"string"},"reason":{"type":"string"}}}`,
		realm + "/main/visits/collection.yaml":   "references:\n  patient: {field: patient, collection: patients}\nrules:\n  read: user != nil\n  write: user != nil\n",
	}, func(c *Config) { c.Log = slog.New(slog.NewJSONHandler(logs, nil)) })
	ctx := context.Background()
	if _, err := f.users[realm].CreateKey(ctx, "HEALTH", "", "test"); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.reg.Realms[realm].Databases["main"].Collections {
		if err := f.rings[realm].Ensure(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	return f, realm, "/v1/" + realm + "/main", logs
}

func TestRevealRulesHideEncryptedFieldsFromWhoMayNotSeeThem(t *testing.T) {
	f, realm, base, _ := revealClinic(t)
	ctx := context.Background()
	ada := signedIn(t, f, realm, "ada@example.com")
	bob := signedIn(t, f, realm, "bob@example.com")
	patients := base + "/patients"

	rec, created := f.doH(t, "POST", patients, `{"name": "Ana", "notes": "allergic to nuts", "history": {"visits": 3}, "address": {"street": "Main 1", "city": "Madrid"}}`, ada)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	id := created["id"].(string)
	// The owner created it: the response is the owner's view (notes yes, street no: not a doctor).
	meta := created["_meta"].(map[string]any)
	if created["notes"] != "allergic to nuts" || created["address"].(map[string]any)["street"] != nil || fmt.Sprint(meta["redacted"]) != "[address.street]" {
		t.Errorf("the owner's view of a create: %v", created)
	}

	view := func(who map[string]string) (map[string]any, string) {
		t.Helper()
		rec, out := f.doH(t, "GET", patients+"/"+id, "", who)
		if rec.Code != http.StatusOK {
			t.Fatalf("get: %d %s", rec.Code, rec.Body)
		}
		return out, rec.Header().Get("ETag")
	}
	// A reader who passes the rule sees the field; one who doesn't gets null and the list of what is hidden.
	adaDoc, adaTag := view(ada)
	if adaDoc["notes"] != "allergic to nuts" || fmt.Sprint(adaDoc["_meta"].(map[string]any)["redacted"]) != "[address.street]" {
		t.Errorf("ada: %v", adaDoc)
	}
	bobDoc, bobTag := view(bob)
	if bobDoc["notes"] != nil || bobDoc["history"].(map[string]any)["visits"] != float64(3) ||
		fmt.Sprint(bobDoc["_meta"].(map[string]any)["redacted"]) != "[address.street notes]" {
		t.Errorf("bob: %v", bobDoc)
	}
	if _, has := bobDoc["notes"]; !has {
		t.Error("a hidden field is absent instead of null: the client can't tell")
	}
	if adaTag == bobTag || !strings.Contains(bobTag, ".r") || !strings.HasPrefix(adaTag, `"1.r`) {
		t.Errorf("tags: %s %s", adaTag, bobTag)
	}
	// Lists apply the same rule per document.
	_, list := f.doH(t, "GET", patients, "", bob)
	if it := list["items"].([]any)[0].(map[string]any); it["notes"] != nil || fmt.Sprint(it["_meta"].(map[string]any)["redacted"]) != "[address.street notes]" {
		t.Errorf("list: %v", it)
	}

	// Conditional GET: bob's tag is his view's; when his role changes the view does, and the old tag stops matching.
	if rec, _ := f.doH(t, "GET", patients+"/"+id, "", map[string]string{"Authorization": bob["Authorization"], "If-None-Match": bobTag}); rec.Code != http.StatusNotModified {
		t.Errorf("same view: %d", rec.Code)
	}
	if err := f.users[realm].AddRole(ctx, "bob@example.com", "doctor"); err != nil {
		t.Fatal(err)
	}
	rec, _ = f.doH(t, "GET", patients+"/"+id, "", map[string]string{"Authorization": bob["Authorization"], "If-None-Match": bobTag})
	if rec.Code != http.StatusOK {
		t.Errorf("a revealed view must not be a 304: %d", rec.Code)
	}
	if doc, tag := view(bob); doc["notes"] != "allergic to nuts" || doc["address"].(map[string]any)["street"] != "Main 1" || tag != `"1"` || doc["_meta"].(map[string]any)["redacted"] != nil {
		t.Errorf("bob as a doctor: %v %s", doc, tag)
	}
	if err := f.users[realm].RemoveRole(ctx, "bob@example.com", "doctor"); err != nil {
		t.Fatal(err)
	}

	// Writing needs no reveal: bob changes the street he can't read, and the notes he can't read stay as they were.
	rec, out := f.doH(t, "PATCH", patients+"/"+id, `{"address": {"street": "Side 2", "city": "Madrid"}}`, asMergePatch(bob))
	if rec.Code != http.StatusOK || out["notes"] != nil || fmt.Sprint(out["_meta"].(map[string]any)["redacted"]) != "[address.street notes]" {
		t.Errorf("patch answer: %d %v", rec.Code, out)
	}
	rec, _ = f.doH(t, "PATCH", patients+"/"+id, `{"name": "Ana B."}`, asMergePatch(bob))
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}
	if doc, _ := view(ada); doc["notes"] != "allergic to nuts" || doc["name"] != "Ana B." {
		t.Errorf("a patch by someone who can't read the notes changed them: %v", doc)
	}
	if rec, _ := f.doH(t, "PATCH", patients+"/"+id, `{"notes": "no allergies"}`, asMergePatch(bob)); rec.Code != http.StatusOK {
		t.Errorf("replacing a hidden field: %d", rec.Code)
	}
	if doc, _ := view(ada); doc["notes"] != "no allergies" {
		t.Errorf("the replaced notes: %v", doc["notes"])
	}
	if doc, _ := view(bob); doc["notes"] != nil {
		t.Errorf("bob reads what he wrote: %v", doc["notes"])
	}

	// A batch answers with the caller's view too.
	rec, bout := f.doH(t, "POST", base+"/_batch", `{"operations": [{"op": "patch", "collection": "patients", "id": "`+id+`", "patch": {"name": "Ana C."}}]}`, bob)
	if res := bout["results"].([]any)[0].(map[string]any); rec.Code != http.StatusOK || res["notes"] != nil {
		t.Errorf("batch: %d %v", rec.Code, bout)
	}

	// An expanded patient is shown by the patient collection's own rules.
	rec, visit := f.doH(t, "POST", base+"/visits", `{"patient": "`+id+`", "reason": "checkup"}`, ada)
	if rec.Code != http.StatusCreated {
		t.Fatalf("visit: %d %s", rec.Code, rec.Body)
	}
	for who, wantNotes := range map[string]any{"ada": "no allergies", "bob": nil} {
		hdr := map[string]string{"ada": ada["Authorization"], "bob": bob["Authorization"]}[who]
		_, v := f.doH(t, "GET", base+"/visits/"+visit["id"].(string)+"?expand=patient", "", map[string]string{"Authorization": hdr})
		p := v["_expanded"].(map[string]any)["patient"].(map[string]any)
		if p["notes"] != wantNotes {
			t.Errorf("expanded as %s: %v", who, p)
		}
	}
	_, v := f.doH(t, "GET", base+"/visits/"+visit["id"].(string)+"?expand=patient(name,notes)", "", bob)
	if p := v["_expanded"].(map[string]any)["patient"].(map[string]any); p["notes"] != nil || fmt.Sprint(p["_meta"]) != "map[redacted:[notes]]" {
		t.Errorf("a selection says what it hides: %v", p)
	}
}

func TestAdministratorsReadEncryptedFieldsThroughTheDataRoute(t *testing.T) {
	f, realm, base, logs := revealClinic(t)
	ctx := context.Background()
	ada := signedIn(t, f, realm, "ada@example.com")
	rec, created := f.doH(t, "POST", base+"/patients", `{"name": "Ana", "notes": "n1", "history": {"visits": 1}, "address": {"street": "Main 1"}}`, ada)
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body)
	}
	id := created["id"].(string)
	f.doH(t, "POST", base+"/patients", `{"name": "Ben", "notes": "n2"}`, ada)

	_, adminKey, _ := f.users[realm].CreateAPIKey(ctx, "ops", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	boss := map[string]string{"Authorization": "Bearer " + adminKey}
	data := "/v1/" + realm + "/_admin/data/main/patients"
	// A full administrator sees plaintext (past the reveal rules), and each read is audited with fields and a count.
	rec, got := f.doH(t, "GET", data+"/"+id, "", boss)
	if rec.Code != http.StatusOK || got["notes"] != "n1" || got["address"].(map[string]any)["street"] != "Main 1" {
		t.Fatalf("admin get: %d %v", rec.Code, got)
	}
	f.doH(t, "GET", data, "", boss)
	_, trail := f.doH(t, "GET", "/v1/"+realm+"/_admin/audit?action=data.reveal", "", boss)
	items := trail["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("one record per read: %v", trail)
	}
	list := items[0].(map[string]any)["details"].(map[string]any) // newest first: the list
	if list["documents"] != float64(2) || list["fields"] != "address.street,history,notes" || list["collection"] != "patients" {
		t.Errorf("the list's record: %v", list)
	}
	if raw := fmt.Sprint(trail); strings.Contains(raw, "n1") || strings.Contains(raw, "Main 1") {
		t.Error("the audit trail holds a value")
	}

	// A read-only administrator who may read data gets every encrypted field redacted.
	auditor := signedIn(t, f, realm, "auditor@example.com")
	if err := f.users[realm].AddRole(ctx, "auditor@example.com", "auditor"); err != nil {
		t.Fatal(err)
	}
	rec, got = f.doH(t, "GET", data+"/"+id, "", auditor)
	if rec.Code != http.StatusOK || got["name"] != "Ana" || got["notes"] != nil || got["history"] != nil || got["address"].(map[string]any)["street"] != nil ||
		fmt.Sprint(got["_meta"].(map[string]any)["redacted"]) != "[address.street history notes]" {
		t.Errorf("read-only admin: %d %v", rec.Code, got)
	}
	_, trail = f.doH(t, "GET", "/v1/"+realm+"/_admin/audit?action=data.reveal", "", boss)
	if n := len(trail["items"].([]any)); n != 2 {
		t.Errorf("a redacted read was audited as a reveal: %d records", n)
	}
	if l := logs.String(); strings.Contains(l, "n1") || strings.Contains(l, "Main 1") {
		t.Error("the log holds a value")
	}
}

func TestAdministratorsMigrateAndRekeyThroughTheAPIAndTheWorker(t *testing.T) {
	f, realm, base, _, _ := clinic(t)
	ctx := context.Background()
	_, adminKey, _ := f.users[realm].CreateAPIKey(ctx, "ops", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	boss := jsonHdr(adminKey)
	admin := "/v1/" + realm + "/_admin"
	ada := signedIn(t, f, realm, "ada@example.com")

	// Data from before the fields were encrypted: plaintext, past the validator.
	coll := f.mongo.Database(realm + "__main").Collection("patients")
	for i := range 5 {
		_, err := coll.InsertOne(ctx, bson.D{
			{Key: "_id", Value: fmt.Sprintf("old%d", i)},
			{Key: "_meta", Value: bson.D{{Key: "created_at", Value: time.Now()}, {Key: "updated_at", Value: time.Now()}, {Key: "version", Value: int64(1)}}},
			{Key: "name", Value: fmt.Sprintf("Old %d", i)}, {Key: "tax_id", Value: fmt.Sprintf("B%d", i)}, {Key: "notes", Value: "old note"},
		}, options.InsertOne().SetBypassDocumentValidation(true))
		if err != nil {
			t.Fatal(err)
		}
	}
	status := func() map[string]any {
		t.Helper()
		rec, out := f.doH(t, "GET", admin+"/encryption?database=main&collection=patients", "", boss)
		if rec.Code != http.StatusOK || len(out["collections"].([]any)) != 1 {
			t.Fatalf("status: %d %s", rec.Code, rec.Body)
		}
		return out["collections"].([]any)[0].(map[string]any)
	}
	field := func(st map[string]any, path string) map[string]any {
		for _, x := range st["fields"].([]any) {
			if m := x.(map[string]any); m["path"] == path {
				return m
			}
		}
		t.Fatalf("no field %s in %v", path, st)
		return nil
	}
	st := status()
	if st["complete"] != false || field(st, "notes")["plaintext"] != float64(5) || field(st, "tax_id")["encrypted"] != float64(0) || field(st, "notes")["key"] != "HEALTH" || field(st, "notes")["fingerprint"] == "" {
		t.Errorf("before: %v", st)
	}
	// Reads work on the old data, a search doesn't see it yet.
	if rec, got := f.doH(t, "GET", base+"/old1", "", ada); rec.Code != 200 || got["notes"] != "old note" {
		t.Errorf("reading old data: %d %v", rec.Code, got)
	}
	if _, out := f.doH(t, "GET", base+`?where={"tax_id":"B1"}`, "", ada); len(out["items"].([]any)) != 0 {
		t.Error("a search found a document that has no index yet")
	}

	// Only a full administrator with proof; unknown fields refused.
	if rec, _ := f.doH(t, "POST", admin+"/encryption/migrate", `{"database":"main","collection":"nope"}`, boss); rec.Code != http.StatusNotFound {
		t.Errorf("unknown collection: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "POST", admin+"/encryption/migrate", `{"database":"main","collection":"patients","fields":["name"]}`, boss); rec.Code != http.StatusBadRequest {
		t.Errorf("a field that is not encrypted: %d", rec.Code)
	}

	// A dry run changes nothing and reports what it would.
	w := NewWorker(Config{Registry: f.reg, Store: f.backing, Users: func(r string) *auth.Users { return f.users[r] }}, "t")
	run := func(body string) map[string]any {
		t.Helper()
		rec, out := f.doH(t, "POST", admin+"/encryption/migrate", body, boss)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("migrate: %d %s", rec.Code, rec.Body)
		}
		if rec2, _ := f.doH(t, "POST", admin+"/encryption/migrate", body, boss); rec2.Code != http.StatusConflict {
			t.Errorf("a second migration of the same collection: %d", rec2.Code)
		}
		id := out["id"].(string)
		for range 20 {
			if j, _, _ := f.users[realm].GetJob(ctx, id); j.Result != nil {
				break
			}
			w.RunOnce(ctx)
		}
		job, _, _ := f.users[realm].GetJob(ctx, id)
		if job.Result == nil || job.Result.Status != "ok" {
			t.Fatalf("job: %+v", job)
		}
		return map[string]any{"job": job}
	}
	res := run(`{"database":"main","collection":"patients","dry_run":true}`)["job"].(auth.Job)
	if res.Encrypt.Scanned != 5 || res.Encrypt.Encrypted != 10 || res.Encrypt.Indexed != 5 || !res.Encrypt.DryRun {
		t.Errorf("dry run counts: %+v", res.Encrypt)
	}
	if field(status(), "notes")["plaintext"] != float64(5) {
		t.Error("a dry run changed the data")
	}

	res = run(`{"database":"main","collection":"patients"}`)["job"].(auth.Job)
	if res.Encrypt.Encrypted != 10 || res.Encrypt.Indexed != 5 || res.Encrypt.After == "" {
		t.Errorf("counts: %+v", res.Encrypt)
	}
	st = status()
	if st["complete"] != true || field(st, "notes")["current"] != float64(5) || field(st, "tax_id")["indexed"] != float64(5) || st["migration"] == nil {
		t.Errorf("after: %v", st)
	}
	if _, out := f.doH(t, "GET", base+`?where={"tax_id":"B1"}`, "", ada); len(out["items"].([]any)) != 1 {
		t.Errorf("a search after the migration: %v", out)
	}
	if audit := f.auditNames(t, boss, realm); !strings.Contains(audit, "encrypt.start") || !strings.Contains(audit, "encrypt.migrate") {
		t.Errorf("audit: %s", audit)
	}

	// Rekey: a new generation; the old values become "old" until another migration.
	if rec, out := f.doH(t, "POST", admin+"/encryption/rekey", `{"database":"main","collection":"patients","field":"notes"}`, boss); rec.Code != http.StatusOK || out["generation"] != float64(2) {
		t.Fatalf("rekey: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := f.doH(t, "POST", admin+"/encryption/rekey", `{"database":"main","collection":"patients","field":"name"}`, boss); rec.Code != http.StatusBadRequest {
		t.Errorf("rekey of a plain field: %d", rec.Code)
	}
	f.rings[realm].Reset()
	st = status()
	if n := field(st, "notes"); st["complete"] != false || n["old"] != float64(5) || n["current"] != float64(0) {
		t.Errorf("after the rekey: %v", n)
	}
	if rec, got := f.doH(t, "GET", base+"/old2", "", ada); rec.Code != 200 || got["notes"] != "old note" {
		t.Errorf("old values stay readable: %d %v", rec.Code, got)
	}
	run(`{"database":"main","collection":"patients"}`)
	st = status()
	notes := field(st, "notes")
	gens := notes["generations"].([]any)
	var statuses []string
	for _, g := range gens {
		statuses = append(statuses, g.(map[string]any)["status"].(string))
	}
	if st["complete"] != true || notes["current"] != float64(5) || strings.Join(statuses, ",") != "retired,active" {
		t.Errorf("after the second migration: %v %v", st["complete"], notes)
	}
}

func (f *fixture) auditNames(t *testing.T, hdr map[string]string, realm string) string {
	t.Helper()
	_, out := f.doH(t, "GET", "/v1/"+realm+"/_admin/audit?limit=100", "", hdr)
	var names []string
	for _, it := range out["items"].([]any) {
		names = append(names, it.(map[string]any)["action"].(string))
	}
	return strings.Join(names, " ")
}
