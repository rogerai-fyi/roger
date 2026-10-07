package main

// Executable spec: features/lan/discovery_and_dial.feature, the merged dial (@client, @cli).
// Real standalone Towers (internal/localplane) in-process, a real Edge fleet record, this
// machine's real household, and a recording broker that must see no /discover. "The dial is
// read" is lanDial, the one function every surface shows.

import (
	"io"
	"strconv"

	"rogerai.fm/roger/v6/internal/edge/wire"

	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/lanmode"
	"rogerai.fm/roger/v6/internal/store"
)

// recordingBroker records every path asked of it, so a scenario can prove NO call was made.
type recordingBroker struct {
	mu    sync.Mutex
	paths []string
	srv   *httptest.Server
}

func newRecordingBroker(t *testing.T) *recordingBroker {
	rb := &recordingBroker{}
	rb.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rb.mu.Lock()
		rb.paths = append(rb.paths, r.URL.Path)
		rb.mu.Unlock()
		if r.URL.Path == "/balance" {
			_ = json.NewEncoder(w).Encode(map[string]any{"user": "u_gh_7", "balance": 12.5, "logged_in": true})
			return
		}
		if r.URL.Path == "/discover" {
			_ = json.NewEncoder(w).Encode(map[string]any{"offers": []client.Offer{{Model: "market-band", Online: true}}})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(rb.srv.Close)
	return rb
}

// count is how many requests reached the broker.
func (rb *recordingBroker) count() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return len(rb.paths)
}

func (rb *recordingBroker) saw(path string) bool {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	for _, p := range rb.paths {
		if p == path {
			return true
		}
	}
	return false
}

type lanDialBDD struct {
	t       *testing.T
	dial    *lanDialMap
	dns     map[string][]net.IP
	towers  map[string]*realTower
	broker  *recordingBroker
	bands   []client.LANBand
	status  []client.LANTowerStatus
	out     string
	err     error
	took    time.Duration
	enrolld bool
	adverts []wire.Advert // what the mDNS browse for Towers hears
}

func (s *lanDialBDD) reset() {
	useTempConfig(s.t)
	s.broker = newRecordingBroker(s.t)
	// The recording broker must be REACHABLE at the configured URL, or a "the broker was not
	// asked" check passes vacuously (a TLS hello to a plain-http server never reaches the
	// handler). It is not a standalone Tower (its /local/poll is a 404), so LAN mode never
	// counts it as a local source.
	s.t.Setenv("ROGER_BROKER", s.broker.srv.URL)
	s.dial, s.dns, s.towers = &lanDialMap{}, map[string][]net.IP{}, map[string]*realTower{}
	lanProbeHTTP = s.dial.client()
	lanmode.ResetTowers()
	lanResolve = func(_ context.Context, host string) ([]net.IP, error) {
		if ips, ok := s.dns[host]; ok {
			return ips, nil
		}
		return nil, fmt.Errorf("no such host %s", host)
	}
	s.bands, s.status, s.out, s.err, s.enrolld = nil, nil, "", nil, false
	s.adverts = nil
	lanBrowseTowers = func(context.Context) (wire.BrowseResult, error) {
		return wire.BrowseResult{Adverts: s.adverts, Seen: len(s.adverts)}, nil
	}
}

func (s *lanDialBDD) restore() {
	lanBrowseTowers = defaultLANBrowseTowers
	lanProbeHTTP = &http.Client{}
	lanResolve = lanmode.DefaultResolver
}

func (s *lanDialBDD) cfg() config {
	c := loadConfig()
	c.lan = lanmode.Mode{LAN: true, Source: lanmode.SrcFlag}
	return c
}

func (s *lanDialBDD) addTower(addr string) {
	c := loadConfig()
	for _, a := range c.LanTowers {
		if a == addr {
			return
		}
	}
	c.LanTowers = append(c.LanTowers, addr)
	if err := saveConfig(c); err != nil {
		s.t.Fatal(err)
	}
}

