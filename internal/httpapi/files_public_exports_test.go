package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A user's own export holds their public files beside the private ones, each read from the
// storage it is in.
func TestPersonalExportHoldsPublicFiles(t *testing.T) {
	f := newRulesFixture(t)
	seedDoc(t, f, "pictures", "p1", f.adaID, map[string]any{"title": "Mine", "photo": map[string]any{
		"id": "fl_pub", "name": "Me.png", "key_name": "Me.png", "public": true, "size": int64(4)}})
	seedDoc(t, f, "pictures", "p2", f.adaID, map[string]any{"title": "Secret", "secret": map[string]any{"id": "fl_priv", "name": "id.png", "size": int64(5)}})
	d := &documents{reg: f.reg}
	pics, _ := f.reg.Collection("acme", "app", "pictures")
	private := fakeObjects{d.fileKey(pics, fileLoc{id: "fl_priv"}): []byte("PRIV!")}
	public := fakeObjects{d.fileKey(pics, fileLoc{id: "fl_pub", public: true, keyName: "Me.png"}): []byte("PUB!")}

	pkg := buildExportWith(t, f, f.adaID, private, public, nil)
	if got := string(pkg.files["app/_files/fl_pub"]); got != "PUB!" {
		t.Errorf("public file = %q (have %v)", got, pkg.names)
	}
	if got := string(pkg.files["app/_files/fl_priv"]); got != "PRIV!" {
		t.Errorf("private file = %q", got)
	}
	if pkg.report.Files != 2 {
		t.Errorf("files = %d", pkg.report.Files)
	}
	// With no public storage to read it from, the file is told in the exclusions, not skipped silently.
	pkg = buildExportWith(t, f, f.adaID, private, nil, nil)
	var told bool
	for _, x := range pkg.manifest.Exclusions {
		if x.What == "app/_files/fl_pub" {
			told = true
		}
	}
	if !told || pkg.files["app/_files/fl_pub"] != nil {
		t.Errorf("exclusions: %+v", pkg.manifest.Exclusions)
	}
}

