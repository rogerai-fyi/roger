package main

// Executable spec: features/edge/scan_on_request.feature (@cli). A machine outside an Edge scans
// the LAN only on request (founder ruling 2026-10-07).
//
// "Binds no socket" is measured twice, so neither can pass vacuously:
//   - the process's own sockets: /proc/self/fd joined to /proc/net/{tcp,tcp6,udp,udp6}, compared
//     with a baseline taken before the host started (Linux; elsewhere the scenario is skipped);
//   - every browse transport the discovery engine opens goes through a recording wrapper around
//     the REAL multicast transport, so an attempt is counted even where multicast is unavailable.

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/bddrun"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edge/wire"
	"rogerai.fm/roger/v6/internal/tui"
)

// ---- the process's own sockets -------------------------------------------------------------

type procSocket struct {
	proto string // tcp, tcp6, udp, udp6
	port  int
	state string // hex TCP state; 0A is LISTEN
}

// procSockets is every socket this process holds, keyed by inode.
func procSockets() (map[string]procSocket, error) {
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return nil, err
	}
	mine := map[string]bool{}
	for _, e := range ents {
		l, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
		if err == nil && strings.HasPrefix(l, "socket:[") {
			mine[strings.TrimSuffix(strings.TrimPrefix(l, "socket:["), "]")] = true
		}
	}
	out := map[string]procSocket{}
	for _, proto := range []string{"tcp", "tcp6", "udp", "udp6"} {
		f, err := os.Open("/proc/self/net/" + proto)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Scan() // header
		for sc.Scan() {
			fl := strings.Fields(sc.Text())
			if len(fl) < 10 || !mine[fl[9]] {
				continue
			}
			local := fl[1]
			port, _ := strconv.ParseInt(local[strings.LastIndex(local, ":")+1:], 16, 32)
			out[fl[9]] = procSocket{proto: proto, port: int(port), state: fl[3]}
		}
		f.Close()
	}
	return out, nil
}

// ---- a recording browse transport ----------------------------------------------------------

// planeRecorder wraps the real multicast transport. A transport it could not open for real
// (no multicast on this host) is still counted, and stands in as an open fake until closed.
type planeRecorder struct {
	mu       sync.Mutex
	attempts int
	live     int
}

type recordedTransport struct {
	wire.Transport
	r      *planeRecorder
	closed sync.Once
}

func (t *recordedTransport) Close() error {
	var err error
	t.closed.Do(func() {
		err = t.Transport.Close()
		t.r.mu.Lock()
		t.r.live--
		t.r.mu.Unlock()
	})
	return err
}

// idleTransport stands in where the host has no multicast: it sends nowhere and every read
// times out, the ordinary end of a browse window.
type idleTransport struct{ deadline time.Time }

type idleTimeout struct{}

func (idleTimeout) Error() string   { return "idle transport: timeout" }
func (idleTimeout) Timeout() bool   { return true }
func (idleTimeout) Temporary() bool { return true }

func (*idleTransport) Send([]byte) error { return nil }
func (t *idleTransport) Recv([]byte) (int, net.Addr, error) {
	if d := time.Until(t.deadline); d > 0 {
		time.Sleep(d)
	}
	return 0, nil, idleTimeout{}
}
func (t *idleTransport) SetReadDeadline(d time.Time) error { t.deadline = d; return nil }
func (*idleTransport) Close() error                        { return nil }

func (r *planeRecorder) plane(ifaces func() []net.Interface) func() (edge.Transport, error) {
	return func() (edge.Transport, error) {
		r.mu.Lock()
		r.attempts++
		r.live++
		r.mu.Unlock()
		var inner wire.Transport = &idleTransport{}
		if real, err := wire.NewRejoiningMulticastTransport(ifaces); err == nil {
			inner = real
		}
		return &recordedTransport{Transport: inner, r: r}, nil
	}
}

func (r *planeRecorder) counts() (attempts, live int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts, r.live
}

// ---- the suite -----------------------------------------------------------------------------

type scanBDD struct {
	t         *testing.T
	baseline  map[string]procSocket
	plane     *planeRecorder
	lanBrowse int
	stopHost  func()
	hooks     tui.Hooks
	endScope  func()
	prevOpts  func(*edge.Fleet) edge.Options
	prevLAN   func(context.Context) (wire.BrowseResult, error)
	prevLease time.Duration
}

