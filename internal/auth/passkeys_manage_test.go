package auth_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/auth/passkey/passkeytest"
	"github.com/fernandezvara/backd/internal/registry"
)

const passkeyOrigin = "https://app.example.com"

func passkeyFixture(t *testing.T) (*Users, *authtest.MemStore, User) {
	t.Helper()
	svc, store, _, u := enrollFixture(t)
	svc.Settings.Passkeys = registry.PasskeySettings{Enabled: true, RPID: "example.com", RPName: "Example", Origins: []string{passkeyOrigin}}
	return svc, store, u
}

// addPasskey runs a whole registration of a new software authenticator for the user.
func addPasskey(t *testing.T, svc *Users, u User, name string) (*passkeytest.Authenticator, PasskeyInfo) {
	t.Helper()
	return addPasskeyWith(t, svc, u, name, nil)
}

// addPasskeyWith is addPasskey for an authenticator set up first (for example one that syncs).
func addPasskeyWith(t *testing.T, svc *Users, u User, name string, set func(*passkeytest.Authenticator)) (*passkeytest.Authenticator, PasskeyInfo) {
	t.Helper()
	a := passkeytest.New(passkeyOrigin)
	if set != nil {
		set(a)
	}
	opts, id, err := svc.BeginPasskeyRegistration(context.Background(), u)
	if err != nil {
		t.Fatal(err)
	}
	info, err := svc.FinishPasskeyRegistration(context.Background(), u, id, a.Register(t, opts), name)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return a, info
}

func TestRegisteringAPasskeyNamesItTellsTheUserAndKeepsTheOtherSessions(t *testing.T) {
	ctx := context.Background()
	svc, _, u := passkeyFixture(t)
	other, _, err := svc.Login(ctx, "ada@example.com", mfaPassword, "")
	if err != nil {
		t.Fatal(err)
	}
	_, info := addPasskey(t, svc, u, "  Phone ")
	if info.Name != "Phone" || info.ID == "" || !info.CreatedAt.IsZero() && info.CreatedAt.After(time.Now().Add(time.Hour)) {
		t.Errorf("info = %+v", info)
	}
	list, _ := svc.ListPasskeys(ctx, u.ID)
	if len(list) != 1 || list[0].ID != info.ID {
		t.Errorf("list = %+v", list)
	}
	if n, _ := svc.PasskeyCount(ctx, u.ID); n != 1 {
		t.Errorf("count = %d", n)
	}
	if !slices.Contains(notices(t, svc), "security-notice:passkey-added") {
		t.Errorf("notices = %v", notices(t, svc))
	}
	// Other sessions stay: registering takes nothing away from how the user signs in.
	if sessions, _ := svc.UserSessions(ctx, u.ID); len(sessions) != 1 || sessions[0].ID != other.Session.ID {
		t.Errorf("sessions = %+v", sessions)
	}
	// The default name, and a name that is too long.
	if _, info2 := addPasskey(t, svc, u, ""); info2.Name != "Passkey" {
		t.Errorf("default name = %q", info2.Name)
	}
	a := passkeytest.New(passkeyOrigin)
	opts, id, _ := svc.BeginPasskeyRegistration(ctx, u)
	var bad *PasskeyNameError
	if _, err := svc.FinishPasskeyRegistration(ctx, u, id, a.Register(t, opts), strings.Repeat("x", 65)); !errors.As(err, &bad) {
		t.Errorf("a long name: %v", err)
	}
}

func TestPasskeysAreOffUntilTheRealmEnablesThem(t *testing.T) {
	svc, _, _, u := enrollFixture(t)
	if _, _, err := svc.BeginPasskeyRegistration(context.Background(), u); !errors.Is(err, ErrPasskeysDisabled) {
		t.Errorf("disabled: %v", err)
	}
	if n, err := svc.PasskeyCount(context.Background(), u.ID); err != nil || n != 0 {
		t.Errorf("count = %d %v", n, err)
	}
}

func TestAUserHasAtMostTenPasskeys(t *testing.T) {
	svc, _, u := passkeyFixture(t)
	for range MaxPasskeys {
		addPasskey(t, svc, u, "k")
	}
	if _, _, err := svc.BeginPasskeyRegistration(context.Background(), u); !errors.Is(err, ErrPasskeyLimit) {
		t.Errorf("the eleventh: %v", err)
	}
}

func TestAnotherUsersCeremonyCannotAddAPasskeyToMe(t *testing.T) {
	ctx := context.Background()
	svc, _, u := passkeyFixture(t)
	bob, err := svc.Create(ctx, "bob@example.com", new(mfaPassword))
	if err != nil {
		t.Fatal(err)
	}
	a := passkeytest.New(passkeyOrigin)
	opts, id, _ := svc.BeginPasskeyRegistration(ctx, bob)
	// Ada finishes Bob's ceremony with a credential of her own: refused, and nothing is stored for her.
	if _, err := svc.FinishPasskeyRegistration(ctx, u, id, a.Register(t, opts), "x"); err == nil {
		t.Fatal("accepted")
	}
	if n, _ := svc.PasskeyCount(ctx, u.ID); n != 0 {
		t.Errorf("stored %d", n)
	}
}

