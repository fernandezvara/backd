package registry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/fernandezvara/backd/internal/imaging"
)

// File fields (roadmap Phase 10): a collection declares which fields of its
// documents carry files in collection.yaml's `files:`. backd owns what a file
// field holds (the file's details), injects its schema into the collection's
// validators, and never lets a write set it.

// Upload modes.
const (
	UploadProxy  = "proxy"  // streamed through backd
	UploadDirect = "direct" // a signed PUT straight to the bucket
)

const (
	maxFilesPerField   = 1000
	defaultMaxFiles    = 10
	maxDirectFileSize  = 5 << 30 // the single signed PUT limit
	maxFilePresignedTL = 7 * 24 * time.Hour
)

// FileField is one file field of a collection, from collection.yaml.
type FileField struct {
	Name     string
	Multiple bool  // an array of files; otherwise at most one
	MaxFiles int   // for a multiple field
	MaxSize  int64 // bytes, per file
	// Types are the allowed content types, detected from the bytes (never the
	// client's claim): "image/png", or "image/*" for a family. Empty allows any.
	Types []string
	// Upload is UploadProxy or UploadDirect.
	Upload string
	// Download is DownloadPresigned or DownloadProxy; "" follows the realm's.
	Download string
	// PresignedTTL is how long this field's links last; 0 follows the realm's.
	PresignedTTL time.Duration
	// Cache is the Cache-Control max-age of proxied downloads of anonymously
	// readable documents; 0 means no caching.
	Cache time.Duration
	// Versions are the resized copies of an image the field declares, by name order.
	Versions []Version
	// MaxPixels lowers the instance's image pixel limit for this field; 0 follows it.
	MaxPixels int64
	// KeepMetadata stores the JPEG and PNG pictures of the field as they were sent. By default
	// their metadata (EXIF with the GPS position, XMP, comments, text chunks) is removed.
	KeepMetadata bool
}

// Version is one declared version of an image field (image versions, milestone 11).
type Version struct {
	Name string
	// Params are what workers make it with; zero (no max_width or max_height) for a
	// version only functions make.
	Params imaging.Params
	// Writable lets functions make the version (with other parameters, too).
	Writable bool
}

// HasParams reports whether workers make the version: it has a box to fit.
func (v Version) HasParams() bool { return v.Params.MaxWidth > 0 || v.Params.MaxHeight > 0 }

// Fingerprint identifies what a version is made with, so that a changed declaration can be
// told from the copies made before it. Defaults are written out, so spelling a default
// doesn't change it.
func (v Version) Fingerprint() string { return ParamsFingerprint(v.Params) }

