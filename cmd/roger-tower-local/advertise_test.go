package main

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge/wire"
)

// TestAdvertiserRefusesBindsALANCannotUse: a loopback, public, link-local or non-address bind
// advertises nothing and says why.
func TestAdvertiserRefusesBindsALANCannotUse(t *testing.T) {
	for host, why := range map[string]string{
		"127.0.0.1":   "loopback",
		"203.0.113.7": "private networks only",
		"169.254.1.1": "private networks only",
		"tower.lan":   "not an address",
	} {
		var out strings.Builder
		require.Nil(t, advertiser(host, 8787, "x", &out), host)
		require.Contains(t, out.String(), why, host)
	}
}

// TestAdvertiserOnAllInterfacesNamesOnlyPrivateAddresses: an unspecified bind announces the
// private interfaces' private addresses, never a public one; an empty label is the hostname.
func TestAdvertiserOnAllInterfacesNamesOnlyPrivateAddresses(t *testing.T) {
	pi, pa, pp := towerInterfaces, towerAddrsOf, towerPlane
	t.Cleanup(func() { towerInterfaces, towerAddrsOf, towerPlane = pi, pa, pp })
	up := net.FlagUp | net.FlagMulticast
	towerInterfaces = func() []net.Interface {
		return []net.Interface{{Index: 1, Name: "lan0", Flags: up}, {Index: 2, Name: "wan0", Flags: up}}
	}
	towerAddrsOf = func(i net.Interface) ([]net.Addr, error) {
		if i.Name == "lan0" {
			return []net.Addr{&net.IPNet{IP: net.ParseIP("10.0.0.5")}}, nil
		}
		return []net.Addr{&net.IPNet{IP: net.ParseIP("203.0.113.7")}}, nil
	}
	plane := &recPlane{in: make(chan []byte, 1)}
	towerPlane = func(func() []net.Interface) (wire.Transport, error) { return plane, nil }
	var out strings.Builder
	run := advertiser("0.0.0.0", 9000, "", &out)
	require.NotNil(t, run)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { run(ctx); close(done) }()
	q, err := wire.QueryPacket(wire.TowerService)
	require.NoError(t, err)
	plane.in <- q
	var ads []wire.Advert
	require.Eventually(t, func() bool {
		plane.mu.Lock()
		defer plane.mu.Unlock()
		for _, p := range plane.sent {
			if ads = wire.ParseAdverts(p, wire.TowerService, nil); len(ads) > 0 {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	<-done
	host, _ := os.Hostname()
	require.Equal(t, host, ads[0].Name)
	require.True(t, ads[0].IP.Equal(net.ParseIP("10.0.0.5")))
}

// TestAdvertiserReportsAPlaneFailure: no multicast plane means no advertisement, said once.
func TestAdvertiserReportsAPlaneFailure(t *testing.T) {
	pp := towerPlane
	t.Cleanup(func() { towerPlane = pp })
	towerPlane = func(func() []net.Interface) (wire.Transport, error) { return nil, errors.New("no LAN") }
	var out strings.Builder
	advertiser("192.168.1.50", 8787, "x", &out)(context.Background())
	require.Contains(t, out.String(), "not advertising: no LAN")
}
