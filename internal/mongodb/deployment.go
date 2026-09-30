package mongodb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// DeploymentDatabase holds what provisioning applied: per realm, the
// fingerprint of the config it provisioned. Its name can't clash with a
// realm's databases (<realm>__<database>, where a database name can't
// start with "_").
const (
	DeploymentDatabase   = "backd___deployment"
	DeploymentCollection = "realms"
)

// deploymentDoc records the config last provisioned for one realm.
type deploymentDoc struct {
	Realm       string    `bson:"_id"`
	Fingerprint string    `bson:"fingerprint"`
	AppliedAt   time.Time `bson:"applied_at"`
	Version     string    `bson:"backd_version"`
}

func (p *Provisioner) deployments() *mongo.Collection {
	return p.Client.Database(DeploymentDatabase).Collection(DeploymentCollection)
}

// recordFingerprint stores the config fingerprint for every configured
// realm. Realms of other deployments sharing the cluster are untouched.
func (p *Provisioner) recordFingerprint(ctx context.Context) error {
	if p.Fingerprint == "" {
		return nil
	}
	now := time.Now().UTC()
	for _, realm := range p.Registry.RealmNames() {
		doc := deploymentDoc{Realm: realm, Fingerprint: p.Fingerprint, AppliedAt: now, Version: p.Version}
		_, err := p.deployments().ReplaceOne(ctx, bson.D{{Key: "_id", Value: realm}}, doc, options.Replace().SetUpsert(true))
		if err != nil {
			return fmt.Errorf("record config fingerprint: %w", err)
		}
	}
	p.Log.Info("config fingerprint recorded", "fingerprint", p.Fingerprint, "realms", len(p.Registry.Realms))
	return nil
}

// ErrConfigMismatch means this instance's config isn't the one last
// provisioned.
var ErrConfigMismatch = errors.New("the config differs from the one last provisioned")

// verifyFingerprint checks that the config was provisioned as is, realm by
// realm, so every instance of a deployment runs the same config.
func (p *Provisioner) verifyFingerprint(ctx context.Context) error {
	if p.Fingerprint == "" {
		return nil
	}
	var problems []string
	for _, realm := range p.Registry.RealmNames() {
		var doc deploymentDoc
		err := p.deployments().FindOne(ctx, bson.D{{Key: "_id", Value: realm}}).Decode(&doc)
		switch {
		case errors.Is(err, mongo.ErrNoDocuments):
			problems = append(problems, fmt.Sprintf("realm %s: no config has been provisioned yet", realm))
		case err != nil:
			return fmt.Errorf("read the provisioned config fingerprint: %w", err)
		case doc.Fingerprint != p.Fingerprint:
			problems = append(problems, fmt.Sprintf("realm %s: provisioned %s (at %s by backd %s)",
				realm, doc.Fingerprint, doc.AppliedAt.UTC().Format(time.RFC3339), doc.Version))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: this instance's config is %s, but\n  %s\nprovision this config (`backd provision`) or deploy the config that was provisioned",
			ErrConfigMismatch, p.Fingerprint, strings.Join(problems, "\n  "))
	}
	return nil
}
