package wire

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrivateInterfacesKeepsOnlyAllPrivateLinks(t *testing.T) {
	up := net.FlagUp | net.FlagMulticast
	ifaces := []net.Interface{
		{Index: 1, Name: "lan0", Flags: up},
		{Index: 2, Name: "wan0", Flags: up},
		{Index: 3, Name: "mixed0", Flags: up},
		{Index: 4, Name: "lo", Flags: up | net.FlagLoopback},
		{Index: 5, Name: "down0", Flags: net.FlagMulticast},
		{Index: 6, Name: "nomc0", Flags: net.FlagUp},
		{Index: 7, Name: "ll0", Flags: up},
	}
	addrs := map[string][]net.Addr{
		"lan0":   {&net.IPNet{IP: net.ParseIP("192.168.1.50")}, &net.IPNet{IP: net.ParseIP("fe80::1")}},
		"wan0":   {&net.IPNet{IP: net.ParseIP("203.0.113.7")}},
		"mixed0": {&net.IPNet{IP: net.ParseIP("10.0.0.5")}, &net.IPNet{IP: net.ParseIP("198.51.100.9")}},
		"lo":     {&net.IPNet{IP: net.ParseIP("127.0.0.1")}},
		"down0":  {&net.IPNet{IP: net.ParseIP("10.1.0.1")}},
		"nomc0":  {&net.IPNet{IP: net.ParseIP("10.2.0.1")}},
		"ll0":    {&net.IPNet{IP: net.ParseIP("fe80::2")}},
	}
	got := PrivateInterfaces(ifaces, func(i net.Interface) ([]net.Addr, error) { return addrs[i.Name], nil })
	require.Len(t, got, 1)
	require.Equal(t, "lan0", got[0].Name)
}
