package httpapi

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	maxFileNameBytes = 255
	sniffBytes       = 512
)

// sanitizeFileName makes a name safe to store and show: normalized (NFC), with no
// path (only what follows the last / or \ ), no control characters, whitespace
// collapsed and at most 255 bytes. It never refuses a name: an empty result is "file".
func sanitizeFileName(name string) string {
	name = norm.NFC.String(name)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		switch {
		case r == utf8.RuneError, unicode.IsControl(r), unicode.Is(unicode.Cf, r), r == 0x2028, r == 0x2029:
			return -1
		case unicode.IsSpace(r):
			return ' '
		}
		return r
	}, name)
	name = strings.Join(strings.Fields(name), " ")
	if len(name) > maxFileNameBytes {
		cut := maxFileNameBytes
		for cut > 0 && !utf8.RuneStart(name[cut]) {
			cut--
		}
		name = strings.TrimSpace(name[:cut])
	}
	if name == "" || name == "." || name == ".." {
		return "file"
	}
	return name
}

// requestedFileName is the name an upload carries: ?name=, or the filename of
// Content-Disposition.
func requestedFileName(r *http.Request) string {
	if n := r.URL.Query().Get("name"); n != "" {
		return n
	}
	if _, params, err := mime.ParseMediaType(r.Header.Get("Content-Disposition")); err == nil {
		return params["filename"]
	}
	return ""
}

// detectContentType decides what a file is from its first bytes, not from what the
// client said. Generic text keeps the type the client claimed when it is a text type
// (a CSV is only plain text to a byte sniffer), and an SVG is recognised by its
// markup; everything else is what the bytes say.
func detectContentType(head []byte, claimed string) string {
	detected := http.DetectContentType(head)
	base, _, _ := strings.Cut(detected, ";")
	claimedBase, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(claimed)), ";")
	claimedBase = strings.TrimSpace(claimedBase)
	switch {
	case base == "text/xml" || base == "text/plain":
		if looksLikeSVG(head) {
			return "image/svg+xml"
		}
		if base == "text/plain" && isTextLike(claimedBase) {
			return claimedBase
		}
	}
	return base
}

func isTextLike(ct string) bool {
	return strings.HasPrefix(ct, "text/") || ct == "application/json" || ct == "application/xml" || ct == "application/x-ndjson" || strings.HasSuffix(ct, "+json") || strings.HasSuffix(ct, "+xml")
}

func looksLikeSVG(head []byte) bool {
	h := bytes.ToLower(bytes.TrimLeft(head, "\xef\xbb\xbf \t\r\n"))
	return bytes.HasPrefix(h, []byte("<svg")) || (bytes.HasPrefix(h, []byte("<?xml")) && bytes.Contains(h, []byte("<svg")))
}

// A backd link to a file is signed with a per-realm key: the HMAC covers what the
// link is for (realm, database, collection, document, field, file) and when it
// expires, so it can't be moved to another file or kept past its time.
func fileLinkMAC(key []byte, realm, database, collection, docID, field, fileID string, exp int64) string {
	m := hmac.New(sha256.New, key)
	for _, part := range []string{"bdl1", realm, database, collection, docID, field, fileID, strconv.FormatInt(exp, 10)} {
		m.Write([]byte(part))
		m.Write([]byte{0})
	}
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// verifyFileLink checks a link's signature in constant time and that it has not expired.
func verifyFileLink(key []byte, realm, database, collection, docID, field, fileID, expStr, sig string, now time.Time) bool {
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || now.Unix() > exp {
		return false
	}
	want := fileLinkMAC(key, realm, database, collection, docID, field, fileID, exp)
	return hmac.Equal([]byte(want), []byte(sig))
}
