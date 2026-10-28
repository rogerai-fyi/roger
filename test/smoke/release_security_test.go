package smoke

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestBrokerAndClientShareOneFallbackVersion keeps the binaries' UN-STAMPED version in step,
// for the builds nobody stamps - a `go build` from source, a developer's local run - where a
// client reporting one version and a broker another makes a bug report impossible to place.
// They agree by construction now: both read internal/buildinfo.Version (features/packaging/
// one_binary.feature, "one version"), so this pins that the shared fallback is shaped like a
// tag and that neither binary declares a version of its own.
func TestBrokerAndClientShareOneFallbackVersion(t *testing.T) {
	root := repoRoot(t)
	v := readVersionAssignment(t, filepath.Join(root, "internal", "buildinfo", "buildinfo.go"), "Version")
	if !regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`).MatchString(v) {
		t.Fatalf("fallback version %q is not a semver: -ldflags stamps a tag-derived value, "+
			"so the fallback has to be shaped like one too", v)
	}
	own := regexp.MustCompile(`(?m)^(?:const|var)\s+[Vv]ersion\s*=\s*"`)
	for _, f := range []string{filepath.Join("cmd", "rogerai", "main.go"), filepath.Join("cmd", "rogerai-broker", "main.go")} {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if own.Match(b) {
			t.Fatalf("%s declares its own version instead of reading internal/buildinfo", f)
		}
	}
}

// Keep the publication wiring independently testable without network access. The
// @release_gate Godog scenario executes the scanner in CI; this test still reaches
// both workflow assertions if that networked scenario fails first.
func TestVulnerabilityGateRunsOnMainAndTags(t *testing.T) {
	root := repoRoot(t)
	const exactGate = "go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./..."
	for _, rel := range []string{
		filepath.Join(".github", "workflows", "coverage.yml"),
		filepath.Join(".github", "workflows", "release.yml"),
	} {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if !strings.Contains(string(b), exactGate) {
			t.Errorf("%s does not use the approved vulnerability gate %q", rel, exactGate)
		}
	}
}

func readVersionAssignment(t *testing.T, path, name string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	re := regexp.MustCompile(`(?m)^(?:const|var)\s+` + regexp.QuoteMeta(name) + `\s*=\s*"([^"]+)"`)
	m := re.FindSubmatch(b)
	if m == nil {
		t.Fatalf("%s has no simple %s version assignment", path, name)
	}
	return string(m[1])
}
