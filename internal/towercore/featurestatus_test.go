package towercore_test

// featurestatus_test.go makes "is this specified thing actually built?" a question the repo
// can answer.
//
// # WHY
//
// The Tower spec corpus is large and mostly ahead of the code: twenty feature files, ~700
// scenarios, and at the time this was written ten of those files had no implementing code at
// all. Every one of them carries an "APPROVED SPEC" header, which says a human agreed to the
// behaviour - and says nothing whatever about whether it exists.
//
// That is a genuinely dangerous ambiguity in a security-and-money spec. Reading
// operator_revenue_share.feature, ninety-eight approved scenarios describing exactly how a
// Tower operator gets paid, there is nothing on the page to tell you that none of it runs.
// Direction changed twice while these were being written, and a reader cannot distinguish
// "approved and shipped" from "approved and superseded" from "approved and never started".
//
// So each file declares a BUILD STATUS and this test holds it to it. The status is a claim,
// and like every other claim in this tree it gets a check rather than trust.
//
// # HOW THE CLAIM IS CHECKED
//
// By the "Contract: features/tower/<file>" convention already used throughout the Tower
// code. It is a weak signal for how MUCH is built - it cannot tell you nine of sixty-eight
// scenarios are covered - but it is an exact signal for the thing that actually goes wrong:
// a file marked built with nothing pointing at it, or a file marked unbuilt that somebody
// has quietly implemented and left mislabelled.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	statusPartial   = "PARTIAL"
	statusNotBuilt  = "NOT BUILT"
	statusReference = "REFERENCE"
	statusBuilt     = "BUILT"
)

func TestEveryTowerFeatureDeclaresWhetherItIsBuilt(t *testing.T) {
	for _, f := range towerFeatures(t) {
		t.Run(filepath.Base(f), func(t *testing.T) {
			require.NotEmpty(t, statusOf(t, f),
				"this feature file does not say whether it is built.\n"+
					"Add a `# BUILD STATUS: <BUILT|PARTIAL|NOT BUILT|REFERENCE>` line: an approved "+
					"spec with no build status cannot be told apart from a shipped one.")
		})
	}
}

// A file claiming to be built must have something pointing at it, and one claiming not to be
// must have nothing. Both directions matter: the first catches a status that was aspirational
// when written, the second catches work that landed and left the spec mislabelled.
func TestTheBuildStatusMatchesWhatTheCodeClaims(t *testing.T) {
	refs := contractReferences(t)
	for _, f := range towerFeatures(t) {
		base := filepath.Base(f)
		t.Run(base, func(t *testing.T) {
			status := statusOf(t, f)
			n := refs[base]
			switch status {
			case statusBuilt, statusPartial:
				require.NotZero(t, n,
					"%s says %s, but no code carries `Contract: features/tower/%s`.\n"+
						"Either the status is wishful, or the implementation exists and does not "+
						"say which spec it answers to.", base, status, base)
			case statusNotBuilt:
				require.Zero(t, n,
					"%s says NOT BUILT, but %d file(s) carry `Contract: features/tower/%s`.\n"+
						"Something was implemented and the spec was left saying it was not.",
					base, n, base)
			case statusReference:
				// A glossary defines terms. There is nothing to implement and nothing to check.
			default:
				t.Fatalf("%s has an unrecognized BUILD STATUS %q", base, status)
			}
		})
	}
}

// The edge path is the direction currently being built, and it is the one most likely to be
// half-remembered later. Pinning it here means the day it is finished, this test is what
// tells somebody to update the status rather than leaving the corpus lying.
func TestTheSupersededScenariosAreNamedWhereAReaderWillFindThem(t *testing.T) {
	root := repoRoot(t)
	edge, err := os.ReadFile(filepath.Join(root, "features/tower/edge_dispatch.feature"))
	require.NoError(t, err)
	old, err := os.ReadFile(filepath.Join(root, "features/tower/job_and_settlement.feature"))
	require.NoError(t, err)

	// Every scenario edge_dispatch claims to supersede must still EXIST in the file it
	// supersedes them from. A stale supersession list is worse than none: it tells a reader
	// a contradiction has been resolved when the two documents have simply drifted apart.
	for _, name := range []string{
		"Roger Core still sees content under the v1 policy contract",
		"Roger Core records channel-bound transit from its own observation",
		"A joined settlement requires a complete matching Core transit observation",
		"The inner TLS session authenticates Roger Core and the selected Station end to end",
	} {
		require.Contains(t, string(edge), name,
			"edge_dispatch.feature must name every scenario it contradicts")
		require.Contains(t, string(old), name,
			"edge_dispatch.feature claims to supersede %q, which is no longer in "+
				"job_and_settlement.feature - the supersession list has gone stale", name)
	}
}

func towerFeatures(t *testing.T) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(repoRoot(t), "features/tower/*.feature"))
	require.NoError(t, err)
	require.NotEmpty(t, found, "no Tower feature files found")
	return found
}

var statusLine = regexp.MustCompile(`(?m)^#\s*BUILD STATUS:\s*([A-Z ]+?)\s*\.`)

func statusOf(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	m := statusLine.FindSubmatch(raw)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(string(m[1]))
}

// contractReferences counts, per feature file, how many Go files point at it.
func contractReferences(t *testing.T) map[string]int {
	t.Helper()
	out := map[string]int{}
	root := repoRoot(t)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Vendored and build output would only add references we did not write.
			if name := info.Name(); name == "vendor" || name == "node_modules" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// The checker names feature files in order to check them. Counting its own prose as
		// evidence of implementation would make every file look built the moment it was added
		// to a list here.
		if filepath.Base(path) == "featurestatus_test.go" {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, m := range contractRef.FindAllSubmatch(raw, -1) {
			out[string(m[1])]++
		}
		return nil
	})
	require.NoError(t, err)
	return out
}

var contractRef = regexp.MustCompile(`features/tower/([a-z_0-9]+\.feature)`)

func repoRoot(t *testing.T) string {
	t.Helper()
	// This package sits at internal/towercore.
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(root, "go.mod"))
	return root
}
