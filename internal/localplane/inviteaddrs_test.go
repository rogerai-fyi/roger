package localplane

import (
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInviteAddrs(t *testing.T) {
	home := func() ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
			&net.IPNet{IP: net.ParseIP("192.168.1.20"), Mask: net.CIDRMask(24, 32)},
			&net.IPNet{IP: net.ParseIP("8.8.8.8"), Mask: net.CIDRMask(24, 32)},
		}, nil
	}
	none := func() ([]net.Addr, error) { return nil, nil }

	got, lo, err := InviteAddrs("192.168.1.20:8787", home)
	require.NoError(t, err)
	require.Equal(t, []string{"192.168.1.20:8787"}, got)
	require.False(t, lo)

	got, lo, err = InviteAddrs("127.0.0.1:8787", home)
	require.NoError(t, err)
	require.Equal(t, []string{"127.0.0.1:8787"}, got)
	require.True(t, lo, "a loopback bind is loopback-only")

	got, lo, err = InviteAddrs("0.0.0.0:8787", home)
	require.NoError(t, err)
	require.Equal(t, []string{"192.168.1.20:8787"}, got, "an all-interfaces bind expands to home addresses only, never loopback or public")
	require.False(t, lo)

	got, lo, err = InviteAddrs(":9000", none)
	require.NoError(t, err)
	require.Equal(t, []string{"127.0.0.1:9000"}, got, "no home address falls back to loopback")
	require.True(t, lo)

	_, _, err = InviteAddrs("nonsense", home)
	require.Error(t, err)

	_, _, err = InviteAddrs(":8787", func() ([]net.Addr, error) { return nil, errors.New("boom") })
	require.Error(t, err)
}
