package mongodb

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Provisioning records the config fingerprint per realm; verify refuses a
// config other than the one provisioned (roadmap F2b).
func TestConfigFingerprint(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	realm, other := testRealm(t, client), testRealm(t, client)
	t.Cleanup(func() {
		_, _ = client.Database(DeploymentDatabase).Collection(DeploymentCollection).DeleteMany(ctx, bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: bson.A{realm, other}}}}})
	})
	log, _ := testLogger()
	reg := loadRegistryWith(t, realm, "", map[string]string{"app/notes": `{}`})
	fpA, fpB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	prov := func(fp string) *Provisioner {
		return &Provisioner{Client: client, Registry: reg, Log: log, Fingerprint: fp, Version: "test"}
	}

	// Provisioned without a fingerprint (as by a backd before F2b): verify
	// with one refuses until provisioned again.
	if err := prov("").Apply(ctx); err != nil {
		t.Fatal(err)
	}
	err := prov(fpA).Verify(ctx)
	if !errors.Is(err, ErrConfigMismatch) || !strings.Contains(err.Error(), "realm "+realm+": no config has been provisioned yet") {
		t.Fatalf("never recorded: %v", err)
	}

	if err := prov(fpA).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := prov(fpA).Verify(ctx); err != nil {
		t.Errorf("same config refused: %v", err)
	}
	err = prov(fpB).Verify(ctx)
	if !errors.Is(err, ErrConfigMismatch) || !strings.Contains(err.Error(), "this instance's config is "+fpB) ||
		!strings.Contains(err.Error(), "realm "+realm+": provisioned "+fpA) || !strings.Contains(err.Error(), "by backd test") {
		t.Errorf("different config: %v", err)
	}
	// Without a fingerprint, verify doesn't check (tests, tools).
	if err := prov("").Verify(ctx); err != nil {
		t.Errorf("no fingerprint: %v", err)
	}

	// Another deployment on the same cluster, with its own realm, records
	// its own fingerprint without touching this one's.
	otherReg := loadRegistryWith(t, other, "", map[string]string{"app/items": `{}`})
	if err := (&Provisioner{Client: client, Registry: otherReg, Log: log, Fingerprint: fpB}).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := prov(fpA).Verify(ctx); err != nil {
		t.Errorf("another deployment's provisioning changed this one's record: %v", err)
	}
	var doc deploymentDoc
	if err := client.Database(DeploymentDatabase).Collection(DeploymentCollection).FindOne(ctx, bson.D{{Key: "_id", Value: other}}).Decode(&doc); err != nil || doc.Fingerprint != fpB {
		t.Errorf("other realm's record: %+v %v", doc, err)
	}
}