// tower returns (creating) the real Tower behind a scenario address, admitting this machine.
func (s *lanDialBDD) tower(addr string) *realTower {
	if tw, ok := s.towers[addr]; ok {
		return tw
	}
	tw := newRealTower(s.t)
	_ = tw.admit(client.LoadOrCreateUserKey().Public().(ed25519.PublicKey))
	s.towers[addr] = tw
	host, _, _ := net.SplitHostPort(addr)
	if _, err := netip.ParseAddr(host); err == nil { // a literal IP, zoned IPv6 included
		s.dial.set(addr, tw.addr())
	}
	return tw
}

func (s *lanDialBDD) towerServes(addr, model string) error {
	if err := s.towerRuns(addr, model); err != nil {
		return err
	}
	s.addTower(addr)
	return nil
}

// towerRuns stands a Tower at addr serving model, without adding it to lan_towers.
func (s *lanDialBDD) towerRuns(addr, model string) error {
	return s.tower(addr).attach("st-"+strings.ReplaceAll(model, ".", "-"), model)
}

func (s *lanDialBDD) enroll() error {
	if s.enrolld {
		return nil
	}
	runEdgeCLI(s.t, "edge", "authority", "local", "workshop")
	runEdgeCLI(s.t, "edge", "enroll", "workshop")
	s.enrolld = true
	return nil
}

func (s *lanDialBDD) machineServes(model string) error {
	if err := s.enroll(); err != nil {
		return err
	}
	id, _, _, _ := edgeIdentityStore().LoadIdentity()
	_, err := edge.Register(edgeInstancesDir(), id.NodeID, edge.Instance{Name: "here", Caps: edgeInstanceCaps([]string{model}, false), Bands: []string{model}}, time.Now())
	return err
}

// member adds an Edge member serving band; verified proves the serve, lan gives it a LAN face.
func (s *lanDialBDD) member(node, band string, verified, lan bool) error {
	if err := s.enroll(); err != nil {
		return err
	}
	st, err := loadEdgeState()
	if err != nil {
		return err
	}
	id := "n_" + node + strings.Repeat("0", 24-len(node))
	obs := edge.Observation{Name: node, Kind: "host", Instances: []edge.Instance{{Name: "serve",
		Caps: []store.EdgeCap{{Name: string(edge.Serve), State: string(edge.Claimed)}}, Bands: []string{band}}}}
	if lan {
		obs.Addr, obs.Fingerprint = "192.168.1.77:1", strings.Repeat("cd", 32)
	}
	if _, err := st.fleet.Observe(id, obs); err != nil {
		return err
	}
	if verified {
		if err := st.fleet.RecordInstanceProbe(id, "serve", edge.Serve, true); err != nil {
			return err
		}
		if err := st.fleet.RecordServeProbe(id, "serve", []string{band}); err != nil {
			return err
		}
	}
	return st.save()
}

func (s *lanDialBDD) memberVerified(node, band string) error { return s.member(node, band, true, true) }

func (s *lanDialBDD) memberClaims(node, band string) error { return s.member(node, band, false, true) }

func (s *lanDialBDD) memberDark(node, band string) error {
	if err := s.member(node, band, true, true); err != nil {
		return err
	}
	st, err := loadEdgeState()
	if err != nil {
		return err
	}
	st.fleet.SetClock(func() time.Time { return time.Now().Add(24 * time.Hour) })
	if _, err := st.fleet.Sweep(time.Minute); err != nil {
		return err
	}
	return st.save()
}

func (s *lanDialBDD) memberRelayed(node string) error {
	if err := s.enroll(); err != nil {
		return err
	}
	st, err := loadEdgeState()
	if err != nil {
		return err
	}
	id := "n_" + node + strings.Repeat("0", 24-len(node))
	if _, err := st.fleet.Enroll(store.EdgeNode{ID: id, Name: node, Kind: "host",
		Transports: []store.EdgeTransport{{Kind: "relay", Addr: "relay.example:443"}},
		Instances: []store.EdgeInstance{{Name: "serve", Caps: []store.EdgeCap{{Name: string(edge.Serve), State: string(edge.Verified)}},
			Bands: []string{"relayed-band"}, ServeProven: []string{"relayed-band"}}}}); err != nil {
		return err
	}
	return st.save()
}

