package mongodb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"github.com/fernandezvara/backd/internal/changefeed"
	"github.com/fernandezvara/backd/internal/registry"
)

// ConnectFeed opens the client change streams are read on (design/triggers-and-realtime.md
// §3). It is its own client, apart from the one serving requests, so long-lived cursors
// never compete with request traffic for connections, and it reads from secondaries when
// the topology has them: events carry their images, so nothing is looked up on the primary.
// A readPreference in the URI wins.
func ConnectFeed(ctx context.Context, uri string) (*mongo.Client, error) {
	opts := options.Client().ApplyURI(uri)
	if opts.ReadPreference == nil {
		opts.SetReadPreference(readpref.SecondaryPreferred())
	}
	opts.SetMaxPoolSize(8)
	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, fmt.Errorf("mongodb: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("mongodb: ping: %w", err)
	}
	return client, nil
}

// AccessCollections are the system collections whose changes a stream's access depends on:
// who is signed in, what roles and memberships they hold, and the organizations those belong
// to. Consents live in the user's own document (`users.consents`).
var AccessCollections = []string{
	UsersCollection, SessionsCollection, APIKeysCollection, OrgsCollection, OrgMembersCollection, OrgTeamsCollection,
	AppConfigCollection,
}

// noisyFields are the fields a system collection updates all the time with nothing anyone
// needs to know: a session's use (and its sliding expiry) and an API key's last use. An update
// that touches nothing else is dropped where the data is read.
var noisyFields = map[string][]string{
	SessionsCollection: {"last_used_at", "expires_at"},
	APIKeysCollection:  {"last_used_at"},
}

// RealtimeWatchSet is what the realtime consumer reads: the realtime collections of every
// realm and, for each realm that has any, the system collections its access depends on.
func RealtimeWatchSet(reg *registry.Registry) changefeed.WatchSet {
	var set changefeed.WatchSet
	realms := map[string]bool{}
	for _, c := range reg.EventCollections() {
		if !c.Realtime {
			continue
		}
		set.Collections = append(set.Collections, changefeed.Namespace{Realm: c.Realm, DB: c.MongoDatabase, Coll: c.Name})
		realms[c.Realm] = true
	}
	// A stream can follow a realm's app configuration with no realtime collection at all.
	for realm, rl := range reg.Realms {
		if rl.AppConfig != nil && !realms[realm] {
			set.System = append(set.System, changefeed.Namespace{Realm: realm, DB: SystemDatabaseName(realm), Coll: AppConfigCollection})
		}
	}
	for realm := range realms {
		for _, name := range AccessCollections {
			set.System = append(set.System, changefeed.Namespace{Realm: realm, DB: SystemDatabaseName(realm), Coll: name})
		}
	}
	return set
}

// ChangeSource implements changefeed.Source over one deployment-wide change stream.
type ChangeSource struct {
	Client *mongo.Client
	// MaxAwait is how long the server holds a read open waiting for changes; 0 is one second.
	MaxAwait time.Duration
	// Decrypt opens the encrypted fields of a changed document, in place (nil: the collections
	// of this deployment encrypt nothing). The consumers of the feed work on plaintext, as a
	// read through the API does.
	Decrypt func(ctx context.Context, n changefeed.Namespace, doc map[string]any) error
}

// NewChangeSource reads change streams on client (see ConnectFeed).
func NewChangeSource(client *mongo.Client) *ChangeSource { return &ChangeSource{Client: client} }

// Open starts a stream over the set. The images come with the events (`whenAvailable`):
// `updateLookup` is never used, because it returns the document as it is now, which can be
// later than the change being read.
func (s *ChangeSource) Open(ctx context.Context, set changefeed.WatchSet, after changefeed.Position) (changefeed.Stream, error) {
	await := s.MaxAwait
	if await <= 0 {
		await = time.Second
	}
	opts := options.ChangeStream().
		SetFullDocument(options.WhenAvailable).
		SetFullDocumentBeforeChange(options.WhenAvailable).
		SetMaxAwaitTime(await)
	if after != nil {
		opts.SetResumeAfter(bson.Raw(after))
	}
	cs, err := s.Client.Watch(ctx, feedPipeline(set), opts)
	if err != nil {
		return nil, classifyFeedError(err)
	}
	ns := map[string]changefeed.Namespace{}
	system := map[string]bool{}
	for _, n := range set.Collections {
		ns[n.DB+"\x00"+n.Coll] = n
	}
	for _, n := range set.System {
		ns[n.DB+"\x00"+n.Coll] = n
		system[n.DB+"\x00"+n.Coll] = true
	}
	st := &changeStream{cs: cs, ns: ns, system: system, decrypt: s.Decrypt}
	st.last = bytesCopy(cs.ResumeToken())
	return st, nil
}

