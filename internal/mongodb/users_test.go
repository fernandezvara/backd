package mongodb

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
)

// The clocks of these tests are fixed dates in the year 2126, not the past:
// MongoDB's TTL monitor deletes a record whose expires_at has passed, which
// would remove records out from under a test (an idempotency claim that was
// "running" a moment ago) as the calendar moves on.

// authFixture provisions an auth-enabled realm and returns its store.
func authFixture(t *testing.T) (*AuthStore, string) {
	t.Helper()
	client := testClient(t)
	realm := testRealm(t, client)
	log, _ := testLogger()
	reg := loadRegistryWith(t, realm, "", map[string]string{"app/notes": `{}`})
	if err := (&Provisioner{Client: client, Registry: reg, Log: log}).Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	return NewAuthStore(client, realm), realm
}

func TestAuthStoreUsers(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Date(2126, 9, 26, 12, 0, 0, 0, time.UTC)

	for _, email := range []string{"bob@example.com", "ada@example.com"} {
		if err := s.CreateUser(ctx, auth.User{ID: "id-" + email[:3], Email: email, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatalf("CreateUser(%s): %v", email, err)
		}
	}
	if err := s.CreateUser(ctx, auth.User{ID: "other", Email: "ada@example.com", CreatedAt: now, UpdatedAt: now}); !errors.Is(err, auth.ErrEmailTaken) {
		t.Errorf("duplicate email: %v", err)
	}

	u, err := s.UserByEmail(ctx, "ada@example.com")
	if err != nil || u.ID != "id-ada" || u.Roles == nil || !u.CreatedAt.Equal(now) {
		t.Errorf("UserByEmail = %+v, %v", u, err)
	}
	if _, err := s.UserByEmail(ctx, "nobody@example.com"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("unknown email: %v", err)
	}
	if u, err := s.UserByID(ctx, "id-ada"); err != nil || u.Email != "ada@example.com" {
		t.Errorf("UserByID = %+v, %v", u, err)
	}
	if _, err := s.UserByID(ctx, "nope"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}

	list, err := s.ListUsers(ctx)
	if err != nil || len(list) != 2 || list[0].Email != "ada@example.com" {
		t.Errorf("ListUsers = %+v, %v", list, err)
	}

	later := now.Add(time.Hour)
	yes := true
	if err := s.UpdateUser(ctx, "id-ada", auth.UserUpdate{EmailVerified: &yes, Disabled: &yes}, later); err != nil {
		t.Fatal(err)
	}
	u, _ = s.UserByEmail(ctx, "ada@example.com")
	if !u.EmailVerified || !u.Disabled || !u.UpdatedAt.Equal(later) {
		t.Errorf("after update: %+v", u)
	}
	if err := s.UpdateUser(ctx, "missing", auth.UserUpdate{Disabled: &yes}, later); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("update missing user: %v", err)
	}

	// Network restrictions round-trip, and an empty list removes them.
	nets, _ := registry.ParseNetworks([]string{"10.0.0.0/8", "2001:db8::1"})
	if err := s.UpdateUser(ctx, "id-ada", auth.UserUpdate{AdminNetworks: &nets, LoginNetworks: &nets}, later); err != nil {
		t.Fatal(err)
	}
	u, _ = s.UserByID(ctx, "id-ada")
	if !u.AdminNetworks.Equal(nets) || !u.LoginNetworks.Equal(nets) {
		t.Errorf("networks = %v / %v", u.AdminNetworks, u.LoginNetworks)
	}
	none := registry.Networks{}
	if err := s.UpdateUser(ctx, "id-ada", auth.UserUpdate{LoginNetworks: &none}, later); err != nil {
		t.Fatal(err)
	}
	u, _ = s.UserByID(ctx, "id-ada")
	if !u.AdminNetworks.Equal(nets) || len(u.LoginNetworks) != 0 {
		t.Errorf("after clearing login networks: %v / %v", u.AdminNetworks, u.LoginNetworks)
	}

	for range 2 {
		if err := s.AddRoles(ctx, "id-ada", []string{"admin", "editor"}, later); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RemoveRole(ctx, "id-ada", "admin", later); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.UserByEmail(ctx, "ada@example.com"); !reflect.DeepEqual(u.Roles, []string{"editor"}) {
		t.Errorf("roles = %v", u.Roles)
	}
	if err := s.AddRoles(ctx, "missing", []string{"x"}, later); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("add role to missing user: %v", err)
	}
}

