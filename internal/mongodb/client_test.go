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

func TestDebugServerMonitor(t *testing.T) {
	t.Setenv("BACKD_MONGO_DEBUG", "")
	if debugServerMonitor() != nil {
		t.Error("unset: want nil")
	}
	t.Setenv("BACKD_MONGO_DEBUG", "0")
	if debugServerMonitor() != nil {
		t.Error("0: want nil")
	}
	for _, v := range []string{"1", "true", "TRUE"} {
		t.Setenv("BACKD_MONGO_DEBUG", v)
		if debugServerMonitor() == nil {
			t.Errorf("%s: want a monitor", v)
		}
	}
}
