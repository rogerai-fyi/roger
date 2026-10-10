package client

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// A name that resolves local when checked and public a moment later is never dialed at the public
// address: the dial uses the addresses the ONE lookup approved (features/join/adversarial.feature,
// "re-checked at connect time, as lanmode requires").
func TestALANNameThatFlipsToPublicIsNeverDialedThere(t *testing.T) {
	var calls atomic.Int32
	flip := func(context.Context, string) ([]net.IP, error) {
		if calls.Add(1) == 1 {
			return []net.IP{net.ParseIP("192.168.1.20")}, nil
		}
		return []net.IP{net.ParseIP("203.0.113.9")}, nil
	}
	base, why := lanTowerBase(context.Background(), ProxyOptions{LANResolve: flip}, "lab.home:8787")
	require.Empty(t, why)
	require.False(t, strings.Contains(base, "203.0.113.9"), "dialed the public address a second lookup returned: %s", base)
	require.Equal(t, int32(1), calls.Load(), "one lookup decides where the connection goes")
}
