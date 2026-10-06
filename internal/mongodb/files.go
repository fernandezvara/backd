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

type fileJournalDoc struct {
	ID         string    `bson:"_id"` // the file's id
	Database   string    `bson:"database"`
	Collection string    `bson:"collection"`
	Field      string    `bson:"field"`
	DocumentID string    `bson:"document_id,omitempty"`
	Key        string    `bson:"key"`
	Caller     string    `bson:"caller,omitempty"`
	Size       int64     `bson:"size"`
	Status     string    `bson:"status"`
	CreatedAt  time.Time `bson:"created_at"`
	UpdatedAt  time.Time `bson:"updated_at"`
	ExpiresAt  time.Time `bson:"expires_at"`
}

type fileDeletionDoc struct {
	Key       string    `bson:"_id"` // the object's key: one deletion per object
	Reason    string    `bson:"reason"`
	Attempts  int32     `bson:"attempts"`
	NotBefore time.Time `bson:"not_before"`
	CreatedAt time.Time `bson:"created_at"`
}

func (s *AuthStore) fileJournal() *mongo.Collection   { return s.db.Collection(FileJournalCollection) }
func (s *AuthStore) fileDeletions() *mongo.Collection { return s.db.Collection(FileDeletionsCollection) }

func (d fileJournalDoc) entry() auth.FileJournalEntry {
	return auth.FileJournalEntry{ID: d.ID, Database: d.Database, Collection: d.Collection, Field: d.Field, DocumentID: d.DocumentID, Key: d.Key,
		Caller: d.Caller, Size: d.Size, Status: d.Status, CreatedAt: d.CreatedAt.UTC(), UpdatedAt: d.UpdatedAt.UTC(), ExpiresAt: d.ExpiresAt.UTC()}
}

// JournalFile records an upload.
func (s *AuthStore) JournalFile(ctx context.Context, e auth.FileJournalEntry) error {
	_, err := s.fileJournal().InsertOne(ctx, fileJournalDoc{ID: e.ID, Database: e.Database, Collection: e.Collection, Field: e.Field, DocumentID: e.DocumentID,
		Key: e.Key, Caller: e.Caller, Size: e.Size, Status: e.Status, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt, ExpiresAt: e.ExpiresAt})
	return err
}

// SetFileJournalStatus moves an upload to a new status.
func (s *AuthStore) SetFileJournalStatus(ctx context.Context, id, status, documentID string, at, expiresAt time.Time) error {
	set := bson.D{{Key: "status", Value: status}, {Key: "updated_at", Value: at}, {Key: "expires_at", Value: expiresAt}}
	if documentID != "" {
		set = append(set, bson.E{Key: "document_id", Value: documentID})
	}
	res, err := s.fileJournal().UpdateByID(ctx, id, bson.D{{Key: "$set", Value: set}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return auth.ErrNotFound
	}
	return nil
}

// StaleFileJournal lists the uploads still writing or stored that were last updated before the time.
func (s *AuthStore) StaleFileJournal(ctx context.Context, before time.Time, limit int) ([]auth.FileJournalEntry, error) {
	filter := bson.D{{Key: "status", Value: bson.D{{Key: "$in", Value: bson.A{auth.JournalWriting, auth.JournalStored}}}}, {Key: "updated_at", Value: bson.D{{Key: "$lt", Value: before}}}}
	cur, err := s.fileJournal().Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "updated_at", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	var docs []fileJournalDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]auth.FileJournalEntry, len(docs))
	for i, d := range docs {
		out[i] = d.entry()
	}
	return out, nil
}

// QueueFileDeletion adds an object to delete; the same key twice is one.
func (s *AuthStore) QueueFileDeletion(ctx context.Context, d auth.FileDeletion) error {
	_, err := s.fileDeletions().UpdateByID(ctx, d.Key, bson.D{{Key: "$setOnInsert", Value: fileDeletionDoc{Key: d.Key, Reason: d.Reason, Attempts: int32(d.Attempts), NotBefore: d.NotBefore, CreatedAt: d.CreatedAt}}}, options.UpdateOne().SetUpsert(true))
	return err
}

// ClaimFileDeletions returns up to limit due deletions, each held until lease:
// the claim is an update that only matches a due one, so two workers never get the same.
func (s *AuthStore) ClaimFileDeletions(ctx context.Context, now, lease time.Time, limit int) ([]auth.FileDeletion, error) {
	var out []auth.FileDeletion
	for len(out) < limit {
		var d fileDeletionDoc
		err := s.fileDeletions().FindOneAndUpdate(ctx,
			bson.D{{Key: "not_before", Value: bson.D{{Key: "$lte", Value: now}}}},
			bson.D{{Key: "$set", Value: bson.D{{Key: "not_before", Value: lease}}}},
			options.FindOneAndUpdate().SetSort(bson.D{{Key: "not_before", Value: 1}}).SetReturnDocument(options.Before)).Decode(&d)
		if errors.Is(err, mongo.ErrNoDocuments) {
			break
		}
		if err != nil {
			return out, err
		}
		out = append(out, auth.FileDeletion{Key: d.Key, Reason: d.Reason, Attempts: int(d.Attempts), NotBefore: d.NotBefore.UTC(), CreatedAt: d.CreatedAt.UTC()})
	}
	return out, nil
}

// CompleteFileDeletion removes a deletion that was carried out.
func (s *AuthStore) CompleteFileDeletion(ctx context.Context, key string) error {
	_, err := s.fileDeletions().DeleteOne(ctx, bson.D{{Key: "_id", Value: key}})
	return err
}

// RetryFileDeletion counts a failed attempt and holds the deletion until notBefore.
func (s *AuthStore) RetryFileDeletion(ctx context.Context, key string, notBefore time.Time) error {
	_, err := s.fileDeletions().UpdateByID(ctx, key, bson.D{{Key: "$set", Value: bson.D{{Key: "not_before", Value: notBefore}}}, {Key: "$inc", Value: bson.D{{Key: "attempts", Value: 1}}}})
	return err
}
