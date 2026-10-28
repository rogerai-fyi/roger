package serve

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"

	"rogerai.fm/roger/v7/internal/edge/wire"
	"rogerai.fm/roger/v7/internal/netpriv"
)

// The network edge of advertising, as seams: the machine's interfaces and their addresses, the
// multicast plane, and the listener. Production uses the system's; a test cannot own them.
var (
	towerInterfaces = func() []net.Interface {
		ifaces, err := net.Interfaces()
		if err != nil {
			return nil
		}
		return ifaces
	}
	towerAddrsOf = func(i net.Interface) ([]net.Addr, error) { return i.Addrs() }
	towerPlane   = wire.NewRejoiningMulticastTransport
	towerListen  = net.Listen
)

// advertiser decides, from the bind, whether and what a --advertise Tower announces
// (features/lan/discovery_and_dial.feature): only on interfaces whose addresses are all private,
// only private addresses, and only its address, port and a label - no account, key or client
// list. A loopback or public bind has nothing a LAN can use, so it says so and advertises nothing.
func advertiser(bindHost string, port int, label string, out io.Writer) func(context.Context) {
	ifaces := func() []net.Interface { return wire.PrivateInterfaces(towerInterfaces(), towerAddrsOf) }
	var ips func() []net.IP
	ip := net.ParseIP(bindHost)
	switch {
	case ip == nil:
		fmt.Fprintln(out, "roger tower serve: not advertising: the bind host is not an address")
		return nil
	case ip.IsUnspecified():
		ips = func() []net.IP {
			var addrs []net.Addr
			for _, i := range ifaces() {
				a, err := towerAddrsOf(i)
				if err == nil {
					addrs = append(addrs, a...)
				}
			}
			return wire.AdvertiseAddrs(addrs)
		}
	case ip.IsLoopback():
		fmt.Fprintln(out, "roger tower serve: not advertising: bound to loopback, which no other machine can reach; bind a LAN address")
		return nil
	case !netpriv.IsHome(ip):
		fmt.Fprintln(out, "roger tower serve: not advertising: advertisements go on private networks only")
		return nil
	default:
		ips = func() []net.IP { return []net.IP{ip} }
	}
	if label == "" {
		label, _ = os.Hostname()
	}
	ad := wire.Advert{NodeID: "tower-" + label + "-" + strconv.Itoa(port), Name: label, Kind: wire.TowerKind, Port: port}
	return func(ctx context.Context) {
		tp, err := towerPlane(ifaces)
		if err != nil {
			fmt.Fprintf(out, "roger tower serve: not advertising: %v\n", err)
			return
		}
		defer tp.Close()
		fmt.Fprintf(out, "roger tower serve: advertising %q on the private network (mDNS %s)\n", label, wire.TowerService)
		_ = wire.NewLiveResponder(tp, wire.TowerService, ad, ips).Serve(ctx)
	}
}
