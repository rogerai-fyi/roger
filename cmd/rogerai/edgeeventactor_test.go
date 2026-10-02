package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

// A maintenance request records WHO asked as the authenticated caller id (it had no id at all
// before), the sentence names the caller only, and once the caller is renamed the timeline says what
// it is called now. A caller no longer on the Edge adds nothing.
func TestAMaintenanceRequestKeepsWhoAskedAndTheTimelineNotesARename(t *testing.T) {
	newFacedAuthority(t, "desk")
	edgeSeed(t, store.EdgeNode{ID: "n_shed", Name: "shed", Kind: "board"}, store.EdgeNode{ID: "n_barn", Name: "barn", Kind: "board"})
	now := time.Now()
	edgeRecordMaintenance("n_shed", "shed", edge.MaintainTask{Action: edge.MaintainLogs}, now)
	edgeRecordMaintenance("n_barn", "barn", edge.MaintainTask{Action: edge.MaintainStop, Target: "watcher"}, now)

	st, err := loadEdgeState()
	require.NoError(t, err)
	self, ok, err := st.fleet.Get(edgeThisNodeID())
	require.NoError(t, err)
	require.True(t, ok)
	var got []store.EdgeEvent
	for _, e := range self.History {
		if e.What == "maintenance" {
			got = append(got, e)
		}
	}
	require.Len(t, got, 2)
	require.Equal(t, "n_shed", got[0].By)
	require.Equal(t, "logs, asked by shed", got[0].Detail)
	require.Equal(t, "n_barn", got[1].By)
	require.Equal(t, "stop watcher, asked by barn", got[1].Detail)

	// Renamed since: said as it was, with what it is called now. Forgotten since: nothing extra.
	out, code := edgeRun(t, "edge", "name", "shed", "potting-shed")
	require.Equal(t, 0, code, out)
	out, code = edgeRun(t, "edge", "forget", "barn", "--yes")
	require.Equal(t, 0, code, out)
	out, code = edgeRun(t, "edge", "events")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "logs, asked by shed (now potting-shed)")
	require.Contains(t, out, "stop watcher, asked by barn\n")
	require.NotContains(t, out, "n_shed")
	require.NotContains(t, out, "n_barn")
}
