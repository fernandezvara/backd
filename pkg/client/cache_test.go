package client

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
)

// etagServer answers GETs with a body and an ETag, and 304 to a matching If-None-Match.
type etagServer struct {
	version atomic.Int32
	full    atomic.Int32
	notMod  atomic.Int32
	cc      string
}

func (s *etagServer) handler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		w.WriteHeader(204)
		return
	}
	tag := fmt.Sprintf(`"v%d"`, s.version.Load())
	w.Header().Set("ETag", tag)
	if s.cc != "" {
		w.Header().Set("Cache-Control", s.cc)
	}
	if r.Header.Get("If-None-Match") == tag {
		s.notMod.Add(1)
		w.WriteHeader(304)
		return
	}
	s.full.Add(1)
	fmt.Fprintf(w, `{"id":"x","rev":%d}`, s.version.Load())
}

func TestUnchangedReadsCostA304(t *testing.T) {
	var s etagServer
	c := newTestClient(t, s.handler)
	posts := c.DB("main").Collection("posts")

	d1, err := posts.Get(bg, "x", ReadOptions{})
	d2, err2 := posts.Get(bg, "x", ReadOptions{})
	if err != nil || err2 != nil || d1["rev"] != float64(0) || d2["rev"] != float64(0) {
		t.Fatalf("%v %v %v %v", err, err2, d1, d2)
	}
	if s.full.Load() != 1 || s.notMod.Load() != 1 {
		t.Errorf("full %d, 304 %d", s.full.Load(), s.notMod.Load())
	}
	if st := c.CacheStats(); st.Hits != 1 || st.Misses != 1 || st.Entries != 1 || st.Bytes == 0 {
		t.Errorf("stats %+v", st)
	}
	s.version.Store(1)
	d3, _ := posts.Get(bg, "x", ReadOptions{})
	if d3["rev"] != float64(1) || s.full.Load() != 2 {
		t.Errorf("a changed document is read in full: %v", d3)
	}
	res, _ := c.Do(bg, "GET", []string{"main", "posts", "x"}, nil, nil)
	if !res.FromCache || res.Status != 200 || res.Header.Get("ETag") != `"v1"` {
		t.Errorf("%+v", res)
	}
}

func TestWritesDropTheDocumentsCopy(t *testing.T) {
	var s etagServer
	c := newTestClient(t, s.handler)
	posts := c.DB("main").Collection("posts")
	posts.Get(bg, "x", ReadOptions{})
	posts.Get(bg, "x", ReadOptions{})
	if _, err := posts.Patch(bg, "x", Doc{"a": 1}); err != nil {
		t.Fatal(err)
	}
	posts.Get(bg, "x", ReadOptions{})
	if s.full.Load() != 2 {
		t.Errorf("after a write the document is read in full again: %d full reads", s.full.Load())
	}
}

func TestWhatIsNotCached(t *testing.T) {
	var s etagServer
	c := newTestClient(t, s.handler)
	posts := c.DB("main").Collection("posts")
	for i := 0; i < 2; i++ {
		posts.Get(bg, "x", ReadOptions{Expand: "author"})
		posts.Get(bg, "x", ReadOptions{FileLinks: true})
		c.Do(bg, "GET", []string{"_config"}, nil, nil)
		c.Do(bg, "GET", []string{"main", "posts", "x", "files"}, nil, nil)
		c.Do(bg, "GET", []string{"main", "posts", "x"}, nil, nil, WithoutCache())
	}
	if s.notMod.Load() != 0 || c.CacheStats().Entries != 0 {
		t.Errorf("304s %d, entries %d", s.notMod.Load(), c.CacheStats().Entries)
	}
	res, _ := c.Do(bg, "GET", []string{"main", "posts", "x"}, nil, nil, WithRaw())
	res.Body.Close()
	if c.CacheStats().Entries != 0 {
		t.Error("a raw read was cached")
	}
}

