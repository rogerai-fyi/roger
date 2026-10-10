package main

// Executable spec: features/packaging/one_binary.feature, the version (section 5, "stamped once";
// 7.8), the hints (7.10), the shared usage style, and the spec-drift scan (section 8).
//
// The version scenarios build real binaries the way a release does: roger with the one -X stamp,
// and the two one-release shims beside it, then ask each for its version.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/bddrun"
	"rogerai.fm/roger/v6/internal/client"
)

const stampModule = "rogerai.fm/roger/v6/internal/buildinfo.Version"

type obD struct {
	t        *testing.T
	tc       *towerCLI // the roger-as-subprocess helpers
	bin      string
	stamp    string
	hint     string
	stdout   string
	stderr   string
	code     int
	versions map[string]string
}

func repoRoot() string { return filepath.Join("..", "..") }

// buildRelease builds roger stamped with a test version, and the two shims beside it.
func (s *obD) buildRelease() error {
	s.bin = s.t.TempDir()
	s.stamp = "9.9.9-onebinary"
	for _, b := range []struct{ out, pkg, ld string }{
		{"roger", "./cmd/rogerai", "-X " + stampModule + "=" + s.stamp},
		{"roger-tower", "./cmd/roger-tower", ""},
		{"roger-tower-local", "./cmd/roger-tower-local", ""},
	} {
		args := []string{"build", "-o", filepath.Join(s.bin, b.out)}
		if b.ld != "" {
			args = append(args, "-ldflags", b.ld)
		}
		cmd := exec.Command("go", append(args, b.pkg)...)
		cmd.Dir = repoRoot()
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("go build %s: %v\n%s", b.pkg, err, out)
		}
	}
	return nil
}

// releaseLDFlags is every -X a release build passes (goreleaser and the broker image).
func releaseLDFlags() (string, error) {
	var all strings.Builder
	for _, f := range []string{".goreleaser.yaml", "Dockerfile", "Makefile"} {
		b, err := os.ReadFile(filepath.Join(repoRoot(), f))
		if err != nil {
			return "", err
		}
		all.Write(b)
		all.WriteString("\n")
	}
	return all.String(), nil
}

var dashX = regexp.MustCompile(`-X ([\w./-]+)=`)

func (s *obD) oneVariable() error {
	text, err := releaseLDFlags()
	if err != nil {
		return err
	}
	vars := map[string]bool{}
	for _, m := range dashX.FindAllStringSubmatch(text, -1) {
		vars[m[1]] = true
	}
	if len(vars) != 1 || !vars[stampModule] {
		return fmt.Errorf("release builds stamp %v, want only %s", vars, stampModule)
	}
	return nil
}

func (s *obD) sameEverywhere() error {
	run := func(name string, args ...string) (string, error) {
		cmd := exec.Command(filepath.Join(s.bin, name), args...)
		cmd.Env = pkgEnv(s.t)
		var o bytes.Buffer
		cmd.Stdout = &o
		err := cmd.Run()
		return o.String(), err
	}
	for _, c := range []struct {
		name string
		args []string
	}{
		{"roger", []string{"version"}}, {"roger", []string{"tower", "version"}}, {"roger", []string{"--version"}},
		{"roger-tower", []string{"version"}}, {"roger-tower-local", []string{"version"}},
	} {
		out, err := run(c.name, c.args...)
		if err != nil || !strings.Contains(out, s.stamp) {
			return fmt.Errorf("%s %v reported %q (%v), want %q", c.name, c.args, out, err, s.stamp)
		}
	}
	return nil
}

// versionFlagsAccepted: `roger tower` takes --version and -v as its version command, so the
// one-release shims pass them straight through (2026-10-09 rough edge).
func (s *obD) versionFlagsAccepted() error {
	for _, c := range []struct {
		name string
		args []string
	}{
		{"roger", []string{"tower", "--version"}}, {"roger", []string{"tower", "-v"}},
		{"roger-tower", []string{"--version"}}, {"roger-tower-local", []string{"-v"}},
	} {
		cmd := exec.Command(filepath.Join(s.bin, c.name), c.args...)
		cmd.Env = pkgEnv(s.t)
		var o, e bytes.Buffer
		cmd.Stdout, cmd.Stderr = &o, &e
		if err := cmd.Run(); err != nil || strings.TrimSpace(o.String()) != s.stamp {
			return fmt.Errorf("%s %v: stdout %q stderr %q (%v), want %q and exit 0", c.name, c.args, o.String(), e.String(), err, s.stamp)
		}
	}
	return nil
}

func (s *obD) brokerNotHardcoded() error {
	return noVersionLiteral(filepath.Join(repoRoot(), "cmd", "rogerai-broker"))
}

func (s *obD) bothStampOneVariable() error {
	if err := s.oneVariable(); err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(repoRoot(), "Dockerfile"))
	if err != nil {
		return err
	}
	if !strings.Contains(string(b), stampModule) {
		return fmt.Errorf("the broker image is not stamped through %s", stampModule)
	}
	for _, d := range []string{"cmd/rogerai", "cmd/rogerai-broker", "internal/towercli"} {
		if err := noVersionLiteral(filepath.Join(repoRoot(), d)); err != nil {
			return err
		}
	}
	return nil
}

