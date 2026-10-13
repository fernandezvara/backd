package mongodb

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

func TestAuthStoreDevices(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	dev := func(id, user, session, hash string, seen time.Duration) auth.Device {
		return auth.Device{ID: id, UserID: user, SessionID: session, Platform: "android", TokenHash: hash, Token: "sealed-" + hash, CreatedAt: now, LastSeen: now.Add(seen)}
	}
	for _, d := range []auth.Device{
		dev("d1", "u1", "s1", "h1", -3*time.Hour), dev("d2", "u1", "s1", "h2", -2*time.Hour), dev("d3", "u1", "s2", "h3", -time.Hour),
	} {
		if _, err := s.PutDevice(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListDevices(ctx, "u1")
	if err != nil || len(list) != 3 || list[0].ID != "d3" || list[2].ID != "d1" {
		t.Fatalf("list: %+v %v", list, err)
	}
	// The same owner re-registering keeps the id and creation time, and refreshes the rest.
	again := dev("dX", "u1", "s2", "h1", 0)
	again.Name = "Phone"
	got, err := s.PutDevice(ctx, again)
	if err != nil || got.ID != "d1" || got.SessionID != "s2" || got.Name != "Phone" || !got.CreatedAt.Equal(now) {
		t.Fatalf("refresh: %+v %v", got, err)
	}
	// Another user's registration of the token moves it: a new id, the old one is dead.
	moved, err := s.PutDevice(ctx, dev("dM", "u2", "s9", "h1", 0))
	if err != nil || moved.ID != "dM" || moved.UserID != "u2" {
		t.Fatalf("move: %+v %v", moved, err)
	}
	if err := s.DeleteDevice(ctx, "u1", "d1"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("the first owner's id: %v", err)
	}
	// The cap: the named device counts as one kept, the most recently seen others fill the rest.
	if n, err := s.DeleteOldestDevices(ctx, "u1", 1, "d2"); err != nil || n != 1 {
		t.Fatalf("oldest: %d %v", n, err)
	}
	list, _ = s.ListDevices(ctx, "u1")
	if len(list) != 1 || list[0].ID != "d2" {
		t.Errorf("after the cap: %+v", list)
	}
	// Ending a session takes its devices.
	_ = s.CreateSession(ctx, auth.Session{ID: "s9", UserID: "u2", TokenHash: "tok9", CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(time.Hour)})
	if err := s.DeleteSession(ctx, "u2", "s9"); err != nil {
		t.Fatal(err)
	}
	if left, _ := s.ListDevices(ctx, "u2"); len(left) != 0 {
		t.Errorf("devices of an ended session: %+v", left)
	}
	// Stale ones, and those whose session is gone or expired.
	_, _ = s.PutDevice(ctx, dev("d4", "u3", "live", "h4", -90*24*time.Hour))
	_, _ = s.PutDevice(ctx, dev("d5", "u3", "live", "h5", 0))
	_, _ = s.PutDevice(ctx, dev("d6", "u3", "expired", "h6", 0))
	_, _ = s.PutDevice(ctx, dev("d7", "u3", "missing", "h7", 0))
	_ = s.CreateSession(ctx, auth.Session{ID: "live", UserID: "u3", TokenHash: "tl", CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(time.Hour)})
	_ = s.CreateSession(ctx, auth.Session{ID: "expired", UserID: "u3", TokenHash: "te", CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(-time.Minute)})
	if n, err := s.DeleteStaleDevices(ctx, now.Add(-60*24*time.Hour)); err != nil || n != 1 {
		t.Fatalf("stale: %d %v", n, err)
	}
	// (d2 of the first part has no session record either.)
	if n, err := s.DeleteOrphanedDevices(ctx, now); err != nil || n != 3 {
		t.Fatalf("orphaned: %d %v", n, err)
	}
	if left, _ := s.ListDevices(ctx, "u3"); len(left) != 1 || left[0].ID != "d5" {
		t.Errorf("left: %+v", left)
	}
	// Revoking all of a user's sessions takes all of their devices; the user's erasure too.
	if _, err := s.DeleteSessions(ctx, "u3"); err != nil {
		t.Fatal(err)
	}
	if left, _ := s.ListDevices(ctx, "u3"); len(left) != 0 {
		t.Errorf("after DeleteSessions: %+v", left)
	}
}