// A realm export reads a public file from the public storage, the one the document says.
func TestRealmExportStreamsAPublicFile(t *testing.T) {
	f := newFilesFixture(t)
	_, docID, file := f.publicFile(t)
	url := admin + "/export"
	rec, plan := f.doH(t, "GET", url+"?database=app", "", bearer(f.key))
	lease, _ := plan["lease"].(string)
	if rec.Code != http.StatusOK || lease == "" {
		t.Fatalf("plan: %d %v", rec.Code, plan)
	}
	hdr := map[string]string{"Authorization": "Bearer " + f.key, exportHeader: lease}
	// A private file of the same collection with the same id would be a different object.
	f.s3.Put("t/acme/app/pictures/"+file["id"].(string), []byte("not this one"))
	req := httptest.NewRequest("GET", url+"?file=app/pictures/"+file["id"].(string), nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "PNGBYTES!" {
		t.Errorf("export file: %d %q", w.Code, w.Body)
	}
	_ = docID
}

func TestImportOfPublicFiles(t *testing.T) {
	f := newFilesFixture(t)
	url := admin + "/import"
	h := map[string]string{"Authorization": "Bearer " + f.key, "Content-Type": "application/json"}
	m := `{"format": 1, "realm": "acme", "databases": [{"name": "app", "collections": [{"name": "pictures", "part": "app/pictures.jsonl"}]}]}`
	rec, b := f.doH(t, "POST", url, `{"manifest": `+m+`}`, h)
	if rec.Code != http.StatusOK {
		t.Fatalf("begin: %d %v", rec.Code, b)
	}
	lease := b["lease"].(string)
	put := func(id, name string, data []byte, sum string) *httptest.ResponseRecorder {
		if sum == "" {
			s := sha256.Sum256(data)
			sum = hex.EncodeToString(s[:])
		}
		req := httptest.NewRequest("PUT", url+"?file=app/pictures/"+id+"&public=true&key_name="+name, bytes.NewReader(data))
		req.Header.Set("Authorization", "Bearer "+f.key)
		req.Header.Set(exportHeader, lease)
		req.Header.Set("X-Backd-Sha256", sum)
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, req)
		return w
	}
	code := func(w *httptest.ResponseRecorder) string {
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		e, _ := out["error"].(map[string]any)
		c, _ := e["code"].(string)
		return c
	}
	png := realPNG(t, 8, 8)
	dirty, _ := dirtyJPEG(t, 8, 8)

	for _, tc := range []struct {
		name string
		w    *httptest.ResponseRecorder
		code int
		want string
	}{
		{"a wrong checksum", put("fl_aaaaaaaaaaaaaaaaaaaa", "A.png", png, hex.EncodeToString(make([]byte, 32))), 400, "checksum_mismatch"},
		{"an extension that is not the content's", put("fl_bbbbbbbbbbbbbbbbbbbb", "B.jpg", png, ""), 400, "file_extension_mismatch"},
		{"a name that needs cleaning", put("fl_cccccccccccccccccccc", "../C.png", png, ""), 400, "validation_error"},
		{"not an image", put("fl_dddddddddddddddddddd", "D.png", []byte("just text, not an image"), ""), 415, "unsupported_file_type"},
		{"a picture that carries metadata", put("fl_eeeeeeeeeeeeeeeeeeee", "E.jpg", dirty, ""), 422, "invalid_image"},
	} {
		if tc.w.Code != tc.code || code(tc.w) != tc.want {
			t.Errorf("%s: %d %s, want %d %s", tc.name, tc.w.Code, tc.w.Body, tc.code, tc.want)
		}
	}
	if keys := f.pub.Keys(); len(keys) != 0 {
		t.Fatalf("something was kept: %v", keys)
	}

	// A clean picture arrives under the key of a public file, with the headers of one.
	id := "fl_0123456789abcdefghij"
	if w := put(id, "Cover.png", png, ""); w.Code != http.StatusNoContent {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	key := "t/acme/app/pictures/" + id + "/Cover.png"
	if keys := f.pub.Keys(); len(keys) != 1 || keys[0] != key || len(f.s3.Keys()) != 0 {
		t.Fatalf("keys: %v %v", keys, f.s3.Keys())
	}
	if res := f.publicObject(t, key); res.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" || res.Header.Get("Content-Type") != "image/png" {
		t.Errorf("headers: %v", res.Header)
	}

	// The document brings the address it had in the realm it came from: it is not trusted, and
	// the one of this realm is computed.
	sum := sha256.Sum256(png)
	doc := importLines(t, 1, func(i int, d map[string]any) {
		d["photo"] = map[string]any{
			"id": id, "name": "Cover.png", "key_name": "Cover.png", "public": true, "size": int64(len(png)), "type": "image/png",
			"sha256": hex.EncodeToString(sum[:]), "uploaded_at": time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
			"url": "https://old-cdn.example.org/prod/acme/app/pictures/" + id + "/Cover.png",
		}
		d["_meta"].(map[string]any)["owner"] = f.adaID
	})
	lh := map[string]string{"Authorization": "Bearer " + f.key, exportHeader: lease}
	rec, out := f.doH(t, "POST", url+"?phase=write&part=app/pictures.jsonl", doc, lh)
	if rec.Code != http.StatusOK || out["counts"].(map[string]any)["created"] != float64(1) {
		t.Fatalf("write: %d %v", rec.Code, out)
	}
	_, got := f.as(t, f.ada, "GET", "/v1/acme/app/pictures/"+xidFor(1), "")
	if u := got["photo"].(map[string]any)["url"]; u != "https://cdn.acme.example/t/acme/app/pictures/"+id+"/Cover.png" {
		t.Errorf("url after the import = %v", u)
	}

	// A realm that has no public storage can't take a package with public files: the documents are invalid.
	ps := f.reg.Realms["acme"].Settings.PublicStorage
	f.reg.Realms["acme"].Settings.PublicStorage = nil
	defer func() { f.reg.Realms["acme"].Settings.PublicStorage = ps }()
	_, out = f.doH(t, "POST", url+"?phase=validate&part=app/pictures.jsonl", doc, lh)
	if out["counts"].(map[string]any)["invalid"] != float64(1) {
		t.Errorf("without a public storage: %v", out)
	}
	if w := put("fl_ffffffffffffffffffff", "F.png", png, ""); w.Code != http.StatusBadRequest {
		t.Errorf("a public file without a public storage: %d", w.Code)
	}
}
