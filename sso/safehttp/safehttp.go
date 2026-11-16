// Package safehttp is the HTTP client sign-in uses to reach identity providers.
//
// Everything it sends goes through a client that dials only the provider's
// own hosts, resolves them itself and refuses private, loopback and link-local
// addresses, so a mistake in configuration can't make backd reach inside the network.
package safehttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"syscall"
	"time"
)

// maxBody is the most a provider's answer may be.
const maxBody = 1 << 20

// ErrHostNotAllowed means a request named a host that is not one of the provider's.
var ErrHostNotAllowed = errors.New("sso: the host is not one of the provider's")

// scopeKey carries what one request may reach beyond the client's own rules.
type scopeKey struct{}

type scope struct {
	hosts   []string
	private bool
}

// ForProvider returns a context whose requests may also reach the given hosts (a provider's own,
// which only its configuration names), and, when private, resolve them to private addresses: the
// ranges of a network (10/8, 172.16/12, 192.168/16, fc00::/7, 100.64/10), never loopback,
// link-local, the metadata address or anything that isn't a unicast address.
func ForProvider(ctx context.Context, private bool, hosts ...string) context.Context {
	cur, _ := ctx.Value(scopeKey{}).(scope)
	cur.hosts = append(slices.Clone(cur.hosts), hosts...)
	cur.private = cur.private || private
	return context.WithValue(ctx, scopeKey{}, cur)
}

// NewClient returns an HTTP client that connects only to hosts allowed accepts, by
// name (or that the request's context names with ForProvider), and, unless allowPrivate, only
// to public addresses (checked after the name is resolved, on the address actually dialed). It
// follows no redirect and uses no proxy from the environment.
func NewClient(allowed func(host string) bool, allowPrivate bool) *http.Client {
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			host = strings.ToLower(host)
			sc, _ := ctx.Value(scopeKey{}).(scope)
			if err != nil || (!allowed(host) && !slices.Contains(sc.hosts, host)) {
				return nil, fmt.Errorf("%w: %s", ErrHostNotAllowed, addr)
			}
			// The dialer judges the address that was resolved, not the name that was asked for.
			dialer := &net.Dialer{
				Timeout: 5 * time.Second,
				Control: func(_, address string, _ syscall.RawConn) error {
					if allowPrivate {
						return nil
					}
					h, _, err := net.SplitHostPort(address)
					if err != nil {
						return err
					}
					ip, err := netip.ParseAddr(h)
					if err != nil || (!publicAddr(ip) && (!sc.private || !networkAddr(ip))) {
						return fmt.Errorf("%w: %s is not a public address", ErrHostNotAllowed, h)
					}
					return nil
				},
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
		!ip.IsMulticast() && !ip.IsUnspecified() && !ip.IsInterfaceLocalMulticast() && !sharedAddr(ip) && !metadataAddr(ip)
}

// networkAddr reports whether ip is an address of a private network that an operator may mean: the
// private ranges of RFC 1918 and RFC 4193, and the shared address space of carrier-grade NAT.
func networkAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && (ip.IsPrivate() || sharedAddr(ip)) && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified() && !ip.IsMulticast() && !metadataAddr(ip)
}

// metadataAddr is the address clouds serve instance credentials on: refused whatever else is allowed.
func metadataAddr(ip netip.Addr) bool {
	for _, m := range []string{"169.254.169.254", "169.254.170.2", "100.100.100.200", "fd00:ec2::254", "fd20:ce::254"} {
		if ip == netip.MustParseAddr(m) {
			return true
		}
	}
	return false
}

// sharedAddr is 100.64.0.0/10 (RFC 6598), which is neither public nor RFC 1918.
func sharedAddr(ip netip.Addr) bool {
	return ip.Is4() && netip.MustParsePrefix("100.64.0.0/10").Contains(ip)
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
		if slices.Contains(hosts, host) {
			return true
		}
	}
	return false
}

// ReadBody reads a provider's answer, at most maxBody bytes.
func ReadBody(resp *http.Response) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBody {
		return nil, errors.New("sso: the provider's answer is too large")
	}
	return data, nil
}
