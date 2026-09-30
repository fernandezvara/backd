package executor

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/egress"
)

func TestProxyURLEmbedsScopedToken(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	e := &Executor{cfg: Config{Proxy: "http://backd-egress:3128", EgressKey: key}}

	u, err := e.proxyURL([]string{"api.stripe.com", "backd:8081"}, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, "http://bdg_") || !strings.HasSuffix(u, "@backd-egress:3128") {
		t.Fatalf("proxy URL = %q, want backd-egress:3128 with an embedded bdg_ token", u)
	}

	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	token := parsed.User.Username()
	c, err := egress.Verify(key, token, time.Now())
	if err != nil {
		t.Fatalf("the embedded token doesn't verify: %v", err)
	}
	if !c.Allows("api.stripe.com:443") || !c.Allows("backd:8081") {
		t.Fatalf("claims %+v don't allow the invocation's hosts", c)
	}
	if c.Allows("evil.example:443") {
		t.Fatalf("claims %+v allow an undeclared host", c)
	}
	if c.Expires.Before(time.Now().Add(10 * time.Second)) {
		t.Fatalf("token expires too early: %v", c.Expires)
	}
}

func TestNewRequiresEgressKeyWithProxy(t *testing.T) {
	if _, err := New(Config{Token: "0123456789012345678901234567890123", Dir: t.TempDir(), Proxy: "http://backd-egress:3128"}); err == nil {
		t.Fatal("expected New to refuse a proxy with no egress key")
	}
}
