package smoke

// module_path_test.go pins the module path to a VANITY path we own (rogerai.fm/roger) rather
// than to whichever code host or organisation happens to hold the source today.
//
// Why this is a guard and not a comment: the module path is baked into every import line in
// the repo (hundreds of them). While it names a code host, renaming the GitHub organisation
// is a breaking change for everyone who imports the module. With a vanity path, the host is
// declared in ONE place (the go-import meta tag on the site) and a future org rename touches
// no Go source at all. (The Apache-2.0 carve-out in /LICENSING.md is unaffected either way:
// those files live under internal/, which no external module may import at any path.)
//
// The tag on the site and the module path here must agree, or `go get` breaks in a way no
// compile error catches, so this test checks them together.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// vanityModulePath is the import path users type. It is a domain we control, so it survives
// any change of code host or organisation name.
//
// The /v6 suffix is not decoration. Go only considers tags whose major version matches the
// module path's suffix, so while this said "rogerai.fm/roger" the toolchain ignored every
// v2+ tag and resolved @latest to v0.3.3 - a five-major-version-old build - for anyone
// running `go install`. Silent, and wrong in the most expensive direction: it hands people
// ancient code that still compiles.
const vanityModulePath = "rogerai.fm/roger/v6"

// vanityPage is the ROOT document. It declares the repo-root prefix WITHOUT the major
// suffix, because a go-import prefix must be a prefix of the URL Go requested: declaring
// ".../v6" at /roger would make Go reject the page outright rather than report the real
// problem. Keeping the bare prefix turns `go get rogerai.fm/roger` into the useful "module
// declares its path as rogerai.fm/roger/v6".
const vanityPage = "web/src/roger/index.html"
const vanityRootPrefix = "rogerai.fm/roger"

// vanityPageVersioned is the document Go actually fetches, at the URL that corresponds to
// the full module path including its major-version suffix.
const vanityPageVersioned = "web/src/roger/v6/index.html"

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
			// Skip hidden trees (.git, and the agent worktree directory under .claude, which holds throwaway
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
	if fields[0] != vanityRootPrefix {
		t.Errorf("the root page declares %q, want the repo-root prefix %q; a go-import prefix "+
			"must be a prefix of the requested URL or Go rejects the page", fields[0], vanityRootPrefix)
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

// TestModulePathMajorMatchesLatestReleaseTag is the guard that would have caught the v0.3.3
// regression. Go resolves @latest only among tags whose major version matches the module
// path's suffix: with no suffix it considers v0/v1 only, so a repo tagged v5 silently serves
// its last v0 tag forever. Nothing errors, nothing fails to compile - users just get old code.
//
// Skips when tags are unavailable (a shallow CI checkout without fetch-tags) rather than
// failing, because absence of tags is not evidence of a wrong module path.
func TestModulePathMajorMatchesLatestReleaseTag(t *testing.T) {
	root := repoRoot(t)

	// Same scrub as the rest of this package: -C does not override an inherited GIT_DIR,
	// and these tests run under the pre-push gate, where git exports one.
	tagCmd := exec.Command("git", "-C", root, "tag", "--sort=-v:refname")
	tagCmd.Env = cleanEnv()
	out, err := tagCmd.Output()
	if err != nil {
		t.Skipf("git tags unavailable: %v", err)
	}
	var latest string
	for _, line := range strings.Split(string(out), "\n") {
		tag := strings.TrimSpace(line)
		if regexp.MustCompile(`^v\d+\.\d+\.\d+$`).MatchString(tag) {
			latest = tag
			break
		}
	}
	if latest == "" {
		t.Skip("no release tags in this checkout")
	}

	tagMajor, err := strconv.Atoi(strings.SplitN(strings.TrimPrefix(latest, "v"), ".", 2)[0])
	if err != nil {
		t.Fatalf("could not read the major version out of tag %q: %v", latest, err)
	}

	// The module's own major: v0 and v1 carry no suffix, so their absence means 1.
	modMajor := 1
	if m := regexp.MustCompile(`/v(\d+)$`).FindStringSubmatch(vanityModulePath); m != nil {
		modMajor, _ = strconv.Atoi(m[1])
	}

	// THE TWO DIRECTIONS ARE NOT THE SAME FAILURE, which is why this compares magnitudes
	// rather than demanding equality.
	//
	// BEHIND (module major < newest tag) is the bug this test was written for, and it is
	// silent: Go resolves @latest only among tags matching the module's suffix, so a repo
	// tagged v5 whose path says nothing serves its last v0 tag forever. Nothing errors,
	// nothing fails to compile, and users get years-old code that still builds.
	//
	// AHEAD (module major > newest tag) is a major migration in flight - the path has to
	// change BEFORE the first tag of that major can exist, so there is a window where this
	// is not merely allowed but required. Demanding equality here would make that window
	// unreachable: the migration could never be committed, because the tag it needs cannot
	// be cut until the migration is committed.
	//
	// Being ahead is also SAFE in the way being behind is not. `go install .../v6@latest`
	// with no v6 tag yet fails loudly with "no matching versions"; it cannot quietly hand
	// anyone the wrong build. And the previous major keeps resolving from its own tags,
	// whose go.mod still names the previous path.
	if modMajor < tagMajor {
		t.Errorf("latest release tag is %s (major %d) but the module path is %q (major %d). "+
			"Go resolves @latest only among tags whose major matches the path's suffix, so "+
			"`go install %s@latest` silently serves an old build instead of %s.",
			latest, tagMajor, vanityModulePath, modMajor, vanityModulePath, latest)
	}
	if modMajor > tagMajor {
		t.Logf("module path is %q (major %d) while the newest tag is %s (major %d): a major "+
			"migration is in flight and the v%d tag has not been cut yet.",
			vanityModulePath, modMajor, latest, tagMajor, modMajor)
	}
}

// TestVersionedVanityPageDeclaresTheModulePath covers the page Go actually fetches.
//
// The root page deliberately declares the suffix-less prefix, so on its own it proves
// nothing about the module being resolvable. This is the assertion that does: the /v6
// document must declare exactly what go.mod declares, or `go get` rejects the mismatch.
func TestVersionedVanityPageDeclaresTheModulePath(t *testing.T) {
	root := repoRoot(t)

	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(vanityPageVersioned)))
	if err != nil {
		t.Fatalf("read %s: %v (go get %s cannot resolve without it)",
			vanityPageVersioned, err, vanityModulePath)
	}

	m := regexp.MustCompile(`<meta\s+name="go-import"\s+content="([^"]+)"`).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatalf("%s has no go-import meta tag", vanityPageVersioned)
	}
	fields := strings.Fields(m[1])
	if len(fields) != 3 {
		t.Fatalf("go-import content = %q, want 3 fields (module vcs repo-url)", m[1])
	}
	if fields[0] != vanityModulePath {
		t.Errorf("versioned page declares %q, but go.mod uses %q; go get rejects the mismatch",
			fields[0], vanityModulePath)
	}
	// The root prefix must remain a prefix of the module path, or the two pages describe
	// unrelated modules and the fallback resolution path is gone.
	if !strings.HasPrefix(vanityModulePath, vanityRootPrefix) {
		t.Errorf("module path %q does not start with the root prefix %q",
			vanityModulePath, vanityRootPrefix)
	}
}
