package link

// Session negotiation and liveness for the joined relay link, per
// features/tower/inventory_and_routing.feature ("protocol negotiation").
//
// The spec's requirement is blunt: when negotiation fails, "no inventory, lease, payload,
// result, or settlement message is accepted". So this package's job is to be the gate that
// nothing gets past - a session either exists with an agreed version and a bound identity,
// or there is nothing to send a message on.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newSessions(t *testing.T) *Sessions {
	t.Helper()
	return New(Config{
		Network:     PublicNetwork,
		Versions:    []int{1, 2},
		Heartbeat:   60 * time.Second,
		Freshness:   180 * time.Second,
		MaxPerTower: 1,
	})
}

func validHello() Hello { return helloFor("tw-abc123") }

// helloFor keeps the claimed Tower ID and the certificate identity in step. They must
// match, so a test that means to use a different Tower has to say so in both places.
func helloFor(towerID string) Hello {
	return Hello{
		Network:      PublicNetwork,
		Versions:     []int{1, 2},
		TowerID:      towerID,
		Capabilities: []string{CapIntegrity, CapInnerSession},
	}
}

// --- negotiation succeeds --------------------------------------------------

func TestAJoinedSessionNegotiatesOneMutuallySupportedVersion(t *testing.T) {
	s := newSessions(t)
	// The Tower offers N and N-1; we support N and N-1; both bind to N.
	acc, err := s.Open(validHello(), "tw-abc123")
	require.NoError(t, err)
	require.Equal(t, 2, acc.Version, "the highest mutually supported version, not the first")
	require.NotEmpty(t, acc.SessionID)
	require.Equal(t, 60, acc.HeartbeatSeconds)
	require.Equal(t, 180, acc.FreshnessSeconds)
}

func TestEveryLaterMessageCarriesTheBoundIdentity(t *testing.T) {
	// "every later message carries that network, version, Tower, and session identity" -
	// so a frame from one session cannot be lifted into another.
	s := newSessions(t)
	acc, err := s.Open(validHello(), "tw-abc123")
	require.NoError(t, err)

	require.NoError(t, s.Check(Frame{
		Network: PublicNetwork, Version: acc.Version,
		TowerID: "tw-abc123", SessionID: acc.SessionID,
	}))

	for name, bad := range map[string]Frame{
		"another network": {Network: "local-1", Version: acc.Version, TowerID: "tw-abc123", SessionID: acc.SessionID},
		"another version": {Network: PublicNetwork, Version: 1, TowerID: "tw-abc123", SessionID: acc.SessionID},
		"another Tower":   {Network: PublicNetwork, Version: acc.Version, TowerID: "tw-other", SessionID: acc.SessionID},
		"another session": {Network: PublicNetwork, Version: acc.Version, TowerID: "tw-abc123", SessionID: "sess-other"},
		"no session":      {Network: PublicNetwork, Version: acc.Version, TowerID: "tw-abc123"},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, s.Check(bad))
		})
	}
}

// --- the spec's rejection table -------------------------------------------

