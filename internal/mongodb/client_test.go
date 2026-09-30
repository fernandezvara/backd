package mongodb

import (
	"context"
	"errors"
	"os"
	"testing"
)

// backd refuses a standalone MongoDB (roadmap F1): transactions need a
// replica set.
func TestConnectTopology(t *testing.T) {
	ctx := context.Background()
	if uri := os.Getenv("MONGO_TEST_URI"); uri != "" {
		c, err := Connect(ctx, uri)
		if err != nil {
			t.Fatalf("replica set refused: %v", err)
		}
		_ = c.Disconnect(ctx)
	}
	uri := os.Getenv("MONGO_STANDALONE_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_STANDALONE_TEST_URI not set; skipping the standalone case")
	}
	if _, err := Connect(ctx, uri); !errors.Is(err, ErrStandalone) {
		t.Errorf("standalone server: %v, want ErrStandalone", err)
	}
}