func TestNoStoreAndDisabled(t *testing.T) {
	s := etagServer{cc: "private, no-store"}
	c := newTestClient(t, s.handler)
	c.DB("main").Collection("posts").Get(bg, "x", ReadOptions{})
	if c.CacheStats().Entries != 0 {
		t.Error("no-store was kept")
	}
	var s2 etagServer
	c2 := newTestClient(t, s2.handler, func(o *Options) { o.Cache.Disable = true })
	c2.DB("main").Collection("posts").Get(bg, "x", ReadOptions{})
	c2.DB("main").Collection("posts").Get(bg, "x", ReadOptions{})
	if s2.full.Load() != 2 || s2.notMod.Load() != 0 || c2.CacheStats() != (CacheStats{}) {
		t.Errorf("full %d, 304 %d", s2.full.Load(), s2.notMod.Load())
	}
}

func TestACallersOwnIfNoneMatchGetsTheNotModified(t *testing.T) {
	var s etagServer
	c := newTestClient(t, s.handler)
	res, err := c.Do(bg, "GET", []string{"main", "posts", "x"}, nil, nil, WithHeader("If-None-Match", `"v0"`))
	if err != nil || res.Status != 304 || len(res.Data) != 0 {
		t.Errorf("%v %+v", err, res)
	}
	if c.CacheStats().Entries != 0 {
		t.Error("a 304 for the caller's own tag must not fill the cache")
	}
}

func TestCopiesAreNotSharedBetweenCallers(t *testing.T) {
	var s etagServer
	c := newTestClient(t, s.handler)
	posts := c.DB("main").Collection("posts")
	posts.Get(bg, "x", ReadOptions{}) // as no one
	c.store.Set(bg, "tok-ada")
	posts.Get(bg, "x", ReadOptions{}) // as Ada: a full read, the copy was someone else's
	posts.Get(bg, "x", ReadOptions{})
	c.store.Set(bg, "tok-bob")
	posts.Get(bg, "x", ReadOptions{})
	if s.full.Load() != 3 || s.notMod.Load() != 1 {
		t.Errorf("full %d, 304 %d", s.full.Load(), s.notMod.Load())
	}
}

func TestSignInAndOutClearTheCache(t *testing.T) {
	var s etagServer
	rt := newRoutes()
	rt.json("POST /_auth/login", "", sessionJSON)
	rt.json("POST /_auth/logout", "204", ``)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			rt.ServeHTTP(w, r)
			return
		}
		s.handler(w, r)
	})
	posts := c.DB("main").Collection("posts")
	posts.Get(bg, "x", ReadOptions{})
	if c.CacheStats().Entries != 1 {
		t.Fatal("not cached")
	}
	c.Auth().Login(bg, SignInInput{})
	if c.CacheStats().Entries != 0 {
		t.Error("a sign-in keeps what was read before it")
	}
	posts.Get(bg, "x", ReadOptions{})
	c.Auth().Logout(bg)
	if c.CacheStats().Entries != 0 {
		t.Error("a sign-out keeps what the user read")
	}
	c.ClearCache()
}

func TestCacheEviction(t *testing.T) {
	c := NewRevalidationCache(CacheOptions{MaxEntries: 2, MaxBytes: 10})
	add := func(k, body string) { c.set(&cacheEntry{key: k, etag: `"e"`, data: []byte(body)}) }
	add("a", "1234")
	add("b", "1234")
	c.get("a") // a is now the most recently used
	add("c", "1234")
	if c.get("b") != nil || c.get("a") == nil || c.get("c") == nil {
		t.Error("the least recently used entry goes first")
	}
	add("d", "12345678") // bytes bound: 4+4+8 > 10 evicts until it fits
	if st := c.Stats(); st.Bytes > 10 {
		t.Errorf("bytes %d", st.Bytes)
	}
	add("big", "12345678901")
	if c.get("big") != nil {
		t.Error("a body that alone exceeds the bound is not kept")
	}
	c.dropPath("")
	c.Clear()
	if st := c.Stats(); st.Entries != 0 || st.Bytes != 0 {
		t.Errorf("%+v", st)
	}
}

func TestDropPathIgnoresTheQuery(t *testing.T) {
	c := NewRevalidationCache(CacheOptions{})
	c.set(&cacheEntry{key: "http://h/v1/r/main/posts/x?a=1", data: []byte("1")})
	c.set(&cacheEntry{key: "http://h/v1/r/main/posts/x", data: []byte("2")})
	c.set(&cacheEntry{key: "http://h/v1/r/main/posts/y", data: []byte("3")})
	c.dropPath("/v1/r/main/posts/x")
	if st := c.Stats(); st.Entries != 1 {
		t.Errorf("%+v", st)
	}
}