// noVersionLiteral fails when a non-test Go file in dir declares a version variable of its own.
var versionDecl = regexp.MustCompile(`(?m)^(var|const) ([Vv]ersion) = "`)

func noVersionLiteral(dir string) error {
	files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		if versionDecl.Match(b) {
			return fmt.Errorf("%s declares its own version", f)
		}
	}
	return nil
}

// --- hints ---------------------------------------------------------------------------------

func (s *obD) situation(what string) error {
	switch what {
	case "a LAN Tower refuses this machine (not admitted)":
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"unauthorized"}`)
		}))
		defer srv.Close()
		_, priv, _ := ed25519.GenerateKey(rand.Reader)
		_, status := client.LANDiscover(context.Background(), client.LANInputs{
			Towers: []string{strings.TrimPrefix(srv.URL, "http://")}, Key: priv, HTTP: srv.Client(),
		})
		if len(status) != 1 {
			return fmt.Errorf("status %v", status)
		}
		s.hint = status[0].Detail
	case "a station's poll gets the uniform 401":
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"unauthorized"}`)
		}))
		defer srv.Close()
		_, priv, _ := ed25519.GenerateKey(rand.Reader)
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		var out bytes.Buffer
		_ = agent.ServeLocalTower(ctx, agent.Config{Broker: srv.URL, Upstream: "http://127.0.0.1:1"}, priv, &out)
		s.hint = out.String()
	case "a joined login needs an invite":
		s.tc.reset(s.t)
		o, e, _ := s.tc.roger("whoami")
		s.hint = o + e
	case "the standalone serve starts":
		s.tc.reset(s.t)
		if err := s.tc.dirFor("standalone"); err != nil {
			return err
		}
		if err := s.tc.serveBind(""); err != nil {
			return err
		}
		s.hint = s.tc.stdout
		s.tc.stop()
	case "tower readiness fails":
		s.tc.reset(s.t)
		if err := os.WriteFile(s.tc.cfg, []byte(standaloneTowerYAML+"storage:\n  profile: durable\n"), 0o600); err != nil {
			return err
		}
		o, e, _ := s.tc.roger("tower", "ready", "--config", s.tc.cfg)
		s.hint = o + e
	default:
		return fmt.Errorf("no situation %q", what)
	}
	return nil
}

var commandPlaceholder = regexp.MustCompile(`<[^>]*>|\.\.\.|on the Tower|on each client`)

// hintNames: the command's own words appear in the hint, in order, with anything between them
// (a placeholder like <keyhash> or ... stands for whatever the hint fills in).
func (s *obD) hintNames(command string) error {
	words := strings.Fields(commandPlaceholder.ReplaceAllString(command, " "))
	parts := make([]string, len(words))
	for i, w := range words {
		parts[i] = regexp.QuoteMeta(w)
	}
	if !regexp.MustCompile(strings.Join(parts, `\s+(?:\S+\s+)*?`)).MatchString(s.hint) {
		return fmt.Errorf("the hint %q does not name %q", s.hint, command)
	}
	return nil
}

// noOldNames: no string the product prints names the old binaries; the shims' own notice
// (internal/towershim) and the certificate subject (an identifier on the wire, not output) are
// the only literals left.
func (s *obD) noOldNames() error {
	if strings.Contains(s.hint, "roger-tower") {
		return fmt.Errorf("the hint names an old binary: %q", s.hint)
	}
	var bad []string
	for _, root := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(repoRoot(), root), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			if strings.Contains(filepath.ToSlash(p), "internal/towershim/") || strings.Contains(filepath.ToSlash(p), "cmd/roger-tower") {
				return nil
			}
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, p, nil, 0)
			if perr != nil {
				return perr
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				v, _ := strconv.Unquote(lit.Value)
				if strings.Contains(v, "roger-tower") && v != "roger-tower" { // the CSR subject is exactly "roger-tower"
					bad = append(bad, fmt.Sprintf("%s: %q", fset.Position(lit.Pos()), v))
				}
				return true
			})
			return nil
		})
		if err != nil {
			return err
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("output strings still name the old binaries:\n%s", strings.Join(bad, "\n"))
	}
	return nil
}

func (s *obD) notAdmittedTower() error { return nil }

func (s *obD) lanUseAgainstIt() error {
	return s.situation("a LAN Tower refuses this machine (not admitted)")
}

func (s *obD) ownerRunsInvite(want string) error {
	if s.hint != want {
		return fmt.Errorf("hint %q, want %q", s.hint, want)
	}
	return nil
}

// --- usage ------------------------------------------------------------------------------------

func (s *obD) badFlag() error {
	s.tc.reset(s.t)
	type res struct {
		stderr string
		code   int
	}
	var got []res
	for _, args := range [][]string{
		{"tower", "status", "--bogus"}, {"tower", "serve", "--dir", "D", "--bogus"},
		{"lan", "add", "--bogus"}, {"lan", "list", "--bogus"},
	} {
		_, e, code := s.tc.roger(args...)
		got = append(got, res{e, code})
	}
	for i, r := range got {
		if r.code != 2 || !strings.Contains(r.stderr, "error: ") || !strings.Contains(r.stderr, "usage: roger ") {
			return fmt.Errorf("case %d: exit %d stderr %q, want exit 2 with \"error: \" and a \"usage: roger ...\" line", i, r.code, r.stderr)
		}
	}
	return nil
}

