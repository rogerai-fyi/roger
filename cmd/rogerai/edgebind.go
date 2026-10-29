package main

import (
	"net"

	"rogerai.fm/roger/v7/internal/netbind"
	"rogerai.fm/roger/v7/internal/netpriv"
)

// edgeInterfaceAddrs, edgeIsLoopback and edgeIsPrivate are this machine's interface addresses and
// the address rules (seams so the bind rule is tested on real sockets).
var (
	edgeInterfaceAddrs = net.InterfaceAddrs
	edgeIsLoopback     = func(ip net.IP) bool { return ip.IsLoopback() }
	edgeIsPrivate      = netpriv.IsHome
)

// edgeListen opens an Edge listener (the face or the authority) through netbind. An explicit
// host in bind is honoured as given. A wildcard bind (":port") listens on loopback and each
// private interface address only, all on one port, so an Edge never answers on a public
// interface (features/lan/regressions.feature). It keeps following this machine's addresses, so a
// member whose address changed answers at the address it now advertises.
func edgeListen(bind string) (net.Listener, error) {
	return netbind.ListenEach(bind, func() ([]net.Addr, error) { return edgeInterfaceAddrs() }, func(ip net.IP) bool {
		return edgeIsLoopback(ip) || edgeIsPrivate(ip)
	})
}
