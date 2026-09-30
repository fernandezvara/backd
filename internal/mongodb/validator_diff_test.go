package mongodb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/jsonnum"
)

// Guards against the API validator and the MongoDB validator disagreeing
// (risk R3): every document the API accepts must also be accepted by the
// database, or valid writes would fail. The database validator may be
// looser (it omits keywords such as format), never stricter.

const agreeSchema = `{
  "type": "object",
  "properties": {
    "name":   {"type": "string", "minLength": 1, "maxLength": 8, "pattern": "^[a-z]+$"},
    "age":    {"type": "integer", "minimum": 0, "maximum": 150},
    "big":    {"type": "integer"},
    "price":  {"type": ["number", "null"], "exclusiveMinimum": 0, "multipleOf": 0.5},
    "ratio":  {"type": "number", "maximum": 1},
    "status": {"enum": ["a", "b", 3]},
    "email":  {"type": "string", "format": "email"},
    "flag":   {"type": "boolean"},
    "tags":   {"type": "array", "items": {"type": "string"}, "minItems": 1, "maxItems": 3, "uniqueItems": true},
    "meta":   {"type": "object", "properties": {"k": {"type": "string"}}, "required": ["k"], "additionalProperties": false},
    "any":    {}
  },
  "required": ["name"],
  "additionalProperties": false
}`

// Candidate JSON values per property, valid and invalid, with number
// spellings chosen to probe int/float handling.
var agreeValues = map[string]struct{ valid, invalid []string }{
	"name":   {[]string{`"abc"`, `"z"`}, []string{`""`, `"ABC"`, `"abcdefghi"`, `5`, `null`}},
	"age":    {[]string{`0`, `150`, `1.0`, `1e2`, `-0`, `7.0e0`}, []string{`151`, `-1`, `1.5`, `"1"`, `null`}},
	"big":    {[]string{`9007199254740993`, `9223372036854775807`, `-9223372036854775808`, `9223372036854775808`, `1e18`, `3.0`, `1e30`}, []string{`2.5`, `1e-3`}},
	"price":  {[]string{`0.5`, `1`, `2.0`, `null`, `1e1`, `1.5e0`}, []string{`0`, `-1`, `0.3`, `"1"`}},
	"ratio":  {[]string{`1`, `1.0`, `0.25`, `-1e308`, `0`}, []string{`1.0000001`, `2`}},
	"status": {[]string{`"a"`, `"b"`, `3`, `3.0`}, []string{`"c"`, `null`, `4`}},
	"email":  {[]string{`"a@b.c"`}, []string{`"not-an-email"`}},
	"flag":   {[]string{`true`, `false`}, []string{`0`, `"true"`}},
	"tags":   {[]string{`["a"]`, `["a","b","c"]`}, []string{`[]`, `["a","a"]`, `["a","b","c","d"]`, `[1]`, `"a"`}},
	"meta":   {[]string{`{"k":"v"}`}, []string{`{}`, `{"k":"v","x":1}`, `{"k":1}`, `[]`}},
	"any":    {[]string{`1`, `1.5`, `"s"`, `null`, `[1,{"a":[]}]`, `{"n":1.0}`}, nil},
}

