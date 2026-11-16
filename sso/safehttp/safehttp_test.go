package safehttp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestAddressClasses(t *testing.T) {
	for addr, tc := range map[string]struct{ public, network bool }{
		"93.184.216.34":   {true, false},
		"2606:4700::1111": {true, false},
		"10.0.0.5":        {false, true},
		"172.16.9.1":      {false, true},
		"192.168.1.1":     {false, true},
		"fd12:3456::1":    {false, true},
		"100.64.0.7":      {false, true},
		"127.0.0.1":       {false, false},
		"::1":             {false, false},
		"169.254.1.1":     {false, false},
		"169.254.169.254": {false, false},
		"100.100.100.200": {false, false},
		"fd00:ec2::254":   {false, false},
		"0.0.0.0":         {false, false},
		"224.0.0.1":       {false, false},
		"::ffff:10.1.2.3": {false, true},
		// Transition addresses go where the IPv4 address inside them goes.
		"64:ff9b::a9fe:a9fe": {false, false}, // NAT64 to the metadata address
		"64:ff9b::7f00:1":    {false, false}, // NAT64 to loopback
		"64:ff9b::a00:5":     {false, true},  // NAT64 to 10.0.0.5
		"64:ff9b::5db8:d822": {true, false},  // NAT64 to 93.184.216.34
		"2002:a9fe:a9fe::1":  {false, false}, // 6to4 of the metadata address
	} {
		ip := netip.MustParseAddr(addr)
		if got := publicAddr(ip); got != tc.public {
			t.Errorf("%s public = %v", addr, got)
		}
		if got := networkAddr(ip); got != tc.network {
			t.Errorf("%s network = %v", addr, got)
		}
	}
}

func TestContextHostsAndLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	hostport := srv.Listener.Addr().String()
	host, _, _ := net.SplitHostPort(hostport)
	c := NewClient(func(string) bool { return false }, false)
	get := func(ctx context.Context) error {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
		resp, err := c.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	// Nothing names the host: refused before anything is sent.
	if err := get(context.Background()); !errors.Is(err, ErrHostNotAllowed) {
		t.Errorf("an unnamed host: %v", err)
	}
	// The provider names it, but it is loopback: refused, with or without the network switch.
	if err := get(ForProvider(context.Background(), false, host)); !errors.Is(err, ErrHostNotAllowed) {
		t.Errorf("a named loopback host: %v", err)
	}
	if err := get(ForProvider(context.Background(), true, host)); !errors.Is(err, ErrHostNotAllowed) {
		t.Errorf("a named loopback host with allow_private_network: %v", err)
	}
	// A development client reaches it when it is named.
	dev := NewClient(func(string) bool { return false }, true)
	req, _ := http.NewRequestWithContext(ForProvider(context.Background(), false, host), http.MethodGet, srv.URL, nil)
	resp, err := dev.Do(req)
	if err != nil {
		t.Fatalf("dev: %v", err)
	}
	resp.Body.Close()
}
