package netpriv_test

// Executable spec: features/packaging/one_binary.feature section 7.1, one private-address
// rule. Each site in the outline is a real source file that must import internal/netpriv, so
// the answer the outline checks is the answer that site computes; the structural scenarios
// read the real source tree and the real dependency graph.

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/netpriv"
	"rogerai.fm/roger/v6/internal/tower"
)

const netprivImport = `"rogerai.fm/roger/v6/internal/netpriv"`

// siteFiles is where each site in the outline decides "local". A site passes only when its
// file imports netpriv: a site with its own rule fails here before the class is compared.
var siteFiles = map[string]string{
	"tower bind":                   "internal/netbind/netbind.go",
	"tower egress allow-list":      "internal/tower/egress.go",
	"lan add":                      "internal/lanmode/lanmode.go",
	"client nonce decision":        "internal/lanmode/lanmode.go", // lanmode.WantsNonce
	"edge advert":                  "internal/edge/wire/advert.go",
	"edge listener":                "cmd/rogerai/edgebind.go",
	"tower advertise":              "cmd/roger-tower-local/advertise.go",
	"joined serve advertised addr": "cmd/roger-tower/serve.go",
	"tower doctor":                 "internal/tower/doctor.go",
}

// privateRule finds a private-address rule outside netpriv: a stdlib IsPrivate call or a
// private, loopback, link-local or CGNAT CIDR literal.
var privateRule = regexp.MustCompile(`\.IsPrivate\(\)|"(10\.0\.0\.0/8|172\.16\.0\.0/12|192\.168\.0\.0/16|fc00::/7|100\.64\.0\.0/10|169\.254\.0\.0/16|fe80::/10|127\.0\.0\.0/8|::1/128)"`)

// allowedRule names the files allowed their own rule: netpriv itself, and protocol.TrustedBase,
// which answers a different question (loopback-only key trust).
func allowedRule(rel string) bool {
	return strings.HasPrefix(rel, "internal/netpriv/") || rel == "internal/protocol/trustedbase.go"
}

func repoRoot(t *testing.T) string {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	return filepath.Dir(strings.TrimSpace(string(out)))
}

type netprivBDD struct {
	root     string
	site     string
	class    string
	findings []string
	deps     []string
	pkg      string
	doctor   []tower.Report
}

func (s *netprivBDD) classifies(site, addr string) error {
	file, ok := siteFiles[site]
	if !ok {
		return fmt.Errorf("unknown site %q", site)
	}
	src, err := os.ReadFile(filepath.Join(s.root, file))
	if err != nil {
		return err
	}
	if !strings.Contains(string(src), netprivImport) {
		return fmt.Errorf("site %q (%s) does not use internal/netpriv: it keeps its own rule", site, file)
	}
	ip := net.ParseIP(addr)
	if ip == nil {
		return fmt.Errorf("%q is not an address", addr)
	}
	s.site, s.class = site, netpriv.Classify(ip).String()
	return nil
}

func (s *netprivBDD) answerIs(want string) error {
	if s.class != want {
		return fmt.Errorf("%s classified it %q, want %q", s.site, s.class, want)
	}
	return nil
}

func (s *netprivBDD) scanTree() error {
	s.findings = nil
	return filepath.WalkDir(s.root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "web":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(s.root, p)
		rel = filepath.ToSlash(rel)
		if allowedRule(rel) {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			if privateRule.MatchString(line) {
				s.findings = append(s.findings, fmt.Sprintf("%s:%d: %s", rel, i+1, strings.TrimSpace(line)))
			}
		}
		return nil
	})
}

func (s *netprivBDD) findsNone() error {
	if len(s.findings) > 0 {
		return fmt.Errorf("private-address rules outside internal/netpriv:\n  %s", strings.Join(s.findings, "\n  "))
	}
	return nil
}

func (s *netprivBDD) egressUsesNetpriv() error {
	src, err := os.ReadFile(filepath.Join(s.root, "internal/tower/egress.go"))
	if err != nil {
		return err
	}
	if !strings.Contains(string(src), netprivImport) || strings.Contains(string(src), "mustCIDRs(") {
		return fmt.Errorf("internal/tower/egress.go still keeps its own CIDR table")
	}
	return nil
}