func TestValidatorsAgree(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	reg := loadRegistry(t, realm, map[string]string{"app/things": agreeSchema})
	log, _ := testLogger()
	if err := (&Provisioner{Client: client, Registry: reg, Log: log}).Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection(realm, "app", "things")
	repo := &Repository{coll: client.Database(c.MongoDatabase).Collection(c.Name)}

	rng := rand.New(rand.NewPCG(3, 5))
	props := []string{"name", "age", "big", "price", "ratio", "status", "email", "flag", "tags", "meta", "any"}
	var apiValid, dbLooser int
	check := func(body string) {
		dec := json.NewDecoder(bytes.NewReader([]byte(body)))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("bad test JSON %s: %v", body, err)
		}
		valid := c.Schema.Validate(v) == nil
		doc := jsonnum.Normalize(v).(map[string]any)
		now := time.Now().UTC()
		doc["id"] = fmt.Sprintf("x%d", rng.Int64())
		doc["_meta"] = sampleMeta(rng, now)
		err := repo.Create(context.Background(), doc)
		switch {
		case valid && err != nil:
			t.Errorf("API accepts but MongoDB rejects %s: %v", body, err)
		case valid:
			apiValid++
		case err == nil:
			dbLooser++ // allowed: the database validator is a subset
		}
	}

	// Each candidate value on its own (with the required name) …
	for _, p := range props {
		for _, val := range append(slices.Clone(agreeValues[p].valid), agreeValues[p].invalid...) {
			if p == "name" {
				check(`{"name":` + val + `}`)
			} else {
				check(`{"name":"abc","` + p + `":` + val + `}`)
			}
		}
	}
	// … and random combinations, mostly of valid values.
	for range 1500 {
		var parts []string
		for _, p := range props {
			if p != "name" && rng.IntN(3) == 0 {
				continue
			}
			vals := agreeValues[p].valid
			if len(agreeValues[p].invalid) > 0 && rng.IntN(10) == 0 {
				vals = agreeValues[p].invalid
			}
			parts = append(parts, fmt.Sprintf("%q:%s", p, vals[rng.IntN(len(vals))]))
		}
		check("{" + strings.Join(parts, ",") + "}")
	}
	t.Logf("%d API-valid documents all accepted by MongoDB; %d API-invalid ones accepted by the looser database validator", apiValid, dbLooser)
	if apiValid < 500 {
		t.Errorf("only %d API-valid documents generated; the test isn't exercising much", apiValid)
	}
}

// sampleMeta returns _meta as backd writes it: without ownership fields
// (realms with auth disabled), or with them, owned or ownerless.
func sampleMeta(rng *rand.Rand, now time.Time) map[string]any {
	meta := map[string]any{"created_at": now, "updated_at": now, "version": int64(1 + rng.IntN(3))}
	switch rng.IntN(3) {
	case 1:
		meta["owner"], meta["created_by"], meta["updated_by"] = "u1", "user:u1", "user:u1"
	case 2:
		meta["owner"], meta["created_by"], meta["updated_by"] = nil, "key:svc", "key:svc as user:u2"
	}
	return meta
}

// TestSampleSchemasValidatorsAgree runs the same check on the repository's
// sample schemas, with documents generated from each schema.
func TestSampleSchemasValidatorsAgree(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	all := samples(t)
	files := map[string]string{}
	for i, s := range all {
		files["app/c"+strconv.Itoa(i)] = s.schema
	}
	reg := loadRegistry(t, realm, files)
	log, _ := testLogger()
	if err := (&Provisioner{Client: client, Registry: reg, Log: log}).Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewPCG(11, 17))
	gen := docGen{rng: rng, wrongRate: 12}
	for i, s := range all {
		c, _ := reg.Collection(realm, "app", "c"+strconv.Itoa(i))
		repo := &Repository{coll: client.Database(c.MongoDatabase).Collection(c.Name)}
		schema := parseSchema(t, s.schema)
		var valid, invalid int
		// Until 300 are valid, so every schema gets a similar number of
		// valid documents however many constraints it has.
		for tries := 0; valid < 300 && tries < 5000; tries++ {
			body := gen.object(schema, 0)
			v := decodeJSON(t, body)
			if c.Schema.Validate(v) != nil {
				invalid++
				continue
			}
			valid++
			doc := jsonnum.Normalize(v).(map[string]any)
			doc["id"] = fmt.Sprintf("x%d", rng.Int64())
			doc["_meta"] = sampleMeta(rng, time.Now().UTC())
			if err := repo.Create(context.Background(), doc); err != nil {
				t.Errorf("%s: API accepts but MongoDB rejects %s: %v", s.path, body, err)
			}
		}
		t.Logf("%s: %d API-valid documents stored, %d API-invalid generated", s.path, valid, invalid)
		if valid < 100 || invalid < 20 {
			t.Errorf("%s: %d valid and %d invalid documents generated; the generator doesn't fit this schema", s.path, valid, invalid)
		}
	}
}
