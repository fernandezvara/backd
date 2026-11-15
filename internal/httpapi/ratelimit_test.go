package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/ratelimit"
	"github.com/fernandezvara/backd/internal/registry"
)

// limited runs the auth fixture with the limiter on and these rates (per minute, a burst of the
// number): classes not named are unlimited.
func limited(t *testing.T, rates map[string]int) *authFixture {
	t.Helper()
	inst := registry.RateSettings{Enabled: true, Rates: map[string]ratelimit.Rate{}}
	for _, class := range registry.RateClasses {
		inst.Rates[class] = ratelimit.Rate{} // none
	}
	for class, n := range rates {
		inst.Rates[class] = ratelimit.Rate{Limit: n, Window: time.Minute}
	}
	return newAuthFixture(t, func(c *Config) { c.RateLimit = inst })
}

func (f *authFixture) status(t *testing.T, method, path string, hdr map[string]string, remote string) (int, http.Header) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remote
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec.Code, rec.Header()
}

func TestAnonymousRequestsAreLimitedByAddress(t *testing.T) {
	f := limited(t, map[string]int{registry.RateAnonymous: 3})
	const items = "/v1/shop/orders/items"
	for i := range 3 {
		if code, h := f.status(t, "GET", items, nil, "203.0.113.5:1000"); code == http.StatusTooManyRequests || h.Get("RateLimit-Remaining") != strconv.Itoa(2-i) || h.Get("RateLimit-Limit") != "3" {
			t.Fatalf("request %d: %d %v", i, code, h)
		}
	}
	code, h := f.status(t, "GET", items, nil, "203.0.113.5:1001")
	if code != http.StatusTooManyRequests || h.Get("Retry-After") == "" || h.Get("RateLimit-Remaining") != "0" {
		t.Fatalf("the 4th: %d %v", code, h)
	}
	// Another address has its own bucket.
	if code, _ := f.status(t, "GET", items, nil, "203.0.113.6:1000"); code == http.StatusTooManyRequests {
		t.Error("another address was limited")
	}
}

func TestIPv6AddressesShareTheirSlash64(t *testing.T) {
	f := limited(t, map[string]int{registry.RateAnonymous: 2})
	const items = "/v1/shop/orders/items"
	f.status(t, "GET", items, nil, "[2001:db8:1:2::1]:1")
	f.status(t, "GET", items, nil, "[2001:db8:1:2::ffff]:1")
	if code, _ := f.status(t, "GET", items, nil, "[2001:db8:1:2:9::5]:1"); code != http.StatusTooManyRequests {
		t.Errorf("the same /64 got a bucket of its own: %d", code)
	}
	if code, _ := f.status(t, "GET", items, nil, "[2001:db8:1:3::1]:1"); code == http.StatusTooManyRequests {
		t.Error("another /64 was limited")
	}
}

func TestTheAddressBehindATrustedProxyIsTheForwardedOne(t *testing.T) {
	rates := registry.RateSettings{Enabled: true, Rates: map[string]ratelimit.Rate{registry.RateAnonymous: {Limit: 1, Window: time.Minute}}}
	f := newFixtureWith(t, itemsRegistry(t), &memStore{}, func(c *Config) {
		c.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
		c.RateLimit = rates
	})
	via := func(client string) int {
		req := httptest.NewRequest("GET", "/v1/shop/orders/items", nil)
		req.RemoteAddr = "10.1.2.3:5555"
		req.Header.Set("X-Forwarded-For", client)
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		return rec.Code
	}
	if via("198.51.100.7") == http.StatusTooManyRequests || via("198.51.100.7") != http.StatusTooManyRequests {
		t.Error("the same forwarded client was not limited")
	}
	if via("198.51.100.8") == http.StatusTooManyRequests {
		t.Error("another forwarded client shared the proxy's bucket")
	}
}

func TestSignInRoutesHaveTheirOwnClass(t *testing.T) {
	f := limited(t, map[string]int{registry.RateAuth: 2, registry.RateAnonymous: 100})
	for range 2 {
		f.status(t, "POST", authBase+"/login", map[string]string{"Content-Type": "application/json"}, "203.0.113.5:1")
	}
	if code, _ := f.status(t, "POST", authBase+"/login", map[string]string{"Content-Type": "application/json"}, "203.0.113.5:1"); code != http.StatusTooManyRequests {
		t.Errorf("the 3rd sign-in attempt: %d", code)
	}
	// Reading data is another class: not affected.
	if code, _ := f.status(t, "GET", "/v1/shop/orders/items", nil, "203.0.113.5:1"); code == http.StatusTooManyRequests {
		t.Error("the auth limit leaked into the anonymous class")
	}
}

