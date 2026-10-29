package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ConfigStorage keeps the app configuration between runs, so a program starts with the last
// values it saw. It is optional: without it the configuration is kept in memory.
type ConfigStorage interface {
	// Load returns the value saved under key, or "" when there is none.
	Load(key string) (string, error)
	Save(key, value string) error
	// Clear removes every key that starts with prefix.
	Clear(prefix string) error
}

// FileConfigStorage keeps each key in a file of a directory.
type FileConfigStorage struct{ dir string }

// NewFileConfigStorage returns a storage that keeps its keys in dir.
func NewFileConfigStorage(dir string) *FileConfigStorage { return &FileConfigStorage{dir: dir} }

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func (f *FileConfigStorage) file(key string) string {
	return filepath.Join(f.dir, unsafeName.ReplaceAllString(key, "_"))
}

func (f *FileConfigStorage) Load(key string) (string, error) {
	b, err := os.ReadFile(f.file(key))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

func (f *FileConfigStorage) Save(key, value string) error {
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(f.file(key), []byte(value), 0o600)
}

func (f *FileConfigStorage) Clear(prefix string) error {
	matches, err := filepath.Glob(f.file(prefix) + "*")
	if err != nil {
		return err
	}
	for _, m := range matches {
		if err := os.Remove(m); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

const configPrefix = "backd.config:"

type configEntry struct {
	ETag      string         `json:"etag"`
	Values    map[string]any `json:"values"`
	Version   string         `json:"version"`
	FetchedAt int64          `json:"fetched_at"` // Unix milliseconds
	TTL       int64          `json:"ttl"`        // seconds
}

func (e *configEntry) fresh() bool {
	return time.Now().UnixMilli() < e.FetchedAt+e.TTL*1000
}

// Config is the realm's app configuration: flags, switches and limits the operator changes
// without a release, cached per user. It revalidates stale copies with If-None-Match and tells
// listeners which paths changed. c.Config().
type Config struct {
	c     *Client
	store ConfigStorage

	mu       sync.Mutex
	entries  map[string]*configEntry
	owner    string
	inflight map[string]*configFetch

	lmu      sync.Mutex
	next     int
	onChange map[int]func([]string)
	onError  map[int]func(error)
}

type configFetch struct {
	done  chan struct{}
	entry *configEntry
	err   error
}

func newConfig(c *Client, store ConfigStorage) *Config {
	cfg := &Config{c: c, store: store, entries: map[string]*configEntry{}, inflight: map[string]*configFetch{},
		onChange: map[int]func([]string){}, onError: map[int]func(error){}}
	c.OnCredentialChange(cfg.clear)
	return cfg
}

func isObject(v any) (map[string]any, bool) { m, ok := v.(map[string]any); return m, ok }

// Flatten turns nested values into dotted paths: {"limits": {"max": 5}} becomes {"limits.max": 5}.
func Flatten(values map[string]any) map[string]any {
	out := map[string]any{}
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			if sub, ok := isObject(v); ok {
				walk(p, sub)
			} else {
				out[p] = v
			}
		}
	}
	walk("", values)
	return out
}

func at(values map[string]any, path string) (any, bool) {
	var cur any = values
	for _, k := range strings.Split(path, ".") {
		m, ok := isObject(cur)
		if !ok {
			return nil, false
		}
		if cur, ok = m[k]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// normalizePaths is the canonical key of a set of paths: sorted, without duplicates.
func normalizePaths(paths []string) string {
	seen := map[string]bool{}
	var list []string
	for _, p := range paths {
		for _, q := range strings.Split(p, ",") {
			if q = strings.TrimSpace(q); q != "" && !seen[q] {
				seen[q] = true
				list = append(list, q)
			}
		}
	}
	sort.Strings(list)
	return strings.Join(list, ",")
}

// scope makes the entries those of the current caller, restoring the saved copy of a new one.
func (cfg *Config) scope(ctx context.Context) (string, error) {
	credential := ""
	if cfg.c.apiKey != "" {
		credential = "key:" + cfg.c.apiKey
	} else if token, err := cfg.c.store.Get(ctx); err != nil {
		return "", err
	} else if token != "" {
		credential = "session:" + token
	}
	owner := "anonymous"
	if credential != "" {
		sum := sha256.Sum256([]byte(credential))
		owner = hex.EncodeToString(sum[:])[:16]
	}
	cfg.mu.Lock()
	defer cfg.mu.Unlock()
	if owner != cfg.owner {
		cfg.entries = map[string]*configEntry{}
		cfg.owner = owner
		cfg.restoreLocked()
	}
	return owner, nil
}

func (cfg *Config) storageKey() string { return configPrefix + cfg.c.realm + ":" + cfg.owner }

func (cfg *Config) restoreLocked() {
	if cfg.store == nil {
		return
	}
	raw, err := cfg.store.Load(cfg.storageKey())
	if err != nil || raw == "" {
		return
	}
	var saved map[string]*configEntry
	if json.Unmarshal([]byte(raw), &saved) != nil {
		return
	}
	for k, e := range saved {
		if e != nil && e.ETag != "" && e.Values != nil {
			cfg.entries[k] = e
		}
	}
}

func (cfg *Config) persistLocked() {
	if cfg.store == nil {
		return
	}
	b, _ := json.Marshal(cfg.entries)
	if err := cfg.store.Save(cfg.storageKey(), string(b)); err != nil {
		slog.Debug("backd: the app configuration could not be saved", "err", err)
	}
}

// clear forgets the copies held, in memory and in the storage, when the caller changes.
func (cfg *Config) clear() {
	cfg.mu.Lock()
	cfg.entries = map[string]*configEntry{}
	cfg.inflight = map[string]*configFetch{}
	cfg.owner = ""
	cfg.mu.Unlock()
	if cfg.store != nil {
		_ = cfg.store.Clear(configPrefix + cfg.c.realm + ":")
	}
}

// Load returns the values under paths (all of them with none), from the held copy while it is
// fresh and from the server otherwise.
func (cfg *Config) Load(ctx context.Context, paths []string, opts ...RequestOption) (map[string]any, error) {
	return cfg.load(ctx, paths, false, opts)
}

// Refresh is Load that always asks the server (the copy held is revalidated, so an unchanged
// configuration costs a 304).
func (cfg *Config) Refresh(ctx context.Context, paths []string, opts ...RequestOption) (map[string]any, error) {
	return cfg.load(ctx, paths, true, opts)
}

func (cfg *Config) load(ctx context.Context, paths []string, refresh bool, opts []RequestOption) (map[string]any, error) {
	key := normalizePaths(paths)
	if _, err := cfg.scope(ctx); err != nil {
		return nil, err
	}
	cfg.mu.Lock()
	e := cfg.entries[key]
	cfg.mu.Unlock()
	if e == nil || refresh || !e.fresh() {
		var err error
		if e, err = cfg.fetch(ctx, key, opts); err != nil {
			return nil, err
		}
	}
	return cloneMap(e.Values), nil
}

// Get returns the value at a dotted path ("limits.max"), nil when it isn't set. A held copy answers
// at once; a stale one is revalidated in the background. Unknown paths are fetched.
func (cfg *Config) Get(ctx context.Context, path string, opts ...RequestOption) (any, error) {
	if _, err := cfg.scope(ctx); err != nil {
		return nil, err
	}
	if key, e := cfg.cover(path); e != nil {
		if !e.fresh() {
			go cfg.revalidate(key)
		}
		v, _ := at(e.Values, path)
		return v, nil
	}
	key := strings.SplitN(path, ".", 2)[0]
	if _, err := cfg.fetch(ctx, key, opts); err != nil {
		return nil, err
	}
	if _, e := cfg.cover(path); e != nil {
		v, _ := at(e.Values, path)
		return v, nil
	}
	return nil, nil
}

// GetSync returns the value at a path from what is held, or fallback when nothing held covers it.
// It never waits and never asks the server: call Load first.
func (cfg *Config) GetSync(path string, fallback any) any {
	if _, e := cfg.cover(path); e != nil {
		if v, ok := at(e.Values, path); ok {
			return v
		}
	}
	return fallback
}

// Decode reads the value at a path into out (a pointer), like json.Unmarshal.
func (cfg *Config) Decode(ctx context.Context, path string, out any, opts ...RequestOption) error {
	v, err := cfg.Get(ctx, path, opts...)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(v)
	return json.Unmarshal(b, out)
}

// cover finds the held entry that covers a path.
func (cfg *Config) cover(path string) (string, *configEntry) {
	cfg.mu.Lock()
	defer cfg.mu.Unlock()
	if all := cfg.entries[""]; all != nil {
		return "", all
	}
	for key, e := range cfg.entries {
		for _, p := range strings.Split(key, ",") {
			if p != "" && (path == p || strings.HasPrefix(path, p+".")) {
				return key, e
			}
		}
	}
	return "", nil
}

// OnChange registers f to be called with the dotted paths whose values changed when a held
// copy is replaced by a different one. It returns the function that removes it.
func (cfg *Config) OnChange(f func(paths []string)) (stop func()) {
	cfg.lmu.Lock()
	defer cfg.lmu.Unlock()
	id := cfg.next
	cfg.next++
	cfg.onChange[id] = f
	return func() { cfg.lmu.Lock(); delete(cfg.onChange, id); cfg.lmu.Unlock() }
}

// OnError registers f to be called with the errors of the background updates (revalidations and
// the live stream), which have nobody to return them to.
func (cfg *Config) OnError(f func(error)) (stop func()) {
	cfg.lmu.Lock()
	defer cfg.lmu.Unlock()
	id := cfg.next
	cfg.next++
	cfg.onError[id] = f
	return func() { cfg.lmu.Lock(); delete(cfg.onError, id); cfg.lmu.Unlock() }
}

func (cfg *Config) emitChange(paths []string) {
	cfg.lmu.Lock()
	fs := make([]func([]string), 0, len(cfg.onChange))
	for _, f := range cfg.onChange {
		fs = append(fs, f)
	}
	cfg.lmu.Unlock()
	for _, f := range fs {
		safely("Config.OnChange", func() { f(paths) })
	}
}

func (cfg *Config) emitError(err error) {
	cfg.lmu.Lock()
	fs := make([]func(error), 0, len(cfg.onError))
	for _, f := range cfg.onError {
		fs = append(fs, f)
	}
	cfg.lmu.Unlock()
	for _, f := range fs {
		safely("Config.OnError", func() { f(err) })
	}
}

func safely(what string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("backd: a listener panicked", "listener", what, "panic", r)
		}
	}()
	f()
}

// fetch asks the server for key, joining a fetch already running for it.
func (cfg *Config) fetch(ctx context.Context, key string, opts []RequestOption) (*configEntry, error) {
	cfg.mu.Lock()
	if run := cfg.inflight[key]; run != nil {
		cfg.mu.Unlock()
		select {
		case <-run.done:
			return run.entry, run.err
		case <-ctx.Done():
			return nil, networkError(ctx, ctx.Err())
		}
	}
	run := &configFetch{done: make(chan struct{})}
	cfg.inflight[key] = run
	cfg.mu.Unlock()

	run.entry, run.err = cfg.doFetch(ctx, key, opts)
	cfg.mu.Lock()
	if cfg.inflight[key] == run {
		delete(cfg.inflight, key)
	}
	cfg.mu.Unlock()
	close(run.done)
	return run.entry, run.err
}

func (cfg *Config) doFetch(ctx context.Context, key string, opts []RequestOption) (*configEntry, error) {
	cfg.mu.Lock()
	owner := cfg.owner
	held := cfg.entries[key]
	cfg.mu.Unlock()

	req := []RequestOption{WithoutCache()}
	if key != "" {
		req = append(req, WithQuery(url.Values{"paths": {key}}))
	}
	if held != nil {
		req = append(req, WithHeader("If-None-Match", held.ETag))
	}
	var body struct {
		Values   map[string]any `json:"values"`
		Version  any            `json:"version"`
		CacheTTL *float64       `json:"cache_ttl"`
	}
	res, err := cfg.c.Do(ctx, http.MethodGet, []string{"_config"}, nil, &body, append(req, opts...)...)
	if err != nil {
		return nil, err
	}
	cfg.mu.Lock()
	defer cfg.mu.Unlock()
	if owner != cfg.owner { // the caller changed while we asked: this answer isn't theirs
		if held != nil {
			return held, nil
		}
		return &configEntry{Values: map[string]any{}}, nil
	}
	if res.Status == http.StatusNotModified && held != nil {
		held.FetchedAt = time.Now().UnixMilli()
		held.TTL = ttlOf(res.Header, held.TTL)
		cfg.persistLocked()
		return held, nil
	}
	entry := &configEntry{ETag: res.Header.Get("ETag"), Values: body.Values, FetchedAt: time.Now().UnixMilli()}
	if entry.Values == nil {
		entry.Values = map[string]any{}
	}
	switch v := body.Version.(type) {
	case string:
		entry.Version = v
	case float64:
		entry.Version = strconv.FormatFloat(v, 'f', -1, 64)
	}
	if body.CacheTTL != nil {
		entry.TTL = int64(*body.CacheTTL)
	}
	before := map[string]any{}
	if held != nil {
		before = Flatten(held.Values)
	}
	cfg.entries[key] = entry
	cfg.persistLocked()
	if held != nil {
		if changed := changedPaths(before, Flatten(entry.Values)); len(changed) > 0 {
			go cfg.emitChange(changed)
		}
	}
	return entry, nil
}

func changedPaths(before, after map[string]any) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range []map[string]any{before, after} {
		for p := range m {
			if seen[p] {
				continue
			}
			seen[p] = true
			a, _ := json.Marshal(before[p])
			b, _ := json.Marshal(after[p])
			if string(a) != string(b) {
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out
}

var maxAge = regexp.MustCompile(`(?i)max-age=(\d+)`)

func ttlOf(h http.Header, fallback int64) int64 {
	if m := maxAge.FindStringSubmatch(h.Get("Cache-Control")); m != nil {
		n, _ := strconv.ParseInt(m[1], 10, 64)
		return n
	}
	return fallback
}

// revalidate refreshes a held copy in the background; a failure goes to the OnError listeners.
func (cfg *Config) revalidate(key string) {
	if _, err := cfg.fetch(cfg.c.life, key, nil); err != nil && cfg.c.life.Err() == nil {
		cfg.emitError(err)
	}
}

// reread revalidates the held entries that touch the changed paths (all of them with none).
func (cfg *Config) reread(ctx context.Context, changed []string) {
	if _, err := cfg.scope(ctx); err != nil {
		cfg.emitError(err)
		return
	}
	cfg.mu.Lock()
	keys := make([]string, 0, len(cfg.entries))
	for k := range cfg.entries {
		keys = append(keys, k)
	}
	cfg.mu.Unlock()
	for _, key := range keys {
		if len(changed) > 0 && !touches(key, changed) {
			continue
		}
		cfg.revalidate(key)
	}
}

func touches(key string, changed []string) bool {
	if key == "" {
		return true
	}
	for _, p := range strings.Split(key, ",") {
		for _, c := range changed {
			if c == p || strings.HasPrefix(c, p+".") || strings.HasPrefix(p, c+".") {
				return true
			}
		}
	}
	return false
}

func cloneMap(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return out
}
