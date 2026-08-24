package localplane

import (
	"fmt"
	"net"
)

// DefaultBind is where the consumer plane listens when the operator names no address:
// loopback, reachable only from the Tower host itself. Widening to the LAN is a deliberate,
// explicit choice, never the default.
const DefaultBind = "127.0.0.1:8787"

// privateBindCIDRs are the addresses the plane may bind without an override: loopback and the
// RFC1918 / IPv6-ULA private ranges. Link-local (169.254/16) is deliberately absent - it is
// where cloud instance-metadata lives, never where a plant's consumer plane should sit.
var privateBindCIDRs = mustCIDRs(
	"127.0.0.0/8", "::1/128",
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7",
)

// ResolveBind validates the address the consumer plane is asked to listen on and returns the
// address to bind plus a human note describing the exposure. The posture is the spec's
// "refuses to masquerade on a public address":
//
//   - loopback or a private-LAN address: allowed, and the note states which.
//   - the UNSPECIFIED address (0.0.0.0 / ::), or any public IP: REFUSED, because an unbound
//     standalone Tower on a routable address is a broker lookalike an attacker can point a
//     victim's ROGER_BROKER at. It is allowed only with an explicit acknowledged override,
//     and even then the note says plainly what was opened.
//   - a hostname is refused rather than resolved: resolving it is a DNS lookup the airgap
//     posture forbids, and a name that resolves privately once can resolve publicly next.
func ResolveBind(addr string, allowPublic bool) (bind, note string, err error) {
	if addr == "" {
		addr = DefaultBind
	}
	host, port, serr := net.SplitHostPort(addr)
	if serr != nil {
		return "", "", fmt.Errorf("--bind %q is not a valid host:port", addr)
	}
	if _, perr := net.LookupPort("tcp", port); perr != nil {
		return "", "", fmt.Errorf("--bind %q has an invalid port", addr)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", "", fmt.Errorf("--bind host %q must be a literal IP (a hostname would need a DNS lookup the airgap posture forbids)", host)
	}
	switch {
	case ip.IsLoopback():
		return addr, "listening on loopback: reachable only from this host", nil
	case ip.IsUnspecified():
		if !allowPublic {
			return "", "", fmt.Errorf("--bind %q listens on ALL interfaces, which exposes this Tower as a public broker lookalike; pass --allow-public to acknowledge, or bind a specific loopback/LAN address", addr)
		}
		return addr, "WARNING: listening on ALL interfaces (--allow-public) - this Tower is reachable from every network it is attached to", nil
	case isPrivateBind(ip):
		return addr, "listening on a private-LAN address: reachable from the local network", nil
	default:
		if !allowPublic {
			return "", "", fmt.Errorf("--bind %q is a PUBLIC address; a standalone Tower there is a broker lookalike for phishing ROGER_BROKER. Pass --allow-public to acknowledge, or bind a loopback/LAN address", addr)
		}
		return addr, "WARNING: listening on a PUBLIC address (--allow-public) - anyone who can reach it can try this Tower", nil
	}
}

func isPrivateBind(ip net.IP) bool {
	for _, n := range privateBindCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func mustCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic("localplane: bad built-in CIDR " + c)
		}
		out = append(out, n)
	}
	return out
}
