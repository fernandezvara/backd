package mongodb

import (
	"context"
	"testing"
	"time"

	"github.com/rs/xid"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/fernandezvara/backd/internal/triggers"
)

func newTriggerState(t *testing.T) *TriggerState {
	t.Helper()
	client := testClient(t)
	s := &TriggerState{Client: client, Deployment: "t" + xid.New().String()}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = s.state().DeleteMany(ctx, bson.D{{Key: "_id", Value: s.id()}})
		_, _ = s.gaps().DeleteMany(ctx, bson.D{{Key: "deployment", Value: s.Deployment}})
	})
	return s
}

func TestTriggerLeaseIsExclusiveAndFenced(t *testing.T) {
	s := newTriggerState(t)
	ctx := context.Background()
	const ttl = 400 * time.Millisecond

	a, ok, err := s.AcquireLease(ctx, "a", ttl)
	if err != nil || !ok || a.Epoch != 1 {
		t.Fatalf("a acquires: %+v %v %v", a, ok, err)
	}
	if _, ok, err := s.AcquireLease(ctx, "b", ttl); err != nil || ok {
		t.Fatalf("b must not take a held lease: ok=%v err=%v", ok, err)
	}
	if ok, err := s.RenewLease(ctx, a, ttl); err != nil || !ok {
		t.Fatalf("renew: %v %v", ok, err)
	}
	if ok, err := s.SetWatermark(ctx, a, []byte("p1"), time.Now()); err != nil || !ok {
		t.Fatalf("watermark: %v %v", ok, err)
	}
	pos, _, found, err := s.Watermark(ctx)
	if err != nil || !found || string(pos) != "p1" {
		t.Fatalf("read watermark: %q %v %v", pos, found, err)
	}

	// After the lease expires another instance takes it, with the next epoch...
	time.Sleep(2 * ttl)
	b, ok, err := s.AcquireLease(ctx, "b", ttl)
	if err != nil || !ok || b.Epoch != 2 {
		t.Fatalf("b takes the expired lease: %+v %v %v", b, ok, err)
	}
	// ...and the old holder is fenced: it can neither renew nor move the position.
	if ok, _ := s.RenewLease(ctx, a, ttl); ok {
		t.Error("the old holder renewed a lease it lost")
	}
	if ok, err := s.SetWatermark(ctx, a, []byte("stale"), time.Now()); err != nil || ok {
		t.Errorf("the old holder moved the position: ok=%v err=%v", ok, err)
	}
	pos, _, _, _ = s.Watermark(ctx)
	if string(pos) != "p1" {
		t.Errorf("position = %q after a fenced write, want p1", pos)
	}
	// The new holder continues from the stored position and may move it.
	if ok, _ := s.SetWatermark(ctx, b, []byte("p2"), time.Now()); !ok {
		t.Error("the holder could not move the position")
	}
}

func TestTriggerLeaseReleaseLetsAnotherTakeItAtOnce(t *testing.T) {
	s := newTriggerState(t)
	ctx := context.Background()
	a, ok, _ := s.AcquireLease(ctx, "a", time.Minute)
	if !ok {
		t.Fatal("a did not acquire")
	}
	if _, ok, _ := s.AcquireLease(ctx, "b", time.Minute); ok {
		t.Fatal("b took a live lease")
	}
	if err := s.ReleaseLease(ctx, a); err != nil {
		t.Fatal(err)
	}
	b, ok, err := s.AcquireLease(ctx, "b", time.Minute)
	if err != nil || !ok || b.Epoch != 2 {
		t.Fatalf("after release: %+v %v %v", b, ok, err)
	}
	// Releasing with a stale lease is harmless.
	if err := s.ReleaseLease(ctx, a); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.RenewLease(ctx, b, time.Minute); !ok {
		t.Error("a stale release ended b's lease")
	}
}

func TestTriggerLeaseRaceHasOneWinner(t *testing.T) {
	s := newTriggerState(t)
	ctx := context.Background()
	const n = 8
	results := make(chan bool, n)
	for i := 0; i < n; i++ {
		holder := string(rune('a' + i))
		go func() {
			_, ok, err := s.AcquireLease(ctx, holder, time.Minute)
			results <- ok && err == nil
		}()
	}
	won := 0
	for i := 0; i < n; i++ {
		if <-results {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("%d instances acquired the lease at once, want exactly 1", won)
	}
}

func TestTriggerGapsAreRecorded(t *testing.T) {
	s := newTriggerState(t)
	ctx := context.Background()
	from := time.Now().Add(-time.Hour)
	if err := s.RecordGap(ctx, triggers.Gap{Realm: "shop", Collection: "orders", Trigger: "on_paid", Reason: triggers.GapPreImage, From: from, To: time.Now(), Count: 3, Keys: []string{"a", "b", "c"}}); err != nil {
		t.Fatal(err)
	}
	var d gapDoc
	if err := s.gaps().FindOne(ctx, bson.D{{Key: "deployment", Value: s.Deployment}}).Decode(&d); err != nil {
		t.Fatal(err)
	}
	if d.Realm != "shop" || d.Reason != triggers.GapPreImage || d.Count != 3 || len(d.Keys) != 3 || d.Trigger != "on_paid" {
		t.Errorf("gap = %+v", d)
	}
}
