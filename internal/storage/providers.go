package storage

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// The object storage backd can use for files (roadmap Phase 10). Every
// provider is one entry in the table below: what a provider can do is data in
// one place, read by the code, never probed at run time and never decided by
// comparing provider names. A provider that isn't in the table fails the
// configuration check; adding one is one entry, its setup page and its smoke test.

// Addressing is how a bucket is reached.
type Addressing string

const (
	// VirtualHosted puts the bucket in the host name: <bucket>.<endpoint host>.
	VirtualHosted Addressing = "virtual-hosted"
	// PathStyle puts it in the path: <endpoint>/<bucket>.
	PathStyle Addressing = "path"
)

// Capability says whether a provider supports something, as the smoke test found.
type Capability string

const (
	Supported   Capability = "yes"
	Unsupported Capability = "no"
	// Unverified is documented or expected but not confirmed by the smoke test yet:
	// it is treated as unsupported until it is.
	Unverified Capability = "unverified"
)

// Provider is what backd knows about one S3-compatible storage service.
type Provider struct {
	Name  string // the value of storage.provider
	Label string // for people

	Addressing Addressing

	// EndpointRequired says whether storage.endpoint must be given; without it
	// DefaultEndpoint(region) is used.
	EndpointRequired bool
	DefaultEndpoint  func(region string) string
	// EndpointPattern is what an endpoint (scheme and host, with no path) must
	// match; Group 1, when the entry has RegionFromEndpoint, is the region.
	EndpointPattern *regexp.Regexp
	EndpointHelp    string // the shape, for error messages and the template
	// AllowHTTP lets the endpoint be plain http (development only: startup
	// refuses it unless BACKD_DEV=true).
	AllowHTTP bool

	// Region rules: FixedRegion is the only valid value ("" = free); with
	// RegionFromEndpoint the region is read from the endpoint and must agree with
	// one given; DefaultRegion is used when none is given.
	FixedRegion        string
	RegionFromEndpoint bool
	DefaultRegion      string

	// ChecksumSHA256 is whether the x-amz-checksum-sha256 header is accepted and
	// verified on a PUT, which direct uploads need.
	ChecksumSHA256 Capability
	// MaxSinglePut is the largest object one PUT can carry.
	MaxSinglePut int64
	// ReadsCORS and ReadsEncryption say whether `backd storage check` can read
	// the bucket's CORS rules and default encryption.
	ReadsCORS, ReadsEncryption bool
	// ChecksumsWhenRequired makes the client send request checksums only where an
	// operation needs them: recent AWS SDKs send CRC checksums on every upload,
	// which some S3-compatible services reject.
	ChecksumsWhenRequired bool
	// PublicEndpointAllowed lets a realm set public_endpoint: only for providers
	// whose address differs between backd and browsers (a self-hosted MinIO).
	PublicEndpointAllowed bool
	// OldestTested is the oldest version of a self-hosted provider the tests ran
	// against, stated in the docs ("" for hosted providers).
	OldestTested string
}

const gib = 1 << 30

