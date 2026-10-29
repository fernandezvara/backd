package client

import (
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// configServer is a fake _config endpoint.
type configServer struct {
	mu     sync.Mutex
	values string
	etag   string
	ttl    int
	paths  []string
	full   int
	notMod int
}

func (s *configServer) set(etag, values string) {
	s.mu.Lock()
	s.etag, s.values = etag, values
	s.mu.Unlock()
}

func (s *configServer) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paths = append(s.paths, r.URL.Query().Get("paths"))
	w.Header().Set("ETag", s.etag)
	if r.Header.Get("If-None-Match") == s.etag {
		s.notMod++
		w.Header().Set("Cache-Control", "max-age=60")
		w.WriteHeader(304)
		return
	}
	s.full++
	fmt.Fprintf(w, `{"values":%s,"version":"v1","cache_ttl":%d}`, s.values, s.ttl)
}

func (s *configServer) counts() (int, int) { s.mu.Lock(); defer s.mu.Unlock(); return s.full, s.notMod }

func TestConfigLoadAndGet(t *testing.T) {
	s := &configServer{values: `{"limits":{"max":5},"flags":{"beta":true},"name":"shop"}`, etag: `"e1"`, ttl: 60}
	c := newTestClient(t, s.handler)
	cfg := c.Config()

	v, err := cfg.Load(bg, []string{"limits", "flags"})
	if err != nil || v["limits"].(map[string]any)["max"] != float64(5) {
		t.Fatalf("%v %v", err, v)
	}
	if s.paths[0] != "flags,limits" {
		t.Errorf("paths = %q (sorted)", s.paths[0])
	}
	// within the ttl nothing is asked
	cfg.Load(bg, []string{"limits", "flags"})
	if got, err := cfg.Get(bg, "limits.max"); err != nil || got != float64(5) {
		t.Errorf("Get = %v %v", got, err)
	}
	if got := cfg.GetSync("flags.beta", false); got != true {
		t.Errorf("GetSync = %v", got)
	}
	if got := cfg.GetSync("nope.x", "fallback"); got != "fallback" {
		t.Errorf("GetSync fallback = %v", got)
	}
	if full, _ := s.counts(); full != 1 {
		t.Errorf("%d full reads", full)
	}
	// a value that is not covered is fetched, by its first segment
	if got, _ := cfg.Get(bg, "name"); got != "shop" || s.paths[len(s.paths)-1] != "name" {
		t.Errorf("Get(name) = %v, paths %q", got, s.paths)
	}
	// the copy handed out is a copy
	v["limits"].(map[string]any)["max"] = 99
	if cfg.GetSync("limits.max", 0) != float64(5) {
		t.Error("the held copy was changed through the one handed out")
	}
}

func TestConfigRevalidatesAndTellsWhatChanged(t *testing.T) {
	s := &configServer{values: `{"limits":{"max":5,"min":1},"flags":{"beta":true}}`, etag: `"e1"`, ttl: 60}
	c := newTestClient(t, s.handler)
	cfg := c.Config()
	changes := make(chan []string, 4)
	cfg.OnChange(func(p []string) { changes <- p })

	cfg.Load(bg, nil)
	if _, err := cfg.Refresh(bg, nil); err != nil {
		t.Fatal(err)
	}
	if full, notMod := s.counts(); full != 1 || notMod != 1 {
		t.Errorf("full %d, 304 %d", full, notMod)
	}
	s.set(`"e2"`, `{"limits":{"max":6,"min":1},"flags":{}}`)
	v, err := cfg.Refresh(bg, nil)
	if err != nil || v["limits"].(map[string]any)["max"] != float64(6) {
		t.Fatalf("%v %v", err, v)
	}
	select {
	case p := <-changes:
		if !reflect.DeepEqual(p, []string{"flags.beta", "limits.max"}) {
			t.Errorf("changed = %v", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no change event")
	}
}

func TestConfigStaleCopyIsServedAndRevalidatedInTheBackground(t *testing.T) {
	s := &configServer{values: `{"a":1}`, etag: `"e1"`, ttl: 0}
	c := newTestClient(t, s.handler)
	cfg := c.Config()
	cfg.Load(bg, []string{"a"})
	changed := make(chan []string, 1)
	cfg.OnChange(func(p []string) { changed <- p })
	s.set(`"e2"`, `{"a":2}`)
	if got, _ := cfg.Get(bg, "a"); got != float64(1) {
		t.Errorf("a stale copy answers at once: %v", got)
	}
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("no background revalidation")
	}
	if cfg.GetSync("a", 0) != float64(2) {
		t.Error("the copy was not replaced")
	}
}

func TestConfigBackgroundErrorsGoToOnError(t *testing.T) {
	var fail atomic.Bool
	s := &configServer{values: `{"a":1}`, etag: `"e1"`, ttl: 0}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, `{"error":{"code":"unavailable","message":"down"}}`, 503)
			return
		}
		s.handler(w, r)
	})
	cfg := c.Config()
	cfg.Load(bg, []string{"a"})
	errs := make(chan error, 1)
	cfg.OnError(func(err error) { errs <- err })
	fail.Store(true)
	cfg.Get(bg, "a")
	select {
	case err := <-errs:
		if err == nil {
			t.Error("nil error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the failed revalidation was not reported")
	}
}

