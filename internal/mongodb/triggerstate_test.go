package mongodb

import (
	"context"
	"fmt"
	"strings"
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

func TestTriggerStatusGapsAndResolve(t *testing.T) {
	s := newTriggerState(t)
	ctx := context.Background()

	st, err := s.Status(ctx)
	if err != nil || st.Found {
		t.Fatalf("before anything: %+v %v", st, err)
	}
	l, ok, err := s.AcquireLease(ctx, "host-1", time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := s.SetWatermark(ctx, l, []byte("p"), at); err != nil {
		t.Fatal(err)
	}
	st, err = s.Status(ctx)
	if err != nil || !st.Found || !st.Held || st.Epoch != 1 || st.Holder != "host-1" || !st.WatermarkAt.Equal(at) {
		t.Fatalf("status: %+v %v", st, err)
	}
	if err := s.ReleaseLease(ctx, l); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Status(ctx); st.Held {
		t.Error("held after release")
	}

	rec := func(g triggers.Gap) {
		t.Helper()
		g.From, g.To = time.Now().Add(-time.Hour), time.Now()
		if err := s.RecordGap(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	rec(triggers.Gap{Realm: "shop", Collection: "orders", Trigger: "on_paid", Reason: triggers.GapPreImage, Count: 2, Keys: []string{"a", "b"}})
	rec(triggers.Gap{Realm: "blog", Collection: "posts", Trigger: "t", Reason: triggers.GapBacklog, Count: 1})
	rec(triggers.Gap{Reason: triggers.GapWindow}) // covers every realm

	shop, err := s.Gaps(ctx, "shop", false, 10)
	if err != nil || len(shop) != 2 {
		t.Fatalf("shop gaps = %+v %v, want its own and the window one", shop, err)
	}
	for _, g := range shop {
		if g.Realm == "blog" {
			t.Errorf("a gap of another realm leaked: %+v", g)
		}
	}
	if n, _ := s.OpenGaps(ctx, "blog"); n != 2 {
		t.Errorf("blog open = %d", n)
	}

	// Resolving: only that realm's own or window gaps, once.
	var own string
	for _, g := range shop {
		if g.Realm == "shop" {
			own = g.ID
		}
	}
	if ok, err := s.ResolveGap(ctx, "blog", own, time.Now()); err != nil || ok {
		t.Errorf("another realm resolved it: %v %v", ok, err)
	}
	if ok, err := s.ResolveGap(ctx, "shop", own, time.Now()); err != nil || !ok {
		t.Fatalf("resolve: %v %v", ok, err)
	}
	if ok, _ := s.ResolveGap(ctx, "shop", own, time.Now()); ok {
		t.Error("resolved twice")
	}
	open, _ := s.Gaps(ctx, "shop", false, 10)
	all, _ := s.Gaps(ctx, "shop", true, 10)
	if len(open) != 1 || len(all) != 2 {
		t.Fatalf("open %d, all %d", len(open), len(all))
	}
	for _, g := range all {
		if g.ID == own && g.ResolvedAt.IsZero() {
			t.Error("resolved gap has no time")
		}
	}
}

func TestDocScannerReadsByUpdatedAt(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	coll := client.Database(realm + "__app").Collection("items")
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		_, err := coll.InsertOne(ctx, bson.D{{Key: "_id", Value: fmt.Sprintf("d%d", i)},
			{Key: "_meta", Value: bson.D{{Key: "version", Value: int64(1)}, {Key: "updated_at", Value: base.Add(time.Duration(i) * time.Hour)}}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	sc := &DocScanner{Client: client}
	n, err := sc.Count(ctx, realm+"__app", "items", base.Add(time.Hour), base.Add(4*time.Hour))
	if err != nil || n != 3 {
		t.Fatalf("count = %d %v, want d1..d3", n, err)
	}
	var ids []string
	err = sc.Updated(ctx, realm+"__app", "items", base.Add(time.Hour), time.Time{}, func(d map[string]any) error {
		ids = append(ids, d["id"].(string))
		return nil
	})
	if err != nil || strings.Join(ids, ",") != "d1,d2,d3,d4" {
		t.Fatalf("ids = %v %v", ids, err)
	}
}
