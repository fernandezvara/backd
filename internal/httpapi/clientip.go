package httpapi

import (
	"context"
	"github.com/fernandezvara/backd/internal/auth"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type trustedProxiesKey struct{}

// withTrustedProxies makes the trusted proxy networks available to clientIP.
func withTrustedProxies(nets []netip.Prefix) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), trustedProxiesKey{}, nets)))
		})
	}
}

// clientIP returns the client's address. X-Forwarded-For is believed only
// when the connection comes from a trusted proxy: the client is the
// right-most address in the chain that isn't a trusted proxy. Anything
// else would let clients choose their own address.
func clientIP(r *http.Request) string {
	nets, _ := r.Context().Value(trustedProxiesKey{}).([]netip.Prefix)
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return ""
	}
	peer = peer.Unmap()
	if !trusted(nets, peer) {
		return peer.String()
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		a = a.Unmap()
		if !trusted(nets, a) {
			return a.String()
		}
		peer = a
	}
	// Every hop is a trusted proxy: use the left-most one seen.
	return peer.String()
}

func trusted(nets []netip.Prefix, a netip.Addr) bool {
	for _, n := range nets {
		if n.Contains(a) {
			return true
		}
	}
	return false
}

// allowedFrom checks where a caller may act from: an API key only from its
// networks, a session only from its user's login networks. It answers 401
// (as for an invalid credential, without saying why) and returns false
// when the caller is outside them. An API key acting on behalf of a user
// is checked against the key's networks only.
func allowedFrom(w http.ResponseWriter, r *http.Request, caller auth.Caller) bool {
	ip := clientIP(r)
	switch {
	case caller.Key != nil && !caller.Key.Networks.Allows(ip):
		logger(r.Context()).Warn("API key used outside its networks", "key", caller.Key.Name, "client", ip)
	case caller.Key == nil && caller.User != nil && !caller.User.User.LoginNetworks.Allows(ip):
		logger(r.Context()).Warn("session used outside its user's login networks", "user", caller.User.User.ID, "client", ip)
	default:
		return true
	}
	unauthenticated(w, r, true, "credentials not accepted from this network")
	return false
}
