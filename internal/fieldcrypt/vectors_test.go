package fieldcrypt

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The test vectors of the format (docs: Operate → Export and import → Encrypted fields format):
// fixed inputs, and the bytes each step of the format produces for them. Other implementations
// check themselves against them; this test checks that backd still does, and that the page that
// publishes them says the same.

var update = flag.Bool("update-vectors", false, "rewrite testdata/vectors.json")

type vectors struct {
	Realm, Database, Collection, Path, Document string
	FieldKey                                    string `json:"field_key_hex"`
	DataKey                                     string `json:"data_key_hex"`
	DataKeyID                                   string `json:"data_key_id_hex"`
	Nonce                                       string `json:"nonce_hex"`
	WrapNonce                                   string `json:"wrap_nonce_hex"`
	ValueJSON                                   string `json:"value_json"`
	ValueAD                                     string `json:"value_associated_data_hex"`
	Ciphertext                                  string `json:"ciphertext_hex"`
	WrapAD                                      string `json:"wrap_associated_data_hex"`
	Wrapped                                     string `json:"wrapped_hex"`
	Fingerprint                                 string `json:"field_key_fingerprint"`
	KeyBase64URL                                string `json:"field_key_base64url"`
	IndexKey                                    string `json:"index_key_hex"`
	Normalize                                   []string
	BlindInput                                  string `json:"blind_value_json"`
	BlindMessage                                string `json:"blind_message_hex"`
	Blind                                       string `json:"blind_index_hex"`
	Normalized                                  string `json:"normalized_value"`
	RawValue                                    string `json:"blind_value_before_normalizing"`
}

func seq(start, n byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = start + byte(i)
	}
	return b
}

func compute(t *testing.T) vectors {
	t.Helper()
	v := vectors{Realm: "clinic", Database: "main", Collection: "patients", Path: "notes", Document: "p1"}
	fieldKey, dataKey, id := seq(0x00, 32), seq(0x20, 32), seq(0xa0, 16)
	nonce, wrapNonce := seq(0x40, 12), seq(0x60, 12)
	indexKey := seq(0x80, 32)
	f := Field{Realm: v.Realm, Database: v.Database, Collection: v.Collection, Path: v.Path}
	v.FieldKey, v.DataKey, v.DataKeyID = hex.EncodeToString(fieldKey), hex.EncodeToString(dataKey), hex.EncodeToString(id)
	v.Nonce, v.WrapNonce = hex.EncodeToString(nonce), hex.EncodeToString(wrapNonce)
	v.KeyBase64URL = base64.RawURLEncoding.EncodeToString(fieldKey)

	// The value: canonical JSON, AES-256-GCM, bound to realm, database, collection, path and document.
	v.ValueJSON = `"allergic to nuts"`
	ad := valueAD(f, v.Document)
	v.ValueAD = hex.EncodeToString(ad)
	g, err := gcm(dataKey)
	if err != nil {
		t.Fatal(err)
	}
	v.Ciphertext = hex.EncodeToString(sealWithNonce(g, id, nonce, []byte(v.ValueJSON), ad))

	// The wrapped data key: nonce followed by the AES-256-GCM of the data key under the field key.
	wad := wrapAD(f, KindData, id)
	v.WrapAD = hex.EncodeToString(wad)
	wg, err := gcm(fieldKey)
	if err != nil {
		t.Fatal(err)
	}
	v.Wrapped = hex.EncodeToString(wg.Seal(append([]byte{}, wrapNonce...), wrapNonce, dataKey, wad))

	// The fingerprint of the field key, as `backd secret create` prints it.
	h := sha256.Sum256(append([]byte("backd-field-key-v1|"), fieldKey...))
	v.Fingerprint = hex.EncodeToString(h[:])[:8]

	// A blind index: HMAC-SHA-256 under the index key of the field's path and the canonical JSON of
	// the normalized value.
	v.IndexKey, v.Normalize, v.RawValue = hex.EncodeToString(indexKey), []string{"trim", "upper"}, "  b1234 "
	v.Normalized = Normalize(v.Normalize, v.RawValue).(string)
	plain, _ := CanonicalJSON(v.Normalized)
	v.BlindInput = string(plain)
	msg := append(ad2("backd-field-blind-v1", "tax_id"), plain...)
	v.BlindMessage = hex.EncodeToString(msg)
	mac := hmac.New(sha256.New, indexKey)
	mac.Write(msg)
	v.Blind = hex.EncodeToString(mac.Sum(nil))
	return v
}

func ad2(parts ...string) []byte { return ad(parts...) }

func TestTheFormatMatchesItsPublishedVectors(t *testing.T) {
	got := compute(t)
	path := filepath.Join("testdata", "vectors.json")
	if *update {
		data, _ := json.MarshalIndent(got, "", "  ")
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var want vectors
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		t.Fatalf("the implementation no longer produces the published vectors (a format change needs a new version):\n got %s\nwant %s", gj, wj)
	}

	// The vectors open with the public functions an implementation would use.
	fieldKey, _ := hex.DecodeString(got.FieldKey)
	ct, _ := hex.DecodeString(got.Ciphertext)
	wrapped, _ := hex.DecodeString(got.Wrapped)
	rec := Record{ID: got.DataKeyID, Database: got.Database, Collection: got.Collection, Path: got.Path, Kind: KindData, Wrapped: wrapped}
	val, err := OpenWith(fieldKey, rec, got.Realm, Ciphertext(ct), got.Document)
	if err != nil || val != "allergic to nuts" {
		t.Errorf("OpenWith: %v %v", val, err)
	}
	// Moved to another document, another field or another realm, the value stops opening.
	for name, f := range map[string]func() error{
		"document": func() error { _, err := OpenWith(fieldKey, rec, got.Realm, Ciphertext(ct), "p2"); return err },
		"realm":    func() error { _, err := OpenWith(fieldKey, rec, "other", Ciphertext(ct), got.Document); return err },
		"path": func() error {
			r := rec
			r.Path = "history"
			_, err := OpenWith(fieldKey, r, got.Realm, Ciphertext(ct), got.Document)
			return err
		},
	} {
		if f() == nil {
			t.Errorf("a value moved to another %s still opens", name)
		}
	}

	// The page that publishes the vectors says the same.
	page, err := os.ReadFile(filepath.Join("..", "..", "docs", "content", "docs", "operate", "administration", "export-import", "encrypted-format.md"))
	if err != nil {
		t.Fatalf("the format page: %v", err)
	}
	for _, s := range []string{got.Ciphertext, got.Wrapped, got.ValueAD, got.WrapAD, got.Blind, got.BlindMessage, got.Fingerprint, got.KeyBase64URL, got.Nonce, got.DataKeyID} {
		if !strings.Contains(string(page), s) {
			t.Errorf("the format page lacks the vector %s", s)
		}
	}
}
