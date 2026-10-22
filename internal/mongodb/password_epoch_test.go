package mongodb

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
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
