package storage

import (
	"strings"
	"testing"
)

func TestProviderTable(t *testing.T) {
	if got := strings.Join(ProviderNames(), ","); got != "aws,digitalocean,minio,r2" {
		t.Errorf("providers: %s", got)
	}
	if _, ok := LookupProvider("gcs"); ok {
		t.Error("a provider that is not in the table was found")
	}
	for _, n := range ProviderNames() {
		p, _ := LookupProvider(n)
		if p.Label == "" || p.EndpointPattern == nil || p.EndpointHelp == "" || p.MaxSinglePut <= 0 || p.Addressing == "" {
			t.Errorf("%s: incomplete entry %+v", n, p)
		}
		// Every provider has a way to get an endpoint: it is required, or derived.
		if !p.EndpointRequired && p.DefaultEndpoint == nil {
			t.Errorf("%s: no endpoint and no default", n)
		}
	}
	if p, _ := LookupProvider("r2"); p.SupportsDirectUploads() {
		t.Error("r2's checksum support is unverified until the smoke test confirms it")
	}
	if p, _ := LookupProvider("minio"); !p.SupportsDirectUploads() || !p.PublicEndpointAllowed || p.OldestTested == "" {
		t.Errorf("minio: %+v", p)
	}
	if p, _ := LookupProvider("aws"); p.PublicEndpointAllowed {
		t.Error("only a self-hosted provider has a public endpoint")
	}
}

func TestResolveEndpointAndRegion(t *testing.T) {
	r2acct := "be24f8b836589be5638e95d6579afa46"
	cases := []struct {
		name, provider, endpoint, region string
		want                             Resolved
		err                              string
	}{
		{"aws derived", "aws", "", "eu-west-1", Resolved{Endpoint: "https://s3.eu-west-1.amazonaws.com", Region: "eu-west-1"}, ""},
		{"aws explicit", "aws", "https://s3.us-east-2.amazonaws.com/", "", Resolved{Endpoint: "https://s3.us-east-2.amazonaws.com", Region: "us-east-2"}, ""},
		{"aws region mismatch", "aws", "https://s3.us-east-2.amazonaws.com", "eu-west-1", Resolved{}, `doesn't match the endpoint's region "us-east-2"`},
		{"aws no region", "aws", "", "", Resolved{}, "region: is required for aws"},
		{"aws wrong host", "aws", "https://example.com", "us-east-1", Resolved{}, "isn't a AWS S3 endpoint"},
		{"aws http", "aws", "http://s3.us-east-1.amazonaws.com", "", Resolved{}, "isn't a AWS S3 endpoint"},
		{"minio", "minio", "http://minio:9000", "", Resolved{Endpoint: "http://minio:9000", Region: "us-east-1", HTTP: true}, ""},
		{"minio region", "minio", "https://files.example.com", "eu-1", Resolved{Endpoint: "https://files.example.com", Region: "eu-1"}, ""},
		{"minio needs endpoint", "minio", "", "", Resolved{}, "endpoint: is required for minio"},
		{"minio with a path", "minio", "https://files.example.com/bucket", "", Resolved{}, "the bucket goes in `bucket:`"},
		{"r2", "r2", "https://" + r2acct + ".r2.cloudflarestorage.com", "", Resolved{Endpoint: "https://" + r2acct + ".r2.cloudflarestorage.com", Region: "auto"}, ""},
		{"r2 eu", "r2", "https://" + r2acct + ".eu.r2.cloudflarestorage.com", "auto", Resolved{Endpoint: "https://" + r2acct + ".eu.r2.cloudflarestorage.com", Region: "auto"}, ""},
		{"r2 with the bucket in the path", "r2", "https://" + r2acct + ".r2.cloudflarestorage.com/tests-backd", "", Resolved{}, "the bucket goes in `bucket:`"},
		{"r2 wrong region", "r2", "https://" + r2acct + ".r2.cloudflarestorage.com", "us-east-1", Resolved{}, `always "auto"`},
		{"r2 http", "r2", "http://" + r2acct + ".r2.cloudflarestorage.com", "", Resolved{}, "isn't a Cloudflare R2 endpoint"},
		{"r2 short account", "r2", "https://abc.r2.cloudflarestorage.com", "", Resolved{}, "isn't a Cloudflare R2 endpoint"},
		{"digitalocean", "digitalocean", "", "nyc3", Resolved{Endpoint: "https://nyc3.digitaloceanspaces.com", Region: "nyc3"}, ""},
		{"digitalocean mismatch", "digitalocean", "https://ams3.digitaloceanspaces.com", "nyc3", Resolved{}, "doesn't match"},
	}
	for _, c := range cases {
		p, _ := LookupProvider(c.provider)
		got, err := p.Resolve(c.endpoint, c.region)
		switch {
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s: error %v, want %q", c.name, err, c.err)
		case c.err == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.err == "" && got != c.want:
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}