func TestSessionsAreLimitedByUserAndClass(t *testing.T) {
	f := limited(t, map[string]int{registry.RateRead: 2, registry.RateWrite: 100, registry.RateAuth: 100, registry.RateAnonymous: 100})
	token, _ := f.signup(t, "ada@example.com")
	other, _ := f.signup(t, "bob@example.com")
	me := authBase + "/me"
	for i := range 2 {
		if code, _ := f.status(t, "GET", me, bearer(token), "203.0.113.5:1"); code != http.StatusOK {
			t.Fatalf("read %d: %d", i, code)
		}
	}
	code, h := f.status(t, "GET", me, bearer(token), "203.0.113.5:1")
	if code != http.StatusTooManyRequests || h.Get("Retry-After") == "" {
		t.Fatalf("the 3rd read: %d %v", code, h)
	}
	// The same address, another user: their own bucket. Writes are another class.
	if code, _ := f.status(t, "GET", me, bearer(other), "203.0.113.5:1"); code != http.StatusOK {
		t.Errorf("another user: %d", code)
	}
	if code, _ := f.status(t, "POST", authBase+"/logout", bearer(token), "203.0.113.5:1"); code == http.StatusTooManyRequests {
		t.Error("the read limit leaked into writes")
	}
}

// limitedRealm is the expand fixture (realm acme, with auth and collections) with the limiter on.
func limitedRealm(t *testing.T, rates map[string]int) *expandFixture {
	t.Helper()
	inst := registry.RateSettings{Enabled: true, Rates: map[string]ratelimit.Rate{}}
	for _, class := range registry.RateClasses {
		inst.Rates[class] = ratelimit.Rate{}
	}
	for class, n := range rates {
		inst.Rates[class] = ratelimit.Rate{Limit: n, Window: time.Minute}
	}
	return newExpandFixture(t, func(c *Config) { c.RateLimit = inst })
}

func (x *expandFixture) statusOf(t *testing.T, method, path string, hdr map[string]string) int {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "203.0.113.5:1"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	x.h.ServeHTTP(rec, req)
	return rec.Code
}

func TestACredentialThatIsRefusedCountsAgainstTheAddress(t *testing.T) {
	x := limitedRealm(t, map[string]int{registry.RateAnonymous: 2})
	var codes []int
	for range 4 {
		codes = append(codes, x.statusOf(t, "GET", app+"tags", bearer("bds_not-a-session")))
	}
	if codes[0] != http.StatusUnauthorized || codes[3] != http.StatusTooManyRequests {
		t.Errorf("codes = %v: guessed tokens must be bounded by the address", codes)
	}
}

