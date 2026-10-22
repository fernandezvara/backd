package mongodb

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
)

func TestThePasswordEpochOnlyMovesForward(t *testing.T) {
	ctx := context.Background()
	s, _ := authFixture(t)
	if e, err := s.PasswordEpoch(ctx); err != nil || !e.ValidAfter.IsZero() {
		t.Fatalf("no epoch yet: %+v %v", e, err)
	}
	t1 := time.Date(2126, 10, 9, 12, 0, 0, 0, time.UTC)
	if ok, err := s.SetPasswordEpoch(ctx, auth.PasswordEpoch{ValidAfter: t1, SetBy: "cli", SetAt: t1, Reason: "leak"}); !ok || err != nil {
		t.Fatalf("first: %v %v", ok, err)
	}
	if ok, err := s.SetPasswordEpoch(ctx, auth.PasswordEpoch{ValidAfter: t1.Add(-time.Hour)}); ok || err != nil {
		t.Errorf("an earlier epoch: %v %v", ok, err)
	}
	if ok, err := s.SetPasswordEpoch(ctx, auth.PasswordEpoch{ValidAfter: t1}); ok || err != nil {
		t.Errorf("the same epoch: %v %v", ok, err)
	}
	t2 := t1.Add(time.Hour)
	if ok, err := s.SetPasswordEpoch(ctx, auth.PasswordEpoch{ValidAfter: t2, SetBy: "api", SetAt: t2}); !ok || err != nil {
		t.Fatalf("a later epoch: %v %v", ok, err)
	}
	e, err := s.PasswordEpoch(ctx)
	if err != nil || !e.ValidAfter.Equal(t2) || e.SetBy != "api" || e.Reason != "" {
		t.Errorf("epoch = %+v %v", e, err)
	}
}

