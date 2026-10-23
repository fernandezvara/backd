package exportpkg

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/fieldcrypt"
)

func TestDocumentRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 9, 12, 30, 45, 123000000, time.UTC)
	doc := map[string]any{
		"id":    "a1",
		"_meta": map[string]any{"version": int64(3), "created_at": at, "owner": nil},
		"n":     int64(7), "f": 1.5, "ok": true, "text": "línea\nnueva \"x\"",
		"nested": map[string]any{"when": at, "list": []any{int64(1), "b", map[string]any{"d": at}}},
		"cipher": []byte{0, 1, 2, 250},
	}
	line, err := EncodeDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.ContainsRune(line, '\n') {
		t.Fatal("a document must be one line")
	}
	if !strings.Contains(string(line), `"$date":"2026-10-09T12:30:45.123Z"`) || !strings.Contains(string(line), `"$binary":{"base64":"AAEC+g==","subType":"00"}`) {
		t.Errorf("not Extended JSON: %s", line)
	}
	got, err := DecodeDocument(line)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, doc) {
		t.Errorf("round trip:\n got %#v\nwant %#v", got, doc)
	}
}

func TestDecodeRejects(t *testing.T) {
	for _, bad := range []string{`{"a":`, `{"a":1} {"b":2}`, `{"d":{"$date":"yesterday"}}`, `{"d":{"$binary":{"base64":"!!","subType":"00"}}}`} {
		if _, err := DecodeDocument([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestReadDocuments(t *testing.T) {
	in := "{\"id\":\"a\"}\n\n{\"id\":\"b\"}\n{\"id\":\"c\"}"
	var ids []string
	if err := ReadDocuments(strings.NewReader(in), func(_ int, d map[string]any) error {
		ids = append(ids, d["id"].(string))
		return nil
	}); err != nil || strings.Join(ids, "") != "abc" {
		t.Fatalf("%v %v", ids, err)
	}
	err := ReadDocuments(strings.NewReader("{\"id\":\"a\"}\nnope\n"), func(int, map[string]any) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("a broken line: %v", err)
	}
}

func TestManifest(t *testing.T) {
	m := &Manifest{Format: FormatVersion, Realm: "r", Databases: []Database{{Name: "b"}, {Name: "a", Collections: []Collection{{Name: "z"}, {Name: "y"}}}}}
	data, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseManifest(data)
	if err != nil || back.Databases[0].Name != "a" || back.Databases[0].Collections[0].Name != "y" || back.Encrypted == nil || back.Checksums == nil {
		t.Fatalf("%+v %v", back, err)
	}
	if _, err := ParseManifest([]byte(`{"format": 99}`)); err == nil || !strings.Contains(err.Error(), "format 99") {
		t.Errorf("another format: %v", err)
	}
}

func TestHasher(t *testing.T) {
	h := NewHasher()
	h.Write([]byte("abc"))
	if h.Sum() != "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" || h.Size() != 3 {
		t.Errorf("%s %d", h.Sum(), h.Size())
	}
}

func TestSafePart(t *testing.T) {
	for _, ok := range []string{"manifest.json", "main/patients.jsonl", "main/_files/abc", "_system/data_keys.jsonl"} {
		if !SafePart(ok) {
			t.Errorf("refused %q", ok)
		}
	}
	for _, bad := range []string{"", "/etc/passwd", "../x", "a/../b", "a//b", `a\b`, "a/./b", "a\x00b"} {
		if SafePart(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestSelect(t *testing.T) {
	doc := map[string]any{"id": "a", "name": "n", "secret": "s", "address": map[string]any{"city": "c", "zip": "z"}, "_meta": map[string]any{"owner": "u", "version": int64(2)}}
	got := Select(doc, []string{"name", "address.city", "_meta.owner", "ghost", "address.ghost.deeper"})
	want := map[string]any{"id": "a", "name": "n", "address": map[string]any{"city": "c"}, "_meta": map[string]any{"owner": "u"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
	if whole := Select(doc, []string{"address"}); len(whole["address"].(map[string]any)) != 2 || whole["_meta"] != nil {
		t.Errorf("an object keeps all of it: %v", whole)
	}
}

func TestEncryptedValuesTravelAsBinaryOfSubtypeSix(t *testing.T) {
	ct := fieldcrypt.Ciphertext{1, 2, 3, 4, 5}
	line, err := EncodeDocument(map[string]any{"id": "a", "notes": ct, "raw": []byte{9, 8}, "n": map[string]any{"deep": ct}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(line), `"subType":"06"`) || !strings.Contains(string(line), `"subType":"00"`) {
		t.Fatalf("line = %s", line)
	}
	back, err := DecodeDocument(line)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := back["notes"].(fieldcrypt.Ciphertext); !ok || !bytes.Equal(got, ct) {
		t.Errorf("notes = %T %v", back["notes"], back["notes"])
	}
	if got, ok := back["n"].(map[string]any)["deep"].(fieldcrypt.Ciphertext); !ok || !bytes.Equal(got, ct) {
		t.Errorf("deep = %T", back["n"].(map[string]any)["deep"])
	}
	if got, ok := back["raw"].([]byte); !ok || !bytes.Equal(got, []byte{9, 8}) {
		t.Errorf("raw = %T", back["raw"])
	}
	// A CSV extract shows an unopened value as base64 text, like any binary.
	var out bytes.Buffer
	if _, err := WriteCSV(strings.NewReader(string(line)+"\n"), &out, nil); err != nil || !strings.Contains(out.String(), "AQIDBAU=") {
		t.Errorf("csv: %q %v", out.String(), err)
	}
}
