package mongodb

import (
	"fmt"
	"slices"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/fernandezvara/backd/internal/storage"
)

// toBSON converts an API-shaped document into BSON: "id" becomes "_id",
// and keys are ordered (_id, _meta, then user fields sorted, recursively)
// so stored documents are deterministic and equality on sub-documents is
// independent of the client's key order.
func toBSON(doc storage.Document) bson.D {
	out := bson.D{}
	if id, ok := doc["id"]; ok {
		out = append(out, bson.E{Key: "_id", Value: id})
	}
	if meta, ok := doc["_meta"]; ok {
		out = append(out, bson.E{Key: "_meta", Value: toBSONValue(meta)})
	}
	for _, k := range sortedKeys(doc) {
		if k == "id" || k == "_meta" {
			continue
		}
		out = append(out, bson.E{Key: k, Value: toBSONValue(doc[k])})
	}
	return out
}

func toBSONValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		d := make(bson.D, 0, len(t))
		for _, k := range sortedKeys(t) {
			d = append(d, bson.E{Key: k, Value: toBSONValue(t[k])})
		}
		return d
	case []any:
		a := make(bson.A, len(t))
		for i, e := range t {
			a[i] = toBSONValue(e)
		}
		return a
	case time.Time:
		return bson.NewDateTimeFromTime(t)
	}
	return v
}

// fromBSON converts a stored document into API shape.
func fromBSON(d bson.D) storage.Document {
	out := make(storage.Document, len(d))
	for _, e := range d {
		key := e.Key
		if key == "_id" {
			key = "id"
		}
		out[key] = fromBSONValue(e.Value)
	}
	return out
}

func fromBSONValue(v any) any {
	switch t := v.(type) {
	case bson.D:
		m := make(map[string]any, len(t))
		for _, e := range t {
			m[e.Key] = fromBSONValue(e.Value)
		}
		return m
	case bson.A:
		s := make([]any, len(t))
		for i, e := range t {
			s[i] = fromBSONValue(e)
		}
		return s
	case bson.DateTime:
		return t.Time().UTC()
	case int32:
		return int64(t)
	case bson.ObjectID:
		return t.Hex()
	case bson.Decimal128:
		return t.String()
	case bson.Null, bson.Undefined:
		return nil
	case string, bool, int64, float64, nil:
		return t
	}
	// Types backd never writes (only possible for data written outside the API).
	return fmt.Sprint(v)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
