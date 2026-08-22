package agent

// tower_reattach_test.go is the spec for a node whose RELAY changes under it.
//
// The hole these cover is not a wrong line anywhere; it is a missing loop. ServeTower attached
// once per `roger share` process, and every value deciding where and how this node polls - the
// endpoint, the certificate pin, the tower id, the relay's identity fingerprint - was read at
// that one attach and frozen for the life of the process, while the serve workers retried the
// resulting hub every two seconds forever. So a tower that turned TLS on, rotated its
// certificate, moved, expired or went away took every node behind it off the relay plane until a
// human restarted their share. It was quiet because it was survivable: the same process holds an
// ordinary broker registration throughout and keeps serving and earning on the classic path, so
// nothing goes down and nobody is paged. Exactly one income line goes to zero.
//
// WHAT THE LOOP RECOVERS IS NARROWER THAN THE LIST THAT BROKE, and these tests now say which is
// which, because for a while they did not. A re-attach re-reads the TOWER'S LINK, so it recovers
// everything that changes about a tower - its address, its certificate, its fingerprint, TLS
// going on or off. It does not recover a tower that stops existing, because Core's attach
// handler answers a live attachment idempotently with the tower it already has and no writer
// anywhere moves a live Station's origin. The flagship test here used to assert the second thing
// and pass, because its Core stub re-placed every attach; that stub now models the handler and
// the two cases have a test each.
//
// WHAT THESE TESTS ARE CAREFUL ABOUT. It would be easy to make all of this pass with a periodic
// re-attach, and that would be a worse system: an attach at Core for every node in the fleet on a
// timer, forever, and a stampede whenever one tower breaks for all of them at once. So the tests
// below pin BOTH directions - that the failures Core can answer differently produce a re-attach,
// and that the ones it cannot do not.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/towerhub"
)

// fastReattach shortens the re-attachment timings for a test and restores them afterwards.
//
// The production values are minutes because the population is correlated - the event that
// strands one node strands every node on its tower in the same instant - and none of that
// reasoning is about the mechanism these tests exercise. They are `var` rather than `const` for
// exactly this, and for nothing else.
func fastReattach(t *testing.T, window, quiet, base, cap time.Duration) {
	t.Helper()
	oldW, oldQ, oldB, oldC, oldR := hubStandingWindow, hubFailureQuiet,
		reattachBackoffBase, reattachBackoffCap, reattachStreakReset
	hubStandingWindow, hubFailureQuiet = window, quiet
	reattachBackoffBase, reattachBackoffCap = base, cap
	reattachStreakReset = time.Hour // never reset the streak inside a test
	t.Cleanup(func() {
		hubStandingWindow, hubFailureQuiet = oldW, oldQ
		reattachBackoffBase, reattachBackoffCap = oldB, oldC
		reattachStreakReset = oldR
	})
}

// attachRecord is one thing Core was told at /tower/edge/attach.
type attachRecord struct {
	StationID    string `json:"station_id"`
	AssertionKey string `json:"assertion_key"`
}

// movingCore is a Roger Core whose answer to /tower/edge/attach can CHANGE between calls, which
// is the whole point: a re-attach is worth doing only because Core may have something new to say.
//
// IT MODELS THE THREE THINGS CORE'S ATTACH HANDLER ACTUALLY DOES, and the reason that is written
// out at length is that the version before it modelled none of them. It returned whatever had
// last been advertised, to anybody, forever - so a test could set "Core now names a different
// tower" and watch a node follow it, and the test on top of it was named for exactly that
// property. Core does not have it. The handler is
// cmd/rogerai-broker/toweredgeattach.go and its shape is:
//
//  1. ByAssertionKey. A node presenting keys Core already holds a LIVE attachment for takes the
//     idempotent-retry branch, and that branch never re-runs placement. The answer names
//     prior.Origin.TowerID - the tower this Station was placed on the first time - whatever has
//     happened to that tower since.
//  2. RelayPlane(towerID), which is a lookup with a SECOND RETURN VALUE. It answers from this
//     instance's live link sessions, so a tower with no session here has no plane, and Core
//     refuses rather than answering with an endpoint it does not have.
//  3. First-fit placement over the live towers, which happens for a FRESH assertion key and
//     nowhere else.
//
// So what a re-attach can recover is everything that changes ABOUT a tower - its address, its
// certificate, its identity fingerprint, TLS going on or off - because all of those are re-read
// from the plane. What it cannot recover is a tower that stops existing, because moving a live
// Station to a different tower is a thing Core has no path for (docs/relay-selection-design.md
// section 6). A stub that hides that distinction turns the second case into a passing test.
type relayPlane struct{ endpoint, pin string }

