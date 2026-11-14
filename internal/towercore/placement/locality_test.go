package placement

// Contract: features/tower/placement_locality.feature ("The locality class between a Station
// and a Tower", "No table configured means locality never helps or hurts anyone").

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTheLocalityClassBetweenAStationAndATower(t *testing.T) {
	for _, tc := range []struct {
		sb, sc, tb, tc string
		want           Class
	}{
		{"203.0.113.0/24", "NA", "203.0.113.0/24", "NA", SameNetwork},
		{"203.0.113.0/24", "", "203.0.113.0/24", "", SameNetwork},
		{"203.0.113.0/24", "NA", "198.51.100.0/24", "NA", SameContinent},
		{"203.0.113.0/24", "NA", "198.51.100.0/24", "AF", Far},
		{"203.0.113.0/24", "", "198.51.100.0/24", "AF", Unknown},
		{"203.0.113.0/24", "NA", "198.51.100.0/24", "", Unknown},
		{"", "", "", "", Unknown},
		{"2001:db8::/32", "EU", "2001:db8::/32", "EU", SameNetwork},
		{"2001:db8::/32", "EU", "203.0.113.0/24", "EU", SameContinent},
		// An empty bucket never matches another empty bucket: two unknowns are not neighbours.
		{"", "NA", "", "NA", SameContinent},
	} {
		got := ClassOf(Locality{Bucket: tc.sb, Continent: tc.sc}, Locality{Bucket: tc.tb, Continent: tc.tc})
		require.Equal(t, tc.want, got, "%+v", tc)
	}
}

func TestClassesRankBestFirst(t *testing.T) {
	require.True(t, SameNetwork.Better(SameContinent))
	require.True(t, SameContinent.Better(Unknown))
	require.True(t, Unknown.Better(Far))
	require.False(t, Far.Better(Unknown))
	require.False(t, SameNetwork.Better(SameNetwork))
	for c, name := range map[Class]string{SameNetwork: "same_network", SameContinent: "same_continent", Unknown: "unknown", Far: "far"} {
		require.Equal(t, name, c.String())
	}
}

func TestATowersLocalityFromItsHubAddresses(t *testing.T) {
	cont := func(m map[string]string) func(string) string { return func(a string) string { return m[a] } }
	bucket := func(m map[string]string) func(string) string { return func(a string) string { return m[a] } }

	// One address: its bucket and continent.
	l, conflict := FromAddrs([]string{"a"}, "", bucket(map[string]string{"a": "B1"}), cont(map[string]string{"a": "NA"}))
	require.Equal(t, Locality{Bucket: "B1", Continent: "NA"}, l)
	require.False(t, conflict)

	// Several addresses, one bucket: that bucket.
	l, _ = FromAddrs([]string{"a", "b"}, "", bucket(map[string]string{"a": "B1", "b": "B1"}), cont(map[string]string{"a": "NA", "b": "NA"}))
	require.Equal(t, Locality{Bucket: "B1", Continent: "NA"}, l)

	// Buckets differ within one continent: the continent, no bucket.
	l, _ = FromAddrs([]string{"a", "b"}, "", bucket(map[string]string{"a": "B1", "b": "B2"}), cont(map[string]string{"a": "NA", "b": "NA"}))
	require.Equal(t, Locality{Continent: "NA"}, l)

	// Across continents: unknown.
	l, _ = FromAddrs([]string{"a", "b"}, "", bucket(map[string]string{"a": "B1", "b": "B2"}), cont(map[string]string{"a": "NA", "b": "EU"}))
	require.Equal(t, Locality{}, l)

	// One address in the table and one not: the continent is not known for all of them.
	l, _ = FromAddrs([]string{"a", "b"}, "", bucket(map[string]string{"a": "B1", "b": "B2"}), cont(map[string]string{"a": "NA"}))
	require.Equal(t, Locality{}, l)

	// "local" (loopback, private, ULA, link-local, unspecified) says nothing about place.
	l, _ = FromAddrs([]string{"a"}, "", bucket(map[string]string{"a": "local"}), cont(map[string]string{"a": "NA"}))
	require.Equal(t, Locality{}, l)

	// Nothing resolved.
	l, _ = FromAddrs(nil, "", bucket(nil), cont(nil))
	require.Equal(t, Locality{}, l)

	// The link's source continent disagrees with the hub's: unknown, and a conflict.
	l, conflict = FromAddrs([]string{"a"}, "EU", bucket(map[string]string{"a": "B1"}), cont(map[string]string{"a": "NA"}))
	require.Equal(t, Locality{}, l)
	require.True(t, conflict)

	// The link's source continent agrees, or is unknown: no conflict.
	l, conflict = FromAddrs([]string{"a"}, "NA", bucket(map[string]string{"a": "B1"}), cont(map[string]string{"a": "NA"}))
	require.Equal(t, Locality{Bucket: "B1", Continent: "NA"}, l)
	require.False(t, conflict)
}

func TestAStationsLocalityFromItsAttachAddress(t *testing.T) {
	require.Equal(t, Locality{Bucket: "B1", Continent: "NA"}, FromAddr("a", func(string) string { return "B1" }, func(string) string { return "NA" }))
	require.Equal(t, Locality{}, FromAddr("a", func(string) string { return "local" }, func(string) string { return "NA" }))
	require.Equal(t, Locality{}, FromAddr("a", func(string) string { return "" }, func(string) string { return "" }))
}
