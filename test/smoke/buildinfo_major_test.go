package smoke

// The unstamped version is the release this source is (internal/buildinfo), so its major must be the
// module path's. Review 2026-10-27: the path moved to /v7 while the fallback still said 6.9.0, so a
// `go install .../v7/...@v7.0.0` build (no ldflags) would report 6.9.0 and be told to update forever.

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestTheFallbackVersionsMajorIsTheModulePathsMajor(t *testing.T) {
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "internal", "buildinfo", "buildinfo.go"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^var Version = "(\d+)\.`).FindSubmatch(b)
	if m == nil {
		t.Fatal("no Version fallback in internal/buildinfo/buildinfo.go")
	}
	mod := regexp.MustCompile(`/v(\d+)$`).FindStringSubmatch(vanityModulePath)
	if mod == nil || string(m[1]) != mod[1] {
		t.Errorf("buildinfo.Version is %s.x but the module path is %s: the unstamped build reports the wrong major",
			m[1], vanityModulePath)
	}
}
