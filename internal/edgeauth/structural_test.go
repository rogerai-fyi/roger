package edgeauth

// Contract: features/edge/enrollment.feature - "the local authority is Core-free by
// construction, not by promise".
//
// THE STRUCTURAL GUARANTEE. "A local Edge authority never contacts Core" must be a
// property of the linkage, not of a runtime check one bug could flip. This test
// inspects this package's full dependency graph and fails if it links anything that
// could reach Core - or, more strongly, if it links an HTTP client or server at all.
// The wire lives in internal/edgeauth/enrollhttp; the authority lives here and CANNOT
// dial, so an owner on a plant network is not trusting a comment.
//
// This is the same guarantee, and deliberately the same kind of test, that
// internal/localplane/serve/structural_test.go earns for the standalone consumer plane,
// through the same shared helper (internal/structural).

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/structural"
)

// deps returns the whole transitive dependency set of a package.
func deps(t *testing.T, pkg string) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", pkg).Output()
	require.NoError(t, err, "go list -deps must succeed")
	var got []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			got = append(got, line)
		}
	}
	require.Greater(t, len(got), 20, "the dep scan must actually have enumerated the graph")
	return got
}

func TestTheEdgeAuthorityLinksNothingThatCouldReachCore(t *testing.T) {
	// Every RogerAI package that dials Roger Core, plus every transport that could. towercore/*
	// is forbidden too, with ONE exception that is itself structural: towercore/cert. See
	// TestTheCertificateLibraryIsALeaf below - it links no other RogerAI package at all, so it has
	// nothing to reach Core with. Reusing it is what makes a local certificate and a Core
	// certificate the same certificate.
	structural.ForbidDeps(t, ".", []string{
		"rogerai.fm/roger/v6/internal/client",    // the Core/broker client
		"rogerai.fm/roger/v6/internal/towerjoin", // dials Core to join
		"rogerai.fm/roger/v6/internal/towerhub",
		"rogerai.fm/roger/v6/internal/towercore",
		"net/http", // no client, no server: this package cannot speak to anything
	}, "rogerai.fm/roger/v6/internal/towercore/cert")
	structural.Deps(t, ".") // the scan really enumerated the graph
}

// TestTheCertificateLibraryIsALeaf is what makes the exception above a guarantee rather
// than a favour: towercore/cert is admissible inside a Core-free authority precisely
// because it links no other RogerAI package, so there is no path from it to anything
// that dials.
func TestTheCertificateLibraryIsALeaf(t *testing.T) {
	for _, dep := range deps(t, "rogerai.fm/roger/v6/internal/towercore/cert") {
		if !strings.HasPrefix(dep, "rogerai.fm/roger/v6/") {
			continue
		}
		require.Equal(t, "rogerai.fm/roger/v6/internal/towercore/cert", dep,
			"towercore/cert links %s: it is only admissible inside a Core-free authority "+
				"while it links nothing of ours.", dep)
	}
}

// TestTheWireLinksNoCoreEither: the transport half may speak HTTP - that is its job -
// but it still may not link a package that knows where Core is.
func TestTheWireLinksNoCoreEither(t *testing.T) {
	for _, dep := range deps(t, "./enrollhttp") {
		for _, bad := range []string{
			"rogerai.fm/roger/v6/internal/client",
			"rogerai.fm/roger/v6/internal/towerjoin",
			"rogerai.fm/roger/v6/internal/towerhub",
		} {
			require.NotEqual(t, bad, dep, "the enrollment wire links %s", dep)
		}
	}
}
