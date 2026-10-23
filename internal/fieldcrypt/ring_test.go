package fieldcrypt

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/registry"
)

func testRing(t *testing.T) (*Ring, *registry.Collection, *MemStore) {
	t.Helper()
	store := NewMemStore()
	keys := map[string][]byte{"HEALTH": bytes.Repeat([]byte{7}, 32), "TAX": bytes.Repeat([]byte{9}, 32)}
	ring := &Ring{Realm: "clinic", Store: store, Keys: func(_ context.Context, name string) (Material, error) {
		k, ok := keys[name]
		if !ok {
			return Material{}, ErrUnavailable
		}
		return Material{Current: k, Version: 1}, nil
	}}
	c := &registry.Collection{Realm: "clinic", Database: "main", Name: "patients", Encrypted: []registry.EncryptedField{
		{Path: "address.street", Key: "HEALTH"}, {Path: "notes", Key: "HEALTH"}, {Path: "tax_id", Key: "TAX"},
	}}
	if err := ring.Ensure(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return ring, c, store
}

func TestValuesAreSealedAndOpenedWithAnyJSONType(t *testing.T) {
	ctx := context.Background()
	ring, c, _ := testRing(t)
	doc := map[string]any{
		"id": "p1", "name": "Ada", "notes": map[string]any{"allergies": []any{"nuts", int64(3)}, "ok": true},
		"tax_id": int64(12345678), "address": map[string]any{"street": "Main 1", "city": "Madrid"},
	}
	sealed, err := ring.Encrypt(ctx, c, doc)
	if err != nil {
		t.Fatal(err)
	}
	// The caller's document is untouched; the copy holds binary where the fields were.
	if doc["tax_id"] != int64(12345678) || doc["address"].(map[string]any)["street"] != "Main 1" {
		t.Fatalf("the original changed: %v", doc)
	}
	for _, p := range []string{"notes", "tax_id"} {
		if _, ok := sealed[p].(Ciphertext); !ok {
			t.Errorf("%s is %T", p, sealed[p])
		}
	}
	addr := sealed["address"].(map[string]any)
	if _, ok := addr["street"].(Ciphertext); !ok || addr["city"] != "Madrid" || sealed["name"] != "Ada" {
		t.Errorf("sealed = %v", sealed)
	}
	if len(sealed["tax_id"].(Ciphertext)) > Overhead+len("12345678")+1 {
		t.Errorf("overhead too large: %d", len(sealed["tax_id"].(Ciphertext)))
	}

	if err := ring.Decrypt(ctx, c, sealed); err != nil {
		t.Fatal(err)
	}
	if sealed["tax_id"] != int64(12345678) || sealed["address"].(map[string]any)["street"] != "Main 1" {
		t.Errorf("opened = %v", sealed)
	}
	n := sealed["notes"].(map[string]any)
	if n["ok"] != true || n["allergies"].([]any)[1] != int64(3) {
		t.Errorf("notes = %v", n)
	}
}

func TestAbsentFieldsStayAbsentAndNullIsSealed(t *testing.T) {
	ctx := context.Background()
	ring, c, _ := testRing(t)
	sealed, _ := ring.Encrypt(ctx, c, map[string]any{"id": "p1", "notes": nil})
	if _, has := sealed["tax_id"]; has {
		t.Error("an absent field appeared")
	}
	if _, ok := sealed["notes"].(Ciphertext); !ok {
		t.Errorf("null is %T", sealed["notes"])
	}
	if err := ring.Decrypt(ctx, c, sealed); err != nil || sealed["notes"] != nil {
		t.Errorf("null back: %v %v", sealed["notes"], err)
	}
}

func TestAValueCopiedElsewhereFailsToDecrypt(t *testing.T) {
	ctx := context.Background()
	ring, c, _ := testRing(t)
	a, _ := ring.Encrypt(ctx, c, map[string]any{"id": "p1", "tax_id": "B1", "notes": "n"})
	// Into another document.
	other := map[string]any{"id": "p2", "tax_id": a["tax_id"]}
	if err := ring.Decrypt(ctx, c, other); !errors.Is(err, ErrCorrupt) {
		t.Errorf("another document: %v", err)
	}
	// Into another field of the same document (same key, different path).
	swapped := map[string]any{"id": "p1", "notes": a["tax_id"]}
	if err := ring.Decrypt(ctx, c, swapped); !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrUnavailable) {
		t.Errorf("another field: %v", err)
	}
	// Altered bytes.
	ct := append(Ciphertext(nil), a["notes"].(Ciphertext)...)
	ct[len(ct)-1] ^= 1
	if err := ring.Decrypt(ctx, c, map[string]any{"id": "p1", "notes": ct}); !errors.Is(err, ErrCorrupt) {
		t.Errorf("altered: %v", err)
	}
	// Truncated or of another version.
	for _, bad := range []Ciphertext{{1, 2, 3}, append(Ciphertext{9}, make([]byte, 60)...)} {
		if err := ring.Decrypt(ctx, c, map[string]any{"id": "p1", "notes": bad}); !errors.Is(err, ErrCorrupt) {
			t.Errorf("malformed: %v", err)
		}
	}
}

