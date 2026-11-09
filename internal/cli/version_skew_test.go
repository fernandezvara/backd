package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The CLI says what it is in its User-Agent, and prints one warning line per command when the
// server answers that it runs another version.
func TestTheCLIWarnsOnceAboutAVersionSkew(t *testing.T) {
	var agents []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agents = append(agents, r.Header.Get("User-Agent"))
		w.Header().Set("X-Backd-Version-Skew", "newer")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"has_more":false}`))
	}))
	defer srv.Close()
	env := map[string]string{"BACKD_API_KEY": "bdk_test", "HOME": t.TempDir()}
	var out, errOut bytes.Buffer
	code := Run([]string{"user", "list", "--realm", "acme", "--url", srv.URL}, func(k string) string { return env[k] }, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	if len(agents) == 0 || agents[0] != "backd/dev" {
		t.Errorf("User-Agent %v", agents)
	}
	if n := strings.Count(errOut.String(), "the server runs a newer backd than this CLI"); n != 1 {
		t.Errorf("%d warnings:\n%s", n, errOut.String())
	}
}