// ParamsFingerprint is Fingerprint for parameters that aren't a declared version's, such as
// the ones a function generates with.
func ParamsFingerprint(p imaging.Params) string {
	fit := string(p.Fit)
	if fit == "" {
		fit = string(imaging.Contain)
	}
	quality := p.Quality
	if quality == 0 {
		quality = imaging.DefaultQuality
	}
	data, _ := json.Marshal([]any{p.MaxWidth, p.MaxHeight, fit, quality, string(p.Format), p.Upscale})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// ValidateVersionParams reports what is wrong with parameters given at run time (by a
// function), with the same rules `versions:` is parsed with.
func ValidateVersionParams(p imaging.Params) []string {
	var out []string
	for _, dim := range []struct {
		name string
		v    int
	}{{"max_width", p.MaxWidth}, {"max_height", p.MaxHeight}} {
		if dim.v < 0 || dim.v > maxVersionDimension {
			out = append(out, fmt.Sprintf("%s: must be between 1 and %d pixels, got %d", dim.name, maxVersionDimension, dim.v))
		}
	}
	if len(out) > 0 {
		return out
	}
	if err := p.Validate(); err != nil {
		return []string{err.Error()}
	}
	if p.Fit == imaging.Cover && (p.MaxWidth == 0 || p.MaxHeight == 0) {
		out = append(out, "fit: cover needs both max_width and max_height")
	}
	if p.Quality != 0 && p.Format == imaging.PNG {
		out = append(out, "quality: only applies to format jpeg")
	}
	return out
}

// Version returns the declared version with the name.
func (f *FileField) Version(name string) (Version, bool) {
	for _, v := range f.Versions {
		if v.Name == name {
			return v, true
		}
	}
	return Version{}, false
}

// versionDoc mirrors one entry of a field's `versions:`.
type versionDoc struct {
	MaxWidth  *int   `yaml:"max_width"`
	MaxHeight *int   `yaml:"max_height"`
	Fit       string `yaml:"fit"`
	Quality   *int   `yaml:"quality"`
	Format    string `yaml:"format"`
	Upscale   bool   `yaml:"upscale"`
	Writable  bool   `yaml:"writable"`
}

// fileFieldDoc mirrors one entry of `files:`.
type fileFieldDoc struct {
	Multiple     bool                   `yaml:"multiple"`
	MaxFiles     *int                   `yaml:"max_files"`
	MaxSize      string                 `yaml:"max_size"`
	Types        []string               `yaml:"types"`
	Upload       string                 `yaml:"upload"`
	Download     string                 `yaml:"download"`
	PresignedTTL string                 `yaml:"presigned_ttl"`
	Cache        string                 `yaml:"cache"`
	MaxPixels    *int64                 `yaml:"max_pixels"`
	KeepMetadata bool                   `yaml:"keep_metadata"`
	Versions     map[string]*versionDoc `yaml:"versions"`
}

var (
	fileFieldName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	fileTypeRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]*/(\*|[a-z0-9][a-z0-9!#$&^_.+-]*)$`)
)

// activeTypes are the content types a browser may run something from. A family such
// as `image/*` or `text/*` never admits them: a field takes one only when it is named
// in `types`, so allowing images doesn't quietly allow SVG with scripts in it.
var activeTypes = map[string]bool{
	"image/svg+xml": true, "text/html": true, "application/xhtml+xml": true,
	"text/xml": true, "application/xml": true, "text/javascript": true, "application/javascript": true,
}

// Allows reports whether a detected content type may be uploaded to the field.
func (f *FileField) Allows(contentType string) bool {
	if len(f.Types) == 0 {
		return true
	}
	ct, _, _ := strings.Cut(strings.ToLower(contentType), ";")
	ct = strings.TrimSpace(ct)
	for _, t := range f.Types {
		if t == ct {
			return true
		}
		if family, ok := strings.CutSuffix(t, "/*"); ok && strings.HasPrefix(ct, family+"/") && !activeTypes[ct] {
			return true
		}
	}
	return false
}

// parseFiles reads the `files:` section of collection.yaml, validated against the
// collection's schema and the realm's storage. A collection without the file or
// the section has no file fields.
func parseFiles(path string, schema map[string]any, settings RealmSettings) (map[string]*FileField, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var doc struct {
		Files map[string]*fileFieldDoc `yaml:"files"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(data)) // the other sections are read (and checked) elsewhere
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: invalid YAML: %w", path, err)
	}
	if len(doc.Files) == 0 {
		return nil, nil
	}
	var errs []error
	add := func(name, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: files.%s: "+format, append([]any{path, name}, args...)...))
	}
	if settings.Storage == nil {
		return nil, fmt.Errorf("%s: files: a collection with file fields needs `storage:` in the realm's realm.yaml (and `backd template realm` shows how)", path)
	}
	props, _ := schema["properties"].(map[string]any)
	out := map[string]*FileField{}
	for _, name := range slices.Sorted(maps.Keys(doc.Files)) {
		d := doc.Files[name]
		if d == nil {
			d = &fileFieldDoc{}
		}
		f := &FileField{Name: name, Multiple: d.Multiple, Upload: UploadProxy}
		switch {
		case !fileFieldName.MatchString(name):
			add(name, "the name must be lower-case letters, digits and underscores, starting with a letter")
			continue
		case isReserved(name):
			add(name, "is a system field")
			continue
		}
		if _, ok := props[name]; ok {
			add(name, "is also declared in schema.json: a file field is declared only under `files:` (backd adds its schema), so remove it from schema.json")
		}

		if d.MaxSize == "" {
			add(name, "max_size: is required, such as 10MiB: the largest one file may be")
		} else if n, err := ParseSize(d.MaxSize); err != nil {
			add(name, "max_size: %v", err)
		} else if n < 1 {
			add(name, "max_size: must be at least 1B")
		} else {
			f.MaxSize = n
		}

		if d.Multiple {
			f.MaxFiles = defaultMaxFiles
			if d.MaxFiles != nil {
				f.MaxFiles = *d.MaxFiles
			}
			if f.MaxFiles < 1 || f.MaxFiles > maxFilesPerField {
				add(name, "max_files: must be between 1 and %d, got %d", maxFilesPerField, f.MaxFiles)
			}
		} else if d.MaxFiles != nil {
			add(name, "max_files: only applies to a field with `multiple: true`")
		}

		for i, t := range d.Types {
			t = strings.ToLower(strings.TrimSpace(t))
			if !fileTypeRe.MatchString(t) {
				add(name, "types[%d]: %q must be a content type such as image/png, or a family such as image/*", i, d.Types[i])
				continue
			}
			f.Types = append(f.Types, t)
		}

		switch d.Upload {
		case "", UploadProxy:
		case UploadDirect:
			switch {
			case !settings.Storage.ProviderEntry().SupportsDirectUploads():
				add(name, "upload: direct needs a provider that verifies a signed SHA-256, which %s doesn't (use proxy)", settings.Storage.Provider)
			case f.MaxSize > maxDirectFileSize:
				add(name, "max_size: a direct upload is one signed PUT, at most 5GiB")
			default:
				f.Upload = UploadDirect
			}
		default:
			add(name, "upload: must be %s or %s, got %q", UploadProxy, UploadDirect, d.Upload)
		}

		switch d.Download {
		case "":
		case DownloadPresigned, DownloadProxy:
			f.Download = d.Download
		default:
			add(name, "download: must be %s or %s, got %q", DownloadPresigned, DownloadProxy, d.Download)
		}
		if d.PresignedTTL != "" {
			if v, err := ParseDuration(d.PresignedTTL); err != nil {
				add(name, "presigned_ttl: %v", err)
			} else if v < minPresignedTTL || v > maxFilePresignedTL {
				add(name, "presigned_ttl: must be between %s and %s, got %s", minPresignedTTL, maxFilePresignedTL, d.PresignedTTL)
			} else {
				f.PresignedTTL = v
			}
		}
		if d.Cache != "" {
			if v, err := ParseDuration(d.Cache); err != nil {
				add(name, "cache: %v", err)
			} else if v <= 0 {
				add(name, "cache: must be a positive duration, such as 1h")
			} else if eff := firstNonEmpty(f.Download, settings.Storage.Download); eff != DownloadProxy {
				add(name, "cache: only applies to proxied downloads (`download: proxy`, on the field or in the realm's storage)")
			} else {
				f.Cache = v
			}
		}
		if d.MaxPixels != nil {
			if *d.MaxPixels < 1 {
				add(name, "max_pixels: must be a positive number of pixels (width × height)")
			} else {
				f.MaxPixels = *d.MaxPixels
			}
		}
		f.KeepMetadata = d.KeepMetadata
		versions, verrs := parseVersions(d)
		for _, e := range verrs {
			add(name, "%s", e)
		}
		f.Versions = versions
		out[name] = f
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

var versionName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// parseVersions reads a field's `versions:`: each needs parameters (a box) or
// `writable: true`, and what it names must be something the image engine can do.
func parseVersions(d *fileFieldDoc) ([]Version, []string) {
	if len(d.Versions) == 0 {
		return nil, nil
	}
	var errs []string
	var out []Version
	for _, vn := range slices.Sorted(maps.Keys(d.Versions)) {
		vd := d.Versions[vn]
		if vd == nil {
			vd = &versionDoc{}
		}
		bad := func(format string, args ...any) {
			errs = append(errs, fmt.Sprintf("versions.%s: "+format, append([]any{vn}, args...)...))
		}
		if !versionName.MatchString(vn) {
			bad("the name must be lower-case letters, digits, hyphens and underscores, starting with a letter (32 at most)")
			continue
		}
		v := Version{Name: vn, Writable: vd.Writable}
		for _, dim := range []struct {
			name string
			in   *int
			set  *int
		}{{"max_width", vd.MaxWidth, &v.Params.MaxWidth}, {"max_height", vd.MaxHeight, &v.Params.MaxHeight}} {
			if dim.in == nil {
				continue
			}
			if *dim.in < 1 || *dim.in > maxVersionDimension {
				bad("%s: must be between 1 and %d pixels, got %d", dim.name, maxVersionDimension, *dim.in)
				continue
			}
			*dim.set = *dim.in
		}
		switch imaging.Fit(vd.Fit) {
		case "", imaging.Contain, imaging.Cover, imaging.Stretch:
			v.Params.Fit = imaging.Fit(vd.Fit)
		default:
			bad("fit: must be contain, cover or stretch, got %q", vd.Fit)
		}
		switch imaging.Format(vd.Format) {
		case "", imaging.JPEG, imaging.PNG:
			v.Params.Format = imaging.Format(vd.Format)
		case "webp":
			bad("format: webp can't be written (no pure-Go encoder makes lossy WebP); use jpeg or png")
		default:
			bad("format: must be jpeg or png, got %q", vd.Format)
		}
		if vd.Quality != nil {
			if *vd.Quality < 1 || *vd.Quality > 100 {
				bad("quality: must be between 1 and 100, got %d", *vd.Quality)
			} else if v.Params.Format == imaging.PNG {
				bad("quality: only applies to format jpeg")
			} else {
				v.Params.Quality = *vd.Quality
			}
		}
		v.Params.Upscale = vd.Upscale
		hasBox := vd.MaxWidth != nil || vd.MaxHeight != nil
		switch {
		case !hasBox && !v.Writable:
			bad("needs max_width or max_height (workers make it), or writable: true (only functions make it)")
		case !hasBox && (vd.Fit != "" || vd.Quality != nil || vd.Format != "" || vd.Upscale):
			bad("fit, quality, format and upscale need max_width or max_height")
		case v.Params.Fit == imaging.Cover && (vd.MaxWidth == nil || vd.MaxHeight == nil):
			bad("fit: cover needs both max_width and max_height")
		}
		out = append(out, v)
	}
	return out, errs
}

// maxVersionDimension bounds a version's box; larger than any useful derived copy.
const maxVersionDimension = 16384

// CheckImageLimits reports the fields whose `max_pixels` is above the instance's
// limit (BACKD_IMAGE_MAX_PIXELS): a field may only lower it.
func (r *Registry) CheckImageLimits(instance int64) error {
	var errs []error
	for _, db := range r.Databases() {
		for _, c := range db.SortedCollections() {
			for _, name := range slices.Sorted(maps.Keys(c.Files)) {
				if f := c.Files[name]; f.MaxPixels > instance {
					errs = append(errs, fmt.Errorf("%s/%s/%s: files.%s: max_pixels: %d is above this instance's limit of %d (BACKD_IMAGE_MAX_PIXELS); a field can only lower it", c.Realm, c.Database, c.Name, name, f.MaxPixels, instance))
				}
			}
		}
	}
	return errors.Join(errs...)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// fileDetailsSchema is what a file field holds, which backd writes and no client
// may: the file's id, its sanitized name, size, detected type, SHA-256 and upload time.
func fileDetailsSchema() map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":          map[string]any{"type": "string", "pattern": "^fl_[0-9a-z]{20}$"},
			"name":        str(),
			"size":        map[string]any{"type": "integer"},
			"type":        str(),
			"sha256":      map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
			"uploaded_at": map[string]any{"type": "string", "format": "date-time"},
			// The pixel size of an image original, and the state of each declared version.
			"width":    map[string]any{"type": "integer"},
			"height":   map[string]any{"type": "integer"},
			"versions": map[string]any{"type": "object", "additionalProperties": versionStateSchema()},
		},
		"required":             []any{"id", "name", "size", "type", "sha256", "uploaded_at"},
		"additionalProperties": false,
	}
}

// versionStateSchema is what a document records of one version: its status, and
// once made, the copy's details.
func versionStateSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"status":       map[string]any{"enum": []any{"pending", "ready", "empty", "skipped", "failed"}},
			"reason":       map[string]any{"type": "string"},
			"id":           map[string]any{"type": "string", "pattern": "^fv_[0-9a-z]{20}$"},
			"size":         map[string]any{"type": "integer"},
			"type":         map[string]any{"type": "string"},
			"width":        map[string]any{"type": "integer"},
			"height":       map[string]any{"type": "integer"},
			"params":       map[string]any{"type": "object"},
			"fingerprint":  map[string]any{"type": "string"},
			"custom":       map[string]any{"type": "boolean"}, // made with parameters a function gave
			"generated_at": map[string]any{"type": "string", "format": "date-time"},
		},
		"required":             []any{"status"},
		"additionalProperties": false,
	}
}

// withFileSchemas returns a copy of the collection's schema with each file
// field's schema added: the details object, or an array of them (at most
// max_files). The author's schema.json is not modified.
func withFileSchemas(schema map[string]any, files map[string]*FileField) map[string]any {
	out := cloneSchema(schema).(map[string]any)
	props, _ := out["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
		out["properties"] = props
	}
	for name, f := range files {
		if f.Multiple {
			props[name] = map[string]any{"type": "array", "items": fileDetailsSchema(), "maxItems": json.Number(strconv.Itoa(f.MaxFiles))}
		} else {
			props[name] = fileDetailsSchema()
		}
	}
	return out
}

func cloneSchema(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, e := range t {
			m[k] = cloneSchema(e)
		}
		return m
	case []any:
		s := make([]any, len(t))
		for i, e := range t {
			s[i] = cloneSchema(e)
		}
		return s
	}
	return v
}
