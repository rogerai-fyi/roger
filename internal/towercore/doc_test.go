package towercore_test

// doc_test.go makes this directory's doc comment CHECKABLE.
//
// towercore holds no code - it is a grouping directory whose whole content is an explanation
// of a boundary. An explanation nothing verifies is a comment that rots quietly, and this one
// has already caused trouble once: naming the in-memory constructors in prose was enough to
// make a dead-code sweep report them as having production callers. So the two claims the doc
// makes that are worth anything are asserted here instead of merely written down.
//
// It also gives the package the tests the coverage gate requires of everything we ship. That
// is a side effect, not the point; a file of `require.True(t, true)` would have satisfied the
// gate and been worth nothing.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const mod = "rogerai.fm/roger/v6"

// THE DIRECTION OF TRUST, checked rather than asserted in prose: a standalone Tower is a
// private network with its own trust root, and it must not be able to reach Roger Core's
// code at all. internal/tower already may make no outbound network CALL; this is the
// compile-time half of the same rule - it may not so much as link Core in.
//
// A violation would not fail any other test. Core code imported into the standalone Tower
// would sit there working perfectly, and the first sign of trouble would be a standalone
// deployment that had quietly grown an opinion Core was supposed to own.
func TestTheStandaloneTowerCannotEvenLinkRogerCoreIn(t *testing.T) {
	deps := depsOf(t, mod+"/internal/tower")
	for _, d := range deps {
		require.False(t, strings.HasPrefix(d, mod+"/internal/towercore"),
			"internal/tower reached Core code (%s) - the standalone half must not link Core in", d)
		require.NotEqual(t, mod+"/internal/towerjoin", d,
			"internal/tower reached the joining half (%s), which dials RogerAI", d)
	}
}

// The wire format belongs to BOTH sides. If towerobj ever grew a dependency on Core, "the
// format" would silently become "whatever Core happens to produce" - and a Tower built from
// the same source would still compile, so nothing else would notice.
func TestTheSharedWireFormatDependsOnNeitherSide(t *testing.T) {
	for _, d := range depsOf(t, mod+"/internal/towerobj") {
		require.False(t, strings.HasPrefix(d, mod+"/internal/towercore"),
			"towerobj depends on Core (%s); a format only Core can produce is not a format", d)
		require.False(t, strings.HasPrefix(d, mod+"/internal/tower/"),
			"towerobj depends on the Tower (%s); the format must belong to neither side", d)
	}
}

// Every Core package the doc comment names must exist. This is the cheap half of keeping the
// map honest: a package that has been renamed or removed leaves the doc describing a layout
// that is not there, and a reader trusts the doc precisely because it is the only map.
func TestTheDocDescribesPackagesThatExist(t *testing.T) {
	raw, err := os.ReadFile("doc.go")
	require.NoError(t, err)

	// The block that lists them, read positionally rather than by grepping the whole file -
	// this package's own history is a warning about prose that matches a pattern by accident.
	body := string(raw)
	i := strings.Index(body, "WHAT EACH CORE PACKAGE DOES")
	require.Positive(t, i, "the doc no longer lists the Core packages")
	block := body[i:]
	if j := strings.Index(block, "# WHY THE IN-MEMORY"); j > 0 {
		block = block[:j]
	}

	// An ENTRY is a tab-indented line whose first character after the tab is the package
	// name; a CONTINUATION of the previous entry is indented further with spaces. Reading the
	// shape rather than the words is what keeps this from failing on ordinary prose - the
	// first draft matched a sentence continuing with the word "under" and reported it as a
	// missing package, which is the same class of mistake the doc itself warns about.
	named := 0
	for _, line := range strings.Split(block, "\n") {
		rest, ok := strings.CutPrefix(line, "//\t")
		if !ok || rest == "" || rest[0] == ' ' || rest[0] == '\t' {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 2 {
			continue
		}
		if _, err := os.Stat(filepath.Join(".", f[0])); err == nil {
			named++
			continue
		}
		entries, derr := os.ReadDir(".")
		require.NoError(t, derr)
		var have []string
		for _, e := range entries {
			if e.IsDir() {
				have = append(have, e.Name())
			}
		}
		t.Fatalf("doc.go names %q as a Core package but there is no such directory "+
			"(present: %v). Either the package moved and the doc is now a wrong map, or this "+
			"line is prose indented as if it were an entry.", f[0], have)
	}

	// Every subdirectory must be mapped, not just every mapped name present: a NEW Core
	// package that nobody added to the doc is how the map goes stale in the other direction.
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	dirs := 0
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			dirs++
		}
	}
	require.Equal(t, dirs, named,
		"the doc maps %d of the %d Core packages - add the new one to doc.go", named, dirs)
}

func depsOf(t *testing.T, pkg string) []string {
	t.Helper()
	// go list, not grep: an import is a fact the toolchain knows and a regex only guesses at,
	// and it follows the graph transitively - which is where a violation would actually hide.
	out, err := exec.Command("go", "list", "-deps", pkg).CombinedOutput()
	require.NoError(t, err, "go list failed: %s", out)
	return strings.Fields(string(out))
}
