package client

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func errFor(t *testing.T, status int, body string, hdr map[string]string) error {
	t.Helper()
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	})
	_, err := c.Do(bg, "GET", []string{"a"}, nil, nil)
	if err == nil {
		t.Fatal("no error")
	}
	return err
}

func TestErrorClasses(t *testing.T) {
	cases := []struct {
		status int
		code   string
		is     []*Error
		not    []*Error
	}{
		{400, "invalid_input", []*Error{ErrValidation}, []*Error{ErrNotFound, ErrInvalidID}},
		{400, "invalid_id", []*Error{ErrValidation, ErrInvalidID}, nil},
		{400, "id_required", []*Error{ErrInvalidID}, nil},
		{401, "unauthenticated", []*Error{ErrAuthentication}, []*Error{ErrForbidden}},
		{403, "org_requires_mfa", []*Error{ErrForbidden, ErrOrgRequiresMFA}, []*Error{ErrOrgRoleRequiresMFA}},
		{404, "not_found", []*Error{ErrNotFound}, nil},
		{409, "id_taken", []*Error{ErrConflict, ErrIDTaken}, []*Error{ErrLastOrgManager}},
		{409, "conflict", []*Error{ErrConflict}, []*Error{ErrIDTaken}},
		{412, "version_mismatch", []*Error{ErrVersionMismatch}, nil},
		{503, "index_building", []*Error{ErrRetryable, ErrIndexBuilding}, nil},
		{429, "rate_limited", []*Error{ErrRetryable}, []*Error{ErrIndexBuilding}},
		{422, "aggregate_too_expensive", []*Error{ErrAggregateTooExpensive}, []*Error{ErrValidation}},
		{400, "field_encrypted", []*Error{ErrFieldEncrypted, ErrValidation}, nil},
		{500, "internal_error", []*Error{ErrBackd}, []*Error{ErrValidation, ErrRetryable}},
	}
	for _, c := range cases {
		err := errFor(t, c.status, `{"error":{"code":"`+c.code+`","message":"m","request_id":"rq"}}`, nil)
		var e *Error
		if !errors.As(err, &e) || e.Status != c.status || e.Code != c.code || e.Message != "m" || e.RequestID != "rq" {
			t.Errorf("%s: %#v", c.code, err)
		}
		if !errors.Is(err, ErrBackd) {
			t.Errorf("%s: every error is an ErrBackd", c.code)
		}
		for _, s := range c.is {
			if !errors.Is(err, s) {
				t.Errorf("%s: want errors.Is %s", c.code, s.kind.name)
			}
		}
		for _, s := range c.not {
			if errors.Is(err, s) {
				t.Errorf("%s: unexpected errors.Is %s", c.code, s.kind.name)
			}
		}
	}
}

func TestErrorFromAnythingBut_APIJSON(t *testing.T) {
	err := errFor(t, 502, "<html>bad gateway</html>", map[string]string{"X-Request-ID": "px"})
	var e *Error
	if !errors.As(err, &e) || e.Code != "http_502" || e.Message != "Bad Gateway" || e.RequestID != "px" {
		t.Errorf("%#v", err)
	}
	err = errFor(t, 404, "", nil)
	if !errors.Is(err, ErrNotFound) || err.Error() != "Not Found" {
		t.Errorf("%v", err)
	}
}

func TestErrorDetails(t *testing.T) {
	err := errFor(t, 400, `{"error":{"code":"invalid_document","message":"bad","details":[{"path":"title","reason":"required"}]}}`, nil)
	var e *Error
	errors.As(err, &e)
	if len(e.Details) != 1 || e.Details[0] != (Detail{Path: "title", Reason: "required"}) {
		t.Errorf("%+v", e.Details)
	}
}

func TestRetryAfter(t *testing.T) {
	err := errFor(t, 429, `{}`, map[string]string{"Retry-After": "7"})
	var e *Error
	errors.As(err, &e)
	if e.RetryAfter != 7*time.Second {
		t.Errorf("RetryAfter = %v", e.RetryAfter)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if d := parseRetryAfter(now.Add(90*time.Second).Format(http.TimeFormat), now); d != 90*time.Second {
		t.Errorf("date: %v", d)
	}
	for _, v := range []string{"", "soon", "-3", now.Add(-time.Hour).Format(http.TimeFormat)} {
		if d := parseRetryAfter(v, now); d != 0 {
			t.Errorf("%q = %v", v, d)
		}
	}
}

func TestConsentRequiredCarriesTheConsents(t *testing.T) {
	err := errFor(t, 403, `{"error":{"code":"consent_required","message":"accept","details":[
		{"path":"consents.terms","reason":"needs version 3"},{"path":"consents.privacy","reason":"needs version 2024-05"},{"path":"other","reason":"x"}]}}`, nil)
	var ce *ConsentRequiredError
	if !errors.As(err, &ce) || len(ce.Consents) != 2 || ce.Consents[0] != (ConsentRef{"terms", "3"}) || ce.Consents[1] != (ConsentRef{"privacy", "2024-05"}) {
		t.Fatalf("%#v", err)
	}
	var e *Error
	if !errors.Is(err, ErrConsentRequired) || !errors.As(err, &e) || e.Status != 403 || ce.Status != 403 {
		t.Errorf("a typed error is still an *Error: %#v", err)
	}
}

func TestPasswordExpiredSaysWhetherAnEmailWasQueued(t *testing.T) {
	err := errFor(t, 401, `{"error":{"code":"password_expired","message":"expired","details":[{"path":"reset_email","reason":"queued"}]}}`, nil)
	var pe *PasswordExpiredError
	if !errors.As(err, &pe) || !pe.ResetEmailQueued || !errors.Is(err, ErrPasswordExpired) || !errors.Is(err, ErrAuthentication) {
		t.Errorf("%#v", err)
	}
}
