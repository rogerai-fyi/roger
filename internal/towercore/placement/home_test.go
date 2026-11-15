package placement

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// private_hub_placement.feature: "same network" is the same public IPv4 address, or the same
// IPv6 /64. Mixed families never match, and a local address has no home key at all.
func TestTheSameNetworkKey(t *testing.T) {
	for _, tc := range []struct{ addr, want string }{
		{"198.51.100.40", "198.51.100.40"},
		{"2001:db8:1:2::40", "2001:db8:1:2::/64"},
		{"2001:db8:1:2:aaaa:bbbb:cccc:dddd", "2001:db8:1:2::/64"},
		{"::ffff:198.51.100.40", "198.51.100.40"},
		{"192.168.1.20", ""},
		{"10.0.0.5", ""},
		{"fd12:3456::20", ""},
		{"127.0.0.1", ""},
		{"::1", ""},
		{"100.64.1.9", ""},
		{"fe80::1", ""},
		{"not-an-ip", ""},
		{"", ""},
	} {
		require.Equal(t, tc.want, HomeKey(tc.addr), tc.addr)
	}
}

func TestSameHomeMatchesTheSpecTable(t *testing.T) {
	for _, tc := range []struct {
		tower, station string
		same           bool
	}{
		{"198.51.100.40", "198.51.100.40", true},
		{"198.51.100.40", "198.51.100.41", false},
		{"2001:db8:1:2::40", "2001:db8:1:2::99", true},
		{"2001:db8:1:2::40", "2001:db8:1:3::99", false},
		{"198.51.100.40", "2001:db8:1:2::99", false},
		{"2001:db8:1:2::40", "198.51.100.40", false},
		{"192.168.1.2", "192.168.1.2", false}, // a local address is never "the same network"
	} {
		tw := Locality{Home: HomeKey(tc.tower)}
		st := Locality{Home: HomeKey(tc.station)}
		require.Equal(t, tc.same, SameHome(st, tw), "%s vs %s", tc.tower, tc.station)
	}
}

func TestFromAddrCarriesTheHomeKey(t *testing.T) {
	bucket := func(a string) string { return "b:" + a }
	cont := func(string) string { return "NA" }
	require.Equal(t, Locality{Bucket: "b:198.51.100.40", Continent: "NA", Home: "198.51.100.40"},
		FromAddr("198.51.100.40", bucket, cont))
	require.Equal(t, Locality{}, FromAddr("192.168.1.2", func(string) string { return "local" }, cont))
}

// private_hub_placement.feature, "How Core classes the hub address a Tower advertises".
func TestHubClassOf(t *testing.T) {
	for _, tc := range []struct {
		addrs []string
		want  HubClass
	}{
		{[]string{"203.0.113.7"}, HubPublic},
		{[]string{"192.168.1.20"}, HubPrivate},
		{[]string{"10.0.0.5"}, HubPrivate},
		{[]string{"172.16.4.2"}, HubPrivate},
		{[]string{"fd12:3456::20"}, HubPrivate},
		{[]string{"100.64.1.9"}, HubPrivate},
		{[]string{"127.0.0.1"}, HubLoopback},
		{[]string{"::1"}, HubLoopback},
		{nil, HubNone},
		// A name that resolves to several addresses: any public one makes the hub public (Core
		// can dial it from outside); private and loopback together cannot be handed to anyone
		// safely, so they are loopback; unusable addresses are loopback too.
		{[]string{"192.168.1.20", "203.0.113.7"}, HubPublic},
		{[]string{"192.168.1.20", "10.0.0.5"}, HubPrivate},
		{[]string{"192.168.1.20", "127.0.0.1"}, HubLoopback},
		{[]string{"fe80::1"}, HubLoopback},
		{[]string{"0.0.0.0"}, HubLoopback},
		{[]string{"garbage"}, HubNone},
	} {
		require.Equal(t, tc.want, HubClassOf(tc.addrs), "%v", tc.addrs)
	}
	require.Equal(t, "public", HubPublic.String())
	require.Equal(t, "private", HubPrivate.String())
	require.Equal(t, "loopback", HubLoopback.String())
	require.Equal(t, "none", HubNone.String())
}

func TestFromAddrsKeepsTheHomeKeyOnlyWhenEveryAddressAgrees(t *testing.T) {
	bucket := func(string) string { return "b" }
	cont := func(string) string { return "NA" }
	one, _ := FromAddrs([]string{"198.51.100.40", "198.51.100.40"}, "", bucket, cont)
	require.Equal(t, "198.51.100.40", one.Home)
	two, _ := FromAddrs([]string{"198.51.100.40", "198.51.100.41"}, "", bucket, cont)
	require.Equal(t, "", two.Home)
}
