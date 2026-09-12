package edge_test

// Units for the discovery engine's knobs, its report, and the paths the scenarios do
// not walk: the daemon loop, a plane that will not open, and the candidate TTL.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func TestConfigFromEnvDefaultsAndOverrides(t *testing.T) {
	empty := edge.ConfigFromEnv(func(string) string { return "" })
	require.True(t, empty.Enabled, "discovery is on unless it is turned off")
	require.Equal(t, edge.DefaultService, empty.Service)
	require.Equal(t, edge.DefaultInterval, empty.Interval)
	require.Equal(t, edge.DefaultTTL, empty.CandidateTTL)
	require.Equal(t, edge.DefaultCeiling, empty.Ceiling)
	require.Equal(t, edge.DefaultWindow, empty.Window)

	set := map[string]string{
		edge.EnvDiscovery: "0",
		edge.EnvService:   " _roger-test._tcp ",
		edge.EnvInterval:  "45s",
		edge.EnvTTL:       "90s",
		edge.EnvCeiling:   "16",
	}
	c := edge.ConfigFromEnv(func(k string) string { return set[k] })
	require.False(t, c.Enabled)
	require.Equal(t, "_roger-test._tcp", c.Service)
	require.Equal(t, 45*time.Second, c.Interval)
	require.Equal(t, 90*time.Second, c.CandidateTTL)
	require.Equal(t, 16, c.Ceiling)

	// Nonsense is ignored rather than fatal: a typo in an env var must not stop a node.
	junk := map[string]string{
		edge.EnvInterval: "soon", edge.EnvTTL: "-5s", edge.EnvCeiling: "many",
	}
	c = edge.ConfigFromEnv(func(k string) string { return junk[k] })
	require.Equal(t, edge.DefaultInterval, c.Interval)
	require.Equal(t, edge.DefaultTTL, c.CandidateTTL)
	require.Equal(t, edge.DefaultCeiling, c.Ceiling)

	// A browse window can never outlast the interval it belongs to.
	short := map[string]string{edge.EnvInterval: "500ms"}
	c = edge.ConfigFromEnv(func(k string) string { return short[k] })
	require.Equal(t, 500*time.Millisecond, c.Window)

	// A nil getenv reads the real environment, which is empty for these keys here.
	require.Equal(t, edge.DefaultService, edge.ConfigFromEnv(nil).Service)
}

func TestReportLookups(t *testing.T) {
	r := edge.Report{Refusals: []edge.Refusal{{Reason: edge.ReasonUnreachable, NodeID: "n_a"}}}
	require.True(t, r.Has(edge.ReasonUnreachable))
	require.False(t, r.Has(edge.ReasonCertExpired))
	f, ok := r.Find(edge.ReasonUnreachable)
	require.True(t, ok)
	require.Equal(t, "n_a", f.NodeID)
	_, ok = r.Find(edge.ReasonCertExpired)
	require.False(t, ok)
}

// planeErr is a plane that will not open, and one that opens but will not send.
type deafPlane struct{ mu sync.Mutex }

func (d *deafPlane) Send([]byte) error                    { return errors.New("no route") }
func (d *deafPlane) Recv(b []byte) (int, net.Addr, error) { return 0, nil, errors.New("closed") }
func (d *deafPlane) SetReadDeadline(time.Time) error      { return nil }
func (d *deafPlane) Close() error                         { return nil }

func newDisc(t *testing.T, cfg edge.Config, plane func() (edge.Transport, error), logf func(string)) (*edge.Discovery, *edge.Fleet) {
	t.Helper()
	db := store.NewMem()
	f := edge.NewFleet(db, "acct-1")
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	d := edge.New(edge.Options{
		Fleet: f, Config: cfg, Plane: plane, Log: logf,
		Self:         edge.Advert{NodeID: edge.NodeID(pub), Account: "acct-1", Port: 1, Fingerprint: "aa"},
		AdvertiseIPs: []net.IP{net.ParseIP("127.0.0.1")},
		Interfaces:   func() []net.Interface { return nil },
	})
	t.Cleanup(d.Stop)
	return d, f
}

func TestDisabledDiscoveryDoesNothing(t *testing.T) {
	cfg := edge.ConfigFromEnv(func(k string) string {
		if k == edge.EnvDiscovery {
			return "0"
		}
		return ""
	})
	var lines []string
	d, _ := newDisc(t, cfg, func() (edge.Transport, error) {
		t.Fatal("a disabled discovery must not open a socket")
		return nil, nil
	}, func(s string) { lines = append(lines, s) })
	require.NoError(t, d.Start(context.Background()))
	require.False(t, d.Advertising())
	require.False(t, d.Browsing())
	require.Nil(t, d.Responder())
	rep := d.RunOnce(context.Background())
	require.Zero(t, rep.Seen)
	require.Equal(t, 1, countWith(lines, "discovery is unavailable"))
}

func TestAPlaneThatWillNotOpenIsReportedOnce(t *testing.T) {
	cfg := edge.ConfigFromEnv(func(string) string { return "" })
	var lines []string
	d, _ := newDisc(t, cfg, func() (edge.Transport, error) { return nil, edge.ErrNoLAN },
		func(s string) { lines = append(lines, s) })
	start := time.Now()
	require.NoError(t, d.Start(context.Background()))
	require.Less(t, time.Since(start), 250*time.Millisecond, "startup is never delayed by discovery")
	require.False(t, d.Advertising())
	require.Contains(t, d.Report().Notes, "discovery is unavailable")
	require.Equal(t, 1, countWith(lines, "discovery is unavailable"))
	require.NoError(t, d.Start(context.Background()))
	require.Equal(t, 1, countWith(lines, "discovery is unavailable"), "said once, not per attempt")
}

