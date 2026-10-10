package tui

import (
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/client"
)

// features/join/tower_identity.feature: "The dial marks pinned Towers as verified". A Tower pinned
// to its TLS key reads "verified" like an Edge member; a plain-http Tower stays "unverified".
func TestADialRowMarksAPinnedTowerVerified(t *testing.T) {
	SetLANDial(func() client.LANDial { return client.LANDial{} })
	t.Cleanup(func() { SetLANDial(nil) })

	pinned := client.LANSource{Kind: client.LANTower, ID: "192.168.1.20:8787", Verified: true}
	plain := client.LANSource{Kind: client.LANTower, ID: "192.168.1.30:8787"}

	require.Equal(t, "tower: 192.168.1.20 (verified)", lanSourceText(client.LANBand{Sources: []client.LANSource{pinned}}))
	require.Equal(t, "tower: 192.168.1.30 (unverified)", lanSourceText(client.LANBand{Sources: []client.LANSource{plain}}))

	require.Equal(t, "  verified", lanTrustTag(band{cheapest: &offer{LANSources: []client.LANSource{pinned}}}))
	require.Equal(t, "  unverified", lanTrustTag(band{cheapest: &offer{LANSources: []client.LANSource{plain}}}))
	require.Equal(t, "  unverified", lanTrustTag(band{cheapest: &offer{LANSources: []client.LANSource{pinned, plain}}}),
		"any plain-http source keeps the row unverified")
}
