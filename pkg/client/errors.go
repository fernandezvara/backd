package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Detail points at one problem, such as a field that failed validation.
type Detail struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	// Code names the problem when it has a code of its own, such as field_encrypted.
	Code string `json:"code,omitempty"`
}

// kind is a class of errors. The classes form a tree, so ErrConflict matches an id_taken error as
// ConflictError matches IdTakenError in backd-js.
type kind struct {
	name   string
	parent *kind
}

func (k *kind) is(target *kind) bool {
	for ; k != nil; k = k.parent {
		if k == target {
			return true
		}
	}
	return false
}

var (
	kBackd = &kind{name: "backd error"}

	kValidation     = &kind{"validation error", kBackd}
	kAuthentication = &kind{"authentication error", kBackd}
	kForbidden      = &kind{"forbidden", kBackd}
	kNotFound       = &kind{"not found", kBackd}
	kConflict       = &kind{"conflict", kBackd}
	kVersion        = &kind{"version mismatch", kBackd}
	kRetryable      = &kind{"retryable error", kBackd}
	kNetwork        = &kind{"network error", kBackd}

	kRealtimeNotEnabled = &kind{"realtime not enabled", kValidation}
	kTooManySubs        = &kind{"too many subscriptions", kValidation}
	kInvalidResume      = &kind{"invalid resume token", kValidation}
	kInvalidID          = &kind{"invalid id", kValidation}
	kTeam               = &kind{"team error", kValidation}
	kInvalidExpand      = &kind{"invalid expand", kValidation}
	kExpandTooLarge     = &kind{"expand too large", kValidation}
	kInvalidAggregate   = &kind{"invalid aggregate", kValidation}
	kFieldEncrypted     = &kind{"field encrypted", kValidation}
	kAggregateNotOn     = &kind{"aggregate not enabled", kValidation}
	kSearchNotOn        = &kind{"search not enabled", kValidation}
	kSsoRequired        = &kind{"sso required", kValidation}

	kPasswordExpired = &kind{"password expired", kAuthentication}
	kSessionEnded    = &kind{"session ended", kAuthentication}

	kOrgRequiresMFA     = &kind{"organization requires mfa", kForbidden}
	kOrgRoleRequiresMFA = &kind{"organization role requires mfa", kForbidden}
	kNotTeamMember      = &kind{"not a team member", kForbidden}
	kNotAllowed         = &kind{"not allowed", kForbidden}
	kSsoSession         = &kind{"sso session", kForbidden}
	kNotOrgMember       = &kind{"not an organization member", kForbidden}

	kIDTaken         = &kind{"id taken", kConflict}
	kLastOrgManager  = &kind{"last organization manager", kConflict}
	kIndexBuilding   = &kind{"index building", kRetryable}
	kAggregateCost   = &kind{"aggregate too expensive", kBackd}
	kSearchCost      = &kind{"search too expensive", kBackd}
	kVerification    = &kind{"verification required", kBackd}
	kSecondStep      = &kind{"second step required", kBackd}
	kConsentRequired = &kind{"consent required", kBackd}
)

