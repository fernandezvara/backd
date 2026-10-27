package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// retryAfter sets Retry-After to d in whole seconds, rounded up and at least one.
func retryAfter(w http.ResponseWriter, d time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(max(int((d+time.Second-1)/time.Second), 1)))
}

// parsePositiveLimit reads the limit query parameter, def when it is absent. Anything that is
// not a positive number is answered with 400 and ok is false.
func parsePositiveLimit(w http.ResponseWriter, r *http.Request, def int) (limit int, ok bool) {
	s := r.URL.Query().Get("limit")
	if s == "" {
		return def, true
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		writeError(w, r, http.StatusBadRequest, codeInvalidQuery, "limit must be a positive number")
		return 0, false
	}
	return n, true
}

// strictDecode decodes one JSON value from data into v and refuses fields v doesn't have.
func strictDecode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
