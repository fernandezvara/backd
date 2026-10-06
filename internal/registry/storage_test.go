package registry

import (
	"os"
	"strings"
	"testing"
	"time"
)

const storageBase = "storage:\n  provider: minio\n  endpoint: http://minio:9000\n  bucket: acme-files\n  prefix: prod\n  access_key: secret:STORAGE_ACCESS_KEY\n  secret_key: secret:STORAGE_SECRET_KEY\n"

func TestStorageSettings(t *testing.T) {
	s, errs := parseRealmSettings([]byte(storageBase + "  public_endpoint: http://localhost:9000/\n  download: proxy\n  presigned_ttl: 7d\n  pending_ttl: 30m\n"))
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	st := s.Storage
	if st == nil || st.Provider != "minio" || st.Endpoint != "http://minio:9000" || st.PublicEndpoint != "http://localhost:9000" || st.Region != "us-east-1" ||
		st.Bucket != "acme-files" || st.Prefix != "prod" || st.AccessKey != "STORAGE_ACCESS_KEY" || st.SecretKey != "STORAGE_SECRET_KEY" ||
		st.Download != DownloadProxy || st.PresignedTTL != 7*24*time.Hour || st.PendingTTL != 30*time.Minute || !st.HTTP {
		t.Errorf("storage = %+v", st)
	}
	if got := strings.Join(st.SecretNames(), ","); got != "STORAGE_ACCESS_KEY,STORAGE_SECRET_KEY" || !st.ProviderEntry().PublicEndpointAllowed {
		t.Errorf("secrets %q, entry %+v", got, st.ProviderEntry())
	}

	// Defaults, and a provider whose endpoint is derived.
	s, errs = parseRealmSettings([]byte("storage:\n  provider: aws\n  region: eu-west-1\n  bucket: acme-files\n  prefix: prod\n  access_key: secret:K\n  secret_key: secret:S\n"))
	if len(errs) != 0 || s.Storage.Endpoint != "https://s3.eu-west-1.amazonaws.com" || s.Storage.Download != DownloadPresigned || s.Storage.PresignedTTL != DefaultPresignedTTL || s.Storage.PendingTTL != DefaultPendingTTL || s.Storage.HTTP {
		t.Errorf("aws: %+v %v", s.Storage, errs)
	}
	s, errs = parseRealmSettings([]byte("storage:\n  provider: r2\n  endpoint: https://be24f8b836589be5638e95d6579afa46.r2.cloudflarestorage.com\n  bucket: tests-backd\n  prefix: ci\n  access_key: secret:K\n  secret_key: secret:S\n"))
	if len(errs) != 0 || s.Storage.Region != "auto" || s.Storage.ProviderEntry().SupportsDirectUploads() {
		t.Errorf("r2: %+v %v", s.Storage, errs)
	}
	// No storage: none.
	if s, _ := parseRealmSettings([]byte("signup: open\n")); s.Storage != nil {
		t.Error("storage appeared from nowhere")
	}
}

func TestStorageSettingsErrors(t *testing.T) {
	good := storageBase
	cases := []struct{ name, yaml, want string }{
		{"no provider", strings.Replace(good, "  provider: minio\n", "", 1), "storage.provider: is required"},
		{"unknown provider", strings.Replace(good, "minio", "gcs", 1), `storage.provider: "gcs" isn't supported`},
		{"no prefix", strings.Replace(good, "  prefix: prod\n", "", 1), "storage.prefix: is required"},
		{"empty prefix", strings.Replace(good, "prefix: prod", `prefix: ""`, 1), "storage.prefix: is required"},
		{"slash in prefix", strings.Replace(good, "prefix: prod", "prefix: a/b", 1), "storage.prefix:"},
		{"no bucket", strings.Replace(good, "  bucket: acme-files\n", "", 1), "storage.bucket:"},
		{"upper-case bucket", strings.Replace(good, "acme-files", "Acme_Files", 1), "storage.bucket:"},
		{"no endpoint for minio", strings.Replace(good, "  endpoint: http://minio:9000\n", "", 1), "storage.endpoint: is required for minio"},
		{"endpoint with the bucket", strings.Replace(good, "9000", "9000/acme-files", 1), "the bucket goes in `bucket:`"},
		{"region mismatch", "storage:\n  provider: aws\n  endpoint: https://s3.us-east-2.amazonaws.com\n  region: eu-west-1\n  bucket: acme-files\n  prefix: p\n  access_key: secret:K\n  secret_key: secret:S\n", "doesn't match the endpoint's region"},
		{"plain key", strings.Replace(good, "secret:STORAGE_ACCESS_KEY", "AKIAXXXX", 1), "access_key"},
		{"no secret key", strings.Replace(good, "  secret_key: secret:STORAGE_SECRET_KEY\n", "", 1), "storage.secret_key: is required"},
		{"same secret", strings.Replace(good, "STORAGE_SECRET_KEY", "STORAGE_ACCESS_KEY", 1), "must be a different secret"},
		{"public endpoint on aws", "storage:\n  provider: aws\n  region: eu-west-1\n  public_endpoint: https://x.example.com\n  bucket: acme-files\n  prefix: p\n  access_key: secret:K\n  secret_key: secret:S\n", "has one address for everyone"},
		{"public endpoint with a path", good + "  public_endpoint: http://localhost:9000/x\n", "public_endpoint:"},
		{"bad download", good + "  download: cdn\n", "storage.download: must be presigned or proxy"},
		{"ttl too long", good + "  presigned_ttl: 8d\n", "storage.presigned_ttl: must be between"},
		{"ttl too short", good + "  presigned_ttl: 1s\n", "storage.presigned_ttl: must be between"},
		{"pending ttl", good + "  pending_ttl: soon\n", "storage.pending_ttl:"},
		{"path_style is not a setting", good + "  path_style: true\n", "path_style"},
		{"sse is not a setting", good + "  sse: AES256\n", "sse"},
		{"auth disabled", "auth: disabled\n" + good, "storage: only applies when auth is enabled"},
	}
	for _, c := range cases {
		_, errs := parseRealmSettings([]byte(c.yaml))
		var all []string
		for _, e := range errs {
			all = append(all, e.Error())
		}
		if !strings.Contains(strings.Join(all, "\n"), c.want) {
			t.Errorf("%s: errors %v, want %q", c.name, all, c.want)
		}
	}
}

// The commented example in `backd template realm` is real configuration: take it
// out of its comments and it has to load.
func TestRealmTemplateStorageExampleLoads(t *testing.T) {
	data, err := os.ReadFile("../templates/files/realm.yaml")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	var block []string
	for i := 0; i < len(lines); i++ {
		if lines[i] != "# storage:" {
			continue
		}
		for _, l := range lines[i:] {
			if l == "#" || l == "" {
				break
			}
			block = append(block, strings.TrimPrefix(strings.TrimPrefix(l, "# "), "#"))
		}
		break
	}
	if len(block) < 8 {
		t.Fatalf("the template has no storage example: %q", block)
	}
	s, errs := parseRealmSettings([]byte(strings.Join(block, "\n")))
	if len(errs) != 0 || s.Storage == nil {
		t.Fatalf("the template's example doesn't load: %v\n%s", errs, strings.Join(block, "\n"))
	}
}