// The sentinels match with errors.Is. A sentinel of a class matches its subclasses too:
// ErrValidation matches ErrInvalidID, ErrConflict matches ErrIDTaken, ErrForbidden matches
// ErrOrgRequiresMFA.
var (
	ErrBackd = &Error{kind: kBackd} // every error of this package

	ErrValidation      = &Error{kind: kValidation}     // 400
	ErrAuthentication  = &Error{kind: kAuthentication} // 401
	ErrForbidden       = &Error{kind: kForbidden}      // 403
	ErrNotFound        = &Error{kind: kNotFound}       // 404
	ErrConflict        = &Error{kind: kConflict}       // 409
	ErrVersionMismatch = &Error{kind: kVersion}        // 412: the If-Match version is not the current one
	ErrRetryable       = &Error{kind: kRetryable}      // 429 and 503
	ErrNetwork         = &Error{kind: kNetwork}        // no answer: the connection failed, or the context ended

	ErrRealtimeNotEnabled    = &Error{kind: kRealtimeNotEnabled}
	ErrTooManySubscriptions  = &Error{kind: kTooManySubs}
	ErrInvalidResumeToken    = &Error{kind: kInvalidResume}
	ErrInvalidID             = &Error{kind: kInvalidID}
	ErrTeam                  = &Error{kind: kTeam}
	ErrInvalidExpand         = &Error{kind: kInvalidExpand}
	ErrExpandTooLarge        = &Error{kind: kExpandTooLarge}
	ErrInvalidAggregate      = &Error{kind: kInvalidAggregate}
	ErrFieldEncrypted        = &Error{kind: kFieldEncrypted}
	ErrAggregateNotEnabled   = &Error{kind: kAggregateNotOn}
	ErrSearchNotEnabled      = &Error{kind: kSearchNotOn}
	ErrSsoRequired           = &Error{kind: kSsoRequired}
	ErrPasswordExpired       = &Error{kind: kPasswordExpired}
	ErrSessionEnded          = &Error{kind: kSessionEnded}
	ErrOrgRequiresMFA        = &Error{kind: kOrgRequiresMFA}
	ErrOrgRoleRequiresMFA    = &Error{kind: kOrgRoleRequiresMFA}
	ErrNotTeamMember         = &Error{kind: kNotTeamMember}
	ErrNotAllowed            = &Error{kind: kNotAllowed}
	ErrSsoSession            = &Error{kind: kSsoSession}
	ErrNotOrgMember          = &Error{kind: kNotOrgMember}
	ErrIDTaken               = &Error{kind: kIDTaken}
	ErrLastOrgManager        = &Error{kind: kLastOrgManager}
	ErrIndexBuilding         = &Error{kind: kIndexBuilding}
	ErrAggregateTooExpensive = &Error{kind: kAggregateCost}
	ErrSearchTooExpensive    = &Error{kind: kSearchCost}
	ErrVerificationRequired  = &Error{kind: kVerification}
	ErrSecondStepRequired    = &Error{kind: kSecondStep}
	ErrConsentRequired       = &Error{kind: kConsentRequired}
)

// Error is what a failed call returns: the API's answer (or, for ErrNetwork, the lack of one).
type Error struct {
	// Status is the HTTP status; 0 when there was no answer.
	Status int
	// Code is the API's error code, such as "not_found" or "id_taken"; "network_error" or
	// "aborted" when there was no answer, and "http_<status>" when the answer was not the API's.
	Code    string
	Message string
	Details []Detail
	// RequestID is the id the server logged the request under, for support.
	RequestID string
	// RetryAfter is how long the server asked to wait, for retryable answers; 0 when it didn't say.
	RetryAfter time.Duration

	kind  *kind
	cause error
}

func (e *Error) Error() string {
	if e.Message == "" {
		if e.kind != nil {
			return e.kind.name
		}
		return "backd error"
	}
	return e.Message
}

// Unwrap returns the underlying cause of a network error, such as context.Canceled.
func (e *Error) Unwrap() error { return e.cause }

// Is reports whether target is a sentinel of e's class.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	if !ok || t.kind == nil || e.kind == nil {
		return false
	}
	if t == e {
		return true
	}
	// Only sentinels (no status, no code) are compared by class.
	return t.Status == 0 && t.Code == "" && e.kind.is(t.kind)
}

// ConsentRef names a consent and the version it asks for.
type ConsentRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ConsentRequiredError is the answer to a call that needs consents the user hasn't accepted.
// baseError lets the typed errors embed *Error without a field named Error shadowing its method.
type baseError = Error

type ConsentRequiredError struct {
	*baseError
	// Consents are the ones to accept, with the versions the server wants.
	Consents []ConsentRef
}

func (e *ConsentRequiredError) Unwrap() error { return e.baseError }

// PasswordExpiredError is the answer to a sign-in with a password that has expired.
type PasswordExpiredError struct {
	*baseError
	// ResetEmailQueued is true when the server has sent the person a link to choose a new one.
	ResetEmailQueued bool
}

func (e *PasswordExpiredError) Unwrap() error { return e.baseError }

