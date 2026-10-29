package main

// The web pages' pinning tests, run from the Go scenarios that name them. A feature names
// the file that pins a page scenario: `web/test/<name>.test.mjs` is a node:test guard,
// `web/test/unit/<name>.test.js` a vitest component test of an island (web/app/lib). Both
// run the way `npm test` runs them, from web/. Skipped, not faked, when node is absent; the
// vitest toolchain is installed on first use when a fresh checkout lacks it (the same
// `npm ci` make web-gate runs). A file's outcome cannot change within one `go test` run, so
// each runs once and every step that names it shares the result.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cucumber/godog"
)

const webSpecDir = "../../web"

type webSpecResult struct {
	out string
	err error
}

var (
	webSpecMu   sync.Mutex
	webSpecDone = map[string]webSpecResult{}
)

// runWebSpec runs one pinning test file (a `web/...` path as a feature names it) and
// returns its output and its outcome; godog.ErrSkip when node is not installed.
func runWebSpec(rel string) (string, error) {
	webSpecMu.Lock()
	defer webSpecMu.Unlock()
	r, ok := webSpecDone[rel]
	if !ok {
		r = webSpecRun(rel)
		webSpecDone[rel] = r
	}
	return r.out, r.err
}

// webSpecsPass asserts every named file passes, with the first failure's output.
func webSpecsPass(rels ...string) error {
	for _, rel := range rels {
		if out, err := runWebSpec(rel); err != nil {
			if err == godog.ErrSkip {
				return err
			}
			return fmt.Errorf("%s failed:\n%s", rel, out)
		}
	}
	return nil
}

func webSpecRun(rel string) webSpecResult {
	node, err := exec.LookPath("node")
	if err != nil {
		return webSpecResult{err: godog.ErrSkip}
	}
	file := strings.TrimPrefix(rel, "web/")
	if _, err := os.Stat(filepath.Join(webSpecDir, file)); err != nil {
		return webSpecResult{err: fmt.Errorf("the pinning test %s is missing (a test runner skips a missing file silently): %v", rel, err)}
	}
	var cmd *exec.Cmd
	if strings.HasSuffix(file, ".test.js") {
		if err := ensureWebToolchain(); err != nil {
			return webSpecResult{err: err}
		}
		cmd = exec.Command(node, "node_modules/vitest/vitest.mjs", "run", file)
	} else {
		cmd = exec.Command(node, "--test", file)
	}
	cmd.Dir = webSpecDir
	out, err := cmd.CombinedOutput()
	return webSpecResult{out: string(out), err: err}
}

// ensureWebToolchain installs web/'s pinned devDependencies when vitest is not there yet.
func ensureWebToolchain() error {
	if _, err := os.Stat(filepath.Join(webSpecDir, "node_modules", "vitest", "vitest.mjs")); err == nil {
		return nil
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		return fmt.Errorf("web/node_modules has no vitest and npm is not on PATH to install it: %v", err)
	}
	cmd := exec.Command(npm, "ci", "--no-audit", "--no-fund")
	cmd.Dir = webSpecDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("npm ci in web/ failed: %v\n%s", err, out)
	}
	return nil
}
