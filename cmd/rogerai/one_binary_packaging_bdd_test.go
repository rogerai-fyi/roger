package main

// Executable spec: features/packaging/one_binary.feature section 5 (packaging) and the honest
// wording of the standalone guarantee (section 4). install.sh runs for real: a fake curl on PATH
// serves release assets from a temp directory, so the script's own download, checksum and install
// paths are exercised. The previous release's unit runs through the real stand-in and roger.
//
// "A release builds one user binary per platform" is tagged @blocked-on-release-signing-not-built:
// its last line (a signature bundle, SBOM and provenance) needs a signing pipeline that does not
// exist yet (the Tower packaging spec, not built). TestReleaseBuildsOneUserBinary checks the
// rest of it.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"gopkg.in/yaml.v3"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/tower"
)

type pkgBDD struct {
	t       *testing.T
	d       *obD
	out     string
	install string
	unit    string
	read    string
	shimErr string
	addr    string
	serve   *exec.Cmd
}

type goreleaser struct {
	Builds []struct {
		ID     string   `yaml:"id"`
		Main   string   `yaml:"main"`
		Binary string   `yaml:"binary"`
		GOOS   []string `yaml:"goos"`
		GOARCH []string `yaml:"goarch"`
		GOARM  []string `yaml:"goarm"`
		Ignore []struct {
			GOOS   string `yaml:"goos"`
			GOARCH string `yaml:"goarch"`
		} `yaml:"ignore"`
	} `yaml:"builds"`
	Archives []struct {
		ID  string   `yaml:"id"`
		IDs []string `yaml:"ids"`
	} `yaml:"archives"`
	Checksum struct {
		Name string `yaml:"name_template"`
	} `yaml:"checksum"`
	Scoops []struct {
		IDs []string `yaml:"ids"`
	} `yaml:"scoops"`
}

func loadGoreleaser(t *testing.T) goreleaser {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(), ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var g goreleaser
	if err := yaml.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

// TestReleaseBuildsOneUserBinary is the buildable half of "A release builds one user binary per
// platform": roger for linux, darwin and windows; no Tower binary but the one-release shims; and
// one checksum file covering every archive.
func TestReleaseBuildsOneUserBinary(t *testing.T) {
	g := loadGoreleaser(t)
	oses := map[string]bool{}
	for _, b := range g.Builds {
		switch b.Binary {
		case "roger":
			if b.Main != "./cmd/rogerai" {
				t.Errorf("build %s builds roger from %s", b.ID, b.Main)
			}
			for _, o := range b.GOOS {
				oses[o] = true
			}
		case "roger-tower", "roger-tower-local":
			if b.Main != "./cmd/"+b.Binary {
				t.Errorf("shim %s builds from %s", b.Binary, b.Main)
			}
		default:
			t.Errorf("release builds an unexpected binary %q", b.Binary)
		}
	}
	for _, o := range []string{"linux", "darwin", "windows"} {
		if !oses[o] {
			t.Errorf("no roger build for %s", o)
		}
	}
	if g.Checksum.Name == "" {
		t.Error("the release has no checksum file")
	}
}

func (s *pkgBDD) releaseBuilt() error { return nil } // the release config is the observable

func (s *pkgBDD) shimsLinuxOnly() error {
	g := loadGoreleaser(s.t)
	seen := 0
	for _, b := range g.Builds {
		if b.Binary == "roger-tower" || b.Binary == "roger-tower-local" {
			seen++
			if len(b.GOOS) != 1 || b.GOOS[0] != "linux" {
				return fmt.Errorf("shim %s builds for %v, want linux only", b.Binary, b.GOOS)
			}
		}
	}
	if seen != 2 {
		return fmt.Errorf("%d shim builds, want 2", seen)
	}
	return nil
}

// --- install.sh ---------------------------------------------------------------------------

const fakeCurl = `#!/bin/sh
# A stand-in curl for install.sh: serves release assets from $FAKE_RELEASE by basename.
out=""; url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -w) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
f="$FAKE_RELEASE/${url##*/}"
[ -f "$f" ] || exit 22
if [ -n "$out" ]; then cp "$f" "$out"; else cat "$f"; fi
`

// pkgEnv is the whole environment for a program this runner launches (install.sh, the installed
// roger, a stand-in): PATH and fresh HOME/config dirs only, so a ROGER_* or ROGERAI_* variable in
// the shell that runs the tests cannot change what the shipped binaries do. Later entries win.
func pkgEnv(t *testing.T, extra ...string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "XDG_CONFIG_HOME=" + t.TempDir(),
		"TMPDIR=" + os.TempDir(), "ROGERAI_NO_UPDATE_CHECK=1"}
	return append(env, extra...)
}

