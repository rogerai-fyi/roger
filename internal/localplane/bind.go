package localplane

import (
	"fmt"
	"net"

	"rogerai.fm/roger/v7/internal/netpriv"
)

// DefaultBind is where the consumer plane listens when the operator names no address:
// loopback, reachable only from the Tower host itself. Widening to the LAN is a deliberate,
// explicit choice, never the default. The posture check itself is internal/netbind.Resolve.
const DefaultBind = "127.0.0.1:8787"

// InviteAddrs is where an invite says the Tower serves, from the address it is bound to: a
// specific address as it is (loopbackOnly when it is loopback), an all-interfaces bind expanded to
// this machine's home addresses from ifaces. The CLI invite and the owner's screens both use it.
func InviteAddrs(bind string, ifaces func() ([]net.Addr, error)) (addrs []string, loopbackOnly bool, err error) {
	host, port, err := net.SplitHostPort(bind)
	if err != nil {
		return nil, false, fmt.Errorf("the serve bind %q is not host:port", bind)
	}
	ip := net.ParseIP(host)
	if host != "" && (ip == nil || !ip.IsUnspecified()) {
		return []string{bind}, ip != nil && ip.IsLoopback(), nil
	}
	ifs, err := ifaces()
	if err != nil {
		return nil, false, err
	}
	for _, a := range ifs {
		if n, ok := a.(*net.IPNet); ok && netpriv.IsHome(n.IP) && !n.IP.IsLoopback() {
			addrs = append(addrs, net.JoinHostPort(n.IP.String(), port))
		}
	}
	if len(addrs) == 0 {
		return []string{net.JoinHostPort("127.0.0.1", port)}, true, nil
	}
	return addrs, false, nil
}
