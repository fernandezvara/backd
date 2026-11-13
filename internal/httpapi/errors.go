package httpapi

import (
	"encoding/json"
	"net/http"
)

// Error codes returned in the error envelope.
const (
	codeInvalidJSON       = "invalid_json"
	codeValidation        = "validation_error"
	codeInvalidQuery      = "invalid_query"
	codeFieldEncrypted    = "field_encrypted"
	codeInvalidPath       = "invalid_path"
	codeUnauthenticated   = "unauthenticated"
	codeSessionUnscoped   = "session_unscoped"
	codeSSORequired       = "sso_required"
	codeSSOSession        = "sso_session"
	codeSSONotReady       = "sso_not_ready"
	codeInvalidCreds      = "invalid_credentials"
	codePasswordExpired   = "password_expired"
	codeForbidden         = "forbidden"
	codeEmailTaken        = "email_taken"
	codeTooManyRequests   = "too_many_requests"
	codeNotFound          = "not_found"
	codeMethodNotAllowed  = "method_not_allowed"
	codeConflict          = "conflict"
	codeInvalidLocale     = "invalid_locale"
	codePushNotConfigured = "push_not_configured"
	codePlatformDisabled  = "platform_not_enabled"
	codeInvalidDevice     = "invalid_device"
	codePushUnavailable   = "push_unavailable"
	codeLastMethod        = "last_sign_in_method"
	codeProviderUnavail   = "provider_unavailable"
	codeInvalidToken      = "invalid_token"
	codeEmailNotDeclared  = "email_not_declared"
	codeAlreadyErased     = "already_erased"
	codeUserErased        = "user_erased"
	codeEmailLimited      = "email_limited"
	codeEmailNotVerified  = "email_not_verified"
	codeConsentRequired   = "consent_required"
	codeInvalidRedirect   = "invalid_redirect"
	// codeStrategyRequired and codeUnknownStrategy answer a sign-in that names no strategy when
	// several start with its method, and one that names a strategy that does not apply.
	codePasskeyDisabled  = "passkey_disabled"
	codePasskeyLimit     = "passkey_limit"
	codeInvalidPasskey   = "invalid_passkey"
	codeStrategyRequired = "strategy_required"
	codeUnknownStrategy  = "unknown_strategy"
	codeWriteConflict    = "write_conflict"
	codeVersionMismatch  = "version_mismatch"
	codeInvalidHeader    = "invalid_header"
	codePreconditionReq  = "precondition_required"
	codePayloadTooLarge  = "payload_too_large"
	codeUnsupportedMedia = "unsupported_media_type"
	codeInternal         = "internal_error"
	codeUnavailable      = "unavailable"
	// codeDevClientRefused answers a development CLI (backd/dev) on a released server.
	codeDevClientRefused = "dev_client_refused"
	// codeConfigNotLoaded answers every API call of a dev-mode server whose config hasn't
	// loaded yet (NewWaitingHandler).
	codeConfigNotLoaded = "config_not_loaded"
)

// Detail points at one problem, e.g. a field failing schema validation.
type Detail struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	// Code names the problem when it has a code of its own, such as field_encrypted.
	Code string `json:"code,omitempty"`
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