func TestPlaintextLeftFromBeforeIsRead(t *testing.T) {
	ctx := context.Background()
	ring, c, _ := testRing(t)
	doc := map[string]any{"id": "p1", "notes": "written before encryption"}
	if err := ring.Decrypt(ctx, c, doc); err != nil || doc["notes"] != "written before encryption" {
		t.Errorf("%v %v", doc, err)
	}
}

func TestErrorsNeverHoldValues(t *testing.T) {
	ctx := context.Background()
	ring, c, store := testRing(t)
	sealed, _ := ring.Encrypt(ctx, c, map[string]any{"id": "p1", "notes": "SECRET-VALUE-1"})
	// A key that can't be used: the error names the field, not the value.
	store.Keys = map[string]Record{}
	ring2 := &Ring{Realm: "clinic", Store: store, Keys: ring.Keys}
	err := ring2.Decrypt(ctx, c, sealed)
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "SECRET-VALUE-1") {
		t.Errorf("err = %v", err)
	}
	if _, err := ring2.Encrypt(ctx, c, map[string]any{"id": "p1", "notes": "SECRET-VALUE-1"}); !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "SECRET-VALUE-1") {
		t.Errorf("encrypt err = %v", err)
	}
}

func TestEnsureCreatesOneKeyPerFieldAndIsRepeatable(t *testing.T) {
	ctx := context.Background()
	ring, c, store := testRing(t)
	if len(store.Keys) != 3 {
		t.Fatalf("keys = %d", len(store.Keys))
	}
	if err := ring.Ensure(ctx, c); err != nil || len(store.Keys) != 3 {
		t.Errorf("again: %v %d", err, len(store.Keys))
	}
	// Fields never share data keys, even when they share a field key.
	seen := map[string]bool{}
	for _, r := range store.Keys {
		if seen[r.ID] {
			t.Error("a shared id")
		}
		seen[r.ID] = true
	}
	// A missing field key fails the field, naming it.
	c2 := &registry.Collection{Realm: "clinic", Database: "main", Name: "other", Encrypted: []registry.EncryptedField{{Path: "x", Key: "NOPE"}}}
	if err := ring.Ensure(ctx, c2); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "other.x") {
		t.Errorf("missing key: %v", err)
	}
}

func TestWrapIsBoundToItsFieldAndId(t *testing.T) {
	fk := bytes.Repeat([]byte{1}, 32)
	id, dk, _ := NewDataKey()
	f := Field{Realm: "r", Database: "d", Collection: "c", Path: "p"}
	w, err := Wrap(fk, dk, id, f, KindData)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Unwrap(fk, w, id, f, KindData); err != nil || !bytes.Equal(got, dk) {
		t.Fatalf("unwrap: %v", err)
	}
	other := f
	other.Path = "q"
	if _, err := Unwrap(fk, w, id, other, KindData); !errors.Is(err, ErrUnavailable) {
		t.Errorf("another field: %v", err)
	}
	if _, err := Unwrap(bytes.Repeat([]byte{2}, 32), w, id, f, KindData); !errors.Is(err, ErrUnavailable) {
		t.Errorf("another key: %v", err)
	}
}

