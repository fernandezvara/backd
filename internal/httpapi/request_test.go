package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{0, "1"}, {time.Millisecond, "1"}, {time.Second, "1"}, {time.Second + 1, "2"}, {90 * time.Second, "90"}, {time.Hour, "3600"},
	} {
		rec := httptest.NewRecorder()
		retryAfter(rec, tc.d)
		if got := rec.Header().Get("Retry-After"); got != tc.want {
			t.Errorf("retryAfter(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestParsePositiveLimit(t *testing.T) {
	for _, tc := range []struct {
		query  string
		want   int
		wantOK bool
	}{
		{"", 20, true}, {"?limit=5", 5, true}, {"?limit=0", 0, false}, {"?limit=-1", 0, false}, {"?limit=x", 0, false},
	} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/"+tc.query, nil)
		got, ok := parsePositiveLimit(rec, r, 20)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("%q: got (%d, %v), want (%d, %v)", tc.query, got, ok, tc.want, tc.wantOK)
		}
		if !ok && rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", tc.query, rec.Code)
		}
	}
}

func TestStrictDecode(t *testing.T) {
	var v struct {
		A int `json:"a"`
	}
	if err := strictDecode([]byte(`{"a":1}`), &v); err != nil || v.A != 1 {
		t.Errorf("valid body: %v, %+v", err, v)
	}
	if err := strictDecode([]byte(`{"a":1,"b":2}`), &v); err == nil {
		t.Error("an unknown field was accepted")
	}
	if err := strictDecode([]byte(`nope`), &v); err == nil {
		t.Error("invalid JSON was accepted")
	}
}

func TestIfMatchFrom(t *testing.T) {
	r := httptest.NewRequest(http.MethodPut, "/", nil)
	r.Header.Set("If-Match", `"3"`)
	if cond, ok := ifMatchFrom(httptest.NewRecorder(), r); !ok || !cond.present {
		t.Errorf("valid header: ok=%v cond=%+v", ok, cond)
	}
	r.Header.Set("If-Match", `3`)
	rec := httptest.NewRecorder()
	if _, ok := ifMatchFrom(rec, r); ok || rec.Code != http.StatusBadRequest {
		t.Errorf("invalid header: ok=%v status=%d", ok, rec.Code)
	}
}
