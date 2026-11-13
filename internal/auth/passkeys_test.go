package auth_test

import (
	"context"
	"errors"
	"testing"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/passkey"
	"github.com/fernandezvara/backd/internal/auth/passkey/passkeytest"
	"github.com/fernandezvara/backd/internal/registry"
)

// The glue gives the leaf the realm's configuration, its store and the shared challenge table: a
// registration and a sign-in run end to end on the in-memory store.
func TestPasskeyServiceRunsOnTheRealmsStoreAndChallenges(t *testing.T) {
	ctx := context.Background()
	f := newMFAFixture(t)
	if _, err := f.svc.PasskeyService(); !errors.Is(err, ErrPasskeysDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	f.svc.Settings.Passkeys = registry.PasskeySettings{Enabled: true, RPID: "example.com", RPName: "Example", Origins: []string{"https://app.example.com"}}
	svc, err := f.svc.PasskeyService()
	if err != nil {
		t.Fatal(err)
	}
	a := passkeytest.New("https://app.example.com")
	opts, id, err := svc.BeginRegistration(ctx, passkey.UserInfo{ID: f.user.ID, Name: f.user.Email, DisplayName: f.user.Email})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.FinishRegistration(ctx, id, f.user.ID, a.Register(t, opts), "Phone"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if n, _ := f.store.CountByUser(ctx, f.user.ID); n != 1 {
		t.Errorf("stored %d", n)
	}
	opts, id, err = svc.BeginLogin(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.FinishLogin(ctx, id, a.Assert(t, opts))
	if err != nil || res.UserID != f.user.ID {
		t.Fatalf("sign in: %+v %v", res, err)
	}
	// The state of a ceremony is the challenge table's: single use, and of its own kind.
	if _, err := svc.FinishLogin(ctx, id, a.Assert(t, opts)); !errors.Is(err, passkey.ErrCeremony) {
		t.Errorf("replay: %v", err)
	}
}
