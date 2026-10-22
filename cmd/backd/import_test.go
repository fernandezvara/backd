package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// importServer is a backd that takes an import and records what it was sent.
type importServer struct {
	mu      sync.Mutex
	calls   []string
	begin   map[string]any
	bodies  map[string]string // phase+part -> what was sent
	invalid string            // the id it calls invalid when validating
	files   map[string]string
}

func (s *importServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		q := r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "DELETE":
			s.calls = append(s.calls, "end:"+q.Get("complete"))
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "PUT":
			data, _ := io.ReadAll(r.Body)
			s.calls = append(s.calls, "file:"+q.Get("file"))
			if s.files == nil {
				s.files = map[string]string{}
			}
			s.files[q.Get("file")] = string(data) + "|" + r.Header.Get("X-Backd-Sha256")
			w.WriteHeader(http.StatusNoContent)
		case q.Get("phase") != "":
			data, _ := io.ReadAll(r.Body)
			key := q.Get("phase") + ":" + q.Get("part")
			s.calls = append(s.calls, key)
			if s.bodies == nil {
				s.bodies = map[string]string{}
			}
			s.bodies[key] += string(data)
			n := strings.Count(string(data), "\n")
			out := map[string]any{"documents": n, "counts": map[string]int{"created": n}}
			if q.Get("phase") == "validate" && s.invalid != "" && strings.Contains(string(data), s.invalid) {
				out["counts"] = map[string]int{"created": n - 1, "invalid": 1}
				out["problems"] = []map[string]any{{"id": s.invalid, "line": 2, "outcome": "invalid", "problems": []map[string]string{{"path": "title", "reason": "got number, want string"}}}}
			}
			_ = json.NewEncoder(w).Encode(out)
		default:
			_ = json.NewDecoder(r.Body).Decode(&s.begin)
			s.calls = append(s.calls, "begin")
			_, _ = io.WriteString(w, `{"lease":"L9","expires_in":600,"max_chunk_bytes":60,"warnings":["config differs"],"file_fields":{"app/docs":["scan"]}}`)
		}
	})
}

