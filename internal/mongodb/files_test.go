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
	stale, err := s.StaleFileJournal(ctx, t0.Add(time.Hour), 10)
	if err != nil || len(stale) != 2 || stale[0].ID != "fl_old" || stale[1].ID != "fl_stored" {
		t.Fatalf("stale: %+v %v", stale, err)
	}
	if err := s.SetFileJournalStatus(ctx, "fl_old", auth.JournalAttached, "doc1", t0.Add(3*time.Hour), t0.Add(90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFileJournalStatus(ctx, "fl_ghost", auth.JournalFailed, "", t0, t0); err == nil {
		t.Error("a status for an unknown upload")
	}
	if stale, _ := s.StaleFileJournal(ctx, t0.Add(time.Hour), 10); len(stale) != 1 || stale[0].ID != "fl_stored" {
		t.Errorf("after attaching one: %+v", stale)
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
