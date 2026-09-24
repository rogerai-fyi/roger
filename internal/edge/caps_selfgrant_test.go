package edge

import (
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/store"
)

// A node must NOT be able to promote its own capabilities by asserting VERIFIED in the household it
// reports. Only a probe passed here (RecordInstanceProbe) or an owner confirmation earns VERIFIED;
// a self-declared state is clamped to the initial state, so nothing routes on a lie (audit 2026-09-24).
func TestANodeCannotSelfGrantVerifiedCaps(t *testing.T) {
	f := NewFleet(store.NewMem(), "acct-1")
	id := "n_selfgrant000000000000000000000000000000000000"

	// The node reports an instance that CLAIMS serve is already VERIFIED, and even that it may
	// actuate the physical world - both unearned.
	_, err := f.Observe(id, Observation{
		Name: "workshop", Kind: "host", Addr: "192.168.1.9:8443",
		Instances: []Instance{{Name: "roger", Caps: []store.EdgeCap{
			{Name: string(Serve), State: string(Verified)},
			{Name: string(Actuate), State: string(Verified)},
		}}},
	})
	require.NoError(t, err)

	n, ok, err := f.Get(id)
	require.NoError(t, err)
	require.True(t, ok)

	// Neither routes: the self-asserted VERIFIED was clamped.
	require.False(t, Routable(n, Serve), "a self-declared serve must not route")
	require.False(t, Routable(n, Actuate), "a self-declared actuate must not route")
	for _, c := range n.Caps {
		if c.Name == string(Serve) {
			require.Equal(t, string(Claimed), c.State, "serve starts CLAIMED until a probe passes")
		}
		if c.Name == string(Actuate) {
			require.Equal(t, string(PendingConfirmation), c.State, "actuate waits on the owner")
		}
	}

	// A REAL probe promotes serve, and only then does it route.
	require.NoError(t, f.RecordInstanceProbe(id, "roger", Serve, true))
	n, _, err = f.Get(id)
	require.NoError(t, err)
	require.True(t, Routable(n, Serve), "a probed serve routes")

	// Even after earning it, a later self-report cannot lift actuate.
	_, err = f.Observe(id, Observation{
		Name: "workshop", Kind: "host", Addr: "192.168.1.9:8443",
		Instances: []Instance{{Name: "roger", Caps: []store.EdgeCap{
			{Name: string(Actuate), State: string(Verified)},
		}}},
	})
	require.NoError(t, err)
	n, _, _ = f.Get(id)
	require.False(t, Routable(n, Actuate), "actuate still needs the owner after a re-report")
}
