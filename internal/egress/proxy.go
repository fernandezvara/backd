package egress

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/fernandezvara/backd/internal/metrics"
)

// cgnat is the shared address space RFC 6598 reserves for carrier-grade NAT.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// blocked reports whether ip must never be dialled from a function process.
func blocked(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || cgnat.Contains(ip)
}

// Proxy is backd's egress role. Every function process is given its own
// token (Sign), naming the hosts it may reach; the proxy checks that, and
// separately resolves each target itself and refuses the addresses
// blocked reports, except the hosts listed in AllowPrivate (backd's
// callback listener).
type Proxy struct {
	Key          []byte
	AllowPrivate []string // host:port exceptions to the private-address block
	Log          *slog.Logger
	Now          func() time.Time // for tests; defaults to time.Now
	Metrics      *metrics.Metrics // counts decisions; nil turns it off
}

func (p *Proxy) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Proxy) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.New(slog.DiscardHandler)
}

// dial connects to hostport, refusing it (after resolving) if it's a
// blocked address, unless it's one of AllowPrivate.
func (p *Proxy) dial(ctx context.Context, hostport string) (net.Conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	if !slices.Contains(p.AllowPrivate, hostport) {
		d.Control = func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || blocked(ap.Addr()) {
				return fmt.Errorf("egress to %s refused: address not allowed", address)
			}
			return nil
		}
	}
	return d.DialContext(ctx, "tcp", hostport)
}

// proxyToken reads the token a function process sent as its
// Proxy-Authorization credential (the username half of HTTP Basic auth,
// which Deno sends for an HTTP_PROXY URL of the form http://<token>@host).
func proxyToken(r *http.Request) (string, bool) {
	v := r.Header.Get("Proxy-Authorization")
	const prefix = "Basic "
	if !strings.HasPrefix(v, prefix) {
		return "", false
	}
	data, err := base64.StdEncoding.DecodeString(v[len(prefix):])
	if err != nil {
		return "", false
	}
	token, _, _ := strings.Cut(string(data), ":")
	return token, token != ""
}

// ensurePort adds scheme's default port to host if it has none.
func ensurePort(host, scheme string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	port := "80"
	if scheme == "https" {
		port = "443"
	}
	return net.JoinHostPort(host, port)
}

// Handler serves the proxy: HTTP forward requests and CONNECT tunnels.
func (p *Proxy) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hostport := ensurePort(r.URL.Host, r.URL.Scheme)
		if r.Method == http.MethodConnect {
			hostport = ensurePort(r.Host, "")
		}
		log := p.log().With("host", hostport, "method", r.Method)

		token, ok := proxyToken(r)
		if !ok {
			p.Metrics.EgressRequest("denied_credentials")
			log.Warn("egress refused: no credentials")
			w.Header().Set("Proxy-Authenticate", `Basic realm="backd-egress"`)
			http.Error(w, "egress refused: credentials required", http.StatusProxyAuthRequired)
			return
		}
		claims, err := Verify(p.Key, token, p.now())
		if err != nil {
			p.Metrics.EgressRequest("denied_credentials")
			log.Warn("egress refused: invalid credentials", "error", err)
			http.Error(w, "egress refused: invalid credentials", http.StatusProxyAuthRequired)
			return
		}
		if !claims.Allows(hostport) {
			p.Metrics.EgressRequest("denied_host")
			log.Warn("egress refused: host not in this invocation's allowlist")
			http.Error(w, "egress refused: host not allowed", http.StatusForbidden)
			return
		}
		conn, err := p.dial(r.Context(), hostport)
		if err != nil {
			p.Metrics.EgressRequest("denied_address")
			log.Warn("egress refused: address blocked", "error", err)
			http.Error(w, "egress refused", http.StatusForbidden)
			return
		}

		p.Metrics.EgressRequest("allowed")
		if r.Method == http.MethodConnect {
			p.tunnel(w, conn)
			return
		}
		p.forward(w, r, conn)
	})
}

// tunnel serves a CONNECT: it hijacks the client connection and copies
// bytes both ways with conn, which the caller has already dialled.
func (p *Proxy) tunnel(w http.ResponseWriter, conn net.Conn) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		conn.Close()
		http.Error(w, "can't hijack", http.StatusInternalServerError)
		return
	}
	cl, buf, err := hj.Hijack()
	if err != nil {
		conn.Close()
		return
	}
	cl.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	go func() { io.Copy(conn, buf); conn.Close() }()
	io.Copy(cl, conn)
	cl.Close()
}

// forward serves a plain HTTP proxy request over conn, which the caller
// has already dialled.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, conn net.Conn) {
	r.RequestURI = ""
	r.Header.Del("Proxy-Authorization")
	tr := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return conn, nil }}
	res, err := tr.RoundTrip(r)
	if err != nil {
		http.Error(w, "egress: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer res.Body.Close()
	for k, v := range res.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(res.StatusCode)
	io.Copy(w, res.Body)
}
