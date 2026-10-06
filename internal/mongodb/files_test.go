package mongodb

import (
	"context"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

func TestFileJournalOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2126, 10, 7, 12, 0, 0, 0, time.UTC)
	entry := func(id, status string, at time.Time) auth.FileJournalEntry {
		return auth.FileJournalEntry{ID: id, Database: "app", Collection: "notes", Field: "avatar", Key: "p/r/app/notes/" + id, Size: 3, Status: status, CreatedAt: at, UpdatedAt: at, ExpiresAt: at.Add(24 * time.Hour)}
	}
	for _, e := range []auth.FileJournalEntry{entry("fl_old", auth.JournalWriting, t0), entry("fl_stored", auth.JournalStored, t0.Add(time.Minute)), entry("fl_new", auth.JournalWriting, t0.Add(2*time.Hour)), entry("fl_done", auth.JournalAttached, t0)} {
		if err := s.JournalFile(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	stale, err := s.StaleFileJournal(ctx, t0.Add(time.Hour), t0.Add(time.Hour), 10)
	if err != nil || len(stale) != 2 || stale[0].ID != "fl_old" || stale[1].ID != "fl_stored" {
		t.Fatalf("stale: %+v %v", stale, err)
	}
	if err := s.SetFileJournalStatus(ctx, "fl_old", auth.JournalAttached, "doc1", t0.Add(3*time.Hour), t0.Add(90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFileJournalStatus(ctx, "fl_ghost", auth.JournalFailed, "", t0, t0); err == nil {
		t.Error("a status for an unknown upload")
	}
	if stale, _ := s.StaleFileJournal(ctx, t0.Add(time.Hour), t0.Add(time.Hour), 10); len(stale) != 1 || stale[0].ID != "fl_stored" {
		t.Errorf("after attaching one: %+v", stale)
	}
}

func TestPendingUploadsOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2126, 10, 7, 12, 0, 0, 0, time.UTC)
	until := t0.Add(time.Hour)
	for _, id := range []string{"fl_p1", "fl_p2"} {
		e := auth.FileJournalEntry{ID: id, Database: "app", Collection: "forms", Field: "scan", Key: "k/" + id, Status: auth.JournalWriting, CreatedAt: t0, UpdatedAt: t0, ExpiresAt: until.Add(24 * time.Hour),
			Pending: true, TokenHash: auth.HashUploadToken("fut_" + id), CallerKey: "user:u1", PendingUntil: until}
		if err := s.JournalFile(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := s.CountOpenPendingUploads(ctx, "user:u1", t0); n != 2 {
		t.Errorf("open uploads: %d", n)
	}
	if n, _ := s.CountOpenPendingUploads(ctx, "user:other", t0); n != 0 {
		t.Errorf("another caller's: %d", n)
	}
	// Not ready until it is completed.
	if _, err := s.ClaimPendingUpload(ctx, "fl_p1", auth.HashUploadToken("fut_fl_p1"), "doc1", t0); err == nil {
		t.Error("claimed an upload still being written")
	}
	if err := s.CompletePendingUpload(ctx, "fl_p1", "scan.png", "image/png", "abc", 12, t0, t0); err != nil {
		t.Fatal(err)
	}
	if e, err := s.FileJournalEntry(ctx, "fl_p1"); err != nil || e.Status != auth.JournalStored || e.Name != "scan.png" || e.Size != 12 || e.SHA256 != "abc" || !e.Pending {
		t.Fatalf("completed: %+v %v", e, err)
	}
	if _, err := s.ClaimPendingUpload(ctx, "fl_p1", auth.HashUploadToken("wrong"), "doc1", t0); err == nil {
		t.Error("claimed with the wrong token")
	}
	if _, err := s.ClaimPendingUpload(ctx, "fl_p1", auth.HashUploadToken("fut_fl_p1"), "doc1", until.Add(time.Second)); err == nil {
		t.Error("claimed an expired upload")
	}
	e, err := s.ClaimPendingUpload(ctx, "fl_p1", auth.HashUploadToken("fut_fl_p1"), "doc1", t0)
	if err != nil || e.Status != auth.JournalAttaching || e.DocumentID != "doc1" {
		t.Fatalf("claim: %+v %v", e, err)
	}
	if _, err := s.ClaimPendingUpload(ctx, "fl_p1", auth.HashUploadToken("fut_fl_p1"), "doc2", t0); err == nil {
		t.Error("claimed twice")
	}
	if err := s.ReleaseUpload(ctx, "fl_p1", auth.JournalStored, t0); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.FileJournalEntry(ctx, "fl_p1"); e.Status != auth.JournalStored || e.DocumentID != "" {
		t.Errorf("released: %+v", e)
	}
	// Stale: expired and unused, whatever its last update; an attaching one past the grace period.
	stale, _ := s.StaleFileJournal(ctx, t0.Add(-time.Hour), until.Add(time.Minute), 10)
	if len(stale) != 2 {
		t.Errorf("expired pending: %+v", stale)
	}
	if stale, _ := s.StaleFileJournal(ctx, t0.Add(-time.Hour), t0, 10); len(stale) != 0 {
		t.Errorf("unexpired pending reported: %+v", stale)
	}
}

func TestDirectUploadsOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2126, 10, 7, 12, 0, 0, 0, time.UTC)
	until := t0.Add(3 * time.Hour)
	e := auth.FileJournalEntry{ID: "fl_d1", Database: "app", Collection: "docs", Field: "video", DocumentID: "doc1", Key: "k/fl_d1", Status: auth.JournalWriting,
		CreatedAt: t0, UpdatedAt: t0, ExpiresAt: until.Add(24 * time.Hour), Direct: true, TokenHash: auth.HashUploadToken("fut_d1"), CallerKey: "user:u1", PendingUntil: until,
		Name: "v.mp4", Type: "video/mp4", SHA256: "abc", Size: 10}
	if err := s.JournalFile(ctx, e); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountOpenPendingUploads(ctx, "user:u1", t0); n != 1 {
		t.Errorf("open: %d", n)
	}
	// Waiting for its bytes for hours is not abandoned at the one-hour grace: it may be used until it expires.
	if stale, _ := s.StaleFileJournal(ctx, t0.Add(2*time.Hour), t0.Add(2*time.Hour), 10); len(stale) != 0 {
		t.Errorf("a live direct upload is stale: %+v", stale)
	}
	if stale, _ := s.StaleFileJournal(ctx, t0, until.Add(time.Minute), 10); len(stale) != 1 {
		t.Errorf("an expired direct upload isn't stale: %+v", stale)
	}
	if _, err := s.ClaimDirectUpload(ctx, "fl_d1", auth.HashUploadToken("nope"), t0); err == nil {
		t.Error("claimed with the wrong token")
	}
	got, err := s.ClaimDirectUpload(ctx, "fl_d1", auth.HashUploadToken("fut_d1"), t0)
	if err != nil || got.Status != auth.JournalAttaching || got.Name != "v.mp4" || got.SHA256 != "abc" || got.Type != "video/mp4" || !got.Direct {
		t.Fatalf("claim: %+v %v", got, err)
	}
	if _, err := s.ClaimDirectUpload(ctx, "fl_d1", auth.HashUploadToken("fut_d1"), t0); err == nil {
		t.Error("claimed twice")
	}
	if err := s.ReleaseUpload(ctx, "fl_d1", auth.JournalWriting, t0); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.FileJournalEntry(ctx, "fl_d1"); e.Status != auth.JournalWriting || e.DocumentID != "doc1" {
		t.Errorf("released: %+v", e)
	}
}

func TestFileDeletionsOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2126, 10, 7, 12, 0, 0, 0, time.UTC)
	for _, k := range []string{"k1", "k2", "k2"} { // the same key twice is one deletion
		if err := s.QueueFileDeletion(ctx, auth.FileDeletion{Key: k, Reason: "replaced", NotBefore: t0, CreatedAt: t0}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.QueueFileDeletion(ctx, auth.FileDeletion{Key: "later", Reason: "cleared", NotBefore: t0.Add(time.Hour), CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ClaimFileDeletions(ctx, t0, t0.Add(5*time.Minute), 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("claim: %+v %v", got, err)
	}
	// Claimed ones are held for the lease: another worker gets none of them.
	if again, _ := s.ClaimFileDeletions(ctx, t0.Add(time.Minute), t0.Add(6*time.Minute), 10); len(again) != 0 {
		t.Errorf("claimed twice: %+v", again)
	}
	if err := s.CompleteFileDeletion(ctx, "k1"); err != nil {
		t.Fatal(err)
	}
	if err := s.RetryFileDeletion(ctx, "k2", t0.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// After the lease nothing of k1 remains; k2 waits for its retry; "later" is due at last.
	if due, _ := s.ClaimFileDeletions(ctx, t0.Add(10*time.Minute), t0.Add(15*time.Minute), 10); len(due) != 0 {
		t.Errorf("not yet due: %+v", due)
	}
	due, _ := s.ClaimFileDeletions(ctx, t0.Add(2*time.Hour), t0.Add(3*time.Hour), 10)
	if len(due) != 2 || (due[0].Key != "k2" && due[1].Key != "k2") {
		t.Fatalf("due: %+v", due)
	}
	for _, d := range due {
		if d.Key == "k2" && d.Attempts != 1 {
			t.Errorf("attempts: %+v", d)
		}
	}
}

func TestStorageUsageOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	for _, c := range []struct {
		owner string
		bytes int64
		files int64
	}{{"u1", 100, 1}, {"u1", 50, 1}, {"u2", 400, 1}, {"", 7, 1}, {"u1", -100, -1}} {
		if err := s.AddStorageUsage(ctx, c.owner, c.bytes, c.files); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.StorageUsage(ctx, 10)
	if err != nil || got.Realm.Bytes != 457 || got.Realm.Files != 3 {
		t.Fatalf("realm: %+v %v", got, err)
	}
	if len(got.Users) != 2 || got.Users[0].UserID != "u2" || got.Users[0].Bytes != 400 || got.Users[1].UserID != "u1" || got.Users[1].Bytes != 50 || got.Users[1].Files != 1 {
		t.Errorf("users: %+v", got.Users)
	}
	if top, _ := s.StorageUsage(ctx, 1); len(top.Users) != 1 || top.Users[0].UserID != "u2" {
		t.Errorf("the top one: %+v", top.Users)
	}

	t0 := time.Date(2126, 10, 7, 12, 0, 0, 0, time.UTC)
	if st, err := s.FileDeletionStats(ctx); err != nil || st.Queued != 0 || !st.Oldest.IsZero() {
		t.Errorf("empty queue: %+v %v", st, err)
	}
	for i, k := range []string{"a", "b"} {
		if err := s.QueueFileDeletion(ctx, auth.FileDeletion{Key: k, Reason: "x", NotBefore: t0, CreatedAt: t0.Add(time.Duration(i) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.RetryFileDeletion(ctx, "b", t0)
	st, err := s.FileDeletionStats(ctx)
	if err != nil || st.Queued != 2 || st.Retrying != 1 || !st.Oldest.Equal(t0) {
		t.Errorf("queue: %+v %v", st, err)
	}
}
