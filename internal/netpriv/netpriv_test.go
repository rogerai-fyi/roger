package netpriv

import (
	"net"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want Class
	}{
		{"127.0.0.1", Loopback}, {"127.1.2.3", Loopback}, {"::1", Loopback}, {"::ffff:127.0.0.1", Loopback},
		{"10.0.0.5", Private}, {"172.16.0.9", Private}, {"172.31.255.254", Private}, {"192.168.1.69", Private},
		{"::ffff:192.168.1.69", Private},
		{"172.32.0.1", Public}, {"172.15.255.255", Public},
		{"fd12:3456::1", ULA}, {"fc00::1", ULA},
		{"169.254.10.10", LinkLocal}, {"fe80::1", LinkLocal}, {"fe80::1%eth0", LinkLocal},
		{"100.64.0.1", CGNAT}, {"100.127.255.255", CGNAT}, {"100.128.0.1", Public},
		{"0.0.0.0", Unspecified}, {"::", Unspecified},
		{"224.0.0.251", Multicast}, {"ff02::fb", Multicast},
		{"8.8.8.8", Public}, {"2001:4860:4860::8888", Public}, {"255.255.255.255", Public},
	} {
		a, err := netip.ParseAddr(tc.addr)
		require.NoError(t, err, tc.addr)
		require.Equal(t, tc.want, ClassifyAddr(a), tc.addr)
		if a.Zone() == "" {
			require.Equal(t, tc.want, Classify(net.ParseIP(tc.addr)), tc.addr)
		}
	}
	require.Equal(t, Invalid, Classify(nil))
	require.Equal(t, Invalid, Classify(net.IP{1, 2, 3}))
	require.Equal(t, Invalid, ClassifyAddr(netip.Addr{}))
}

func TestClassNames(t *testing.T) {
	for c, name := range classNames {
		require.Equal(t, name, Class(c).String())
	}
	require.Equal(t, "unknown", Class(200).String())
}

func TestIsHomeAndIsLocal(t *testing.T) {
	home := []string{"127.0.0.1", "::1", "10.1.2.3", "172.20.0.1", "192.168.0.1", "fd00::1"}
	for _, s := range home {
		ip := net.ParseIP(s)
		require.True(t, IsHome(ip), s)
		require.True(t, IsLocal(ip, false), s)
	}
	for _, s := range []string{"169.254.1.1", "fe80::1"} {
		ip := net.ParseIP(s)
		require.False(t, IsHome(ip), s+": link-local is not a home bind")
		require.True(t, IsLocal(ip, false), s+": link-local is local")
	}
	cg := net.ParseIP("100.64.3.9")
	require.False(t, IsHome(cg))
	require.False(t, IsLocal(cg, false), "CGNAT is not local by default")
	require.True(t, IsLocal(cg, true), "CGNAT is local when the owner added it")
	for _, s := range []string{"8.8.8.8", "0.0.0.0", "224.0.0.251"} {
		require.False(t, IsLocal(net.ParseIP(s), true), s)
		require.False(t, IsHome(net.ParseIP(s)), s)
	}
	require.False(t, IsHome(nil))
}

func TestHomeCIDRsIsTheHomeSetAndFresh(t *testing.T) {
	a, b := HomeCIDRs(), HomeCIDRs()
	require.Len(t, a, 6)
	a[0] = nil
	require.NotNil(t, b[0], "each call returns its own slice")
	in := func(s string) bool {
		for _, n := range HomeCIDRs() {
			if n.Contains(net.ParseIP(s)) {
				return true
			}
		}
		return false
	}
	for _, s := range []string{"127.0.0.1", "::1", "10.1.2.3", "172.20.0.1", "192.168.0.1", "fd00::1"} {
		require.True(t, in(s), s)
		require.True(t, IsHome(net.ParseIP(s)), s)
	}
	for _, s := range []string{"169.254.1.1", "100.64.0.1", "8.8.8.8"} {
		require.False(t, in(s), s)
	}
}
