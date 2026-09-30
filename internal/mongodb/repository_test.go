package mongodb

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/rs/xid"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/storage"
)

func testRepository(t *testing.T) storage.Repository {
	t.Helper()
	client := testClient(t)
	realm := testRealm(t, client)
	log, _ := testLogger()
	reg := loadRegistry(t, realm, map[string]string{"app/items": `{}`})
	if err := (&Provisioner{Client: client, Registry: reg, Log: log}).Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	c, _ := reg.Collection(realm, "app", "items")
	return (&Store{Client: client}).Repository(c)
}

func newDoc(fields map[string]any) storage.Document {
	now := time.Now().UTC().Truncate(time.Millisecond)
	doc := storage.Document{"id": xid.New().String(), "_meta": map[string]any{"created_at": now, "updated_at": now, "version": int64(1)}}
	for k, v := range fields {
		doc[k] = v
	}
	return doc
}

func TestRepositoryCRUD(t *testing.T) {
	repo := testRepository(t)
	ctx := context.Background()

	doc := newDoc(map[string]any{"name": "Widget", "price": 12.5, "qty": int64(3), "tags": []any{"a"}, "dims": map[string]any{"w": int64(1)}})
	if err := repo.Create(ctx, doc); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.Get(ctx, doc["id"].(string))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got, doc) {
		t.Errorf("Get = %#v\nwant %#v", got, doc)
	}

	replaced := newDoc(map[string]any{"name": "Gadget"})
	replaced["id"] = doc["id"]
	replaced["_meta"].(map[string]any)["version"] = int64(2)
	if err := repo.Replace(ctx, replaced, 5); !errors.Is(err, storage.ErrVersionMismatch) {
		t.Errorf("Replace with stale version = %v, want ErrVersionMismatch", err)
	}
	if err := repo.Replace(ctx, replaced, 1); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	got, _ = repo.Get(ctx, doc["id"].(string))
	if !reflect.DeepEqual(got, replaced) {
		t.Errorf("after Replace = %#v\nwant %#v", got, replaced)
	}

	stale := int64(1)
	if err := repo.Delete(ctx, doc["id"].(string), &stale); !errors.Is(err, storage.ErrVersionMismatch) {
		t.Errorf("Delete with stale version = %v, want ErrVersionMismatch", err)
	}
	current := int64(2)
	if err := repo.Delete(ctx, doc["id"].(string), &current); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.Get(ctx, doc["id"].(string)); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Get after delete = %v, want ErrNotFound", err)
	}
}

func TestRepositoryNotFound(t *testing.T) {
	repo := testRepository(t)
	ctx := context.Background()
	missing := newDoc(nil)
	if err := repo.Replace(ctx, missing, 1); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Replace missing = %v, want ErrNotFound", err)
	}
	if err := repo.Delete(ctx, missing["id"].(string), nil); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Delete missing = %v, want ErrNotFound", err)
	}
	one := int64(1)
	if err := repo.Delete(ctx, missing["id"].(string), &one); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("conditional Delete missing = %v, want ErrNotFound", err)
	}
}

func TestRepositoryListPaging(t *testing.T) {
	repo := testRepository(t)
	ctx := context.Background()
	var ids []string
	for i := range 5 {
		d := newDoc(map[string]any{"n": int64(i)})
		if err := repo.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, d["id"].(string))
	}

	tests := []struct {
		limit, skip int
		wantIDs     []string
		wantMore    bool
	}{
		{2, 0, ids[0:2], true},
		{2, 2, ids[2:4], true},
		{2, 4, ids[4:5], false},
		{5, 0, ids, false},
		{2, 10, nil, false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("limit=%d,skip=%d", tt.limit, tt.skip), func(t *testing.T) {
			page, err := repo.List(ctx, storage.Query{Limit: tt.limit, Skip: tt.skip})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			var got []string
			for _, d := range page.Items {
				got = append(got, d["id"].(string))
			}
			if !reflect.DeepEqual(got, tt.wantIDs) || page.HasMore != tt.wantMore {
				t.Errorf("got %v more=%v, want %v more=%v", got, page.HasMore, tt.wantIDs, tt.wantMore)
			}
		})
	}
}

func TestRepositoryUniqueConflict(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	log, _ := testLogger()
	p := &Provisioner{Client: client, Log: log, Registry: loadRegistry(t, realm, map[string]string{
		"app/people":              indexedSchema,
		"app/people/indexes.json": `[{"fields": ["email"], "unique": true}, {"fields": ["status", "address.city"], "unique": true}]`,
	})}
	if err := p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	repo := (&Store{Client: client}).Repository(mustCollection(t, p, realm, "app", "people"))

	first := newDoc(map[string]any{"email": "a@x", "status": "s", "address": map[string]any{"city": "M"}})
	if err := repo.Create(ctx, first); err != nil {
		t.Fatal(err)
	}
	var ce *storage.ConflictError
	err := repo.Create(ctx, newDoc(map[string]any{"email": "a@x"}))
	if !errors.Is(err, storage.ErrConflict) || !errors.As(err, &ce) || !reflect.DeepEqual(ce.Fields, []string{"email"}) {
		t.Errorf("duplicate create = %v", err)
	}

	second := newDoc(map[string]any{"email": "b@x"})
	if err := repo.Create(ctx, second); err != nil {
		t.Fatal(err)
	}
	second["status"], second["address"] = "s", map[string]any{"city": "M"}
	err = repo.Replace(ctx, second, 1)
	if !errors.As(err, &ce) || !reflect.DeepEqual(ce.Fields, []string{"status", "address.city"}) {
		t.Errorf("duplicate replace = %v", err)
	}
}

func TestRepositoryReplaceLegacyDocument(t *testing.T) {
	repo := testRepository(t)
	ctx := context.Background()
	// A document stored before versioning existed has no _meta.version.
	r := repo.(*Repository)
	doc := newDoc(map[string]any{"n": int64(1)})
	delete(doc["_meta"].(map[string]any), "version")
	if _, err := r.coll.InsertOne(ctx, toBSON(doc), options.InsertOne().SetBypassDocumentValidation(true)); err != nil {
		t.Fatal(err)
	}
	doc["_meta"].(map[string]any)["version"] = int64(1)
	if err := repo.Replace(ctx, doc, 1); !errors.Is(err, storage.ErrVersionMismatch) {
		t.Errorf("Replace legacy with version 1 = %v, want mismatch", err)
	}
	if err := repo.Replace(ctx, doc, 0); err != nil {
		t.Errorf("Replace legacy with version 0 = %v", err)
	}
}
