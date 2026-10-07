package main

// Executable spec: features/lan/discovery_and_dial.feature, the local-address rule and
// `roger lan add|rm` (increment a). A real standalone Tower in-process answers the probe;
// lanResolve is pointed at scenario DNS answers; the config is isolated.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/lanmode"
	"rogerai.fm/roger/v6/internal/netpriv"
)

type lanAddBDD struct {
	t       *testing.T
	dial    *lanDialMap
	dns     map[string][]net.IP
	out     string
	err     error
	verdict lanmode.Verdict
	ran     bool // a command has run, so "lan_towers contains" is now an assertion, not a seed
}

func (s *lanAddBDD) reset() {
	useTempConfig(s.t)
	s.dial = &lanDialMap{}
	s.dns = map[string][]net.IP{}
	lanProbeHTTP = s.dial.client()
	lanResolve = func(_ context.Context, host string) ([]net.IP, error) {
		if ips, ok := s.dns[host]; ok {
			return ips, nil
		}
		return nil, fmt.Errorf("no such host %s", host)
	}
	s.out, s.err, s.ran = "", nil, false
	// The spec's named Tower addresses are standalone Towers unless a scenario says otherwise
	// (a later Given re-points or removes them).
	for _, a := range []string{"192.168.1.20:8787", "100.64.3.9:8787"} {
		s.dial.set(a, newRealTower(s.t).addr())
	}
}

func (s *lanAddBDD) restore() {
	lanProbeHTTP = &http.Client{}
	lanResolve = lanmode.DefaultResolver
}

func (s *lanAddBDD) run(line string) error {
	out, err := captureEdgeStdout(func() error { return dispatch(loadConfig(), strings.Fields(line)[1:]) })
	s.out, s.err, s.ran = out, err, true
	if err != nil {
		s.out += "\nerror: " + err.Error()
	}
	return nil
}

func (s *lanAddBDD) classify(addr string) error {
	a, err := lanmode.ParseAddr(addr)
	if err != nil {
		s.verdict = lanmode.Verdict{Reason: err.Error()}
		return nil
	}
	s.verdict = lanmode.Classify(context.Background(), a, lanResolve)
	return nil
}

func (s *lanAddBDD) itIs(want string) error {
	got := "not local"
	if s.verdict.Local {
		got = "local"
	}
	if got != want {
		return fmt.Errorf("classified %q (%s), want %q", got, s.verdict.Reason, want)
	}
	return nil
}

func (s *lanAddBDD) resolvesTo(host, a, b string) error {
	s.dns[host] = []net.IP{net.ParseIP(a), net.ParseIP(b)}
	return nil
}

func (s *lanAddBDD) hostIs(host, want string) error {
	if err := s.classify(host + ":8787"); err != nil {
		return err
	}
	return s.itIs(want)
}

func (s *lanAddBDD) towerAt(addr string) error {
	tw := newRealTower(s.t)
	s.dial.set(addr, tw.addr())
	return nil
}

func (s *lanAddBDD) answersNotTower(addr string) error {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	s.t.Cleanup(srv.Close)
	s.dial.set(addr, srv.Listener.Addr().String())
	return nil
}

func (s *lanAddBDD) nothingListening(string) error { return nil }

func (s *lanAddBDD) towers() []string {
	var got []string
	_ = json.Unmarshal(readRawConfig(configPath())["lan_towers"], &got)
	return got
}

func (s *lanAddBDD) listContains(addr string) error {
	for _, a := range s.towers() {
		if a == addr {
			return nil
		}
	}
	return fmt.Errorf("lan_towers %v lacks %q\n%s", s.towers(), addr, s.out)
}

