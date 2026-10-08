package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"time"
)

// The bookkeeping of a realm's files (roadmap Phase 10). Every upload is
// journaled before a byte is stored and updated as it goes, so a process killed
// in the middle leaves a trace a worker cleans up; an object that nothing
// references any more is queued for deletion and deleted with retries. The
// bucket is never listed to find them.

// Journal states of an upload.
const (
	JournalWriting   = "writing"   // the upload started; the object may be partly stored
	JournalStored    = "stored"    // the object is stored; the document doesn't reference it yet
	JournalAttaching = "attaching" // a write is attaching a pending upload to a document
	JournalAttached  = "attached"  // a document references it
	JournalFailed    = "failed"    // it never became part of a document; its object was queued for deletion
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

	// A pending upload is one made before the document that will hold it exists
	// (or without the document being touched): it is attached by naming it, with
	// its token, in a write. Its details are kept here until then.
	Pending      bool
	Direct       bool      // a direct upload: the client sends the bytes to the bucket and completes it
	TokenHash    string    // SHA-256 of the secret token the creator holds
	Owner        string    // the user who made it; "" for an anonymous caller
	CallerKey    string    // who the open-uploads limit counts it for
	PendingUntil time.Time // unused, it expires then (also set for a direct upload to a document)
	Name         string
	Type         string
	SHA256       string
	UploadedAt   time.Time
	// Image is a pending upload's image details as JSON (width, height and the state of each
	// declared version), for the document that will hold it.
	Image string

	// A multipart direct upload (a file above one signed PUT): the storage's upload id, the
	// part size and the SHA-256 of each part as declared at the start. SHA256 is then their
	// composite, which storage can verify.
	MultipartID string
	PartSize    int64
	PartSHA256  []string
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

// MaxOpenPendingUploads is how many unused pending uploads one caller may hold.
const MaxOpenPendingUploads = 20

// NewUploadToken makes the secret a pending upload's creator holds, and the hash
// that is stored instead of it.
func NewUploadToken() (token, hash string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token = "fut_" + base64.RawURLEncoding.EncodeToString(b)
	return token, HashUploadToken(token)
}

// HashUploadToken is what is stored of an upload token.
func HashUploadToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// JournalPendingUpload records a pending upload that is starting; unused, it
// expires after ttl.
func (s *Users) JournalPendingUpload(ctx context.Context, e FileJournalEntry, ttl time.Duration) error {
	now := s.now()
	e.Pending, e.PendingUntil = true, now.Add(ttl)
	e.Status, e.CreatedAt, e.UpdatedAt = JournalWriting, now, now
	e.ExpiresAt = e.PendingUntil.Add(fileJournalFailedKeep)
	return s.Store.JournalFile(ctx, e)
}

// CompletePendingUpload records what was stored: the upload is now ready to be attached.
func (s *Users) CompletePendingUpload(ctx context.Context, id, name, contentType, sha256sum string, size int64, uploadedAt time.Time, image string) error {
	return s.Store.CompletePendingUpload(ctx, id, name, contentType, sha256sum, size, uploadedAt, image, s.now())
}

// UploadEntry returns an upload's record whatever kind it is, or ErrNotFound.
func (s *Users) UploadEntry(ctx context.Context, id string) (FileJournalEntry, error) {
	return s.Store.FileJournalEntry(ctx, id)
}

// PendingUpload returns a pending upload's record, or ErrNotFound.
func (s *Users) PendingUpload(ctx context.Context, id string) (FileJournalEntry, error) {
	e, err := s.Store.FileJournalEntry(ctx, id)
	if err != nil {
		return e, err
	}
	if !e.Pending {
		return FileJournalEntry{}, ErrNotFound
	}
	return e, nil
}

// ClaimPendingUpload takes a completed, unexpired pending upload whose token
// matches, once: it is held as attaching to the document until it is attached or
// released. ErrNotFound when there is none to take.
func (s *Users) ClaimPendingUpload(ctx context.Context, id, token, documentID string) (FileJournalEntry, error) {
	return s.Store.ClaimPendingUpload(ctx, id, HashUploadToken(token), documentID, s.now())
}

// ReleasePendingUpload gives a claimed pending upload back, when the write that
// claimed it didn't happen.
func (s *Users) ReleasePendingUpload(ctx context.Context, id string) error {
	return s.Store.ReleaseUpload(ctx, id, JournalStored, s.now())
}

// JournalDirectUpload records a direct upload that is starting: the client will send
// the bytes to the bucket and complete it before it expires after ttl. A pending one
// (e.Pending) is attached later by a write; the others belong to a document.
func (s *Users) JournalDirectUpload(ctx context.Context, e FileJournalEntry, ttl time.Duration) error {
	now := s.now()
	e.Direct, e.PendingUntil = true, now.Add(ttl)
	e.Status, e.CreatedAt, e.UpdatedAt = JournalWriting, now, now
	e.ExpiresAt = e.PendingUntil.Add(fileJournalFailedKeep)
	return s.Store.JournalFile(ctx, e)
}

// ClaimDirectUpload takes a direct upload to verify and complete it, once: held as
// attaching until it is done or released. ErrNotFound when the token doesn't match, the
// upload isn't one that waits for its bytes, or it expired.
func (s *Users) ClaimDirectUpload(ctx context.Context, id, token string) (FileJournalEntry, error) {
	return s.Store.ClaimDirectUpload(ctx, id, HashUploadToken(token), s.now())
}

// ReleaseDirectUpload gives a claimed direct upload back, to wait for its bytes again.
func (s *Users) ReleaseDirectUpload(ctx context.Context, id string) error {
	return s.Store.ReleaseUpload(ctx, id, JournalWriting, s.now())
}

// OpenPendingUploads counts the unused pending uploads a caller holds.
func (s *Users) OpenPendingUploads(ctx context.Context, callerKey string) (int, error) {
	return s.Store.CountOpenPendingUploads(ctx, callerKey, s.now())
}

// QueueFileDeletion queues an object for deletion; queueing the same key twice
// is one deletion.
func (s *Users) QueueFileDeletion(ctx context.Context, key, reason string) error {
	now := s.now()
	return s.Store.QueueFileDeletion(ctx, FileDeletion{Key: key, Reason: reason, NotBefore: now, CreatedAt: now})
}

// AbandonedUploads returns uploads left writing, stored or attaching for longer
// than the grace period, and pending uploads nobody used before they expired.
func (s *Users) AbandonedUploads(ctx context.Context, limit int) ([]FileJournalEntry, error) {
	now := s.now()
	return s.Store.StaleFileJournal(ctx, now.Add(-FileJournalGrace), now, limit)
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

// StorageUsageTotals is bytes and files a scope holds.
type StorageUsageTotals struct {
	Bytes int64
	Files int64
}

// UserUsage is what the files of the documents a user owns add up to.
type UserUsage struct {
	UserID string
	StorageUsageTotals
}

// StorageUsage is a realm's running totals: all of it, and the users holding the most.
type StorageUsage struct {
	Realm StorageUsageTotals
	Users []UserUsage
}

// FileDeletionStats describes the deletion queue.
type FileDeletionStats struct {
	Queued   int       // objects waiting to be deleted
	Retrying int       // of them, the ones that failed at least once
	Oldest   time.Time // when the oldest was queued; zero when empty
}

// FileAdded counts a file a document now references, for the realm and, when the
// document has an owner, for them.
func (s *Users) FileAdded(ctx context.Context, owner string, size int64) error {
	return s.Store.AddStorageUsage(ctx, owner, size, 1)
}

// FileRemoved counts a file no document references any more.
func (s *Users) FileRemoved(ctx context.Context, owner string, size int64) error {
	return s.Store.AddStorageUsage(ctx, owner, -size, -1)
}

// StorageUsage returns the running totals: the realm's and the limit users holding the most.
func (s *Users) StorageUsage(ctx context.Context, limit int) (StorageUsage, error) {
	return s.Store.StorageUsage(ctx, limit)
}

// FileDeletionStats describes what waits to be deleted.
func (s *Users) FileDeletionStats(ctx context.Context) (FileDeletionStats, error) {
	return s.Store.FileDeletionStats(ctx)
}

// StorageUsageOf returns the realm's totals and those of one user, for the quotas.
func (s *Users) StorageUsageOf(ctx context.Context, owner string) (realm, user StorageUsageTotals, err error) {
	return s.Store.StorageUsageOf(ctx, owner)
}

// ImageDeclarations returns the fingerprints recorded for the declared image versions.
func (s *Users) ImageDeclarations(ctx context.Context) (map[string]string, error) {
	return s.Store.ImageDeclarations(ctx)
}

// SetImageDeclaration records a declared version's fingerprint.
func (s *Users) SetImageDeclaration(ctx context.Context, key, fingerprint string) error {
	return s.Store.SetImageDeclaration(ctx, key, fingerprint)
}