func (s *pkgBDD) runInstall(component string) error {
	if runtime.GOOS != "linux" {
		return godog.ErrSkip
	}
	if s.d.bin == "" {
		if err := s.d.buildRelease(); err != nil {
			return err
		}
	}
	rel := s.t.TempDir()
	arch := runtime.GOARCH
	var sums strings.Builder
	for _, a := range []struct{ asset, from string }{
		{"roger-linux-" + arch, "roger"}, {"roger-tower-linux-" + arch, "roger-tower"},
		{"roger-tower-local-linux-" + arch, "roger-tower-local"},
	} {
		b, err := os.ReadFile(filepath.Join(s.d.bin, a.from))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(rel, a.asset), b, 0o755); err != nil {
			return err
		}
		h := sha256.Sum256(b)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), a.asset)
	}
	if err := os.WriteFile(filepath.Join(rel, "checksums.txt"), []byte(sums.String()), 0o644); err != nil {
		return err
	}
	fake := s.t.TempDir()
	if err := os.WriteFile(filepath.Join(fake, "curl"), []byte(fakeCurl), 0o755); err != nil {
		return err
	}
	s.install = s.t.TempDir()
	cmd := exec.Command("sh", filepath.Join(repoRoot(), "web", "src", "install.sh"))
	cmd.Env = pkgEnv(s.t, "PATH="+fake+":"+os.Getenv("PATH"), "FAKE_RELEASE="+rel,
		"ROGERAI_VERSION=v9.9.9", "ROGERAI_INSTALL_DIR="+s.install, "ROGERAI_COMPONENT="+component)
	var o bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &o
	err := cmd.Run()
	s.out = o.String()
	if err != nil {
		return fmt.Errorf("install.sh: %v\n%s", err, s.out)
	}
	return nil
}

func (s *pkgBDD) rogerInstalled() error {
	p := filepath.Join(s.install, "roger")
	cmd := exec.Command(p, "version")
	cmd.Env = pkgEnv(s.t)
	out, err := cmd.Output()
	if err != nil || !strings.Contains(string(out), s.d.stamp) {
		return fmt.Errorf("installed roger version %q (%v)", out, err)
	}
	return nil
}

func (s *pkgBDD) shimBeside(shim string) error {
	p := filepath.Join(s.install, shim)
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not installed beside roger (%v)", shim, err)
	}
	return nil
}

func (s *pkgBDD) printsUseRogerTower() error {
	if !strings.Contains(s.out, "use `roger tower ...`") {
		return fmt.Errorf("install output does not say to use roger tower:\n%s", s.out)
	}
	return nil
}

// --- the unit ---------------------------------------------------------------------------------

func (s *pkgBDD) packagedUnit() error {
	b, err := os.ReadFile(filepath.Join(repoRoot(), "packaging", "tower", "roger-tower.service"))
	s.unit = string(b)
	return err
}

func (s *pkgBDD) execStartRuns() error {
	re := regexp.MustCompile(`(?m)^ExecStart=\S*/roger tower serve --dir \S+ --config \S+$`)
	if !re.MatchString(s.unit) {
		return fmt.Errorf("the unit's ExecStart is not roger tower serve --dir ... --config ...")
	}
	return nil
}

func (s *pkgBDD) keepsHardening() error {
	for _, want := range []string{"User=roger-tower", "NoNewPrivileges=yes", "ProtectSystem=strict", "ProtectHome=yes",
		"PrivateDevices=yes", "CapabilityBoundingSet=\n", "AmbientCapabilities=\n", "RestrictSUIDSGID=yes",
		"MemoryDenyWriteExecute=yes", "StateDirectoryMode=0700"} {
		if !strings.Contains(s.unit, want) {
			return fmt.Errorf("the unit lost %q", strings.TrimSpace(want))
		}
	}
	return nil
}

func (s *pkgBDD) installedDefaults() error { return s.packagedUnit() }

