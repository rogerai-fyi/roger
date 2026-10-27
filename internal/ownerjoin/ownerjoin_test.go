package ownerjoin

import (
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/tower"
)

var lab = Tower{Dir: "/srv/lab", Label: "lab", Addr: "192.168.1.20:8787", FP: "9f3c-0000-1111-2222"}

func TestARequestCarriesTheMatchingPhraseAndWhereItWaits(t *testing.T) {
	r := RequestFor(lab, tower.PairRequest{ID: "r1", Name: "laptop-x", Phrase: "amber-river-71"})
	require.Equal(t, Request{Dir: "/srv/lab", Tower: "lab", ID: "r1", Name: "laptop-x",
		Ask:  "laptop-x wants to join · amber-river-71",
		Hint: "approve only if laptop-x shows the same phrase"}, r)
	require.Equal(t, "laptop-x wants to join · amber-river-71 · y approves, n refuses", r.Card())
}

func TestTheNoticeNamesOneDeviceOrCountsSeveral(t *testing.T) {
	one := RequestFor(lab, tower.PairRequest{ID: "r1", Name: "laptop-x", Phrase: "a-b-c-01"})
	two := RequestFor(lab, tower.PairRequest{ID: "r2", Name: "pi-2", Phrase: "a-b-c-02"})
	for _, c := range []struct {
		reqs []Request
		want string
	}{
		{nil, ""},
		{[]Request{one}, "laptop-x wants to join lab: open [3] EDGE to answer"},
		{[]Request{one, two}, "2 devices want to join: open [3] EDGE to answer"},
	} {
		require.Equal(t, c.want, Notice(c.reqs))
	}
}

func TestTowersAndPinsAreListedWithTheirFingerprints(t *testing.T) {
	require.Equal(t, "Tower lab · 192.168.1.20:8787 · 9f3c-0000-1111-2222", TowerLine(lab))
	require.Equal(t, "192.168.1.30:8787 · pinned 1234-5678", PinnedLine(Pinned{Addr: "192.168.1.30:8787", FP: "1234-5678"}))
}

func TestRequestsAreAnsweredOnTheTowersThenTheEdge(t *testing.T) {
	edge := Tower{Dir: "edge:authority", Label: "shed"}
	h := &Hooks{}
	require.Equal(t, []Tower{lab}, h.Answering([]Tower{lab}))
	h.Edge = func() (Tower, bool) { return Tower{}, false }
	require.Equal(t, []Tower{lab}, h.Answering([]Tower{lab}), "an Edge that is not serving adds nothing")
	h.Edge = func() (Tower, bool) { return edge, true }
	towers := []Tower{lab}
	require.Equal(t, []Tower{lab, edge}, h.Answering(towers))
	require.Equal(t, []Tower{lab}, towers, "the served list itself is left alone")
}
