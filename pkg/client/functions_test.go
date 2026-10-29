package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestSyncCallDecodesTheOutput(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || r.URL.Path != "/v1/r/main/_func/refund" || string(b) != `{"order":"o1"}` || r.Header.Get("Idempotency-Key") != "k" {
			t.Errorf("%s %s %s %v", r.Method, r.URL.Path, b, r.Header)
		}
		io.WriteString(w, `{"refunded":12.5}`)
	})
	var out struct{ Refunded float64 }
	job, err := c.DB("main").Call(bg, "refund", map[string]string{"order": "o1"}, &out, WithIdempotencyKey("k"))
	if err != nil || job != nil || out.Refunded != 12.5 {
		t.Errorf("%v %v %+v", err, job, out)
	}
}

func TestNilInputSendsNoBodyAndFunctionErrorsAreErrors(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if len(b) != 0 {
			t.Errorf("body %q", b)
		}
		w.WriteHeader(409)
		io.WriteString(w, `{"error":{"code":"already_refunded","message":"nope"}}`)
	})
	_, err := c.Functions().Call(bg, "main/refund", nil, nil)
	var e *Error
	if !errors.As(err, &e) || e.Code != "already_refunded" || !errors.Is(err, ErrConflict) {
		t.Errorf("%#v", err)
	}
	for _, bad := range []string{"refund", "/refund", "main/", "a/b/c"} {
		if _, err := c.Functions().Call(bg, bad, nil, nil); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// jobServer answers a call with a 202 and then the job's state on each poll.
func jobServer(t *testing.T, states ...string) *Client {
	var polls atomic.Int32
	return newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST":
			if r.Header.Get("Prefer") != "respond-async" {
				t.Errorf("Prefer = %q", r.Header.Get("Prefer"))
			}
			w.WriteHeader(202)
			io.WriteString(w, `{"id":"j1","function":"main/report","status":"queued","created_at":"t","result":null}`)
		case r.URL.Path == "/v1/r/main/_jobs/j1":
			i := int(polls.Add(1)) - 1
			io.WriteString(w, states[min(i, len(states)-1)])
		default:
			http.NotFound(w, r)
		}
	})
}

const (
	running = `{"id":"j1","function":"main/report","status":"running","created_at":"t","result":null,"steps":[{"n":1,"name":"load","status":"running","started_at":"t","ended_at":null,"duration_ms":null,"current":2,"total":10,"message":null,"updated_at":"t"}]}`
	done    = `{"id":"j1","function":"main/report","status":"done","created_at":"t","result":{"status":"ok","output":{"rows":3},"duration_ms":40}}`
)

func TestAsyncJobWait(t *testing.T) {
	c := jobServer(t, running, running, done)
	job, err := c.DB("main").Call(bg, "report", nil, nil, WithRespondAsync())
	if err != nil || job == nil || job.ID != "j1" || job.Function != "main/report" || job.Data.Status != "queued" {
		t.Fatalf("%v %+v", err, job)
	}
	var progress [][]JobStep
	var out struct{ Rows int }
	err = job.WaitInto(bg, &out, WaitOptions{PollInterval: time.Millisecond, OnProgress: func(s []JobStep, j *Job) { progress = append(progress, s) }})
	if err != nil || out.Rows != 3 {
		t.Fatalf("%v %+v", err, out)
	}
	if len(progress) != 3 || len(progress[0]) != 1 || progress[0][0].Name != "load" || *progress[0][0].Total != 10 || progress[0][0].EndedAt != nil {
		t.Errorf("progress %+v", progress)
	}
	if len(job.Steps()) != 0 || job.Data.Status != "done" {
		t.Errorf("the last poll has no steps: %+v", job.Data)
	}
}

func TestJobStatusAndLookup(t *testing.T) {
	c := jobServer(t, running, done)
	job, _ := c.DB("main").Job(bg, "j1")
	if job == nil || job.Function != "main/report" || job.Data.Status != "running" {
		t.Fatalf("%+v", job)
	}
	if st, err := job.Status(bg); err != nil || st != "done" {
		t.Errorf("%v %q", err, st)
	}
}

func TestJobTimeoutLeavesTheJobRunning(t *testing.T) {
	c := jobServer(t, running)
	job, _ := c.DB("main").Call(bg, "report", nil, nil, WithRespondAsync())
	start := time.Now()
	_, err := job.Wait(bg, WaitOptions{PollInterval: 5 * time.Millisecond, Timeout: 50 * time.Millisecond})
	var te *JobTimeoutError
	if !errors.As(err, &te) || te.JobID != "j1" || time.Since(start) > 2*time.Second {
		t.Errorf("%v after %v", err, time.Since(start))
	}
}

func TestJobWaitStopsWithTheContext(t *testing.T) {
	c := jobServer(t, running)
	job, _ := c.DB("main").Call(bg, "report", nil, nil, WithRespondAsync())
	ctx, cancel := context.WithTimeout(bg, 30*time.Millisecond)
	defer cancel()
	_, err := job.Wait(ctx, WaitOptions{PollInterval: 5 * time.Millisecond})
	if !errors.Is(err, ErrNetwork) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("%v", err)
	}
}

func TestAsyncFailureIsTheErrorASyncCallWouldGive(t *testing.T) {
	cases := []struct {
		result string
		status int
		code   string
		is     error
	}{
		{`{"status":"function_error","http_status":409,"code":"already_refunded","message":"nope","details":[{"path":"order","reason":"x"}]}`, 409, "already_refunded", ErrConflict},
		{`{"status":"timeout","http_status":504,"code":"function_timeout","message":"too slow"}`, 504, "function_timeout", ErrBackd},
		{`{"status":"crash"}`, 500, "function_failed", ErrBackd},
		{`{"status":"function_error","http_status":400,"code":"invalid_expand"}`, 400, "invalid_expand", ErrInvalidExpand},
	}
	for _, c := range cases {
		cl := jobServer(t, fmt.Sprintf(`{"id":"j1","function":"main/report","status":"done","created_at":"t","result":%s}`, c.result))
		job, _ := cl.DB("main").Call(bg, "report", nil, nil, WithRespondAsync())
		_, err := job.Wait(bg, WaitOptions{})
		var e *Error
		if !errors.As(err, &e) || e.Status != c.status || e.Code != c.code || !errors.Is(err, c.is) {
			t.Errorf("%s: %#v", c.result, err)
		}
		if c.code == "already_refunded" && (len(e.Details) != 1 || e.Details[0].Path != "order") {
			t.Errorf("details %+v", e.Details)
		}
	}
}

func TestRunWaitsForAJobAndReturnsASyncOutput(t *testing.T) {
	c := jobServer(t, done)
	var out struct{ Rows int }
	if err := c.DB("main").Run(bg, "report", nil, &out, WaitOptions{PollInterval: time.Millisecond}, WithRespondAsync()); err != nil || out.Rows != 3 {
		t.Errorf("async: %v %+v", err, out)
	}
	s := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"rows":9}`) })
	if err := s.Functions().Run(bg, "main/report", nil, &out, WaitOptions{}); err != nil || out.Rows != 9 {
		t.Errorf("sync: %v %+v", err, out)
	}
}

func TestDoneWithoutAResultIsAnError(t *testing.T) {
	c := jobServer(t, `{"id":"j1","function":"f","status":"done","created_at":"t","result":null}`)
	job, _ := c.DB("main").Call(bg, "report", nil, nil, WithRespondAsync())
	if _, err := job.Wait(bg, WaitOptions{}); err == nil {
		t.Error("no result")
	}
}