func (s *scanBDD) reset(t *testing.T) {
	s.t = t
	useTempConfig(t)
	t.Setenv("ROGER_BROKER", "http://127.0.0.1:1")
	t.Setenv(edge.EnvInterval, "150ms")
	t.Setenv("ROGERAI_EDGE_DISCOVERY", "")
	t.Setenv("ROGER_LAN", "")
	edgeScans = newScanScopes()
	s.plane = &planeRecorder{}
	s.lanBrowse = 0
	s.stopHost, s.endScope = nil, nil
	s.hooks = tui.Hooks{Station: "gentle-mongoose-93"}
	s.prevOpts, s.prevLAN, s.prevLease = edgeDiscoveryOptions, lanBrowseTowers, edgeConsoleLease
	edgeConsoleLease = 400 * time.Millisecond
	edgeDiscoveryOptions = func(f *edge.Fleet) edge.Options {
		o := defaultEdgeDiscoveryOptions(f)
		ifaces := func() []net.Interface {
			l, err := net.Interfaces()
			if err != nil {
				return nil
			}
			return l
		}
		o.Interfaces = ifaces
		o.Plane = s.plane.plane(ifaces)
		return o
	}
	lanBrowseTowers = func(context.Context) (wire.BrowseResult, error) {
		s.lanBrowse++
		return wire.BrowseResult{}, nil
	}
	s.baseline, _ = procSockets()
}

func (s *scanBDD) cleanup() {
	if s.endScope != nil {
		s.endScope()
	}
	if s.stopHost != nil {
		s.stopHost()
	}
	edgeDiscoveryOptions, lanBrowseTowers, edgeConsoleLease = s.prevOpts, s.prevLAN, s.prevLease
}

func (s *scanBDD) fresh() error {
	if edgeInAnEdge() {
		return fmt.Errorf("the temp config already holds an Edge identity or root")
	}
	return nil
}

func (s *scanBDD) joined() error {
	enrollThisMachine(s.t, "workshop")
	s.baseline, _ = procSockets() // enrollment itself opens nothing that stays; re-take it anyway
	return nil
}

// startHost stands up the Edge host exactly as the TUI, `roger resume` and `roger webui` do.
func (s *scanBDD) startHost() error {
	if s.stopHost == nil {
		s.stopHost = startEdge(&s.hooks)
	}
	return nil
}

func (s *scanBDD) passes() error {
	time.Sleep(5 * 150 * time.Millisecond)
	return nil
}

func (s *scanBDD) noNewSocket() error {
	if a, _ := s.plane.counts(); a != 0 {
		return fmt.Errorf("the discovery engine opened a browse transport %d time(s)", a)
	}
	if runtime.GOOS != "linux" {
		return nil
	}
	now, err := procSockets()
	if err != nil {
		return err
	}
	var extra []string
	for ino, sk := range now {
		if _, was := s.baseline[ino]; !was {
			extra = append(extra, fmt.Sprintf("%s:%d(state %s)", sk.proto, sk.port, sk.state))
		}
	}
	if len(extra) > 0 {
		return fmt.Errorf("this process bound new sockets: %s", strings.Join(extra, ", "))
	}
	return nil
}

func (s *scanBDD) nothingOn5353() error {
	if _, live := s.plane.counts(); live != 0 {
		return fmt.Errorf("%d browse transport(s) still open", live)
	}
	if runtime.GOOS != "linux" {
		return nil
	}
	now, err := procSockets()
	if err != nil {
		return err
	}
	for ino, sk := range now {
		if _, was := s.baseline[ino]; !was && strings.HasPrefix(sk.proto, "udp") && sk.port == 5353 {
			return fmt.Errorf("this process holds a UDP socket on 5353")
		}
	}
	return nil
}

func (s *scanBDD) eventually(what string, ok func() bool) error {
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("%s did not happen within a few seconds", what)
}

func (s *scanBDD) browses() error {
	return s.eventually("a browse of the LAN", func() bool { a, live := s.plane.counts(); return a > 0 && live > 0 })
}

func (s *scanBDD) stopsWithin() error {
	if err := s.eventually("the browse stopping", func() bool { _, live := s.plane.counts(); return live == 0 }); err != nil {
		return err
	}
	return s.nothingOn5353()
}

func (s *scanBDD) openEdgeScreen() error {
	if err := s.startHost(); err != nil {
		return err
	}
	s.endScope = edgeScans.Begin(scanEdge) // what the TUI's hook does on [3]
	return nil
}

func (s *scanBDD) leaveEdgeScreen() error {
	if s.endScope == nil {
		return fmt.Errorf("the [3] screen was never opened")
	}
	s.endScope()
	s.endScope = nil
	return nil
}

func (s *scanBDD) consoleReads() error {
	if err := s.startHost(); err != nil {
		return err
	}
	lease := edgeHooksFor(loadConfig(), &s.hooks).ScanLease
	if lease == nil {
		return fmt.Errorf("the console's Edge hooks carry no scan lease")
	}
	lease()
	return nil
}