func TestNegotiationFailsBeforeInventory(t *testing.T) {
	// Each row is a way a session could exist without both peers agreeing what it is. The
	// assertion in every case is the same: no session, so nothing can be sent on it.
	cases := map[string]struct {
		hello  Hello
		certID string
	}{
		"no common protocol version": {
			hello:  Hello{Network: PublicNetwork, Versions: []int{7, 8}, TowerID: "tw-abc123", Capabilities: mandatory()},
			certID: "tw-abc123",
		},
		"a version below the signed minimum": {
			hello:  Hello{Network: PublicNetwork, Versions: []int{0}, TowerID: "tw-abc123", Capabilities: mandatory()},
			certID: "tw-abc123",
		},
		"a missing mandatory integrity capability": {
			hello:  Hello{Network: PublicNetwork, Versions: []int{2}, TowerID: "tw-abc123", Capabilities: []string{CapInnerSession}},
			certID: "tw-abc123",
		},
		"a missing inner secure-session capability": {
			hello:  Hello{Network: PublicNetwork, Versions: []int{2}, TowerID: "tw-abc123", Capabilities: []string{CapIntegrity}},
			certID: "tw-abc123",
		},
		"an unknown mandatory capability": {
			hello: Hello{Network: PublicNetwork, Versions: []int{2}, TowerID: "tw-abc123",
				Capabilities: append(mandatory(), "!unknown-mandatory")},
			certID: "tw-abc123",
		},
		"a network ID other than the public network": {
			hello:  Hello{Network: "local-9f", Versions: []int{2}, TowerID: "tw-abc123", Capabilities: mandatory()},
			certID: "tw-abc123",
		},
		"a Tower ID different from the certificate": {
			hello:  Hello{Network: PublicNetwork, Versions: []int{2}, TowerID: "tw-claimed", Capabilities: mandatory()},
			certID: "tw-actual",
		},
		"no Tower ID at all": {
			hello:  Hello{Network: PublicNetwork, Versions: []int{2}, Capabilities: mandatory()},
			certID: "tw-actual",
		},
		"no versions offered": {
			hello:  Hello{Network: PublicNetwork, TowerID: "tw-abc123", Capabilities: mandatory()},
			certID: "tw-abc123",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := newSessions(t)
			_, err := s.Open(tc.hello, tc.certID)
			require.Error(t, err)
			require.Zero(t, s.Count(), "a refused negotiation leaves no session behind")
		})
	}
}

func TestAReplayedSessionIDIsRefused(t *testing.T) {
	// The spec's row. A session id is ours to mint, so the only way one repeats is if
	// somebody is replaying - and a replayed session would let old frames be accepted as
	// current ones.
	s := newSessions(t)
	acc, err := s.Open(validHello(), "tw-abc123")
	require.NoError(t, err)

	require.Error(t, s.Adopt(acc.SessionID, "tw-abc123"),
		"a session id already in use cannot be claimed again")
}

// --- one session per Tower -------------------------------------------------

func TestASecondConnectionReplacesTheFirst(t *testing.T) {
	// A Tower that reconnects after a network blip must not end up with two sessions, or
	// its capacity is counted twice and half its frames go to a session nobody reads.
	s := newSessions(t)
	first, err := s.Open(validHello(), "tw-abc123")
	require.NoError(t, err)

	second, err := s.Open(validHello(), "tw-abc123")
	require.NoError(t, err)
	require.NotEqual(t, first.SessionID, second.SessionID)
	require.Equal(t, 1, s.Count(), "one Tower, one live session")

	// The displaced session is dead: its frames are no longer accepted.
	require.Error(t, s.Check(Frame{
		Network: PublicNetwork, Version: first.Version,
		TowerID: "tw-abc123", SessionID: first.SessionID,
	}))
	require.NoError(t, s.Check(Frame{
		Network: PublicNetwork, Version: second.Version,
		TowerID: "tw-abc123", SessionID: second.SessionID,
	}))
}

// --- liveness --------------------------------------------------------------

func TestALiveSessionStaysFreshWhileItHeartbeats(t *testing.T) {
	s := newSessions(t)
	acc, err := s.Open(validHello(), "tw-abc123")
	require.NoError(t, err)

	s.advance(150 * time.Second)
	require.NoError(t, s.Heartbeat(acc.SessionID, "tw-abc123"))

	s.advance(150 * time.Second) // 300s since open, but only 150s since the heartbeat
	require.True(t, s.Live("tw-abc123"), "a heartbeating Tower stays in routing")
}

func TestADisconnectCannotLeaveImmortalInventory(t *testing.T) {
	// "When its connection lease or inventory freshness window expires, every leaf behind
	// it is removed from new routing." Nothing polls; the window simply lapses.
	s := newSessions(t)
	_, err := s.Open(validHello(), "tw-abc123")
	require.NoError(t, err)
	require.True(t, s.Live("tw-abc123"))

	s.advance(181 * time.Second)
	require.False(t, s.Live("tw-abc123"), "a silent Tower ages out without anybody asking")
}

