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

	Pending      bool      `bson:"pending,omitempty"`
	Direct       bool      `bson:"direct,omitempty"`
	TokenHash    string    `bson:"token_hash,omitempty"`
	Owner        string    `bson:"owner,omitempty"`
	CallerKey    string    `bson:"caller_key,omitempty"`
	PendingUntil time.Time `bson:"pending_until,omitempty"`
	Name         string    `bson:"name,omitempty"`
	Type         string    `bson:"type,omitempty"`
	SHA256       string    `bson:"sha256,omitempty"`
	UploadedAt   time.Time `bson:"uploaded_at,omitempty"`
}

type fileDeletionDoc struct {
	Key       string    `bson:"_id"` // the object's key: one deletion per object
	Reason    string    `bson:"reason"`
	Attempts  int32     `bson:"attempts"`
	NotBefore time.Time `bson:"not_before"`
	CreatedAt time.Time `bson:"created_at"`
}

func (s *AuthStore) fileJournal() *mongo.Collection { return s.db.Collection(FileJournalCollection) }
func (s *AuthStore) fileDeletions() *mongo.Collection {
	return s.db.Collection(FileDeletionsCollection)
}

func (d fileJournalDoc) entry() auth.FileJournalEntry {
	return auth.FileJournalEntry{ID: d.ID, Database: d.Database, Collection: d.Collection, Field: d.Field, DocumentID: d.DocumentID, Key: d.Key,
		Caller: d.Caller, Size: d.Size, Status: d.Status, CreatedAt: d.CreatedAt.UTC(), UpdatedAt: d.UpdatedAt.UTC(), ExpiresAt: d.ExpiresAt.UTC(),
		Pending: d.Pending, Direct: d.Direct, TokenHash: d.TokenHash, Owner: d.Owner, CallerKey: d.CallerKey, PendingUntil: d.PendingUntil.UTC(), Name: d.Name, Type: d.Type, SHA256: d.SHA256, UploadedAt: d.UploadedAt.UTC()}
}

