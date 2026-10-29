// Package structural is the one helper behind every structural test in the tree
// (features/packaging/one_binary.feature section 4): a package's whole dependency graph must not
// reach a forbidden package, and a package's own source must not open an outbound path. Tests
// call it with their own forbidden lists, so every Core-free guarantee is checked the same way.
//
// It is imported only by tests.
package structural

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// T is the part of *testing.T the helper needs.
type T interface {
	Helper()
	Fatalf(format string, args ...any)
}

// GoTool is the go command the dependency scan runs (a seam: a test proves a missing toolchain
// fails the check rather than passing it).
var GoTool = "go"

// MinDeps is how many dependencies a real package's graph has at least: a scan that enumerates
// fewer did not actually read the graph.
const MinDeps = 20

// Deps is pkg's whole transitive dependency set. It fails when the go toolchain cannot run or the
// list is suspiciously tiny.
func Deps(t T, pkg string) []string { t.Helper(); return DepsAtLeast(t, pkg, MinDeps) }

// DepsAtLeast is Deps with an explicit floor.
func DepsAtLeast(t T, pkg string, floor int) []string {
	t.Helper()
	out, err := exec.Command(GoTool, "list", "-deps", pkg).Output()
	if err != nil {
		t.Fatalf("go list -deps %s failed (%v): a structural check that cannot read the graph proves nothing", pkg, err)
		return nil
	}
	var deps []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			deps = append(deps, line)
		}
	}
	if len(deps) < floor {
		t.Fatalf("go list -deps %s listed only %d packages (want at least %d): the scan did not enumerate the graph", pkg, len(deps), floor)
	}
	return deps
}

// ForbidDeps fails when pkg's dependency graph contains a forbidden package or anything under
// it, except the packages in allow. A forbidden entry is a prefix: "x/towercore" covers
// "x/towercore/cert" too, unless that one is allowed.
func ForbidDeps(t T, pkg string, forbidden []string, allow ...string) {
	t.Helper()
	allowed := map[string]bool{}
	for _, a := range allow {
		allowed[a] = true
	}
	for _, dep := range DepsAtLeast(t, pkg, 1) {
		if allowed[dep] {
			continue
		}
		for _, bad := range forbidden {
			if dep == bad || strings.HasPrefix(dep, bad+"/") {
				t.Fatalf("%s links %s: it must link none of %v", pkg, dep, forbidden)
				return
			}
		}
	}
}

// NoOutbound is every source call that dials, resolves or runs a program.
var NoOutbound = []string{
	"net.Dial", "net.DialTimeout", "http.Get", "http.Post", "http.Head", "http.NewRequest",
	"http.Client{", "http.DefaultClient",
	"net.LookupHost", "net.LookupIP", "net.LookupAddr", "net.LookupCNAME", "net.LookupPort",
	"net.LookupSRV", "net.LookupTXT", "net.Resolver",
	"exec.Command", "exec.CommandContext",
}

// NoListen is every source call that opens a listener.
var NoListen = []string{"net.Listen", "net.ListenPacket", "http.ListenAndServe"}

// ScanSource fails when a non-test .go file in dir contains a forbidden call, and returns how many
// files it read.
func ScanSource(t T, dir string, forbidden ...[]string) int {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("scan %s: %v", dir, err)
		return 0
	}
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("scan %s: %v", f, err)
			return checked
		}
		for _, list := range forbidden {
			for _, bad := range list {
				if strings.Contains(string(b), bad) {
					t.Fatalf("%s uses %s: this package must make no such call; a new path needs an approved spec", f, bad)
					return checked
				}
			}
		}
		checked++
	}
	if checked == 0 {
		t.Fatalf("scan %s read no source: the gate proves nothing", dir)
	}
	return checked
}
