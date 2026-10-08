// Package oauth talks to external identity providers (Google, Microsoft, Apple):
// the authorize URL, the code exchange, the verification of ID tokens and Apple's
// client secret. It knows nothing of users or sessions.
//
// Everything it sends goes through an HTTP client that dials only the provider's
// own hosts, resolves them itself and refuses private, loopback and link-local
// addresses, so a mistake in configuration can't make backd reach inside the network.
package oauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

// maxBody is the most a provider's answer may be.
const maxBody = 1 << 20

// ErrHostNotAllowed means a request named a host that is not one of the provider's.
var ErrHostNotAllowed = errors.New("oauth: the host is not one of the provider's")

// NewClient returns an HTTP client that connects only to hosts allowed accepts, by
// name, and, unless allowPrivate, only to public addresses (checked after the name
// is resolved, on the address actually dialed). It follows no redirect and uses no
// proxy from the environment.
func NewClient(allowed func(host string) bool, allowPrivate bool) *http.Client {
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			if allowPrivate {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || !publicAddr(ip) {
				return fmt.Errorf("%w: %s is not a public address", ErrHostNotAllowed, host)
			}
			return nil
		},
	}
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil || !allowed(strings.ToLower(host)) {
				return nil, fmt.Errorf("%w: %s", ErrHostNotAllowed, addr)
			}
			return dialer.DialContext(ctx, network, addr)
		},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConns:          10,
		IdleConnTimeout:       60 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{
		Transport:     tr,
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// publicAddr reports whether ip is an address on the public internet.
func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsMulticast() && !ip.IsUnspecified() && !ip.IsInterfaceLocalMulticast()
}

// BuiltinHosts are the hosts of each built-in provider.
var BuiltinHosts = map[string][]string{
	"google":    {"accounts.google.com", "oauth2.googleapis.com", "www.googleapis.com"},
	"microsoft": {"login.microsoftonline.com"},
	"apple":     {"appleid.apple.com"},
}

// BuiltinHostAllowed accepts the hosts of the built-in providers.
func BuiltinHostAllowed(host string) bool {
	for _, hosts := range BuiltinHosts {
		for _, h := range hosts {
			if host == h {
				return true
			}
		}
	}
	return false
}

// readBody reads a provider's answer, at most maxBody bytes.
func readBody(resp *http.Response) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBody {
		return nil, errors.New("oauth: the provider's answer is too large")
	}
	return data, nil
}
