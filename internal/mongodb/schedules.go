package mongodb

import (
	"context"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/auth"
)

type scheduleDoc struct {
	ID        string    `bson:"_id"` // <database>/<function>
	Paused    bool      `bson:"paused"`
	ChangedAt time.Time `bson:"changed_at"`
	ChangedBy string    `bson:"changed_by,omitempty"`
}

func (s *AuthStore) schedules() *mongo.Collection { return s.db.Collection(SchedulesCollection) }

// SetScheduleState stores a scheduled function's state.
func (s *AuthStore) SetScheduleState(ctx context.Context, st auth.ScheduleState) error {
	_, err := s.schedules().ReplaceOne(ctx, bson.D{{Key: "_id", Value: st.Database + "/" + st.Function}},
		scheduleDoc{ID: st.Database + "/" + st.Function, Paused: st.Paused, ChangedAt: st.ChangedAt, ChangedBy: st.ChangedBy},
		options.Replace().SetUpsert(true))
	return err
}

// ListScheduleStates returns every stored state.
func (s *AuthStore) ListScheduleStates(ctx context.Context) ([]auth.ScheduleState, error) {
	cur, err := s.schedules().Find(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	var docs []scheduleDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]auth.ScheduleState, 0, len(docs))
	for _, d := range docs {
		database, function, _ := strings.Cut(d.ID, "/")
		out = append(out, auth.ScheduleState{Database: database, Function: function, Paused: d.Paused, ChangedAt: d.ChangedAt.UTC(), ChangedBy: d.ChangedBy})
	}
	return out, nil
}
