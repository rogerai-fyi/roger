package serve

// Executable spec: features/lan/discovery_and_dial.feature ("a standalone Tower advertises on the
// LAN only when asked to") and features/lan/adversarial.feature ("a standalone Tower's
// advertisement never reaches a public interface"). The real prepare/serve path and the real
// mDNS responder run; only the network edge (interfaces, the multicast plane, the listen
// address) is a seam, because a test cannot own a machine's interfaces.

import (
	"context"
	"fmt"
	"net"
	"rogerai.fm/roger/v7/internal/bddrun"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/edge/wire"
)

// recPlane is the multicast plane: it records what the responder sends and feeds it queries.
type recPlane struct {
	mu     sync.Mutex
	sent   [][]byte
	in     chan []byte
	ifaces []string
	opened bool
}

// isOpen waits briefly for the advertiser goroutine to open the plane.
func (p *recPlane) isOpen(wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		p.mu.Lock()
		o := p.opened
		p.mu.Unlock()
		if o || time.Now().After(deadline) {
			return o
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (p *recPlane) Send(b []byte) error {
	p.mu.Lock()
	p.sent = append(p.sent, append([]byte(nil), b...))
	p.mu.Unlock()
	return nil
}

func (p *recPlane) Recv(b []byte) (int, net.Addr, error) {
	select {
	case m := <-p.in:
		return copy(b, m), &net.UDPAddr{IP: net.ParseIP("192.168.1.10"), Port: 5353}, nil
	case <-time.After(50 * time.Millisecond):
		return 0, nil, timeoutErr{}
	}
}
func (p *recPlane) SetReadDeadline(time.Time) error { return nil }
func (p *recPlane) Close() error                    { return nil }

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

type advState struct {
	t     *testing.T
	plane *recPlane
	p     *Prepared
	out   strings.Builder
}

// fakeIfaces is a machine with a public and a private interface.
func (s *advState) fakeIfaces() {
	up := net.FlagUp | net.FlagMulticast
	towerInterfaces = func() []net.Interface {
		return []net.Interface{{Index: 1, Name: "lan0", Flags: up}, {Index: 2, Name: "wan0", Flags: up}}
	}
	towerAddrsOf = func(i net.Interface) ([]net.Addr, error) {
		if i.Name == "lan0" {
			return []net.Addr{&net.IPNet{IP: net.ParseIP("192.168.1.50"), Mask: net.CIDRMask(24, 32)}}, nil
		}
		return []net.Addr{&net.IPNet{IP: net.ParseIP("203.0.113.7"), Mask: net.CIDRMask(24, 32)}}, nil
	}
	towerPlane = func(list func() []net.Interface) (wire.Transport, error) {
		s.plane.mu.Lock()
		defer s.plane.mu.Unlock()
		s.plane.opened = true
		for _, i := range list() {
			s.plane.ifaces = append(s.plane.ifaces, i.Name)
		}
		return s.plane, nil
	}
	towerListen = func(network, _ string) (net.Listener, error) { return net.Listen(network, "127.0.0.1:0") }
}

func (s *advState) start(args ...string) error {
	p, err := Prepare(append([]string{"--dir", standaloneDir(s.t), "--bind", "192.168.1.50:8787", "--label", "garage"}, args...), &s.out)
	if err != nil {
		return err
	}
	s.p = p
	done := make(chan error, 1)
	go func() { done <- p.Serve() }()
	s.t.Cleanup(func() {
		_ = p.srv.Close()
		<-done
	})
	return nil
}

// ask sends a browse query and returns the adverts answered within a short window.
func (s *advState) ask() []wire.Advert {
	q, err := wire.QueryPacket(wire.TowerService)
	if err != nil {
		return nil
	}
	s.plane.mu.Lock()
	before := len(s.plane.sent)
	s.plane.mu.Unlock()
	select {
	case s.plane.in <- q:
	default:
	}
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		s.plane.mu.Lock()
		sent := append([][]byte(nil), s.plane.sent[before:]...)
		s.plane.mu.Unlock()
		for _, pkt := range sent {
			if ads := wire.ParseAdverts(pkt, wire.TowerService, nil); len(ads) > 0 {
				return ads
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

func TestTowerAdvertiseBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			s := &advState{t: t}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.plane, s.p = &recPlane{in: make(chan []byte, 4)}, nil
				pi, pa, pp, pl := towerInterfaces, towerAddrsOf, towerPlane, towerListen
				t.Cleanup(func() { towerInterfaces, towerAddrsOf, towerPlane, towerListen = pi, pa, pp, pl })
				s.fakeIfaces()
				return ctx, nil
			})
			sc.Step(`^LAN mode is on$`, func() error { return nil })
			sc.Step(`^a standalone Tower started without --advertise$`, func() error { return s.start() })
			sc.Step(`^it sends no mDNS advertisement$`, func() error {
				if s.plane.isOpen(300 * time.Millisecond) {
					return fmt.Errorf("the Tower opened the multicast plane without --advertise")
				}
				if ads := s.ask(); len(ads) > 0 {
					return fmt.Errorf("the Tower answered a browse without --advertise: %+v", ads)
				}
				return nil
			})
			sc.Step(`^a standalone Tower started with --advertise( on a machine with a public and a private interface)?$`, func(string) error {
				s.plane = &recPlane{in: make(chan []byte, 4)}
				return s.start("--advertise")
			})
			advertisesPrivateOnly := func() error {
				if !s.plane.isOpen(2 * time.Second) {
					return fmt.Errorf("the Tower never opened the multicast plane with --advertise")
				}
				s.plane.mu.Lock()
				joined := strings.Join(s.plane.ifaces, ",")
				s.plane.mu.Unlock()
				if joined != "lan0" {
					return fmt.Errorf("the advertisement plane joined %v, want only the private lan0", joined)
				}
				ads := s.ask()
				if len(ads) != 1 {
					return fmt.Errorf("the Tower answered %d adverts, want 1", len(ads))
				}
				if !ads[0].IP.Equal(net.ParseIP("192.168.1.50")) {
					return fmt.Errorf("the advert names %v, want the private 192.168.1.50", ads[0].IP)
				}
				return nil
			}
			sc.Step(`^it advertises on private interfaces only$`, advertisesPrivateOnly)
			sc.Step(`^the advertisement is sent on the private interface only$`, advertisesPrivateOnly)
			sc.Step(`^the advertisement carries its address and a label, and no account, key or client list$`, func() error {
				ad := s.ask()[0]
				if ad.Port != 8787 || ad.Name != "garage" || ad.Kind != wire.TowerKind {
					return fmt.Errorf("advert = %+v, want port 8787, label garage, kind tower", ad)
				}
				if ad.Account != "" || ad.Fingerprint != "" || len(ad.Caps) != 0 {
					return fmt.Errorf("the advert carries account/key/caps: %+v", ad)
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", Strict: true, TestingT: t, Paths: []string{
			"../../../features/lan/discovery_and_dial.feature:174", "../../../features/lan/adversarial.feature:113"}},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("Tower advertise scenarios failed")
	}
}
