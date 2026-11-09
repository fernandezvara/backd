package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
)

func TestVersionSkewOnAuthenticatedAdminAnswersOnly(t *testing.T) {
	f := newRulesFixture(t, func(c *Config) { c.Version = "v1.2.0" })
	f.svc.Realm = "acme"
	_, key, err := f.svc.CreateAPIKey(context.Background(), "skew-admin", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	as := func(agent string, hdr map[string]string) map[string]string {
		out := map[string]string{"User-Agent": agent}
		for k, v := range hdr {
			out[k] = v
		}
		return out
	}
	for _, tt := range []struct {
		agent, want string
	}{
		{"backd/1.1.9", "newer"},
		{"backd/v1.2.0", ""},
		{"backd/1.2.0", ""},
		{"backd/1.3.0-rc.1", "older"},
		{"backd/not-a-version", ""},
		{"Mozilla/5.0 backd/1.0.0", ""}, // only the first product token counts
		{"backd-js/1.0.0", ""},
	} {
		rec, _ := f.doH(t, "GET", admin+"/whoami", "", as(tt.agent, bearer(key)))
		if rec.Code != http.StatusOK || rec.Header().Get(versionSkewHeader) != tt.want {
			t.Errorf("%s: %d, skew %q, want %q", tt.agent, rec.Code, rec.Header().Get(versionSkewHeader), tt.want)
		}
	}
	// Never before authentication, and never off the admin API.
	if rec, _ := f.doH(t, "GET", admin+"/whoami", "", as("backd/1.0.0", nil)); rec.Code != http.StatusUnauthorized || rec.Header().Get(versionSkewHeader) != "" {
		t.Errorf("unauthenticated: %d, skew %q", rec.Code, rec.Header().Get(versionSkewHeader))
	}
	if rec, _ := f.doH(t, "GET", "/v1/acme/app/posts", "", as("backd/1.0.0", bearer(key))); rec.Header().Get(versionSkewHeader) != "" {
		t.Errorf("a data route: skew %q", rec.Header().Get(versionSkewHeader))
	}
	// Browsers never see it.
	rec, _ := f.doH(t, "GET", admin+"/whoami", "", as("backd/1.0.0", map[string]string{"Authorization": "Bearer " + key, "Origin": "https://app.acme.example"}))
	if exposed := rec.Header().Get("Access-Control-Expose-Headers"); exposed == "" || containsToken(exposed, versionSkewHeader) {
		t.Errorf("exposed headers: %q", exposed)
	}
}

func TestADevelopmentCLIOnlyReachesADevelopmentServer(t *testing.T) {
	for _, tt := range []struct {
		name    string
		version string
		dev     bool
		agent   string
		refused bool
	}{
		{"released server, dev CLI", "v1.2.0", false, "backd/dev", true},
		{"released server, released CLI", "v1.2.0", false, "backd/1.2.0", false},
		{"released server, another client", "v1.2.0", false, "backd-go/dev", false},
		{"released server in dev mode", "v1.2.0", true, "backd/dev", false},
		{"development build", "dev", false, "backd/dev", false},
	} {
		f := newRulesFixture(t, func(c *Config) { c.Version, c.Dev = tt.version, tt.dev })
		for _, path := range []string{"/healthz", "/v1/acme/app/posts", admin + "/whoami"} {
			rec, body := f.doH(t, "GET", path, "", map[string]string{"User-Agent": tt.agent})
			code, _ := body["error"].(map[string]any)
			refused := rec.Code == http.StatusForbidden && code != nil && code["code"] == codeDevClientRefused
			if refused != tt.refused {
				t.Errorf("%s, %s: %d %v", tt.name, path, rec.Code, body)
			}
		}
	}
}

func containsToken(list, token string) bool {
	for _, t := range splitComma(list) {
		if t == token {
			return true
		}
	}
	return false
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			part := s[start:i]
			for len(part) > 0 && part[0] == ' ' {
				part = part[1:]
			}
			out = append(out, part)
			start = i + 1
		}
	}
	return out
}
