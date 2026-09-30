package mongodb

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/registry"
)

const (
	validationLevel  = "moderate"
	validationAction = "error"
)

// Provisioner makes MongoDB match the registry (Apply) or checks that it
// does (Verify). It never drops anything: objects in MongoDB that are not
// in the config are only reported as warnings.
type Provisioner struct {
	Client   *mongo.Client
	Registry *registry.Registry
	Log      *slog.Logger
	// Fingerprint of the config (registry.Fingerprint): Apply records it per
	// realm, Verify refuses a different one. Empty skips both.
	Fingerprint string
	Version     string // backd's version, recorded with the fingerprint
}

// Apply creates missing collections with their validators, updates
// validators that differ and creates missing declared indexes. It is idempotent.
func (p *Provisioner) Apply(ctx context.Context) error {
	for _, db := range p.Registry.Databases() {
		existing, err := p.collectionOptions(ctx, db.MongoName)
		if err != nil {
			return err
		}
		mdb := p.Client.Database(db.MongoName)
		for _, c := range db.SortedCollections() {
			if err := p.ensureCollection(ctx, mdb, existing, c.Name, p.validator(c)); err != nil {
				return err
			}
			if err := p.applyIndexes(ctx, c, mdb.Collection(c.Name)); err != nil {
				return err
			}
		}
		p.warnUnknownCollections(db, existing)
	}
	if err := p.applySystem(ctx); err != nil {
		return err
	}
	p.warnUnknownDatabases(ctx)
	return p.recordFingerprint(ctx)
}

// ensureCollection creates the collection with its validator, or updates
// the validator when it differs. existing holds the database's current
// collection options (see collectionOptions).
func (p *Provisioner) ensureCollection(ctx context.Context, mdb *mongo.Database, existing map[string]bson.Raw, name string, validator map[string]any) error {
	opts, ok := existing[name]
	switch {
	case !ok:
		err := mdb.CreateCollection(ctx, name, options.CreateCollection().
			SetValidator(validator).
			SetValidationLevel(validationLevel).
			SetValidationAction(validationAction))
		if err != nil {
			return fmt.Errorf("create collection %s.%s: %w", mdb.Name(), name, err)
		}
		p.Log.Info("collection created", "database", mdb.Name(), "collection", name)
	case validatorDiff(opts, validator) != "":
		cmd := bson.D{
			{Key: "collMod", Value: name},
			{Key: "validator", Value: validator},
			{Key: "validationLevel", Value: validationLevel},
			{Key: "validationAction", Value: validationAction},
		}
		if err := mdb.RunCommand(ctx, cmd).Err(); err != nil {
			return fmt.Errorf("update validator %s.%s: %w", mdb.Name(), name, err)
		}
		p.Log.Info("collection validator updated", "database", mdb.Name(), "collection", name)
	}
	return nil
}

// Verify compares collections, validators and indexes with the registry
// without changing anything and returns an error listing every difference.
func (p *Provisioner) Verify(ctx context.Context) error {
	var diffs []string
	for _, db := range p.Registry.Databases() {
		existing, err := p.collectionOptions(ctx, db.MongoName)
		if err != nil {
			return err
		}
		for _, c := range db.SortedCollections() {
			ns := db.MongoName + "." + c.Name
			opts, ok := existing[c.Name]
			if !ok {
				diffs = append(diffs, ns+": collection missing")
				continue
			}
			if d := validatorDiff(opts, p.validator(c)); d != "" {
				diffs = append(diffs, ns+": "+d)
			}
			ixDiffs, err := p.verifyIndexes(ctx, c, p.Client.Database(db.MongoName).Collection(c.Name))
			if err != nil {
				return err
			}
			for _, d := range ixDiffs {
				diffs = append(diffs, ns+": "+d)
			}
		}
		p.warnUnknownCollections(db, existing)
	}
	sysDiffs, err := p.verifySystem(ctx)
	if err != nil {
		return err
	}
	diffs = append(diffs, sysDiffs...)
	p.warnUnknownDatabases(ctx)
	if len(diffs) > 0 {
		return fmt.Errorf("MongoDB does not match the config (run `backd provision`):\n  %s", strings.Join(diffs, "\n  "))
	}
	return p.verifyFingerprint(ctx)
}

// validator translates the collection schema, logging omitted keywords.
func (p *Provisioner) validator(c *registry.Collection) map[string]any {
	v, omissions := Validator(c.RawSchema)
	for _, o := range omissions {
		p.Log.Info("schema keyword not enforced by the database validator",
			"file", c.SchemaPath, "path", o.Path, "keyword", o.Keyword)
	}
	return v
}