func TestBlindIndexesAreEqualForEqualValuesAndNothingElse(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	key := bytes.Repeat([]byte{3}, 32)
	ring := &Ring{Realm: "clinic", Store: store, Keys: func(context.Context, string) (Material, error) { return Material{Current: key, Version: 1}, nil }}
	c := &registry.Collection{Realm: "clinic", Database: "main", Name: "patients", Encrypted: []registry.EncryptedField{
		{Path: "tax_id", Key: "TAX", Search: registry.SearchEquality, Normalize: []string{"trim", "upper"}},
		{Path: "other", Key: "TAX", Search: registry.SearchEquality},
		{Path: "notes", Key: "TAX"},
	}}
	if err := ring.Ensure(ctx, c); err != nil {
		t.Fatal(err)
	}
	// Two keys per searchable field (data and index), one for the rest; none shared.
	if len(store.Keys) != 5 {
		t.Fatalf("keys = %d", len(store.Keys))
	}
	blind := func(path string, v any) string {
		b, err := ring.Blind(ctx, c, path, v)
		if err != nil || len(b) != 32 {
			t.Fatalf("blind(%s, %v): %v", path, v, err)
		}
		return string(b)
	}
	if blind("tax_id", " b1234 ") != blind("tax_id", "B1234") {
		t.Error("normalization didn't equate the values")
	}
	if blind("tax_id", "B1234") == blind("tax_id", "B1235") {
		t.Error("different values collide")
	}
	if blind("tax_id", "B1") == blind("other", "B1") {
		t.Error("the same value in two fields has one index: it would link them")
	}
	if blind("other", "b1") == blind("other", "B1") {
		t.Error("a field without normalization folded case")
	}
	if blind("other", int64(5)) != blind("other", float64(5)) || blind("other", int64(5)) == blind("other", "5") {
		t.Error("numbers and text")
	}
	// The sealed document carries the index under the flat name, and plaintext values stay out of it.
	sealed, err := ring.Encrypt(ctx, c, map[string]any{"id": "p1", "tax_id": "b1", "notes": "n"})
	if err != nil {
		t.Fatal(err)
	}
	idx := sealed[registry.BlindRoot].(map[string]any)
	if string(idx["tax_id"].([]byte)) != blind("tax_id", "B1") || len(idx) != 1 {
		t.Errorf("index = %v", idx)
	}
	// A client's own _bidx is dropped.
	sealed, _ = ring.Encrypt(ctx, c, map[string]any{"id": "p1", registry.BlindRoot: map[string]any{"other": []byte("forged")}})
	if _, has := sealed[registry.BlindRoot]; has {
		t.Errorf("a forged index survived: %v", sealed)
	}
	if _, err := ring.Blind(ctx, c, "notes", "n"); err == nil {
		t.Error("a field without search has a blind index")
	}
}

