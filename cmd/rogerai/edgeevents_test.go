package main

// Executable pins for features/edge/events.feature, the CLI half.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/store"
)

func TestEdgeEventsListsTheTimelineAndNarrowsIt(t *testing.T) {
	offline(t)
	now := time.Now()
	edgeSeed(t,
		store.EdgeNode{ID: "n_desk", Name: "desk", Kind: "host", Presence: string(edge.PresenceVerified),
			History: []store.EdgeEvent{{At: now.Add(-3 * time.Hour).Unix(), What: "renamed", Detail: "was box"}}},
		store.EdgeNode{ID: "n_shed", Name: "shed", Kind: "host", Presence: string(edge.PresenceVerified),
			History: []store.EdgeEvent{{At: now.Add(-20 * time.Minute).Unix(), What: "tagged", Detail: "production"}}},
	)
	// A real crossing in the alert log, so the timeline merges both sources.
	hot := 95.0
	_, _, err := edgeAlertLog().Judge([]edge.NodeHealth{{ID: "n_desk", Name: "desk", Kind: "host",
		Presence: string(edge.PresenceVerified), LastHeard: now,
		Readings: &store.EdgeResources{CPUTempC: &hot, ReadAt: now.Unix()}}}, now)
	require.NoError(t, err)

	out, code := edgeRun(t, "edge", "events")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "EVENTS")
	require.Contains(t, out, "renamed")
	require.Contains(t, out, "tagged")
	require.Contains(t, out, "raised cpu_temp")
	require.Contains(t, out, "was box")

	out, code = edgeRun(t, "edge", "events", "--node", "shed")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "tagged")
	require.NotContains(t, out, "renamed")
	require.NotContains(t, out, "raised cpu_temp", "that crossing is desk's")

	out, code = edgeRun(t, "edge", "events", "--since", "1h")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "tagged")
	require.NotContains(t, out, "renamed", "older than the window")

	out, code = edgeRun(t, "edge", "events", "--limit", "1")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "EVENTS · 1,")

	out, code = edgeRun(t, "edge", "events", "--json")
	require.Equal(t, 0, code, out)
	var got struct {
		Events []edge.Event `json:"events"`
		Note   string       `json:"note"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.NotEmpty(t, got.Events)
	require.Equal(t, "", got.Note)
	for i := 1; i < len(got.Events); i++ {
		require.False(t, got.Events[i].At.After(got.Events[i-1].At), "newest first")
	}

	// Refusals.
	_, code = edgeRun(t, "edge", "events", "--since", "soon")
	require.NotEqual(t, 0, code)
	_, code = edgeRun(t, "edge", "events", "--limit", "many")
	require.NotEqual(t, 0, code)
	_, code = edgeRun(t, "edge", "events", "--node", "nosuch")
	require.NotEqual(t, 0, code)
}

// A run of identical events prints as one counted line (features/edge/events.feature).
func TestEdgeEventsCollapsesARun(t *testing.T) {
	offline(t)
	now := time.Now()
	var hist []store.EdgeEvent
	for i := 0; i < 12; i++ {
		hist = append(hist, store.EdgeEvent{At: now.Add(-time.Duration(i) * time.Minute).Unix(),
			What: "probe", Detail: "serve -> VERIFIED"})
	}
	edgeSeed(t, store.EdgeNode{ID: "n_desk", Name: "desk", Kind: "host",
		Presence: string(edge.PresenceVerified), History: hist})
	out, code := edgeRun(t, "edge", "events")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "EVENTS · 2,", "the collapsed run, and the enrolled line the seed writes")
	require.Contains(t, out, "×12, over 11m")
	require.Equal(t, 1, strings.Count(out, "serve -> VERIFIED"))
}

func TestEdgeEventsOnAnEdgeWithNothingRecorded(t *testing.T) {
	offline(t)
	out, code := edgeRun(t, "edge", "events")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "nothing has happened on this Edge yet")
}
