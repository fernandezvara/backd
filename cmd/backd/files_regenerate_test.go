package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestFilesRegenerateStartsAndFollowsTheJob(t *testing.T) {
	var started map[string]any
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/acme/_admin/files/versions/regenerate":
			started = map[string]any{}
			_ = json.NewDecoder(r.Body).Decode(&started)
			if started["field"] == "busy" {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":{"code":"regenerate_running","message":"already running","request_id":"r"}}`))
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"j1","status":"queued"}`))
		case r.Method == "GET" && r.URL.Path == "/v1/acme/_admin/jobs/j1":
			if polls.Add(1) < 2 {
				_, _ = w.Write([]byte(`{"id":"j1","status":"running","progress":{"step":1,"name":"app/library.picture","status":"running","current":40,"message":"3 files made again so far"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"j1","status":"done","result":{"status":"ok"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &cliEnv{t: t, env: map[string]string{"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "backd", "credentials"), "BACKD_API_KEY": "bdk_x"}}
	base := []string{"files", "regenerate", "--realm", "acme", "--url", srv.URL, "--database", "app", "--collection", "library", "--field", "picture", "--interval", "10ms"}

	out := c.expect(0, "done", "", append(base, "--version", "thumb", "--missing-only", "--rate", "5")...)
	_ = out
	if started["database"] != "app" || started["collection"] != "library" || started["field"] != "picture" || started["version"] != "thumb" || started["missing_only"] != true || started["rate"] != float64(5) {
		t.Errorf("the request: %v", started)
	}
	// Without the optional flags only what is needed is sent.
	polls.Store(5)
	c.expect(0, "done", "", base...)
	if _, has := started["version"]; has || started["missing_only"] != false {
		t.Errorf("defaults: %v", started)
	}
	// --no-wait prints the job's id and leaves.
	c.expect(0, "j1", "", append(base, "--no-wait")...)
	// A regeneration already running is said plainly.
	c.expect(1, "already running", "", "files", "regenerate", "--realm", "acme", "--url", srv.URL, "--database", "app", "--collection", "library", "--field", "busy")
	c.expect(2, "--interval", "", append(base, "--interval", "soon")...)
}