// feedPipeline builds the stages that make a stream cheap and safe:
//
//  1. only the watched namespaces;
//  2. no update of a system collection that touches only fields nobody needs to hear about;
//  3. system collections reduced where they are read to the document key and the names of the
//     fields that changed. No value of a system document leaves MongoDB: password hashes,
//     second-factor secrets and token hashes never reach backd's memory, logs or events.
func feedPipeline(set changefeed.WatchSet) mongo.Pipeline {
	byDB := map[string][]string{}
	var dbs []string
	add := func(n changefeed.Namespace) {
		if _, ok := byDB[n.DB]; !ok {
			dbs = append(dbs, n.DB)
		}
		byDB[n.DB] = append(byDB[n.DB], n.Coll)
	}
	for _, n := range set.Collections {
		add(n)
	}
	for _, n := range set.System {
		add(n)
	}
	var or bson.A
	for _, db := range dbs {
		or = append(or, bson.D{{Key: "ns.db", Value: db}, {Key: "ns.coll", Value: bson.D{{Key: "$in", Value: dedupe(byDB[db])}}}})
	}
	pipe := mongo.Pipeline{{{Key: "$match", Value: bson.D{{Key: "$or", Value: or}}}}}
	if len(set.System) == 0 {
		return pipe
	}
	sysDBs := bson.A{}
	seen := map[string]bool{}
	for _, n := range set.System {
		if !seen[n.DB] {
			seen[n.DB] = true
			sysDBs = append(sysDBs, n.DB)
		}
	}
	fieldNames := bson.D{{Key: "$map", Value: bson.D{
		{Key: "input", Value: bson.D{{Key: "$objectToArray", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$updateDescription.updatedFields", bson.D{}}}}}}},
		{Key: "as", Value: "f"},
		{Key: "in", Value: "$$f.k"},
	}}}
	removed := bson.D{{Key: "$ifNull", Value: bson.A{"$updateDescription.removedFields", bson.A{}}}}

	var noise bson.A
	for _, coll := range []string{SessionsCollection, APIKeysCollection} {
		noisy := bson.A{}
		for _, f := range noisyFields[coll] {
			noisy = append(noisy, f)
		}
		noise = append(noise, bson.D{{Key: "$and", Value: bson.A{
			bson.D{{Key: "$in", Value: bson.A{"$ns.db", sysDBs}}},
			bson.D{{Key: "$eq", Value: bson.A{"$ns.coll", coll}}},
			bson.D{{Key: "$eq", Value: bson.A{"$operationType", "update"}}},
			bson.D{{Key: "$eq", Value: bson.A{bson.D{{Key: "$size", Value: bson.D{{Key: "$setDifference", Value: bson.A{fieldNames, noisy}}}}}, 0}}},
			bson.D{{Key: "$eq", Value: bson.A{bson.D{{Key: "$size", Value: removed}}, 0}}},
		}}})
	}
	pipe = append(pipe, bson.D{{Key: "$match", Value: bson.D{{Key: "$expr", Value: bson.D{{Key: "$not", Value: bson.D{{Key: "$or", Value: noise}}}}}}}})

	minimal := bson.D{
		{Key: "_id", Value: "$_id"},
		{Key: "operationType", Value: "$operationType"},
		{Key: "ns", Value: "$ns"},
		{Key: "documentKey", Value: "$documentKey"},
		{Key: "clusterTime", Value: "$clusterTime"},
		{Key: "fieldNames", Value: fieldNames},
	}
	pipe = append(pipe, bson.D{{Key: "$replaceWith", Value: bson.D{{Key: "$cond", Value: bson.A{
		bson.D{{Key: "$in", Value: bson.A{"$ns.db", sysDBs}}},
		minimal,
		"$$ROOT",
	}}}}})
	return pipe
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func bytesCopy(b []byte) []byte {
	if b == nil {
		return nil
	}
	return bytes.Clone(b)
}

// classifyFeedError turns the server's "the history no longer reaches your position" into
// changefeed.ErrHistoryLost. Everything else is left for the consumer to retry.
func classifyFeedError(err error) error {
	if err == nil {
		return nil
	}
	var ce mongo.CommandError
	if errors.As(err, &ce) {
		switch ce.Code {
		case 286, 280: // ChangeStreamHistoryLost, ChangeStreamFatalError
			return fmt.Errorf("%w: %v", changefeed.ErrHistoryLost, err)
		}
	}
	msg := err.Error()
	if strings.Contains(msg, "resume token was not found") || strings.Contains(msg, "resume of change stream was not possible") ||
		strings.Contains(msg, "ChangeStreamHistoryLost") {
		return fmt.Errorf("%w: %v", changefeed.ErrHistoryLost, err)
	}
	return err
}

// changeStream is one open deployment-wide stream.
type changeStream struct {
	cs     *mongo.ChangeStream
	ns     map[string]changefeed.Namespace
	system map[string]bool
	last   []byte // the last position handed out, to report a quiet period only once
	// decrypt opens the encrypted fields of the documents of a change (see ChangeSource.Decrypt).
	decrypt func(ctx context.Context, n changefeed.Namespace, doc map[string]any) error
}

func (s *changeStream) Position() changefeed.Position { return bytesCopy(s.cs.ResumeToken()) }

func (s *changeStream) Close() { _ = s.cs.Close(context.Background()) }

// Next returns the next change, or — when a read came back empty but the position moved — the
// new position.
func (s *changeStream) Next(ctx context.Context) (changefeed.Event, error) {
	for {
		if s.cs.TryNext(ctx) {
			c, ok, err := s.decode(s.cs.Current)
			if err != nil {
				return changefeed.Event{}, err
			}
			if ok && !c.System && s.decrypt != nil {
				n := s.ns[c.DB+"\x00"+c.Coll]
				for _, d := range []map[string]any{c.After, c.Before} {
					if d == nil {
						continue
					}
					if err := s.decrypt(ctx, n, d); err != nil {
						return changefeed.Event{}, err
					}
				}
				// A migration rewrites how a value is stored, not the value: when the document
				// reads the same before and after, nobody needs to hear about it.
				if c.Op == changefeed.Updated && c.Before != nil && c.After != nil && reflect.DeepEqual(c.Before, c.After) {
					ok = false
				}
			}
			pos := bytesCopy(s.cs.ResumeToken())
			s.last = pos
			if !ok {
				return changefeed.Event{Position: pos}, nil
			}
			return changefeed.Event{Change: &c, Position: pos}, nil
		}
		if err := s.cs.Err(); err != nil {
			if ctx.Err() != nil {
				return changefeed.Event{}, ctx.Err()
			}
			return changefeed.Event{}, classifyFeedError(err)
		}
		if err := ctx.Err(); err != nil {
			return changefeed.Event{}, err
		}
		if pos := s.cs.ResumeToken(); pos != nil && !bytes.Equal(pos, s.last) {
			s.last = bytesCopy(pos)
			return changefeed.Event{Position: bytesCopy(pos)}, nil
		}
	}
}

// rawEvent is the part of a change event the feed reads.
type rawEvent struct {
	OperationType string `bson:"operationType"`
	NS            struct {
		DB   string `bson:"db"`
		Coll string `bson:"coll"`
	} `bson:"ns"`
	DocumentKey              bson.D         `bson:"documentKey"`
	ClusterTime              bson.Timestamp `bson:"clusterTime"`
	FullDocument             bson.D         `bson:"fullDocument"`
	FullDocumentBeforeChange bson.D         `bson:"fullDocumentBeforeChange"`
	UpdateDescription        *struct {
		UpdatedFields bson.D `bson:"updatedFields"`
	} `bson:"updateDescription"`
	FieldNames []string `bson:"fieldNames"`
}

// decode turns one event into a Change; ok is false for an event that isn't a document
// change (a drop, a rename, an invalidation), which the feed skips.
func (s *changeStream) decode(raw bson.Raw) (c changefeed.Change, ok bool, err error) {
	var ev rawEvent
	if err := bson.Unmarshal(raw, &ev); err != nil {
		// The event may carry values; report where it came from, never what it held.
		return c, false, fmt.Errorf("changefeed: undecodable change event: %w", err)
	}
	var op changefeed.Op
	replace := false
	switch ev.OperationType {
	case "insert":
		op = changefeed.Created
	case "update":
		op = changefeed.Updated
	case "replace":
		op, replace = changefeed.Updated, true
	case "delete":
		op = changefeed.Removed
	default:
		return c, false, nil
	}
	key := ev.NS.DB + "\x00" + ev.NS.Coll
	n, known := s.ns[key]
	if !known {
		return c, false, nil // outside the set: a stale event of a stream being replaced
	}
	c = changefeed.Change{
		Realm: n.Realm, DB: ev.NS.DB, Coll: ev.NS.Coll, System: s.system[key], Op: op, Replace: replace,
		At: time.Unix(int64(ev.ClusterTime.T), 0).UTC(), Increment: ev.ClusterTime.I,
	}
	for _, e := range ev.DocumentKey {
		if e.Key == "_id" {
			c.Key = idString(e.Value)
		}
	}
	if c.System {
		c.Fields = ev.FieldNames
		return c, true, nil
	}
	if len(ev.FullDocument) > 0 {
		c.After = fromBSON(ev.FullDocument)
	}
	if len(ev.FullDocumentBeforeChange) > 0 {
		c.Before = fromBSON(ev.FullDocumentBeforeChange)
	}
	if ev.UpdateDescription != nil {
		for _, e := range ev.UpdateDescription.UpdatedFields {
			c.Fields = append(c.Fields, e.Key)
		}
	}
	doc := c.After
	if doc == nil {
		doc = c.Before
	}
	c.Version = versionOf(doc)
	return c, true, nil
}

func idString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// versionOf reads `_meta.version` of a document in API shape, 0 when there is none.
func versionOf(doc map[string]any) int64 {
	meta, _ := doc["_meta"].(map[string]any)
	switch v := meta["version"].(type) {
	case int64:
		return v
	case int32:
		return int64(v)
	case int:
		return int64(v)
	case float64:
		return int64(v)
	}
	return 0
}
