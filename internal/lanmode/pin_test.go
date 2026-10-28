package lanmode

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// features/join/tower_identity.feature: a pinned Tower is reached over TLS with a client pinned to
// its key; an unpinned one stays plain http for the transition release, unverified.
func TestRouteUsesThePinForAPinnedTower(t *testing.T) {
	t.Cleanup(func() { SetPins(nil) })
	fp := "9f3c0000000000000000000000000000000000000000000000000000000000e1"
	SetPins(map[string]string{"192.168.1.20:8787": "sha256:" + fp})
	plain := &http.Client{}

	base, hc, pinned := Route("http://192.168.1.20:8787", plain)
	require.Equal(t, "https://192.168.1.20:8787", base)
	require.True(t, pinned)
	require.NotSame(t, plain, hc)
	got, ok := PinFor("192.168.1.20:8787")
	require.True(t, ok)
	require.Equal(t, fp, got)

	base, hc, pinned = Route("http://192.168.1.21:8787", plain)
	require.Equal(t, "http://192.168.1.21:8787", base)
	require.False(t, pinned)
	require.Same(t, plain, hc)

	SetPins(map[string]string{"bad": "short"})
	_, ok = PinFor("bad")
	require.False(t, ok, "a pin that is not a full fingerprint is ignored")
}
