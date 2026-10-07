// Package netpriv is the one rule for "is this address local". Every site that binds,
// advertises, allow-lists, signs for or classifies an address by locality asks this package,
// so the sites cannot drift apart (features/packaging/one_binary.feature section 7.1).
//
// It is a leaf: standard library only, so anything can import it without pulling a plane, a
// Tower or a client along.
package netpriv

import (
	"net"
	"net/netip"
)

// Class is where an address sits.
type Class uint8

const (
	// Public is everything routable that no other class claims.
	Public Class = iota
	// Invalid is not an address.
	Invalid
	// Unspecified is 0.0.0.0 or ::, a bind wildcard nothing can dial.
	Unspecified
	// Loopback is 127.0.0.0/8 and ::1.
	Loopback
	// Private is RFC 1918: 10/8, 172.16/12, 192.168/16.
	Private
	// ULA is the IPv6 unique-local range fc00::/7.
	ULA
	// LinkLocal is 169.254/16 and fe80::/10 unicast: local to one segment, not routable, and
	// where cloud instance metadata lives.
	LinkLocal
	// CGNAT is the carrier-grade NAT range 100.64.0.0/10 (RFC 6598): shared, not ours.
	CGNAT
	// Multicast is any multicast group.
	Multicast
)

var classNames = [...]string{
	Public:      "public",
	Invalid:     "invalid",
	Unspecified: "unspecified",
	Loopback:    "loopback",
	Private:     "private",
	ULA:         "ula",
	LinkLocal:   "link-local",
	CGNAT:       "cgnat",
	Multicast:   "multicast",
}

func (c Class) String() string {
	if int(c) < len(classNames) {
		return classNames[c]
	}
	return "unknown"
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// ClassifyAddr places a. An IPv4-mapped IPv6 address is judged by the IPv4 address it carries,
// and a zone is ignored.
func ClassifyAddr(a netip.Addr) Class {
	if !a.IsValid() {
		return Invalid
	}
	a = a.Unmap().WithZone("")
	switch {
	case a.IsUnspecified():
		return Unspecified
	case a.IsLoopback():
		return Loopback
	case a.IsMulticast():
		return Multicast
	case a.IsLinkLocalUnicast():
		return LinkLocal
	case a.IsPrivate():
		if a.Is4() {
			return Private
		}
		return ULA
	case cgnat.Contains(a):
		return CGNAT
	}
	return Public
}

// Classify places ip (nil is Invalid).
func Classify(ip net.IP) Class {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return Invalid
	}
	return ClassifyAddr(a)
}

// IsHome reports loopback, RFC 1918 or ULA: the addresses a standalone plane binds and an Edge
// advertises on without an override. Link-local is not one of them, deliberately: it is where
// cloud metadata lives and where a self-assigned address means the network did not work.
func IsHome(ip net.IP) bool {
	switch Classify(ip) {
	case Loopback, Private, ULA:
		return true
	}
	return false
}

// IsLocal reports whether ip is on this machine or this LAN: home addresses plus link-local,
// and CGNAT only when the caller was told to accept it (a Tower the owner added by hand).
func IsLocal(ip net.IP, allowCGNAT bool) bool {
	switch Classify(ip) {
	case Loopback, Private, ULA, LinkLocal:
		return true
	case CGNAT:
		return allowCGNAT
	}
	return false
}

// HomeCIDRs is the home set as networks: loopback, RFC 1918 and ULA. A Tower's egress
// allow-list starts from it when the operator declares none. A fresh slice every call.
func HomeCIDRs() []*net.IPNet {
	out := make([]*net.IPNet, 0, 6)
	for _, c := range []string{"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"} {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic("netpriv: bad built-in CIDR " + c) // a build-time constant, not input
		}
		out = append(out, n)
	}
	return out
}