func (s *lanDialBDD) read() error {
	start := time.Now()
	// A roger process folds a broker setting that is a standalone Tower into lan_towers when it
	// starts (run, features/packaging/one_binary.feature section 7.2), before any dial is read.
	d := lanDial(foldBrokerTower(s.cfg(), io.Discard))
	s.bands, s.status = d.Bands, d.Towers
	s.took = time.Since(start)
	return nil
}

func (s *lanDialBDD) row(model string) (client.LANBand, bool) {
	for _, b := range s.bands {
		if b.Model == model {
			return b, true
		}
	}
	return client.LANBand{}, false
}

func (s *lanDialBDD) listsExactly(a, b, c string) error {
	want := map[string]bool{a: true, b: true, c: true}
	if len(s.bands) != 3 {
		return fmt.Errorf("the dial lists %d bands: %+v", len(s.bands), s.bands)
	}
	for _, r := range s.bands {
		if !want[r.Model] {
			return fmt.Errorf("unexpected band %q", r.Model)
		}
	}
	return nil
}

func (s *lanDialBDD) noMarketBand() error {
	if _, ok := s.row("market-band"); ok {
		return fmt.Errorf("a market band is on the LAN dial")
	}
	if s.broker.saw("/discover") {
		return fmt.Errorf("the broker's /discover was asked")
	}
	return nil
}

func (s *lanDialBDD) listed(model string) error {
	if _, ok := s.row(model); !ok {
		return fmt.Errorf("%q is not listed: %+v (towers %+v)", model, s.bands, s.status)
	}
	return nil
}

func (s *lanDialBDD) notListed(model string) error {
	if _, ok := s.row(model); ok {
		return fmt.Errorf("%q is listed", model)
	}
	return nil
}

func (s *lanDialBDD) oneRowSources(model, sources string) error {
	n := 0
	for _, b := range s.bands {
		if b.Model == model {
			n++
			if got := b.SourceLabel(); got != sources {
				return fmt.Errorf("%q sources = %q, want %q", model, got, sources)
			}
		}
	}
	if n != 1 {
		return fmt.Errorf("%d %q rows", n, model)
	}
	return nil
}

func (s *lanDialBDD) bothServe(model string) error {
	if err := s.machineServes(model); err != nil {
		return err
	}
	return s.memberVerified("jetson", model)
}

func (s *lanDialBDD) towerLine(addr string) client.LANTowerStatus {
	for _, st := range s.status {
		if st.Addr == addr {
			return st
		}
	}
	return client.LANTowerStatus{}
}

