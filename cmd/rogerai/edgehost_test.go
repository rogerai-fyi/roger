package main

// The Edge HOST: what turns a shipped `roger` from a truthful-but-empty Edge screen into
// the operator's real fleet.
//
// There is one Edge per machine, and both front ends must be looking at it: `roger edge`
// reads the cache beside config.json, and the TUI's [3] EDGE screen has to read and write
// THAT, not a second source of truth. Discovery keeps it current from a background
// goroutine that is allowed to fail, to be slow, and to be switched off - and is never
// allowed to delay startup, block a relay, or invent a heartbeat.
//
// REAL dependencies: the real fleet over the real store, the real on-disk state file, the
// real discovery engine, and (for the sighting test) a real mDNS responder + a real TLS
// peer with a real certificate on the loopback packet bus this package's CLI spec already
// stands up. The only substitution is the multicast GROUP, exactly as the CLI spec does.

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/tui"
)

// edgeHostOff wires the Edge with discovery switched OFF: the hooks are real, nothing
// touches the network.
func edgeHostOff(t *testing.T) (*edgeHost, tui.Hooks) {
	t.Helper()
	t.Setenv(edge.EnvDiscovery, "0")
	h, err := newEdgeHost("roger-desk")
	require.NoError(t, err)
	var hooks tui.Hooks
	h.wire(&hooks)
	h.start(context.Background())
	t.Cleanup(h.stop)
	return h, hooks
}

// The four seams the Edge screen draws from are filled with the REAL fleet - the one the
// CLI already keeps on this machine.
func TestEdgeHooksAreFilledWithTheRealFleet(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	edgeSeed(t,
		store.EdgeNode{ID: "n_a1", Name: "bench-pi", Kind: "board",
			Transports: []store.EdgeTransport{{Kind: "lan", Addr: "10.0.0.5:8443"}}},
		store.EdgeNode{ID: "n_b2", Name: "attic-sensor", Kind: "board"},
	)
	_, hooks := edgeHostOff(t)

	require.Equal(t, "roger-desk", hooks.EdgeSelf, "self is this station, named by the host")
	require.NotNil(t, hooks.EdgeFleet)
	require.NotNil(t, hooks.EdgeCandidates)
	require.NotNil(t, hooks.EdgeAdopt)
	require.NotNil(t, hooks.EdgeHeartbeats)

	list, err := hooks.EdgeFleet.List()
	require.NoError(t, err)
	var names []string
	for _, n := range list {
		names = append(names, n.Name)
	}
	require.ElementsMatch(t, []string{"bench-pi", "attic-sensor"}, names)
	require.Equal(t, "owner", hooks.EdgeFleet.Account())
}

// A candidate is a candidate: it is offered for adoption and it is NEVER in the fleet the
// screen draws as members. This machine holds no Edge authority yet, so an unadopted peer
// has had nothing about it verified.
func TestEdgeCandidatesAreNeverPresentedAsMembers(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	st, err := loadEdgeState()
	require.NoError(t, err)
	st.candidates = []store.EdgeNode{{
		ID: "n_cand", Name: "n_cand", Kind: "host", Pin: "aa11",
		Presence:   string(edge.PresenceCandidate),
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: "10.0.0.9:8443", Fingerprint: "aa11"}},
	}}
	require.NoError(t, st.save())

	_, hooks := edgeHostOff(t)
	members, err := hooks.EdgeFleet.List()
	require.NoError(t, err)
	require.Empty(t, members, "a candidate is not a member")
	cands := hooks.EdgeCandidates()
	require.Len(t, cands, 1)
	require.Equal(t, "n_cand", cands[0].ID)
	require.Equal(t, string(edge.PresenceCandidate), cands[0].Presence)
}

