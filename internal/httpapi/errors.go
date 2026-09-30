package httpapi

import (
	"encoding/json"
	"net/http"
)

// Error codes returned in the error envelope.
const (
	codeInvalidJSON      = "invalid_json"
	codeValidation       = "validation_error"
	codeInvalidQuery     = "invalid_query"
	codeUnauthenticated  = "unauthenticated"
	codeInvalidCreds     = "invalid_credentials"
	codeForbidden        = "forbidden"
	codeEmailTaken       = "email_taken"
	codeTooManyRequests  = "too_many_requests"
	codeNotFound         = "not_found"
	codeMethodNotAllowed = "method_not_allowed"
	codeConflict         = "conflict"
	codeWriteConflict    = "write_conflict"
	codeVersionMismatch  = "version_mismatch"
	codeInvalidHeader    = "invalid_header"
	codePayloadTooLarge  = "payload_too_large"
	codeUnsupportedMedia = "unsupported_media_type"
	codeInternal         = "internal_error"
	codeUnavailable      = "unavailable"
)

// Detail points at one problem, e.g. a field failing schema validation.
type Detail struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type errorBody struct {
	Error errorPayload `json:"error"`
}

type errorPayload struct {
	Code      string   `json:"code"`
	Message   string   `json:"message"`
	Details   []Detail `json:"details,omitempty"`
	RequestID string   `json:"request_id"`
}

// writeError writes the standard error envelope.
func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string, details ...Detail) {
	writeJSON(w, status, errorBody{Error: errorPayload{
		Code:      code,
		Message:   message,
		Details:   details,
		RequestID: requestID(r.Context()),
	}})
}

// writeJSON writes v as a JSON response. Success payloads are never wrapped.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func notFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusNotFound, codeNotFound, "resource not found")
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusMethodNotAllowed, codeMethodNotAllowed, "method not allowed on this resource")
}
