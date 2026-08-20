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
	"rogerai.fm/roger/v5/internal/towerhub"
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
type movingCore struct {
	mu       sync.Mutex
	endpoint string
	pin      string
	towerID  string
	seen     []attachRecord
}

func (c *movingCore) advertise(endpoint, pin, towerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.endpoint, c.pin, c.towerID = endpoint, pin, towerID
}

func (c *movingCore) attaches() []attachRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]attachRecord(nil), c.seen...)
}

// start stands the stub Core up and returns its base URL.
func (c *movingCore) start(t *testing.T) string {
	t.Helper()
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
		c.seen = append(c.seen, body)
		endpoint, pin, towerID := c.endpoint, c.pin, c.towerID
		c.mu.Unlock()
		_ = json.NewEncoder(w).Encode(TowerAttachment{
			StationID: body.StationID, TowerID: towerID,
			Endpoint: endpoint, EndpointTLSSPKI: pin,
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
// This is the shape of every one of the four causes at once - a tower turning TLS on, moving,
// losing its lease, or going away for good. From the node's side they are indistinguishable: the
// endpoint it was handed at attach stops behaving like a hub, and nothing about the next poll
// will differ from this one. The only party that can say anything new is Core, and before this
// nothing ever asked it again.
//
// The test also pins the property that makes re-attachment SAFE rather than a way to mint a
// second identity per outage: every attach carries the same persistent station identity and the
// same keys, so Core answers it idempotently with the registration it already has.
func TestANodeWhoseRelayGoesAwayMovesToTheOneCoreNamesNext(t *testing.T) {
	// window 1ms: the FIRST error must not trip on its own (see hubFailureStreak), the second
	// must. quiet 30s: comfortably longer than towerhub's two-second poll backoff, so a genuine
	// streak is never mistaken for two unrelated blips.
	fastReattach(t, time.Millisecond, 30*time.Second, 20*time.Millisecond, 40*time.Millisecond)

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

	// It is on the first relay.
	require.Eventually(t, func() bool { return len(core.attaches()) >= 1 }, 5*time.Second, 10*time.Millisecond,
		"the node never attached at all")

	// THE RELAY GOES AWAY, AND CORE MOVES ON. This is a tower whose lease expired, or which
	// restarted on a different address, or which turned TLS on - the node cannot tell them apart
	// and does not need to.
	dead.Close()
	core.advertise(live.Listener.Addr().String(), "", "tw-new")

	require.Eventually(t, func() bool { return livePolls() > 0 }, 15*time.Second, 20*time.Millisecond,
		"the node is still polling a relay that is gone; it never went back to Core for a current one")

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