// ROGERAI_EDGE_DISCOVERY=0 starts NOTHING: no engine, no socket, no goroutine.
func TestEdgeDiscoveryHonoursTheOffSwitch(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		want      bool
	}{
		{"off", "0", false},
		{"on by default", "", true},
		{"explicitly on", "1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useTempConfig(t)
			edgeWriteAuth(t, "owner")
			t.Setenv(edge.EnvDiscovery, tc.env)
			planes := edgeCountingPlane(t)

			h, err := newEdgeHost("roger-desk")
			require.NoError(t, err)
			h.start(context.Background())
			t.Cleanup(h.stop)

			require.Equal(t, tc.want, h.running(), "discovery engine")
			if !tc.want {
				time.Sleep(150 * time.Millisecond)
				require.Zero(t, planes(), "a disabled discovery opens no socket at all")
				return
			}
			require.Eventually(t, func() bool { return planes() > 0 }, 3*time.Second, 20*time.Millisecond,
				"an enabled discovery browses on its own")
		})
	}
}

// Discovery that cannot work at all is a line in the log, not a broken Edge: the fleet
// still reads, the screen still draws, and nothing the host does waits on it.
func TestEdgeDiscoveryFailingEntirelyLeavesEverythingElseWorking(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	edgeSeed(t, store.EdgeNode{ID: "n_a1", Name: "bench-pi", Kind: "board"})
	edgeUseOptions(t, func(o edge.Options) edge.Options {
		o.Plane = func() (edge.Transport, error) { return nil, edge.ErrNoLAN }
		return o
	})

	h, err := newEdgeHost("roger-desk")
	require.NoError(t, err)
	var hooks tui.Hooks
	h.wire(&hooks)
	h.start(context.Background())
	t.Cleanup(h.stop)

	list, err := hooks.EdgeFleet.List()
	require.NoError(t, err, "a fleet read never depends on discovery")
	require.Len(t, list, 1)
	require.Empty(t, hooks.EdgeCandidates())
	// And the relay path is untouched: the reachability probe every fleet view uses
	// answers while discovery is failing behind it.
	require.NotNil(t, edgeReachAll(config{}, list))
}

// Startup is not delayed by discovery. A plane that takes seconds to build must cost the
// launch nothing at all.
func TestEdgeStartupIsNotDelayedByDiscovery(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	edgeUseOptions(t, func(o edge.Options) edge.Options {
		o.Plane = func() (edge.Transport, error) {
			time.Sleep(2 * time.Second)
			return nil, edge.ErrNoLAN
		}
		return o
	})
	var hooks tui.Hooks
	hooks.Station = "roger-desk"
	start := time.Now()
	stop := startEdge(&hooks)
	took := time.Since(start)
	go stop() // the slow plane is still in flight; quitting does not wait on it either
	require.Less(t, took, 250*time.Millisecond, "startEdge blocked the launch for %s", took)
	require.NotNil(t, hooks.EdgeFleet, "and the screen was wired anyway")
}

// A VERIFIED sighting - the peer was dialed, its certificate checked and the fleet
// updated - is a real heartbeat, and it beats for exactly that node. A candidate is not a
// sighting: nothing was verified about it, so nothing pulses for it.
func TestEdgeVerifiedSightingBeatsExactlyThatNode(t *testing.T) {
	s := &edgeCLIBDD{}
	s.reset(t)
	s.login("owner")
	s.useBus()
	member := s.newPeer("owner", []string{"serve"}, "")
	stranger := s.newPeer("", []string{"serve"}, "")
	s.enroll(store.EdgeNode{
		ID: member.id, Name: "beta", Kind: "host", Pin: member.advert.Fingerprint,
		Presence:   string(edge.PresenceVerified),
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: member.advert.Addr(), Fingerprint: member.advert.Fingerprint}},
	})

	h, err := newEdgeHost("roger-desk")
	require.NoError(t, err)
	var hooks tui.Hooks
	h.wire(&hooks)
	h.start(context.Background())
	t.Cleanup(h.stop)
	rep := h.runPass(context.Background())
	require.Len(t, rep.Verified, 1, "the member was dialed and checked: %+v", rep)
	require.Equal(t, member.id, rep.Verified[0].NodeID)

	var beats []string
	for len(h.beats) > 0 {
		beats = append(beats, <-h.beats)
	}
	require.Equal(t, []string{member.id}, beats,
		"one heartbeat, for the node that was actually heard from - and none for the candidate")

	cands := hooks.EdgeCandidates()
	require.Len(t, cands, 1)
	require.Equal(t, stranger.id, cands[0].ID, "the unenrolled peer is a candidate, not a member")

	// What the pass learned is on disk, so the CLI in the next process sees it too.
	st, err := loadEdgeState()
	require.NoError(t, err)
	require.Len(t, st.candidates, 1)
}

