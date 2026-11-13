package cli

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/httpapi"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/triggers"
)

// stateFake is the dispatcher's recorded state, with one open gap.
type stateFake struct {
	mu       sync.Mutex
	resolved bool
}

func (s *stateFake) Status(context.Context) (triggers.DispatcherStatus, error) {
	return triggers.DispatcherStatus{Found: true, Held: true, Epoch: 7, Holder: "host-1", LeaseExpires: time.Now().Add(time.Minute), WatermarkAt: time.Now().Add(-3 * time.Second)}, nil
}

func (s *stateFake) Gaps(_ context.Context, realm string, all bool, _ int) ([]triggers.GapRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := triggers.GapRecord{
		Realm: realm, Collection: "orders", Trigger: "on_paid", Reason: triggers.GapWindow, From: time.Now().Add(-time.Hour), To: time.Now(), Count: 1,
		ID: "gap-abc", RecordedAt: time.Now(),
	}
	if s.resolved {
		if !all {
			return nil, nil
		}
		g.ResolvedAt = time.Now()
	}
	return []triggers.GapRecord{g}, nil
}

func (s *stateFake) ResolveGap(_ context.Context, _, id string, _ time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != "gap-abc" || s.resolved {
		return false, nil
	}
	s.resolved = true
	return true, nil
}

func TestTriggersStatusGapsAndResolveCommands(t *testing.T) {
	fnDir := "acme/main/" + registry.FunctionsDir
	root := writeConfig(t, map[string]string{
		"acme/realm.yaml":                  "auth:\n  strategies:\n    password: [password]\nroles:\n  ops:\n    admin: true\n",
		"acme/main/orders/schema.json":     `{"type":"object","properties":{"status":{"type":"string"}}}`,
		"acme/main/orders/collection.yaml": "triggers:\n  on_paid:\n    events: [created, updated]\n    when: \"document.status == 'paid'\"\n    function: main/notify\n    ordered: true\n",
		fnDir + "/notify/function.yaml":    "internal: true\nmode: async\n",
		fnDir + "/notify/index.js":         "",
	})
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	svc := &auth.Users{
		Store:    authtest.NewMemStore(),
		Hasher:   auth.NewHasher(2, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}),
		Settings: reg.Realms["acme"].Settings,
	}
	_, apiKey, err := svc.CreateAPIKey(ctx, "cli", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpapi.NewHandler(httpapi.Config{
		Log:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Registry: reg,
		Ready:    func(context.Context) error { return nil },
		Triggers: &stateFake{},
		Users: func(realm string) *auth.Users {
			if realm == "acme" {
				return svc
			}
			return nil
		},
	}))
	defer srv.Close()
	c := &cliEnv{t: t, env: map[string]string{
		"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "credentials"),
		"BACKD_API_KEY":     apiKey,
	}}

	out := c.expect(0, "dispatcher: leased (epoch 7)", "", "triggers", "status", "--realm", "acme", "--url", srv.URL)
	for _, want := range []string{"main/orders/on_paid", "created,updated", "main/notify", "runs with admin data access", "open gaps: 1", "document.status == 'paid'"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "host-1") {
		t.Error("the lease holder's name is shown")
	}
	c.expect(0, `"epoch":7`, "", "triggers", "status", "--realm", "acme", "--url", srv.URL, "--json")

	out = c.expect(0, "gap-abc", "", "triggers", "gaps", "--realm", "acme", "--url", srv.URL)
	if !strings.Contains(out, "window") || !strings.Contains(out, "open") {
		t.Errorf("gaps:\n%s", out)
	}
	c.expect(0, "gap gap-abc resolved at", "", "triggers", "resolve", "--realm", "acme", "--id", "gap-abc", "--url", srv.URL)
	code, out2, _ := c.run("", "triggers", "gaps", "--realm", "acme", "--url", srv.URL)
	if code != 0 || strings.Contains(out2, "gap-abc") {
		t.Errorf("open gaps after resolving: %d %q", code, out2)
	}
	c.expect(0, "resolved", "", "triggers", "gaps", "--realm", "acme", "--url", srv.URL, "--all")
	c.expect(1, "404", "", "triggers", "resolve", "--realm", "acme", "--id", "nope", "--url", srv.URL)
}
