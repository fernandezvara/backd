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

func TestCheckPublicAgainstTheFakeServer(t *testing.T) {
	srv := storagetest.New(t, "public")
	o, err := NewObjects(ObjectsConfig{Provider: "minio", Endpoint: srv.URL, Region: "us-east-1", Bucket: "public", Prefix: "p", AccessKey: "k", SecretKey: "s"})
	if err != nil {
		t.Fatal(err)
	}
	rep := o.CheckPublic(context.Background(), "acme", "https://cdn.example.com/", PublicOriginalHeaders)
	if !rep.OK() {
		t.Fatalf("report: %+v", rep.Steps)
	}
	if want := "https://cdn.example.com/p/acme/_check/backd-check.png"; rep.SentinelURL != want {
		t.Errorf("sentinel url = %q, want %q", rep.SentinelURL, want)
	}
	// The sentinel stays, so the operator can open it; the next check replaces it.
	if keys := srv.Keys(); len(keys) != 1 || keys[0] != "p/acme/_check/backd-check.png" {
		t.Errorf("left behind: %v", keys)
	}
	info, err := o.Head(context.Background(), o.PublicSentinelKey("acme"))
	if err != nil || info.CacheControl != PublicOriginalHeaders.CacheControl || info.ContentDisposition != "inline" || info.ContentType != "image/png" {
		t.Errorf("sentinel = %+v, %v", info, err)
	}
	if rep := o.CheckPublic(context.Background(), "acme", "https://cdn.example.com", PublicOriginalHeaders); !rep.OK() {
		t.Errorf("second check: %+v", rep.Steps)
	}
	// Headers the storage doesn't keep fail the check.
	if rep := o.CheckPublic(context.Background(), "acme", "https://cdn.example.com", Headers{CacheControl: "no-store"}); !rep.OK() {
		t.Errorf("other headers are stored too: %+v", rep.Steps)
	}
}
