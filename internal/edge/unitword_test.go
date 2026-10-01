package edge_test

// features/edge/fleet_window.feature §2: heard and controllable are different, and every state word
// carries its age. One wording for the [3] cards, the rack and the console.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func TestEveryStateWordCarriesItsAge(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	lan := []store.EdgeTransport{{Kind: "lan", Addr: "192.168.1.9:8791", Fingerprint: "ab"}}
	unit := func(presence string, ago time.Duration, tr []store.EdgeTransport) store.EdgeNode {
		return store.EdgeNode{Presence: presence, LastSeen: now.Add(-ago).Unix(), Transports: tr}
	}
	cases := []struct {
		n      store.EdgeNode
		alerts int
		word   string
		line   string
	}{
		{unit(string(edge.PresenceVerified), 2*time.Second, lan), 0, edge.WordOnAir, "ON AIR now"},
		{unit(string(edge.PresenceVerified), 40*time.Second, lan), 0, edge.WordOnAir, "ON AIR 40s"},
		// Heard, but there is no face this machine can dial: what can be DONE to it differs.
		{unit(string(edge.PresenceVerified), 9*time.Second, nil), 0, edge.WordHeard, "HEARD 9s · no control path"},
		{unit(string(edge.PresenceVerified), 9*time.Second, []store.EdgeTransport{{Kind: "relay", Addr: "pi"}}), 0, edge.WordHeard, "HEARD 9s · no control path"},
		{unit(string(edge.PresenceVerified), 3*time.Second, lan), 2, edge.WordTrouble, "TROUBLE now"},
		{unit(string(edge.PresenceDark), 50*time.Hour, lan), 1, edge.WordDark, "DARK 2d"},
		{unit(string(edge.PresenceReenroll), time.Hour, lan), 0, edge.WordReenroll, "RE-ENROLL 1h"},
	}
	for _, c := range cases {
		w, line := edge.UnitWord(c.n, c.alerts, now)
		require.Equal(t, c.word, w, c.line)
		require.Equal(t, c.line, line)
	}
	// Never heard at all: the word has no age to carry, and says so rather than inventing one.
	w, line := edge.UnitWord(store.EdgeNode{Presence: string(edge.PresenceVerified)}, 0, now)
	require.Equal(t, edge.WordHeard, w)
	require.Equal(t, "HEARD · never directly", line)
	require.True(t, edge.Controllable(unit("", 0, lan)))
	require.False(t, edge.Controllable(unit("", 0, []store.EdgeTransport{{Kind: "lan", Addr: "x"}})), "a LAN face with no pin is not dialled")
}

// features/edge/fleet_window.feature §1: a unit nobody named is called by what it is - its hardware
// word, else its kind word - with a short suffix only when two units would otherwise read the same.
func TestAUnitNobodyNamedIsCalledByWhatItIs(t *testing.T) {
	pi := func(id string) store.EdgeNode {
		return store.EdgeNode{ID: id, Name: id, Kind: "host", Instances: []store.EdgeInstance{{Name: "r",
			Software: &store.EdgeSoftware{Machine: "Raspberry Pi 4 Model B Rev 1.5"}}}}
	}
	phone := store.EdgeNode{ID: "n_cccccccccccccccccccccccccccccccccccccccc", Name: "n_cccccccccccccccccccccccccccccccccccccccc", Kind: "mobile"}
	named := store.EdgeNode{ID: "n_dddd", Name: "greenhouse", Kind: "host"}
	a, b := pi("n_aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111"), pi("n_bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222")
	all := []store.EdgeNode{a, b, phone, named}

	require.Equal(t, "greenhouse", edge.UnitName(named, all), "an owner's name always wins")
	require.Equal(t, "phone", edge.UnitName(phone, all), "a lone unnamed phone is the phone")
	require.Equal(t, "Pi 4 aaaa", edge.UnitName(a, all), "two unnamed Pi 4s are told apart by a short suffix")
	require.Equal(t, "Pi 4 bbbb", edge.UnitName(b, all))
	require.Equal(t, "Pi 4", edge.UnitName(a, []store.EdgeNode{a, phone}), "and only when they would read the same")
	for _, n := range all {
		require.False(t, edge.LooksLikeID(edge.UnitName(n, all)), "%q", edge.UnitName(n, all))
	}
	// Nothing known at all: still not an id.
	bare := store.EdgeNode{ID: "n_eeee", Name: "n_eeee"}
	require.Equal(t, "device", edge.UnitName(bare, []store.EdgeNode{bare}))
}

// features/edge/fleet_window.feature §8: a phone the system put in the background is ASLEEP, not
// broken, and its vitals read in words with absent readings left out.
func TestAPhoneInTheBackgroundIsAsleepNotBroken(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	no, yes, b := false, true, 64
	phone := store.EdgeNode{Kind: "mobile", Presence: string(edge.PresenceVerified), LastSeen: now.Add(-3 * time.Minute).Unix(),
		Phone: &store.EdgePhone{Foreground: &no}}
	w, line := edge.UnitWord(phone, 0, now)
	require.Equal(t, edge.WordAsleep, w)
	require.Equal(t, "ASLEEP 3m", line)
	phone.Phone.Foreground = &yes
	w, _ = edge.UnitWord(phone, 0, now)
	require.Equal(t, edge.WordHeard, w, "in front, with no face to dial, it is heard")
	phone.Presence = string(edge.PresenceDark)
	phone.Phone.Foreground = &no
	w, _ = edge.UnitWord(phone, 0, now)
	require.Equal(t, edge.WordDark, w, "past the liveness window it is dark, whatever it last said")

	require.Equal(t, "battery 64% · charging · thermal fair · app in front",
		edge.PhoneLine(&store.EdgePhone{BatteryPct: &b, Charging: &yes, Thermal: "fair", Foreground: &yes}))
	require.Equal(t, "app in the background", edge.PhoneLine(&store.EdgePhone{Foreground: &no}))
	require.Equal(t, "battery 64% · on battery", edge.PhoneLine(&store.EdgePhone{BatteryPct: &b, Charging: &no}))
	require.Empty(t, edge.PhoneLine(nil))
	require.Empty(t, edge.PhoneLine(&store.EdgePhone{}))
}
