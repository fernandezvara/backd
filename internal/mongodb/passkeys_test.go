package mongodb

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/passkey"
)

func TestAuthStorePasskeys(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Date(2126, 9, 26, 12, 0, 0, 0, time.UTC)
	if err := s.CreateUser(ctx, auth.User{ID: "u1", Email: "ada@example.com", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	// The handle: 32 random bytes made once, found again by the lookup.
	h, err := s.UserHandle(ctx, "u1")
	if err != nil || len(h) != 32 {
		t.Fatalf("handle: %x %v", h, err)
	}
	if again, _ := s.UserHandle(ctx, "u1"); string(again) != string(h) {
		t.Error("the handle changed")
	}
	if id, err := s.UserByHandle(ctx, h); err != nil || id != "u1" {
		t.Errorf("by handle: %q %v", id, err)
	}
	if _, err := s.UserByHandle(ctx, []byte("nobody")); !errors.Is(err, passkey.ErrUnknownCredential) {
		t.Errorf("unknown handle: %v", err)
	}
	if _, err := s.UserHandle(ctx, "ghost"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("handle of a missing user: %v", err)
	}

	c := passkey.Credential{
		ID: "k1", UserID: "u1", CredentialID: []byte{1, 2, 3}, PublicKey: []byte{9, 9}, RPID: "example.com",
		SignCount: 4, Transports: []string{"internal"}, AAGUID: make([]byte, 16), BackupEligible: true, Name: "Phone", CreatedAt: now,
	}
	if err := s.Add(ctx, c); err != nil {
		t.Fatal(err)
	}
	// The credential id is unique.
	dup := c
	dup.ID = "k2"
	if err := s.Add(ctx, dup); !errors.Is(err, passkey.ErrInvalid) {
		t.Errorf("a credential id registered twice: %v", err)
	}
	got, err := s.ByCredentialID(ctx, []byte{1, 2, 3})
	if err != nil || got.ID != "k1" || got.SignCount != 4 || !got.BackupEligible || got.RPID != "example.com" || got.Name != "Phone" || !got.LastUsedAt.IsZero() {
		t.Fatalf("by credential id: %+v %v", got, err)
	}
	if _, err := s.ByCredentialID(ctx, []byte{7}); !errors.Is(err, passkey.ErrUnknownCredential) {
		t.Errorf("unknown credential: %v", err)
	}

	count, used, yes, name := uint32(5), now.Add(time.Hour), true, "Laptop"
	if err := s.Update(ctx, []byte{1, 2, 3}, passkey.Update{SignCount: &count, LastUsedAt: &used, BackupState: &yes, CloneSuspected: &yes, Name: &name}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ByCredentialID(ctx, []byte{1, 2, 3})
	if got.SignCount != 5 || !got.BackupState || !got.CloneSuspected || got.Name != "Laptop" || !got.LastUsedAt.Equal(used) {
		t.Errorf("after the update: %+v", got)
	}
	if err := s.Update(ctx, []byte{7}, passkey.Update{Name: &name}); !errors.Is(err, passkey.ErrUnknownCredential) {
		t.Errorf("update of an unknown credential: %v", err)
	}

	if n, _ := s.CountByUser(ctx, "u1"); n != 1 {
		t.Errorf("count = %d", n)
	}
	if list, _ := s.ListByUser(ctx, "u1"); len(list) != 1 || list[0].ID != "k1" {
		t.Errorf("list = %+v", list)
	}
	if ids, err := s.PasskeyRPIDs(ctx); err != nil || len(ids) != 1 || ids[0] != "example.com" {
		t.Errorf("rp ids = %v %v", ids, err)
	}
	// Another user's id does not delete it.
	if err := s.Delete(ctx, "u2", "k1"); !errors.Is(err, passkey.ErrUnknownCredential) {
		t.Errorf("delete as another user: %v", err)
	}
	if err := s.Delete(ctx, "u1", "k1"); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountByUser(ctx, "u1"); n != 0 {
		t.Errorf("count after delete = %d", n)
	}
}

func TestAuthStoreEndsThePasskeySessionsAndDeletesPasskeysWithTheUser(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Date(2126, 9, 26, 12, 0, 0, 0, time.UTC)
	if err := s.CreateUser(ctx, auth.User{ID: "u1", Email: "ada@example.com", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for id, passkeyID := range map[string]string{"s1": "k1", "s2": "k1", "s3": "", "s4": "k2"} {
		if err := s.CreateSession(ctx, auth.Session{ID: id, UserID: "u1", TokenHash: "h" + id, CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(time.Hour), Passkey: passkeyID}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.DeleteSessionsOfPasskey(ctx, "u1", "k1")
	if err != nil || n != 2 {
		t.Fatalf("ended %d: %v", n, err)
	}
	left, _ := s.ListSessions(ctx, "u1")
	if len(left) != 2 {
		t.Errorf("sessions left = %d", len(left))
	}
	for _, id := range []string{"k1", "k2"} {
		if err := s.Add(ctx, passkey.Credential{ID: id, UserID: "u1", CredentialID: []byte(id), PublicKey: []byte{1}, RPID: "example.com", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteUser(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountByUser(ctx, "u1"); n != 0 {
		t.Errorf("%d passkeys survive the user", n)
	}
}
