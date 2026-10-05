package auth

import (
	"context"
	"time"
)

// ScheduleState is the runtime state of a scheduled function: whether an
// administrator paused it. A function with no state is running. The state
// is kept after a resume, because ChangedAt keeps a run that fell inside the
// pause from being made up at the moment of the resume.
type ScheduleState struct {
	Database, Function string
	Paused             bool
	ChangedAt          time.Time
	ChangedBy          string // the actor that last paused or resumed it
}

// SetSchedulePaused pauses or resumes a scheduled function's runs. Asking for
// the state it already has changes nothing.
func (s *Users) SetSchedulePaused(ctx context.Context, database, function string, paused bool, by string) (ScheduleState, error) {
	states, err := s.Store.ListScheduleStates(ctx)
	if err != nil {
		return ScheduleState{}, err
	}
	for _, st := range states {
		if st.Database == database && st.Function == function && st.Paused == paused {
			return st, nil
		}
	}
	st := ScheduleState{Database: database, Function: function, Paused: paused, ChangedAt: s.now(), ChangedBy: by}
	if err := s.Store.SetScheduleState(ctx, st); err != nil {
		return ScheduleState{}, err
	}
	return st, nil
}

// ScheduleStates returns the state of every function that has ever been
// paused, keyed by "<database>/<function>".
func (s *Users) ScheduleStates(ctx context.Context) (map[string]ScheduleState, error) {
	list, err := s.Store.ListScheduleStates(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]ScheduleState, len(list))
	for _, st := range list {
		out[st.Database+"/"+st.Function] = st
	}
	return out, nil
}