// collectionOptions returns the options document of every collection in
// the database, keyed by collection name. System collections and views are skipped.
func (p *Provisioner) collectionOptions(ctx context.Context, dbName string) (map[string]bson.Raw, error) {
	specs, err := p.Client.Database(dbName).ListCollectionSpecifications(ctx, bson.D{})
	if err != nil {
		return nil, fmt.Errorf("list collections of %s: %w", dbName, err)
	}
	out := map[string]bson.Raw{}
	for _, s := range specs {
		if s.Type == "collection" && !strings.HasPrefix(s.Name, "system.") {
			out[s.Name] = s.Options
		}
	}
	return out, nil
}

func (p *Provisioner) warnUnknownCollections(db *registry.Database, existing map[string]bson.Raw) {
	var unknown []string
	for name := range existing {
		if _, ok := db.Collections[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	for _, name := range unknown {
		p.Log.Warn("collection exists in MongoDB but not in config; it is not served", "database", db.MongoName, "collection", name)
	}
}

// warnUnknownDatabases reports databases that look like backd databases
// (<realm>__<database> or <realm>___system) but are not configured.
func (p *Provisioner) warnUnknownDatabases(ctx context.Context) {
	names, err := p.Client.ListDatabaseNames(ctx, bson.D{}, options.ListDatabases().SetAuthorizedDatabases(true))
	if err != nil {
		p.Log.Warn("could not list databases to check for unconfigured ones", "error", err)
		return
	}
	configured := map[string]bool{}
	for _, db := range p.Registry.Databases() {
		configured[db.MongoName] = true
	}
	for _, realm := range p.authRealms() {
		configured[SystemDatabaseName(realm)] = true
	}
	configured[DeploymentDatabase] = true
	for _, name := range names {
		switch {
		case configured[name]:
		case strings.HasSuffix(name, registry.SystemDatabaseSuffix):
			p.Log.Warn("system database of a realm that is not configured or has auth disabled; it is not used", "database", name)
		case strings.Contains(name, "__"):
			p.Log.Warn("database exists in MongoDB but not in config; it is not served", "database", name)
		}
	}
}

// validatorDiff describes how the collection options differ from the
// wanted validator settings, or returns "" when they match.
func validatorDiff(opts bson.Raw, want map[string]any) string {
	var cur struct {
		Validator        bson.Raw `bson:"validator"`
		ValidationLevel  string   `bson:"validationLevel"`
		ValidationAction string   `bson:"validationAction"`
	}
	if len(opts) > 0 {
		if err := bson.Unmarshal(opts, &cur); err != nil {
			return "cannot read collection options: " + err.Error()
		}
	}
	var diffs []string
	if cur.ValidationLevel != validationLevel {
		diffs = append(diffs, fmt.Sprintf("validationLevel is %q, want %q", cur.ValidationLevel, validationLevel))
	}
	if cur.ValidationAction != validationAction {
		diffs = append(diffs, fmt.Sprintf("validationAction is %q, want %q", cur.ValidationAction, validationAction))
	}
	equal, err := sameDocument(cur.Validator, want)
	if err != nil {
		diffs = append(diffs, "cannot compare validator: "+err.Error())
	} else if !equal {
		diffs = append(diffs, "validator differs from schema.json")
	}
	return strings.Join(diffs, "; ")
}

// sameDocument compares a stored BSON document with a Go map, ignoring key order.
func sameDocument(stored bson.Raw, want map[string]any) (bool, error) {
	if len(stored) == 0 {
		return false, nil
	}
	wantRaw, err := bson.Marshal(want)
	if err != nil {
		return false, err
	}
	a, err := canonical(stored)
	if err != nil {
		return false, err
	}
	b, err := canonical(wantRaw)
	if err != nil {
		return false, err
	}
	return reflect.DeepEqual(a, b), nil
}

// canonical decodes BSON into plain maps and slices so that documents can
// be compared regardless of key order.
func canonical(raw bson.Raw) (any, error) {
	var d bson.D
	if err := bson.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	return toPlain(d), nil
}

func toPlain(v any) any {
	switch t := v.(type) {
	case bson.D:
		m := make(map[string]any, len(t))
		for _, e := range t {
			m[e.Key] = toPlain(e.Value)
		}
		return m
	case bson.A:
		s := make([]any, len(t))
		for i, e := range t {
			s[i] = toPlain(e)
		}
		return s
	}
	return v
}
