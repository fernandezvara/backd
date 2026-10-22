package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/exportpkg"
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

// authPackage writes a package of authentication data by hand: a user, an identity with an
// Apple token and a second factor, sealed under an export key the test returns.
func authPackage(t *testing.T) (dir, keyText string) {
	t.Helper()
	keyText = "a-one-time-export-key-of-43-characters-abcd"
	ek, err := auth.ExportKey(keyText)
	if err != nil {
		t.Fatal(err)
	}
	seal := func(v string) string {
		s, err := auth.SealWith(ek, v)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	dir = t.TempDir()
	m := &exportpkg.Manifest{Format: 1, Realm: "acme", AuthKey: ek.KeyID, Flags: exportpkg.Flags{Auth: true}, Checksums: map[string]string{}}
	docs := map[string][]map[string]any{
		"users":      {{"id": "u1", "email": "ada@example.com"}},
		"identities": {{"id": "i1", "user_id": "u1", "provider": "apple", "apple_refresh_token": seal("apple-refresh")}},
		"mfa":        {{"id": "u1", "secret": seal("JBSWY3DPEHPK3PXP"), "recovery_codes": []any{}}},
		"devices":    {{"id": "d1", "user_id": "u1", "token": seal("fcm"), "session_id": ""}},
	}
	for _, coll := range auth.PortableCollections {
		var b bytes.Buffer
		for _, d := range docs[coll] {
			line, _ := exportpkg.EncodeDocument(d)
			b.Write(line)
			b.WriteByte('\n')
		}
		part := exportpkg.AuthPart(coll)
		path := filepath.Join(dir, filepath.FromSlash(part))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		h := exportpkg.NewHasher()
		h.Write(b.Bytes())
		m.Checksums[part] = h.Sum()
		m.Auth = append(m.Auth, exportpkg.Collection{Name: coll, Part: part, Documents: int64(len(docs[coll]))})
	}
	data, _ := m.Marshal()
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, keyText
}

func TestImportAuthDataOpensSecretsLocally(t *testing.T) {
	dir, keyText := authPackage(t)
	s := &importServer{}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	c := &cliEnv{t: t, env: map[string]string{"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "credentials"), "BACKD_API_KEY": "bdk_x", "EXPORT_KEY": keyText}}
	base := []string{"import", "--realm", "acme", "--url", srv.URL, "--from", dir}
	// Chunks are big enough here: one request a part.
	s.begin = nil

	// With the key, the sealed fields reach the server as plaintext, and the key never does.
	c.expect(0, "imported", "", append(base, "--auth-key", "env:EXPORT_KEY")...)
	all := ""
	for _, b := range s.bodies {
		all += b
	}
	if !strings.Contains(s.bodies["write:_system/auth/mfa.jsonl"], `"secret":{"$plain":"JBSWY3DPEHPK3PXP"}`) || !strings.Contains(s.bodies["write:_system/auth/devices.jsonl"], `"$plain":"fcm"`) || !strings.Contains(s.bodies["write:_system/auth/identities.jsonl"], `"$plain":"apple-refresh"`) {
		t.Errorf("bodies: %v", s.bodies)
	}
	if strings.Contains(all, keyText) {
		t.Error("the export key was sent")
	}
	if !strings.HasPrefix(strings.Join(s.calls, " "), "begin validate:_system/auth/users.jsonl") {
		t.Errorf("authentication data goes first: %v", s.calls)
	}

	// Without it, second factors and devices stay out and the Apple token is dropped.
	s.calls, s.bodies = nil, nil
	c.expect(0, "without second factors", "", base...)
	if _, sent := s.bodies["write:_system/auth/mfa.jsonl"]; sent {
		t.Errorf("mfa was sent: %v", s.bodies)
	}
	if _, sent := s.bodies["write:_system/auth/devices.jsonl"]; sent {
		t.Error("devices were sent")
	}
	if b := s.bodies["write:_system/auth/identities.jsonl"]; !strings.Contains(b, `"provider":"apple"`) || strings.Contains(b, "apple_refresh_token") {
		t.Errorf("identities: %s", b)
	}

	// A key that isn't the package's, or isn't a key, is refused before the server hears of it.
	s.calls = nil
	c.expect(1, "not the key this package was exported with", "", append(base, "--auth-key", "key:another-one-time-export-key-of-43-chars-xxxx")...)
	c.expect(2, "--auth-key", "", append(base, "--auth-key", "env:NOT_SET")...)
	c.expect(2, "--auth-key", "", append(base, "--auth-key", "nonsense")...)
	if len(s.calls) != 0 {
		t.Errorf("the server was called: %v", s.calls)
	}
}

func TestExportAuthDataShowsTheKeyOnce(t *testing.T) {
	const keyText = "a-one-time-export-key-of-43-characters-abcd"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.Method == "DELETE":
			w.WriteHeader(http.StatusNoContent)
		case q.Get("part") == "_system/auth/users.jsonl":
			_, _ = io.WriteString(w, "{\"id\":\"u1\",\"email\":\"ada@example.com\"}\n")
		case q.Get("part") != "":
		default:
			if q.Get("include") != "auth" {
				t.Errorf("include not sent: %v", q)
			}
			_, _ = io.WriteString(w, `{"lease":"L","export_key":"`+keyText+`","manifest":{"format":1,"realm":"acme","flags":{"auth":true},"auth_key_fingerprint":"fp","databases":[],"auth":[{"name":"users","part":"_system/auth/users.jsonl"}],"encrypted_fields":[]},"file_fields":{}}`)
		}
	}))
	defer srv.Close()
	c := &cliEnv{t: t, env: map[string]string{"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "credentials"), "BACKD_API_KEY": "bdk_x"}}
	out := filepath.Join(t.TempDir(), "pkg")

	c.expect(2, "whole realm", "", "export", "--realm", "acme", "--url", srv.URL, "--out", out, "--include", "auth", "--db", "app")
	c.expect(2, "--include", "", "export", "--realm", "acme", "--url", srv.URL, "--out", out, "--include", "sessions")
	c.expect(2, "needs --include auth", "", "export", "--realm", "acme", "--url", srv.URL, "--out", out, "--auth-key-out", "k")
	got := c.expect(0, "EXPORT KEY (shown once", "", "export", "--realm", "acme", "--url", srv.URL, "--out", out, "--include", "auth")
	_ = got
	data, _ := os.ReadFile(filepath.Join(out, "manifest.json"))
	m, err := exportpkg.ParseManifest(data)
	if err != nil || !m.Flags.Auth || len(m.Auth) != 1 || m.Auth[0].Documents != 1 || m.Checksums["_system/auth/users.jsonl"] == "" || m.AuthKey != "fp" {
		t.Errorf("manifest: %+v %v", m, err)
	}
	// Into a file instead: readable only by the operator, and not printed.
	keyFile := filepath.Join(t.TempDir(), "export.key")
	out2 := filepath.Join(t.TempDir(), "pkg")
	code, _, errOut := c.run("", "export", "--realm", "acme", "--url", srv.URL, "--out", out2, "--include", "auth", "--auth-key-out", keyFile)
	if code != 0 || strings.Contains(errOut, keyText) {
		t.Errorf("key file: %d %s", code, errOut)
	}
	if b, err := os.ReadFile(keyFile); err != nil || strings.TrimSpace(string(b)) != keyText {
		t.Errorf("key file: %q %v", b, err)
	}
	if st, _ := os.Stat(keyFile); st.Mode().Perm() != 0o600 {
		t.Errorf("key file mode: %v", st.Mode())
	}
}
