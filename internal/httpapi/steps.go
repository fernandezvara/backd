package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/executor"
)

// Limits on what a function's steps may hold, enforced where they arrive: the
// runner clips them, but its code runs in the function's process.
const (
	maxStepName    = 100
	maxStepMessage = 200
)

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// stepsFromExecutor converts what the runner reported, dropping anything
// malformed, and clips the text.
func stepsFromExecutor(in []executor.Step) []auth.Step {
	out := make([]auth.Step, 0, len(in))
	for _, s := range in {
		if s.N < 1 || s.Name == "" {
			continue
		}
		st := auth.Step{N: s.N, Name: clip(s.Name, maxStepName), Status: s.Status, StartedAt: s.StartedAt.UTC(), Current: max(s.Current, 0), UpdatedAt: s.UpdatedAt.UTC()}
		switch st.Status {
		case auth.StepRunning, auth.StepDone, auth.StepFailed, auth.StepTimedOut, auth.StepCancelled:
		default:
			st.Status = auth.StepFailed
		}
		if s.EndedAt != nil {
			st.EndedAt = s.EndedAt.UTC()
		}
		if s.DurationMS != nil {
			st.DurationMS = max(*s.DurationMS, 0)
		}
		if s.Total != nil && *s.Total >= 0 {
			t := *s.Total
			st.Total = &t
		}
		if s.Message != nil {
			st.Message = clip(*s.Message, maxStepMessage)
		}
		out = append(out, st)
		if len(out) == auth.MaxSteps {
			break
		}
	}
	return out
}

func stepsToExecutor(in []auth.Step) []executor.Step {
	out := make([]executor.Step, len(in))
	for i, s := range in {
		st := executor.Step{N: s.N, Name: s.Name, Status: s.Status, StartedAt: s.StartedAt, Current: s.Current, Total: s.Total, UpdatedAt: s.UpdatedAt}
		if !s.EndedAt.IsZero() {
			t, d := s.EndedAt, s.DurationMS
			st.EndedAt, st.DurationMS = &t, &d
		}
		if s.Message != "" {
			m := s.Message
			st.Message = &m
		}
		out[i] = st
	}
	return out
}

// stepEnding is the status the step still running gets when its attempt ends
// with result status.
func stepEnding(status string) string {
	switch status {
	case executor.StatusOK:
		return auth.StepDone
	case executor.StatusTimeout:
		return auth.StepTimedOut
	case executor.StatusCancelled:
		return auth.StepCancelled
	}
	return auth.StepFailed
}

// settleSteps returns the final steps of an attempt that ended with res: the
// ones the runner sent with its result, or (a killed run sends none) the last
// ones a job was sent live; the step still running is closed according to how
// the attempt ended. For an async job they are also stored on it, so the job
// shows them closed until a retry starts again (persist; a cancelled job already
// holds its own).
func (f *functions) settleSteps(ctx context.Context, svc *auth.Users, job *auth.Job, persist bool, res executor.Result) executor.Result {
	steps, omitted := stepsFromExecutor(res.Steps), res.StepsOmitted
	if len(steps) == 0 && job != nil {
		if cur, found, err := svc.GetJob(ctx, job.ID); err == nil && found {
			steps, omitted = cur.Steps, cur.StepsOmitted
		}
	}
	if len(steps) == 0 {
		res.Steps, res.StepsOmitted = nil, 0
		return res
	}
	steps = auth.CloseSteps(steps, stepEnding(res.Status), f.docs.now())
	if job != nil && persist {
		if _, err := svc.SetJobSteps(ctx, job.ID, job.Attempts, steps, omitted); err != nil {
			f.log.Error("store the job's final steps", "job_id", job.ID, "error", err)
		}
	}
	res.Steps, res.StepsOmitted = stepsToExecutor(steps), omitted
	return res
}

// putSteps handles PUT /v1/{realm}/_job/steps on the internal listener: the
// running attempt of an async job reports its steps (what ctx.step and
// ctx.progress keep), live. The callback token names the job and the attempt;
// a write for an attempt that isn't running any more changes nothing.
func (f *functions) putSteps(w http.ResponseWriter, r *http.Request) {
	realm := chi.URLParam(r, "realm")
	caller, _, ok := f.docs.identify(w, r, realm)
	if !ok {
		return
	}
	fc := caller.Func
	if fc == nil || fc.Admin || fc.Job == "" {
		writeError(w, r, http.StatusForbidden, codeForbidden, "only a running job reports its steps")
		return
	}
	svc := f.docs.users(realm)
	if svc == nil {
		notFound(w, r)
		return
	}
	body, err := io.ReadAll(r.Body)
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		tooLarge(w, r, tooBig.Limit)
		return
	}
	var in struct {
		Steps   []executor.Step `json:"steps"`
		Omitted int             `json:"omitted"`
	}
	if err != nil || json.Unmarshal(body, &in) != nil || len(in.Steps) > auth.MaxSteps {
		writeError(w, r, http.StatusBadRequest, codeInvalidJSON, "the body must be {steps: [...up to 100], omitted}")
		return
	}
	// The write is the job's own state, never the function's say-so about a
	// step ending: closing is backd's, when the attempt ends.
	steps := stepsFromExecutor(in.Steps)
	if _, err := svc.SetJobSteps(r.Context(), fc.Job, fc.Attempt, steps, max(in.Omitted, 0)); err != nil {
		writeError(w, r, http.StatusInternalServerError, codeInternal, "could not store the steps")
		logger(r.Context()).Error("store job steps", "job_id", fc.Job, "error", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// stepJSON renders a step the way the API shows it.
func stepJSON(s auth.Step) map[string]any {
	out := map[string]any{
		"n": s.N, "name": s.Name, "status": s.Status, "started_at": formatTime(s.StartedAt),
		"ended_at": nil, "duration_ms": nil, "current": s.Current, "total": nil, "message": nil, "updated_at": formatTime(s.UpdatedAt),
	}
	if !s.EndedAt.IsZero() {
		out["ended_at"], out["duration_ms"] = formatTime(s.EndedAt), s.DurationMS
	}
	if s.Total != nil {
		out["total"] = *s.Total
	}
	if s.Message != "" {
		out["message"] = s.Message
	}
	return out
}

func stepsJSON(steps []auth.Step) []map[string]any {
	out := make([]map[string]any, len(steps))
	for i, s := range steps {
		out[i] = stepJSON(s)
	}
	return out
}
