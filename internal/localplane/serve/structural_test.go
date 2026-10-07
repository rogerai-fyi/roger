package serve

// Contract: features/packaging/one_binary.feature section 4 (supersedes the binary-level
// "by linkage" proof in features/tower/standalone_consumer_plane.feature).
//
// THE PACKAGE-LEVEL GUARANTEE. The standalone serve package's whole dependency graph links none
// of the packages that reach Roger Core, nor the broker client or the share agent; and its own
// source makes no outbound call (it may listen: that is its job). The runtime guard
// (internal/egressguard) covers the rest of the process.

import (
	"testing"

	"rogerai.fm/roger/v6/internal/structural"
)

func TestTheStandaloneServePackageLinksNoCore(t *testing.T) {
	structural.ForbidDeps(t, ".", []string{
		"rogerai.fm/roger/v6/internal/towerjoin",
		"rogerai.fm/roger/v6/internal/towercore", // every towercore/* subpackage
		"rogerai.fm/roger/v6/internal/towerhub",
		"rogerai.fm/roger/v6/internal/client",
		"rogerai.fm/roger/v6/internal/agent",
	})
	structural.Deps(t, ".") // the scan really enumerated the graph
}

func TestTheStandaloneServePackageMakesNoOutboundCall(t *testing.T) {
	structural.ScanSource(t, ".", structural.NoOutbound)
}