func TestNoHeartbeatFabricatedByAnotherTowerRefreshesIt(t *testing.T) {
	// The spec's exact requirement. It holds structurally: a heartbeat names the session,
	// and a session is bound to one Tower identity at open.
	s := newSessions(t)
	victim, err := s.Open(helloFor("tw-victim"), "tw-victim")
	require.NoError(t, err)

	_, err = s.Open(helloFor("tw-other"), "tw-other")
	require.NoError(t, err)

	require.Error(t, s.Heartbeat(victim.SessionID, "tw-other"),
		"another Tower cannot refresh a session that is not its own")

	s.advance(181 * time.Second)
	require.False(t, s.Live("tw-victim"))
}

func TestHeartbeatingAnUnknownSessionIsRefused(t *testing.T) {
	s := newSessions(t)
	require.Error(t, s.Heartbeat("sess-nobody-issued", "tw-abc123"))
}

func TestClosingRemovesTheSession(t *testing.T) {
	// A clean disconnect should not wait out the freshness window: an operator who drains
	// deliberately deserves to leave routing immediately.
	s := newSessions(t)
	acc, err := s.Open(validHello(), "tw-abc123")
	require.NoError(t, err)

	s.Close(acc.SessionID, "tw-abc123")
	require.False(t, s.Live("tw-abc123"))
	require.Zero(t, s.Count())
}

func TestClosingSomebodyElsesSessionDoesNothing(t *testing.T) {
	s := newSessions(t)
	acc, err := s.Open(validHello(), "tw-abc123")
	require.NoError(t, err)

	s.Close(acc.SessionID, "tw-other")
	require.True(t, s.Live("tw-abc123"), "a Tower cannot hang up somebody else's link")
}

// --- reconnect economics ---------------------------------------------------

func TestAReconnectWithAMatchingHeadNeedsNoSnapshot(t *testing.T) {
	// The measurement's consequence: a reconnect carries a head revision and hash, not a
	// snapshot. A fleet that did not change while we redeployed sends ~100 bytes instead
	// of megabytes, and a thousand of them do not become a thundering herd.
	s := newSessions(t)
	s.RecordHead("tw-abc123", 40, "hash-40")

	h := validHello()
	h.HeadRevision, h.HeadHash = 40, "hash-40"
	acc, err := s.Open(h, "tw-abc123")
	require.NoError(t, err)
	require.False(t, acc.NeedFullInventory, "nothing changed, so nothing is resent")
}

func TestAReconnectWithADivergedHeadResyncs(t *testing.T) {
	s := newSessions(t)
	s.RecordHead("tw-abc123", 40, "hash-40")

	for name, h := range map[string]Hello{
		"a different hash at the same revision": {HeadRevision: 40, HeadHash: "hash-other"},
		"an older revision":                     {HeadRevision: 39, HeadHash: "hash-39"},
		"a newer revision we never accepted":    {HeadRevision: 41, HeadHash: "hash-41"},
		"no head at all":                        {},
	} {
		t.Run(name, func(t *testing.T) {
			hello := validHello()
			hello.HeadRevision, hello.HeadHash = h.HeadRevision, h.HeadHash
			acc, err := s.Open(hello, "tw-abc123")
			require.NoError(t, err)
			require.True(t, acc.NeedFullInventory,
				"a head we cannot reconcile means resync, never a guess")
		})
	}
}

func TestAFirstConnectionAlwaysNeedsASnapshot(t *testing.T) {
	s := newSessions(t)
	acc, err := s.Open(helloFor("tw-newcomer"), "tw-newcomer")
	require.NoError(t, err)
	require.True(t, acc.NeedFullInventory)
}

// --- what routing asks ------------------------------------------------------

