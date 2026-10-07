package serve_test

// Executable spec: features/packaging/one_binary.feature section 4, the Core-free guarantee
// re-expressed: a package-level dependency proof on internal/localplane/serve, source gates, one
// shared structural helper, and the standalone runtime guard (internal/egressguard) with the
// startup assertion (internal/coreclient).
//
// "The guard is the process's default transport, not an opt-in" (line 293) runs in the wording
// the founder approved 2026-10-07: the default client, transport and resolver refuse public
// addresses, and a direct net.Dial is forbidden in the serve package by the source gate (Go has
// no process-wide hook for net.Dial).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/coreclient"
	"rogerai.fm/roger/v6/internal/egressguard"
	"rogerai.fm/roger/v6/internal/localplane/serve"
	"rogerai.fm/roger/v6/internal/structural"
	"rogerai.fm/roger/v6/internal/tower"
)

// recordT is a structural.T that records failures instead of failing the scenario.
type recordT struct{ failed []string }

func (r *recordT) Helper() {}
func (r *recordT) Fatalf(format string, args ...any) {
	r.failed = append(r.failed, fmt.Sprintf(format, args...))
	panic(r) // stop like a real Fatalf
}

func catch(r *recordT, fn func()) (failed string) {
	defer func() {
		if p := recover(); p != nil {
			if p != r {
				panic(p)
			}
			failed = r.failed[len(r.failed)-1]
		}
	}()
	fn()
	return ""
}

type guaranteeBDD struct {
	t         *testing.T
	pkg       string
	deps      []string
	log       bytes.Buffer
	uninstall func()
	err       error
	resp      *http.Response
	out       bytes.Buffer
	paths     map[string]error
}

func (s *guaranteeBDD) reset() {
	s.pkg, s.deps, s.err, s.resp = "", nil, nil, nil
	s.log.Reset()
	s.out.Reset()
	coreclient.Reset()
}

func (s *guaranteeBDD) cleanup() {
	if s.uninstall != nil {
		s.uninstall()
		s.uninstall = nil
	}
	coreclient.Reset()
}

var serveForbidden = []string{
	"rogerai.fm/roger/v6/internal/towerjoin",
	"rogerai.fm/roger/v6/internal/towercore",
	"rogerai.fm/roger/v6/internal/towerhub",
	"rogerai.fm/roger/v6/internal/client",
	"rogerai.fm/roger/v6/internal/agent",
}

func (s *guaranteeBDD) importsNoCore() error {
	s.deps = structural.Deps(s.t, "rogerai.fm/roger/v6/internal/localplane/serve")
	structural.ForbidDeps(s.t, "rogerai.fm/roger/v6/internal/localplane/serve", serveForbidden[:3])
	return nil
}

func (s *guaranteeBDD) importsNeitherClientNorAgent() error {
	structural.ForbidDeps(s.t, "rogerai.fm/roger/v6/internal/localplane/serve", serveForbidden[3:])
	return nil
}

func (s *guaranteeBDD) failsIfAdded() error {
	// The same helper refuses a package that does import a forbidden one.
	r := &recordT{}
	if catch(r, func() { structural.ForbidDeps(r, "rogerai.fm/roger/v6/internal/client", serveForbidden[3:4]) }) == "" {
		return fmt.Errorf("the dependency check did not fail on a forbidden import")
	}
	return nil
}

func (s *guaranteeBDD) sourceGates() error {
	plane := structural.ScanSource(s.t, "../", structural.NoOutbound, structural.NoListen)
	srv := structural.ScanSource(s.t, ".", structural.NoOutbound)
	if plane < 2 || srv < 1 {
		return fmt.Errorf("the gates read %d and %d files", plane, srv)
	}
	return nil
}

func (s *guaranteeBDD) sharedHelper() error {
	for _, f := range []string{
		"structural_test.go",
		"../../edgeauth/structural_test.go",
	} {
		src, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		if !strings.Contains(string(src), "structural.ForbidDeps(") {
			return fmt.Errorf("%s does not use the shared structural helper", f)
		}
	}
	return nil
}

func (s *guaranteeBDD) helperFailsCorrectly() error {
	r := &recordT{}
	if catch(r, func() {
		structural.ForbidDeps(r, "rogerai.fm/roger/v6/internal/client", []string{"rogerai.fm/roger/v6/internal/protocol"})
	}) == "" {
		return fmt.Errorf("no failure on a forbidden prefix")
	}
	old := structural.GoTool
	structural.GoTool = "go-that-does-not-exist"
	msg := catch(r, func() { structural.Deps(r, "rogerai.fm/roger/v6/internal/netpriv") })
	structural.GoTool = old
	if msg == "" {
		return fmt.Errorf("no failure on a missing go toolchain")
	}
	if catch(r, func() { structural.DepsAtLeast(r, "rogerai.fm/roger/v6/internal/netpriv", 1000) }) == "" {
		return fmt.Errorf("no failure on a suspiciously tiny dependency list")
	}
	return nil
}

