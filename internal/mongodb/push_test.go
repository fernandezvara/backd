package mongodb

import (
	"context"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/push"
)

func TestAuthStorePushSendCountsOnce(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.CreatePushSend(ctx, auth.PushSend{ID: "ps_1", Kind: "raw", Sender: "function:app/notify", Users: -1, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	// An audience's send is not planned yet: nothing is complete.
	rec, err := s.AddPushSend(ctx, "ps_1", auth.PushDelta{}, now)
	if err != nil || !rec.CompletedAt.IsZero() {
		t.Fatalf("empty delta: %+v %v", rec, err)
	}
	// A batch ends with its totals, once, however often the update runs.
	d := auth.PushDelta{Units: 2, Queued: 150, Limited: 3, NoDevices: 7, DoneBatch: "ps_1-b0"}
	for range 3 {
		if rec, err = s.AddPushSend(ctx, "ps_1", d, now); err != nil {
			t.Fatal(err)
		}
	}
	if rec.BatchesDone != 1 || rec.Units != 2 || rec.Queued != 150 || rec.Limited != 3 || rec.NoDevices != 7 {
		t.Fatalf("batch counted more than once: %+v", rec)
	}
	// Units end once each, with the answers of their devices added as they come.
	if _, err = s.AddPushSend(ctx, "ps_1", auth.PushDelta{Sent: 90, Invalid: 5}, now); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"ps_1-b0-u0", "ps_1-b0-u0", "ps_1-b0-u1"} {
		if rec, err = s.AddPushSend(ctx, "ps_1", auth.PushDelta{Sent: 10, DoneUnit: u}, now); err != nil {
			t.Fatal(err)
		}
	}
	if rec.UnitsDone != 2 || rec.Sent != 120 || rec.Invalid != 5 {
		t.Fatalf("units: %+v", rec)
	}
	// Planned and everything done: completed.
	if err := s.SetPushPlanned(ctx, "ps_1", 1, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if rec, _ = s.PushSendByID(ctx, "ps_1"); !rec.Planned || rec.Batches != 1 || rec.CompletedAt.IsZero() {
		t.Errorf("planned: %+v", rec)
	}
	if _, err := s.PushSendByID(ctx, "nope"); err != auth.ErrPushSendNotFound {
		t.Errorf("unknown send: %v", err)
	}
}

func TestAuthStorePushJobsRoundTrip(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	verified := true
	ttl := time.Hour
	badge := 2
	job := auth.Job{
		ID: "ps_2-plan", Database: "_push", Function: "send", Origin: auth.PushOrigin, Status: auth.JobQueued, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		Push: &auth.PushJob{
			Kind: auth.PushPlan, SendID: "ps_2",
			Messages: map[string]push.Message{"en": {Kind: "raw", Title: "Hi", Body: "There", Data: map[string]string{"a": "b"}, TTL: &ttl, Badge: &badge, Platform: map[string]map[string]any{"ios": {"x": "y"}}}},
			Audience: &auth.PushAudience{Roles: []string{"member"}, EmailVerified: &verified, CreatedBefore: now},
		},
	}
	if err := s.EnqueueJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueJob(ctx, job); err != auth.ErrJobExists {
		t.Errorf("the same id twice: %v", err)
	}
	if err := s.SetPushPlanPosition(ctx, "ps_2-plan", "u123", 4); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetJob(ctx, "ps_2-plan")
	if err != nil || !found || got.Push == nil {
		t.Fatalf("job: %+v %v %v", got, found, err)
	}
	p := got.Push
	m := p.Messages["en"]
	if p.After != "u123" || p.NextBatch != 4 || p.Audience == nil || p.Audience.Roles[0] != "member" || p.Audience.EmailVerified == nil || !*p.Audience.EmailVerified ||
		!p.Audience.CreatedBefore.Equal(now) || m.Title != "Hi" || *m.TTL != time.Hour || *m.Badge != 2 || m.Platform["ios"]["x"] != "y" || m.Data["a"] != "b" {
		t.Errorf("round trip: %+v", p)
	}
	unit := auth.Job{ID: "ps_2-b0-u0", Database: "_push", Function: "send", Status: auth.JobQueued, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		Push: &auth.PushJob{Kind: auth.PushDeliver, SendID: "ps_2", Recipients: []auth.PushRecipient{{UserID: "u1", Locale: "es", Devices: []string{"d1", "d2"}}}, Limited: 1}}
	if err := s.EnqueueJob(ctx, unit); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePushRecipients(ctx, "ps_2-b0-u0", []auth.PushRecipient{{UserID: "u1", Locale: "es", Devices: []string{"d2"}}}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.GetJob(ctx, "ps_2-b0-u0")
	if r := got.Push.Recipients; len(r) != 1 || len(r[0].Devices) != 1 || r[0].Devices[0] != "d2" || got.Push.Limited != 1 {
		t.Errorf("recipients: %+v", got.Push)
	}
}

func TestAuthStoreAudienceAndLiveDevices(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	mk := func(id string, roles []string, verified, disabled bool, locale string, created time.Time) {
		t.Helper()
		if err := s.CreateUser(ctx, auth.User{ID: id, Email: id + "@example.com", Roles: roles, EmailVerified: verified, Disabled: disabled, Locale: locale, CreatedAt: created, UpdatedAt: created}); err != nil {
			t.Fatal(err)
		}
	}
	mk("a1", []string{"member"}, true, false, "en", now.Add(-48*time.Hour))
	mk("a2", []string{"member", "staff"}, false, false, "es", now.Add(-24*time.Hour))
	mk("a3", nil, true, false, "en", now.Add(-time.Hour))
	mk("a4", []string{"member"}, true, true, "en", now)
	yes := true
	ids := func(a auth.PushAudience, after string, limit int) []string {
		t.Helper()
		out, err := s.AudienceUserIDs(ctx, a, after, limit)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := ids(auth.PushAudience{All: true}, "", 10); len(got) != 3 || got[0] != "a1" || got[2] != "a3" {
		t.Errorf("all (disabled is out): %v", got)
	}
	if got := ids(auth.PushAudience{All: true}, "a1", 1); len(got) != 1 || got[0] != "a2" {
		t.Errorf("paging: %v", got)
	}
	if got := ids(auth.PushAudience{Roles: []string{"staff", "member"}}, "", 10); len(got) != 2 {
		t.Errorf("roles: %v", got)
	}
	if got := ids(auth.PushAudience{EmailVerified: &yes, Locale: "en"}, "", 10); len(got) != 2 {
		t.Errorf("verified and locale: %v", got)
	}
	if got := ids(auth.PushAudience{CreatedBefore: now.Add(-12 * time.Hour), CreatedAfter: now.Add(-72 * time.Hour)}, "", 10); len(got) != 2 {
		t.Errorf("created: %v", got)
	}
	users, err := s.UsersByIDs(ctx, []string{"a1", "a3", "nope"})
	if err != nil || len(users) != 2 {
		t.Errorf("users by ids: %v %v", users, err)
	}
	// Only devices of live sessions.
	_ = s.CreateSession(ctx, auth.Session{ID: "live", UserID: "a1", TokenHash: "t1", CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(time.Hour)})
	_ = s.CreateSession(ctx, auth.Session{ID: "dead", UserID: "a1", TokenHash: "t2", CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(-time.Hour)})
	for _, d := range []struct{ id, session, hash string }{{"d1", "live", "h1"}, {"d2", "dead", "h2"}, {"d3", "gone", "h3"}} {
		if _, err := s.PutDevice(ctx, auth.Device{ID: d.id, UserID: "a1", SessionID: d.session, Platform: "android", TokenHash: d.hash, Token: "x", CreatedAt: now, LastSeen: now}); err != nil {
			t.Fatal(err)
		}
	}
	live, err := s.LiveDevicesOf(ctx, []string{"a1", "a2"}, now)
	if err != nil || len(live["a1"]) != 1 || live["a1"][0].ID != "d1" || len(live["a2"]) != 0 {
		t.Errorf("live devices: %v %v", live, err)
	}
	if got, _ := s.DevicesByIDs(ctx, []string{"d1", "d2", "nope"}); len(got) != 2 {
		t.Errorf("by ids: %v", got)
	}
	_ = s.DeleteDeviceByID(ctx, "d1")
	if got, _ := s.DevicesByIDs(ctx, []string{"d1"}); len(got) != 0 {
		t.Errorf("deleted: %v", got)
	}
}