func (s *obD) houseStyle() error { return nil } // asserted with badFlag's results

// --- spec drift --------------------------------------------------------------------------------

func (s *obD) everyFeature() error { return nil }

var oldNames = regexp.MustCompile(`roger-tower(-local)?\b`)

func (s *obD) scanFeatures() error {
	var bad []string
	err := filepath.WalkDir(filepath.Join(repoRoot(), "features"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".feature") {
			return err
		}
		if filepath.Base(p) == "one_binary.feature" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "#") { // superseded notes keep the old text on purpose
				continue
			}
			if oldNames.MatchString(t) {
				bad = append(bad, fmt.Sprintf("%s:%d: %s", p, i+1, t))
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(bad) > 0 {
		return fmt.Errorf("feature lines still name the old binaries:\n%s", strings.Join(bad, "\n"))
	}
	return nil
}

func (s *obD) onlyThisFile() error { return nil } // the scan above excludes only one_binary.feature

// towerCommandRef is a quoted tower command ("roger tower x" or `roger tower x`); prose such as
// "roger tower has" or "roger tower with no subcommand" is not a command reference.
var towerCommandRef = regexp.MustCompile("[\"`]roger tower ([a-z]+)")

func (s *obD) commandsExist() error {
	o, _, _ := s.tc.roger("tower", "help")
	listed := map[string]bool{}
	for _, l := range strings.Split(o, "\n") {
		if f := strings.Fields(l); len(f) > 0 {
			listed[f[0]] = true
		}
	}
	var bad []string
	_ = filepath.WalkDir(filepath.Join(repoRoot(), "features"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".feature") {
			return err
		}
		b, _ := os.ReadFile(p)
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			for _, m := range towerCommandRef.FindAllStringSubmatch(line, -1) {
				if !listed[m[1]] && m[1] != "help" {
					bad = append(bad, fmt.Sprintf("%s:%d: roger tower %s", p, i+1, m[1]))
				}
			}
		}
		return nil
	})
	if len(bad) > 0 {
		return fmt.Errorf("scenarios name tower commands roger tower help does not list:\n%s", strings.Join(bad, "\n"))
	}
	return nil
}

func TestOneBinaryVersionHintsBDD(t *testing.T) {
	s := &obD{t: t, tc: &towerCLI{t: t}}
	t.Cleanup(s.tc.stop)
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.hint = ""
				s.tc.reset(t)
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				s.tc.stop()
				return ctx, nil
			})
			sc.Step(`^a release is built$`, s.buildRelease)
			sc.Step(`^one build-time variable stamps the version$`, s.oneVariable)
			sc.Step(`^"roger version", "roger tower version", "roger --version" and each shim's version report the same string$`, s.sameEverywhere)
			sc.Step(`^roger and rogerai-broker stamp their version from one build-time variable each, set by the release pipeline$`, s.bothStampOneVariable)
			sc.Step(`^the broker no longer hardcodes its version in source$`, s.brokerNotHardcoded)
			sc.Step(`^"roger tower --version", "roger tower -v", "roger-tower --version" and "roger-tower-local -v" each report the stamped version and exit 0$`, s.versionFlagsAccepted)
			sc.Step(`^"([^"]*)"$`, s.situation)
			sc.Step(`^the hint names "([^"]*)"$`, s.hintNames)
			sc.Step(`^no output names roger-tower or roger-tower-local except the one-release shims' notice$`, s.noOldNames)
			sc.Step(`^a LAN Tower that answers this machine with the uniform 401$`, s.notAdmittedTower)
			sc.Step(`^the user runs roger --lan use against it$`, s.lanUseAgainstIt)
			sc.Step(`^the hint says "([^"]*)"$`, s.ownerRunsInvite)
			sc.Step(`^any tower or lan command is given a bad flag$`, s.badFlag)
			sc.Step(`^it prints the same usage format as other roger commands and exits 2$`, s.houseStyle)
			sc.Step(`^every features/ file$`, s.everyFeature)
			sc.Step(`^a structural test scans for "roger-tower", "roger-tower-local", "roger-tower release audit" and "roger-tower station invite"$`, s.scanFeatures)
			sc.Step(`^only this file and the one-release shim scenarios mention the old names$`, s.onlyThisFile)
			sc.Step(`^every scenario that names a tower command names one that "roger tower help" lists$`, s.commandsExist)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{
				"../../features/packaging/one_binary.feature:366",
				"../../features/packaging/one_binary.feature:565",
				"../../features/packaging/one_binary.feature:587",
				"../../features/packaging/one_binary.feature:600",
				"../../features/packaging/one_binary.feature:607",
				"../../features/packaging/one_binary.feature:615",
				"../../features/packaging/one_binary.feature:622",
			},
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("one_binary.feature version, hints, usage and drift scenarios failed")
	}
}