func TestAFieldMovedToAnotherKeyStartsANewGenerationAndOldValuesStayReadable(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	keys := map[string][]byte{"OLD": bytes.Repeat([]byte{1}, 32), "NEW": bytes.Repeat([]byte{2}, 32)}
	ring := &Ring{Realm: "clinic", Store: store, Keys: func(_ context.Context, n string) (Material, error) {
		return Material{Current: keys[n], Version: 1}, nil
	}}
	c := &registry.Collection{Realm: "clinic", Database: "main", Name: "patients", Encrypted: []registry.EncryptedField{{Path: "notes", Key: "OLD"}}}
	if err := ring.Ensure(ctx, c); err != nil {
		t.Fatal(err)
	}
	old, _ := ring.Encrypt(ctx, c, map[string]any{"id": "p1", "notes": "n"})

	c.Encrypted[0].Key = "NEW" // the configuration points the field at another key
	if err := ring.Ensure(ctx, c); err != nil {
		t.Fatal(err)
	}
	recs, _ := store.DataKeysOf(ctx, "main", "patients", "notes")
	if len(recs) != 2 || recs[0].Key != "OLD" || recs[0].Status != StatusDecryptOnly || recs[1].Key != "NEW" || recs[1].Status != StatusActive || recs[1].Generation != 2 {
		t.Fatalf("keys: %+v", recs)
	}
	// Ensure again changes nothing.
	if err := ring.Ensure(ctx, c); err != nil {
		t.Fatal(err)
	}
	if recs, _ = store.DataKeysOf(ctx, "main", "patients", "notes"); len(recs) != 2 {
		t.Errorf("a second Ensure made keys: %d", len(recs))
	}
	// New values use the new generation; the old one is still read, as long as OLD exists.
	fresh, _ := ring.Encrypt(ctx, c, map[string]any{"id": "p2", "notes": "m"})
	if id1, _ := ParseID(old["notes"].(Ciphertext)); hexOf(id1) == recs[1].ID {
		t.Error("the old value was sealed with the new key")
	}
	if id2, _ := ParseID(fresh["notes"].(Ciphertext)); hexOf(id2) != recs[1].ID {
		t.Error("a new value was not sealed with the new key")
	}
	if err := ring.Decrypt(ctx, c, old); err != nil || old["notes"] != "n" {
		t.Errorf("old value: %v %v", old["notes"], err)
	}
	// Deleting OLD is refused by its data keys until they retire.
	if n, _ := store.DataKeysByKey(ctx, "OLD"); n != 1 {
		t.Errorf("data keys of OLD = %d", n)
	}
	retired, err := ring.Retire(ctx, c, "notes", func(string) (int64, error) { return 0, nil })
	if err != nil || len(retired) != 1 {
		t.Fatalf("retire: %v %v", retired, err)
	}
	if n, _ := store.DataKeysByKey(ctx, "OLD"); n != 0 {
		t.Errorf("a retired key still counts: %d", n)
	}
	// The active key of a field that is no longer encrypted retires once nothing uses it.
	c.Encrypted = nil
	ring.Reset()
	if paths, _ := ring.HistoricPaths(ctx, c); len(paths) != 1 || paths[0] != "notes" {
		t.Errorf("historic paths = %v", paths)
	}
	if retired, _ = ring.Retire(ctx, c, "notes", func(string) (int64, error) { return 0, nil }); len(retired) != 1 {
		t.Errorf("retire after removal: %v", retired)
	}
}

func hexOf(b []byte) string { return hex.EncodeToString(b) }

func TestKeyRecordsTravelAsLines(t *testing.T) {
	rec := Record{ID: "aabbccdd00112233aabbccdd00112233", Database: "main", Collection: "patients", Path: "address.street", Kind: KindIndex,
		Generation: 2, Key: "HEALTH", Wrapped: []byte{1, 2, 3, 4}, WrapVersion: 3, Status: StatusDecryptOnly}
	line, err := EncodeKeyLine(rec)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeKeyLine(line)
	if err != nil || got.ID != rec.ID || got.Path != rec.Path || got.Kind != rec.Kind || got.Generation != 2 || got.Key != "HEALTH" || !bytes.Equal(got.Wrapped, rec.Wrapped) || got.WrapVersion != 3 || got.Status != StatusDecryptOnly {
		t.Errorf("round trip: %+v %v", got, err)
	}
	for _, bad := range []string{`{}`, `not json`, `{"id":"x","path":"p","wrapped":"!!"}`} {
		if _, err := DecodeKeyLine([]byte(bad)); err == nil {
			t.Errorf("%s decoded", bad)
		}
	}
}