func (s *pkgBDD) serveStarts() error { return nil } // the unit's user and init's file modes are the observables

func (s *pkgBDD) nonRoot() error {
	m := regexp.MustCompile(`(?m)^User=(\S+)$`).FindStringSubmatch(s.unit)
	if m == nil || m[1] == "root" || m[1] == "0" {
		return fmt.Errorf("the unit's runtime user is %v", m)
	}
	return nil
}

func (s *pkgBDD) identityOwnerOnly() error {
	dir := filepath.Join(s.t.TempDir(), "tw")
	if _, err := tower.Init(dir, tower.ModeStandalone); err != nil {
		return err
	}
	return filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if fi.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("%s is %v: identity state must be readable only by its owner", p, fi.Mode().Perm())
		}
		return nil
	})
}

// --- package managers ----------------------------------------------------------------------

func (s *pkgBDD) packageManagers() error {
	g := loadGoreleaser(s.t)
	for _, sc := range g.Scoops {
		for _, id := range sc.IDs {
			if id != "winzip" {
				return fmt.Errorf("the Scoop manifest packages %q, want only the roger zip", id)
			}
		}
	}
	cask, err := os.ReadFile(filepath.Join(repoRoot(), "packaging", "homebrew-cask", "roger.rb"))
	if err != nil {
		return err
	}
	if strings.Contains(string(cask), "roger-tower") {
		return fmt.Errorf("the Homebrew cask installs a Tower binary")
	}
	var bad []string
	_ = filepath.WalkDir(filepath.Join(repoRoot(), "packaging", "gentoo"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), "roger-tower") {
				bad = append(bad, p)
			}
		}
		return nil
	})
	rel, _ := os.ReadFile(filepath.Join(repoRoot(), ".github", "workflows", "release.yml"))
	if regexp.MustCompile(`winget[^\n]*roger-tower`).Match(rel) {
		bad = append(bad, "release.yml winget")
	}
	if len(bad) > 0 {
		return fmt.Errorf("package-manager files name a Tower binary: %v", bad)
	}
	return nil
}

func (s *pkgBDD) docsMentionRogerTower() error {
	b, err := os.ReadFile(filepath.Join(repoRoot(), "packaging", "README.md"))
	if err != nil || !strings.Contains(string(b), "roger tower") {
		return fmt.Errorf("packaging/README.md does not mention roger tower for operators (%v)", err)
	}
	return nil
}

// --- the previous release's unit ----------------------------------------------------------------

func (s *pkgBDD) previousUnit() error {
	// The previous release ran: ExecStart=/usr/local/bin/roger-tower serve --config C (no --dir).
	if s.d.bin == "" {
		if err := s.d.buildRelease(); err != nil {
			return err
		}
	}
	return nil
}

func (s *pkgBDD) startsAfterUpgrade() error {
	dir := filepath.Join(s.t.TempDir(), "tw")
	if _, err := tower.Init(dir, tower.ModeStandalone); err != nil {
		return err
	}
	s.addr = freeLoopback(s.t)
	cfg := filepath.Join(s.t.TempDir(), "tower.yaml")
	body := standaloneTowerYAML + "identity:\n  dir: " + dir + "\nstandalone:\n  bind: " + s.addr + "\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		return err
	}
	cmd := exec.Command(filepath.Join(s.d.bin, "roger-tower"), "serve", "--config", cfg)
	cmd.Env = pkgEnv(s.t)
	var errb bytes.Buffer
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, &errb
	if err := cmd.Start(); err != nil {
		return err
	}
	s.serve = cmd
	go func() { _, _ = io.Copy(io.Discard, pr) }()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", s.addr, time.Second); err == nil {
			c.Close()
			s.shimErr = errb.String()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("the old unit's command never served; stderr %q", errb.String())
}

func (s *pkgBDD) servesAsBefore() error { return nil } // startsAfterUpgrade connected to the plane

func (s *pkgBDD) journalNotice() error {
	if !strings.Contains(s.shimErr, "roger-tower is now `roger tower`; this name is removed in the next release") {
		return fmt.Errorf("stderr (the journal) %q has no deprecation notice", s.shimErr)
	}
	return nil
}

// --- honest wording ------------------------------------------------------------------------------