// exportedPackage writes the package TestExportWritesAPackageAndResumes makes, through the real
// export command against the fake server of that test.
func exportedPackage(t *testing.T, archive bool) string {
	t.Helper()
	s := &exportServer{}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	c := &cliEnv{t: t, env: map[string]string{"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "credentials"), "BACKD_API_KEY": "bdk_x"}}
	out := filepath.Join(t.TempDir(), "pkg")
	args := []string{"export", "--realm", "acme", "--url", srv.URL, "--out", out}
	if archive {
		args = append(args, "--archive")
	}
	c.expect(0, "exported", "", args...)
	if archive {
		return out + ".tar.gz"
	}
	return out
}

func TestImportValidatesThenWrites(t *testing.T) {
	pkg := exportedPackage(t, false)
	s := &importServer{}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	c := &cliEnv{t: t, env: map[string]string{"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "credentials"), "BACKD_API_KEY": "bdk_x"}}
	base := []string{"import", "--realm", "acme", "--url", srv.URL, "--from", pkg}

	out := c.expect(0, "imported 4 documents", "", append(base, "--mode", "replace")...)
	_ = out
	if s.begin["mode"] != "replace" || s.begin["dry_run"] != false {
		t.Errorf("begin: %v", s.begin)
	}
	if m, _ := s.begin["manifest"].(map[string]any); m == nil || m["checksums"] != nil && len(m["checksums"].(map[string]any)) != 0 {
		t.Errorf("the manifest sent holds checksums: %v", s.begin["manifest"])
	}
	// Every document is validated before anything is written: files, then documents.
	// The server asks for chunks of 60 bytes: each part of two documents takes two requests.
	want := "begin validate:app/docs.jsonl validate:app/docs.jsonl validate:app/notes.jsonl validate:app/notes.jsonl file:app/docs/f1 write:app/docs.jsonl write:app/docs.jsonl write:app/notes.jsonl write:app/notes.jsonl end:true"
	if got := strings.Join(s.calls, " "); got != want {
		t.Errorf("calls:\n%s\nwant\n%s", got, want)
	}
	if s.bodies["write:app/notes.jsonl"] != exportNotes || s.bodies["validate:app/docs.jsonl"] != exportDocs {
		t.Errorf("bodies: %v", s.bodies)
	}
	if !strings.HasPrefix(s.files["app/docs/f1"], "PDF-1|") || len(s.files["app/docs/f1"]) != len("PDF-1|")+64 {
		t.Errorf("the file: %q", s.files["app/docs/f1"])
	}

	// A dry run validates and stops.
	s.calls, s.bodies = nil, nil
	c.expect(0, "dry run: nothing was written", "", append(base, "--dry-run")...)
	if got := strings.Join(s.calls, " "); got != "begin validate:app/docs.jsonl validate:app/docs.jsonl validate:app/notes.jsonl validate:app/notes.jsonl end:true" {
		t.Errorf("dry run calls: %s", got)
	}
}

func TestImportStopsOnInvalidDocuments(t *testing.T) {
	pkg := exportedPackage(t, false)
	s := &importServer{invalid: "n2"}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	c := &cliEnv{t: t, env: map[string]string{"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "credentials"), "BACKD_API_KEY": "bdk_x"}}
	report := filepath.Join(t.TempDir(), "report.json")
	base := []string{"import", "--realm", "acme", "--url", srv.URL, "--from", pkg, "--report", report}

	c.expect(1, "nothing was written", "", base...)
	if strings.Contains(strings.Join(s.calls, " "), "write:") || strings.Contains(strings.Join(s.calls, " "), "file:") {
		t.Errorf("something was written: %v", s.calls)
	}
	if strings.Contains(strings.Join(s.calls, " "), "end:true") {
		t.Errorf("a failed import said it completed: %v", s.calls)
	}
	data, err := os.ReadFile(report)
	if err != nil || !strings.Contains(string(data), `"title"`) || !strings.Contains(string(data), "app/notes.jsonl") {
		t.Errorf("report: %s %v", data, err)
	}
	// With --skip-invalid the rest goes in.
	s.calls = nil
	c.expect(0, "imported", "", append(base, "--skip-invalid")...)
	if !strings.Contains(strings.Join(s.calls, " "), "write:app/notes.jsonl") {
		t.Errorf("skip-invalid: %v", s.calls)
	}
}

func TestImportChecksPackageAndMode(t *testing.T) {
	pkg := exportedPackage(t, false)
	s := &importServer{}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	c := &cliEnv{t: t, env: map[string]string{"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "credentials"), "BACKD_API_KEY": "bdk_x"}}
	base := []string{"import", "--realm", "acme", "--url", srv.URL, "--from", pkg}

	c.expect(2, "--mode", "", append(base, "--mode", "merge")...)
	// A package that was changed after it was made is refused before the server hears of it.
	if err := os.WriteFile(filepath.Join(pkg, "app", "notes.jsonl"), []byte("{\"id\":\"x\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.expect(1, "app/notes.jsonl: checksum differs", "", base...)
	os.Remove(filepath.Join(pkg, "app", "docs.jsonl"))
	c.expect(1, "app/docs.jsonl: missing", "", base...)
	if len(s.calls) != 0 {
		t.Errorf("the server was called: %v", s.calls)
	}
	c.expect(1, "not a backd export package", "", "import", "--realm", "acme", "--url", srv.URL, "--from", t.TempDir())
}

func TestImportReadsAnArchive(t *testing.T) {
	pkg := exportedPackage(t, true)
	s := &importServer{}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	c := &cliEnv{t: t, env: map[string]string{"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "credentials"), "BACKD_API_KEY": "bdk_x"}}
	c.expect(0, "imported 4 documents", "", "import", "--realm", "acme", "--url", srv.URL, "--from", pkg)
}

func TestEachChunk(t *testing.T) {
	var chunks []string
	var firsts []int
	err := eachChunk(strings.NewReader("aaaa\nbbbb\ncc\ndddddddddddd\ne"), 10, func(c []byte, first int) error {
		chunks = append(chunks, string(c))
		firsts = append(firsts, first)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"aaaa\nbbbb\n", "cc\n", "dddddddddddd\n", "e\n"}
	if strings.Join(chunks, "|") != strings.Join(want, "|") || firsts[0] != 1 || firsts[1] != 3 || firsts[2] != 4 || firsts[3] != 5 {
		t.Errorf("chunks %q firsts %v", chunks, firsts)
	}
}