func TestPasswordsRecordWhenTheyChangedAndUsersWhenTheyExpired(t *testing.T) {
	ctx := context.Background()
	s, realm := authFixture(t)
	now := time.Date(2126, 10, 9, 12, 0, 0, 0, time.UTC)
	svc := usersOn(t, s, realm, "master-key-0123456789abcdef-012345", now)
	pw := "dev-p4ssw0rd!"
	u, err := svc.Create(ctx, "ada@example.com", &pw)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.IdentityOf(ctx, u.ID, auth.ProviderPassword)
	if err != nil || !id.PasswordChangedAt.Equal(now) {
		t.Fatalf("created: %+v %v", id, err)
	}
	later := now.Add(time.Hour)
	svc.Now = func() time.Time { return later }
	if err := svc.SetPassword(ctx, u.Email, "dev-p4ssw0rd!2"); err != nil {
		t.Fatal(err)
	}
	if id, _ = s.IdentityOf(ctx, u.ID, auth.ProviderPassword); !id.PasswordChangedAt.Equal(later) || !id.CreatedAt.Equal(now) {
		t.Errorf("replaced: %+v", id)
	}

	later = later.Add(30 * time.Second)
	if err := svc.ExpirePassword(ctx, u.Email, "test"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.UserByID(ctx, u.ID); !got.PasswordExpiredAt.Equal(later) {
		t.Errorf("expired at %v", got.PasswordExpiredAt)
	}
	if _, err := svc.SignIn(ctx, u.Email, "dev-p4ssw0rd!2", ""); !errors.Is(err, auth.ErrPasswordExpired) {
		t.Errorf("sign in: %v", err)
	}
	svc.Now = func() time.Time { return later.Add(time.Minute) }
	if err := svc.SetPassword(ctx, u.Email, "dev-p4ssw0rd!3"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SignIn(ctx, u.Email, "dev-p4ssw0rd!3", ""); err != nil {
		t.Errorf("after a new password: %v", err)
	}
	var zero time.Time
	if err := s.UpdateUser(ctx, u.ID, auth.UserUpdate{PasswordExpiredAt: &zero}, later); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.UserByID(ctx, u.ID); !got.PasswordExpiredAt.IsZero() {
		t.Errorf("not cleared: %v", got.PasswordExpiredAt)
	}
}

func TestEverySessionOfALargeRealmEndsAtOnce(t *testing.T) {
	ctx := context.Background()
	s, realm := authFixture(t)
	now := time.Date(2126, 10, 9, 12, 0, 0, 0, time.UTC)
	svc := usersOn(t, s, realm, "master-key-0123456789abcdef-012345", now)
	pw := "dev-p4ssw0rd!"
	for i := range 3 {
		if _, err := svc.Create(ctx, "user"+string(rune('a'+i))+"@example.com", &pw); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Create(ctx, "nopassword@example.com", nil); err != nil {
		t.Fatal(err)
	}
	const sessions = 2500
	for i := range sessions {
		id := fmt.Sprintf("s%05d", i)
		if err := s.CreateSession(ctx, auth.Session{ID: id, UserID: "u" + id, TokenHash: id, CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	later := now.Add(time.Minute)
	svc.Now = func() time.Time { return later }
	res, err := svc.ExpireAllPasswords(ctx, "test", false)
	if err != nil || res.Accounts != 3 || res.Sessions != sessions {
		t.Fatalf("= %+v, %v", res, err)
	}
	if n, err := svc.RevokeAllSessions(ctx); err != nil || n != 0 {
		t.Errorf("a second time: %d %v", n, err)
	}
}

func TestThePasswordNoticeJobKeepsItsPlaceAndPicksTheExpiredAccounts(t *testing.T) {
	ctx := context.Background()
	s, realm := authFixture(t)
	now := time.Date(2126, 10, 9, 12, 0, 0, 0, time.UTC)
	svc := usersOn(t, s, realm, "master-key-0123456789abcdef-012345", now)
	svc.Settings.Email = &registry.EmailSettings{Function: "app/deliver", From: "a@example.com", Limits: registry.EmailLimits{PerKindPerHour: 100, PerDay: 100, PerIPPerHour: 100}}
	pw := "dev-p4ssw0rd!"
	var ids []string
	for _, e := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		u, err := svc.Create(ctx, e, &pw)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u.ID)
	}
	// One changed the password after the epoch; one has an identity from before the change time was recorded.
	later := now.Add(time.Hour)
	svc.Now = func() time.Time { return later }
	if err := svc.SetPassword(ctx, "c@example.com", "dev-p4ssw0rd!2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.identities().UpdateOne(ctx, bson.D{{Key: "user_id", Value: ids[1]}}, bson.D{{Key: "$unset", Value: bson.D{{Key: "password_changed_at", Value: ""}}}}); err != nil {
		t.Fatal(err)
	}
	epoch := now.Add(30 * time.Minute)
	got, err := s.PasswordUserIDs(ctx, epoch, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{ids[0], ids[1]}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("expired accounts = %v, want %v", got, want)
	}
	if page, _ := s.PasswordUserIDs(ctx, epoch, want[0], 10); len(page) != 1 || page[0] != want[1] {
		t.Errorf("after the first: %v", page)
	}

	// The job round-trips with its place.
	svc.Now = func() time.Time { return later.Add(time.Hour) }
	if _, err := svc.ExpireAllPasswords(ctx, "drill", true); err != nil {
		t.Fatal(err)
	}
	job, err := svc.StartPasswordNotice(ctx, "key:ops", "r")
	if err != nil {
		t.Fatal(err)
	}
	var running *auth.ErrPasswordNoticeRunning
	if _, err := svc.StartPasswordNotice(ctx, "key:ops", "r"); !errors.As(err, &running) || running.JobID != job.ID {
		t.Errorf("a second run: %v", err)
	}
	if err := s.SetPasswordNoticeProgress(ctx, job.ID, ids[0], auth.PasswordNoticeCounts{Queued: 4, Skipped: 2, Deferred: 1}); err != nil {
		t.Fatal(err)
	}
	back, found, err := s.GetJob(ctx, job.ID)
	if err != nil || !found || back.PasswordNotice == nil {
		t.Fatalf("job: %+v %v %v", back, found, err)
	}
	pn := back.PasswordNotice
	if pn.After != ids[0] || pn.Queued != 4 || pn.Skipped != 2 || pn.Deferred != 1 || pn.Reason != "drill" || !pn.Epoch.Equal(later.Add(time.Hour)) {
		t.Errorf("notice = %+v", pn)
	}
	// A batch queues the emails of the due users and marks them told.
	c, err := func() (auth.PasswordNoticeCounts, error) {
		_, c, _, err := svc.NotifyPasswordBatch(ctx, auth.PasswordNoticeJob{Epoch: pn.Epoch})
		return c, err
	}()
	if err != nil || c.Queued != 3 {
		t.Errorf("batch = %+v %v", c, err)
	}
	if u, _ := s.UserByID(ctx, ids[0]); u.PasswordNotifiedAt.IsZero() {
		t.Error("the user wasn't marked as told")
	}
}
