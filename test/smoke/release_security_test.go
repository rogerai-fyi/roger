package smoke

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestBrokerAndClientFallbackVersionsMatch(t *testing.T) {
	root := repoRoot(t)
	client := readVersionAssignment(t, filepath.Join(root, "cmd", "rogerai", "main.go"), "Version")
	broker := readVersionAssignment(t, filepath.Join(root, "cmd", "rogerai-broker", "main.go"), "version")
	if client != broker {
		t.Fatalf("client fallback version %q != broker fallback version %q", client, broker)
	}
	if client != "5.7.1" {
		t.Fatalf("release repair must be v5.7.1, got %q", client)
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
