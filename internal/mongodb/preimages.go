package mongodb

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/fernandezvara/backd/internal/registry"
)

// Pre-change images (design/triggers-and-realtime.md §5).
//
// `changeStreamPreAndPostImages` makes the change stream carry the document before a change,
// which `removed` and `before` need to be correct. It costs storage and writes, and switching
// it off deletes the stored images for good, so it follows the provisioning model: `backd
// provision` enables it, `verify` fails on a missing one, and nothing here ever disables it
// except the explicit DisablePreImages.

// preImageOption is the collection option MongoDB reports and sets.
const preImageOption = "changeStreamPreAndPostImages"

// minPreImageVersion is the first MongoDB with pre-change images.
var minPreImageVersion = [2]int{6, 0}

// preImagesEnabled reads a collection's options (listCollections) for the setting.
func preImagesEnabled(opts bson.Raw) bool {
	if len(opts) == 0 {
		return false
	}
	var cur struct {
		Images struct {
			Enabled bool `bson:"enabled"`
		} `bson:"changeStreamPreAndPostImages"`
	}
	if err := bson.Unmarshal(opts, &cur); err != nil {
		return false
	}
	return cur.Images.Enabled
}

func setPreImages(ctx context.Context, mdb *mongo.Database, name string, enabled bool) error {
	return mdb.RunCommand(ctx, bson.D{
		{Key: "collMod", Value: name},
		{Key: preImageOption, Value: bson.D{{Key: "enabled", Value: enabled}}},
	}).Err()
}

// ensurePreImages enables the images of a collection that needs them (a realtime one); opts
// are its options before this run, nil when it was just created.
func (p *Provisioner) ensurePreImages(ctx context.Context, mdb *mongo.Database, c *registry.Collection, opts bson.Raw) error {
	if !c.NeedsEvents() || preImagesEnabled(opts) {
		return nil
	}
	if err := setPreImages(ctx, mdb, c.Name, true); err != nil {
		return fmt.Errorf("enable pre-change images of %s.%s: %w", mdb.Name(), c.Name, err)
	}
	p.Log.Info("pre-change images enabled", "database", mdb.Name(), "collection", c.Name)
	return nil
}

// preImageDiff describes what is missing for a collection in verify mode.
func preImageDiff(c *registry.Collection, opts bson.Raw) string {
	if c.NeedsEvents() && !preImagesEnabled(opts) {
		return "pre-change images are not enabled (this collection is realtime)"
	}
	return ""
}

// warnUnusedPreImages names the collections that keep images nothing needs: they cost storage
// and writes. It never fails startup and never disables them.
func (p *Provisioner) warnUnusedPreImages(db *registry.Database, existing map[string]bson.Raw) {
	for _, name := range sortedNames(existing) {
		c, ok := db.Collections[name]
		if !ok || c.NeedsEvents() || !preImagesEnabled(existing[name]) {
			continue
		}
		p.Log.Warn("pre-change images are enabled on a collection that no longer needs them: they cost storage and writes; `backd provision --disable-preimages` removes them for good",
			"database", db.MongoName, "collection", name)
	}
}

func sortedNames(m map[string]bson.Raw) []string {
	out := make([]string, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// DisablePreImages switches the images off on every configured collection that has them and
// doesn't need them, and returns the namespaces. With apply false it changes nothing and
// only returns them. It is irreversible: the stored images are deleted.
func (p *Provisioner) DisablePreImages(ctx context.Context, apply bool) ([]string, error) {
	var out []string
	for _, db := range p.Registry.Databases() {
		existing, err := p.collectionOptions(ctx, db.MongoName)
		if err != nil {
			return nil, err
		}
		for _, name := range sortedNames(existing) {
			c, ok := db.Collections[name]
			if !ok || c.NeedsEvents() || !preImagesEnabled(existing[name]) {
				continue
			}
			ns := db.MongoName + "." + name
			if apply {
				if err := setPreImages(ctx, p.Client.Database(db.MongoName), name, false); err != nil {
					return out, fmt.Errorf("disable pre-change images of %s: %w", ns, err)
				}
				p.Log.Info("pre-change images disabled", "database", db.MongoName, "collection", name)
			}
			out = append(out, ns)
		}
	}
	return out, nil
}

// CheckEventRequirements refuses a configuration with realtime collections on a server that
// can't give pre-change images (before MongoDB 6.0), naming the requirement and the
// collections. There is no degraded mode: without images `removed` could leak ids.
func CheckEventRequirements(ctx context.Context, client *mongo.Client, reg *registry.Registry) error {
	cols := reg.EventCollections()
	if len(cols) == 0 {
		return nil
	}
	var info struct {
		VersionArray []int32 `bson:"versionArray"`
		Version      string  `bson:"version"`
	}
	if err := client.Database("admin").RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&info); err != nil {
		return fmt.Errorf("mongodb: buildInfo: %w", err)
	}
	return requirePreImageVersion(info.VersionArray, info.Version, cols)
}

func requirePreImageVersion(version []int32, text string, cols []registry.EventCollection) error {
	if len(version) >= 2 && (int(version[0]) > minPreImageVersion[0] || (int(version[0]) == minPreImageVersion[0] && int(version[1]) >= minPreImageVersion[1])) {
		return nil
	}
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.MongoDatabase + "." + c.Name
	}
	return fmt.Errorf("realtime needs MongoDB %d.%d or later for pre-change images, and this server is %s; collections that need them: %s",
		minPreImageVersion[0], minPreImageVersion[1], text, strings.Join(names, ", "))
}

// CheckStreamPrivilege opens, and at once closes, a deployment-wide change stream, to tell at
// startup — not at the first subscriber — that backd's MongoDB user may read one. MongoDB
// checks the scope of the stream, not its filter: the user needs the changeStream and find
// actions on every database, for example the built-in readAnyDatabase role.
func CheckStreamPrivilege(ctx context.Context, client *mongo.Client) error {
	cs, err := client.Watch(ctx, mongo.Pipeline{{{Key: "$match", Value: bson.D{{Key: "ns.db", Value: DeploymentDatabase}}}}})
	if err != nil {
		return streamPrivilegeError(err)
	}
	_ = cs.Close(ctx)
	return nil
}

func streamPrivilegeError(err error) error {
	var ce mongo.CommandError
	if errors.As(err, &ce) && ce.Code == 13 { // Unauthorized
		return fmt.Errorf("the MongoDB user in MONGO_URI may not open a change stream on the deployment, which realtime needs: "+
			"grant it the changeStream and find actions on every database (for example the built-in readAnyDatabase role); "+
			"MongoDB checks the scope of the stream, not its filter: %w", err)
	}
	return fmt.Errorf("open a change stream on the deployment: %w", err)
}
