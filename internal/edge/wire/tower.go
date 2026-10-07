package wire

import (
	"net"

	"rogerai.fm/roger/v6/internal/localplane"
)

// TowerService is the mDNS service a standalone Tower advertises on when started with
// --advertise (features/lan/discovery_and_dial.feature). It is separate from the Edge's service
// so an Edge browse never sees a Tower and a Tower advert can never pose as an Edge member.
const TowerService = "_rogertower._tcp"

// TowerKind is the kind a Tower advertisement carries.
const TowerKind = "tower"

// PrivateInterfaces keeps the interfaces a Tower may advertise on: up, multicast-capable, with
// at least one private address and NO public one, so the advertisement is never sent onto a
// public network segment. addrsOf reads an interface's addresses (nil = the system's).
func PrivateInterfaces(ifaces []net.Interface, addrsOf func(net.Interface) ([]net.Addr, error)) []net.Interface {
	if addrsOf == nil {
		addrsOf = func(i net.Interface) ([]net.Addr, error) { return i.Addrs() }
	}
	var out []net.Interface
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := addrsOf(ifi)
		if err != nil {
			continue
		}
		private, public := false, false
		for _, a := range addrs {
			ip := addrIP(a)
			switch {
			case ip == nil, ip.IsLinkLocalUnicast(), ip.IsLoopback():
			case localplane.IsPrivateAddr(ip):
				private = true
			default:
				public = true
			}
		}
		if private && !public {
			out = append(out, ifi)
		}
	}
	return out
}

// QueryPacket is the browse question for service, as Browse sends it.
func QueryPacket(service string) ([]byte, error) { return buildQuery(service) }

// ParseAdverts decodes the advertisements one response packet carries for service; a packet
// that is not a well-formed response yields none.
func ParseAdverts(pkt []byte, service string, from net.Addr) []Advert {
	m, err := parseMessage(pkt)
	if err != nil || !m.response {
		return nil
	}
	return advertsFrom(m, service, from)
}
