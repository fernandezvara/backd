package egress

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBlockedAddresses(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1":       true,  // loopback
		"10.0.0.5":        true,  // private
		"172.17.0.2":      true,  // private (docker default bridge)
		"192.168.1.1":     true,  // private
		"169.254.169.254": true,  // link-local / cloud metadata
		"100.64.0.1":      true,  // CGNAT
		"224.0.0.1":       true,  // multicast
		"0.0.0.0":         true,  // unspecified
		"::1":             true,  // loopback, IPv6
		"8.8.8.8":         false, // public
		"93.184.216.34":   false, // public
	}
	for ip, want := range cases {
		addr := netip.MustParseAddr(ip)
		if got := blocked(addr); got != want {
			t.Errorf("blocked(%s) = %v, want %v", ip, got, want)
		}
	}
}

// client returns an http.Client that routes through proxy, authenticated
// with token.
func client(proxy, token string) *http.Client {
	pu, _ := url.Parse(proxy)
	pu.User = url.User(token)
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
}

func TestForwardBlocksPrivateByDefault(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hi")) }))
	defer target.Close()
	targetHost := strings.TrimPrefix(target.URL, "http://")

	key := []byte(testKey)
	proxy := httptest.NewServer((&Proxy{Key: key}).Handler())
	defer proxy.Close()

	token := Sign(key, Claims{Hosts: []string{targetHost}, Expires: time.Now().Add(time.Minute)})
	res, err := client(proxy.URL, token).Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403 (the target is a loopback address)", res.StatusCode)
	}
}

func TestForwardAllowsWithException(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hi")) }))
	defer target.Close()
	targetHost := strings.TrimPrefix(target.URL, "http://")

	key := []byte(testKey)
	proxy := httptest.NewServer((&Proxy{Key: key, AllowPrivate: []string{targetHost}}).Handler())
	defer proxy.Close()

	token := Sign(key, Claims{Hosts: []string{targetHost}, Expires: time.Now().Add(time.Minute)})
	res, err := client(proxy.URL, token).Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || string(body) != "hi" {
		t.Fatalf("status %d body %q", res.StatusCode, body)
	}
}

func TestForwardRefusesHostNotInToken(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hi")) }))
	defer target.Close()
	targetHost := strings.TrimPrefix(target.URL, "http://")

	key := []byte(testKey)
	proxy := httptest.NewServer((&Proxy{Key: key, AllowPrivate: []string{targetHost}}).Handler())
	defer proxy.Close()

	// A valid token, but scoped to a different host: the exception alone
	// (an operator mistake or a stale AllowPrivate entry) must not be
	// enough to reach it.
	token := Sign(key, Claims{Hosts: []string{"somewhere-else.example"}, Expires: time.Now().Add(time.Minute)})
	res, err := client(proxy.URL, token).Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", res.StatusCode)
	}
}

func TestForwardRequiresCredentials(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hi")) }))
	defer target.Close()
	targetHost := strings.TrimPrefix(target.URL, "http://")

	proxy := httptest.NewServer((&Proxy{Key: []byte(testKey), AllowPrivate: []string{targetHost}}).Handler())
	defer proxy.Close()

	res, err := (&http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(mustParse(proxy.URL))}}).Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("status %d, want 407", res.StatusCode)
	}
}

func mustParse(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

// connectRoundTrip dials proxyAddr, issues a raw CONNECT to targetHost
// with token as its Proxy-Authorization credential, and returns the
// status line. On a 2xx it also sends a plain HTTP GET through the tunnel
// and returns the response body.
func connectRoundTrip(t *testing.T, proxyAddr, targetHost, token string) (status string, body string) {
	t.Helper()
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	auth := base64.StdEncoding.EncodeToString([]byte(token + ":"))
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", targetHost, targetHost, auth)
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	status = strings.TrimSpace(line)
	if !strings.Contains(status, "200") {
		return status, ""
	}
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if l == "\r\n" {
			break
		}
	}
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", targetHost)
	data, _ := io.ReadAll(conn)
	return status, string(data)
}

func TestConnectBlocksPrivateByDefault(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hi")) }))
	defer target.Close()
	targetHost := strings.TrimPrefix(target.URL, "http://")

	key := []byte(testKey)
	proxy := httptest.NewServer((&Proxy{Key: key}).Handler())
	defer proxy.Close()
	proxyAddr := strings.TrimPrefix(proxy.URL, "http://")

	token := Sign(key, Claims{Hosts: []string{targetHost}, Expires: time.Now().Add(time.Minute)})
	status, _ := connectRoundTrip(t, proxyAddr, targetHost, token)
	if strings.Contains(status, "200") {
		t.Fatalf("CONNECT to a loopback target succeeded: %s", status)
	}
}

func TestConnectAllowsWithException(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hi")) }))
	defer target.Close()
	targetHost := strings.TrimPrefix(target.URL, "http://")

	key := []byte(testKey)
	proxy := httptest.NewServer((&Proxy{Key: key, AllowPrivate: []string{targetHost}}).Handler())
	defer proxy.Close()
	proxyAddr := strings.TrimPrefix(proxy.URL, "http://")

	token := Sign(key, Claims{Hosts: []string{targetHost}, Expires: time.Now().Add(time.Minute)})
	status, body := connectRoundTrip(t, proxyAddr, targetHost, token)
	if !strings.Contains(status, "200") {
		t.Fatalf("CONNECT refused: %s", status)
	}
	if !strings.Contains(body, "hi") {
		t.Fatalf("tunnel body = %q, want it to contain the target's response", body)
	}
}

func TestExpiredTokenRefused(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hi")) }))
	defer target.Close()
	targetHost := strings.TrimPrefix(target.URL, "http://")

	key := []byte(testKey)
	proxy := httptest.NewServer((&Proxy{Key: key, AllowPrivate: []string{targetHost}}).Handler())
	defer proxy.Close()

	token := Sign(key, Claims{Hosts: []string{targetHost}, Expires: time.Now().Add(-time.Minute)})
	res, err := client(proxy.URL, token).Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("status %d, want 407", res.StatusCode)
	}
}
