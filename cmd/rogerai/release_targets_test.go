package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestEveryReleaseTargetCompiles builds every binary .goreleaser.yaml releases, for every
// GOOS/GOARCH it lists (minus its ignores), the way the release does (CGO off). It exists because
// a Unix-only call reached the Windows build and nothing noticed until a release would have
// failed: the gate runs this test, so a platform break fails the gate instead. The target list
// is read from .goreleaser.yaml, so it can never drift from what ships.
func TestEveryReleaseTargetCompiles(t *testing.T) {
	g := loadGoreleaser(t)
	built := 0
	for _, b := range g.Builds {
		arms := b.GOARM
		if len(arms) == 0 {
			arms = []string{""}
		}
		for _, goos := range b.GOOS {
			for _, goarch := range b.GOARCH {
				ignored := false
				for _, ig := range b.Ignore {
					if ig.GOOS == goos && ig.GOARCH == goarch {
						ignored = true
					}
				}
				if ignored {
					continue
				}
				goarm := ""
				if goarch == "arm" {
					goarm = arms[0]
				}
				b, goos, goarch := b, goos, goarch
				built++
				t.Run(b.ID+"/"+goos+"_"+goarch, func(t *testing.T) {
					t.Parallel()
					cmd := exec.Command("go", "build", "-trimpath", "-o", os.DevNull, b.Main)
					cmd.Dir = repoRoot()
					cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "GOARM="+goarm, "CGO_ENABLED=0")
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("%s does not build for %s/%s:\n%s", b.Main, goos, goarch, strings.TrimSpace(string(out)))
					}
				})
			}
		}
	}
	if built < 9 {
		t.Fatalf("read only %d release targets from .goreleaser.yaml; the parse is wrong", built)
	}
}
