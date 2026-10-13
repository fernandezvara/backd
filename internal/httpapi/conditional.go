package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// Conditional GET (design/conditional-get.md): reads carry an ETag, and a
// request whose If-None-Match names it answers 304 without a body. The read
// rule has already run by then, so a caller that lost access gets 404, never
// 304.

// representationTag is the suffix a document's ETag gains when the caller's
// view differs from the full document (hidden or selected fields): ".r" plus
// the first 8 hex characters of SHA-256 over the sorted field names. It is
// empty when the caller sees the whole document.
func representationTag(fields []string) string {
	if len(fields) == 0 {
		return ""
	}
	sorted := append([]string(nil), fields...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return ".r" + hex.EncodeToString(sum[:])[:8]
}

// docETag is a document's strong ETag: its version, plus the representation
// tag when the view is partial.
func docETag(version int64, representation string) string {
	return `"` + strconv.FormatInt(version, 10) + representation + `"`
}

// noneMatch reports whether an If-None-Match header lists tag (or "*"). The
// comparison is weak, as RFC 9110 requires for If-None-Match: W/"12" matches
// "12".
func noneMatch(values []string, tag string) bool {
	want := strings.TrimPrefix(tag, "W/")
	for _, v := range values {
		for _, t := range strings.Split(v, ",") {
			t = strings.TrimSpace(t)
			if t == "*" || (t != "" && strings.TrimPrefix(t, "W/") == want) {
				return true
			}
		}
	}
	return false
}

// readHeaders sets the validator of a read. Cache-Control private, no-cache
// and Vary come from dataCaching, so browsers and proxies revalidate each time.
func readHeaders(w http.ResponseWriter, tag string) {
	w.Header().Set("ETag", tag)
}

// notModified answers 304 when the request's If-None-Match names tag.
func notModified(w http.ResponseWriter, r *http.Request, tag string) bool {
	if !noneMatch(r.Header.Values("If-None-Match"), tag) {
		return false
	}
	w.WriteHeader(http.StatusNotModified)
	return true
}

// listETag is the weak validator of a list page: a hash over every returned
// document's id, version and representation tag, then has_more, the next
// cursor and the total when they are included. Any document added, removed,
// edited, hidden or revealed for the caller changes it.
func listETag(items []map[string]any, representation string, hasMore bool, cursor string, total *int64) string {
	h := sha256.New()
	for _, doc := range items {
		id, _ := doc["id"].(string)
		h.Write([]byte(id))
		h.Write([]byte{0})
		h.Write([]byte(strconv.FormatInt(version(doc), 10)))
		h.Write([]byte(representation))
		h.Write([]byte{'\n'})
	}
	h.Write([]byte("more=" + strconv.FormatBool(hasMore) + "\ncursor=" + cursor))
	if total != nil {
		h.Write([]byte("\ntotal=" + strconv.FormatInt(*total, 10)))
	}
	return `W/"` + hex.EncodeToString(h.Sum(nil))[:32] + `"`
}
