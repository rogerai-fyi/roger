package smoke

// module_path_test.go pins the module path to a VANITY path we own (rogerai.fm/roger) rather
// than to whichever code host or organisation happens to hold the source today.
//
// Why this is a guard and not a comment: the module path is baked into every import line in
// the repo (hundreds of them). While it names a code host, renaming the GitHub organisation
// is a breaking change for everyone who imports the module - including third parties building
// against the Apache-2.0 node-agent protocol carve-out (see internal/protocol/LICENSING). With
// a vanity path, the host is declared in ONE place (the go-import meta tag on the site) and a
// future org rename touches no Go source at all.
//
// The tag on the site and the module path here must agree, or `go get` breaks in a way no
// compile error catches, so this test checks them together.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// vanityModulePath is the import path users type. It is a domain we control, so it survives
// any change of code host or organisation name.
const vanityModulePath = "rogerai.fm/roger"

// vanityPage is the document that must serve the go-import meta tag, at the URL that
// corresponds to the module path.
const vanityPage = "web/src/roger/index.html"

// legacyModulePath is the host-coupled path we migrated away from. No source file may import
// it: a half-migrated tree still compiles locally while being unbuildable for anyone else.
const legacyModulePath = "github.com/rogerai-fyi/roger"

func TestModulePathIsHostIndependent(t *testing.T) {
	root := repoRoot(t)

	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	var module string
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			module = strings.TrimSpace(rest)
			break
		}
	}
	if module == "" {
		t.Fatal("go.mod has no module line")
	}
	if module != vanityModulePath {
		t.Errorf("module path = %q, want the vanity path %q", module, vanityModulePath)
	}
	for _, host := range []string{"github.com", "gitlab.com", "bitbucket.org", "codeberg.org"} {
		if strings.Contains(module, host) {
			t.Errorf("module path %q names the code host %q; renaming the org would then be a "+
				"breaking change for every importer", module, host)
		}
	}
}

func TestNoSourceImportsTheLegacyModulePath(t *testing.T) {
	root := repoRoot(t)

	var offenders []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip hidden trees (.git, and .claude/worktrees, which holds throwaway agent
			// checkouts of this same repo - stale copies there are not our source) plus the
			// usual generated/vendored directories.
			if name := d.Name(); name != "." && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			switch d.Name() {
			case "node_modules", "dist", "vendor", "gh-pages":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, readErr := os.ReadFile(filepath.Clean(path))
		if readErr != nil {
			return readErr
		}
		// The constant above necessarily contains the legacy string; skip this file so the
		// guard cannot flag itself.
		rel, _ := filepath.Rel(root, path)
		if rel == filepath.Join("test", "smoke", "module_path_test.go") {
			return nil
		}
		if strings.Contains(string(b), legacyModulePath+"/") ||
			strings.Contains(string(b), `"`+legacyModulePath+`"`) {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("%d file(s) still import the legacy module path %q; the tree is half-migrated "+
			"and will not build for anyone outside this checkout:\n  %s",
			len(offenders), legacyModulePath, strings.Join(offenders, "\n  "))
	}
}

func TestVanityPageDeclaresTheModuleAndItsRealHost(t *testing.T) {
	root := repoRoot(t)

	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(vanityPage)))
	if err != nil {
		t.Fatalf("read %s: %v (go get %s cannot resolve without it)", vanityPage, err, vanityModulePath)
	}
	page := string(b)

	// <meta name="go-import" content="<module> <vcs> <repo-url>">
	re := regexp.MustCompile(`<meta\s+name="go-import"\s+content="([^"]+)"`)
	m := re.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("%s has no go-import meta tag; `go get %s` would fail", vanityPage, vanityModulePath)
	}
	fields := strings.Fields(m[1])
	if len(fields) != 3 {
		t.Fatalf("go-import content = %q, want exactly 3 fields (module vcs repo-url)", m[1])
	}
	if fields[0] != vanityModulePath {
		t.Errorf("go-import declares module %q, but go.mod uses %q; go get would reject the mismatch",
			fields[0], vanityModulePath)
	}
	if fields[1] != "git" {
		t.Errorf("go-import vcs = %q, want %q", fields[1], "git")
	}
	// The repo URL is the ONE place the code host is named. That is the whole point: an org
	// rename edits this line and nothing else.
	if !strings.HasPrefix(fields[2], "https://") {
		t.Errorf("go-import repo URL %q must be an https clone URL", fields[2])
	}
}

// TestNoBuildScriptPinsTheLegacyModulePath extends the guard beyond Go source.
//
// TestNoSourceImportsTheLegacyModulePath only walks *.go, and that blind spot cost us: the
// module rename left scripts/cover-gate.sh matching `go test` output against the OLD path, so
// its package extraction returned empty for every line and BOTH the no-zero-coverage rule and
// the per-package floors silently stopped enforcing anything. A coverage gate that passes
// because it parsed nothing is worse than no gate. Nothing compiled wrong, and nothing failed.
//
// Build, CI, and packaging files pin the module path in string form, so they need the same
// guard. URLs are exempt: the repository genuinely lives on a code host, and linking to it
// (clone URLs, release downloads, source links) is correct - it is the *import path* that must
// not name the host.
func TestNoBuildScriptPinsTheLegacyModulePath(t *testing.T) {
	root := repoRoot(t)

	var offenders []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name != "." && strings.HasPrefix(name, ".") && name != ".github" {
				return filepath.SkipDir
			}
			switch d.Name() {
			case "node_modules", "dist", "vendor", "gh-pages":
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case strings.HasSuffix(path, ".sh"), strings.HasSuffix(path, ".yml"),
			strings.HasSuffix(path, ".yaml"), strings.HasSuffix(path, ".mk"),
			filepath.Base(path) == "Makefile":
		default:
			return nil
		}
		b, readErr := os.ReadFile(filepath.Clean(path))
		if readErr != nil {
			return readErr
		}
		rel, _ := filepath.Rel(root, path)
		for _, line := range strings.Split(string(b), "\n") {
			if !strings.Contains(line, legacyModulePath) {
				continue
			}
			// A link to the repository is legitimate; an import path is not.
			if strings.Contains(line, "https://"+legacyModulePath) ||
				strings.Contains(line, "git@github.com:") {
				continue
			}
			offenders = append(offenders, rel+": "+strings.TrimSpace(line))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("%d build/CI file(s) still pin the legacy module path %q as an import path. "+
			"These do not fail to compile - they fail SILENTLY:\n  %s",
			len(offenders), legacyModulePath, strings.Join(offenders, "\n  "))
	}
}