func TestAKeyHasItsOwnLimitAndNoneLiftsIt(t *testing.T) {
	x := limitedRealm(t, map[string]int{registry.RateRead: 1})
	ctx := context.Background()
	keyWith := func(name, limit string) string {
		_, key, err := x.svc.CreateAPIKey(ctx, name, auth.KeyOptions{RateLimit: limit})
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	slow, free, plain := keyWith("slow", "2/1m"), keyWith("free", "none"), keyWith("plain", "")
	count := func(key string, n int) (ok int) {
		for range n {
			if x.statusOf(t, "GET", app+"tags", bearer(key)) != http.StatusTooManyRequests {
				ok++
			}
		}
		return ok
	}
	if got := count(slow, 5); got != 2 {
		t.Errorf("a key limited to 2/1m got %d requests through", got)
	}
	if got := count(free, 20); got != 20 {
		t.Errorf("a key with none was limited: %d", got)
	}
	if got := count(plain, 5); got != 1 {
		t.Errorf("a key without its own limit uses the read class (1): %d", got)
	}
}

func TestOnBehalfOfCountsAgainstTheKey(t *testing.T) {
	x := limitedRealm(t, map[string]int{registry.RateRead: 3})
	_, key, err := x.svc.CreateAPIKey(context.Background(), "svc", auth.KeyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, out := x.doH(t, "GET", "/v1/acme/_auth/me", "", bearer(x.ada))
	ada, _ := out["id"].(string)
	_, out = x.doH(t, "GET", "/v1/acme/_auth/me", "", bearer(x.bob))
	bob, _ := out["id"].(string)
	if ada == "" || bob == "" {
		t.Skip("could not read the ids of the users")
	}
	ok := 0
	for _, who := range []string{ada, bob, ada, bob, ada} {
		if x.statusOf(t, "GET", app+"tags", map[string]string{"Authorization": "Bearer " + key, "X-Backd-On-Behalf-Of": who}) != http.StatusTooManyRequests {
			ok++
		}
	}
	if ok != 3 {
		t.Errorf("%d requests through: acting as other users must not give the key more", ok)
	}
}

func TestExemptions(t *testing.T) {
	f := limited(t, map[string]int{registry.RateAnonymous: 1, registry.RateAuth: 1})
	for range 5 {
		if code, _ := f.status(t, "GET", "/healthz", nil, "203.0.113.5:1"); code != http.StatusOK {
			t.Fatalf("/healthz: %d", code)
		}
		if code, _ := f.status(t, "OPTIONS", "/v1/shop/orders/items", map[string]string{"Origin": "https://app.example", "Access-Control-Request-Method": "GET"}, "203.0.113.5:1"); code == http.StatusTooManyRequests {
			t.Fatal("a preflight was limited")
		}
	}
}

func TestARealmCanRaiseOrLowerAClass(t *testing.T) {
	reg := itemsRegistry(t)
	s := reg.Realms["shop"]
	s.Settings.RateLimits = map[string]ratelimit.Rate{registry.RateAnonymous: {Limit: 1, Window: time.Minute}}
	inst := registry.RateSettings{Enabled: true, Rates: map[string]ratelimit.Rate{registry.RateAnonymous: {Limit: 100, Window: time.Minute}}}
	f := newFixtureWith(t, reg, &memStore{}, func(c *Config) { c.RateLimit = inst })
	req := func() int {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/v1/shop/orders/items", nil)
		r.RemoteAddr = "203.0.113.5:1"
		f.h.ServeHTTP(rec, r)
		return rec.Code
	}
	if req() == http.StatusTooManyRequests || req() != http.StatusTooManyRequests {
		t.Error("the realm's 1/1m did not replace the instance's 100/1m")
	}
}

func TestTheLimiterIsOffByDefault(t *testing.T) {
	f := newFixture(t)
	for range 50 {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/v1/shop/orders/items", nil)
		f.h.ServeHTTP(rec, r)
		if rec.Code == http.StatusTooManyRequests || rec.Header().Get("RateLimit-Limit") != "" {
			t.Fatal("a handler built without rate settings limits requests")
		}
	}
}

func TestAPIKeyRateLimitThroughTheAdminAPI(t *testing.T) {
	f := newRulesFixture(t)
	adminKeys := "/v1/acme/_admin/apikeys"
	hdr := map[string]string{"Authorization": "Bearer " + f.key, "Content-Type": "application/json"}

	rec, out := f.doH(t, "POST", adminKeys, `{"name": "fast", "rate_limit": " 3000/1m "}`, hdr)
	if rec.Code != http.StatusCreated || out["rate_limit"] != "3000/1m0s" {
		t.Fatalf("create: %d %v", rec.Code, out)
	}
	rec, out = f.doH(t, "POST", adminKeys, `{"name": "free", "rate_limit": "none"}`, hdr)
	if rec.Code != http.StatusCreated || out["rate_limit"] != "none" {
		t.Fatalf("none: %d %v", rec.Code, out)
	}
	rec, out = f.doH(t, "POST", adminKeys, `{"name": "plain"}`, hdr)
	if rec.Code != http.StatusCreated || out["rate_limit"] != nil {
		t.Fatalf("no limit: %d %v", rec.Code, out)
	}
	rec, out = f.doH(t, "POST", adminKeys, `{"name": "bad", "rate_limit": "lots"}`, hdr)
	details, _ := out["error"].(map[string]any)["details"].([]any)
	if rec.Code != http.StatusBadRequest || len(details) == 0 || details[0].(map[string]any)["path"] != "rate_limit" {
		t.Fatalf("bad: %d %v", rec.Code, out)
	}
	// The listing shows it.
	_, list := f.doH(t, "GET", adminKeys, "", hdr)
	byName := map[string]any{}
	for _, it := range list["items"].([]any) {
		m := it.(map[string]any)
		byName[m["name"].(string)] = m["rate_limit"]
	}
	if byName["fast"] != "3000/1m0s" || byName["free"] != "none" || byName["plain"] != nil {
		t.Errorf("listing = %v", byName)
	}
	// The change is audited, with the limit.
	_, trail := f.doH(t, "GET", "/v1/acme/_admin/audit?action=apikey.create", "", hdr)
	found := false
	for _, it := range trail["items"].([]any) {
		if d, _ := it.(map[string]any)["details"].(map[string]any); d["rate_limit"] == "3000/1m0s" {
			found = true
		}
	}
	if !found {
		t.Errorf("the audit record lacks rate_limit: %v", trail)
	}
}