// TestSecondPlaneFailureClosesTheFirst covers the half-open case: the responder's
// socket opened and the browser's did not, so the first must not be leaked.
func TestSecondPlaneFailureClosesTheFirst(t *testing.T) {
	cfg := edge.ConfigFromEnv(func(string) string { return "" })
	n := 0
	closed := false
	d, _ := newDisc(t, cfg, func() (edge.Transport, error) {
		n++
		if n == 1 {
			return &closeSpy{closed: &closed}, nil
		}
		return nil, errors.New("second socket refused")
	}, nil)
	require.NoError(t, d.Start(context.Background()))
	require.True(t, closed, "the first plane was leaked")
	require.False(t, d.Browsing())
}

type closeSpy struct{ closed *bool }

func (c *closeSpy) Send([]byte) error                    { return nil }
func (c *closeSpy) Recv(b []byte) (int, net.Addr, error) { return 0, nil, errors.New("closed") }
func (c *closeSpy) SetReadDeadline(time.Time) error      { return nil }
func (c *closeSpy) Close() error                         { *c.closed = true; return nil }

// TestDaemonLoopKeepsBrowsing covers the default (non-manual) schedule: Start browses
// immediately and then every interval, and Stop ends it.
func TestDaemonLoopKeepsBrowsing(t *testing.T) {
	cfg := edge.ConfigFromEnv(func(string) string { return "" })
	cfg.Interval = 20 * time.Millisecond
	cfg.Window = 5 * time.Millisecond
	var mu sync.Mutex
	var lines []string
	d, _ := newDisc(t, cfg, func() (edge.Transport, error) { return &deafPlane{}, nil },
		func(s string) { mu.Lock(); lines = append(lines, s); mu.Unlock() })
	require.NoError(t, d.Start(context.Background()))
	require.True(t, d.Browsing())
	require.True(t, d.Advertising())
	require.NotNil(t, d.Responder())
	require.Eventually(t, func() bool {
		return len(d.Report().Notes) > 0
	}, 2*time.Second, 10*time.Millisecond)
	require.Contains(t, d.Report().Notes, "no peers found")
	d.Stop()
	require.False(t, d.Browsing())
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, countWith(lines, "discovery is unavailable"),
		"a plane that cannot send says so once, not once per interval")
}

func TestAdoptRefusesWhatWasNeverSeen(t *testing.T) {
	cfg := edge.ConfigFromEnv(func(string) string { return "" })
	d, _ := newDisc(t, cfg, func() (edge.Transport, error) { return &deafPlane{}, nil }, nil)
	_, err := d.Adopt("n_never", "ghost")
	require.Error(t, err)
	require.Empty(t, d.Candidates())
}

func TestResponderPacketIsTheAdvertisement(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	ad := edge.Advert{NodeID: edge.NodeID(pub), Account: "acct-1", Kind: "host",
		Caps: []string{"sense"}, Port: 9443, Fingerprint: strings.Repeat("ab", 32)}
	r := edge.NewResponder(nil, edge.DefaultService, ad, []net.IP{net.ParseIP("192.168.1.5")})
	pkt, err := r.Packet()
	require.NoError(t, err)
	require.NotEmpty(t, pkt)

	// A node id that will not fit in a DNS label is refused rather than truncated.
	bad := ad
	bad.NodeID = strings.Repeat("x", 64)
	_, err = edge.NewResponder(nil, edge.DefaultService, bad, nil).Packet()
	require.Error(t, err)
}

func TestServerDescribeIsReadOnly(t *testing.T) {
	s := edge.NewServer(edge.Describe{NodeID: "n_a", Account: "acct-1", Kind: "host"}, tls.Certificate{})
	require.Empty(t, s.Fingerprint(), "a server with no certificate has no fingerprint")
	require.Equal(t, "n_a", s.Advert(1).NodeID)
	require.NotNil(t, s.Handler())
}

func countWith(lines []string, sub string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

// TestABrowseOnlyInstanceAdvertisesNothing covers the one-shot `roger edge scan`: a CLI
// run has no identity of its own to advertise (enrollment is what issues one), so it must
// browse WITHOUT putting a record on the LAN. An empty advertisement is worse than none -
// every other node on the network would offer this machine as a candidate for a record
// that names nothing and serves no certificate.
func TestABrowseOnlyInstanceAdvertisesNothing(t *testing.T) {
	cfg := edge.ConfigFromEnv(func(string) string { return "" })
	cfg.ManualPasses = true
	opened := 0
	d := edge.New(edge.Options{
		Fleet:  edge.NewFleet(store.NewMem(), "acct-1"),
		Config: cfg,
		Plane: func() (edge.Transport, error) {
			opened++
			return &deafPlane{}, nil
		},
		Interfaces: func() []net.Interface { return nil },
	})
	t.Cleanup(d.Stop)
	require.NoError(t, d.Start(context.Background()))
	require.False(t, d.Advertising(), "a node with nothing to advertise advertises nothing")
	require.Nil(t, d.Responder())
	require.True(t, d.Browsing(), "it still looks for the owner's other machines")
	require.Equal(t, 1, opened, "no socket is opened for an advertising half that does not exist")
}