// ONE fleet. What the TUI adopts, the CLI prints; what the CLI cached, the TUI draws.
func TestTheCLIAndTheTUIShareOneFleet(t *testing.T) {
	s := &edgeCLIBDD{}
	s.reset(t)
	s.login("owner")
	p := s.newPeer("", []string{"serve"}, "")
	s.addCandidate(store.EdgeNode{
		ID: p.id, Name: p.id, Kind: "host", Pin: p.advert.Fingerprint,
		Presence:   string(edge.PresenceCandidate),
		LastSeen:   time.Now().Unix(),
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: p.advert.Addr(), Fingerprint: p.advert.Fingerprint}},
	})

	// The TUI draws the candidate the CLI's own scan cached.
	_, hooks := edgeHostOff(t)
	require.Len(t, hooks.EdgeCandidates(), 1)

	// The operator presses `a` on the Edge screen...
	require.NoError(t, hooks.EdgeAdopt(p.id, p.id))
	require.Empty(t, hooks.EdgeCandidates(), "an adopted candidate stops being one")
	members, err := hooks.EdgeFleet.List()
	require.NoError(t, err)
	require.Len(t, members, 1)

	// ...and `roger edge list` - which loads the cache from disk, exactly as a separate
	// process would - shows the very same node.
	out, code := edgeRun(t, "edge", "list")
	require.Equal(t, 0, code)
	require.Contains(t, out, p.id[:10])
}

// An Edge that cannot be read is not an empty Edge: the host says so and leaves the screen
// honestly unwired rather than drawing a fleet of none.
func TestEdgeHostRefusesToInventAFleetItCouldNotRead(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	require.NoError(t, os.MkdirAll(edgeStatePath(), 0o700)) // a directory where the cache belongs

	_, err := newEdgeHost("roger-desk")
	require.Error(t, err)

	var hooks tui.Hooks
	hooks.Station = "roger-desk"
	stop := startEdge(&hooks)
	t.Cleanup(stop)
	require.Nil(t, hooks.EdgeFleet, "no fleet is wired rather than a wrong one")
	require.Nil(t, hooks.EdgeAdopt)
}

// --- test seams -----------------------------------------------------------

// edgeUseOptions points the discovery seam (the one `roger edge scan` already uses) at a
// mutated copy of the production options for the life of one test.
func edgeUseOptions(t *testing.T, mutate func(edge.Options) edge.Options) {
	t.Helper()
	edgeDiscoveryOptions = func(f *edge.Fleet) edge.Options { return mutate(defaultEdgeDiscoveryOptions(f)) }
	t.Cleanup(func() { edgeDiscoveryOptions = defaultEdgeDiscoveryOptions })
}

// edgeCountingPlane counts how many times discovery asked for a packet plane, which is how
// a test tells "browsing" from "started nothing at all" without a real network.
func edgeCountingPlane(t *testing.T) func() int {
	t.Helper()
	var n atomic.Int64
	edgeUseOptions(t, func(o edge.Options) edge.Options {
		o.Config.Interval = 50 * time.Millisecond
		o.Plane = func() (edge.Transport, error) { n.Add(1); return nil, edge.ErrNoLAN }
		return o
	})
	return func() int { return int(n.Load()) }
}
