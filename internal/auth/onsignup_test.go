package auth_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
)

// flakyQueue fails the first n job inserts, like a database that blinks.
type flakyQueue struct {
	*authtest.MemStore
	mu   sync.Mutex
	fail int
	hits int
}

func (f *flakyQueue) EnqueueJob(ctx context.Context, j Job) error {
	f.mu.Lock()
	f.hits++
	if f.fail > 0 {
		f.fail--
		f.mu.Unlock()
		return errors.New("database unreachable")
	}
	f.mu.Unlock()
	return f.MemStore.EnqueueJob(ctx, j)
}

func signupService(store Store) *Users {
	return &Users{
		Store:  store,
		Hasher: NewHasher(2, Argon2Params{Memory: 64, Time: 1, Threads: 1}),
		Settings: registry.RealmSettings{
			AuthEnabled: true, Signup: registry.SignupOpen, PasswordMinLength: 12,
			Account: registry.AccountSettings{OnSignup: "app/welcome", OnSignupTimeout: 10 * time.Second},
		},
	}
}

// A blink of the database doesn't lose the hook: the call is tried again, and being keyed by
// the user it can't be queued twice.
func TestOnSignupIsRetriedAndQueuedOnce(t *testing.T) {
	q := &flakyQueue{MemStore: authtest.NewMemStore(), fail: 2}
	svc := signupService(q)
	ctx := context.Background()
	p, _, err := svc.Signup(ctx, "ada@example.com", "dev-p4ssw0rd!", "")
	if err != nil {
		t.Fatal(err)
	}
	jobs, _, err := svc.Jobs(ctx, JobFilter{Origin: OnSignupOrigin})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %+v %v, want the hook queued once after two failures", jobs, err)
	}
	if jobs[0].ID != KeyedJobID(OnSignupOrigin, p.User.ID) || jobs[0].Database != "app" || jobs[0].Function != "welcome" || !jobs[0].ActsAsFunction {
		t.Errorf("job = %+v", jobs[0])
	}
	if q.hits != 3 {
		t.Errorf("attempts = %d, want 3", q.hits)
	}
}

// A queue that stays down fails nothing the user did: the account exists and signs in.
func TestOnSignupGivesUpQuietlyWhenTheQueueStaysDown(t *testing.T) {
	q := &flakyQueue{MemStore: authtest.NewMemStore(), fail: 100}
	svc := signupService(q)
	ctx := context.Background()
	if _, _, err := svc.Signup(ctx, "ada@example.com", "dev-p4ssw0rd!", ""); err != nil {
		t.Fatalf("the sign-up failed with the hook: %v", err)
	}
	if q.hits != 3 {
		t.Errorf("attempts = %d, want a bounded 3", q.hits)
	}
	if _, err := svc.Find(ctx, "ada@example.com"); err != nil {
		t.Errorf("the account is gone: %v", err)
	}
}

func TestKeyedJobIDsAreStableAndDistinct(t *testing.T) {
	a := KeyedJobID("backd:org.member_joined", "org1", "u1", "1")
	if a != KeyedJobID("backd:org.member_joined", "org1", "u1", "1") || len(a) != 20 {
		t.Errorf("not stable: %q", a)
	}
	if a == KeyedJobID("backd:org.member_joined", "org1", "u1", "2") || a == KeyedJobID("backd:org.member_left", "org1", "u1", "1") {
		t.Error("distinct events share an id")
	}
	// parts are separated: ("ab","c") is not ("a","bc")
	if KeyedJobID("ab", "c") == KeyedJobID("a", "bc") {
		t.Error("parts run together")
	}
}
