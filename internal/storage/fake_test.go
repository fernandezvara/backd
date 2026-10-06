package storage

import (
	"context"
	"testing"

	"github.com/fernandezvara/backd/internal/storage/storagetest"
)

// The same check, hermetic: against the in-memory server, so it runs everywhere.
func TestCheckAgainstTheFakeServer(t *testing.T) {
	srv := storagetest.New(t, "files")
	o, err := NewObjects(ObjectsConfig{Provider: "minio", Endpoint: srv.URL, Region: "us-east-1", Bucket: "files", Prefix: "p", AccessKey: "k", SecretKey: "s"})
	if err != nil {
		t.Fatal(err)
	}
	rep := o.Check(context.Background(), "acme")
	if !rep.OK() || rep.ChecksumSHA256 != "verified" {
		t.Fatalf("report: %+v", rep.Steps)
	}
	if keys := srv.Keys(); len(keys) != 0 {
		t.Errorf("left behind: %v", keys)
	}
	// A bucket that isn't there.
	o, _ = NewObjects(ObjectsConfig{Provider: "minio", Endpoint: srv.URL, Region: "us-east-1", Bucket: "nope", Prefix: "p", AccessKey: "k", SecretKey: "s"})
	if rep := o.Check(context.Background(), "acme"); rep.OK() || rep.Steps[0].Level != CheckFail {
		t.Errorf("a missing bucket: %+v", rep.Steps)
	}
}
