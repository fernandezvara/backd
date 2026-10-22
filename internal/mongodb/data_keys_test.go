package mongodb

import (
	"context"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/fieldcrypt"
)

func TestDataKeysAreStoredOncePerFieldAndGeneration(t *testing.T) {
	ctx := context.Background()
	s, _ := authFixture(t)
	at := time.Date(2126, 10, 22, 12, 0, 0, 0, time.UTC)
	rec := func(id string, gen, ver int, status string) fieldcrypt.Record {
		return fieldcrypt.Record{ID: id, Database: "app", Collection: "notes", Path: "body", Kind: fieldcrypt.KindData, Generation: gen,
			Key: "HEALTH", Wrapped: []byte("wrapped-" + id), WrapVersion: ver, Status: status, CreatedAt: at}
	}
	if ok, err := s.CreateDataKey(ctx, rec("aa", 1, 1, fieldcrypt.StatusActive)); !ok || err != nil {
		t.Fatalf("first: %v %v", ok, err)
	}
	// A second creator of the same generation loses, whatever its id.
	if ok, err := s.CreateDataKey(ctx, rec("bb", 1, 1, fieldcrypt.StatusActive)); ok || err != nil {
		t.Fatalf("racing creator: %v %v", ok, err)
	}
	got, ok, err := s.ActiveDataKey(ctx, "app", "notes", "body", fieldcrypt.KindData)
	if err != nil || !ok || got.ID != "aa" || string(got.Wrapped) != "wrapped-aa" || got.WrapVersion != 1 {
		t.Fatalf("active: %+v %v %v", got, ok, err)
	}
	if _, ok, _ := s.ActiveDataKey(ctx, "app", "notes", "other", fieldcrypt.KindData); ok {
		t.Error("another field has a key")
	}
	// The newest active generation wins; a retired one never does.
	if ok, _ := s.CreateDataKey(ctx, rec("cc", 2, 1, fieldcrypt.StatusActive)); !ok {
		t.Fatal("generation 2")
	}
	if ok, _ := s.CreateDataKey(ctx, rec("dd", 3, 1, fieldcrypt.StatusRetired)); !ok {
		t.Fatal("generation 3")
	}
	if got, _, _ := s.ActiveDataKey(ctx, "app", "notes", "body", fieldcrypt.KindData); got.ID != "cc" {
		t.Errorf("active = %s", got.ID)
	}
	if byID, ok, _ := s.DataKeyByID(ctx, "dd"); !ok || byID.Status != fieldcrypt.StatusRetired {
		t.Errorf("by id: %+v", byID)
	}

	// Rewrapping moves keys from one version to the next, in id order, once.
	todo, err := s.DataKeysToRewrap(ctx, "HEALTH", 2, "", 2)
	if err != nil || len(todo) != 2 || todo[0].ID != "aa" || todo[1].ID != "cc" {
		t.Fatalf("to rewrap: %+v %v", todo, err)
	}
	if rest, _ := s.DataKeysToRewrap(ctx, "HEALTH", 2, "cc", 10); len(rest) != 1 || rest[0].ID != "dd" {
		t.Errorf("after cc: %+v", rest)
	}
	if ok, err := s.RewrapDataKey(ctx, "aa", []byte("new"), 1, 2); !ok || err != nil {
		t.Fatalf("rewrap: %v %v", ok, err)
	}
	if ok, _ := s.RewrapDataKey(ctx, "aa", []byte("again"), 1, 2); ok {
		t.Error("a key already on version 2 was rewrapped from 1")
	}
	if back, _, _ := s.DataKeyByID(ctx, "aa"); string(back.Wrapped) != "new" || back.WrapVersion != 2 {
		t.Errorf("after: %+v", back)
	}
	if n, _ := s.DataKeysByKey(ctx, "HEALTH"); n != 3 {
		t.Errorf("count = %d", n)
	}
	if n, _ := s.DataKeysByKey(ctx, "OTHER"); n != 0 {
		t.Errorf("other count = %d", n)
	}
}

func TestTheValidatorExpectsBinaryWhereAFieldIsEncrypted(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"name":    map[string]any{"type": "string"},
		"tax_id":  map[string]any{"type": "string", "pattern": "^B"},
		"address": map[string]any{"type": "object", "properties": map[string]any{"street": map[string]any{"type": "string"}, "city": map[string]any{"type": "string"}}},
	}}
	out := EncryptedSchema(schema, []string{"tax_id", "address.street"})
	props := out["properties"].(map[string]any)
	if props["tax_id"].(map[string]any)[encryptedMarker] != true || props["tax_id"].(map[string]any)["pattern"] != nil {
		t.Errorf("tax_id = %v", props["tax_id"])
	}
	addr := props["address"].(map[string]any)["properties"].(map[string]any)
	if addr["street"].(map[string]any)[encryptedMarker] != true || addr["city"].(map[string]any)["type"] != "string" {
		t.Errorf("address = %v", addr)
	}
	// The original is untouched.
	if schema["properties"].(map[string]any)["tax_id"].(map[string]any)["type"] != "string" {
		t.Error("EncryptedSchema modified its input")
	}
	v, _ := Validator(out)
	js := v["$jsonSchema"].(map[string]any)["properties"].(map[string]any)
	if bt := js["tax_id"].(map[string]any)["bsonType"]; bt != "binData" {
		t.Errorf("validator tax_id = %v", bt)
	}
}
