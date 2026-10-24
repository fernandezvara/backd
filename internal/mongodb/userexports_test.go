package mongodb

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

func TestUserExportsOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2126, 10, 1, 12, 0, 0, 0, time.UTC)
	mk := func(id, user, status string, created time.Time) auth.UserExport {
		return auth.UserExport{ID: id, UserID: user, Status: status, JobID: "j-" + id, Locale: "en", CreatedAt: created}
	}
	for _, e := range []auth.UserExport{
		mk("e1", "u1", auth.ExportReady, t0),
		mk("e2", "u1", auth.ExportQueued, t0.Add(time.Hour)),
		mk("e3", "u2", auth.ExportReady, t0.Add(2*time.Hour)),
	} {
		if e.Status == auth.ExportReady {
			e.ExpiresAt = e.CreatedAt.Add(7 * 24 * time.Hour)
			e.Key, e.Size, e.Counts = "k/"+e.ID, 1234, map[string]int64{"files": 2}
		}
		if err := s.CreateUserExport(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.UserExportByID(ctx, "e1")
	if err != nil || got.UserID != "u1" || got.Key != "k/e1" || got.Size != 1234 || got.Counts["files"] != 2 || !got.CreatedAt.Equal(t0) || !got.ExpiresAt.Equal(t0.Add(7*24*time.Hour)) || !got.StartedAt.IsZero() {
		t.Fatalf("by id: %+v %v", got, err)
	}
	if _, err := s.UserExportByID(ctx, "nope"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}

	// Newest first, only the user's.
	list, err := s.ListUserExports(ctx, "u1", 10)
	if err != nil || len(list) != 2 || list[0].ID != "e2" || list[1].ID != "e1" {
		t.Errorf("list: %+v %v", list, err)
	}
	if list, _ := s.ListUserExports(ctx, "u1", 1); len(list) != 1 {
		t.Errorf("limit: %d", len(list))
	}

	// The record is replaced as the job moves on; a record that is not there is not made.
	got.Status, got.Downloads, got.FinishedAt = auth.ExportReady, 2, t0.Add(time.Minute)
	if err := s.ReplaceUserExport(ctx, got); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.UserExportByID(ctx, "e1"); again.Downloads != 2 || !again.FinishedAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("replaced: %+v", again)
	}
	if err := s.ReplaceUserExport(ctx, auth.UserExport{ID: "ghost", UserID: "u1", Status: auth.ExportReady, CreatedAt: t0}); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("replace a ghost: %v", err)
	}

	// Ready exports whose retention ended, oldest first; the queued one has no expiry.
	expired, err := s.ListExpiredUserExports(ctx, t0.Add(7*24*time.Hour+time.Hour), 10)
	if err != nil || len(expired) != 1 || expired[0].ID != "e1" {
		t.Errorf("expired: %+v %v", expired, err)
	}
	expired, _ = s.ListExpiredUserExports(ctx, t0.Add(30*24*time.Hour), 10)
	if len(expired) != 2 || expired[0].ID != "e1" || expired[1].ID != "e3" {
		t.Errorf("all expired: %+v", expired)
	}
	if none, _ := s.ListExpiredUserExports(ctx, t0, 10); len(none) != 0 {
		t.Errorf("not yet expired: %+v", none)
	}
}
