package mongodb

import (
	"context"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

func TestAuthStoreConsents(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.CreateUser(ctx, auth.User{ID: "u1", Email: "ada@example.com", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	rec := func(id, consent, version, action string, at time.Duration) auth.ConsentRecord {
		return auth.ConsentRecord{ID: id, UserID: "u1", Consent: consent, Version: version, Locale: "en", TextHash: "h" + version, Action: action, Method: auth.ConsentMethodAPI, At: now.Add(at), IP: "203.0.113.9"}
	}
	for _, r := range []auth.ConsentRecord{
		rec("r2", "marketing", "1", auth.ConsentWithdrawn, time.Minute),
		rec("r1", "marketing", "1", auth.ConsentAccepted, 0),
		rec("r3", "terms", "2", auth.ConsentAccepted, 2*time.Minute),
	} {
		if err := s.AppendConsentRecord(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListConsentRecords(ctx, "u1")
	if err != nil || len(list) != 3 || list[0].ID != "r1" || list[1].ID != "r2" || list[2].ID != "r3" || list[0].IP != "203.0.113.9" || !list[2].At.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("history = %+v %v", list, err)
	}

	if err := s.SetUserConsent(ctx, "u1", "terms", "2"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserConsent(ctx, "u1", "marketing", "1"); err != nil {
		t.Fatal(err)
	}
	u, _ := s.UserByID(ctx, "u1")
	if u.Consents["terms"] != "2" || u.Consents["marketing"] != "1" {
		t.Errorf("summary = %v", u.Consents)
	}
	if err := s.SetUserConsent(ctx, "u1", "marketing", ""); err != nil {
		t.Fatal(err)
	}
	u, _ = s.UserByID(ctx, "u1")
	if _, ok := u.Consents["marketing"]; ok || u.Consents["terms"] != "2" {
		t.Errorf("after withdrawal = %v", u.Consents)
	}
	if err := s.SetUserConsent(ctx, "nobody", "terms", "1"); err != auth.ErrNotFound {
		t.Errorf("unknown user: %v", err)
	}

	refs, err := s.ConsentVersionsInUse(ctx)
	if err != nil || len(refs) != 2 {
		t.Errorf("versions in use = %+v %v", refs, err)
	}

	// An erase keeps the proof, without the IP address, under a pseudonym, until it expires.
	deleteAt := now.Add(24 * time.Hour)
	n, err := s.ReduceConsentRecords(ctx, "u1", "erased:abc", deleteAt)
	if err != nil || n != 3 {
		t.Fatalf("reduce = %d %v", n, err)
	}
	if recs, _ := s.ListConsentRecords(ctx, "u1"); len(recs) != 0 {
		t.Errorf("under the id: %+v", recs)
	}
	reduced, _ := s.ListConsentRecords(ctx, "erased:abc")
	if len(reduced) != 3 || reduced[0].IP != "" || reduced[0].TextHash == "" || !reduced[0].DeleteAt.Equal(deleteAt) {
		t.Errorf("reduced = %+v", reduced)
	}
	if n, err := s.DeleteConsentRecordsOf(ctx, "erased:abc"); err != nil || n != 3 {
		t.Errorf("delete = %d %v", n, err)
	}

}
