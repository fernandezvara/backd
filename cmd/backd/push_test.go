package main

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// backd push web-keys: the private key becomes a realm secret, the public one is printed,
// an existing key is not replaced without --rotate, and rotating removes the web devices.
func TestPushWebKeys(t *testing.T) {
	var stored map[string]any
	var storedName string
	exists := false
	removed := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/acme/_admin/secrets":
			items := `[]`
			if exists {
				items = `[{"database":"","name":"VAPID_PRIVATE_KEY","created_at":"t","updated_at":"t","updated_by":"x"}]`
			}
			_, _ = w.Write([]byte(`{"items":` + items + `}`))
		case r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/v1/acme/_admin/secrets/"):
			storedName = strings.TrimPrefix(r.URL.Path, "/v1/acme/_admin/secrets/")
			_ = json.NewDecoder(r.Body).Decode(&stored)
			exists = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "DELETE" && r.URL.Path == "/v1/acme/_admin/push/devices":
			if r.URL.Query().Get("platform") != "web" {
				t.Errorf("platform: %q", r.URL.RawQuery)
			}
			removed++
			_, _ = w.Write([]byte(`{"platform":"web","removed":7}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &cliEnv{t: t, env: map[string]string{"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "backd", "credentials"), "BACKD_API_KEY": "bdk_x"}}
	base := []string{"push", "web-keys", "--realm", "acme", "--url", srv.URL}

	out := c.expect(0, "public_key:", "", base...)
	if storedName != "VAPID_PRIVATE_KEY" {
		t.Errorf("secret name: %q", storedName)
	}
	private, _ := stored["value"].(string)
	raw, err := base64.RawURLEncoding.DecodeString(private)
	if err != nil || len(raw) != 32 {
		t.Fatalf("private key: %q %v", private, err)
	}
	// The public key printed is the one of the private key stored.
	priv, err := ecdh.P256().NewPrivateKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes())
	if !strings.Contains(out, "public_key: "+want) || !strings.Contains(out, "private_key: secret:VAPID_PRIVATE_KEY") || strings.Contains(out, private) {
		t.Errorf("output: %s", out)
	}
	if removed != 0 {
		t.Error("devices were removed for a first key")
	}
	// An existing key is not replaced.
	c.expect(1, "--rotate", "", base...)
	if removed != 0 {
		t.Error("devices removed without --rotate")
	}
	// Rotating replaces it and removes the web devices.
	before := private
	c.expect(0, "7 web devices removed", "", append(base, "--rotate")...)
	if after, _ := stored["value"].(string); after == before || removed != 1 {
		t.Errorf("rotate: key changed %v, devices call %d", after != before, removed)
	}
}

func TestPushSecretsReadFromAFile(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got, _ = body["value"].(string)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := &cliEnv{t: t, env: map[string]string{"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "backd", "credentials"), "BACKD_API_KEY": "bdk_x"}}
	pem := "-----BEGIN PRIVATE KEY-----\nMIGHAgEAMBMGByqGSM49\n-----END PRIVATE KEY-----\n"
	c.expect(0, "set", pem, "secret", "set", "--realm", "acme", "--url", srv.URL, "--name", "APNS_KEY")
	if got != strings.TrimSuffix(pem, "\n") {
		t.Errorf("a multi-line secret was cut: %q", got)
	}
}
