package mongodb

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/fernandezvara/backd/internal/changefeed"
)

const itemSchema = `{"type":"object","properties":{"name":{"type":"string"}}}`

// feedFixture is a realm with one realtime collection whose pre-images are enabled.
type feedFixture struct {
	client *mongo.Client
	realm  string
	items  *mongo.Collection
	set    changefeed.WatchSet
	src    *ChangeSource
}

func newFeedFixture(t *testing.T) *feedFixture {
	t.Helper()
	client := testClient(t)
	realm := testRealm(t, client)
	reg := loadRegistry(t, realm, map[string]string{
		"app/items":                 itemSchema,
		"app/items/collection.yaml": "realtime: true\n",
		"app/other":                 itemSchema,
	})
	ctx := context.Background()
	db := client.Database(realm + "__app")
	if err := db.CreateCollection(ctx, "items"); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateCollection(ctx, "other"); err != nil {
		t.Fatal(err)
	}
	if err := db.RunCommand(ctx, bson.D{{Key: "collMod", Value: "items"}, {Key: "changeStreamPreAndPostImages", Value: bson.D{{Key: "enabled", Value: true}}}}).Err(); err != nil {
		t.Fatalf("enable pre-images: %v", err)
	}
	return &feedFixture{
		client: client, realm: realm, items: db.Collection("items"),
		set: RealtimeWatchSet(reg), src: &ChangeSource{Client: client, MaxAwait: 200 * time.Millisecond},
	}
}

// next reads the next event that carries a change, within a deadline.
func next(t *testing.T, ctx context.Context, st changefeed.Stream) changefeed.Change {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		ev, err := st.Next(ctx)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if ev.Change != nil {
			return *ev.Change
		}
	}
}

func TestRealtimeWatchSetFromRegistry(t *testing.T) {
	f := newFeedFixture(t)
	if len(f.set.Collections) != 1 || f.set.Collections[0].Coll != "items" || f.set.Collections[0].DB != f.realm+"__app" || f.set.Collections[0].Realm != f.realm {
		t.Fatalf("collections = %+v", f.set.Collections)
	}
	if len(f.set.System) != len(AccessCollections) {
		t.Fatalf("system = %+v", f.set.System)
	}
	for _, n := range f.set.System {
		if n.DB != SystemDatabaseName(f.realm) {
			t.Errorf("system namespace in %q", n.DB)
		}
	}
}