func TestLiveIsAnswerableWithoutTouchingAnythingSlow(t *testing.T) {
	// Routing asks this per request, so it must be a map read and nothing else. The test
	// asserts the shape rather than the speed: no error return, no context, no I/O.
	s := newSessions(t)
	require.False(t, s.Live("tw-nobody"))

	_, err := s.Open(validHello(), "tw-abc123")
	require.NoError(t, err)
	require.True(t, s.Live("tw-abc123"))
}

func TestLiveTowersListsOnlyFreshOnes(t *testing.T) {
	// Routing reads this list. A Tower that stopped heartbeating must leave it without
	// anybody asking that Tower anything.
	s := newSessions(t)
	fresh, err := s.Open(helloFor("tw-fresh"), "tw-fresh")
	require.NoError(t, err)
	_, err = s.Open(helloFor("tw-stale"), "tw-stale")
	require.NoError(t, err)

	require.ElementsMatch(t, []string{"tw-fresh", "tw-stale"}, s.LiveTowers())

	s.advance(181 * time.Second)
	require.NoError(t, s.Heartbeat(fresh.SessionID, "tw-fresh"), "only one of them is still there")

	live := s.LiveTowers()
	require.Contains(t, live, "tw-fresh")
	require.NotContains(t, live, "tw-stale", "the silent one is gone from routing")
}

func TestHeartbeatingWithNoSessionIsRefused(t *testing.T) {
	s := newSessions(t)
	require.Error(t, s.Heartbeat("", ""), "an empty session id is not a session")
}

func TestReapDropsAgedSessions(t *testing.T) {
	// Without this the session map only grows across a long uptime with churn.
	s := newSessions(t)
	_, err := s.Open(validHello(), "tw-abc123")
	require.NoError(t, err)
	require.Equal(t, 1, s.Count())

	s.advance(181 * time.Second)
	s.Reap()
	require.Zero(t, s.Count())
}

func mandatory() []string { return []string{CapIntegrity, CapInnerSession} }

// The relay endpoint travels Hello -> session -> here, and only while the session lives:
// an endpoint for a Tower that went away is a timeout handed to a customer.
func TestTheRelayEndpointLivesAndDiesWithTheSession(t *testing.T) {
	s := New(Config{Network: "roger-public", Versions: []int{1},
		Heartbeat: time.Minute, Freshness: 5 * time.Minute})

	// Before any session: nothing.
	_, ok := s.RelayEndpoint("tw-1")
	require.False(t, ok)

	acc, err := s.Open(Hello{Network: "roger-public", Versions: []int{1}, TowerID: "tw-1",
		Capabilities:  []string{CapIntegrity, CapInnerSession},
		RelayEndpoint: "203.0.113.7:8443"}, "tw-1")
	require.NoError(t, err)

	got, ok := s.RelayEndpoint("tw-1")
	require.True(t, ok)
	require.Equal(t, "203.0.113.7:8443", got)

	// A Tower that advertises nothing reports nothing - and a reconnect REPLACES the old
	// session, so its endpoint replaces too rather than lingering.
	_, err = s.Open(Hello{Network: "roger-public", Versions: []int{1}, TowerID: "tw-1",
		Capabilities: []string{CapIntegrity, CapInnerSession}}, "tw-1")
	require.NoError(t, err)
	_, ok = s.RelayEndpoint("tw-1")
	require.False(t, ok, "the new session advertised no endpoint, so there is none")

	s.Close(acc.SessionID, "tw-1")
}

// An endpoint that is not host:port is refused AT THE DOOR. Accepted here, it would surface
// hours later as consumers failing to connect, attributed to the wrong component.
func TestAnUnparseableRelayEndpointIsRefusedAtTheDoor(t *testing.T) {
	s := New(Config{Network: "roger-public", Versions: []int{1},
		Heartbeat: time.Minute, Freshness: 5 * time.Minute})
	_, err := s.Open(Hello{Network: "roger-public", Versions: []int{1}, TowerID: "tw-1",
		Capabilities:  []string{CapIntegrity, CapInnerSession},
		RelayEndpoint: "not-an-endpoint"}, "tw-1")
	require.ErrorContains(t, err, "host:port")
}
