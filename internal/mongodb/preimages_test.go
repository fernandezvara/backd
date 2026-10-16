package mongodb

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/fernandezvara/backd/internal/registry"
)

func imagesOf(t *testing.T, p *Provisioner, db, coll string) bool {
	t.Helper()
	opts, err := p.collectionOptions(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return preImagesEnabled(opts[coll])
}

func TestProvisionEnablesPreImagesOnlyForRealtimeCollections(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	log, _ := testLogger()
	reg := loadRegistry(t, realm, map[string]string{
		"app/items":                 itemSchema,
		"app/items/collection.yaml": "realtime: true\n",
		"app/plain":                 itemSchema,
	})
	p := &Provisioner{Client: client, Registry: reg, Log: log}

	// verify names the collection and says what to do
	err := p.Verify(ctx)
	if err == nil || !strings.Contains(err.Error(), realm+"__app.items: collection missing") {
		t.Fatalf("Verify before apply = %v", err)
	}
	if err := p.Apply(ctx); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !imagesOf(t, p, realm+"__app", "items") || imagesOf(t, p, realm+"__app", "plain") {
		t.Fatal("pre-images must be enabled on the realtime collection only")
	}
	if err := p.Verify(ctx); err != nil {
		t.Fatalf("Verify after apply: %v", err)
	}
	// idempotent
	if err := p.Apply(ctx); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
}

func TestVerifyFailsWhenAnExistingCollectionLacksPreImages(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	log, _ := testLogger()
	// The collection exists, provisioned before it became realtime.
	plain := loadRegistry(t, realm, map[string]string{"app/items": itemSchema})
	p := &Provisioner{Client: client, Registry: plain, Log: log}
	if err := p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	p.Registry = loadRegistry(t, realm, map[string]string{"app/items": itemSchema, "app/items/collection.yaml": "realtime: true\n"})
	err := p.Verify(ctx)
	if err == nil || !strings.Contains(err.Error(), realm+"__app.items: pre-change images are not enabled") || !strings.Contains(err.Error(), "backd provision") {
		t.Fatalf("Verify = %v, want the missing images named with the command", err)
	}
	if err := p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.Verify(ctx); err != nil {
		t.Fatalf("Verify after apply: %v", err)
	}
}

// Removing realtime: leaves the images in place and warns; only DisablePreImages removes
// them, and only when asked to apply.
func TestPreImagesAreNeverDisabledByProvisioning(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	log, logs := testLogger()
	p := &Provisioner{Client: client, Log: log, Registry: loadRegistry(t, realm, map[string]string{
		"app/items": itemSchema, "app/items/collection.yaml": "realtime: true\n",
	})}
	if err := p.Apply(ctx); err != nil {
		t.Fatal(err)
	}

	p.Registry = loadRegistry(t, realm, map[string]string{"app/items": itemSchema})
	logs.Reset()
	if err := p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if !imagesOf(t, p, realm+"__app", "items") {
		t.Fatal("a provision removed the images when realtime: was dropped")
	}
	if !strings.Contains(logs.String(), "pre-change images are enabled on a collection that no longer needs them") {
		t.Errorf("no warning about the unused images: %s", logs)
	}
	if err := p.Verify(ctx); err != nil {
		t.Fatalf("unused images must only warn in verify, got %v", err)
	}

	// A dry run lists and changes nothing.
	got, err := p.DisablePreImages(ctx, false)
	if err != nil || len(got) != 1 || got[0] != realm+"__app.items" {
		t.Fatalf("dry run = %v, %v", got, err)
	}
	if !imagesOf(t, p, realm+"__app", "items") {
		t.Fatal("a dry run disabled the images")
	}
	got, err = p.DisablePreImages(ctx, true)
	if err != nil || len(got) != 1 {
		t.Fatalf("apply = %v, %v", got, err)
	}
	if imagesOf(t, p, realm+"__app", "items") {
		t.Fatal("the images are still enabled")
	}
	if got, _ := p.DisablePreImages(ctx, true); len(got) != 0 {
		t.Errorf("second run = %v, want nothing", got)
	}
}

func TestDisableLeavesNeededImagesAlone(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	log, _ := testLogger()
	p := &Provisioner{Client: client, Log: log, Registry: loadRegistry(t, realm, map[string]string{
		"app/items": itemSchema, "app/items/collection.yaml": "realtime: true\n",
	})}
	if err := p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := p.DisablePreImages(ctx, true)
	if err != nil || len(got) != 0 {
		t.Fatalf("DisablePreImages = %v, %v: a needed collection must keep its images", got, err)
	}
	if !imagesOf(t, p, realm+"__app", "items") {
		t.Fatal("needed images were removed")
	}
}

func TestRequirePreImageVersion(t *testing.T) {
	cols := []registry.EventCollection{
		{MongoDatabase: "shop__main", Name: "items"},
		{MongoDatabase: "shop__main", Name: "orders"},
	}
	for _, v := range [][]int32{{6, 0, 0, 0}, {7, 0, 2, 0}, {8, 2, 0, 0}} {
		if err := requirePreImageVersion(v, "x", cols); err != nil {
			t.Errorf("%v refused: %v", v, err)
		}
	}
	for _, v := range [][]int32{{5, 0, 29, 0}, {4, 4, 0, 0}, nil} {
		err := requirePreImageVersion(v, "5.0.29", cols)
		if err == nil || !strings.Contains(err.Error(), "MongoDB 6.0 or later") || !strings.Contains(err.Error(), "shop__main.items, shop__main.orders") {
			t.Errorf("%v: %v", v, err)
		}
	}
}

func TestCheckEventRequirementsNeedsNothingWithoutRealtime(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	reg := loadRegistry(t, realm, map[string]string{"app/items": itemSchema})
	if err := CheckEventRequirements(context.Background(), client, reg); err != nil {
		t.Fatal(err)
	}
	reg = loadRegistry(t, realm, map[string]string{"app/items": itemSchema, "app/items/collection.yaml": "realtime: true\n"})
	if err := CheckEventRequirements(context.Background(), client, reg); err != nil {
		t.Fatalf("this server must have pre-images: %v", err)
	}
}

func TestCheckStreamPrivilege(t *testing.T) {
	client := testClient(t)
	if err := CheckStreamPrivilege(context.Background(), client); err != nil {
		t.Fatalf("an unrestricted user must pass: %v", err)
	}
}

func TestStreamPrivilegeErrorNamesTheRole(t *testing.T) {
	err := streamPrivilegeError(mongo.CommandError{Code: 13, Name: "Unauthorized", Message: "not authorized"})
	if !strings.Contains(err.Error(), "readAnyDatabase") || !strings.Contains(err.Error(), "changeStream") {
		t.Errorf("error = %v", err)
	}
	other := errors.New("boom")
	if got := streamPrivilegeError(other); !errors.Is(got, other) || strings.Contains(got.Error(), "readAnyDatabase") {
		t.Errorf("other error = %v", got)
	}
}
