package netbind_test

// Executable spec: features/packaging/one_binary.feature section 7.4, one bind helper. Each
// listener in the outline is a real source file that must bind through internal/netbind, and
// the outline then exercises the netbind function that site calls, on real sockets.

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"rogerai.fm/roger/v6/internal/bddrun"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/netbind"
)

const netbindImport = `"rogerai.fm/roger/v6/internal/netbind"`

// listenerFiles is where each listener binds.
var listenerFiles = map[string]string{
	"standalone serve": "internal/localplane/serve/serve.go",
	"edge face":        "cmd/rogerai/edgebind.go",
	"web console":      "internal/webui/server.go",
	"joined hub":       "internal/towercli/hub.go",
	"tui proxy":        "internal/tui/tui.go",
}

func repoRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", err
	}
	return filepath.Dir(strings.TrimSpace(string(out))), nil
}

func usesNetbind(root, file string) error {
	src, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		return err
	}
	if !strings.Contains(string(src), netbindImport) {
		return fmt.Errorf("%s does not bind through internal/netbind", file)
	}
	return nil
}

// withPort0 keeps a scenario's host but asks the OS for the port, so a test never depends on
// a fixed port being free on the machine running it.
func withPort0(bind string) string {
	host, _, err := net.SplitHostPort(bind)
	if err != nil {
		return bind
	}
	return net.JoinHostPort(host, "0")
}

type netbindBDD struct {
	root   string
	result string
	ln     net.Listener
	note   string
}

func (s *netbindBDD) close() {
	if s.ln != nil {
		_ = s.ln.Close()
		s.ln = nil
	}
}

func (s *netbindBDD) starts(listener, bind string) error {
	file, ok := listenerFiles[listener]
	if !ok {
		return fmt.Errorf("unknown listener %q", listener)
	}
	if err := usesNetbind(s.root, file); err != nil {
		return err
	}
	switch listener {
	case "standalone serve":
		if _, _, err := netbind.Resolve(bind, false); err != nil {
			if strings.Contains(err.Error(), "--allow-public") {
				s.result = "refused without --allow-public"
			} else {
				s.result = "refused: " + err.Error()
			}
			return nil
		}
		s.result = "listens"
	case "edge face":
		// Two loopback addresses stand in for two private interfaces: both are real sockets.
		addrs := func() ([]net.Addr, error) {
			return []net.Addr{
				&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
				&net.IPNet{IP: net.ParseIP("127.0.0.2"), Mask: net.CIDRMask(8, 32)},
			}, nil
		}
		ln, err := netbind.ListenEach(":0", addrs, func(net.IP) bool { return true })
		if err != nil {
			return err
		}
		s.ln = ln
		addrsBound := netbind.Addrs(ln)
		ports := map[string]bool{}
		for _, a := range addrsBound {
			_, p, _ := net.SplitHostPort(a.String())
			ports[p] = true
		}
		if len(addrsBound) == 2 && len(ports) == 1 {
			s.result = "one listener per private interface, same port"
		} else {
			s.result = fmt.Sprintf("listeners %v", addrsBound)
		}
	case "web console", "tui proxy":
		ln, err := netbind.ListenFree(bind)
		if err != nil {
			return err
		}
		s.ln = ln
		tcp := ln.Addr().(*net.TCPAddr)
		if tcp.IP.IsLoopback() && tcp.Port != 0 {
			s.result = "listens on loopback, free port"
		} else {
			s.result = "listens on " + ln.Addr().String()
		}
	case "joined hub":
		ln, note, err := netbind.Stated("hub", withPort0(bind))
		if err != nil {
			return err
		}
		s.ln, s.note = ln, note
		if strings.Contains(note, "hub listening on") && strings.Contains(note, "(public") {
			s.result = "listens, with the posture logged"
		} else {
			s.result = "listens, logged " + note
		}
	}
	return nil
}

func (s *netbindBDD) resultIs(want string) error {
	if s.result != want {
		return fmt.Errorf("got %q, want %q", s.result, want)
	}
	return nil
}

// freePortSites must pick a free port through netbind and keep no probe-then-rebind helper.
var freePortSites = []string{"internal/tui/tui.go", "internal/webui/server.go", "cmd/rogerai/onboard.go"}

var probeHelper = regexp.MustCompile(`func (listenFreePort|freePort)\(`)

func (s *netbindBDD) freePortThroughNetbind() error {
	for _, f := range freePortSites {
		if err := usesNetbind(s.root, f); err != nil {
			return err
		}
	}
	return nil
}

func (s *netbindBDD) noProbeRebind() error {
	return filepath.WalkDir(s.root, func(p string, d os.DirEntry, err error) error {
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
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if probeHelper.Match(src) {
			rel, _ := filepath.Rel(s.root, p)
			return fmt.Errorf("%s still keeps its own free-port helper", rel)
		}
		return nil
	})
}

func TestNetbindBDD(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	s := &netbindBDD{root: root}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.result, s.note = "", ""
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				s.close()
				return ctx, nil
			})
			sc.Step(`^"([^"]*)" starts with bind "([^"]*)"$`, s.starts)
			sc.Step(`^the result is "([^"]*)"$`, s.resultIs)
			sc.Step(`^the joined hub starts on "([^"]*)"$`, func(bind string) error { return s.starts("joined hub", bind) })
			sc.Step(`^it listens$`, func() error {
				if s.ln == nil {
					return fmt.Errorf("no listener")
				}
				return nil
			})
			sc.Step(`^the log states the posture$`, func() error {
				if !strings.Contains(s.note, "hub listening on") || !strings.Contains(s.note, "(public") {
					return fmt.Errorf("posture line %q", s.note)
				}
				return nil
			})
			sc.Step(`^the TUI, the web console and onboarding pick a free port through netbind$`, s.freePortThroughNetbind)
			sc.Step(`^none of them closes a probe listener and binds the port again$`, s.noProbeRebind)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{
				"../../features/packaging/one_binary.feature:490",
				"../../features/packaging/one_binary.feature:503",
				"../../features/packaging/one_binary.feature:512",
			},
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("one_binary.feature section 7.4 failed")
	}
}
