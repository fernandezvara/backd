package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/fernandezvara/backd/internal/fieldcrypt"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/registry"
)

// fieldRing returns the realm's field encryption: the data keys in its system database,
// wrapped by the `key` secrets it holds. nil for a realm without auth (it has nowhere to keep
// keys; a collection that encrypts a field is refused at startup).
func (a *app) fieldRing(realm string) *fieldcrypt.Ring {
	if a.rings == nil {
		a.rings = &ringState{} // first used while the app is being set up, before any request
	}
	a.rings.mu.Lock()
	defer a.rings.mu.Unlock()
	if r, ok := a.rings.byRealm[realm]; ok {
		return r
	}
	var ring *fieldcrypt.Ring
	if svc := a.realmUsers()(realm); svc != nil {
		if ds, ok := svc.Store.(fieldcrypt.Store); ok {
			ring = &fieldcrypt.Ring{Realm: realm, Store: ds, Keys: svc.KeyMaterial}
		}
	}
	if a.rings.byRealm == nil {
		a.rings.byRealm = map[string]*fieldcrypt.Ring{}
	}
	a.rings.byRealm[realm] = ring
	return ring
}

// ringState holds the realms' field encryption, built on first use.
type ringState struct {
	mu      sync.Mutex
	byRealm map[string]*fieldcrypt.Ring
}

// documentStore hands out the repositories of documents, encrypting and decrypting the fields
// their collections mark.
func (a *app) documentStore() *mongodb.Store {
	return &mongodb.Store{Client: a.client, Crypt: a.fieldRing}
}

// ensureDataKeys gives every encrypted field of every realm a data key to write with, so a
// write never has to create one. Instances starting together race harmlessly: one key wins.
func (a *app) ensureDataKeys(ctx context.Context) error {
	for _, rl := range a.reg.SortedRealms() {
		if len(rl.EncryptionKeyNames()) == 0 {
			continue
		}
		ring := a.fieldRing(rl.Name)
		if ring == nil {
			continue // refused by checkEncryptionKeys before
		}
		for _, db := range rl.Databases {
			for _, c := range db.Collections {
				if err := ring.Ensure(ctx, c); err != nil {
					return fmt.Errorf("realm %s: %w", rl.Name, err)
				}
			}
		}
	}
	return nil
}

// decryptDocument opens the encrypted fields of a document of a MongoDB collection, in place:
// what the change feed and the trigger rescan do, so their consumers work on plaintext.
func (a *app) decryptDocument(ctx context.Context, realm, mongoDB, coll string, doc map[string]any) error {
	rl, ok := a.reg.Realms[realm]
	if !ok {
		return nil
	}
	var c *registry.Collection
	for _, db := range rl.Databases {
		if x, found := db.Collections[coll]; found && x.MongoDatabase == mongoDB {
			c = x
		}
	}
	if c == nil || len(c.Encrypted) == 0 {
		return nil
	}
	ring := a.fieldRing(realm)
	if ring == nil {
		return fieldcrypt.ErrUnavailable
	}
	return ring.Decrypt(ctx, c, doc)
}
