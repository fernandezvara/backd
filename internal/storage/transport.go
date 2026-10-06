package storage

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// backd connects to object storage directly, not through the functions'
// egress proxy (documented exception: the egress role serves function code,
// and storage is backd's own, declared in a realm's realm.yaml). To keep that
// exception small the storage client uses its own transport, which dials only
// the declared endpoint (and, for virtual-hosted addressing, the bucket's name
// under it), checks the address it resolved to, follows no redirect and uses
// no proxy. The public endpoint is never dialled: it is only signed into links.

// allowedHosts is what a transport may connect to.
type allowedHosts struct {
	port  string
	hosts map[string]bool
	// literal is true when the endpoint is an IP address: then it is allowed
	// whatever the address is; a name may not resolve to a metadata address.
	literal bool
}

func newAllowedHosts(endpoint, bucket string, addressing Addressing) (*allowedHosts, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("storage endpoint %q is not a URL", endpoint)
	}
	host := strings.ToLower(u.Hostname())
	a := &allowedHosts{port: u.Port(), hosts: map[string]bool{host: true}, literal: net.ParseIP(host) != nil}
	if a.port == "" {
		a.port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
	}
	if addressing == VirtualHosted && !a.literal {
		a.hosts[strings.ToLower(bucket)+"."+host] = true
	}
	return a, nil
}

// errNotDeclared is what a dial to anything but the declared storage returns.
var errNotDeclared = errors.New("storage client: refusing to connect to an address that isn't the realm's declared storage endpoint")

// blockedAddress reports addresses a storage endpoint never resolves to, even
// when declared by name: the link-local range that holds cloud metadata services,
// the unspecified address and multicast. (Private ranges are allowed: a MinIO on
// the same network is the normal case.)
func blockedAddress(ip net.IP) bool {
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// dial resolves addr itself and connects to the address it chose.
func (a *allowedHosts) dial(ctx context.Context, network, addr string, d *net.Dialer, resolver *net.Resolver) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	host = strings.ToLower(host)
	if !a.hosts[host] || port != a.port {
		return nil, fmt.Errorf("%w: %s", errNotDeclared, addr)
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		found, err := resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, f := range found {
			ips = append(ips, f.IP)
		}
	}
	var last error
	for _, ip := range ips {
		if !a.literal && blockedAddress(ip) {
			last = fmt.Errorf("%w: %s resolves to %s", errNotDeclared, host, ip)
			continue
		}
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	if last == nil {
		last = fmt.Errorf("%s resolves to no address", host)
	}
	return nil, last
}

// NewHTTPClient returns the HTTP client of a realm's storage connection.
func NewHTTPClient(endpoint, bucket string, addressing Addressing) (*http.Client, error) {
	allowed, err := newAllowedHosts(endpoint, bucket, addressing)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		Proxy: nil, // direct, never through HTTP_PROXY
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return allowed.dial(ctx, network, addr, dialer, net.DefaultResolver)
		},
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   16,
	}
	return &http.Client{
		Transport: tr,
		// A redirect would be a way to another host.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}