func TestRenameAndRemoveOnlyTouchTheUsersOwnPasskeys(t *testing.T) {
	ctx := context.Background()
	svc, _, u := passkeyFixture(t)
	_, info := addPasskey(t, svc, u, "Phone")
	got, err := svc.RenamePasskey(ctx, u.ID, info.ID, " Laptop ")
	if err != nil || got.Name != "Laptop" {
		t.Fatalf("rename: %+v %v", got, err)
	}
	var bad *PasskeyNameError
	if _, err := svc.RenamePasskey(ctx, u.ID, info.ID, "  "); !errors.As(err, &bad) {
		t.Errorf("empty name: %v", err)
	}
	if _, err := svc.RenamePasskey(ctx, "someone-else", info.ID, "Mine"); !errors.Is(err, ErrNotFound) {
		t.Errorf("another user's passkey: %v", err)
	}
	if err := svc.RemovePasskey(ctx, User{ID: "someone-else"}, info.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove another user's: %v", err)
	}
	if err := svc.RemovePasskey(ctx, u, info.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := svc.PasskeyCount(ctx, u.ID); n != 0 {
		t.Errorf("count after removal = %d", n)
	}
	if !slices.Contains(notices(t, svc), "security-notice:passkey-removed") {
		t.Errorf("notices = %v", notices(t, svc))
	}
}

func TestRemovingAPasskeyEndsTheSessionsItOpened(t *testing.T) {
	ctx := context.Background()
	svc, store, u := passkeyFixture(t)
	_, info := addPasskey(t, svc, u, "Phone")
	_, keep := addPasskey(t, svc, u, "Key")
	// Two sessions: one a passkey opened (sign-in with passkeys comes in a later step, so the record is made
	// as that step will), one opened by the password.
	by, _, _ := svc.Login(ctx, "ada@example.com", mfaPassword, "")
	pw, _, _ := svc.Login(ctx, "ada@example.com", mfaPassword, "")
	s := by.Session
	s.ID, s.TokenHash, s.Passkey = "s-pk", "h-pk", info.ID
	if err := store.CreateSession(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemovePasskey(ctx, u, info.ID); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	sessions, _ := svc.UserSessions(ctx, u.ID)
	for _, x := range sessions {
		ids[x.ID] = true
	}
	if ids["s-pk"] || !ids[pw.Session.ID] || !ids[by.Session.ID] {
		t.Errorf("sessions after the removal: %v (only the passkey's goes)", ids)
	}
	_ = keep
}

func TestTheLastWayToSignInStaysWhenPasskeysAreOne(t *testing.T) {
	ctx := context.Background()
	svc, store, u := passkeyFixture(t)
	// A user with no password: the passkey would be their only way in, once a strategy starts with it.
	if err := store.DeleteIdentity(ctx, u.ID, ProviderPassword); err != nil {
		t.Fatal(err)
	}
	_, info := addPasskey(t, svc, u, "Phone")
	// No strategy starts with a passkey yet: it is a credential, not a way in, so it can go.
	svc.Settings.Strategies = []registry.Strategy{{Name: "pk", Methods: []string{"passkey"}}}
	if err := svc.RemovePasskey(ctx, u, info.ID); !errors.Is(err, ErrLastSignInMethod) {
		t.Fatalf("the last way in: %v", err)
	}
	// With a second passkey, the first can go.
	addPasskey(t, svc, u, "Key")
	if err := svc.RemovePasskey(ctx, u, info.ID); err != nil {
		t.Errorf("one of two: %v", err)
	}
}

func TestAnAdministratorRemovesAPasskeyAndTheUserIsTold(t *testing.T) {
	ctx := context.Background()
	svc, _, u := passkeyFixture(t)
	_, info := addPasskey(t, svc, u, "Lost phone")
	if err := svc.AdminRemovePasskey(ctx, "someone-else", info.ID); err == nil {
		t.Error("another user's passkey")
	}
	if err := svc.AdminRemovePasskey(ctx, u.ID, info.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := svc.PasskeyCount(ctx, u.ID); n != 0 {
		t.Errorf("count = %d", n)
	}
	if !slices.Contains(notices(t, svc), "security-notice:passkey-removed") {
		t.Errorf("notices = %v", notices(t, svc))
	}
}

func TestErasingAnAccountDeletesItsPasskeys(t *testing.T) {
	ctx := context.Background()
	svc, store, u := passkeyFixture(t)
	addPasskey(t, svc, u, "Phone")
	if err := store.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.CountByUser(ctx, u.ID); n != 0 {
		t.Errorf("%d passkeys survive the account", n)
	}
}

func TestTheAccountExportListsPasskeysWithoutTheKey(t *testing.T) {
	ctx := context.Background()
	svc, _, u := passkeyFixture(t)
	addPasskey(t, svc, u, "Phone")
	parts, err := svc.AccountExport(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	for _, p := range parts {
		if p.Name == "passkeys" {
			rows = p.Rows
		}
	}
	if len(rows) != 1 || rows[0]["name"] != "Phone" {
		t.Fatalf("export = %+v", rows)
	}
	for k := range rows[0] {
		if strings.Contains(k, "key") && k != "backup_eligible" {
			t.Errorf("the export carries %q", k)
		}
	}
}
