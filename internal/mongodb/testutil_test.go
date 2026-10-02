package mongodb

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/rs/xid"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/fernandezvara/backd/internal/registry"
)

// testClient connects to MONGO_TEST_URI, skipping the test when it's unset.
func testClient(t *testing.T) *mongo.Client {
	t.Helper()
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set; skipping integration test")
	}
	client, err := Connect(context.Background(), uri)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	return client
}

// testRealm returns a unique realm name and drops its databases after the test.
func testRealm(t *testing.T, client *mongo.Client) string {
	t.Helper()
	realm := "t" + xid.New().String()
	t.Cleanup(func() {
		ctx := context.Background()
		names, _ := client.ListDatabaseNames(ctx, map[string]any{"name": map[string]any{"$regex": "^" + realm + "__"}})
		for _, n := range names {
			_ = client.Database(n).Drop(ctx)
		}
	})
	return realm
}

// loadRegistry writes collection schemas (keyed by "<database>/<collection>")
// under realm, with auth disabled, and loads them. Keys ending in ".json"
// are written as-is, e.g. "app/people/indexes.json".
func loadRegistry(t *testing.T, realm string, files map[string]string) *registry.Registry {
	t.Helper()
	return loadRegistryWith(t, realm, "auth: disabled\n", files)
}

// loadRegistryWith is loadRegistry with the given realm.yaml content.
func loadRegistryWith(t *testing.T, realm, realmYAML string, files map[string]string) *registry.Registry {
	t.Helper()
	root := t.TempDir()
	for p, s := range files {
		file := filepath.Join(root, realm, p)
		if ext := filepath.Ext(p); ext != ".json" && ext != ".yaml" {
			file = filepath.Join(file, "schema.json")
		}
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, realm, registry.RealmFile), []byte(realmYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return reg
}

// logBuffer is a concurrency-safe log sink for assertions on log output.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func testLogger() (*slog.Logger, *logBuffer) {
	buf := &logBuffer{}
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}