func (s *scanBDD) consoleStops() error { return nil } // no further reads: the lease runs out

func (s *scanBDD) edgeScan() error {
	runEdgeCLI(s.t, "edge", "scan")
	return nil
}

func (s *scanBDD) browsedDuringRun() error {
	if a, _ := s.plane.counts(); a == 0 {
		return fmt.Errorf("roger edge scan never opened a browse transport")
	}
	return nil
}

func (s *scanBDD) discoveryFact() error {
	if s.hooks.EdgeStatus == nil {
		return fmt.Errorf("the host wired no status")
	}
	var line string
	err := s.eventually("the status reading the open scope", func() bool {
		line = s.hooks.EdgeStatus().DiscoveryLine(time.Now())
		return strings.Contains(line, "while this screen is open")
	})
	if err != nil {
		return fmt.Errorf("DISCOVERY says %q", line)
	}
	return nil
}

func (s *scanBDD) lanOn() error {
	s.t.Setenv("ROGER_LAN", "1")
	return nil
}

func (s *scanBDD) lanDialOpen() error {
	s.endScope = edgeScans.Begin(scanLAN) // what the TUI's hook does while the LAN dial shows
	return nil
}

func (s *scanBDD) lanRefresh() error {
	_ = lanDial(loadConfig())
	return nil
}

// lanSearch runs what `roger --lan search` dispatches to (main strips --lan before dispatch).
func (s *scanBDD) lanSearch() error {
	_, err := captureEdgeStdout(func() error { return cmdLANSearch(loadConfig(), nil) })
	return err
}

func (s *scanBDD) lanBrowses(n int) func() error {
	return func() error {
		if s.lanBrowse != n {
			return fmt.Errorf("%d browse(s) for advertised Towers, want %d", s.lanBrowse, n)
		}
		return nil
	}
}

func TestScanOnRequestFeatureCLI(t *testing.T) {
	st := &scanBDD{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(t); return c, nil })
			sc.After(func(c context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.cleanup()
				return c, nil
			})
			sc.Given(`^a fresh install that has not joined an Edge and holds no Edge root$`, st.fresh)
			sc.Given(`^this machine has joined an Edge$`, st.joined)
			sc.Given(`^the TUI is open on the TUNE IN screen$`, st.startHost)
			sc.Given(`^the web console is open and nobody is on its Edge view$`, st.startHost)
			sc.Given(`^the web console is open$`, st.startHost)
			sc.Given(`^ROGERAI_EDGE_DISCOVERY is "([^"]*)"$`, func(v string) error { t.Setenv("ROGERAI_EDGE_DISCOVERY", v); return nil })
			sc.Given(`^LAN mode is on$`, st.lanOn)
			sc.Given(`^no LAN view is open$`, func() error { return nil })
			sc.Given(`^the LAN dial is open$`, st.lanDialOpen)
			sc.When(`^the Edge host has run several passes$`, st.passes)
			sc.When(`^the owner opens the \[3\] EDGE screen$`, st.openEdgeScreen)
			sc.When(`^the owner leaves the \[3\] EDGE screen$`, st.leaveEdgeScreen)
			sc.When(`^the console's Edge view reads the Edge data$`, st.consoleReads)
			sc.When(`^the console's Edge view stops reading$`, st.consoleStops)
			sc.When(`^the owner runs "roger edge scan"$`, st.edgeScan)
			sc.When(`^the owner runs "roger --lan search"$`, st.lanSearch)
			sc.When(`^the LAN dial refreshes$`, st.lanRefresh)
			sc.Then(`^this process has bound no new socket, TCP or UDP$`, st.noNewSocket)
			sc.Then(`^in particular nothing is bound on UDP port 5353$`, st.nothingOn5353)
			sc.Then(`^nothing is bound on UDP port 5353$`, func() error { st.passes(); return st.noNewSocket() })
			sc.Then(`^this process browses the LAN on UDP port 5353$`, st.browses)
			sc.Then(`^within a few seconds nothing is bound on UDP port 5353$`, st.stopsWithin)
			sc.Then(`^it browsed the LAN during the run$`, st.browsedDuringRun)
			sc.Then(`^after it exits nothing is bound on UDP port 5353$`, st.nothingOn5353)
			sc.Then(`^the DISCOVERY fact says it scans while this screen is open$`, st.discoveryFact)
			sc.Then(`^no browse for advertised Towers was made$`, st.lanBrowses(0))
			sc.Then(`^one browse for advertised Towers was made$`, st.lanBrowses(1))
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@cli",
			Paths: []string{"../../features/edge/scan_on_request.feature"},
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("scan_on_request.feature (@cli) failed")
	}
}
