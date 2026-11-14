package link

// A Tower's measured locality rides its live session and the shared mirror, so every broker
// instance reads the same value. Contract: features/tower/placement_locality.feature ("A
// Tower's location comes from its hub address, resolved by Core", "A Tower's location is
// recomputed on every new link session and not in between").

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/towercore/placement"
)

func TestATowersLocalityIsSharedThroughTheMirror(t *testing.T) {
	m := NewMemMirror()
	a, b := New(mirrorCfg(m)), New(mirrorCfg(m))
	sess := openOn(t, a, "tw-1", "hub.example:7443")

	_, ok := a.Locality("tw-1")
	require.False(t, ok, "nothing is known until Core has resolved the hub")

	loc := placement.Locality{Bucket: "203.0.113.0/24", Continent: "NA"}
	require.True(t, a.SetLocality("tw-1", sess, loc))

	got, ok := a.Locality("tw-1")
	require.True(t, ok)
	require.Equal(t, loc, got)
	got, ok = b.Locality("tw-1")
	require.True(t, ok, "a peer instance reads it from the mirror")
	require.Equal(t, loc, got)

	// A heartbeat re-puts the record whole and keeps the locality.
	require.NoError(t, a.Heartbeat(sess, "tw-1"))
	got, _ = b.Locality("tw-1")
	require.Equal(t, loc, got)

	// An instance that adopts the session carries it too, and its own heartbeats keep it.
	require.NoError(t, b.Heartbeat(sess, "tw-1"))
	m.FailForTest(true)
	got, ok = b.Locality("tw-1")
	m.FailForTest(false)
	require.True(t, ok, "the adopted session holds it locally")
	require.Equal(t, loc, got)
}

func TestANewSessionStartsWithNoLocality(t *testing.T) {
	m := NewMemMirror()
	a := New(mirrorCfg(m))
	sess := openOn(t, a, "tw-1", "hub.example:7443")
	require.True(t, a.SetLocality("tw-1", sess, placement.Locality{Continent: "NA"}))

	openOn(t, a, "tw-1", "hub.example:7443")
	_, ok := a.Locality("tw-1")
	require.False(t, ok, "no value from the old session survives a new one")

	// A late answer for the superseded session is not written.
	require.False(t, a.SetLocality("tw-1", sess, placement.Locality{Continent: "NA"}))
	_, ok = a.Locality("tw-1")
	require.False(t, ok)
}

func TestLocalityWithoutAMirror(t *testing.T) {
	a := New(mirrorCfg(nil))
	sess := openOn(t, a, "tw-1", "hub.example:7443")
	require.False(t, a.SetLocality("tw-x", sess, placement.Locality{Continent: "NA"}), "no such tower")
	require.True(t, a.SetLocality("tw-1", sess, placement.Locality{Continent: "EU"}))
	got, ok := a.Locality("tw-1")
	require.True(t, ok)
	require.Equal(t, "EU", got.Continent)
	_, ok = a.Locality("tw-none")
	require.False(t, ok)
}

func TestAnUnknownLocalityIsKnownToBeUnknown(t *testing.T) {
	// Resolving and finding nothing is an answer: it is stored, so the sweep does not retry it.
	a := New(mirrorCfg(NewMemMirror()))
	sess := openOn(t, a, "tw-1", "hub.example:7443")
	require.True(t, a.SetLocality("tw-1", sess, placement.Locality{}))
	got, ok := a.Locality("tw-1")
	require.True(t, ok)
	require.Equal(t, placement.Locality{}, got)
}

func TestPGMirrorRoundTripsLocality(t *testing.T) {
	m := pgMirror(t)
	a := New(mirrorCfg(m))
	sess := openOn(t, a, "tw-1", "hub.example:7443")
	loc := placement.Locality{Bucket: "2001:db8::/32", Continent: "EU"}
	require.True(t, a.SetLocality("tw-1", sess, loc))
	rec, ok, err := m.Get("tw-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, loc, rec.Locality)
	require.True(t, rec.LocalityKnown)
	all, err := m.All()
	require.NoError(t, err)
	require.Equal(t, loc, all["tw-1"].Locality)
	require.True(t, all["tw-1"].LocalityKnown)
}

func TestHoldsLocallyIsOnlyThisInstancesOwnFreshSession(t *testing.T) {
	m := NewMemMirror()
	a, b := New(mirrorCfg(m)), New(mirrorCfg(m))
	openOn(t, a, "tw-1", "hub.example:7443")
	require.True(t, a.HoldsLocally("tw-1"))
	require.False(t, b.HoldsLocally("tw-1"), "a peer's session is not held here")
	require.False(t, a.HoldsLocally("tw-none"))
	a.advance(time.Hour)
	require.False(t, a.HoldsLocally("tw-1"), "a stale session is not held")
}

func TestLiveTowersCheckedReportsAMirrorFailure(t *testing.T) {
	m := NewMemMirror()
	a, b := New(mirrorCfg(m)), New(mirrorCfg(m))
	openOn(t, a, "tw-1", "")
	openOn(t, b, "tw-2", "")
	got, err := a.LiveTowersChecked()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"tw-1", "tw-2"}, got)
	m.FailForTest(true)
	got, err = a.LiveTowersChecked()
	require.ErrorIs(t, err, ErrMirrorDown)
	require.Equal(t, []string{"tw-1"}, got, "the local set, and the error")
	got, err = New(mirrorCfg(nil)).LiveTowersChecked()
	require.NoError(t, err)
	require.Empty(t, got)
}
