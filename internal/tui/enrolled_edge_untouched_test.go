package tui

// The screens features/edge/enrolled_edge_look.feature §6 leaves alone, pinned byte for byte as
// they were drawn before the restyle (goldens written at 04c878ec, before any of it): the empty
// state, the setup wizard and its cabinet, and a node's detail page.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
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
