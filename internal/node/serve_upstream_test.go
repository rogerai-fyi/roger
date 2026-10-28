package node

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// ServeUpstreamFor is what lets a peer on the Edge be relayed to the same backend the
// market uses (features/edge/local_inference.feature): an ON-AIR model resolves to its
// local chat URL and key; a model that is not on air is not servable to a peer, and the
// key is returned in-process only.
func TestServeUpstreamForOnlyOnAir(t *testing.T) {
	const secret = "sk-serve-key"
	c := New(Config{Broker: fakeBroker(t), Station: "amber-fox"})
	// A row that carries its OWN key is served with that key, in-process only. (The
	// headline key is never sprayed onto a row on a different endpoint - pickUpstreamKey.)
	c.SetRows([]ShareRow{{Model: "free-1", Ctx: 8192, Upstream: "http://127.0.0.1:0/v1/chat/completions", UpstreamKey: secret}})

	// Not on air: not servable.
	_, _, ok := c.ServeUpstreamFor("free-1")
	require.False(t, ok, "a model that is not on air may not be served to a peer")

	// On air: the local chat URL and the key.
	require.NoError(t, c.ToggleOnAir("free-1").Err)
	url, key, ok := c.ServeUpstreamFor("free-1")
	require.True(t, ok)
	require.Equal(t, "http://127.0.0.1:0/v1/chat/completions", url, "the row's own upstream, normalized")
	require.Equal(t, secret, key, "the key the backend needs, in-process only")

	// A model this node does not have is not servable.
	_, _, ok = c.ServeUpstreamFor("no-such-model")
	require.False(t, ok)

	// Taken off air: not servable again.
	c.ToggleOnAir("free-1")
	_, _, ok = c.ServeUpstreamFor("free-1")
	require.False(t, ok)
}