func (s *netprivBDD) egressUnchanged() error {
	g := tower.NewEgressGuard(nil)
	for _, a := range []string{"127.0.0.1:5432", "[::1]:5432", "10.1.2.3:5432", "172.20.0.1:5432", "192.168.1.9:5432", "[fd00::5]:5432"} {
		if err := g.Allow(a); err != nil {
			return fmt.Errorf("%s refused: %v", a, err)
		}
	}
	for _, a := range []string{"169.254.169.254:80", "[fe80::1]:80", "100.64.0.9:80", "8.8.8.8:53", "172.32.0.1:80"} {
		if err := g.Allow(a); err == nil {
			return fmt.Errorf("%s allowed, want refused", a)
		}
	}
	return nil
}

func (s *netprivBDD) doctorChecks(a, b string) error {
	s.doctor = nil
	for _, bind := range []string{a, b} {
		c := &tower.Config{Mode: tower.ModeStandalone, Hub: &tower.HubConfig{Address: bind}}
		s.doctor = append(s.doctor, tower.Doctor(c))
	}
	return nil
}

func (s *netprivBDD) bothLoopback() error {
	for _, r := range s.doctor {
		if !r.AllListenersLoopback {
			return fmt.Errorf("doctor called %v not loopback: %v", r.Listeners, r.Notes)
		}
	}
	return nil
}

func (s *netprivBDD) depsOf(pkg string) error {
	s.pkg = pkg
	out, err := exec.Command("go", "list", "-deps", "rogerai.fm/roger/v6/"+pkg).Output()
	if err != nil {
		return fmt.Errorf("go list -deps %s: %v", pkg, err)
	}
	s.deps = strings.Fields(string(out))
	return nil
}

func (s *netprivBDD) importsNone(forbidden ...string) error {
	for _, d := range s.deps {
		for _, f := range forbidden {
			if d == "rogerai.fm/roger/v6/"+f {
				return fmt.Errorf("%s imports %s", s.pkg, f)
			}
		}
	}
	return nil
}

func (s *netprivBDD) onlyStdlib() error {
	for _, d := range s.deps {
		if strings.Contains(strings.SplitN(d, "/", 2)[0], ".") && d != "rogerai.fm/roger/v6/internal/netpriv" {
			return fmt.Errorf("internal/netpriv imports %s", d)
		}
	}
	return nil
}

func TestNetprivBDD(t *testing.T) {
	s := &netprivBDD{root: repoRoot(t)}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.site, s.class, s.findings, s.deps, s.pkg, s.doctor = "", "", nil, nil, "", nil
				return ctx, nil
			})
			sc.Step(`^"([^"]*)" classifies "([^"]*)"$`, s.classifies)
			sc.Step(`^the answer is "([^"]*)"$`, s.answerIs)
			sc.Step(`^the source tree$`, func() error { return nil })
			sc.Step(`^a structural test searches for private-address CIDR tables and IsPrivate calls outside internal/netpriv$`, s.scanTree)
			sc.Step(`^it finds none, except protocol\.TrustedBase's loopback-only key-trust rule, which is a different question and is named in the test's allow-list$`, s.findsNone)
			sc.Step(`^internal/tower/egress\.go uses internal/netpriv$`, s.egressUsesNetpriv)
			sc.Step(`^its allow-list behavior is unchanged for loopback, RFC1918 and ULA$`, s.egressUnchanged)
			sc.Step(`^doctor checks a config whose bind is "([^"]*)" or "([^"]*)"$`, s.doctorChecks)
			sc.Step(`^both are reported as loopback$`, s.bothLoopback)
			sc.Step(`^internal/lanmode$`, func() error { s.pkg = "internal/lanmode"; return nil })
			sc.Step(`^its dependency graph is inspected$`, func() error { return s.depsOf(s.pkg) })
			sc.Step(`^it does not import internal/localplane or internal/tower$`, func() error {
				return s.importsNone("internal/localplane", "internal/tower")
			})
			sc.Step(`^internal/netpriv's dependency graph is inspected$`, func() error { return s.depsOf("internal/netpriv") })
			sc.Step(`^it imports only the standard library$`, s.onlyStdlib)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{
				"../../features/packaging/one_binary.feature:401",
				"../../features/packaging/one_binary.feature:422",
				"../../features/packaging/one_binary.feature:427",
				"../../features/packaging/one_binary.feature:431",
				"../../features/packaging/one_binary.feature:444",
				"../../features/packaging/one_binary.feature:450",
			},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("one_binary.feature section 7.1 failed")
	}
}
