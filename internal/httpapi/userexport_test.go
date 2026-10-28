package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/exportpkg"
	"github.com/fernandezvara/backd/internal/storage"
)

// fakeObjects serves the files of a test from memory.
type fakeObjects map[string][]byte

func (o fakeObjects) Get(_ context.Context, key, _ string) (io.ReadCloser, storage.ObjectInfo, error) {
	b, ok := o[key]
	if !ok {
		return nil, storage.ObjectInfo{}, storage.ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), storage.ObjectInfo{Size: int64(len(b))}, nil
}

// seedDoc stores a document of the app database as another writer would have.
func seedDoc(t *testing.T, f *rulesFixture, collection, id, owner string, fields map[string]any) {
	t.Helper()
	c, ok := f.reg.Collection("acme", "app", collection)
	if !ok {
		t.Fatalf("no %s collection", collection)
	}
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	meta := map[string]any{"created_at": now, "updated_at": now, "version": int64(1)}
	if owner != "" {
		meta["owner"] = owner
	}
	doc := map[string]any{"id": id, "_meta": meta}
	maps.Copy(doc, fields)
	if err := f.store.Repository(c).Create(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
}

type builtExport struct {
	files    map[string][]byte
	names    []string
	manifest *exportpkg.Manifest
	report   *personalExportReport
}

func buildExport(t *testing.T, f *rulesFixture, userID string, objects packageObjects, steps *[]string) builtExport {
	t.Helper()
	d := &documents{reg: f.reg, store: f.store, now: func() time.Time { return time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC) }}
	var buf bytes.Buffer
	report, err := d.buildPersonalExport(context.Background(), personalExportParams{
		Realm: "acme", Svc: f.svc, UserID: userID, Operator: "user:" + userID, Retention: 7 * 24 * time.Hour,
		Objects: objects, Version: "test", Fingerprint: "fp",
		Progress: func(s string) {
			if steps != nil {
				*steps = append(*steps, s)
			}
		},
	}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	out := builtExport{files: map[string][]byte{}, report: report}
	for _, zf := range zr.File {
		rc, _ := zf.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		out.files[zf.Name] = b
		out.names = append(out.names, zf.Name)
	}
	if out.manifest, err = exportpkg.ParseManifest(out.files[exportpkg.ManifestName]); err != nil {
		t.Fatal(err)
	}
	if report.Bytes != int64(buf.Len()) {
		t.Errorf("reported %d bytes, wrote %d", report.Bytes, buf.Len())
	}
	return out
}

func (b builtExport) lines(t *testing.T, name string) []map[string]any {
	t.Helper()
	raw, ok := b.files[name]
	if !ok {
		t.Fatalf("%s is not in the archive (have %v)", name, b.names)
	}
	var docs []map[string]any
	if err := exportpkg.ReadDocuments(bytes.NewReader(raw), func(_ int, doc map[string]any) error {
		docs = append(docs, doc)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return docs
}

func TestPersonalExportHoldsWhatTheErasureModelFinds(t *testing.T) {
	f := newRulesFixture(t)
	// Her own documents, a colleague's that names her by email, and one that doesn't.
	seedOrder(t, f, "o1", f.adaID, map[string]any{"buyer": "Ada Lovelace", "phone": "555"})
	seedOrder(t, f, "o2", f.adaID, map[string]any{"buyer": "Ada Lovelace"})
	seedOrder(t, f, "o3", f.bobID, map[string]any{"buyer": "Bob", "members": []any{"ada@example.com", "bob@example.com"}})
	seedOrder(t, f, "o4", f.bobID, map[string]any{"buyer": "Bob", "members": []any{"bob@example.com"}})
	// ...and one that names her in a field the erase clears (`unset`), written by someone else.
	seedOrder(t, f, "o5", f.bobID, map[string]any{"buyer": "Bob", "paid_by": f.adaID})
	// Only some fields of a collection are hers; another is exempt altogether.
	seedDoc(t, f, "contacts", "k1", f.adaID, map[string]any{"title": "Ada's card", "body": "likes tea", "internal_note": "NOTE-FOR-STAFF-ONLY"})
	seedDoc(t, f, "private", "p1", f.adaID, map[string]any{"title": "FRAUD-SCORE-0.9"})
	// A document with a file, and one whose file is gone from the storage.
	seedDoc(t, f, "library", "l1", f.adaID, map[string]any{"title": "Holiday", "avatar": map[string]any{"id": "file-present", "name": "me.png", "size": int64(8)}})
	seedDoc(t, f, "library", "l2", f.adaID, map[string]any{"title": "Lost", "avatar": map[string]any{"id": "file-gone", "name": "gone.png", "size": int64(3)}})
	seedDoc(t, f, "library", "l3", f.bobID, map[string]any{"title": "Bob's", "avatar": map[string]any{"id": "file-bobs", "name": "bob.png", "size": int64(3)}})
	d := &documents{reg: f.reg}
	lib, _ := f.reg.Collection("acme", "app", "library")
	objects := fakeObjects{
		d.fileKey(lib, fileLoc{id: "file-present"}): []byte("PNGBYTES"),
		d.fileKey(lib, fileLoc{id: "file-bobs"}):    []byte("BOB"),
	}

	var steps []string
	pkg := buildExport(t, f, f.adaID, objects, &steps)

	// Her documents, and the colleague's that names her; never the one that doesn't.
	orders := pkg.lines(t, "app/orders.jsonl")
	var ids []string
	for _, o := range orders {
		ids = append(ids, o["id"].(string))
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"o1", "o2", "o3", "o5"}) {
		t.Errorf("orders = %v", ids)
	}
	// The scope is hers, not what a rule lets her read: the phone is a field of her order.
	if o1 := orders[slices.IndexFunc(orders, func(o map[string]any) bool { return o["id"] == "o1" })]; o1["phone"] != "555" {
		t.Errorf("o1 = %v", o1)
	}

	// Only the fields personal_data lists, plus id and _meta; nothing of the staff note.
	contacts := pkg.lines(t, "app/contacts.jsonl")
	if len(contacts) != 1 || contacts[0]["title"] != "Ada's card" || contacts[0]["body"] != "likes tea" || contacts[0]["_meta"] == nil {
		t.Errorf("contacts = %v", contacts)
	}
	if _, has := contacts[0]["internal_note"]; has {
		t.Error("a field outside personal_data.fields was exported")
	}
	if _, has := pkg.files["app/private.jsonl"]; has {
		t.Error("an excluded collection was exported")
	}

	// Her file, and only hers; the missing one is told.
	if got := string(pkg.files["app/_files/file-present"]); got != "PNGBYTES" {
		t.Errorf("file = %q", got)
	}
	if _, has := pkg.files["app/_files/file-bobs"]; has {
		t.Error("another user's file was exported")
	}
	if pkg.report.Files != 1 {
		t.Errorf("files = %d", pkg.report.Files)
	}
	var excluded []string
	for _, x := range pkg.manifest.Exclusions {
		excluded = append(excluded, x.What)
		if x.Reason == "" {
			t.Errorf("an exclusion without a reason: %+v", x)
		}
	}
	slices.Sort(excluded)
	want := []string{"app/_files/file-gone", "app/contacts (only the fields body, title)", "app/private"}
	if !slices.Equal(excluded, want) {
		t.Errorf("exclusions = %v, want %v", excluded, want)
	}

	// The system data, one part each, with her account first among them.
	for _, name := range []string{"account", "identities", "sessions", "devices", "mfa", "organizations", "invitations", "consents", "audit", "invocations"} {
		if _, ok := pkg.files["_account/"+name+".jsonl"]; !ok {
			t.Errorf("_account/%s.jsonl is missing", name)
		}
	}
	if acc := pkg.files["_account/account.jsonl"]; !strings.Contains(string(acc), `"email":"ada@example.com"`) {
		t.Errorf("account = %s", acc)
	}

	// The manifest says what it is, and every checksum is of the bytes in the archive.
	m := pkg.manifest
	if !m.Flags.Partial || !m.Flags.Personal || m.Subject != "user:"+f.adaID || m.Realm != "acme" || m.Operator != "user:"+f.adaID {
		t.Errorf("manifest = %+v", m)
	}
	for name, sum := range m.Checksums {
		raw, ok := pkg.files[name]
		if !ok {
			t.Errorf("checksum of %s, which is not in the archive", name)
			continue
		}
		h := sha256.Sum256(raw)
		if sum != "sha256:"+hex.EncodeToString(h[:]) {
			t.Errorf("checksum of %s is wrong", name)
		}
	}
	for _, name := range pkg.names {
		if name != exportpkg.ManifestName && m.Checksums[name] == "" {
			t.Errorf("%s has no checksum", name)
		}
	}

	// The README is in the archive, names her and lists the exclusions.
	readme := string(pkg.files["README.html"])
	for _, want := range []string{"ada@example.com", "app/orders.jsonl", "app/private", "fraud scores, exempt from access requests", "7 days"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README lacks %q", want)
		}
	}

	// Never a credential, a token, or another person's data, anywhere in the archive.
	for name, raw := range pkg.files {
		for _, forbidden := range []string{"argon2", "NOTE-FOR-STAFF-ONLY", "FRAUD-SCORE-0.9", "bob@example.com"} {
			if strings.Contains(string(raw), forbidden) && (forbidden != "bob@example.com" || name != "app/orders.jsonl") {
				t.Errorf("%s holds %q", name, forbidden)
			}
		}
	}
	// o3 names Bob too: a colleague's document comes whole, which the docs tell the operator to
	// narrow with `fields`.
	if len(steps) == 0 || !strings.HasPrefix(steps[0], "documents ") {
		t.Errorf("progress = %v", steps)
	}
	if steps[len(steps)-1] != "summary" {
		t.Errorf("last step = %q", steps[len(steps)-1])
	}
}

// With nothing held, an export is still an archive with her account, and with no storage the
// files are told as left out.
func TestPersonalExportOfAUserWithNothingAndNoStorage(t *testing.T) {
	f := newRulesFixture(t)
	seedDoc(t, f, "library", "l1", f.bobID, map[string]any{"title": "Bob's", "avatar": map[string]any{"id": "f1", "size": int64(3)}})
	pkg := buildExport(t, f, f.adaID, nil, nil)
	for _, name := range pkg.names {
		if strings.HasPrefix(name, "app/") {
			t.Errorf("a part with nothing of hers: %s", name)
		}
	}
	if len(pkg.manifest.Databases) != 0 || pkg.manifest.Files != 0 || pkg.files["_account/account.jsonl"] == nil {
		t.Errorf("manifest = %+v", pkg.manifest)
	}
	// Her own file with no storage is an exclusion, not a failure.
	seedDoc(t, f, "library", "l2", f.adaID, map[string]any{"title": "Hers", "avatar": map[string]any{"id": "f2", "size": int64(3)}})
	pkg = buildExport(t, f, f.adaID, nil, nil)
	found := false
	for _, x := range pkg.manifest.Exclusions {
		if x.What == "app/_files/f2" && strings.Contains(x.Reason, "storage") {
			found = true
		}
	}
	if !found {
		t.Errorf("exclusions = %+v", pkg.manifest.Exclusions)
	}
}
