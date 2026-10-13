package mongodb

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/appconfig"
)

// appConfigStateID is the _id of the one document that holds the current values.
const appConfigStateID = "current"

// Values and changes are stored as JSON text: numbers keep their JSON meaning
// and nothing depends on how BSON decodes nested documents.
type appConfigStateDoc struct {
	ID        string    `bson:"_id"`
	Version   int64     `bson:"version"`
	Values    string    `bson:"values"`
	UpdatedAt time.Time `bson:"updated_at"`
	UpdatedBy string    `bson:"updated_by,omitempty"`
}

type appConfigEntryDoc struct {
	Version      int64     `bson:"_id"`
	At           time.Time `bson:"at"`
	Actor        string    `bson:"actor"`
	Kind         string    `bson:"kind"`
	RolledBackTo int64     `bson:"rolled_back_to,omitempty"`
	Changes      string    `bson:"changes"`
	Values       string    `bson:"values"`
}

type appConfigChangeJSON struct {
	Path string `json:"path"`
	Old  any    `json:"old"`
	New  any    `json:"new"`
}

func (s *AuthStore) appConfig() *mongo.Collection {
	return s.db.Collection(AppConfigCollection)
}

func (s *AuthStore) appConfigHistory() *mongo.Collection {
	return s.db.Collection(AppConfigHistoryCollection)
}

func decodeValues(text string) map[string]any {
	out := map[string]any{}
	if text != "" {
		_ = json.Unmarshal([]byte(text), &out)
	}
	if out == nil {
		out = map[string]any{}
	}
	return out
}

func encodeValues(v map[string]any) string {
	if v == nil {
		v = map[string]any{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func (s *AuthStore) AppConfigState(ctx context.Context) (appconfig.State, error) {
	var d appConfigStateDoc
	err := s.appConfig().FindOne(ctx, bson.D{{Key: "_id", Value: appConfigStateID}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return appconfig.State{Values: map[string]any{}}, nil
	}
	if err != nil {
		return appconfig.State{}, err
	}
	return appconfig.State{Version: d.Version, Values: decodeValues(d.Values), UpdatedAt: d.UpdatedAt.UTC(), UpdatedBy: d.UpdatedBy}, nil
}

// SaveAppConfig writes the state conditionally on its version, then the history entry.
// A crash between the two leaves a version without an entry, which only means a gap
// in the history; the values are never half written.
func (s *AuthStore) SaveAppConfig(ctx context.Context, expected int64, next appconfig.State, e appconfig.Entry, pruneTo int64, pruneBefore time.Time) error {
	doc := appConfigStateDoc{ID: appConfigStateID, Version: next.Version, Values: encodeValues(next.Values), UpdatedAt: next.UpdatedAt, UpdatedBy: next.UpdatedBy}
	if expected == 0 {
		// The first version: inserting fails when somebody else got there first.
		if _, err := s.appConfig().InsertOne(ctx, doc); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return appconfig.ErrConflict
			}
			return err
		}
	} else {
		res, err := s.appConfig().ReplaceOne(ctx, bson.D{{Key: "_id", Value: appConfigStateID}, {Key: "version", Value: expected}}, doc)
		if err != nil {
			return err
		}
		if res.MatchedCount == 0 {
			return appconfig.ErrConflict
		}
	}
	changes := make([]appConfigChangeJSON, len(e.Changes))
	for i, c := range e.Changes {
		changes[i] = appConfigChangeJSON{Path: c.Path, Old: c.Old, New: c.New}
	}
	cb, _ := json.Marshal(changes)
	if _, err := s.appConfigHistory().InsertOne(ctx, appConfigEntryDoc{
		Version: e.Version, At: e.At, Actor: e.Actor, Kind: e.Kind, RolledBackTo: e.RolledBackTo,
		Changes: string(cb), Values: encodeValues(e.Values),
	}); err != nil && !mongo.IsDuplicateKeyError(err) {
		return err
	}
	if pruneTo > 0 {
		// Best effort: an entry that stays is removed by the next change.
		_, _ = s.appConfigHistory().DeleteMany(ctx, bson.D{
			{Key: "_id", Value: bson.D{{Key: "$lte", Value: pruneTo}}},
			{Key: "at", Value: bson.D{{Key: "$lt", Value: pruneBefore}}},
		})
	}
	return nil
}

func entryFromDoc(d appConfigEntryDoc) appconfig.Entry {
	var cs []appConfigChangeJSON
	_ = json.Unmarshal([]byte(d.Changes), &cs)
	changes := make([]appconfig.Change, len(cs))
	for i, c := range cs {
		changes[i] = appconfig.Change{Path: c.Path, Old: c.Old, New: c.New}
	}
	return appconfig.Entry{
		Version: d.Version, At: d.At.UTC(), Actor: d.Actor, Kind: d.Kind, RolledBackTo: d.RolledBackTo,
		Changes: changes, Values: decodeValues(d.Values),
	}
}

func (s *AuthStore) AppConfigHistory(ctx context.Context, limit int) ([]appconfig.Entry, error) {
	cur, err := s.appConfigHistory().Find(ctx, bson.D{},
		options.Find().SetSort(bson.D{{Key: "_id", Value: -1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	var docs []appConfigEntryDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]appconfig.Entry, len(docs))
	for i, d := range docs {
		out[i] = entryFromDoc(d)
	}
	return out, nil
}

func (s *AuthStore) AppConfigEntry(ctx context.Context, version int64) (appconfig.Entry, error) {
	var d appConfigEntryDoc
	err := s.appConfigHistory().FindOne(ctx, bson.D{{Key: "_id", Value: version}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return appconfig.Entry{}, appconfig.ErrNoVersion
	}
	if err != nil {
		return appconfig.Entry{}, err
	}
	return entryFromDoc(d), nil
}