type movingCore struct {
	mu sync.Mutex
	// planes is link.Sessions' relay plane per tower id: what a tower is advertising on its
	// link right now. A tower with no entry has no live link session on this instance, which
	// is what RelayPlane reports by returning false.
	planes map[string]relayPlane
	// placed is what ByAssertionKey finds: assertion key -> the tower this Station was placed
	// on. Once written it is never rewritten, because Core has no writer that moves a live
	// attachment's origin_tower.
	placed map[string]string
	// next is where a FRESH placement lands - the stand-in for first-fit over LiveTowers.
	next     string
	seen     []attachRecord
	refusals int
}

// advertise brings tower `towerID` up at this endpoint and pin, and makes it where the next
// FRESH placement lands. Calling it again for a tower that already has stations moves that
// tower's plane under them, which is the ordinary case a re-attach exists to pick up.
func (c *movingCore) advertise(endpoint, pin, towerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.planes == nil {
		c.planes, c.placed = map[string]relayPlane{}, map[string]string{}
	}
	c.planes[towerID] = relayPlane{endpoint: endpoint, pin: pin}
	c.next = towerID
}

// linkLost drops a tower's relay plane: its link session is gone from this instance. THE
// STATIONS ALREADY PLACED ON IT KEEP POINTING AT IT, which is the fact the flagship test used to
// paper over.
func (c *movingCore) linkLost(towerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.planes, towerID)
}

func (c *movingCore) attaches() []attachRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]attachRecord(nil), c.seen...)
}

// refused counts the attaches Core could not answer - the 503 an attach takes when the tower it
// is bound to has no data plane.
func (c *movingCore) refused() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refusals
}

