package smoke

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestBrokerAndClientFallbackVersionsMatch keeps the two binaries' UN-STAMPED fallbacks in
// step. Both are overwritten by -ldflags at release time, so this is about the builds nobody
// stamps - a `go build` from source, a developer's local run - where a client reporting one
// version and a broker reporting another makes a bug report impossible to place.
//
// IT NO LONGER PINS A LITERAL. It used to also assert client == "5.7.1", added by
// 6c9cecec as part of that release's contract. That served its purpose - v5.7.1 shipped and
// is live - but a hardcoded version in a test that guards versioning fails on EVERY
// subsequent bump, and the failure names the release it was written for rather than
// anything wrong with the change in front of you. The durable property is that the two
// agree; which value they agree on is decided by the tag and pinned against the manual by
// the version-sync gate in cover-gate-fast.
func TestBrokerAndClientFallbackVersionsMatch(t *testing.T) {
	root := repoRoot(t)
	client := readVersionAssignment(t, filepath.Join(root, "cmd", "rogerai", "main.go"), "Version")
	broker := readVersionAssignment(t, filepath.Join(root, "cmd", "rogerai-broker", "main.go"), "version")
	if client != broker {
		t.Fatalf("client fallback version %q != broker fallback version %q", client, broker)
	}
	if !regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`).MatchString(client) {
		t.Fatalf("fallback version %q is not a semver: -ldflags stamps a tag-derived value, "+
			"so the fallback has to be shaped like one too", client)
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
