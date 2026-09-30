package registry

import (
	"fmt"
	"net/netip"
	"strings"
)

// Networks is a list of IP networks. An empty list restricts nothing.
type Networks []netip.Prefix

// ParseNetworks parses IP addresses and CIDR networks, IPv4 or IPv6.
func ParseNetworks(list []string) (Networks, error) {
	var out Networks
	for _, v := range list {
		v = strings.TrimSpace(v)
		p, err := netip.ParsePrefix(v)
		if err != nil {
			a, aerr := netip.ParseAddr(v)
			if aerr != nil {
				return nil, fmt.Errorf("%q is not an IP address or CIDR network", v)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// Allows reports whether addr is in one of the networks; an empty list
// allows every address. addr is an IP address such as the one the HTTP
// layer resolves with TRUSTED_PROXIES; an unparsable one is refused by a
// non-empty list.
func (n Networks) Allows(addr string) bool {
	if len(n) == 0 {
		return true
	}
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range n {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Within reports whether every network of n lies inside one of outer;
// always true when outer is empty.
func (n Networks) Within(outer Networks) bool {
	if len(outer) == 0 {
		return true
	}
	for _, p := range n {
		ok := false
		for _, o := range outer {
			if o.Bits() <= p.Bits() && o.Contains(p.Addr()) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// Strings returns the networks in CIDR notation.
func (n Networks) Strings() []string {
	out := make([]string, len(n))
	for i, p := range n {
		out[i] = p.String()
	}
	return out
}

// Equal reports whether both lists hold the same networks in the same order.
func (n Networks) Equal(o Networks) bool {
	if len(n) != len(o) {
		return false
	}
	for i := range n {
		if n[i] != o[i] {
			return false
		}
	}
	return true
}
