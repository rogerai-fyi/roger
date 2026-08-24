package main

// Contract: features/tower/standalone_consumer_plane.feature
//
// THE STRUCTURAL GUARANTEE. "A standalone Tower never bridges to the Open Market" must be a
// property of the linkage, not of a runtime flag one bug could flip. This test inspects the
// binary's full dependency graph and fails if it links any Core-dialing package. The first
// draft trusted an `if Mode != joined` check inside a binary that LINKED the forwarding code;
// this binary does not contain that code at all, and this test is what keeps it that way.

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTheConsumerBinaryLinksNoCore(t *testing.T) {
	// The whole transitive dependency set of THIS binary.
	outb, err := exec.Command("go", "list", "-deps", ".").Output()
	require.NoError(t, err, "go list -deps must succeed")
	deps := strings.Split(string(outb), "\n")

	forbidden := []string{
		"rogerai.fm/roger/v6/internal/towerjoin",
		"rogerai.fm/roger/v6/internal/towercore", // catches every towercore/* subpackage
		"rogerai.fm/roger/v6/internal/towerhub",
	}
	for _, dep := range deps {
		dep = strings.TrimSpace(dep)
		if dep == "" {
			continue
		}
		for _, bad := range forbidden {
			require.False(t, dep == bad || strings.HasPrefix(dep, bad+"/"),
				"the standalone consumer binary links %s: it must import NONE of towerjoin, "+
					"towercore, or towerhub, so no code in it can reach Roger Core. If you need "+
					"one of those, it does not belong in this binary.", dep)
		}
	}
	require.Greater(t, len(deps), 20, "the dep scan must actually have enumerated the graph")
}
