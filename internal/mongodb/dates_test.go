package mongodb

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/fernandezvara/backd/internal/query"
	"github.com/fernandezvara/backd/internal/storage"
)

const datesSchema = `{
  "type": "object",
  "properties": {
    "title":     {"type": "string"},
    "starts_at": {"type": "string", "format": "date-time", "x-backd-store": "date"},
    "ends_at":   {"type": ["string", "null"], "format": "date-time", "x-backd-store": "date"}
  },
  "required": ["title", "starts_at"]
}`

// Fields stored as dates are BSON Dates, the validator says so, and where,
// order_by and cursors compare them as dates.
func TestDatesOnMongoDB(t *testing.T) {
	c, repo := provisionOne(t, datesSchema)
	ctx := context.Background()
	base := time.Date(2126, 1, 1, 0, 0, 0, 0, time.UTC)
	at := func(days int) time.Time { return base.AddDate(0, 0, days) }
	create := func(title string, start time.Time, end any) string {
		t.Helper()
		doc := newDoc(map[string]any{"title": title, "starts_at": start, "ends_at": end})
		if err := repo.Create(ctx, doc); err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		return doc["id"].(string)
	}
	// Equal starts, a null end, and out-of-order creation.
	create("c", at(2), at(9))
	create("a", at(0), nil)
	create("b", at(2), at(5))
	create("d", at(4), nil)

	// The stored value is a BSON date, not text.
	var raw bson.M
	if err := repo.coll.FindOne(ctx, bson.D{{Key: "title", Value: "a"}}).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["starts_at"].(bson.DateTime); !ok {
		t.Errorf("starts_at stored as %T, want a date", raw["starts_at"])
	}
	// The database refuses text, and null where the schema doesn't allow it.
	for name, doc := range map[string]bson.D{
		"text":          {{Key: "_id", Value: "t1"}, {Key: "title", Value: "x"}, {Key: "starts_at", Value: "2126-01-01T00:00:00Z"}},
		"null required": {{Key: "_id", Value: "t2"}, {Key: "title", Value: "x"}, {Key: "starts_at", Value: nil}},
		"text end":      {{Key: "_id", Value: "t3"}, {Key: "title", Value: "x"}, {Key: "starts_at", Value: at(1)}, {Key: "ends_at", Value: "soon"}},
	} {
		doc = append(doc, bson.E{Key: "_meta", Value: bson.D{{Key: "created_at", Value: at(0)}, {Key: "updated_at", Value: at(0)}, {Key: "version", Value: int64(1)}}})
		if _, err := repo.coll.InsertOne(ctx, doc); err == nil {
			t.Errorf("the validator accepted %s", name)
		}
	}

	// Reading gives times back.
	page, err := repo.List(ctx, storage.Query{Limit: 10})
	if err != nil || len(page.Items) != 4 {
		t.Fatalf("list: %v %v", page.Items, err)
	}
	if _, ok := page.Items[0]["starts_at"].(time.Time); !ok {
		t.Errorf("read starts_at as %T, want a time", page.Items[0]["starts_at"])
	}

	// where, in any offset.
	titles := func(where string) []string {
		t.Helper()
		f, err := query.ParseWhere(where, c.Fields)
		if err != nil {
			t.Fatal(err)
		}
		sort, _ := query.ParseOrderBy("starts_at,title", c.Fields)
		p, err := repo.List(ctx, storage.Query{Filter: f, Sort: sort, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, d := range p.Items {
			out = append(out, d["title"].(string))
		}
		return out
	}
	join := func(s []string) string {
		out := ""
		for _, x := range s {
			out += x
		}
		return out
	}
	day2 := at(2).Format(time.RFC3339)
	if got := join(titles(`{"starts_at": {"$gte": "` + day2 + `"}}`)); got != "bcd" {
		t.Errorf("$gte: %s", got)
	}
	if got := join(titles(`{"starts_at": {"$gt": "` + at(2).In(time.FixedZone("x", 3600)).Format(time.RFC3339) + `"}}`)); got != "d" {
		t.Errorf("$gt with an offset: %s", got)
	}
	if got := join(titles(`{"starts_at": {"$between": ["` + at(1).Format(time.RFC3339) + `", "` + at(3).Format(time.RFC3339) + `"]}}`)); got != "bc" {
		t.Errorf("$between: %s", got)
	}
	if got := join(titles(`{"ends_at": null}`)); got != "ad" {
		t.Errorf("null end: %s", got)
	}
	if got := join(titles(`{"ends_at": {"$gt": "` + at(6).Format(time.RFC3339) + `"}}`)); got != "c" {
		t.Errorf("end $gt: %s", got)
	}

	// A cursor over dates (ties on starts_at, nulls in ends_at) loses and repeats nothing.
	for _, order := range []string{"starts_at", "-starts_at", "ends_at,title", "-ends_at,-title"} {
		sort, err := query.ParseOrderBy(order, c.Fields)
		if err != nil {
			t.Fatal(err)
		}
		all, _ := repo.List(ctx, storage.Query{Sort: sort, Limit: 10})
		var got []string
		var after storage.Filter
		for range 10 {
			p, err := repo.List(ctx, storage.Query{Sort: sort, Limit: 1, After: after})
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range p.Items {
				got = append(got, d["title"].(string))
			}
			if !p.HasMore {
				break
			}
			if after, err = query.ParseCursor(query.EncodeCursor(sort, p.Items[0]), sort, c.Fields); err != nil {
				t.Fatal(err)
			}
		}
		var want []string
		for _, d := range all.Items {
			want = append(want, d["title"].(string))
		}
		if join(got) != join(want) || len(got) != 4 {
			t.Errorf("order_by=%s: cursor pages %v, full list %v", order, got, want)
		}
	}
}
