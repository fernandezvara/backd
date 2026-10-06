package storage

import (
	"context"
	"os"
	"testing"
)

// The R2 smoke test (roadmap #148): the one place R2's capabilities are learned.
// It uses real, paid storage, so it is never part of the normal run: it needs
// BACKD_TEST_R2_ENDPOINT (https://<account>.r2.cloudflarestorage.com), _BUCKET,
// _ACCESS_KEY and _SECRET_KEY and skips without them. Run it before a release and
// whenever the provider's entry changes:
//
//	BACKD_TEST_R2_ENDPOINT=… BACKD_TEST_R2_BUCKET=… BACKD_TEST_R2_ACCESS_KEY=… \
//	BACKD_TEST_R2_SECRET_KEY=… go test ./internal/storage -run R2 -v
//
// It makes a handful of tiny requests (a few small objects, deleted again). What
// it finds about the SHA-256 checksum decides the "r2" entry's ChecksumSHA256: on
// 2026-10-06 R2 verified it, so the entry says Supported.
func TestR2SmokeTest(t *testing.T) {
	endpoint, bucket := os.Getenv("BACKD_TEST_R2_ENDPOINT"), os.Getenv("BACKD_TEST_R2_BUCKET")
	access, secret := os.Getenv("BACKD_TEST_R2_ACCESS_KEY"), os.Getenv("BACKD_TEST_R2_SECRET_KEY")
	if endpoint == "" || bucket == "" || access == "" || secret == "" {
		t.Skip("the R2 smoke test needs BACKD_TEST_R2_ENDPOINT, _BUCKET, _ACCESS_KEY and _SECRET_KEY")
	}
	p, _ := LookupProvider("r2")
	res, err := p.Resolve(endpoint, "")
	if err != nil {
		t.Fatalf("the endpoint doesn't fit the r2 entry: %v", err)
	}
	o, err := NewObjects(ObjectsConfig{Provider: "r2", Endpoint: res.Endpoint, Region: res.Region, Bucket: bucket, Prefix: "backd-smoke", AccessKey: access, SecretKey: secret})
	if err != nil {
		t.Fatal(err)
	}
	rep := o.Check(context.Background(), "smoke")
	for _, s := range rep.Steps {
		t.Logf("%-16s %-8s %s", s.Name, s.Level, s.Detail)
	}
	t.Logf("x-amz-checksum-sha256: %s (the entry says %s); encryption: %s; link host: %s", rep.ChecksumSHA256, p.ChecksumSHA256, rep.Encryption, rep.LinkHost)
	// The basics must work whatever R2 does with checksums.
	byName := map[string]string{}
	for _, s := range rep.Steps {
		byName[s.Name] = s.Level
	}
	for _, name := range []string{"bucket", "write", "head", "read", "range"} {
		if byName[name] != CheckOK {
			t.Errorf("step %s: %s", name, byName[name])
		}
	}
	// What R2 does with the signed checksum is the finding; it must be one of the two answers.
	if rep.ChecksumSHA256 != "verified" && rep.ChecksumSHA256 != "ignored" {
		t.Errorf("the checksum test didn't run: %q", rep.ChecksumSHA256)
	}
	if p.ChecksumSHA256 == Supported && rep.ChecksumSHA256 != "verified" {
		t.Error("the r2 entry claims checksum support that R2 does not show")
	}
}
