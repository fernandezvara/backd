package httpapi

import (
	"net/http"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/fernandezvara/backd/internal/registry"
)

// The name a public file's address ends with (design/public-files.md §4.1). The address is read
// by browsers and travels through CDNs, so it is stricter than the name a file is shown under
// (which keeps whatever sanitizeFileName allows): the original name stays in the file's details.

const maxPublicNameBytes = 120

// Error codes of public uploads.
const (
	codeInvalidFileName       = "invalid_file_name"
	codeFileExtensionMismatch = "file_extension_mismatch"
)

// sanitizePublicName makes the name a public file's key ends with: Unicode NFC, only what follows
// the last / or \, letters, digits and combining marks of any script, spaces, and . - _ ( ) +
// (anything else becomes _), blanks collapsed, no spaces or dots at the start, at most 120 bytes
// (cut on a character boundary, keeping the extension). It refuses a name with nothing left, or
// with an extension and no name before it.
func sanitizePublicName(raw string) (string, bool) {
	name := norm.NFC.String(raw)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		switch {
		case r == utf8.RuneError || unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			return -1
		case unicode.IsSpace(r):
			return ' '
		case unicode.IsLetter(r), unicode.IsDigit(r), unicode.IsMark(r):
			return r
		case strings.ContainsRune(".-_()+", r):
			return r
		}
		return '_'
	}, name)
	name = strings.Join(strings.Fields(name), " ")
	if strings.Trim(strings.TrimSuffix(name, path.Ext(name)), ". ") == "" { // nothing before the extension
		return "", false
	}
	name = strings.TrimLeft(name, ". ")
	ext := path.Ext(name)
	stem := strings.TrimRight(strings.TrimSuffix(name, ext), ". ")
	if stem == "" {
		return "", false
	}
	if len(stem)+len(ext) > maxPublicNameBytes {
		cut := max(maxPublicNameBytes-len(ext), 0)
		for cut > 0 && !utf8.RuneStart(stem[cut]) {
			cut--
		}
		stem = strings.TrimRight(stem[:cut], ". ")
		if stem == "" {
			return "", false
		}
	}
	return stem + ext, true
}

// publicExtensions are the extensions each content type a public field takes may carry.
var publicExtensions = map[string][]string{
	"image/jpeg": {".jpg", ".jpeg"},
	"image/png":  {".png"},
}

// publicExtensionMatches reports whether the name's extension is one of the detected type.
func publicExtensionMatches(name, contentType string) bool {
	ext := strings.ToLower(path.Ext(name))
	for _, e := range publicExtensions[contentType] {
		if e == ext {
			return true
		}
	}
	return false
}

// publicNameFor sanitizes the requested name of an upload to a public field; it answers 400
// invalid_file_name and returns false when nothing usable is left.
func publicNameFor(w http.ResponseWriter, r *http.Request, f *registry.FileField, raw string) (string, bool) {
	name, ok := sanitizePublicName(raw)
	if !ok {
		writeError(w, r, http.StatusBadRequest, codeInvalidFileName, "this field is public: the file needs a name with an extension (?name=photo.jpg or a Content-Disposition filename)")
		return "", false
	}
	return name, true
}