func (s *lanAddBDD) listContainsOnce(addr string) error {
	n := 0
	for _, a := range s.towers() {
		if a == addr {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("lan_towers %v has %q %d times", s.towers(), addr, n)
	}
	return nil
}

func (s *lanAddBDD) listUnchanged() error {
	if len(s.towers()) != 0 {
		return fmt.Errorf("lan_towers changed: %v", s.towers())
	}
	return nil
}

func (s *lanAddBDD) seedList(a, b string) error {
	c := loadConfig()
	c.LanTowers = append(c.LanTowers, a)
	if b != "" {
		c.LanTowers = append(c.LanTowers, b)
	}
	return saveConfig(c)
}

func (s *lanAddBDD) listContainsOnly(addr string) error {
	if got := s.towers(); len(got) != 1 || got[0] != addr {
		return fmt.Errorf("lan_towers = %v, want only %q", got, addr)
	}
	return nil
}

func (s *lanAddBDD) outputHas(parts ...string) error {
	for _, p := range parts {
		if !strings.Contains(s.out, p) {
			return fmt.Errorf("output lacks %q:\n%s", p, s.out)
		}
	}
	return nil
}

func (s *lanAddBDD) exitsNonZero() error {
	if s.err == nil {
		return fmt.Errorf("exited 0:\n%s", s.out)
	}
	return nil
}

func TestLANAddBDD(t *testing.T) {
	s := &lanAddBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { s.reset(); return ctx, nil })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				s.restore()
				return ctx, nil
			})
			sc.Step(`^LAN mode is on$`, func() error { return nil }) // classification and `roger lan` are mode-independent
			sc.Step(`^the client's check is the private-address check \(netpriv\.IsHome\) plus link-local, for every address in the classification table$`, func() error {
				for _, a := range []string{"127.0.0.1", "::1", "10.0.0.5", "172.16.0.9", "172.31.255.254", "172.32.0.1", "192.168.1.69",
					"fd12:3456::1", "169.254.10.10", "fe80::1", "100.64.0.1", "8.8.8.8", "0.0.0.0", "::", "255.255.255.255", "224.0.0.251"} {
					ip := net.ParseIP(a)
					want := netpriv.IsHome(ip) || netpriv.Classify(ip) == netpriv.LinkLocal
					if got := lanmode.IsLocalIP(ip); got != want {
						return fmt.Errorf("%s: client check %v, IsHome plus link-local %v", a, got, want)
					}
				}
				return nil
			})
			sc.Step(`^the address "([^"]*)" is classified$`, s.classify)
			sc.Step(`^it is "([^"]*)"$`, s.itIs)
			sc.Step(`^"([^"]*)" resolves to ([0-9.]+) and ([0-9.]+)$`, s.resolvesTo)
			sc.Step(`^"([^"]*)" is (local|not local)$`, s.hostIs)
			sc.Step(`^they run "([^"]*)"$`, s.run)
			sc.Step(`^a standalone Tower answers at ([0-9.:]+)$`, s.towerAt)
			sc.Step(`^the config's lan_towers list contains "([^"]*)"$`, func(a string) error { return s.listContains(a) })
			sc.Step(`^lan_towers contains "([^"]*)"$`, func(a string) error {
				if !s.ran {
					return s.seedList(a, "")
				}
				return s.listContains(a)
			})
			sc.Step(`^the output says it was added and whether it answered as a standalone Tower$`, func() error {
				return s.outputHas("added", "answered as a standalone Tower")
			})
			sc.Step(`^it exits non-zero$`, s.exitsNonZero)
			sc.Step(`^the error says LAN mode only uses local addresses and lists the local ranges$`, func() error {
				return s.outputHas("LAN mode only uses local addresses", "192.168.0.0/16", "fc00::/7")
			})
			sc.Step(`^lan_towers is unchanged$`, s.listUnchanged)
			sc.Step(`^the output notes it is a carrier/overlay range that LAN mode uses only because it was added by hand$`, func() error {
				return s.outputHas("carrier/overlay", "added it by hand")
			})
			sc.Step(`^([0-9.:]+) answers HTTP but /local/poll does not return the standalone fingerprint$`, s.answersNotTower)
			sc.Step(`^it exits non-zero naming that the address is not a standalone Tower$`, func() error {
				if err := s.exitsNonZero(); err != nil {
					return err
				}
				return s.outputHas("not a standalone Tower")
			})
			sc.Step(`^nothing is listening at ([0-9.:]+)$`, s.nothingListening)
			sc.Step(`^the output says it did not answer and will be tried at each refresh$`, func() error {
				return s.outputHas("did not answer", "tried at each refresh")
			})
			sc.Step(`^lan_towers contains "([^"]*)" and "([^"]*)"$`, s.seedList)
			sc.Step(`^lan_towers contains only "([^"]*)"$`, s.listContainsOnly)
			sc.Step(`^lan_towers contains "([^"]*)" once$`, s.listContainsOnce)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{
				"../../features/lan/discovery_and_dial.feature:63",
				"../../features/lan/discovery_and_dial.feature:87",
				"../../features/lan/discovery_and_dial.feature:104",
				"../../features/lan/discovery_and_dial.feature:112",
				"../../features/lan/discovery_and_dial.feature:118",
				"../../features/lan/discovery_and_dial.feature:125",
				"../../features/lan/discovery_and_dial.feature:131",
				"../../features/lan/discovery_and_dial.feature:138",
				"../../features/lan/discovery_and_dial.feature:153",
			},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("discovery_and_dial.feature (increment a) failed")
	}
}
