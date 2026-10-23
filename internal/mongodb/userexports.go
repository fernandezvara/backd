package mongodb

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/auth"
)

type userExportDoc struct {
	ID         string           `bson:"_id"`
	User       string           `bson:"user"`
	Status     string           `bson:"status"`
	Job        string           `bson:"job,omitempty"`
	Admin      string           `bson:"admin,omitempty"`
	Ticket     string           `bson:"ticket,omitempty"`
	Notify     bool             `bson:"notify,omitempty"`
	Silent     bool             `bson:"silent,omitempty"`
	Reason     string           `bson:"reason,omitempty"`
	Locale     string           `bson:"locale,omitempty"`
	CreatedAt  time.Time        `bson:"created_at"`
	StartedAt  *time.Time       `bson:"started_at,omitempty"`
	FinishedAt *time.Time       `bson:"finished_at,omitempty"`
	ExpiresAt  *time.Time       `bson:"expires_at,omitempty"`
	Key        string           `bson:"key,omitempty"`
	Size       int64            `bson:"size,omitempty"`
	Counts     map[string]int64 `bson:"counts,omitempty"`
	Error      string           `bson:"error,omitempty"`
	Downloads  int64            `bson:"downloads,omitempty"`
}

func timeVal(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

func userExportToDoc(e auth.UserExport) userExportDoc {
	return userExportDoc{
		ID: e.ID, User: e.UserID, Status: e.Status, Job: e.JobID, Admin: e.Admin, Ticket: e.Ticket, Notify: e.Notify, Silent: e.Silent,
		Reason: e.Reason, Locale: e.Locale, CreatedAt: e.CreatedAt.UTC(), StartedAt: timePtr(e.StartedAt), FinishedAt: timePtr(e.FinishedAt),
		ExpiresAt: timePtr(e.ExpiresAt), Key: e.Key, Size: e.Size, Counts: e.Counts, Error: e.Error, Downloads: int64(e.Downloads),
	}
}

func (d userExportDoc) export() auth.UserExport {
	return auth.UserExport{
		ID: d.ID, UserID: d.User, Status: d.Status, JobID: d.Job, Admin: d.Admin, Ticket: d.Ticket, Notify: d.Notify, Silent: d.Silent,
		Reason: d.Reason, Locale: d.Locale, CreatedAt: d.CreatedAt.UTC(), StartedAt: timeVal(d.StartedAt), FinishedAt: timeVal(d.FinishedAt),
		ExpiresAt: timeVal(d.ExpiresAt), Key: d.Key, Size: d.Size, Counts: d.Counts, Error: d.Error, Downloads: int(d.Downloads),
	}
}

func (s *AuthStore) userExports() *mongo.Collection { return s.db.Collection(UserExportsCollection) }

// CreateUserExport implements auth.Store.
func (s *AuthStore) CreateUserExport(ctx context.Context, e auth.UserExport) error {
	_, err := s.userExports().InsertOne(ctx, userExportToDoc(e))
	return err
}

// UserExportByID implements auth.Store.
func (s *AuthStore) UserExportByID(ctx context.Context, id string) (auth.UserExport, error) {
	var d userExportDoc
	err := s.userExports().FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.UserExport{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.UserExport{}, err
	}
	return d.export(), nil
}

// ReplaceUserExport implements auth.Store.
func (s *AuthStore) ReplaceUserExport(ctx context.Context, e auth.UserExport) error {
	res, err := s.userExports().ReplaceOne(ctx, bson.D{{Key: "_id", Value: e.ID}}, userExportToDoc(e))
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return auth.ErrNotFound
	}
	return nil
}

func (s *AuthStore) findUserExports(ctx context.Context, filter bson.D, opts *options.FindOptionsBuilder) ([]auth.UserExport, error) {
	cur, err := s.userExports().Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	var docs []userExportDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]auth.UserExport, len(docs))
	for i, d := range docs {
		out[i] = d.export()
	}
	return out, nil
}

// ListUserExports implements auth.Store.
func (s *AuthStore) ListUserExports(ctx context.Context, userID string, limit int) ([]auth.UserExport, error) {
	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}})
	if limit > 0 {
		opts.SetLimit(int64(limit))
	}
	return s.findUserExports(ctx, bson.D{{Key: "user", Value: userID}}, opts)
}

// ListExpiredUserExports implements auth.Store.
func (s *AuthStore) ListExpiredUserExports(ctx context.Context, now time.Time, limit int) ([]auth.UserExport, error) {
	opts := options.Find().SetSort(bson.D{{Key: "expires_at", Value: 1}, {Key: "_id", Value: 1}})
	if limit > 0 {
		opts.SetLimit(int64(limit))
	}
	return s.findUserExports(ctx, bson.D{{Key: "status", Value: auth.ExportReady}, {Key: "expires_at", Value: bson.D{{Key: "$lte", Value: now.UTC()}}}}, opts)
}
