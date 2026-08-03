package tower

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Phase 1.4: the local Station registry and routing.
// Contract: features/tower/modes.feature "Standalone mode routes only attached local
// Stations in v1".
//
// The shape of the guarantee: a standalone Tower routes to Stations IT admitted, issues
// a receipt stamped with its OWN network, and makes no claim of RogerAI verification.

func bootstrappedTower(t *testing.T) *State {
	t.Helper()
	st, _ := newBootstrapStore(t)
	inv, code, err := st.CreateInvitation(testClientKey, time.Hour, 5)
	require.NoError(t, err)
	_, err = st.ConsumeInvitation(inv.ID, code, testClientKey)
	require.NoError(t, err)
	return st
}

func TestAttachAndListAStation(t *testing.T) {
	st := bootstrappedTower(t)

	station, err := st.AttachStation("st-1", "station-key-1", []string{"llama-8b"})
	require.NoError(t, err)
	require.Equal(t, "st-1", station.ID)
	require.Equal(t, st.LocalNetworkID, station.NetworkID)

	list, err := st.Stations()
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "st-1", list[0].ID)
}

func TestAttachRequiresAnAdmittedOperator(t *testing.T) {
	st, _ := newBootstrapStore(t) // never bootstrapped
	_, err := st.AttachStation("st-1", "k", []string{"m"})
	require.Error(t, err, "a Station cannot attach before the network has an operator")
}

func TestAttachRejectsIncompleteStations(t *testing.T) {
	st := bootstrappedTower(t)
	for name, tc := range map[string]struct {
		id, key string
		models  []string
	}{
		"no id":     {"", "k", []string{"m"}},
		"no key":    {"st", "", []string{"m"}},
		"no models": {"st", "k", nil},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := st.AttachStation(tc.id, tc.key, tc.models)
			require.Error(t, err)
		})
	}
}

func TestReattachingTheSameStationUpdatesItRatherThanDuplicating(t *testing.T) {
	st := bootstrappedTower(t)
	_, err := st.AttachStation("st-1", "k1", []string{"a"})
	require.NoError(t, err)
	_, err = st.AttachStation("st-1", "k1", []string{"a", "b"})
	require.NoError(t, err)

	list, err := st.Stations()
	require.NoError(t, err)
	require.Len(t, list, 1, "re-attaching must not duplicate a Station")
	require.ElementsMatch(t, []string{"a", "b"}, list[0].Models)
}

// A Station cannot silently take over another's id with a different key.
func TestAStationCannotHijackAnothersID(t *testing.T) {
	st := bootstrappedTower(t)
	_, err := st.AttachStation("st-1", "key-one", []string{"a"})
	require.NoError(t, err)

	_, err = st.AttachStation("st-1", "key-two", []string{"a"})
	require.Error(t, err, "a different key must not reattach an existing Station id")
}

// --- routing ---------------------------------------------------------------

func TestRoutesToAnAttachedStationOfferingTheModel(t *testing.T) {
	st := bootstrappedTower(t)
	_, err := st.AttachStation("st-1", "k1", []string{"llama-8b"})
	require.NoError(t, err)

	rec, err := st.Route(testClientKey, "llama-8b")
	require.NoError(t, err)
	require.Equal(t, "st-1", rec.StationID)
	require.Equal(t, "llama-8b", rec.Model)
	require.Equal(t, st.LocalNetworkID, rec.NetworkID)
	require.NotEmpty(t, rec.RootFingerprint, "a local receipt pins the local trust history")
	require.NotEmpty(t, rec.RequestID)
}

// The receipt must be unmistakably LOCAL. It carries the standalone network id and says
// so, and it must never read as RogerAI-verified.
func TestLocalReceiptMakesNoRogerAIClaim(t *testing.T) {
	st := bootstrappedTower(t)
	_, err := st.AttachStation("st-1", "k1", []string{"m"})
	require.NoError(t, err)

	rec, err := st.Route(testClientKey, "m")
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(rec.NetworkID, "local-"))
	require.Contains(t, rec.String(), "local network")
	require.NotContains(t, strings.ToLower(rec.String()), "rogerai")
	require.NotContains(t, strings.ToLower(rec.String()), "verified by")
	require.Zero(t, rec.Cost, "standalone routing is free and locally accounted in v1")
}

func TestRoutingRefusesAnUnknownModel(t *testing.T) {
	st := bootstrappedTower(t)
	_, err := st.AttachStation("st-1", "k1", []string{"a"})
	require.NoError(t, err)

	_, err = st.Route(testClientKey, "not-offered")
	require.Error(t, err)
}

func TestRoutingRefusesWithNoStationsAttached(t *testing.T) {
	st := bootstrappedTower(t)
	_, err := st.Route(testClientKey, "anything")
	require.Error(t, err, "a standalone Tower routes only to Stations it admitted")
}

// Only the admitted local client may route. A standalone Tower is not an open relay.
func TestRoutingRefusesAnUnadmittedClient(t *testing.T) {
	st := bootstrappedTower(t)
	_, err := st.AttachStation("st-1", "k1", []string{"m"})
	require.NoError(t, err)

	_, err = st.Route("some-other-client", "m")
	require.Error(t, err, "an unadmitted client must not be able to route")
}

func TestEachRequestGetsItsOwnID(t *testing.T) {
	st := bootstrappedTower(t)
	_, err := st.AttachStation("st-1", "k1", []string{"m"})
	require.NoError(t, err)

	seen := map[string]bool{}
	for i := 0; i < 10; i++ {
		rec, err := st.Route(testClientKey, "m")
		require.NoError(t, err)
		require.False(t, seen[rec.RequestID], "request ids must not repeat")
		seen[rec.RequestID] = true
	}
}

// A joined Tower has no local registry: its Stations are admitted by Roger Core.
func TestJoinedModeHasNoLocalStationRegistry(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, ModeJoined)
	require.NoError(t, err)
	st, err := Open(dir)
	require.NoError(t, err)

	_, err = st.AttachStation("st-1", "k", []string{"m"})
	require.ErrorIs(t, err, ErrNotStandalone)
	_, err = st.Route("c", "m")
	require.ErrorIs(t, err, ErrNotStandalone)
	_, err = st.Stations()
	require.ErrorIs(t, err, ErrNotStandalone)
}