// start stands the stub Core up and returns its base URL.
func (c *movingCore) start(t *testing.T) string {
	t.Helper()
	c.mu.Lock()
	if c.planes == nil {
		c.planes, c.placed = map[string]relayPlane{}, map[string]string{}
	}
	c.mu.Unlock()
	corePub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	envKey := make([]byte, 32)
	_, err = rand.Read(envKey)
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.HandleFunc("/tower/edge/attach", func(w http.ResponseWriter, r *http.Request) {
		var body attachRecord
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.seen = append(c.seen, body)
		// ByAssertionKey first, exactly as the handler does: a live attachment for these keys is
		// answered with the tower it already has. Placement is only reached by a key Core has
		// never seen.
		towerID, attached := c.placed[body.AssertionKey]
		if !attached {
			towerID = c.next
		}
		plane, has := c.planes[towerID]
		if !has {
			// RelayPlane said no. Core refuses rather than answering with an endpoint it does
			// not have, and it does NOT re-place a live Station onto some other tower.
			c.refusals++
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"no data plane for this attachment right now"}`))
			return
		}
		if !attached {
			c.placed[body.AssertionKey] = towerID
		}
		_ = json.NewEncoder(w).Encode(TowerAttachment{
			StationID: body.StationID, TowerID: towerID,
			Endpoint: plane.endpoint, EndpointTLSSPKI: plane.pin,
			// A current node refuses an attach answer with no relay fingerprint: it is what lets
			// it tell the hub's own epoch from an on-path attacker's.
			TowerKeyHash: "00", State: "active",
		})
	})
	mux.HandleFunc("/tower/dispatch/key", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"dispatch_key": hex.EncodeToString(corePub),
			"envelope_key": hex.EncodeToString(envKey),
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// countingHub answers empty long polls and counts them, which is how a test can tell the
// difference between a relay a node is USING and one it was merely told about.
func countingHub(t *testing.T, tls bool) (*httptest.Server, func() int) {
	t.Helper()
	var polls int64
	var mu sync.Mutex
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == towerhub.PathPoll {
			mu.Lock()
			polls++
			mu.Unlock()
		}
		// A short empty long poll: the node's own emptyPollFloor keeps this from spinning.
		w.WriteHeader(http.StatusNoContent)
	})
	var srv *httptest.Server
	if tls {
		srv = httptest.NewTLSServer(h)
	} else {
		srv = httptest.NewServer(h)
	}
	t.Cleanup(srv.Close)
	return srv, func() int {
		mu.Lock()
		defer mu.Unlock()
		return int(polls)
	}
}

// A RELAY THAT STOPS ANSWERING AT THE ADDRESS CORE GAVE OUT IS NOT THE END OF THIS NODE'S DAY.
//
// This is what re-attachment actually buys, stated as narrowly as it is true. The endpoint, the
// certificate pin and the relay's identity fingerprint are all read from the TOWER'S LINK at the
// moment Core answers, so a tower that moved to a new address, restarted on a new port, turned
// TLS on or rotated its certificate is picked up by asking again - and before this there was
// nothing that asked. From the node's side those causes are indistinguishable: the endpoint it
// was handed stops behaving like a hub and nothing about the next poll will differ from this one.
//
// WHAT IT DOES NOT BUY IS A DIFFERENT TOWER, and the test below this one is where that is said.
// This one is deliberately the same tower id throughout, because that is the case Core can
// answer.
//
// The test also pins the property that makes re-attachment SAFE rather than a way to mint a
// second identity per outage: every attach carries the same persistent station identity and the
// same keys, so Core answers it idempotently with the registration it already has.
func TestANodeWhoseRelayMovesFollowsItToItsNewAddress(t *testing.T) {
	// window 1ms: the FIRST error must not trip on its own (see hubFailureStreak), the second
	// must. quiet 30s: comfortably longer than towerhub's two-second poll backoff, so a genuine
	// streak is never mistaken for two unrelated blips.
	fastReattach(t, time.Millisecond, 30*time.Second, 20*time.Millisecond, 40*time.Millisecond)

	dead, _ := countingHub(t, false)
	live, livePolls := countingHub(t, false)
	core := &movingCore{}
	core.advertise(dead.Listener.Addr().String(), "", "tw-1")
	brokerURL := core.start(t)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	notices := &noticeSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = ServeTower(ctx, Config{
			Broker: brokerURL, Model: "m", Modality: "chat", Parallel: 1, Upstream: "http://127.0.0.1:1",
		}, priv, t.TempDir(), discard{}, notices.report)
	}()

	// It is on the first relay.
	require.Eventually(t, func() bool { return len(core.attaches()) >= 1 }, 5*time.Second, 10*time.Millisecond,
		"the node never attached at all")

	// THE RELAY MOVES. Same tower, new address - a hub restarted on a different port, a lease
	// renewed onto a new IP, a container rescheduled. The node cannot tell which and does not
	// need to; what it needs is to ask.
	dead.Close()
	core.advertise(live.Listener.Addr().String(), "", "tw-1")

	require.Eventually(t, func() bool { return livePolls() > 0 }, 15*time.Second, 20*time.Millisecond,
		"the node is still polling the address its relay has left; it never went back to Core for the current one")

	// THE OPERATOR IS TOLD, AND TOLD WHY. Both halves matter: that a plane they never opted into
	// has failed, and that the node is handling it rather than waiting for them. The "%!w" guard
	// is not decoration - the first version of this notice unwrapped a multi-%w error, which
	// yields nil, and handed the operator a formatting artefact where the reason should be.
	require.True(t, notices.sawContaining("asking Roger Core for its current relay"),
		"the relay plane failed and recovered and the operator was never told either half")
	require.False(t, notices.sawContaining("%!w"),
		"the re-attach notice rendered its cause as a formatting artefact")

	// AND IT WENT BACK AS ITSELF. A re-attach that presented fresh keys would be a second
	// identity per outage, would not be answered idempotently, and would strand the attachment
	// Core already holds this station's receipts against.
	seen := core.attaches()
	require.GreaterOrEqual(t, len(seen), 2, "expected a re-attach, got %d attaches", len(seen))
	for i, a := range seen {
		require.Equal(t, seen[0].StationID, a.StationID,
			"attach %d offered a different station identity", i)
		require.Equal(t, seen[0].AssertionKey, a.AssertionKey,
			"attach %d offered different keys than the ones Core recorded", i)
		require.NotEmpty(t, a.AssertionKey)
	}
	cancel()
	<-done
}

// A TOWER THAT STOPS EXISTING IS NOT RECOVERED BY RE-ATTACHING, AND THIS TEST IS HERE TO SAY SO.
//
// It replaces one named TestANodeWhoseRelayGoesAwayMovesToTheOneCoreNamesNext, which asserted
// that a node whose tower goes away follows Core to the next one. It passed. The system has
// never done it. The stub it ran against answered every attach with whatever had last been
// advertised, so "Core now names a different tower" was a line in a test and nothing else; the
// real handler takes the idempotent-retry branch on a live attachment and answers with
// prior.Origin.TowerID, and nothing anywhere rewrites that value for a live Station. The only
// thing that makes an origin writable again is DetachIdle - it writes state='dormant', which is
// the one precondition Admit's upsert needs before it may write origin_tower - and DetachIdle
// runs from publishRoutable, which the housekeeping tick calls only for towers in LiveTowers(),
// on a seven-day horizon. A permanently dead tower is in no such list, so the escape hatch does
// not fire for the single case that needs it.
//
// WHAT THE NODE DOES INSTEAD IS THE PART THAT IS WORTH PINNING. It keeps its identity, it asks
// Core again on the backoff rather than polling a dead address in silence, it is refused
// honestly (a 503, not a 200 carrying an empty endpoint - see
// TestSelfAttachRetryIsRefusedWhenItsTowerHasNoDataPlane), and it says so to the operator. That
// is a bounded, visible, recoverable-by-Core state rather than a stranding, and it is the whole
// of what this branch delivers for this case.
//
// Rehoming a live Station is section 6 of docs/relay-selection-design.md and it needs a
// settle-time fence that does not exist yet. When it lands, THIS test is the one that changes.
func TestATowerThatStopsExistingIsNotRecoveredByReattaching(t *testing.T) {
	fastReattach(t, time.Millisecond, 30*time.Second, 50*time.Millisecond, 100*time.Millisecond)

	dead, _ := countingHub(t, false)
	live, livePolls := countingHub(t, false)
	core := &movingCore{}
	core.advertise(dead.Listener.Addr().String(), "", "tw-old")
	brokerURL := core.start(t)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	notices := &noticeSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = ServeTower(ctx, Config{
			Broker: brokerURL, Model: "m", Modality: "chat", Parallel: 1, Upstream: "http://127.0.0.1:1",
		}, priv, t.TempDir(), discard{}, notices.report)
	}()
	require.Eventually(t, func() bool { return len(core.attaches()) >= 1 }, 5*time.Second, 10*time.Millisecond,
		"the node never attached at all")

	// THE TOWER GOES, FOR GOOD: its lease expired, it was revoked, the operator turned it off.
	// Its link session is gone, so it has no relay plane. A DIFFERENT tower is live and
	// advertising - which is what makes this a test about placement and not about an empty fleet.
	dead.Close()
	core.linkLost("tw-old")
	core.advertise(live.Listener.Addr().String(), "", "tw-new")

	// The node goes back to Core, repeatedly, and is refused - which is the honest answer and
	// the one it can act on later.
	require.Eventually(t, func() bool { return core.refused() >= 2 }, 15*time.Second, 20*time.Millisecond,
		"the node stopped asking Core after its relay died")
	require.True(t, notices.sawContaining("could not get back onto the relay fabric"),
		"the node was refused a relay over and over and the operator was told nothing")

	// AND IT IS STILL BOUND TO THE TOWER THAT IS GONE. This is the limitation, asserted rather
	// than hoped about: Core does not re-place a live Station, so the live tower standing right
	// there is never polled.
	require.Zero(t, livePolls(),
		"the node reached a tower Core never placed it on - if re-placement has landed, this test "+
			"is the one to rewrite, along with section 6 of docs/relay-selection-design.md")

	// It never minted a second identity while being refused, either.
	seen := core.attaches()
	require.GreaterOrEqual(t, len(seen), 3)
	for i, a := range seen {
		require.Equal(t, seen[0].AssertionKey, a.AssertionKey,
			"attach %d offered different keys; a refusal must not turn into a new station per retry", i)
	}
	cancel()
	<-done
}

// A REPLACED CERTIFICATE IS PICKED UP WITHOUT A RESTART, AND WITHOUT WAITING OUT THE WINDOW.
//
// This is the case that was found while building the pin (commit 1f8fbb7a) and written up as a
// migration cost in docs/relay-selection-design.md section 5.7: a tower rotating its hub
// certificate strands every node already serving through it, because the pin is read at attach
// and held for the life of the process. A pin mismatch reaches the node as an ordinary transport
// error, which its loops answer with a quiet two-second retry into a discarded writer - correct
// for a hub that is down and exactly wrong here, where retrying cannot help by construction.
//
// The standing window is set to an HOUR on purpose. Every other symptom might be ninety seconds
// of bad luck; this one cannot be, so it does not serve the window, and a test that shortened the
// window would prove nothing about that. Recovery here has to come from the short circuit or not
// at all.
func TestAReplacedHubCertificateIsPickedUpWithoutARestart(t *testing.T) {
	fastReattach(t, time.Hour, time.Hour, 20*time.Millisecond, 40*time.Millisecond)

	hub, hubPolls := countingHub(t, true)
	core := &movingCore{}
	// The pin for a certificate this hub does not hold: an on-path attacker, and equally a relay
	// whose certificate was replaced after Core published the old fingerprint.
	core.advertise(hub.Listener.Addr().String(), stubPin, "tw-1")
	brokerURL := core.start(t)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	notices := &noticeSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = ServeTower(ctx, Config{
			Broker: brokerURL, Model: "m", Modality: "chat", Parallel: 1, Upstream: "http://127.0.0.1:1",
		}, priv, t.TempDir(), discard{}, notices.report)
	}()

	require.Eventually(t, func() bool { return notices.sawContaining("not the one Roger Core named") },
		5*time.Second, 10*time.Millisecond, "the node never noticed the certificate was wrong")

	// CORE LEARNS THE NEW FINGERPRINT - which is what the tower's next Hello does for it.
	core.advertise(hub.Listener.Addr().String(), towerhub.CertPin(hub.Certificate()), "tw-1")

	require.Eventually(t, func() bool { return hubPolls() > 0 }, 15*time.Second, 20*time.Millisecond,
		"the node never accepted the certificate Core now names; it is still holding the old pin")

	// AND THE OPERATOR WAS NOT TOLD TO GO AND DO IT BY HAND. The instruction used to be "restart
	// this share to pick up the new one", and a ritual an operator learns will outlive the reason
	// for it by years.
	require.False(t, notices.sawContaining("restart this share"),
		"the node recovers on its own now, and telling the operator otherwise teaches a ritual")
	cancel()
	<-done
}

// A HUB THAT REFUSES THIS NODE'S IDENTITY IS NOT A REASON TO ASK CORE AGAIN.
//
// This is the other direction, and it is the one a lazy implementation gets wrong. A 401 means
// the hub is there, answering, and has decided this node is nobody - a relay running a
// roger-tower older than signed polls, a revoked attachment, a clock that is badly wrong. Attach
// is idempotent for a live attachment, so a re-attach hands back the same tower, the same
// endpoint and the same keys, and the next poll is refused exactly as this one was. Re-attaching
// on it would be a permanent, pointless load at Core from every mismatched pair in the fleet.
//
// SAID PLAINLY: this assertion also holds against the code before re-attachment existed, where
// there was exactly one attach because there was no second one available. What it is here to
// catch is the WRONG version of this change - a blind timer, or an exclusion list that forgot
// the 401 - and the operator-facing half below is what it shares with the old behaviour.
func TestARefusedIdentityDoesNotSendTheNodeBackToCore(t *testing.T) {
	fastReattach(t, time.Millisecond, 30*time.Second, 10*time.Millisecond, 20*time.Millisecond)

	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not the registered node for this Station", http.StatusUnauthorized)
	}))
	t.Cleanup(hub.Close)
	core := &movingCore{}
	core.advertise(hub.Listener.Addr().String(), "", "tw-old")
	brokerURL := core.start(t)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	notices := &noticeSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = ServeTower(ctx, Config{
			Broker: brokerURL, Model: "m", Modality: "chat", Parallel: 1, Upstream: "http://127.0.0.1:1",
		}, priv, t.TempDir(), discard{}, notices.report)
	}()

	// The operator IS told - that half has not changed, and it is the whole of the available
	// remedy for a failure Core cannot answer differently.
	require.Eventually(t, func() bool { return notices.sawContaining("refuses its identity") },
		5*time.Second, 10*time.Millisecond, "the hub refused every poll and the operator was told nothing")

	// LONG ENOUGH TO CATCH THE WRONG VERSION, which is the only thing this assertion is for.
	// towerhub backs a failing poll off by two seconds, so a 401 wrongly classified as evidence
	// that the address is stale would produce its second error at ~2s, trip the streak, and
	// re-attach inside the tiny backoff above. Five seconds is two full cycles past that; a
	// shorter wait passed against a version that DID re-attach on a 401, which is how a guard
	// test comes to guard nothing.
	time.Sleep(5 * time.Second)
	require.Len(t, core.attaches(), 1,
		"a refused identity sent this node back to Core; attach is idempotent, so it would get the "+
			"same tower and the same refusal, forever, from every mismatched pair in the fleet")
	cancel()
	<-done
}

// The classification, stated as a rule rather than inferred from the behaviour of one stub.
//
// staleAdvertisement answers one question: is this failure evidence that what Core told this node
// - the address, or the certificate that answers at it - has stopped being true? It is an
// exclusion list because the default has to be "ask Core again": the opposite default is the one
// that shipped, and the one that shipped stranded nodes in silence.
func TestOnlyAStaleAdvertisementSendsANodeBackToCore(t *testing.T) {
	// Evidence the address is wrong. Nothing about the next poll differs from this one, and Core
	// is the only party that can hand out a different one.
	require.True(t, staleAdvertisement(towerhub.ErrHubCertificateUnpinned))
	require.True(t, staleAdvertisement(&towerhub.HTTPError{Status: 404, Body: "not found"}))
	require.True(t, staleAdvertisement(&towerhub.HTTPError{Status: 410, Body: "gone"}))
	require.True(t, staleAdvertisement(&towerhub.HTTPError{Status: 503, Body: "unavailable"}))
	require.True(t, staleAdvertisement(errorString("dial tcp 203.0.113.9:8443: connect: connection refused")),
		"an unrecognised transport failure defaults to asking Core, because the other default is what shipped")

	// NOT evidence the address is wrong, each for its own reason.
	require.False(t, staleAdvertisement(nil))
	require.False(t, staleAdvertisement(context.Canceled))
	require.False(t, staleAdvertisement(&towerhub.HTTPError{Status: 401, Body: "no"}),
		"the hub is answering and has refused this node; Core repeating itself changes nothing")
	require.False(t, staleAdvertisement(towerhub.ErrHubMultipleProcesses),
		"a fresh client has an empty retired-epoch memory, so re-attaching would hide the detection")
	require.False(t, staleAdvertisement(towerhub.ErrNotCarried),
		"the hub answered: it is up and handing this node work, which is the opposite of this evidence")
	require.False(t, staleAdvertisement(towerhub.ErrResultUndelivered))
}

// The window, which is what keeps this from being a blind timer.
func TestOneBadPollIsNotAStandingFailure(t *testing.T) {
	fastReattach(t, 90*time.Second, 60*time.Second, time.Second, time.Second)
	s := &hubFailureStreak{}
	t0 := time.Now()
	down := errorString("dial tcp: connection refused")

	require.False(t, s.observe(down, t0), "one error is a blip, not a standing failure")
	require.False(t, s.observe(down, t0.Add(30*time.Second)))
	require.False(t, s.observe(down, t0.Add(89*time.Second)))
	require.True(t, s.observe(down, t0.Add(90*time.Second)),
		"ninety seconds of continuous failure is the relay, not the weather")

	// A QUIET MINUTE ENDS A STREAK. Without this, one bad poll an hour accumulates into a
	// "standing" failure on a node that has been serving perfectly all day.
	q := &hubFailureStreak{}
	require.False(t, q.observe(down, t0))
	require.False(t, q.observe(down, t0.Add(61*time.Second)), "the gap exceeded the quiet window: new streak")
	// From here the errors arrive the way a broken hub actually produces them - towerhub backs a
	// failing poll off by two seconds - so nothing below re-triggers the quiet reset, and the
	// clock that matters is the one running from the start of THIS streak.
	require.False(t, q.observe(down, t0.Add(91*time.Second)))
	require.False(t, q.observe(down, t0.Add(121*time.Second)))
	require.False(t, q.observe(down, t0.Add(150*time.Second)), "89s into the second streak, not 150s into the first")
	require.True(t, q.observe(down, t0.Add(151*time.Second)))

	// A COMPLETION THE HUB ANSWERED CLEARS THE STREAK. It costs the operator money and it is
	// loud, but it proves the relay is up and handing this node work - and re-attaching to the
	// same tower would not fix a hub that loses receipts.
	r := &hubFailureStreak{}
	require.False(t, r.observe(down, t0))
	require.False(t, r.observe(towerhub.ErrNotCarried, t0.Add(time.Second)))
	require.False(t, r.observe(down, t0.Add(89*time.Second)),
		"the streak should have restarted at the completion, not run through it")

	// AND THE CERTIFICATE SKIPS THE WINDOW ENTIRELY, because it is the one failure that cannot
	// be ninety seconds of bad luck: the pin is fixed for the tenancy, so no retry can differ.
	c := &hubFailureStreak{}
	require.True(t, c.observe(towerhub.ErrHubCertificateUnpinned, t0))
}

// A HUB THAT ACCEPTS THE CONNECTION AND ANSWERS NOTHING IS STILL A HUB THAT WENT AWAY.
//
// This is the case the window was unreachable for, and it is not an exotic one - it is a tower
// powered off with the socket still listening, an address reassigned under a running listener, a
// firewall or NAT rule that black-holes rather than refuses. The node's symptom is one error per
// (hubPollTimeout + towerhub.PollBackoff), which was SIXTY-TWO SECONDS against a quiet window of
// sixty: every error landed outside the window it should have extended, restarted the streak
// instead of continuing it, and hubStandingWindow was never reached. Not slowly - never. A node
// polled a dead address for the life of the process and nothing in the loop above ever tripped.
//
// IT RUNS AT PRODUCTION CONSTANTS ON PURPOSE, with no fastReattach, because the defect WAS the
// production constants and every test that shortened them hid it. The failure the older tests
// use is a refusing listener, which answers in milliseconds and spaces its errors two seconds
// apart, so a sixty-second quiet window was never in danger and the mechanism looked fine.
//
// The first assertion is the invariant rather than the symptom: one whole failure must fit
// inside the quiet window with room to spare, or the streak can never accumulate whatever the
// window below it is set to. That is the relationship the fix encodes, and it is the thing to
// keep true if either number is ever changed again.
func TestASlowFailureStillAccumulatesIntoAStreak(t *testing.T) {
	require.Greater(t, hubFailureQuiet, hubPollTimeout+towerhub.PollBackoff,
		"one failure costs the poll timeout plus the backoff; a quiet window shorter than that is "+
			"restarted by every error it is supposed to be extended by, and no streak can ever form")

	// What net/http hands back when a hub accepts and then says nothing at all.
	blackHole := errorString(`Get "http://203.0.113.9:8443/hub/poll": context deadline exceeded ` +
		`(Client.Timeout exceeded while awaiting headers)`)
	require.True(t, staleAdvertisement(blackHole),
		"a timed-out poll must default to asking Core; it is the exact shape of a tower going away")

	s := &hubFailureStreak{}
	at := time.Now()
	spacing := hubPollTimeout + towerhub.PollBackoff
	tripped := -1
	for i := 0; i < 200; i++ {
		if s.observe(blackHole, at) {
			tripped = i
			break
		}
		at = at.Add(spacing)
	}
	require.GreaterOrEqual(t, tripped, 0,
		"two hundred consecutive timed-out polls - over three hours of a relay carrying nothing - "+
			"and this node still believes in the address it was handed")
	// Two gaps of sixty-two seconds clears a ninety-second window, so the third failure is the
	// one that trips it. Bounded rather than pinned exactly: what matters is that it is a small
	// number of failures and not "never".
	require.LessOrEqual(t, tripped, 4,
		"the streak took %d failures (%s) to trip on a relay that never answered", tripped,
		time.Duration(tripped)*spacing)
}

// The backoff, which is what keeps a broken tower from arriving at Core as a broken fleet.
func TestReattachBackoffGrowsIsCappedAndIsJittered(t *testing.T) {
	fastReattach(t, time.Second, time.Second, 30*time.Second, 15*time.Minute)

	inBand := func(consecutive int, d time.Duration) {
		t.Helper()
		want := reattachBackoffBase
		for i := 0; i < consecutive && want < reattachBackoffCap; i++ {
			want *= 2
		}
		if want > reattachBackoffCap {
			want = reattachBackoffCap
		}
		require.GreaterOrEqual(t, d, want/2, "consecutive=%d fell below half the band", consecutive)
		require.LessOrEqual(t, d, want, "consecutive=%d exceeded its band", consecutive)
	}
	for _, n := range []int{0, 1, 2, 3, 8, 40, 1000} {
		for i := 0; i < 50; i++ {
			inBand(n, reattachDelay(n))
		}
	}
	// JITTERED, not merely bounded. The event that strands one node strands every node on its
	// tower in the same instant; an un-jittered backoff would have all of them attach in the same
	// second, wait the same amount, and attach in the same second again.
	seen := map[time.Duration]bool{}
	for i := 0; i < 200; i++ {
		seen[reattachDelay(0)] = true
	}
	require.Greater(t, len(seen), 50, "the backoff is not jittered: a fleet would attach in lockstep")

	// AND THE CAP HOLDS. A node stranded on a permanently broken pairing costs Core one message
	// per cap, not one per poll.
	require.LessOrEqual(t, reattachDelay(1000), reattachBackoffCap)
}

// THE BACKOFF'S MEMORY, which had none of its own.
//
// reattachStreakReset decides whether a broken tenancy is the next attempt at an old problem or
// a fresh one, and it had zero coverage: fastReattach sets it to an hour so that no test's
// tenancy can reach it, which is right for the tests that use it and meant the reset never
// executed under test at all. Every node whose relay breaks twice in one day runs this line.
//
// It is asserted through reattachDelay rather than on the integer, because the integer is not
// the point - what an operator experiences is the wait, and the property is that a tenancy which
// STOOD does not make them serve out the previous outage's fifteen minutes.
func TestATenancyThatStoodStartsTheBackoffOver(t *testing.T) {
	fastReattach(t, time.Second, time.Second, 30*time.Second, 15*time.Minute)
	reattachStreakReset = 10 * time.Minute // fastReattach parks it at an hour; this test is about it

	// A run of short tenancies is one event with many attempts, so the count carries and the
	// wait climbs.
	require.Equal(t, 6, streakAfterTenancy(6, reattachStreakReset-time.Nanosecond),
		"a tenancy that broke immediately is the next attempt at the same outage")
	require.Equal(t, 0, streakAfterTenancy(6, reattachStreakReset),
		"the boundary belongs to the tenancy that lasted: >= reattachStreakReset, not >")
	require.Equal(t, 0, streakAfterTenancy(6, time.Hour))
	require.Equal(t, 0, streakAfterTenancy(0, time.Nanosecond),
		"a node that has never had to re-attach does not start at one")

	// AND THE CONSEQUENCE, IN THE UNIT THE OPERATOR FEELS. Six consecutive failures puts the
	// backoff at its fifteen-minute cap; a tenancy that stood for ten minutes must not cost the
	// next recovery that cap.
	stillBroken := reattachDelay(streakAfterTenancy(6, time.Minute))
	require.GreaterOrEqual(t, stillBroken, reattachBackoffCap/2,
		"a relay that keeps failing must keep backing off")

	freshEvent := reattachDelay(streakAfterTenancy(6, 11*time.Minute))
	require.LessOrEqual(t, freshEvent, reattachBackoffBase,
		"a tenancy that stood for eleven minutes and then broke is a new event; this node waited "+
			"%s to ask about it, which is the previous outage's penalty being served twice", freshEvent)
}

// THE FIRST ATTACH IS RETRIED, AND IT STOPS.
//
// Generation zero used to return on its first refusal, which preserved for process startup the
// exact stranding the rest of this file exists to remove: a `roger share` that comes up while
// Core is redeploying gets no relay plane until a human restarts a share that is otherwise
// working perfectly. It is bounded rather than endless because "no relay is free" is a
// FLEET-WIDE condition - unlike a broken tower, which strands one tower's worth of nodes - and
// an unbounded retry on it would put the whole fleet at Core's door on a schedule.
func TestAFirstAttachIsRetriedABoundedNumberOfTimes(t *testing.T) {
	fastReattach(t, time.Millisecond, 30*time.Second, time.Millisecond, 2*time.Millisecond)
	old := firstAttachAttempts
	firstAttachAttempts = 4
	t.Cleanup(func() { firstAttachAttempts = old })

	var asked int64
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tower/edge/attach" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mu.Lock()
		asked++
		mu.Unlock()
		http.Error(w, "no tower can host this node right now - try again shortly",
			http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	serr := ServeTower(ctx, Config{
		Broker: srv.URL, Model: "m", Modality: "chat", Parallel: 1, Upstream: "http://127.0.0.1:1",
	}, priv, t.TempDir(), discard{}, nil)

	// THE SAME ERROR STILL COMES BACK. cmd/rogerai reads the returned error and reports one
	// shape of it; the change is when it arrives, not what it is.
	require.Error(t, serr, "a first attach that was refused every time must still be reported")
	mu.Lock()
	defer mu.Unlock()
	require.EqualValues(t, firstAttachAttempts, asked,
		"a refused first attach asked %d times; it must ask more than once and then stop", asked)
}

// errorString is a plain error with no sentinel in it - what an unrecognised transport failure
// actually looks like coming out of net/http.
type errorString string

func (e errorString) Error() string { return string(e) }

// discard is the progress seam `roger share` passes, spelled out here so these tests exercise the
// same silence an operator gets.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// stubPin is a well-formed fingerprint for a certificate nobody holds.
const stubPin = "abababababababababababababababababababababababababababababababab"
