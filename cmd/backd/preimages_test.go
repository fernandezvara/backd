package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/xid"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/fernandezvara/backd/internal/mongodb"
)

// provision enables pre-change images on realtime collections, never disables them by
// itself, and --disable-preimages needs --yes before it removes anything.
func TestProvisionPreImagesLifecycle(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set; skipping integration test")
	}
	realm := "t" + xid.New().String()
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, realm, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("realm.yaml", "auth: disabled\n")
	write("app/items/schema.json", `{"type":"object","properties":{"name":{"type":"string"}}}`)
	write("app/items/collection.yaml", "realtime: true\n")
	env := map[string]string{"CONFIG_DIR": root, "MONGO_URI": uri}
	cli := func(args ...string) (int, string) {
		var out, errOut bytes.Buffer
		code := run(args, func(k string) string { return env[k] }, nil, &out, &errOut)
		return code, errOut.String()
	}
	ctx := context.Background()
	client, err := mongodb.Connect(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx) //nolint:errcheck
	t.Cleanup(func() { _ = client.Database(realm + "__app").Drop(ctx) })
	enabled := func() bool {
		t.Helper()
		specs, err := client.Database(realm + "__app").ListCollectionSpecifications(ctx, bson.D{{Key: "name", Value: "items"}})
		if err != nil || len(specs) != 1 {
			t.Fatalf("collection specs: %v %v", specs, err)
		}
		var o struct {
			Images struct {
				Enabled bool `bson:"enabled"`
			} `bson:"changeStreamPreAndPostImages"`
		}
		_ = bson.Unmarshal(specs[0].Options, &o)
		return o.Images.Enabled
	}

	if code, errOut := cli("provision"); code != 0 {
		t.Fatalf("provision: %d %s", code, errOut)
	}
	if !enabled() {
		t.Fatal("provision did not enable the images of the realtime collection")
	}

	// The collection stops being realtime: a plain provision leaves the images alone.
	write("app/items/collection.yaml", "")
	if code, errOut := cli("provision"); code != 0 || !enabled() {
		t.Fatalf("provision: %d %s (images still on: %v)", code, errOut, enabled())
	}

	// --disable-preimages without --yes names what it would remove and changes nothing.
	code, errOut := cli("provision", "--disable-preimages")
	if code != 1 || !strings.Contains(errOut, realm+"__app.items") || !strings.Contains(errOut, "--yes") || !enabled() {
		t.Fatalf("without --yes: %d %q (images on: %v)", code, errOut, enabled())
	}
	if code, errOut := cli("provision", "--disable-preimages", "--yes"); code != 0 || enabled() {
		t.Fatalf("with --yes: %d %s (images on: %v)", code, errOut, enabled())
	}
}
