package registry

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/storage"
)

// Storage defaults and bounds (storage in realm.yaml).
const (
	DefaultPresignedTTL = 5 * time.Minute
	DefaultPendingTTL   = time.Hour
	minPresignedTTL     = 10 * time.Second
	maxPresignedTTL     = 7 * 24 * time.Hour // what a signed link can last on S3-compatible storage
	minPendingTTL       = time.Minute
	maxPendingTTL       = 7 * 24 * time.Hour

	// Download modes.
	DownloadPresigned = "presigned"
	DownloadProxy     = "proxy"

	secretPrefix = "secret:"
)

var (
	bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	prefixPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	hostPortRe    = regexp.MustCompile(`^https?://[^/?#\s]+$`)
)

// storageDoc mirrors the storage section of realm.yaml. There is no path_style
// (the provider decides) and no sse (encryption at rest is the bucket's default).
type storageDoc struct {
	Provider       string `yaml:"provider"`
	Endpoint       string `yaml:"endpoint"`
	PublicEndpoint string `yaml:"public_endpoint"`
	Region         string `yaml:"region"`
	Bucket         string `yaml:"bucket"`
	Prefix         string `yaml:"prefix"`
	AccessKey      string `yaml:"access_key"`
	SecretKey      string `yaml:"secret_key"`
	Download       string `yaml:"download"`
	PresignedTTL   string `yaml:"presigned_ttl"`
	PendingTTL     string `yaml:"pending_ttl"`
	Quota          *struct {
		Realm string `yaml:"realm"`
		User  string `yaml:"user"`
	} `yaml:"quota"`
}

// StorageSettings is where a realm keeps its files: its own S3-compatible
// bucket on one of the supported providers. backd has no storage of its own.
type StorageSettings struct {
	Provider string // storage.Provider's Name; LookupProvider gives its capabilities
	// Endpoint and Region are what backd connects to, as the provider's rules
	// resolve them; PublicEndpoint (providers that allow it) is only signed into
	// links for browsers and never connected to.
	Endpoint       string
	PublicEndpoint string
	Region         string
	Bucket         string
	// Prefix starts every object key of this instance (<prefix>/<realm>/…): each
	// backd instance sharing a bucket needs its own.
	Prefix string
	// AccessKey and SecretKey are the names of secrets of the realm's encrypted
	// store (storage.access_key: secret:NAME), never values.
	AccessKey, SecretKey string
	Download             string // DownloadPresigned or DownloadProxy: the default of the realm's fields
	PresignedTTL         time.Duration
	PendingTTL           time.Duration
	// QuotaRealm and QuotaUser are the most bytes the files of all documents of the realm, and
	// of the documents one user owns, may add up to; 0 is no limit. An upload that would pass
	// either answers 413 quota_exceeded.
	QuotaRealm, QuotaUser int64
	// HTTP is true when Endpoint or PublicEndpoint is plain http, which startup
	// allows only in development.
	HTTP bool
}

// ProviderEntry returns the provider's capabilities.
func (s *StorageSettings) ProviderEntry() storage.Provider {
	p, _ := storage.LookupProvider(s.Provider)
	return p
}

// SecretNames are the realm secrets the storage needs.
func (s *StorageSettings) SecretNames() []string { return []string{s.AccessKey, s.SecretKey} }

