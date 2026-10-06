package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

const codeStorageUnavailable = "storage_unavailable"

// Why a realm's storage can't be used.
var (
	// errNoStorage: the realm has no storage: in realm.yaml.
	errNoStorage = errors.New("this realm has no storage configured")
	// errStorageUnavailable: it has, but its access keys aren't usable (a secret
	// is not set, or the instance has no secrets key). File endpoints answer
	// 503 storage_unavailable.
	errStorageUnavailable = errors.New("the realm's storage is not available")
)

// realmObjects hands out each realm's connection to its object storage. The
// access keys are read from the realm's secrets whenever one is asked for (they
// are cached for a minute there, so a rotated key applies without a restart); the
// connection itself is reused until the settings or the keys change.
type realmObjects struct {
	reg     *registry.Registry
	users   func(realm string) *auth.Users
	connect func(storage.ObjectsConfig) (*storage.Objects, error)

	mu    sync.Mutex
	cache map[string]*cachedObjects
}

type cachedObjects struct {
	cfg storage.ObjectsConfig
	obj *storage.Objects
}

func newRealmObjects(reg *registry.Registry, users func(string) *auth.Users) *realmObjects {
	return &realmObjects{reg: reg, users: users, connect: storage.NewObjects, cache: map[string]*cachedObjects{}}
}

// settings returns the realm's storage settings, or errNoStorage.
func (s *realmObjects) settings(realm string) (*registry.StorageSettings, error) {
	rl := s.reg.Realms[realm]
	if rl == nil || rl.Settings.Storage == nil {
		return nil, errNoStorage
	}
	return rl.Settings.Storage, nil
}

// missingSecrets lists the secrets of the realm's storage that are not set.
func (s *realmObjects) missingSecrets(ctx context.Context, realm string) ([]string, error) {
	st, err := s.settings(realm)
	if err != nil {
		return nil, err
	}
	svc := s.users(realm)
	if svc == nil {
		return nil, errNoStorage
	}
	refs := make([]registry.SecretRef, 0, 2)
	for _, n := range st.SecretNames() {
		refs = append(refs, registry.SecretRef{Realm: true, Name: n})
	}
	_, missing, err := svc.ResolveSecrets(ctx, refs, "")
	return missing, err
}

// For returns the realm's connection to its storage: errNoStorage, an error
// wrapping errStorageUnavailable (naming what is missing) or the storage error.
func (s *realmObjects) For(ctx context.Context, realm string) (*storage.Objects, error) {
	st, err := s.settings(realm)
	if err != nil {
		return nil, err
	}
	svc := s.users(realm)
	if svc == nil {
		return nil, errNoStorage
	}
	refs := []registry.SecretRef{{Realm: true, Name: st.AccessKey}, {Realm: true, Name: st.SecretKey}}
	values, missing, err := svc.ResolveSecrets(ctx, refs, "")
	if err != nil {
		return nil, fmt.Errorf("%w: reading the access keys: %v", errStorageUnavailable, err)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: the secret(s) %s of the realm are not set (backd secret set)", errStorageUnavailable, strings.Join(missing, ", "))
	}
	cfg := storage.ObjectsConfig{
		Provider: st.Provider, Endpoint: st.Endpoint, PublicEndpoint: st.PublicEndpoint, Region: st.Region, Bucket: st.Bucket, Prefix: st.Prefix,
		AccessKey: values["realm."+st.AccessKey], SecretKey: values["realm."+st.SecretKey],
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.cache[realm]; c != nil && c.cfg == cfg {
		return c.obj, nil
	}
	obj, err := s.connect(cfg)
	if err != nil {
		return nil, err
	}
	s.cache[realm] = &cachedObjects{cfg: cfg, obj: obj}
	return obj, nil
}
