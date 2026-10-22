package main

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fernandezvara/backd/internal/exportpkg"
)

// exportServer is a backd that serves an export of two collections, one with a file field.
type exportServer struct {
	mu       sync.Mutex
	requests []string
	failPart string // answered 500 once
	released bool
	query    string
}

const (
	exportNotes = "{\"id\":\"n1\",\"title\":\"one\",\"_meta\":{\"version\":1}}\n{\"id\":\"n2\",\"title\":\"two\",\"_meta\":{\"version\":1}}\n"
	exportDocs  = "{\"id\":\"d1\",\"scan\":{\"id\":\"f1\",\"size\":5},\"_meta\":{\"version\":1}}\n{\"id\":\"d2\",\"scan\":null,\"_meta\":{\"version\":1}}\n"
)

func (s *exportServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.URL.Path != "/v1/acme/_admin/export" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		switch {
		case r.Method == "DELETE":
			s.released = q.Get("complete") == "true" && r.Header.Get("X-Backd-Export") == "L1"
			w.WriteHeader(http.StatusNoContent)
		case q.Get("part") != "":
			s.requests = append(s.requests, q.Get("part"))
			if q.Get("part") == s.failPart {
				s.failPart = ""
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"boom","request_id":"r"}}`))
				return
			}
			switch q.Get("part") {
			case "app/notes.jsonl":
				_, _ = io.WriteString(w, exportNotes)
			case "app/docs.jsonl":
				_, _ = io.WriteString(w, exportDocs)
			}
		case q.Get("file") != "":
			s.requests = append(s.requests, "file:"+q.Get("file"))
			_, _ = io.WriteString(w, "PDF-1")
		default:
			s.query = r.URL.RawQuery
			w.Header().Set("Content-Type", "application/json")
			fields := `{"app/docs":["scan"]}`
			if q.Get("files") == "false" {
				fields = `{}`
			}
			_, _ = io.WriteString(w, `{"lease":"L1","expires_in":600,"manifest":{"format":1,"backd_version":"test","realm":"acme","created_at":"2026-10-09T10:00:00Z","operator":"key:k","config_fingerprint":"abc","flags":{"partial":false,"anonymized":false,"auth":false,"no_files":false},"databases":[{"name":"app","collections":[{"name":"docs","documents":0,"part":"app/docs.jsonl"},{"name":"notes","documents":0,"part":"app/notes.jsonl"}]}],"encrypted_fields":[]},"file_fields":`+fields+`}`)
		}
	})
}

func TestExportWritesAPackageAndResumes(t *testing.T) {
	s := &exportServer{failPart: "app/notes.jsonl"}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	c := &cliEnv{t: t, env: map[string]string{"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "credentials"), "BACKD_API_KEY": "bdk_x"}}
	out := filepath.Join(t.TempDir(), "pkg")
	args := []string{"export", "--realm", "acme", "--url", srv.URL, "--out", out}

	// A part that fails stops the export and leaves what was done.
	c.expect(1, "500 internal", "", args...)
	if _, err := os.Stat(filepath.Join(out, ".export-progress.json")); err != nil {
		t.Fatalf("no progress file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "app", "notes.jsonl.partial")); err == nil {
		t.Error("a half-written part was left")
	}
	// Running it again continues: the part that was done is not fetched again.
	s.requests = nil
	c.expect(0, "exported 4 documents and 1 files", "", args...)
	if strings.Join(s.requests, ",") != "app/notes.jsonl,file:app/docs/f1" {
		t.Errorf("requests on resume: %v", s.requests)
	}
	if !s.released {
		t.Error("the server was not told the export ended")
	}
	if _, err := os.Stat(filepath.Join(out, ".export-progress.json")); err == nil {
		t.Error("the progress file outlived a complete export")
	}

	// The package: documents, the file, the wrapped keys and a manifest whose checksums match.
	data, err := os.ReadFile(filepath.Join(out, exportpkg.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	m, err := exportpkg.ParseManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if m.Files != 1 || m.Databases[0].Collections[0].Name != "docs" || m.Databases[0].Collections[0].Documents != 2 || m.Databases[0].Collections[1].Documents != 2 {
		t.Errorf("manifest: %+v", m)
	}
	for part, sum := range m.Checksums {
		h := exportpkg.NewHasher()
		b, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(part)))
		if err != nil {
			t.Fatalf("%s: %v", part, err)
		}
		h.Write(b)
		if h.Sum() != sum {
			t.Errorf("%s: checksum %s, manifest says %s", part, h.Sum(), sum)
		}
	}
	if len(m.Checksums) != 4 { // two collections, the file and the keys
		t.Errorf("checksums: %v", m.Checksums)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "app", "_files", "f1")); string(b) != "PDF-1" {
		t.Errorf("file: %q", b)
	}
	if st, _ := os.Stat(filepath.Join(out, exportpkg.ManifestName)); st.Mode().Perm() != 0o600 {
		t.Errorf("manifest permissions: %v", st.Mode())
	}

	// A directory with other content is not written into.
	c.expect(1, "not empty", "", args...)
}

func TestExportScopeNoFilesAndArchive(t *testing.T) {
	s := &exportServer{}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	c := &cliEnv{t: t, env: map[string]string{"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "credentials"), "BACKD_API_KEY": "bdk_x"}}
	out := filepath.Join(t.TempDir(), "pkg")

	c.expect(2, "--collection needs --db", "", "export", "--realm", "acme", "--url", srv.URL, "--out", out, "--collection", "notes")
	c.expect(0, "pkg.tar.gz", "", "export", "--realm", "acme", "--url", srv.URL, "--out", out, "--db", "app", "--collection", "notes, docs", "--no-files", "--archive")
	if s.query != "collection=notes&collection=docs&database=app&files=false" {
		t.Errorf("the plan request: %s", s.query)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("the directory stayed beside its archive")
	}
	f, err := os.Open(out + ".tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if st, _ := f.Stat(); st.Mode().Perm() != 0o600 {
		t.Errorf("archive permissions: %v", st.Mode())
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, h.Name)
	}
	want := "pkg/_system/data_keys.jsonl pkg/app/docs.jsonl pkg/app/notes.jsonl pkg/manifest.json"
	if strings.Join(names, " ") != want {
		t.Errorf("archive entries: %v", names)
	}
	// An archive isn't overwritten.
	c.expect(1, "exists", "", "export", "--realm", "acme", "--url", srv.URL, "--out", out, "--archive")
}

func TestExportCSVExtract(t *testing.T) {
	var planQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.Method == "DELETE":
			w.WriteHeader(http.StatusNoContent)
		case q.Get("part") == "app/notes.jsonl":
			_, _ = io.WriteString(w, "{\"id\":\"n1\",\"title\":\"one, two\",\"meta\":{\"a\":1}}\n{\"id\":\"n2\",\"title\":\"=1+1\"}\n")
		default:
			planQuery = r.URL.RawQuery
			_, _ = io.WriteString(w, `{"lease":"L","manifest":{"format":1,"realm":"acme","flags":{"partial":true,"csv":true,"no_files":true},"databases":[{"name":"app","collections":[{"name":"notes","part":"app/notes.jsonl"}]}],"encrypted_fields":[]},"file_fields":{}}`)
		}
	}))
	defer srv.Close()
	c := &cliEnv{t: t, env: map[string]string{"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "credentials"), "BACKD_API_KEY": "bdk_x"}}
	out := filepath.Join(t.TempDir(), "report")
	base := []string{"export", "--realm", "acme", "--url", srv.URL, "--out", out}

	c.expect(2, "--where needs", "", append(base, "--where", `{"a":1}`)...)
	c.expect(2, "want jsonl or csv", "", append(base, "--format", "xml")...)
	c.expect(2, "--no-files has no meaning", "", append(base, "--format", "csv", "--no-files")...)
	c.expect(0, "exported 2 documents", "", append(base, "--db", "app", "--collection", "notes", "--format", "csv", "--fields", "title,meta", "--where", `{"title":"x"}`)...)
	if planQuery != "collection=notes&database=app&fields=title%2Cmeta&format=csv&where=%7B%22title%22%3A%22x%22%7D" {
		t.Errorf("plan request: %s", planQuery)
	}
	got, err := os.ReadFile(filepath.Join(out, "app", "notes.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "id,title,meta.a\nn1,\"one, two\",1\nn2,'=1+1,\n"; string(got) != want {
		t.Errorf("csv:\n%s\nwant:\n%s", got, want)
	}
	if _, err := os.Stat(filepath.Join(out, "app", "notes.jsonl.tmp")); err == nil {
		t.Error("the temporary documents were left")
	}
	if _, err := os.Stat(filepath.Join(out, "_system")); err == nil {
		t.Error("an extract has no system data")
	}
	data, _ := os.ReadFile(filepath.Join(out, "manifest.json"))
	m, err := exportpkg.ParseManifest(data)
	if err != nil || !m.Flags.CSV || !m.Flags.Partial || m.Checksums["app/notes.csv"] == "" || len(m.Checksums) != 1 || m.Databases[0].Collections[0].Part != "app/notes.csv" || m.Databases[0].Collections[0].Documents != 2 {
		t.Errorf("manifest: %+v %v", m, err)
	}
}