// codeKinds maps the API's error codes to their class.
var codeKinds = map[string]*kind{
	"realtime_not_enabled": kRealtimeNotEnabled, "too_many_subscriptions": kTooManySubs,
	"invalid_resume_token": kInvalidResume,
	"id_required":          kInvalidID, "invalid_id": kInvalidID, "id_not_allowed": kInvalidID,
	"id_taken":         kIDTaken,
	"org_requires_mfa": kOrgRequiresMFA, "org_role_requires_mfa": kOrgRoleRequiresMFA,
	"not_team_member": kNotTeamMember,
	"team_required":   kTeam, "team_not_allowed": kTeam,
	"not_org_member": kNotOrgMember, "not_allowed": kNotAllowed,
	"sso_required": kSsoRequired, "sso_session": kSsoSession,
	"last_org_manager":  kLastOrgManager,
	"invalid_aggregate": kInvalidAggregate, "aggregate_not_enabled": kAggregateNotOn,
	"aggregate_too_expensive": kAggregateCost,
	"search_not_enabled":      kSearchNotOn, "search_too_expensive": kSearchCost,
	"index_building": kIndexBuilding,
	"invalid_expand": kInvalidExpand, "field_encrypted": kFieldEncrypted, "expand_too_large": kExpandTooLarge,
	"password_expired": kPasswordExpired, "consent_required": kConsentRequired,
}

var statusKinds = map[int]*kind{
	400: kValidation, 401: kAuthentication, 403: kForbidden, 404: kNotFound,
	409: kConflict, 412: kVersion, 429: kRetryable, 503: kRetryable,
}

var consentVersion = regexp.MustCompile(`version (\S+)$`)
var consentName = regexp.MustCompile(`^consents\.(.+)$`)

// maxErrorBody bounds what is read of an error answer.
const maxErrorBody = 1 << 20

// errorFromResponse builds the error for a non-2xx answer. It reads and closes the body.
func errorFromResponse(res *http.Response) error {
	defer res.Body.Close()
	var body struct {
		Error struct {
			Code      string   `json:"code"`
			Message   string   `json:"message"`
			Details   []Detail `json:"details"`
			RequestID string   `json:"request_id"`
		} `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBody))
	_ = json.Unmarshal(raw, &body) // not JSON, for example from a proxy: keep the defaults below
	e := body.Error
	code := e.Code
	if code == "" {
		code = "http_" + strconv.Itoa(res.StatusCode)
	}
	k := codeKinds[code]
	if k == nil {
		k = statusKinds[res.StatusCode]
	}
	if k == nil {
		k = kBackd
	}
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(res.StatusCode)
		if msg == "" {
			msg = "request failed"
		}
	}
	reqID := e.RequestID
	if reqID == "" {
		reqID = res.Header.Get("X-Request-ID")
	}
	err := &Error{Status: res.StatusCode, Code: code, Message: msg, Details: e.Details, RequestID: reqID, kind: k}
	if k.is(kRetryable) {
		err.RetryAfter = parseRetryAfter(res.Header.Get("Retry-After"), time.Now())
	}
	switch k {
	case kConsentRequired:
		typed := &ConsentRequiredError{baseError: err}
		for _, d := range e.Details {
			name, version := consentName.FindStringSubmatch(d.Path), consentVersion.FindStringSubmatch(d.Reason)
			if name != nil && version != nil {
				typed.Consents = append(typed.Consents, ConsentRef{Name: name[1], Version: version[1]})
			}
		}
		return typed
	case kPasswordExpired:
		typed := &PasswordExpiredError{baseError: err}
		for _, d := range e.Details {
			if d.Path == "reset_email" {
				typed.ResetEmailQueued = true
			}
		}
		return typed
	}
	return err
}

// parseRetryAfter reads a Retry-After header: seconds or an HTTP date.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.ParseUint(v, 10, 32); err == nil {
		return time.Duration(n) * time.Second
	}
	if at, err := http.ParseTime(v); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}

// networkError wraps a failure to get an answer. A context that ended is "aborted".
func networkError(ctx context.Context, cause error) *Error {
	code, msg := "network_error", "network error: "+cause.Error()
	if ctx.Err() != nil {
		code, msg, cause = "aborted", "request aborted", errors.Join(ctx.Err(), cause)
	}
	return &Error{Code: code, Message: msg, kind: kNetwork, cause: cause}
}

// newError makes an error of the API's class for a code the client raises itself.
func newError(status int, code, message string, k *kind) *Error {
	return &Error{Status: status, Code: code, Message: message, kind: k}
}

// asError finds the *Error in err.
func asError(err error, target **Error) bool { return errors.As(err, target) }
