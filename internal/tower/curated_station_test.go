package tower

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// CURATED on the standalone plane (features/curated/curated_tower.feature): a Station
// attached as a curated proxy is LABELED apart everywhere the plane speaks - the attach
// registry, discovery, and the receipt - and stays free like everything local (the
// Core-free guarantee: a markup with no broker would be a toll collected by nobody).

func TestAttachCuratedStationCarriesTheLabel(t *testing.T) {
	st := bootstrappedTower(t)
	s, err := st.AttachCuratedStation("st-c", "key-c", []string{"gpt-4o"}, "openrouter")
	require.NoError(t, err)
	require.True(t, s.Curated)
	require.Equal(t, "openrouter", s.CuratedProvider)

	list, err := st.Stations()
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.True(t, list[0].Curated)
}

func TestAttachCuratedStationRequiresAProviderName(t *testing.T) {
	st := bootstrappedTower(t)
	for _, provider := range []string{"", "   ", "\x1b\x07"} { // empty after sanitation too
		_, err := st.AttachCuratedStation("st-c", "key-c", []string{"m"}, provider)
		require.Error(t, err, "an unnamed proxy is the exact ambiguity the label exists to remove")
	}
}

func TestCuratedProviderNameIsSanitizedAtAttach(t *testing.T) {
	// The provider name renders in discovery and receipts - the same display surface the
	// broker sanitizes at its register door. Control chars stripped, bounded.
	st := bootstrappedTower(t)
	s, err := st.AttachCuratedStation("st-c", "key-c", []string{"m"}, "open\x1b[2Jrouter"+strings.Repeat("x", 300))
	require.NoError(t, err)
	require.NotContains(t, s.CuratedProvider, "\x1b")
	require.LessOrEqual(t, len(s.CuratedProvider), 40)
}

func TestAStationCannotFlipBetweenHumanAndCurated(t *testing.T) {
	// The broker's kind-flip guard, mirrored: a callsign that earned trust as one kind
	// cannot re-attach as the other - that is a new thing wearing an earned identity.
	st := bootstrappedTower(t)
	_, err := st.AttachStation("st-1", "k1", []string{"m"})
	require.NoError(t, err)
	_, err = st.AttachCuratedStation("st-1", "k1", []string{"m"}, "openrouter")
	require.Error(t, err, "human -> curated re-attach must be refused")

	_, err = st.AttachCuratedStation("st-2", "k2", []string{"m"}, "openrouter")
	require.NoError(t, err)
	_, err = st.AttachStation("st-2", "k2", []string{"m"})
	require.Error(t, err, "curated -> human re-attach must be refused")
}

func TestACuratedReceiptIsLabeledAndFree(t *testing.T) {
	// Scenario: "The standalone plane never bills for curated" + "the answer is marked
	// local-and-curated". The receipt names the routing honestly and Cost is structurally 0.
	st := bootstrappedTower(t)
	_, err := st.AttachCuratedStation("st-c", "key-c", []string{"m"}, "openrouter")
	require.NoError(t, err)
	rec, err := st.RecordReceipt("client-hash", "st-c", "m")
	require.NoError(t, err)
	require.True(t, rec.Curated)
	require.Equal(t, "openrouter", rec.CuratedProvider)
	require.Zero(t, rec.Cost, "every curated answer on the local plane is free")
	require.Contains(t, rec.String(), "curated via openrouter")
	require.Contains(t, rec.String(), "free")

	// And a human station's receipt is unchanged.
	_, err = st.AttachStation("st-h", "key-h", []string{"m"})
	require.NoError(t, err)
	hrec, err := st.RecordReceipt("client-hash", "st-h", "m")
	require.NoError(t, err)
	require.False(t, hrec.Curated)
	require.NotContains(t, hrec.String(), "curated")
}
