package main

import (
	"net"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeInterfaces stands in for this machine's interfaces with loopback addresses (Linux binds any
// of 127.0.0.0/8), and a classifier that calls one "private" and one "public", so the bind rule is
// tested on real sockets without real network cards (features/lan/regressions.feature).
func fakeInterfaces(t *testing.T) (loop, private, public string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("binding arbitrary 127.0.0.0/8 addresses needs Linux")
	}
	loop, private, public = "127.0.0.1", "127.0.0.2", "127.0.0.3"
	prevAddrs, prevLoop, prevPriv := edgeInterfaceAddrs, edgeIsLoopback, edgeIsPrivate
	edgeInterfaceAddrs = func() ([]net.Addr, error) {
		var out []net.Addr
		for _, a := range []string{loop, private, public} {
			out = append(out, &net.IPNet{IP: net.ParseIP(a), Mask: net.CIDRMask(8, 32)})
		}
		return out, nil
	}
	edgeIsLoopback = func(ip net.IP) bool { return ip.Equal(net.ParseIP(loop)) }
	edgeIsPrivate = func(ip net.IP) bool { return ip.Equal(net.ParseIP(private)) }
	t.Cleanup(func() { edgeInterfaceAddrs, edgeIsLoopback, edgeIsPrivate = prevAddrs, prevLoop, prevPriv })
	return
}

func dialOK(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// A wildcard bind listens on the private interface and loopback only: the public one refuses.
func TestEdgeListenBindsPrivateInterfacesOnly(t *testing.T) {
	loop, private, public := fakeInterfaces(t)
	ln, err := edgeListen(":0")
	require.NoError(t, err)
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	require.True(t, dialOK(net.JoinHostPort(private, port)), "the private interface answers")
	require.True(t, dialOK(net.JoinHostPort(loop, port)), "loopback answers")
	require.False(t, dialOK(net.JoinHostPort(public, port)), "the public interface refuses")
}

// An explicit bind is honoured as given, even on an address the default would not choose.
func TestEdgeListenHonoursAnExplicitBind(t *testing.T) {
	_, _, public := fakeInterfaces(t)
	ln, err := edgeListen(net.JoinHostPort(public, "0"))
	require.NoError(t, err)
	defer ln.Close()
	require.Equal(t, public, ln.Addr().(*net.TCPAddr).IP.String())
}