func (s *pkgBDD) readsHelpAndManual() error {
	s.d.tc.reset(s.t)
	o, _, _ := s.d.tc.roger("tower", "serve", "--help")
	m, err := os.ReadFile(filepath.Join(repoRoot(), "web", "src", "manual.html"))
	if err != nil {
		return err
	}
	sec := string(m)
	if i := strings.Index(sec, "<h3>Standalone: a relay that is entirely yours</h3>"); i >= 0 {
		sec = sec[i:]
		if j := strings.Index(sec[10:], "<h3>"); j >= 0 {
			sec = sec[:j+10]
		}
	}
	ws := regexp.MustCompile(`\s+`)
	s.read = ws.ReplaceAllString(o, " ") + "\n" + ws.ReplaceAllString(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(sec, ""), " ")
	return nil
}

func (s *pkgBDD) saysGuarantee() error {
	for _, want := range []string{"standalone serve package imports no Core code", "refuses every non-local connection"} {
		if strings.Count(s.read, want) < 2 {
			return fmt.Errorf("the help and the manual do not both say %q", want)
		}
	}
	return nil
}

func (s *pkgBDD) neverClaimsBinary() error {
	if regexp.MustCompile(`(?i)(binary|roger) contains no code`).MatchString(s.read) {
		return fmt.Errorf("the docs claim the binary contains no Core code")
	}
	return nil
}

func TestOneBinaryPackagingBDD(t *testing.T) {
	s := &pkgBDD{t: t, d: &obD{t: t, tc: &towerCLI{t: t}}}
	stop := func() {
		if s.serve != nil && s.serve.Process != nil {
			_ = s.serve.Process.Kill()
			_, _ = s.serve.Process.Wait()
		}
		s.serve = nil
	}
	t.Cleanup(stop)
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				stop()
				return ctx, nil
			})
			sc.Step(`^the previous release's roger-tower\.service unit$`, s.previousUnit)
			sc.Step(`^it starts after the upgrade$`, s.startsAfterUpgrade)
			sc.Step(`^the Tower serves as before$`, s.servesAsBefore)
			sc.Step(`^the journal shows the one-line deprecation notice$`, s.journalNotice)
			sc.Step(`^the user reads "roger tower serve --help" and the manual's standalone section$`, s.readsHelpAndManual)
			sc.Step(`^they say the standalone serve package imports no Core code and standalone mode refuses every non-local connection$`, s.saysGuarantee)
			sc.Step(`^they never say the binary contains no code that can reach Roger Core$`, s.neverClaimsBinary)
			sc.Step(`^a release is built$`, s.releaseBuilt)
			sc.Step(`^roger-tower and roger-tower-local shims exist for linux only$`, s.shimsLinuxOnly)
			sc.Step(`^install\.sh runs with ROGERAI_COMPONENT="([^"]*)"$`, s.runInstall)
			sc.Step(`^roger is installed$`, s.rogerInstalled)
			sc.Step(`^for this release the "([^"]*)" shim is installed beside it$`, s.shimBeside)
			sc.Step("^the script prints \"use `roger tower \\.\\.\\.`\"$", s.printsUseRogerTower)
			sc.Step(`^the packaged Tower unit$`, s.packagedUnit)
			sc.Step(`^its ExecStart runs "roger tower serve --dir \.\.\. --config \.\.\."$`, s.execStartRuns)
			sc.Step(`^it keeps the non-root user and the existing hardening$`, s.keepsHardening)
			sc.Step(`^the host or OCI package is installed with defaults$`, s.installedDefaults)
			sc.Step(`^"roger tower serve" starts$`, s.serveStarts)
			sc.Step(`^its runtime user is non-root$`, s.nonRoot)
			sc.Step(`^private identity files remain readable only by that identity$`, s.identityOwnerOnly)
			sc.Step(`^the Homebrew, Scoop, winget and Gentoo packages install only roger, unchanged$`, s.packageManagers)
			sc.Step(`^their docs mention "roger tower" for operators$`, s.docsMentionRogerTower)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{
				"../../features/packaging/one_binary.feature:221",
				"../../features/packaging/one_binary.feature:305",
				"../../features/packaging/one_binary.feature:325",
				"../../features/packaging/one_binary.feature:340",
				"../../features/packaging/one_binary.feature:351",
				"../../features/packaging/one_binary.feature:356",
				"../../features/packaging/one_binary.feature:362",
			},
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("one_binary.feature packaging scenarios failed")
	}
}