var providers = []Provider{
	{
		Name: "aws", Label: "AWS S3", Addressing: VirtualHosted,
		DefaultEndpoint: func(region string) string { return "https://s3." + region + ".amazonaws.com" },
		EndpointPattern: regexp.MustCompile(`^https://s3\.([a-z0-9-]+)\.amazonaws\.com$`),
		EndpointHelp:    "https://s3.<region>.amazonaws.com", RegionFromEndpoint: true,
		ChecksumSHA256: Supported, MaxSinglePut: 5 * gib, ReadsCORS: true, ReadsEncryption: true,
	},
	{
		Name: "minio", Label: "MinIO", Addressing: PathStyle, EndpointRequired: true,
		EndpointPattern: regexp.MustCompile(`^https?://[^/?#\s]+$`),
		EndpointHelp:    "https://<host>[:port] (http only for development)", AllowHTTP: true,
		DefaultRegion:  "us-east-1",
		ChecksumSHA256: Supported, MaxSinglePut: 5 * gib, ReadsCORS: false, ReadsEncryption: false,
		ChecksumsWhenRequired: true, PublicEndpointAllowed: true, OldestTested: "RELEASE.2024-01-01T00-00-00Z",
	},
	{
		Name: "r2", Label: "Cloudflare R2", Addressing: VirtualHosted, EndpointRequired: true,
		EndpointPattern: regexp.MustCompile(`^https://[0-9a-f]{32}(\.(?:eu|fedramp))?\.r2\.cloudflarestorage\.com$`),
		EndpointHelp:    "https://<account id>.r2.cloudflarestorage.com", FixedRegion: "auto",
		// Not confirmed yet: the smoke test (internal/storage's R2 test) decides.
		ChecksumSHA256: Unverified, MaxSinglePut: 5 * gib, ChecksumsWhenRequired: true,
	},
	{
		Name: "digitalocean", Label: "DigitalOcean Spaces", Addressing: VirtualHosted,
		DefaultEndpoint: func(region string) string { return "https://" + region + ".digitaloceanspaces.com" },
		EndpointPattern: regexp.MustCompile(`^https://([a-z0-9]+)\.digitaloceanspaces\.com$`),
		EndpointHelp:    "https://<region>.digitaloceanspaces.com", RegionFromEndpoint: true,
		ChecksumSHA256: Supported, MaxSinglePut: 5 * gib, ChecksumsWhenRequired: true,
	},
}

// ProviderNames lists the supported providers, sorted.
func ProviderNames() []string {
	names := make([]string, len(providers))
	for i, p := range providers {
		names[i] = p.Name
	}
	slices.Sort(names)
	return names
}

// LookupProvider returns the table's entry for name.
func LookupProvider(name string) (Provider, bool) {
	for _, p := range providers {
		if p.Name == name {
			return p, true
		}
	}
	return Provider{}, false
}

// SupportsDirectUploads reports whether direct uploads work with this provider:
// they sign a SHA-256 into the PUT, so the provider must have been confirmed to
// verify it.
func (p Provider) SupportsDirectUploads() bool { return p.ChecksumSHA256 == Supported }

// Resolved is the endpoint and region a configuration comes to once the
// provider's rules are applied.
type Resolved struct {
	Endpoint string // no trailing slash
	Region   string
	HTTP     bool // the endpoint is plain http
}

// Resolve applies the provider's endpoint and region rules to what a realm wrote
// and returns what to connect to, or says what is wrong.
func (p Provider) Resolve(endpoint, region string) (Resolved, error) {
	endpoint = strings.TrimSuffix(endpoint, "/")
	if endpoint == "" {
		if p.EndpointRequired {
			return Resolved{}, fmt.Errorf("endpoint: is required for %s (%s)", p.Name, p.EndpointHelp)
		}
		if region == "" {
			return Resolved{}, fmt.Errorf("region: is required for %s (the endpoint is derived from it)", p.Name)
		}
		endpoint = p.DefaultEndpoint(region)
	}
	if u, err := url.Parse(endpoint); err == nil && (u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil) {
		return Resolved{}, fmt.Errorf("endpoint: %q must be only the scheme and host (%s): the bucket goes in `bucket:`", endpoint, p.EndpointHelp)
	}
	m := p.EndpointPattern.FindStringSubmatch(endpoint)
	if m == nil {
		return Resolved{}, fmt.Errorf("endpoint: %q isn't a %s endpoint (want %s)", endpoint, p.Label, p.EndpointHelp)
	}
	r := Resolved{Endpoint: endpoint, HTTP: strings.HasPrefix(endpoint, "http://")}
	if r.HTTP && !p.AllowHTTP {
		return Resolved{}, fmt.Errorf("endpoint: %q must be https for %s", endpoint, p.Name)
	}
	switch {
	case p.FixedRegion != "":
		if region != "" && region != p.FixedRegion {
			return Resolved{}, fmt.Errorf("region: %s's region is always %q, got %q", p.Name, p.FixedRegion, region)
		}
		r.Region = p.FixedRegion
	case p.RegionFromEndpoint:
		from := m[1]
		if region != "" && region != from {
			return Resolved{}, fmt.Errorf("region: %q doesn't match the endpoint's region %q", region, from)
		}
		r.Region = from
	case region != "":
		r.Region = region
	default:
		r.Region = p.DefaultRegion
	}
	return r, nil
}