// JournalFile records an upload.
func (s *AuthStore) JournalFile(ctx context.Context, e auth.FileJournalEntry) error {
	_, err := s.fileJournal().InsertOne(ctx, fileJournalDoc{ID: e.ID, Database: e.Database, Collection: e.Collection, Field: e.Field, DocumentID: e.DocumentID,
		Key: e.Key, Caller: e.Caller, Size: e.Size, Status: e.Status, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt, ExpiresAt: e.ExpiresAt,
		Pending: e.Pending, Direct: e.Direct, TokenHash: e.TokenHash, Owner: e.Owner, CallerKey: e.CallerKey, PendingUntil: e.PendingUntil,
		Name: e.Name, Type: e.Type, SHA256: e.SHA256, UploadedAt: e.UploadedAt})
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

// StaleFileJournal lists the uploads that were left writing, stored or attaching and last
// updated before the time, and the pending uploads that expired unused.
func (s *AuthStore) StaleFileJournal(ctx context.Context, before, now time.Time, limit int) ([]auth.FileJournalEntry, error) {
	epoch := time.Unix(0, 0)
	waiting := bson.D{{Key: "$in", Value: bson.A{auth.JournalWriting, auth.JournalStored}}}
	filter := bson.D{{Key: "$or", Value: bson.A{
		// A claimed upload that nobody finished.
		bson.D{{Key: "status", Value: auth.JournalAttaching}, {Key: "updated_at", Value: bson.D{{Key: "$lt", Value: before}}}},
		// One that may be used until it expires.
		bson.D{{Key: "status", Value: waiting}, {Key: "pending_until", Value: bson.D{{Key: "$gt", Value: epoch}, {Key: "$lt", Value: now}}}},
		// A proxy upload that stopped.
		bson.D{{Key: "status", Value: waiting}, {Key: "updated_at", Value: bson.D{{Key: "$lt", Value: before}}}, {Key: "pending_until", Value: bson.D{{Key: "$not", Value: bson.D{{Key: "$gt", Value: epoch}}}}}},
	}}}
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

// FileJournalEntry returns one upload's record.
func (s *AuthStore) FileJournalEntry(ctx context.Context, id string) (auth.FileJournalEntry, error) {
	var d fileJournalDoc
	err := s.fileJournal().FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.FileJournalEntry{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.FileJournalEntry{}, err
	}
	return d.entry(), nil
}

// CompletePendingUpload records what was stored and makes the upload ready to attach.
func (s *AuthStore) CompletePendingUpload(ctx context.Context, id, name, contentType, sha256sum string, size int64, uploadedAt, at time.Time) error {
	res, err := s.fileJournal().UpdateByID(ctx, id, bson.D{{Key: "$set", Value: bson.D{
		{Key: "name", Value: name}, {Key: "type", Value: contentType}, {Key: "sha256", Value: sha256sum}, {Key: "size", Value: size},
		{Key: "uploaded_at", Value: uploadedAt}, {Key: "status", Value: auth.JournalStored}, {Key: "updated_at", Value: at}}}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return auth.ErrNotFound
	}
	return nil
}

// ClaimPendingUpload takes a ready pending upload once: the update only matches one that is
// stored, unexpired and has this token, so two writes never get the same.
func (s *AuthStore) ClaimPendingUpload(ctx context.Context, id, tokenHash, documentID string, now time.Time) (auth.FileJournalEntry, error) {
	var d fileJournalDoc
	err := s.fileJournal().FindOneAndUpdate(ctx,
		bson.D{{Key: "_id", Value: id}, {Key: "pending", Value: true}, {Key: "status", Value: auth.JournalStored}, {Key: "token_hash", Value: tokenHash}, {Key: "pending_until", Value: bson.D{{Key: "$gt", Value: now}}}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "status", Value: auth.JournalAttaching}, {Key: "document_id", Value: documentID}, {Key: "updated_at", Value: now}}}},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.FileJournalEntry{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.FileJournalEntry{}, err
	}
	return d.entry(), nil
}

// ClaimDirectUpload takes a direct upload that waits for its bytes, once.
func (s *AuthStore) ClaimDirectUpload(ctx context.Context, id, tokenHash string, now time.Time) (auth.FileJournalEntry, error) {
	var d fileJournalDoc
	err := s.fileJournal().FindOneAndUpdate(ctx,
		bson.D{{Key: "_id", Value: id}, {Key: "direct", Value: true}, {Key: "status", Value: auth.JournalWriting}, {Key: "token_hash", Value: tokenHash}, {Key: "pending_until", Value: bson.D{{Key: "$gt", Value: now}}}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "status", Value: auth.JournalAttaching}, {Key: "updated_at", Value: now}}}},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.FileJournalEntry{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.FileJournalEntry{}, err
	}
	return d.entry(), nil
}

// ReleaseUpload moves a claimed upload back to status; a pending upload forgets the document it was claimed for.
func (s *AuthStore) ReleaseUpload(ctx context.Context, id, status string, at time.Time) error {
	_, err := s.fileJournal().UpdateOne(ctx, bson.D{{Key: "_id", Value: id}, {Key: "status", Value: auth.JournalAttaching}},
		bson.A{bson.D{{Key: "$set", Value: bson.D{{Key: "status", Value: status}, {Key: "updated_at", Value: at},
			{Key: "document_id", Value: bson.D{{Key: "$cond", Value: bson.A{bson.D{{Key: "$eq", Value: bson.A{"$pending", true}}}, "$$REMOVE", "$document_id"}}}}}}}})
	return err
}

// CountOpenPendingUploads counts a caller's pending uploads that are still being made or waiting.
func (s *AuthStore) CountOpenPendingUploads(ctx context.Context, callerKey string, now time.Time) (int, error) {
	n, err := s.fileJournal().CountDocuments(ctx, bson.D{{Key: "caller_key", Value: callerKey},
		{Key: "status", Value: bson.D{{Key: "$in", Value: bson.A{auth.JournalWriting, auth.JournalStored}}}}, {Key: "pending_until", Value: bson.D{{Key: "$gt", Value: now}}}})
	return int(n), err
}