func (s *guaranteeBDD) standaloneRunning() error {
	s.uninstall = egressguard.Install(&s.log)
	return nil
}

func (s *guaranteeBDD) connects(addr string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := egressguard.DialContext(ctx, "tcp", addr)
	if conn != nil {
		conn.Close()
	}
	s.err = err
	return nil
}

func (s *guaranteeBDD) triesPublic(addr string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+"/", nil)
	s.resp, s.err = http.DefaultClient.Do(req)
	if s.resp != nil {
		s.resp.Body.Close()
	}
	return nil
}

func (s *guaranteeBDD) refusedBeforeAPacket() error {
	if !errors.Is(s.err, egressguard.ErrRefused) {
		return fmt.Errorf("the connection was not refused by the guard: %v", s.err)
	}
	return nil
}

func (s *guaranteeBDD) refusalLogged() error {
	if !strings.Contains(s.log.String(), "standalone: non-local connection refused") || !strings.Contains(s.log.String(), "93.184.216.34:443") {
		return fmt.Errorf("refusal log %q", s.log.String())
	}
	return nil
}

func (s *guaranteeBDD) keepsRunning() error {
	// The guard returned an error to the caller; nothing panicked or exited, and a local
	// connection still works.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()
	conn, err := egressguard.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		return fmt.Errorf("a local connection after a refusal failed: %v", err)
	}
	conn.Close()
	return nil
}

func (s *guaranteeBDD) connectsByPublicName() error {
	// A name the guard resolves to a public address: the resolver is pointed at a fixed answer.
	egressguard.SetResolver(func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	})
	return s.connects("public.example:443")
}

func (s *guaranteeBDD) refusedByResolvedAddress() error {
	if !errors.Is(s.err, egressguard.ErrRefused) || !strings.Contains(s.err.Error(), "93.184.216.34") {
		return fmt.Errorf("refusal %v does not name the resolved address", s.err)
	}
	return nil
}

func (s *guaranteeBDD) resultIs(want string) error {
	got := "allowed"
	if errors.Is(s.err, egressguard.ErrRefused) {
		got = "refused"
	}
	if got != want {
		return fmt.Errorf("got %s (%v), want %s", got, s.err, want)
	}
	return nil
}

func (s *guaranteeBDD) buggyBuild() error {
	coreclient.Note("a test standing in for a bug")
	return nil
}

func (s *guaranteeBDD) serveStarts() error {
	dir := s.t.TempDir()
	if _, err := tower.Init(dir, tower.ModeStandalone); err != nil {
		return err
	}
	_, s.err = serve.Prepare([]string{"--dir", dir, "--bind", "127.0.0.1:0"}, &s.out)
	return nil
}

func (s *guaranteeBDD) exitsBeforeListening() error {
	if s.err == nil {
		return fmt.Errorf("standalone serve started with a Core client constructed")
	}
	if strings.Contains(s.out.String(), "serving the standalone consumer plane") {
		return fmt.Errorf("the consumer listener opened before the refusal")
	}
	return nil
}

func (s *guaranteeBDD) messageSays() error {
	if !strings.Contains(s.err.Error(), "a Core client was constructed in standalone mode") {
		return fmt.Errorf("message %q", s.err)
	}
	return nil
}

func (s *guaranteeBDD) joinedRunning() error {
	// Joined mode never installs the guard: the default transport is the stock one.
	if _, ok := http.DefaultTransport.(*http.Transport); !ok {
		return fmt.Errorf("the default transport is replaced outside standalone serve")
	}
	return nil
}

var guardCall = regexp.MustCompile(`egressguard\.Install\b`)

func (s *guaranteeBDD) joinedMayReachCore() error {
	// Only the standalone serve installs the guard; nothing else in the tree does.
	root := "../../.."
	var installers []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "web":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if guardCall.Match(b) {
			installers = append(installers, filepath.ToSlash(p))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(installers) != 1 || !strings.HasSuffix(installers[0], "internal/localplane/serve/serve.go") {
		return fmt.Errorf("the guard is installed by %v, want only the standalone serve", installers)
	}
	return nil
}

// defaultPaths drives each process default toward a public address: http.DefaultClient,
// http.DefaultTransport directly, and the default resolver's own dial to a public DNS server.
func (s *guaranteeBDD) defaultPaths() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.paths = map[string]error{}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://93.184.216.34/", nil)
	resp, err := http.DefaultClient.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	s.paths["client"] = err
	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://93.184.216.34/", nil)
	resp, err = http.DefaultTransport.RoundTrip(req2)
	if resp != nil {
		resp.Body.Close()
	}
	s.paths["transport"] = err
	if net.DefaultResolver.Dial == nil {
		s.paths["resolver"] = nil
	} else {
		conn, err := net.DefaultResolver.Dial(ctx, "udp", "8.8.8.8:53")
		if conn != nil {
			conn.Close()
		}
		s.paths["resolver"] = err
	}
	return nil
}

