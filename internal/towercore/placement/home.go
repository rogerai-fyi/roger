package placement

// home.go: the two facts a Tower with no public address needs (private_hub_placement.feature):
// whether its hub can be dialed from outside at all, and which Stations share its network.

import (
	"net/netip"
	"strings"

	"rogerai.fm/roger/v7/internal/netpriv"
)

// HomeKey is the "same network" key of an observed address: the address itself for public
// IPv4 (one household behind one NAT shares it), the /64 for public IPv6 (one home LAN). A
// local or unparseable address has none, so it never matches anything.
func HomeKey(addr string) string {
	a, err := netip.ParseAddr(strings.TrimSpace(addr))
	if err != nil {
		return ""
	}
	a = a.Unmap().WithZone("")
	if netpriv.ClassifyAddr(a) != netpriv.Public {
		return ""
	}
	if a.Is4() {
		return a.String()
	}
	return netip.PrefixFrom(a, 64).Masked().String()
}

// SameHome reports whether a Station and a Tower were observed on the same network.
func SameHome(station, tower Locality) bool {
	return station.Home != "" && station.Home == tower.Home
}

// HubClass is what kind of address a Tower's hub advertises.
type HubClass int

const (
	HubNone HubClass = iota
	HubPublic
	HubPrivate
	HubLoopback
)

func (c HubClass) String() string {
	return [...]string{"none", "public", "private", "loopback"}[c]
}

// HubClassOf classes the addresses a hub host resolved to. Any public address makes the hub
// public, since Core can dial it from outside. Otherwise all private (RFC 1918, ULA, CGNAT) is
// private; anything else that parses (loopback, link-local, unspecified, or private mixed with
// one of those) is loopback, which is never handed to a Station. Nothing parseable is none.
func HubClassOf(addrs []string) HubClass {
	var public, private, other bool
	for _, s := range addrs {
		a, err := netip.ParseAddr(strings.TrimSpace(s))
		if err != nil {
			continue
		}
		switch netpriv.ClassifyAddr(a) {
		case netpriv.Public:
			public = true
		case netpriv.Private, netpriv.ULA, netpriv.CGNAT:
			private = true
		default:
			other = true
		}
	}
	switch {
	case public:
		return HubPublic
	case other:
		return HubLoopback
	case private:
		return HubPrivate
	}
	return HubNone
}
