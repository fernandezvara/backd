package mongodb

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/auth"
)

type checkProblemDoc struct {
	Path   string `bson:"path"`
	Reason string `bson:"reason"`
}

type checkedDocDoc struct {
	ID           string            `bson:"id"`
	Deleted      bool              `bson:"deleted,omitempty"`
	Problems     []checkProblemDoc `bson:"problems"`
	MoreProblems int32             `bson:"more_problems,omitempty"`
}

type checkReportDoc struct {
	ID         string          `bson:"_id"` // <database>/<collection>
	Database   string          `bson:"database"`
	Collection string          `bson:"collection"`
	JobID      string          `bson:"job_id"`
	StartedAt  time.Time       `bson:"started_at"`
	FinishedAt time.Time       `bson:"finished_at"`
	Scanned    int64           `bson:"scanned"`
	Invalid    int64           `bson:"invalid"`
	Complete   bool            `bson:"complete"`
	StoppedBy  string          `bson:"stopped_by,omitempty"`
	Limit      int32           `bson:"limit"`
	SchemaHash string          `bson:"schema_hash"`
	Documents  []checkedDocDoc `bson:"documents"`
}

func (s *AuthStore) checkReports() *mongo.Collection { return s.db.Collection(SchemaChecksCollection) }

func (d checkReportDoc) report() auth.CheckReport {
	r := auth.CheckReport{
		Database: d.Database, Collection: d.Collection, JobID: d.JobID, StartedAt: d.StartedAt.UTC(), FinishedAt: d.FinishedAt.UTC(),
		Scanned: d.Scanned, Invalid: d.Invalid, Complete: d.Complete, StoppedBy: d.StoppedBy, Limit: int(d.Limit), SchemaHash: d.SchemaHash,
	}
	for _, dd := range d.Documents {
		doc := auth.CheckedDocument{ID: dd.ID, Deleted: dd.Deleted, MoreProblems: int(dd.MoreProblems)}
		for _, p := range dd.Problems {
			doc.Problems = append(doc.Problems, auth.CheckProblem{Path: p.Path, Reason: p.Reason})
		}
		r.Documents = append(r.Documents, doc)
	}
	return r
}

// SetCheckReport stores the report as the collection's latest.
func (s *AuthStore) SetCheckReport(ctx context.Context, r auth.CheckReport) error {
	d := checkReportDoc{
		ID: r.Database + "/" + r.Collection, Database: r.Database, Collection: r.Collection, JobID: r.JobID,
		StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Scanned: r.Scanned, Invalid: r.Invalid, Complete: r.Complete,
		StoppedBy: r.StoppedBy, Limit: int32(r.Limit), SchemaHash: r.SchemaHash, Documents: []checkedDocDoc{},
	}
	for _, rd := range r.Documents {
		doc := checkedDocDoc{ID: rd.ID, Deleted: rd.Deleted, MoreProblems: int32(rd.MoreProblems), Problems: []checkProblemDoc{}}
		for _, p := range rd.Problems {
			doc.Problems = append(doc.Problems, checkProblemDoc{Path: p.Path, Reason: p.Reason})
		}
		d.Documents = append(d.Documents, doc)
	}
	_, err := s.checkReports().ReplaceOne(ctx, bson.D{{Key: "_id", Value: d.ID}}, d, options.Replace().SetUpsert(true))
	return err
}

// ListCheckReports returns every latest report.
func (s *AuthStore) ListCheckReports(ctx context.Context, documents bool) ([]auth.CheckReport, error) {
	opts := options.Find()
	if !documents {
		opts.SetProjection(bson.D{{Key: "documents", Value: 0}})
	}
	cur, err := s.checkReports().Find(ctx, bson.D{}, opts)
	if err != nil {
		return nil, err
	}
	var docs []checkReportDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]auth.CheckReport, len(docs))
	for i, d := range docs {
		out[i] = d.report()
	}
	return out, nil
}

// GetCheckReport returns one collection's latest report.
func (s *AuthStore) GetCheckReport(ctx context.Context, database, collection string) (auth.CheckReport, bool, error) {
	if strings.Contains(database, "/") {
		return auth.CheckReport{}, false, nil
	}
	var d checkReportDoc
	err := s.checkReports().FindOne(ctx, bson.D{{Key: "_id", Value: database + "/" + collection}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.CheckReport{}, false, nil
	}
	if err != nil {
		return auth.CheckReport{}, false, err
	}
	return d.report(), true, nil
}
