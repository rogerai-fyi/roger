package smoke

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/unicode/norm"
)

// TestInvalidUTF8NormalizationTerminates pins GO-2026-5970 / CVE-2026-56852 at the
// dependency boundary. The vulnerable x/text release loops forever on this exact upstream
// regression input, so the dangerous call runs in a child process that the parent can kill
// and report cleanly instead of hanging the entire suite.
func TestInvalidUTF8NormalizationTerminates(t *testing.T) {
	if os.Getenv("ROGERAI_XTEXT_INVALID_UTF8_HELPER") == "1" {
		var iter norm.Iter
		iter.InitString(norm.NFC, "\xf3\xcc\x80")
		for !iter.Done() {
			_ = iter.Next()
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInvalidUTF8NormalizationTerminates$")
	cmd.Env = append(os.Environ(), "ROGERAI_XTEXT_INVALID_UTF8_HELPER=1")
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatal("Unicode normalization hung on invalid UTF-8 (GO-2026-5970)")
	}
	if err != nil {
		t.Fatalf("normalization helper failed: %v", err)
	}
}

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

func TestVulnerabilityGateRunsOnMainAndTags(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range []string{
		filepath.Join(".github", "workflows", "coverage.yml"),
		filepath.Join(".github", "workflows", "release.yml"),
	} {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if !strings.Contains(string(b), "golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...") {
			t.Errorf("%s does not gate publication on govulncheck v1.6.0", rel)
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
