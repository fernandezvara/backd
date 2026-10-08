package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// The security review of the file surface (roadmap #155) turned some of its checks into
// tests; they live here, with the findings they came from.

// Active content has to be named: allowing images or text doesn't allow SVG or HTML.
func TestFamiliesDontAdmitActiveContent(t *testing.T) {
	f := newFilesFixture(t)
	id := f.newDoc(t, "library") // receipts takes application/pdf and image/*
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"><script>alert(1)</script></svg>`)
	rec, out := f.upload(t, f.ada, "library", id, "receipts", "x.svg", "image/svg+xml", svg, nil)
	if rec.Code != http.StatusUnsupportedMediaType || out["error"].(map[string]any)["code"] != "unsupported_file_type" {
		t.Errorf("an SVG into image/*: %d %v", rec.Code, out)
	}
	html := []byte("<!doctype html><html><body><script>alert(1)</script></body></html>")
	if rec, _ := f.upload(t, f.ada, "library", id, "receipts", "x.png", "image/png", html, nil); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("HTML claiming to be a PNG: %d", rec.Code)
	}
	if len(f.s3.Keys()) != 0 {
		t.Errorf("a refused file was stored: %v", f.s3.Keys())
	}
	// A field that is unrestricted takes it, and serves it only as a download, sandboxed.
	_, doc := f.upload(t, f.ada, "library", id, "manual", "evil.html", "text/html", html, nil)
	file := doc["manual"].(map[string]any)
	rec2 := f.get(t, "/v1/acme/app/library/"+id+"/_files/manual/"+file["id"].(string), map[string]string{"Authorization": "Bearer " + f.ada})
	h := rec2.Header()
	if rec2.Code != 200 || !strings.HasPrefix(h.Get("Content-Disposition"), "attachment") || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != "sandbox" {
		t.Errorf("active content served: %d %v", rec2.Code, h)
	}
}

// A name is only ever text: it can't add a header, escape the quotes or reach a key.
func TestFileNamesCantInjectAnything(t *testing.T) {
	f := newFilesFixture(t)
	id := f.newDoc(t, "library")
	hostile := "a\r\nX-Evil: 1\"; filename=\"b\x00/../../etc/passwd\u202egnp.html"
	_, doc := f.upload(t, f.ada, "library", id, "manual", hostile, "text/plain", []byte("x"), nil)
	file := doc["manual"].(map[string]any)
	if strings.ContainsAny(file["name"].(string), "\r\n\x00/\u202e") {
		t.Errorf("the stored name: %q", file["name"])
	}
	rec := f.get(t, "/v1/acme/app/library/"+id+"/_files/manual/"+file["id"].(string), map[string]string{"Authorization": "Bearer " + f.ada})
	for k := range rec.Header() {
		if strings.EqualFold(k, "X-Evil") {
			t.Error("a name added a response header")
		}
	}
	if cd := rec.Header().Get("Content-Disposition"); strings.ContainsAny(cd, "\r\n") || strings.Count(cd, `filename="`) != 1 {
		t.Errorf("Content-Disposition: %q", cd)
	}
	// The name never reaches the key: only the file's id does.
	for _, k := range f.s3.Keys() {
		if strings.Contains(k, "passwd") || strings.Contains(k, "Evil") {
			t.Errorf("a name is in a key: %s", k)
		}
	}
}

// A file id in a URL is only ever looked up in the document: it is no path.
func TestFileIDsAreNotPaths(t *testing.T) {
	f := newFilesFixture(t)
	id := f.newDoc(t, "library")
	f.upload(t, f.ada, "library", id, "avatar", "a.png", "image/png", pngBytes(5), nil)
	f.s3.Put("t/acme/app/library/secret", []byte("not a file of any document"))
	hdr := map[string]string{"Authorization": "Bearer " + f.ada}
	before := f.s3.Requests["GET"]
	for _, fid := range []string{"..%2Fsecret", "..%2F..%2Fgallery%2Ffl_x", "%2e%2e/secret", "secret", "fl_" + strings.Repeat("a", 20)} {
		rec := f.get(t, "/v1/acme/app/library/"+id+"/_files/avatar/"+fid, hdr)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", fid, rec.Code)
		}
	}
	if f.s3.Requests["GET"] != before {
		t.Error("a request for a file that isn't the document's reached the storage")
	}
}

// The secrets of the realm are not in what backd answers or logs: not the storage keys,
// not an upload token, not the key links are signed with.
func TestSecretsStayOutOfAnswersAndLogs(t *testing.T) {
	f := newFilesFixture(t)
	id, token := f.pending(t, f.ada, "forms", "scan", pngBytes(5))
	_, doc := f.as(t, f.ada, "POST", "/v1/acme/app/forms", `{"title": "t", "scan": `+ref(id, token)+`}`)
	did := doc["id"].(string)
	f.upload(t, f.ada, "library", f.newDoc(t, "library"), "manual", "m.txt", "text/plain", []byte("m"), nil)
	for _, p := range []string{"/v1/acme/_admin/storage", "/v1/acme/_admin/config", "/v1/acme/_admin/secrets"} {
		rec, _ := f.doRaw(t, "GET", p, nil, map[string]string{"Authorization": "Bearer " + f.key})
		if strings.Contains(rec.Body.String(), "v-STORAGE") {
			t.Errorf("%s shows a storage key", p)
		}
	}
	rec, _ := f.doRaw(t, "POST", "/v1/acme/_admin/storage/check", nil, map[string]string{"Authorization": "Bearer " + f.key})
	if strings.Contains(rec.Body.String(), "v-STORAGE") {
		t.Error("the storage check shows a key")
	}
	logs := f.log.String()
	for _, secret := range []string{token, "v-STORAGE_ACCESS_KEY", "v-STORAGE_SECRET_KEY"} {
		if strings.Contains(logs, secret) {
			t.Errorf("the log holds %q", secret[:min(len(secret), 12)]+"…")
		}
	}
	_ = did
}

// A leaked upload id alone does nothing, and neither does a token without its id's owner.
func TestAnUploadIDAloneAttachesNothing(t *testing.T) {
	f := newFilesFixture(t)
	id, _ := f.pending(t, f.ada, "forms", "scan", pngBytes(5))
	for _, body := range []string{`{"title": "t", "scan": {"upload": "` + id + `"}}`, `{"title": "t", "scan": {"upload": "` + id + `", "token": ""}}`, `{"title": "t", "scan": {"upload": "` + id + `", "token": "` + id + `"}}`} {
		if code, _ := f.as(t, f.ada, "POST", "/v1/acme/app/forms", body); code != 400 {
			t.Errorf("%s: %d", body, code)
		}
	}
	// Nor can it be completed or turned into a document by someone who only knows it.
	rec, _ := f.complete(t, f.bob, "forms", "scan", id, "")
	if rec.Code != 400 {
		t.Errorf("complete without a token: %d", rec.Code)
	}
}