func TestConfigIsPerCredential(t *testing.T) {
	s := &configServer{values: `{"a":1}`, etag: `"e1"`, ttl: 60}
	c := newTestClient(t, s.handler)
	cfg := c.Config()
	cfg.Load(bg, nil)
	c.store.Set(bg, "tok-ada")
	cfg.Load(bg, nil)
	if full, _ := s.counts(); full != 2 {
		t.Errorf("another caller must not see the copy of the first: %d full reads", full)
	}
	c.store.Set(bg, "")
	cfg.Load(bg, nil) // back to anonymous: its copy is gone too (one owner at a time)
}

func TestConfigSurvivesARestartThroughTheStorage(t *testing.T) {
	dir := t.TempDir()
	s := &configServer{values: `{"limits":{"max":5}}`, etag: `"e1"`, ttl: 60}
	c1 := newTestClient(t, s.handler, func(o *Options) { o.ConfigStorage = NewFileConfigStorage(dir) })
	c1.Config().Load(bg, nil)

	c2 := newTestClient(t, s.handler, func(o *Options) { o.ConfigStorage = NewFileConfigStorage(dir) })
	v, err := c2.Config().Load(bg, nil) // fresh in the saved copy: no request
	if err != nil || v["limits"].(map[string]any)["max"] != float64(5) {
		t.Fatalf("%v %v", err, v)
	}
	if full, notMod := s.counts(); full != 1 || notMod != 0 {
		t.Errorf("full %d, 304 %d", full, notMod)
	}
	// signing in clears the saved copies
	rt := newRoutes()
	rt.json("POST /_auth/login", "", sessionJSON)
	c3 := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			rt.ServeHTTP(w, r)
			return
		}
		s.handler(w, r)
	}, func(o *Options) { o.ConfigStorage = NewFileConfigStorage(dir) })
	c3.Config().Load(bg, nil)
	c3.Auth().Login(bg, SignInInput{})
	if raw, _ := NewFileConfigStorage(dir).Load(configPrefix + "r:anonymous"); raw != "" {
		t.Errorf("the anonymous copy survived a sign-in: %s", raw)
	}
}

func TestConfigDecodeAndSharedFetches(t *testing.T) {
	var calls atomic.Int32
	gate := make(chan struct{})
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-gate
		w.Header().Set("ETag", `"e"`)
		io.WriteString(w, `{"values":{"limits":{"max":7,"tags":["a","b"]}},"cache_ttl":60}`)
	})
	cfg := c.Config()
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); cfg.Load(bg, []string{"limits"}) }()
	}
	time.Sleep(100 * time.Millisecond)
	close(gate)
	wg.Wait()
	if calls.Load() != 1 {
		t.Errorf("%d requests for 5 concurrent loads", calls.Load())
	}
	var limits struct {
		Max  int      `json:"max"`
		Tags []string `json:"tags"`
	}
	if err := cfg.Decode(bg, "limits", &limits); err != nil || limits.Max != 7 || len(limits.Tags) != 2 {
		t.Errorf("%v %+v", err, limits)
	}
}

func TestFlatten(t *testing.T) {
	got := Flatten(map[string]any{"a": map[string]any{"b": 1.0, "c": map[string]any{"d": "x"}}, "e": []any{1.0}})
	if !reflect.DeepEqual(got, map[string]any{"a.b": 1.0, "a.c.d": "x", "e": []any{1.0}}) {
		t.Errorf("%v", got)
	}
}