func (s *guaranteeBDD) defaultPathsRefused() error {
	for _, p := range []string{"client", "transport", "resolver"} {
		if !errors.Is(s.paths[p], egressguard.ErrRefused) {
			return fmt.Errorf("the default %s was not refused by the guard: %v", p, s.paths[p])
		}
	}
	return nil
}

// directDialForbidden: the serve package passes the source gate, and the same gate refuses a
// file that calls net.Dial directly (so the gate is what holds the line, not a convention).
func (s *guaranteeBDD) directDialForbidden() error {
	if structural.ScanSource(s.t, ".", structural.NoOutbound) < 1 {
		return fmt.Errorf("the gate read no serve source")
	}
	dir := s.t.TempDir()
	src := "package x\n\nimport \"net\"\n\nfunc f() { _, _ = net.Dial(\"tcp\", \"93.184.216.34:443\") }\n"
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o600); err != nil {
		return err
	}
	r := &recordT{}
	if catch(r, func() { structural.ScanSource(r, dir, structural.NoOutbound) }) == "" {
		return fmt.Errorf("the source gate let a direct net.Dial through")
	}
	return nil
}

func TestCoreFreeGuaranteeBDD(t *testing.T) {
	s := &guaranteeBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { s.reset(); return ctx, nil })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				s.cleanup()
				return ctx, nil
			})
			sc.Step(`^the package internal/localplane/serve$`, func() error { return nil })
			sc.Step(`^its full dependency graph is inspected$`, func() error { return nil })
			sc.Step(`^it imports none of internal/towerjoin, internal/towercore and its subpackages, or internal/towerhub$`, s.importsNoCore)
			sc.Step(`^it imports neither internal/client nor internal/agent$`, s.importsNeitherClientNorAgent)
			sc.Step(`^the check fails the build if any is added$`, s.failsIfAdded)
			sc.Step(`^internal/localplane and internal/localplane/serve$`, func() error { return nil })
			sc.Step(`^the source-scan gates run$`, s.sourceGates)
			sc.Step(`^internal/localplane forbids every outbound call and listener, as before$`, func() error { return nil })
			sc.Step(`^internal/localplane/serve forbids every outbound call \(it may listen\)$`, func() error { return nil })
			sc.Step(`^the structural tests for localplane/serve, edgeauth and any later Core-free package$`, func() error { return nil })
			sc.Step(`^they run$`, func() error { return nil })
			sc.Step(`^each calls the same shared helper with its forbidden list$`, s.sharedHelper)
			sc.Step(`^the helper fails on a forbidden prefix, on a missing go toolchain, and on a suspiciously tiny dependency list$`, s.helperFailsCorrectly)
			sc.Step(`^roger tower serve running a standalone directory$`, s.standaloneRunning)
			sc.Step(`^any code in the process tries to connect to ([0-9.:]+)$`, s.triesPublic)
			sc.Step(`^the connection is refused before a packet is sent$`, s.refusedBeforeAPacket)
			sc.Step(`^the refusal is logged with the destination and the words "standalone: non-local connection refused"$`, s.refusalLogged)
			sc.Step(`^the serve keeps running$`, s.keepsRunning)
			sc.Step(`^any code in the process connects by a name that resolves to a public address$`, s.connectsByPublicName)
			sc.Step(`^the connection is refused using the resolved address, not the name$`, s.refusedByResolvedAddress)
			sc.Step(`^the process connects to "([^"]*)"$`, s.connects)
			sc.Step(`^the connection is "([^"]*)"$`, s.resultIs)
			sc.Step(`^a build in which standalone startup constructs a Core client \(a bug\)$`, s.buggyBuild)
			sc.Step(`^roger tower serve starts on a standalone directory$`, s.serveStarts)
			sc.Step(`^it exits non-zero before opening the consumer listener$`, s.exitsBeforeListening)
			sc.Step(`^the message says a Core client was constructed in standalone mode$`, s.messageSays)
			sc.Step(`^roger tower serve running a joined directory$`, s.joinedRunning)
			sc.Step(`^it may connect to Roger Core as before$`, s.joinedMayReachCore)
			sc.Step(`^"roger use", "roger share" and the TUI are unaffected by the standalone guard$`, s.joinedMayReachCore)
			sc.Step(`^code uses http\.DefaultClient, http\.DefaultTransport or the default resolver to reach a public address$`, s.defaultPaths)
			sc.Step(`^the default HTTP client, the default transport and the default resolver refuse public addresses$`, s.defaultPathsRefused)
			sc.Step(`^a direct net\.Dial is forbidden in the serve package by the source gate$`, s.directDialForbidden)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{
				"../../../features/packaging/one_binary.feature:237",
				"../../../features/packaging/one_binary.feature:244",
				"../../../features/packaging/one_binary.feature:250",
				"../../../features/packaging/one_binary.feature:256",
				"../../../features/packaging/one_binary.feature:263",
				"../../../features/packaging/one_binary.feature:268",
				"../../../features/packaging/one_binary.feature:287",
				"../../../features/packaging/one_binary.feature:293",
				"../../../features/packaging/one_binary.feature:300",
			},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("one_binary.feature section 4 failed")
	}
}
