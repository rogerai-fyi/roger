package admit

// EventsOfKinds reads a Tower's events of some kinds after a time, oldest first, so a reader
// is never limited to a window of the newest rows (the trust stage, tower_trust_ramp.feature).

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEventsOfKindsReadsByKindAndTimeNotByWindow(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		r := NewWithStore(Config{}, s)
		base := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
		require.NoError(t, r.Record(Event{TowerID: "tw-1", At: base, Kind: "incident", Actor: "admin:broker-key", Reason: "old"}))
		for i := 0; i < 600; i++ { // far more than any window
			require.NoError(t, r.Record(Event{TowerID: "tw-1", At: base.Add(time.Duration(i+1) * time.Second), Kind: "station_placed", Actor: ActorSystem}))
		}
		require.NoError(t, r.Record(Event{TowerID: "tw-1", At: base.Add(20 * time.Minute), Kind: "incident", Actor: "admin:broker-key", Reason: "new"}))
		require.NoError(t, r.Record(Event{TowerID: "tw-2", At: base.Add(20 * time.Minute), Kind: "incident", Actor: "admin:broker-key"}))

		evs, err := r.EventsOfKinds("tw-1", []string{"incident"}, time.Time{}, 100)
		require.NoError(t, err)
		require.Len(t, evs, 2)
		require.Equal(t, "old", evs[0].Reason, "oldest first")
		require.Equal(t, "new", evs[1].Reason)

		evs, err = r.EventsOfKinds("tw-1", []string{"incident"}, base, 100)
		require.NoError(t, err)
		require.Len(t, evs, 1, "strictly after since")
		require.Equal(t, "new", evs[0].Reason)

		evs, err = r.EventsOfKinds("tw-1", []string{"incident", "station_placed"}, base, 5)
		require.NoError(t, err)
		require.Len(t, evs, 5, "limit applies")
	})
}

// Only lifecycle moves INTO given states are read, so a Tower's own drain/resume toggling can
// never fill a bounded read and hide a suspension (review finding).
func TestEventsMovingToReadsOnlyThoseStates(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		r := NewWithStore(Config{}, s)
		base := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
		for i := 0; i < 50; i++ {
			to := StateDraining
			if i%2 == 1 {
				to = StateActive
			}
			require.NoError(t, r.Record(Event{TowerID: "tw-1", At: base.Add(time.Duration(i) * time.Second), Kind: "lifecycle", Actor: "operator:o", To: to}))
		}
		require.NoError(t, r.Record(Event{TowerID: "tw-1", At: base.Add(time.Minute * 10), Kind: "lifecycle", Actor: ActorSystem, To: StateSuspended}))
		evs, err := r.EventsMovingTo("tw-1", "lifecycle", []State{StateSuspended, StateRevoked}, time.Time{}, 5)
		require.NoError(t, err)
		require.Len(t, evs, 1)
		require.Equal(t, StateSuspended, evs[0].To)
	})
}
