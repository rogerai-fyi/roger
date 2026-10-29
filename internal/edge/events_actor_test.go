package edge_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/store"
)

// A remote request is recorded with WHO asked as a structured field (the authenticated node id),
// and the sentence carries only the name. Names change and can be reused, so where the asker has
// been renamed since, the line says what it is called now; an asker that has left adds nothing.
func TestEventsSayWhoAskedByNameAndNoteARename(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	desk := store.EdgeNode{ID: "n_desk", Name: "desk", Kind: "host", History: []store.EdgeEvent{
		{At: 10, What: "model", Detail: "load qwen, asked by attic", By: "n_attic"},
		{At: 20, What: "maintenance", Detail: "start fixer, asked by shed", By: "n_shed"},
		{At: 30, What: "model", Detail: "unload qwen, asked by old-box", By: "n_gone"},
		{At: 40, What: "model", Detail: "load qwen, asked by Pi 4", By: "n_0f0f0f0f0f0f0f0f0f0f0f0f"},
		{At: 50, What: "renamed", Detail: "a -> b"},
	}}
	nodes := []store.EdgeNode{desk,
		{ID: "n_attic", Name: "attic", Kind: "board"},
		{ID: "n_shed", Name: "barn", Kind: "board"},
		{ID: "n_0f0f0f0f0f0f0f0f0f0f0f0f", Name: "n_0f0f0f0f0f0f0f0f0f0f0f0f", Kind: "host", Instances: []store.EdgeInstance{
			{Name: "r", Software: &store.EdgeSoftware{Machine: "Raspberry Pi 4"}}}},
	}
	evs := edge.Events(nodes, nil, edge.EventQuery{NodeID: "n_desk"}, now)
	got := map[int64]edge.Event{}
	for _, e := range evs {
		got[e.At.Unix()] = e
	}
	require.Equal(t, "load qwen, asked by attic", got[10].Detail, "same name: nothing extra")
	require.Equal(t, "n_attic", got[10].By)
	require.Equal(t, "start fixer, asked by shed (now barn)", got[20].Detail)
	require.Equal(t, "unload qwen, asked by old-box", got[30].Detail, "no longer on the Edge: nothing extra")
	require.Equal(t, "load qwen, asked by Pi 4", got[40].Detail, "an unnamed asker is its hardware word, still the same")
	require.Equal(t, "a -> b", got[50].Detail)
	require.Empty(t, got[50].By)

	require.Equal(t, "start fixer, asked by shed (now barn)", edge.ActorDetail(desk.History[1], nodes))
	require.Equal(t, "load qwen, asked by attic", edge.ActorDetail(desk.History[0], nodes))
	require.Equal(t, "a -> b", edge.ActorDetail(desk.History[4], nodes), "an event nobody asked for is its sentence")
}

func TestRecordEventByKeepsTheActor(t *testing.T) {
	f := edge.NewFleet(store.NewMem(), "acct")
	f.SetClock(func() time.Time { return time.Unix(77, 0) })
	_, err := f.Enroll(store.EdgeNode{ID: "n_desk", Name: "desk", Kind: "host"})
	require.NoError(t, err)
	require.NoError(t, f.RecordEventBy("n_desk", "model", "load qwen, asked by attic", "n_attic"))
	n, _, err := f.Get("n_desk")
	require.NoError(t, err)
	last := n.History[len(n.History)-1]
	require.Equal(t, store.EdgeEvent{At: 77, What: "model", Detail: "load qwen, asked by attic", By: "n_attic"}, last)
	require.ErrorIs(t, f.RecordEventBy("n_nope", "model", "x", "n_attic"), edge.ErrNoSuchNode)
}
