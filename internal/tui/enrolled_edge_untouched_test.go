package tui

// The screens features/edge/enrolled_edge_look.feature §6 leaves alone, pinned byte for byte as
// they were drawn before the restyle (goldens written at 04c878ec, before any of it): the empty
// state, the setup wizard and its cabinet, and a node's detail page.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/store"
)

// untouchedFrames renders each screen the restyle must not move, by golden name.
func untouchedFrames(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	empty := nearbyGoldenModel(t, 100, false)
	out["enrolled_untouched_empty_100.golden"] = stripANSI(empty.edgeView(100))

	wiz := nearbyGoldenModel(t, 100, false)
	wiz.edge.setup = &edgeSetup{open: true, step: setupChoose, name: "shed"}

	f := newEnrolledLook(t)
	out["enrolled_untouched_wizard_100.golden"] = stripANSI(wiz.edgeView(100))

	for name, i := range map[string]int{"hub": 0, "member": 1, "nearby": 2} {
		d := f.model(t, 100, 0)
		d.edge.netSel, d.edge.netSelSet, d.edge.netDetail = i, true, true
		out["enrolled_untouched_detail_"+name+"_100.golden"] = stripANSI(d.edgeView(100))
	}
	return out
}

// untouchedMatches compares a frame with its BEFORE golden. These are never rewritten (not even
// under UPDATE_GOLDEN): they record how the screens were drawn before the restyle.
func untouchedMatches(name, got string) error {
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		return err
	}
	if string(want) != got {
		return fmt.Errorf("%s moved:\n%s\nwant:\n%s", name, got, want)
	}
	return nil
}

func TestEnrolledLookLeavesTheOtherScreensAlone(t *testing.T) {
	for name, got := range untouchedFrames(t) {
		require.NoError(t, untouchedMatches(name, got))
	}
}

// A row too wide for the terminal ends in an ellipsis, never a cut word: found on the simulated hub
// at 80x24, where the NO DEVICES YET line stopped at "to invite" (2026-10-13).
func TestAnEnrolledRowTooWideEndsInAnEllipsis(t *testing.T) {
	f := newEnrolledLook(t)
	f.cands = nil
	m := f.model(t, 80, 0)
	for _, ln := range strings.Split(stripANSI(m.edgeView(80)), "\n") {
		if strings.Contains(ln, "NO DEVICES YET") {
			require.True(t, strings.HasSuffix(ln, "…"), "cut without an ellipsis: %q", ln)
			return
		}
	}
	t.Fatal("no NO DEVICES YET row")
}

// A card keeps its alert count (enrolled_edge_look.feature: every card keeps today's words). Seen on
// the simulated hub: "MACHINE · Pi 5 · TROUBLE 23s · 1 alert" lost "1 alert" to the card's width.
// The hardware gives way first, and a card with no wire words beside it may use the row.
func TestACardKeepsItsAlertCount(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	n := store.EdgeNode{ID: "n_pi", Name: "pi-1", Kind: "host", Presence: "VERIFIED", LastSeen: now.Unix() - 23,
		Instances: []store.EdgeInstance{{Name: "pi-1", Software: &store.EdgeSoftware{Machine: "Raspberry Pi 5 Model B Rev 1.0"}}}}
	it := edgeNetItem{member: true, node: &n, id: n.ID, kind: n.Kind, name: n.Name, at: now, alerts: []string{"cpu hot"}}
	p := lookPalette(true)
	for _, legend := range []string{"", "┊ through a relay"} {
		rows := edgeLookCard(p, it, false, 96, legend)
		require.Contains(t, rows[1], "1 alert", "legend %q: %q", legend, rows[1])
	}
	// Quiet, the hardware has room again.
	it.alerts = nil
	require.Contains(t, edgeLookCard(p, it, false, 96, "┊ through a relay")[1], "Pi 5")
}