func parseStorage(d *storageDoc) (*StorageSettings, []error) {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf("storage."+format, args...)) }
	st := &StorageSettings{
		Provider: d.Provider, Bucket: d.Bucket, Prefix: d.Prefix,
		Download: DownloadPresigned, PresignedTTL: DefaultPresignedTTL, PendingTTL: DefaultPendingTTL,
	}

	p, ok := storage.LookupProvider(d.Provider)
	switch {
	case d.Provider == "":
		add("provider: is required, one of %s", strings.Join(storage.ProviderNames(), ", "))
	case !ok:
		add("provider: %q isn't supported (want %s); a new provider is one entry in internal/storage's table", d.Provider, strings.Join(storage.ProviderNames(), ", "))
	default:
		r, err := p.Resolve(d.Endpoint, d.Region)
		if err != nil {
			errs = append(errs, errors.New("storage."+err.Error()))
		} else {
			st.Endpoint, st.Region, st.HTTP = r.Endpoint, r.Region, r.HTTP
		}
		if d.PublicEndpoint != "" {
			switch {
			case !p.PublicEndpointAllowed:
				add("public_endpoint: %s has one address for everyone; only a self-hosted provider (minio) can have a different address for browsers", d.Provider)
			case !hostPortRe.MatchString(strings.TrimSuffix(d.PublicEndpoint, "/")):
				add("public_endpoint: %q must be only the scheme and host (https://<host>[:port], http only for development)", d.PublicEndpoint)
			default:
				st.PublicEndpoint = strings.TrimSuffix(d.PublicEndpoint, "/")
				if strings.HasPrefix(st.PublicEndpoint, "http://") {
					st.HTTP = true
				}
			}
		}
	}

	if !bucketPattern.MatchString(d.Bucket) {
		add("bucket: %q isn't a valid bucket name (3 to 63 lower-case letters, digits, dots and hyphens)", d.Bucket)
	}
	if d.Prefix == "" {
		add("prefix: is required: it starts every object key, and each backd instance sharing the bucket (prod, staging…) needs its own")
	} else if !prefixPattern.MatchString(d.Prefix) {
		add("prefix: %q must be one name of lower-case letters, digits, dots, hyphens and underscores (up to 63), such as prod", d.Prefix)
	}

	for _, k := range []struct {
		field, value string
		dst          *string
	}{{"access_key", d.AccessKey, &st.AccessKey}, {"secret_key", d.SecretKey, &st.SecretKey}} {
		name, found := strings.CutPrefix(k.value, secretPrefix)
		switch {
		case k.value == "":
			add("%s: is required, written secret:NAME (a secret of the realm: backd secret set)", k.field)
		case !found || !ValidSecretName(name):
			add("%s: %q must be secret:NAME with NAME in upper case letters, digits and _ (the key itself never goes in realm.yaml)", k.field, k.value)
		default:
			*k.dst = name
		}
	}
	if st.AccessKey != "" && st.AccessKey == st.SecretKey {
		add("secret_key: must be a different secret from access_key")
	}

	if d.Download != "" {
		if d.Download != DownloadPresigned && d.Download != DownloadProxy {
			add("download: must be %s or %s, got %q", DownloadPresigned, DownloadProxy, d.Download)
		} else {
			st.Download = d.Download
		}
	}
	for _, t := range []struct {
		field, value string
		dst          *time.Duration
		min, max     time.Duration
	}{
		{"presigned_ttl", d.PresignedTTL, &st.PresignedTTL, minPresignedTTL, maxPresignedTTL},
		{"pending_ttl", d.PendingTTL, &st.PendingTTL, minPendingTTL, maxPendingTTL},
	} {
		if t.value == "" {
			continue
		}
		v, err := ParseDuration(t.value)
		switch {
		case err != nil:
			add("%s: %v", t.field, err)
		case v < t.min || v > t.max:
			add("%s: must be between %s and %s, got %s", t.field, t.min, t.max, t.value)
		default:
			*t.dst = v
		}
	}
	if d.Quota != nil {
		for _, q := range []struct {
			field, value string
			dst          *int64
		}{{"quota.realm", d.Quota.Realm, &st.QuotaRealm}, {"quota.user", d.Quota.User, &st.QuotaUser}} {
			if q.value == "" {
				continue
			}
			v, err := ParseSize(q.value)
			switch {
			case err != nil:
				add("%s: %v", q.field, err)
			case v < 1:
				add("%s: must be at least 1B (leave it out for no limit)", q.field)
			default:
				*q.dst = v
			}
		}
		if st.QuotaRealm > 0 && st.QuotaUser > st.QuotaRealm {
			add("quota.user: a user's quota can't be larger than the realm's")
		}
	}
	return st, errs
}