func TestAuthStoreIdentitiesAndDelete(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Date(2126, 9, 26, 12, 0, 0, 0, time.UTC)
	if err := s.CreateUser(ctx, auth.User{ID: "u1", Email: "ada@example.com", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	first := auth.Identity{ID: "i1", UserID: "u1", Provider: auth.ProviderPassword, Subject: "u1", PasswordHash: "$argon2id$first", CreatedAt: now, UpdatedAt: now}
	if err := s.PutIdentity(ctx, first); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Hour)
	if err := s.PutIdentity(ctx, auth.Identity{ID: "i2", UserID: "u1", Provider: auth.ProviderPassword, Subject: "u1", PasswordHash: "$argon2id$second", CreatedAt: later, UpdatedAt: later}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Identity(ctx, auth.ProviderPassword, "u1")
	if err != nil || got.ID != "i1" || !got.CreatedAt.Equal(now) || !got.UpdatedAt.Equal(later) || got.PasswordHash != "$argon2id$second" {
		t.Errorf("identity after second put = %+v, %v", got, err)
	}
	if _, err := s.Identity(ctx, auth.ProviderPassword, "nobody"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("unknown identity: %v", err)
	}

	// A session to be removed with the user.
	if _, err := s.sessions().InsertOne(ctx, bson.D{
		{Key: "_id", Value: "s1"}, {Key: "token_hash", Value: "h1"}, {Key: "user_id", Value: "u1"},
		{Key: "created_at", Value: now}, {Key: "last_used_at", Value: now}, {Key: "expires_at", Value: now.Add(24 * time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{UsersCollection, IdentitiesCollection, SessionsCollection} {
		n, err := s.db.Collection(c).CountDocuments(ctx, bson.D{})
		if err != nil || n != 0 {
			t.Errorf("%s: %d documents left after DeleteUser (%v)", c, n, err)
		}
	}
	if err := s.DeleteUser(ctx, "u1"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestAuthStoreSeveralIdentities(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Date(2126, 10, 8, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"u1", "u2"} {
		if err := s.CreateUser(ctx, auth.User{ID: id, Email: id + "@example.com", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	put := func(id, user, provider, subject string, at time.Time) error {
		return s.PutIdentity(ctx, auth.Identity{ID: id, UserID: user, Provider: provider, Subject: subject, Email: subject + "@mail.example", EmailVerified: true, CreatedAt: at, UpdatedAt: at})
	}
	if err := s.PutIdentity(ctx, auth.Identity{ID: "p1", UserID: "u1", Provider: auth.ProviderPassword, Subject: "u1", PasswordHash: "h", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := put("g1", "u1", "google", "gsub", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := put("a1", "u1", "apple", "asub", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// The database enforces one identity per provider per user, and one user per provider account.
	if err := put("g2", "u1", "google", "other-sub", now.Add(3*time.Minute)); !errors.Is(err, auth.ErrIdentityExists) {
		t.Errorf("second google identity of a user: %v", err)
	}
	if err := put("g3", "u2", "google", "gsub", now.Add(3*time.Minute)); !errors.Is(err, auth.ErrIdentityExists) {
		t.Errorf("a google account of another user: %v", err)
	}
	// Names the validator doesn't allow are refused by the database.
	if err := put("x1", "u2", "Bad Name", "x", now); err == nil {
		t.Error("a provider name that is not a slug was stored")
	}
	if err := put("m1", "u2", "keycloak", "ksub", now); err != nil {
		t.Errorf("a generic provider's identity: %v", err)
	}

	ids, err := s.ListIdentities(ctx, "u1")
	if err != nil || len(ids) != 3 || ids[0].Provider != auth.ProviderPassword || ids[1].Provider != "google" || ids[2].Provider != "apple" {
		t.Fatalf("ListIdentities = %+v, %v", ids, err)
	}
	g, err := s.IdentityOf(ctx, "u1", "google")
	if err != nil || g.ID != "g1" || g.Email != "gsub@mail.example" || !g.EmailVerified || !g.LastUsedAt.IsZero() {
		t.Errorf("IdentityOf = %+v, %v", g, err)
	}
	if _, err := s.IdentityOf(ctx, "u2", "apple"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("IdentityOf a missing one: %v", err)
	}

	email, verified, used := "new@mail.example", false, now.Add(time.Hour)
	if err := s.UpdateIdentity(ctx, "g1", auth.IdentityUpdate{Email: &email, EmailVerified: &verified, LastUsedAt: &used}, used); err != nil {
		t.Fatal(err)
	}
	g, _ = s.IdentityOf(ctx, "u1", "google")
	if g.Email != email || g.EmailVerified || !g.LastUsedAt.Equal(used) || !g.UpdatedAt.Equal(used) {
		t.Errorf("after update: %+v", g)
	}
	if err := s.UpdateIdentity(ctx, "missing", auth.IdentityUpdate{}, used); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("update of a missing identity: %v", err)
	}

	if err := s.DeleteIdentity(ctx, "u1", "google"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteIdentity(ctx, "u1", "google"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	// Once removed, the provider account can be linked to someone else.
	if err := put("g4", "u2", "google", "gsub", now.Add(time.Hour)); err != nil {
		t.Errorf("relinking a freed account: %v", err)
	}
	// Deleting a user takes every identity.
	if err := s.DeleteUser(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.ListIdentities(ctx, "u1"); len(ids) != 0 {
		t.Errorf("identities left after DeleteUser: %+v", ids)
	}
}

// TestUsersServiceOnMongoDB runs the user service against the real store,
// so the system validators must accept everything it writes.
func TestUsersServiceOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	svc := &auth.Users{
		Store:    s,
		Hasher:   auth.NewHasher(1, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}),
		Settings: registry.RealmSettings{PasswordMinLength: 12},
	}
	pw := "dev-p4ssw0rd!"
	u, err := svc.Create(ctx, "Ada@Example.com", &pw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, "bob@example.com", nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetPassword(ctx, "bob@example.com", pw); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetEmailVerified(ctx, "ada@example.com", true); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetDisabled(ctx, "ada@example.com", true); err != nil {
		t.Fatal(err)
	}
	id, err := s.Identity(ctx, auth.ProviderPassword, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := svc.Hasher.Verify(ctx, pw, id.PasswordHash); !ok || err != nil {
		t.Errorf("stored password doesn't verify: %v", err)
	}
	if err := svc.Delete(ctx, "ada@example.com"); err != nil {
		t.Fatal(err)
	}
	if list, _ := svc.List(ctx); len(list) != 1 || list[0].Email != "bob@example.com" {
		t.Errorf("list = %+v", list)
	}
}

// TestSessionsOnMongoDB runs the session flows against the real store.
func TestSessionsOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	clock := time.Date(2126, 9, 26, 12, 0, 0, 0, time.UTC)
	svc := &auth.Users{
		Store:  s,
		Hasher: auth.NewHasher(1, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}),
		Settings: registry.RealmSettings{
			Signup: registry.SignupOpen, IdleTimeout: time.Hour, MaxLifetime: 24 * time.Hour, PasswordMinLength: 12,
		},
		Now: func() time.Time { return clock },
	}

	me, token, err := svc.Signup(ctx, "ada@example.com", "dev-p4ssw0rd!", "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.Authenticate(ctx, token)
	if err != nil || p.User.Email != "ada@example.com" || p.Session.ID != me.Session.ID {
		t.Fatalf("Authenticate = %+v, %v", p, err)
	}

	// A use after a minute slides the expiry, persisted in MongoDB.
	clock = clock.Add(10 * time.Minute)
	if _, err := svc.Authenticate(ctx, token); err != nil {
		t.Fatal(err)
	}
	p, _ = svc.Authenticate(ctx, token)
	if !p.Session.LastUsedAt.Equal(clock) || !p.Session.ExpiresAt.Equal(clock.Add(time.Hour)) {
		t.Errorf("after touch: %+v", p.Session)
	}

	clock = clock.Add(time.Minute)
	_, token2, err := svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!", "")
	if err != nil {
		t.Fatal(err)
	}
	list, err := svc.Sessions(ctx, me)
	if err != nil || len(list) != 2 || list[1].ID != me.Session.ID {
		t.Fatalf("Sessions = %+v, %v", list, err)
	}

	if err := svc.ChangePassword(ctx, me, "dev-p4ssw0rd!", "dev-p4ssw0rd!2"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, token2); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("other session survived password change: %v", err)
	}
	if _, err := svc.Authenticate(ctx, token); err != nil {
		t.Errorf("current session ended: %v", err)
	}
	if err := svc.RevokeSession(ctx, me, "missing"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("revoke missing: %v", err)
	}
	if err := svc.LogoutAll(ctx, me); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("session survived logout-all: %v", err)
	}
	if _, _, err := svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!", ""); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("login with old password: %v", err)
	}
}

func TestAPIKeysOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	clock := time.Date(2126, 9, 26, 12, 0, 0, 0, time.UTC)
	svc := &auth.Users{Store: s, Settings: registry.RealmSettings{}, Now: func() time.Time { return clock }}

	_, key, err := svc.CreateAPIKey(ctx, "billing", auth.KeyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, tempKey, err := svc.CreateAPIKey(ctx, "temp", auth.KeyOptions{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateAPIKey(ctx, "billing", auth.KeyOptions{}); !errors.Is(err, auth.ErrKeyNameTaken) {
		t.Errorf("duplicate name: %v", err)
	}

	clock = clock.Add(2 * time.Minute)
	k, err := svc.AuthenticateKey(ctx, key)
	if err != nil || k.Name != "billing" {
		t.Fatalf("AuthenticateKey = %+v, %v", k, err)
	}
	list, err := svc.ListAPIKeys(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "billing" || list[0].LastUsedAt == nil || !list[0].LastUsedAt.Equal(clock) ||
		list[0].ExpiresAt != nil || list[1].ExpiresAt == nil || list[1].LastUsedAt != nil {
		t.Errorf("ListAPIKeys = %+v, %v", list, err)
	}

	pinned, _ := registry.ParseNetworks([]string{"192.0.2.0/24"})
	if _, _, err := svc.CreateAPIKey(ctx, "pinned", auth.KeyOptions{Role: auth.KeyRoleAdmin, Networks: pinned}); err != nil {
		t.Fatal(err)
	}
	keys, _ := svc.ListAPIKeys(ctx)
	for _, k := range keys {
		if k.Name == "pinned" && (!k.IsAdmin() || !k.Networks.Equal(pinned)) {
			t.Errorf("pinned key = %+v", k)
		}
		if k.Name == "billing" && (k.Role != auth.KeyRoleData || len(k.Networks) != 0) {
			t.Errorf("billing key = %+v", k)
		}
	}

	clock = clock.Add(time.Hour)
	if _, err := svc.AuthenticateKey(ctx, tempKey); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("expired key: %v", err)
	}
	if err := svc.RevokeAPIKey(ctx, "billing"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateKey(ctx, key); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("revoked key: %v", err)
	}
	if err := svc.RevokeAPIKey(ctx, "billing"); !errors.Is(err, auth.ErrKeyNotFound) {
		t.Errorf("revoke twice: %v", err)
	}
}

func TestLoginAttemptsOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2126, 9, 26, 12, 0, 0, 0, time.UTC)

	if a, err := s.LoginAttempts(ctx, "account:a@x.io"); err != nil || a.Failures != 0 {
		t.Fatalf("no counter: %+v, %v", a, err)
	}
	for i := range 3 {
		at := t0.Add(time.Duration(i) * time.Minute)
		if err := s.RecordLoginFailure(ctx, "account:a@x.io", at, at.Add(15*time.Minute)); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	a, err := s.LoginAttempts(ctx, "account:a@x.io")
	if err != nil || a.Failures != 3 || !a.LastFailureAt.Equal(t0.Add(2*time.Minute)) {
		t.Errorf("after 3 failures: %+v, %v", a, err)
	}

	// A failure after the window restarts the count, even if MongoDB's TTL
	// monitor hasn't deleted the old counter yet.
	late := t0.Add(time.Hour)
	if err := s.RecordLoginFailure(ctx, "account:a@x.io", late, late.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.LoginAttempts(ctx, "account:a@x.io"); a.Failures != 1 {
		t.Errorf("after the window: %+v", a)
	}

	if err := s.ClearLoginAttempts(ctx, "account:a@x.io"); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.LoginAttempts(ctx, "account:a@x.io"); a.Failures != 0 {
		t.Errorf("after clear: %+v", a)
	}
}

// TestIncrementCounterOnMongoDB proves the fixed-window behavior
// IncrementCounter needs for rate limits (F9), which differs from
// RecordLoginFailure's sliding cleanup deadline (TestLoginAttemptsOnMongoDB
// above): repeated calls inside one window must not keep pushing the
// reset time forward, or a steady stream of calls would never reset.
func TestIncrementCounterOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2126, 9, 29, 12, 0, 0, 0, time.UTC)
	window := time.Minute

	var resetAt time.Time
	for i := range 3 {
		at := t0.Add(time.Duration(i) * 10 * time.Second)
		count, r, err := s.IncrementCounter(ctx, "func:shop/app/send:ip:203.0.113.1", at, at.Add(window))
		if err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
		if count != i+1 {
			t.Errorf("call %d: count = %d, want %d", i+1, count, i+1)
		}
		if i == 0 {
			resetAt = r
		} else if !r.Equal(resetAt) {
			t.Errorf("call %d: reset time moved from %v to %v; a fixed window must not keep sliding forward", i+1, resetAt, r)
		}
	}

	// After the window actually ends, the next call restarts the count.
	late := resetAt.Add(time.Second)
	count, newReset, err := s.IncrementCounter(ctx, "func:shop/app/send:ip:203.0.113.1", late, late.Add(window))
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("after the window: count = %d, want 1", count)
	}
	if !newReset.After(resetAt) {
		t.Errorf("after the window: reset time %v didn't move past %v", newReset, resetAt)
	}
}

// TestLoginThrottleAcrossInstances runs two backd "instances" (separate
// clients and services) on one realm: failures on one throttle the other.
func TestLoginThrottleAcrossInstances(t *testing.T) {
	s1, realm := authFixture(t)
	ctx := context.Background()
	s2 := NewAuthStore(testClient(t), realm)
	settings := registry.RealmSettings{Signup: registry.SignupOpen, IdleTimeout: time.Hour, MaxLifetime: 24 * time.Hour, PasswordMinLength: 12}
	hasher := auth.NewHasher(2, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1})
	a := &auth.Users{Store: s1, Hasher: hasher, Settings: settings}
	b := &auth.Users{Store: s2, Hasher: hasher, Settings: settings}
	if _, _, err := a.Signup(ctx, "ada@example.com", "dev-p4ssw0rd!", ""); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		svc := []*auth.Users{a, b}[i%2]
		if _, _, err := svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!0", "203.0.113.1"); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("failure %d: %v", i+1, err)
		}
	}
	var te *auth.ThrottledError
	for _, svc := range []*auth.Users{a, b} {
		if _, _, err := svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!", "198.51.100.1"); !errors.As(err, &te) {
			t.Errorf("not throttled on one instance: %v", err)
		}
	}
}

func TestInvitationsOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Date(2126, 9, 26, 12, 0, 0, 0, time.UTC)
	svc := &auth.Users{
		Store: s, Hasher: auth.NewHasher(2, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}),
		Settings: registry.RealmSettings{Signup: registry.SignupInvite, IdleTimeout: time.Hour, MaxLifetime: time.Hour, PasswordMinLength: 12},
		Now:      func() time.Time { return now },
	}
	inv, token, err := svc.CreateInvitation(ctx, "ada@example.com", 0, "key:svc")
	if err != nil {
		t.Fatal(err)
	}
	if list, err := svc.Invitations(ctx); err != nil || len(list) != 1 || list[0].ID != inv.ID || list[0].Email != "ada@example.com" {
		t.Errorf("list = %+v, %v", list, err)
	}

	// Two concurrent claims: exactly one wins.
	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := s.ClaimInvitation(ctx, auth.HashToken(token), now)
			results <- err
		}()
	}
	var wins int
	for range 2 {
		if err := <-results; err == nil {
			wins++
		} else if !errors.Is(err, auth.ErrNotFound) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Errorf("%d claims won", wins)
	}
	// Restoring (as after a failed sign-up) makes it usable again.
	if err := s.CreateInvitation(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Signup(ctx, "ada@example.com", "dev-p4ssw0rd!", token); err != nil {
		t.Errorf("signup with restored invitation: %v", err)
	}

	// Expired invitations can't be claimed, even before the TTL monitor deletes them.
	_, late, _ := svc.CreateInvitation(ctx, "", time.Minute, "key:svc")
	if _, err := s.ClaimInvitation(ctx, auth.HashToken(late), now.Add(time.Hour)); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("expired claim: %v", err)
	}
}

func TestListUnverifiedUsersOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	at := func(h int) time.Time { return time.Date(2126, 9, 26, h, 0, 0, 0, time.UTC) }
	for i, u := range []auth.User{
		{ID: "old-unverified", Email: "a@example.com", CreatedAt: at(1)},
		{ID: "old-verified", Email: "b@example.com", EmailVerified: true, CreatedAt: at(2)},
		{ID: "older-unverified", Email: "c@example.com", CreatedAt: at(0)},
		{ID: "young-unverified", Email: "d@example.com", CreatedAt: at(10)},
	} {
		u.UpdatedAt = u.CreatedAt
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatalf("user %d: %v", i, err)
		}
	}
	got, err := s.ListUnverifiedUsers(ctx, at(5), 10)
	if err != nil || len(got) != 2 || got[0].ID != "older-unverified" || got[1].ID != "old-unverified" {
		t.Errorf("unverified before 05:00: %+v, %v", got, err)
	}
	if got, _ = s.ListUnverifiedUsers(ctx, at(5), 1); len(got) != 1 || got[0].ID != "older-unverified" {
		t.Errorf("limit: %+v", got)
	}
}

func TestEraseUserOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Date(2126, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, u := range []auth.User{
		{ID: "u1", Email: "ada@example.com", Roles: []string{"admin"}, Locale: "es", PendingEmail: "new@example.com", PreviousEmail: "old@example.com", CreatedAt: now, UpdatedAt: now},
		{ID: "u2", Email: "bob@example.com", CreatedAt: now, UpdatedAt: now},
	} {
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutIdentity(ctx, auth.Identity{UserID: "u1", Provider: auth.ProviderPassword, PasswordHash: "h", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, auth.Session{ID: "s1", UserID: "u1", TokenHash: "t1", CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	for _, tk := range []auth.EmailToken{{Hash: "k1", Purpose: "reset-password", UserID: "u1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, {Hash: "k2", Purpose: "reset-password", UserID: "u2", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}} {
		if err := s.CreateEmailToken(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}

	for range 2 { // repeating it is harmless
		if err := s.EraseUser(ctx, "u1", "erased-u1@erased.invalid", now); err != nil {
			t.Fatal(err)
		}
	}
	u, err := s.UserByID(ctx, "u1")
	if err != nil || u.Email != "erased-u1@erased.invalid" || !u.ErasedAt.Equal(now) || !u.Disabled || !u.EmailVerified || len(u.Roles) != 0 ||
		u.Locale != "" || u.PendingEmail != "" || u.PreviousEmail != "" {
		t.Errorf("tombstone: %+v %v", u, err)
	}
	if _, err := s.Identity(ctx, auth.ProviderPassword, "u1"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("identity: %v", err)
	}
	if sessions, _ := s.ListSessions(ctx, "u1"); len(sessions) != 0 {
		t.Errorf("sessions: %+v", sessions)
	}
	// The real address is free again, and the placeholder can't be anyone's.
	if err := s.CreateUser(ctx, auth.User{ID: "u3", Email: "ada@example.com", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Errorf("reusing the address: %v", err)
	}
	if other, _ := s.UserByID(ctx, "u2"); other.ErasedAt != (time.Time{}) || other.Email != "bob@example.com" {
		t.Errorf("another user: %+v", other)
	}
	if err := s.DeleteEmailTokensOfUser(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetEmailToken(ctx, "k1"); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("her token: %v", err)
	}
	if _, err := s.GetEmailToken(ctx, "k2"); err != nil {
		t.Errorf("his token: %v", err)
	}
}

func TestEraseJobOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Date(2126, 10, 1, 12, 0, 0, 0, time.UTC)
	job := auth.Job{ID: "e1", Database: "_backd", Function: "erase", Origin: auth.EraseOrigin, TimeoutMS: 1000, Status: auth.JobQueued, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		Erase: &auth.EraseJob{UserID: "u1", Email: "ada@example.com"}}
	if err := s.EnqueueJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := s.AddEraseCount(ctx, "e1", "main/orders/anonymized", 2); err != nil {
			t.Fatal(err)
		}
	}
	got, _, _ := s.GetJob(ctx, "e1")
	if got.Erase == nil || got.Erase.UserID != "u1" || got.Erase.Email != "ada@example.com" || got.Erase.Counts["main/orders/anonymized"] != 6 {
		t.Errorf("erase job: %+v", got.Erase)
	}
	if found, _, _ := s.ListJobs(ctx, auth.JobFilter{Origin: auth.EraseOrigin, EraseUser: "u1"}); len(found) != 1 {
		t.Errorf("by user: %+v", found)
	}
	if found, _, _ := s.ListJobs(ctx, auth.JobFilter{EraseUser: "u2"}); len(found) != 0 {
		t.Errorf("another user: %+v", found)
	}
	if reopened, err := s.ReopenJob(ctx, "e1", now.Add(time.Hour)); err != nil || reopened {
		t.Errorf("reopening a job that isn't done: %v %v", reopened, err)
	}
	if err := s.CompleteJob(ctx, "e1", auth.JobResult{Status: "error", Message: "boom"}, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	reopened, err := s.ReopenJob(ctx, "e1", now.Add(48*time.Hour))
	if err != nil || !reopened {
		t.Fatalf("reopen: %v %v", reopened, err)
	}
	if got, _, _ = s.GetJob(ctx, "e1"); got.Status != auth.JobQueued || got.Result != nil || got.Failures != 0 || got.Erase.Counts["main/orders/anonymized"] != 6 {
		t.Errorf("reopened: %+v", got)
	}
	if err := s.ClearEraseEmail(ctx, "e1"); err != nil {
		t.Fatal(err)
	}
	if got, _, _ = s.GetJob(ctx, "e1"); got.Erase.Email != "" || got.Erase.UserID != "u1" {
		t.Errorf("cleared: %+v", got.Erase)
	}
	// A finished-ok job isn't reopened.
	if err := s.CompleteJob(ctx, "e1", auth.JobResult{Status: "ok"}, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if reopened, _ := s.ReopenJob(ctx, "e1", now); reopened {
		t.Error("an ok job was reopened")
	}
	// Email jobs of the user are removed.
	mail := auth.Job{ID: "m1", Database: "n", Function: "d", Status: auth.JobQueued, CreatedAt: now, ExpiresAt: now.Add(time.Hour), Email: &auth.EmailJob{Kind: "reset-password", UserID: "u1"}}
	if err := s.EnqueueJob(ctx, mail); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteEmailJobsOfUser(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.GetJob(ctx, "m1"); found {
		t.Error("the email job should be gone")
	}
}

func TestListUsersPageOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Date(2126, 9, 26, 12, 0, 0, 0, time.UTC)
	for _, email := range []string{"d@example.com", "a@example.com", "c@example.com", "b@example.com", "e@example.com"} {
		if err := s.CreateUser(ctx, auth.User{ID: "id-" + email[:1], Email: email, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	emails := func(us []auth.User) []string {
		var out []string
		for _, u := range us {
			out = append(out, u.Email)
		}
		return out
	}
	page, more, err := s.ListUsersPage(ctx, "", "", 0, 2)
	if err != nil || !more || !reflect.DeepEqual(emails(page), []string{"a@example.com", "b@example.com"}) {
		t.Errorf("first page = %v %v %v", emails(page), more, err)
	}
	page, more, _ = s.ListUsersPage(ctx, "", "b@example.com", 0, 2)
	if !more || !reflect.DeepEqual(emails(page), []string{"c@example.com", "d@example.com"}) {
		t.Errorf("after b = %v %v", emails(page), more)
	}
	page, more, _ = s.ListUsersPage(ctx, "", "d@example.com", 0, 2)
	if more || !reflect.DeepEqual(emails(page), []string{"e@example.com"}) {
		t.Errorf("last page = %v %v", emails(page), more)
	}
	page, more, _ = s.ListUsersPage(ctx, "", "", 3, 5)
	if more || !reflect.DeepEqual(emails(page), []string{"d@example.com", "e@example.com"}) {
		t.Errorf("skip 3 = %v %v", emails(page), more)
	}
	// An address that isn't a user's still gives a position.
	page, _, _ = s.ListUsersPage(ctx, "", "bb@example.com", 0, 1)
	if !reflect.DeepEqual(emails(page), []string{"c@example.com"}) {
		t.Errorf("after bb = %v", emails(page))
	}
	// A search keeps the users whose email holds the text, paged like the list;
	// the text is plain (a dot is a dot), and a position works inside it.
	page, more, _ = s.ListUsersPage(ctx, "@example.", "", 0, 3)
	if !more || !reflect.DeepEqual(emails(page), []string{"a@example.com", "b@example.com", "c@example.com"}) {
		t.Errorf("search = %v %v", emails(page), more)
	}
	page, more, _ = s.ListUsersPage(ctx, "@example.", "c@example.com", 0, 3)
	if more || !reflect.DeepEqual(emails(page), []string{"d@example.com", "e@example.com"}) {
		t.Errorf("search after c = %v %v", emails(page), more)
	}
	if page, _, _ = s.ListUsersPage(ctx, "d@", "", 0, 5); !reflect.DeepEqual(emails(page), []string{"d@example.com"}) {
		t.Errorf("search d@ = %v", emails(page))
	}
	if page, _, _ = s.ListUsersPage(ctx, "a.e", "", 0, 5); len(page) != 0 {
		t.Errorf("search a.e must not treat the dot as a pattern: %v", emails(page))
	}
}

func TestAPIKeyScopesOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	svc := &auth.Users{Store: s, Settings: registry.RealmSettings{}}
	scopes, _ := auth.ParseScopes([]string{"read:blog", "write:blog/posts", "call:main/export"})
	_, key, err := svc.CreateAPIKey(ctx, "scoped", auth.KeyOptions{Scopes: scopes})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateAPIKey(ctx, "plain", auth.KeyOptions{}); err != nil {
		t.Fatal(err)
	}
	k, err := svc.AuthenticateKey(ctx, key)
	if err != nil || !reflect.DeepEqual(k.Scopes.Strings(), []string{"read:blog", "write:blog/posts", "call:main/export"}) {
		t.Fatalf("scopes after a round trip: %v, %v", k.Scopes, err)
	}
	if !k.Scopes.Allows(auth.ScopeRead, "blog", "comments") || k.Scopes.Allows(auth.ScopeWrite, "blog", "comments") {
		t.Error("the stored scopes don't decide as they were made")
	}
	list, _ := svc.ListAPIKeys(ctx)
	if len(list) != 2 || list[0].Name != "plain" || list[0].Scopes.Limited() || !list[1].Scopes.Limited() {
		t.Errorf("list = %+v", list)
	}
	// A grant edited into something unparsable fails closed: the key reaches nothing.
	if _, err := s.apiKeys().UpdateOne(ctx, bson.D{{Key: "name", Value: "scoped"}}, bson.D{{Key: "$set", Value: bson.D{{Key: "scopes", Value: bson.A{"read:BLOG"}}}}}); err != nil {
		t.Fatal(err)
	}
	k, err = svc.AuthenticateKey(ctx, key)
	if err != nil || !k.Scopes.Limited() || k.Scopes.Allows(auth.ScopeRead, "blog", "posts") {
		t.Errorf("an unparsable grant should allow nothing: %+v, %v", k.Scopes, err)
	}
}