func TestFeedDeliversCreateUpdateReplaceRemoveWithImages(t *testing.T) {
	f := newFeedFixture(t)
	ctx := context.Background()
	st, err := f.src.Open(ctx, f.set, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	doc := bson.D{{Key: "_id", Value: "i1"}, {Key: "_meta", Value: bson.D{{Key: "version", Value: int64(1)}}}, {Key: "name", Value: "a"}}
	if _, err := f.items.InsertOne(ctx, doc); err != nil {
		t.Fatal(err)
	}
	c := next(t, ctx, st)
	if c.Op != changefeed.Created || c.Key != "i1" || c.Realm != f.realm || c.System || c.Before != nil || c.After["name"] != "a" || c.After["id"] != "i1" || c.Version != 1 {
		t.Fatalf("created = %+v", c)
	}

	// A replacement (how backd writes) carries both images and no field list.
	repl := bson.D{{Key: "_id", Value: "i1"}, {Key: "_meta", Value: bson.D{{Key: "version", Value: int64(2)}}}, {Key: "name", Value: "b"}}
	if _, err := f.items.ReplaceOne(ctx, bson.D{{Key: "_id", Value: "i1"}}, repl); err != nil {
		t.Fatal(err)
	}
	c = next(t, ctx, st)
	if c.Op != changefeed.Updated || !c.Replace || c.Before["name"] != "a" || c.After["name"] != "b" || c.Version != 2 || len(c.Fields) != 0 {
		t.Fatalf("replaced = %+v", c)
	}

	// A $set update reports the fields it touched and still has both images.
	if _, err := f.items.UpdateByID(ctx, "i1", bson.D{{Key: "$set", Value: bson.D{{Key: "name", Value: "c"}}}}); err != nil {
		t.Fatal(err)
	}
	c = next(t, ctx, st)
	if c.Op != changefeed.Updated || c.Replace || c.Before["name"] != "b" || c.After["name"] != "c" || len(c.Fields) != 1 || c.Fields[0] != "name" {
		t.Fatalf("updated = %+v", c)
	}

	// A removal has the last document as its before image and the version it had.
	if _, err := f.items.DeleteOne(ctx, bson.D{{Key: "_id", Value: "i1"}}); err != nil {
		t.Fatal(err)
	}
	c = next(t, ctx, st)
	if c.Op != changefeed.Removed || c.After != nil || c.Before["name"] != "c" || c.Version != 2 || c.Key != "i1" {
		t.Fatalf("removed = %+v", c)
	}
}

func TestFeedIgnoresCollectionsOutsideTheSet(t *testing.T) {
	f := newFeedFixture(t)
	ctx := context.Background()
	st, err := f.src.Open(ctx, f.set, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	other := f.client.Database(f.realm + "__app").Collection("other")
	if _, err := other.InsertOne(ctx, bson.D{{Key: "_id", Value: "o1"}}); err != nil {
		t.Fatal(err)
	}
	// The write outside the set comes first; the first change delivered must be the marker.
	if _, err := f.items.InsertOne(ctx, bson.D{{Key: "_id", Value: "marker"}}); err != nil {
		t.Fatal(err)
	}
	if c := next(t, ctx, st); c.Key != "marker" || c.Coll != "items" {
		t.Fatalf("first change = %+v, want the marker of the watched collection", c)
	}
}

func TestFeedResumesAfterAPosition(t *testing.T) {
	f := newFeedFixture(t)
	ctx := context.Background()
	st, err := f.src.Open(ctx, f.set, nil)
	if err != nil {
		t.Fatal(err)
	}
	insert := func(id string) {
		t.Helper()
		if _, err := f.items.InsertOne(ctx, bson.D{{Key: "_id", Value: id}, {Key: "_meta", Value: bson.D{{Key: "version", Value: int64(1)}}}}); err != nil {
			t.Fatal(err)
		}
	}
	insert("a")
	ctxT, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var pos changefeed.Position
	for pos == nil {
		ev, err := st.Next(ctxT)
		if err != nil {
			t.Fatal(err)
		}
		if ev.Change != nil {
			pos = ev.Position
		}
	}
	st.Close()
	insert("b") // written while no stream was open

	st2, err := f.src.Open(ctx, f.set, pos)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if c := next(t, ctx, st2); c.Key != "b" {
		t.Fatalf("resumed stream's first change = %q, want b", c.Key)
	}
}

// A system document's values never leave MongoDB: the stream sees its key and the names of
// the fields that changed.
func TestFeedProjectsSystemCollectionsToKeysAndFieldNames(t *testing.T) {
	f := newFeedFixture(t)
	ctx := context.Background()
	st, err := f.src.Open(ctx, f.set, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sys := f.client.Database(SystemDatabaseName(f.realm))
	sessions, users := sys.Collection(SessionsCollection), sys.Collection(UsersCollection)

	if _, err := sessions.InsertOne(ctx, bson.D{{Key: "_id", Value: "s1"}, {Key: "token_hash", Value: "SECRET-HASH"}, {Key: "user_id", Value: "u1"}, {Key: "last_used_at", Value: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	c := next(t, ctx, st)
	if !c.System || c.Realm != f.realm || c.Coll != SessionsCollection || c.Op != changefeed.Created || c.Key != "s1" || c.After != nil || c.Before != nil {
		t.Fatalf("session created = %+v", c)
	}

	// The sliding use of a session is noise: nothing arrives.
	if _, err := sessions.UpdateByID(ctx, "s1", bson.D{{Key: "$set", Value: bson.D{{Key: "last_used_at", Value: time.Now()}, {Key: "expires_at", Value: time.Now().Add(time.Hour)}}}}); err != nil {
		t.Fatal(err)
	}

	// A change to a field that matters arrives as a name; had the session update leaked, it
	// would have come first.
	if _, err := users.InsertOne(ctx, bson.D{{Key: "_id", Value: "u1"}, {Key: "email", Value: "a@example.com"}, {Key: "roles", Value: bson.A{"user"}}}); err != nil {
		t.Fatal(err)
	}
	if c := next(t, ctx, st); c.Coll != UsersCollection || c.Op != changefeed.Created {
		t.Fatalf("after the filtered session update the first change = %+v, want the user's creation", c)
	}
	if _, err := users.UpdateByID(ctx, "u1", bson.D{{Key: "$set", Value: bson.D{{Key: "roles", Value: bson.A{"user", "admin"}}}}}); err != nil {
		t.Fatal(err)
	}
	c = next(t, ctx, st)
	if !c.System || c.Coll != UsersCollection || c.Op != changefeed.Updated || c.Key != "u1" || len(c.Fields) != 1 || c.Fields[0] != "roles" || c.After != nil {
		t.Fatalf("user updated = %+v", c)
	}

	if _, err := sessions.DeleteOne(ctx, bson.D{{Key: "_id", Value: "s1"}}); err != nil {
		t.Fatal(err)
	}
	c = next(t, ctx, st)
	if c.Coll != SessionsCollection || c.Op != changefeed.Removed || c.Key != "s1" {
		t.Fatalf("session removed = %+v", c)
	}
}

// A session update that touches a real field (not only its use) is delivered.
func TestFeedKeepsSessionUpdatesThatChangeMoreThanUse(t *testing.T) {
	f := newFeedFixture(t)
	ctx := context.Background()
	sessions := f.client.Database(SystemDatabaseName(f.realm)).Collection(SessionsCollection)
	if _, err := sessions.InsertOne(ctx, bson.D{{Key: "_id", Value: "s1"}, {Key: "user_id", Value: "u1"}, {Key: "mfa", Value: false}}); err != nil {
		t.Fatal(err)
	}
	st, err := f.src.Open(ctx, f.set, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := sessions.UpdateByID(ctx, "s1", bson.D{{Key: "$set", Value: bson.D{{Key: "last_used_at", Value: time.Now()}, {Key: "mfa", Value: true}}}}); err != nil {
		t.Fatal(err)
	}
	c := next(t, ctx, st)
	if c.Key != "s1" || c.Op != changefeed.Updated {
		t.Fatalf("change = %+v", c)
	}
}

func TestClassifyFeedErrorFindsLostHistory(t *testing.T) {
	for _, err := range []error{
		mongo.CommandError{Code: 286, Name: "ChangeStreamHistoryLost", Message: "Resume of change stream was not possible"},
		mongo.CommandError{Code: 280, Name: "ChangeStreamFatalError", Message: "cannot resume"},
		errors.New("resume of change stream was not possible, as the resume point may no longer be in the oplog"),
	} {
		if got := classifyFeedError(err); !errors.Is(got, changefeed.ErrHistoryLost) {
			t.Errorf("%v: not classified as lost history (%v)", err, got)
		}
	}
	other := errors.New("connection reset by peer")
	if got := classifyFeedError(other); errors.Is(got, changefeed.ErrHistoryLost) || got != other {
		t.Errorf("transient error changed: %v", got)
	}
	if classifyFeedError(nil) != nil {
		t.Error("nil must stay nil")
	}
}

// A stream whose cursor is lost on the server (what a failover or a restart of the node it
// reads from does to it) keeps delivering: the driver resumes it from its own position.
// The test kills the cursor rather than stepping the primary down because the replica set is
// shared with tests running in parallel.
func TestFeedSurvivesALostCursor(t *testing.T) {
	f := newFeedFixture(t)
	ctx := context.Background()
	st, err := f.src.Open(ctx, f.set, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	insert := func(id string) {
		t.Helper()
		if _, err := f.items.InsertOne(ctx, bson.D{{Key: "_id", Value: id}, {Key: "_meta", Value: bson.D{{Key: "version", Value: int64(1)}}}}); err != nil {
			t.Fatal(err)
		}
	}
	insert("one")
	if c := next(t, ctx, st); c.Key != "one" {
		t.Fatalf("first = %+v", c)
	}
	cs := st.(*changeStream).cs
	err = f.client.Database("admin").RunCommand(ctx, bson.D{{Key: "killCursors", Value: "$cmd.aggregate"}, {Key: "cursors", Value: bson.A{cs.ID()}}}).Err()
	if err != nil {
		t.Fatalf("killCursors: %v", err)
	}
	insert("two")
	if c := next(t, ctx, st); c.Key != "two" {
		t.Fatalf("after the cursor was killed the stream delivered %+v, want two", c)
	}
}

// A consumer on the real source continues after the watched set changes: the stream is
// replaced and the position carries over, so nothing written meanwhile is lost.
func TestConsumerContinuesAfterSetChange(t *testing.T) {
	f := newFeedFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &changefeed.Consumer{Name: "test", Source: f.src, Backoff: func(int) time.Duration { return 50 * time.Millisecond }}
	c.SetWatched(f.set)
	got := make(chan changefeed.Change, 32)
	go c.Run(ctx, nil, handlerFunc(func(ch changefeed.Change) { got <- ch })) //nolint:errcheck

	insert := func(id string) {
		t.Helper()
		if _, err := f.items.InsertOne(ctx, bson.D{{Key: "_id", Value: id}, {Key: "_meta", Value: bson.D{{Key: "version", Value: int64(1)}}}}); err != nil {
			t.Fatal(err)
		}
	}
	expect := func(id string) {
		t.Helper()
		deadline := time.After(15 * time.Second)
		for {
			select {
			case ch := <-got:
				if ch.Key == id {
					return
				}
			case <-deadline:
				t.Fatalf("no change for %q", id)
			}
		}
	}
	// A stream opens from now: give the consumer time to open before the first write.
	time.Sleep(time.Second)
	insert("before")
	expect("before")

	bigger := f.set
	bigger.Collections = append(append([]changefeed.Namespace(nil), f.set.Collections...), changefeed.Namespace{Realm: f.realm, DB: f.realm + "__app", Coll: "other"})
	c.SetWatched(bigger)
	insert("after-set-change")
	expect("after-set-change")
}

type handlerFunc func(changefeed.Change)

func (h handlerFunc) Change(_ context.Context, c changefeed.Change, _ changefeed.Position) { h(c) }
func (handlerFunc) Idle(context.Context, changefeed.Position)                              {}
func (handlerFunc) Gap(context.Context, changefeed.Gap)                                    {}
