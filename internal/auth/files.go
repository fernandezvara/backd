package auth

import (
	"context"
	"time"
)

// The bookkeeping of a realm's files (roadmap Phase 10). Every upload is
// journaled before a byte is stored and updated as it goes, so a process killed
// in the middle leaves a trace a worker cleans up; an object that nothing
// references any more is queued for deletion and deleted with retries. The
// bucket is never listed to find them.

// Journal states of an upload.
const (
	JournalWriting  = "writing"  // the upload started; the object may be partly stored
	JournalStored   = "stored"   // the object is stored; the document doesn't reference it yet
	JournalAttached = "attached" // a document references it
	JournalFailed   = "failed"   // it never became part of a document; its object was queued for deletion
)

const (
	// FileJournalGrace is how long an upload may stay writing or stored before a
	// worker treats it as abandoned.
	FileJournalGrace = time.Hour
	// fileJournalKeep is how long the journal keeps what happened to an upload.
	fileJournalAttachedKeep = 90 * 24 * time.Hour
	fileJournalFailedKeep   = 7 * 24 * time.Hour
	// FileDeletionLease is how long a worker owns a deletion it claimed.
	FileDeletionLease = 5 * time.Minute
)

// FileJournalEntry is one upload.
type FileJournalEntry struct {
	ID         string // the file's id (fl_…)
	Database   string
	Collection string
	Field      string
	DocumentID string // "" for an upload no document references yet
	Key        string // the object's key
	Caller     string // who uploaded it, for display (user:<id>, key:<name>, func:…)
	Size       int64
	Status     string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ExpiresAt  time.Time
}

// FileDeletion is an object to delete from the bucket.
type FileDeletion struct {
	Key       string
	Reason    string // replaced, cleared, removed, abandoned…
	Attempts  int
	NotBefore time.Time // not claimed before this
	CreatedAt time.Time
}

// JournalUpload records an upload that is starting.
func (s *Users) JournalUpload(ctx context.Context, e FileJournalEntry) error {
	now := s.now()
	e.Status, e.CreatedAt, e.UpdatedAt = JournalWriting, now, now
	e.ExpiresAt = now.Add(FileJournalGrace + fileJournalFailedKeep)
	return s.Store.JournalFile(ctx, e)
}

// SetUploadStatus moves an upload through its states (JournalStored, then
// JournalAttached or JournalFailed).
func (s *Users) SetUploadStatus(ctx context.Context, id, status, documentID string) error {
	now := s.now()
	expires := now.Add(fileJournalFailedKeep)
	if status == JournalAttached {
		expires = now.Add(fileJournalAttachedKeep)
	}
	return s.Store.SetFileJournalStatus(ctx, id, status, documentID, now, expires)
}

// QueueFileDeletion queues an object for deletion; queueing the same key twice
// is one deletion.
func (s *Users) QueueFileDeletion(ctx context.Context, key, reason string) error {
	now := s.now()
	return s.Store.QueueFileDeletion(ctx, FileDeletion{Key: key, Reason: reason, NotBefore: now, CreatedAt: now})
}

// AbandonedUploads returns uploads left writing or stored for longer than the
// grace period.
func (s *Users) AbandonedUploads(ctx context.Context, limit int) ([]FileJournalEntry, error) {
	return s.Store.StaleFileJournal(ctx, s.now().Add(-FileJournalGrace), limit)
}

// ClaimFileDeletions returns up to limit deletions that are due, holding them for
// FileDeletionLease so no other worker takes them meanwhile.
func (s *Users) ClaimFileDeletions(ctx context.Context, limit int) ([]FileDeletion, error) {
	now := s.now()
	return s.Store.ClaimFileDeletions(ctx, now, now.Add(FileDeletionLease), limit)
}

// FinishFileDeletion removes a deletion that was carried out.
func (s *Users) FinishFileDeletion(ctx context.Context, key string) error {
	return s.Store.CompleteFileDeletion(ctx, key)
}

// RetryFileDeletion keeps a deletion that failed, to be claimed again after wait.
func (s *Users) RetryFileDeletion(ctx context.Context, key string, wait time.Duration) error {
	return s.Store.RetryFileDeletion(ctx, key, s.now().Add(wait))
}