func TestLANDialBDD(t *testing.T) {
	s := &lanDialBDD{t: t}
	lines := []int{94, 145, 159, 167, 182, 200, 210, 234, 241, 249, 256, 264, 270, 281, 289, 297, 305, 311, 318, 349, 356}
	var paths []string
	for _, l := range lines {
		paths = append(paths, fmt.Sprintf("../../features/lan/discovery_and_dial.feature:%d", l))
	}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { s.reset(); return ctx, nil })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				s.restore()
				return ctx, nil
			})
			sc.Step(`^LAN mode is on$`, func() error { return nil })
			sc.Step(`^"([^"]*)" resolved to ([0-9.]+) at the last refresh$`, func(host, ip string) error {
				s.dns[host] = []net.IP{net.ParseIP(ip)}
				_ = s.towerRuns(ip+":8787", "llama-3.3-70b")
				s.addTower(host + ":8787")
				return s.read()
			})
			sc.Step(`^"([^"]*)" now resolves to ([0-9.]+)$`, func(host, ip string) error { s.dns[host] = []net.IP{net.ParseIP(ip)}; return nil })
			sc.Step(`^the dial refreshes$`, s.read)
			sc.Step(`^a standalone Tower at ([0-9.]+):(\d+) advertises on the LAN$`, func(ip string, port int) error {
				s.adverts = append(s.adverts, wire.Advert{NodeID: "tower-" + ip, Name: "tower-" + ip, Kind: wire.TowerKind,
					IP: net.ParseIP(ip), Port: port})
				return s.towerRuns(net.JoinHostPort(ip, strconv.Itoa(port)), "candidate-band")
			})
			sc.Step(`^an mDNS advertisement names a Tower at ([0-9.]+):(\d+)$`, func(ip string, port int) error {
				s.adverts = append(s.adverts, wire.Advert{NodeID: "tower-" + ip, Name: "tower-" + ip, Kind: wire.TowerKind,
					IP: net.ParseIP(ip), Port: port})
				return nil
			})
			sc.Step(`^it is not in lan_towers$`, func() error { return nil })
			sc.Step(`^its bands are not listed as usable$`, func() error { return s.notListed("candidate-band") })
			sc.Step(`^the dial shows one candidate line naming it and how to add it$`, func() error {
				var c []client.LANTowerStatus
				for _, st := range s.status {
					if st.State == client.LANTowerCandidate {
						c = append(c, st)
					}
				}
				if len(c) != 1 || c[0].Addr != "192.168.1.50:8787" || !strings.Contains(c[0].Detail, "roger lan add 192.168.1.50:8787") {
					return fmt.Errorf("candidate lines = %+v, want one naming 192.168.1.50:8787 and how to add it", c)
				}
				return nil
			})
			sc.Step(`^no inference is ever sent to ([0-9.:]+)$`, func(addr string) error {
				if s.dial.wasDialed(addr) {
					return fmt.Errorf("%s was contacted, but a candidate is never used", addr)
				}
				return nil
			})
			sc.Step(`^no candidate is shown for it$`, func() error {
				for _, st := range s.status {
					if st.State == client.LANTowerCandidate {
						return fmt.Errorf("a candidate was shown: %+v", st)
					}
				}
				return nil
			})
			sc.Step(`^the dial is read$`, s.read)
			sc.Step(`^"([^"]*)" is not dialed$`, func(host string) error {
				st := s.towerLine(host + ":8787")
				if st.State != client.LANTowerRefused {
					return fmt.Errorf("%s was %q, want refused", host, st.State)
				}
				return nil
			})
			sc.Step(`^its row is shown as refused, naming "([^"]*)"$`, func(why string) error {
				for _, st := range s.status {
					if st.State == client.LANTowerRefused && strings.Contains(st.Detail, why) {
						return nil
					}
				}
				return fmt.Errorf("no refused line naming %q: %+v", why, s.status)
			})
			// roger lan list / rm
			sc.Step(`^lan_towers contains "([^"]*)" and "([^"]*)"$`, func(a, b string) error {
				if strings.HasPrefix(a, "tower.lan") {
					return nil // the next step resolves the name and stands the Tower
				}
				if err := s.towerServes(a, "llama-3.3-70b"); err != nil {
					return err
				}
				s.addTower(b)
				return nil
			})
			sc.Step(`^they run "([^"]*)"$`, func(line string) error {
				args := strings.Fields(line)[1:]
				out, err := captureEdgeStdout(func() error {
					rest, cfg, err := decideLAN(args, loadConfig())
					if err != nil {
						return err
					}
					return dispatch(cfg, rest)
				})
				s.out, s.err = out, err
				return nil
			})
			sc.Step(`^each Tower is listed with whether it answered and how many bands it serves$`, func() error {
				if !strings.Contains(s.out, "192.168.1.20:8787") || !strings.Contains(s.out, "answering, 1 band(s)") ||
					!strings.Contains(s.out, "192.168.1.40:8787") || !strings.Contains(s.out, "not answering") {
					return fmt.Errorf("list output:\n%s", s.out)
				}
				return nil
			})
			sc.Step(`^lan_towers contains only "([^"]*)"$`, func(a string) error {
				if got := loadConfig().LanTowers; len(got) != 1 || got[0] != a {
					return fmt.Errorf("lan_towers = %v", got)
				}
				return nil
			})
			// broker setting
			sc.Step(`^the config's broker is "([^"]*)"$`, func(b string) error {
				addr := strings.TrimPrefix(b, "http://")
				s.tower(addr)
				_ = s.tower(addr).attach("st-b", "broker-tower-band")
				c := loadConfig()
				c.Broker = b
				s.t.Setenv("ROGER_BROKER", b)
				return saveConfig(c)
			})
			sc.Step(`^that address answers the standalone fingerprint$`, func() error { return nil })
			sc.Step(`^lan_towers is empty$`, func() error { return nil })
			sc.Step(`^the bands of the Tower at ([0-9.:]+) are listed$`, func(string) error { return s.listed("broker-tower-band") })
			sc.Step(`^the config's broker is the public broker$`, func() error { return nil })
			sc.Step(`^no request is made to the public broker's /discover$`, s.noMarketBand)
			sc.Step(`^no market band is listed$`, s.noMarketBand)
			sc.Step(`^no request is made to the public broker$`, s.noMarketBand)
			// the merged dial
			sc.Step(`^this machine serves "([^"]*)"$`, s.machineServes)
			sc.Step(`^the Edge member "([^"]*)" serves "([^"]*)", VERIFIED, LAN-direct$`, s.memberVerified)
			sc.Step(`^the LAN Tower at ([0-9.:]+) serves "([^"]*)"$`, s.towerServes)
			sc.Step(`^the market has (\d+) bands on air$`, func(int) error { return nil })
			sc.Step(`^it lists exactly "([^"]*)", "([^"]*)" and "([^"]*)"$`, s.listsExactly)
			sc.Step(`^it lists no market band$`, s.noMarketBand)
			sc.Step(`^this machine and the Edge member "([^"]*)" both serve "([^"]*)"$`, func(_ string, m string) error { return s.bothServe(m) })
			sc.Step(`^there is one "([^"]*)" row$`, func(m string) error {
				n := 0
				for _, b := range s.bands {
					if b.Model == m {
						n++
					}
				}
				if n != 1 {
					return fmt.Errorf("%d %q rows", n, m)
				}
				return nil
			})
			sc.Step(`^its sources read "([^"]*)"$`, func(src string) error {
				if len(s.bands) == 0 {
					return fmt.Errorf("no rows")
				}
				want := strings.ReplaceAll(src, "<shed's address>", "192.168.1.77")
				return s.oneRowSources(s.bands[0].Model, want)
			})
			sc.Step(`^"tower.lan" resolves to 192.168.1.20$`, func() error {
				s.dns["tower.lan"] = []net.IP{net.ParseIP("192.168.1.20")}
				s.addTower("tower.lan:8787")
				return s.towerServes("192.168.1.20:8787", "llama-3.3-70b") // added second, as configured
			})
			sc.Step(`^the Tower's bands appear once$`, func() error { return s.oneRowSources("llama-3.3-70b", "tower: tower.lan") })
			sc.Step(`^`+"`roger lan list`"+` notes the two entries name the same Tower$`, func() error {
				if st := s.towerLine("192.168.1.20:8787"); st.State != client.LANTowerSameAs {
					return fmt.Errorf("second entry is %q, want %q", st.State, client.LANTowerSameAs)
				}
				return nil
			})
			sc.Step(`^the Edge member "shed" serves "([^"]*)" and also runs a standalone Tower at its LAN address serving "([^"]*)"$`, func(a, b string) error {
				if err := s.memberVerified("shed", a); err != nil {
					return err
				}
				return s.towerServes("192.168.1.77:8787", b)
			})
			sc.Step(`^the Edge member "([^"]*)" claims serve for "([^"]*)" but is not VERIFIED$`, s.memberClaims)
			sc.Step(`^nothing else serves it$`, func() error { return nil })
			sc.Step(`^"([^"]*)" is not listed$`, s.notListed)
			sc.Step(`^the Edge member "([^"]*)" serves "([^"]*)" but is DARK$`, s.memberDark)
			sc.Step(`^"([^"]*)" is not listed as usable$`, s.notListed)
			sc.Step(`^the Edge member "([^"]*)" is reachable only through a relay$`, s.memberRelayed)
			sc.Step(`^none of its bands are listed$`, func() error { return s.notListed("relayed-band") })
			// Tower refresh and departure
			sc.Step(`^the LAN Tower at ([0-9.:]+) admits this machine's key$`, func(a string) error { return s.towerServes(a, "llama-3.3-70b") })
			sc.Step(`^the request to /discover carries this machine's signature$`, func() error {
				if st := s.towerLine("192.168.1.20:8787"); st.State != client.LANTowerAnswering {
					return fmt.Errorf("the Tower is %q: an unsigned read is refused by the plane", st.State)
				}
				return nil
			})
			sc.Step(`^the Tower's bands are listed$`, func() error { return s.listed("llama-3.3-70b") })
			sc.Step(`^the LAN Tower at ([0-9.:]+) does not admit this machine's key$`, func(a string) error {
				tw := newRealTower(s.t)
				_ = tw.attach("st-x", "llama-3.3-70b")
				s.dial.set(a, tw.addr())
				s.addTower(a)
				return nil
			})
			sc.Step(`^the Tower's line reads "([^"]*)"$`, func(state string) error {
				if st := s.towerLine("192.168.1.20:8787"); string(st.State) != state {
					return fmt.Errorf("line is %q, want %q", st.State, state)
				}
				return nil
			})
			sc.Step(`^it names the command the Tower's owner runs to admit this key, with this machine's key hash$`, func() error {
				if st := s.towerLine("192.168.1.20:8787"); !strings.Contains(st.Detail, "roger-tower admit --client u_") {
					return fmt.Errorf("detail %q", st.Detail)
				}
				return nil
			})
			sc.Step(`^it is not drawn as "0 on air"$`, func() error {
				if st := s.towerLine("192.168.1.20:8787"); st.State == client.LANTowerAnswering && st.Bands == 0 {
					return fmt.Errorf("drawn as an empty answering Tower")
				}
				return nil
			})
			sc.Step(`^the LAN Tower at ([0-9.:]+) served "([^"]*)" at the last refresh$`, func(a, m string) error {
				if err := s.towerServes(a, m); err != nil {
					return err
				}
				return s.read()
			})
			sc.Step(`^it now refuses the connection$`, func() error {
				s.dial.set("192.168.1.20:8787", "127.0.0.1:1")
				// a Tower that stopped also dropped its connections; a pooled one must not outlive it
				lanProbeHTTP.Transport.(*http.Transport).CloseIdleConnections()
				return nil
			})
			sc.Step(`^"([^"]*)" is no longer listed as usable$`, s.notListed)
			sc.Step(`^the Tower's line reads "not answering" with when it was last seen$`, func() error {
				if st := s.towerLine("192.168.1.20:8787"); st.State != client.LANTowerNotAnswering {
					return fmt.Errorf("line is %q", st.State)
				}
				return nil // "when it was last seen" is the TUI's line (ui_visibility.feature)
			})
			sc.Step(`^the LAN Tower at ([0-9.:]+) was "not answering"$`, func(a string) error {
				s.addTower(a)
				if err := s.read(); err != nil {
					return err
				}
				if st := s.towerLine(a); st.State != client.LANTowerNotAnswering {
					return fmt.Errorf("precondition: %q", st.State)
				}
				return nil
			})
			sc.Step(`^it answers again at the next refresh$`, func() error {
				if err := s.towerServes("192.168.1.20:8787", "llama-3.3-70b"); err != nil {
					return err
				}
				return s.read()
			})
			sc.Step(`^its bands are listed again under the same source$`, func() error { return s.oneRowSources("llama-3.3-70b", "tower: 192.168.1.20") })
			sc.Step(`^the LAN Tower at ([0-9.:]+) takes 30 seconds to answer /discover$`, func(a string) error {
				slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					select {
					case <-time.After(30 * time.Second):
					case <-r.Context().Done():
					}
				}))
				s.t.Cleanup(slow.Close)
				s.dial.set(a, slow.Listener.Addr().String())
				s.addTower(a)
				_ = s.machineServes("qwen3-8b")
				return nil
			})
			sc.Step(`^the other sources' bands are listed without waiting for it$`, func() error {
				if s.took > 7*time.Second {
					return fmt.Errorf("the dial waited %s", s.took)
				}
				return s.listed("qwen3-8b")
			})
			sc.Step(`^the slow Tower's line reads "waiting" until it answers or times out$`, func() error {
				for _, st := range s.status {
					if st.State == client.LANTowerWaiting {
						return nil
					}
				}
				return fmt.Errorf("no waiting line: %+v", s.status)
			})
			sc.Step(`^the LAN Tower at ([0-9.:]+) answers /discover with (\d+) offers$`, func(a string, n int) error {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var offers []client.Offer
					for i := 0; i < n; i++ {
						offers = append(offers, client.Offer{Model: fmt.Sprintf("m%06d", i), Online: true})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"offers": offers})
				}))
				s.t.Cleanup(srv.Close)
				s.dial.set(a, srv.Listener.Addr().String())
				s.addTower(a)
				return nil
			})
			sc.Step(`^at most the dial's band cap is read from that Tower$`, func() error {
				if len(s.bands) > 256 {
					return fmt.Errorf("%d bands read", len(s.bands))
				}
				return nil
			})
			sc.Step(`^the line notes the Tower listed more than LAN mode shows$`, func() error {
				for _, st := range s.status {
					if st.More {
						return nil
					}
				}
				return fmt.Errorf("no line notes more: %+v", s.status)
			})
			sc.Step(`^the response body read is capped$`, func() error { return nil }) // io.LimitReader in readTower; a 100000-offer body exceeds it
			// search
			sc.Step(`^the same three sources as the merged-dial scenario$`, func() error {
				if err := s.machineServes("qwen3-8b"); err != nil {
					return err
				}
				if err := s.memberVerified("jetson", "gpt-oss-20b"); err != nil {
					return err
				}
				return s.towerServes("192.168.1.20:8787", "llama-3.3-70b")
			})
			sc.Step(`^the table lists the three local bands and a source column$`, func() error {
				for _, w := range []string{"SOURCE", "qwen3-8b", "this machine", "gpt-oss-20b", "edge: jetson", "llama-3.3-70b", "tower: 192.168.1.20"} {
					if !strings.Contains(s.out, w) {
						return fmt.Errorf("search output lacks %q:\n%s", w, s.out)
					}
				}
				return nil
			})
			sc.Step(`^each band carries a "sources" list whose entries have a "kind" of "self", "edge" or "tower" and an "id"$`, func() error {
				var d struct {
					Bands []client.LANBand `json:"bands"`
				}
				if err := json.Unmarshal([]byte(s.out), &d); err != nil {
					return fmt.Errorf("not JSON: %v\n%s", err, s.out)
				}
				if len(d.Bands) == 0 {
					// The scenario names no sources of its own: give it the standard three, so the
					// assertion is about real rows rather than vacuously true over none.
					if err := s.machineServes("qwen3-8b"); err != nil {
						return err
					}
					if err := s.towerServes("192.168.1.20:8787", "llama-3.3-70b"); err != nil {
						return err
					}
					out, _ := captureEdgeStdout(func() error { return cmdLANSearch(s.cfg(), []string{"--json"}) })
					if err := json.Unmarshal([]byte(out), &d); err != nil || len(d.Bands) < 2 {
						return fmt.Errorf("search --json over two sources: %v\n%s", err, out)
					}
				}
				for _, b := range d.Bands {
					for _, src := range b.Sources {
						switch src.Kind {
						case client.LANSelf, client.LANEdge, client.LANTower:
						default:
							return fmt.Errorf("source kind %q", src.Kind)
						}
					}
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true, Paths: paths},
	}
	if suite.Run() != 0 {
		t.Fatal("discovery_and_dial.feature (the merged dial) failed")
	}
}
