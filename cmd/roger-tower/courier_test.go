package main

// THE SETTLE COURIER, ON A CLOCK THE TEST HOLDS.
//
// A completed job's receipt enters the courier's queue from a goroutine the hub handler spawns,
// and the courier can meet it in two places: its main select (then it waits out the ack grace
// and forwards) or, if the stop signal was taken first, the final drain. Which one ran was
// decided by the scheduler, and the whole-ride test closed stop the instant the consumer's
// submit returned - so the coverage gate measured cmd/roger-tower at 91.1% and then 90.9% on an
// identical tree, with the five blocks of the main path and the one block of the final drain
// flipping as exact complements. The retry tick, the ack-grace skip on the overflow path and
// the deadline abandonment were never reached at all: their clocks are 15 seconds, 2 seconds
// and 10 minutes.
//
// The courier's three clocks are package variables (hub.go: settleNow, settleAfter,
// settleTicker), the same seam shape as lookupIPFn and the ticker runLink takes. courierClock
// holds all three: the grace wait and the quiet window open when the test says, a retry tick
// fires when the test sends one, and "now" moves when the test advances it. Every receipt still
// rides a real hub, a real signed /complete and a real forward to the Core stub; only when the
// courier looks at a clock is decided here.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/tower"
	"rogerai.fm/roger/v6/internal/towercore/dispatch"
	"rogerai.fm/roger/v6/internal/towercore/link"
	"rogerai.fm/roger/v6/internal/towerhub"
)

// courierClock is the test's hand on the courier's clocks for ONE hub run. Install it before
// runHubInBackground and call wait() before the test returns; the cleanup restores the real
// clocks, and the hub's other goroutines never read these.
type courierClock struct {
	mu   sync.Mutex
	now  time.Time
	tick chan time.Time
	// inGrace is closed the first time the courier enters its ack-grace wait; releaseGrace ends
	// that wait when the test closes it.
	inGrace, releaseGrace chan struct{}
	graceOnce             sync.Once
	// inQuiet is closed the first time the courier enters its final-drain quiet window;
	// endQuiet ends the window when the test closes it.
	inQuiet, endQuiet chan struct{}
	quietOnce         sync.Once
	// releaseOnce and endOnce guard the two signals the TEST closes: a test closes them where
	// its scenario needs, and the cleanup closes whatever is still open, so no After goroutine
	// outlives the test however far the courier got.
	releaseOnce, endOnce sync.Once
}

// release ends the ack-grace wait; end closes the final drain's quiet window. Both are idempotent.
func (c *courierClock) release() { c.releaseOnce.Do(func() { close(c.releaseGrace) }) }
func (c *courierClock) end()     { c.endOnce.Do(func() { close(c.endQuiet) }) }

func installCourierClock(t *testing.T) *courierClock {
	t.Helper()
	c := &courierClock{
		now:     time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC),
		tick:    make(chan time.Time),
		inGrace: make(chan struct{}), releaseGrace: make(chan struct{}),
		inQuiet: make(chan struct{}), endQuiet: make(chan struct{}),
	}
	prevNow, prevAfter, prevTicker := settleNow, settleAfter, settleTicker
	settleNow = c.Now
	settleAfter = c.After
	settleTicker = func(time.Duration) (<-chan time.Time, func()) { return c.tick, func() {} }
	t.Cleanup(func() {
		// Every wait the clock handed out is released before the real clocks come back: a
		// run that never reached the grace or the quiet window would otherwise leave a
		// goroutine parked on each for the life of the process.
		c.release()
		c.end()
		settleNow, settleAfter, settleTicker = prevNow, prevAfter, prevTicker
	})
	return c
}

func (c *courierClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *courierClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// After tells the two waits apart by their length: the quiet window is a named constant and
// the ack grace is whatever remains of settleAckGrace, which is never that constant.
func (c *courierClock) After(d time.Duration) <-chan time.Time {
	out := make(chan time.Time, 1)
	if d == settleQuietWindow {
		c.quietOnce.Do(func() { close(c.inQuiet) })
		go func() { <-c.endQuiet; out <- c.Now() }()
		return out
	}
	c.graceOnce.Do(func() { close(c.inGrace) })
	go func() { <-c.releaseGrace; out <- c.Now() }()
	return out
}

// awaitClosed is a bounded wait on one of the clock's signals, named so a hang reads as the
// courier never reaching that point rather than as a timeout.
func awaitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("the courier never %s", what)
	}
}

// hubRide is one running hub with a real consumer and a real node on it, up to the point where
// the node holds a job it has not yet completed. The completion is the test's, because what
// the tests here differ on is WHEN it lands.
type hubRide struct {
	core     *coreStub
	st       *tower.State
	out      *syncBuffer
	stop     chan struct{}
	stopOnce sync.Once
	wait     func()
	addr     string
	node     *towerhub.Client
	consumer *towerhub.Client
	grant    []byte
	// submitted receives the consumer's answer once the node completes.
	submitted chan towerhub.Result
}

// startHubRide builds the Core stub (with a settle endpoint that answers 200 unless `settle`
// says otherwise), the tower, the hub, and the two clients, and submits one job. It returns
// once the node has polled that job. nodeTransport, when not nil, is the node client's
// transport from its very first request: a towerhub.Client copies its http.Client on first use,
// so a transport installed later is never consulted.
//
// The hub is ALWAYS wound down when the test ends, pass or fail: a ride abandoned by a failed
// assertion would otherwise keep its courier reading the clock seams while the clock's own
// cleanup restores them. This cleanup is registered after installCourierClock's, so it runs
// first.
func startHubRide(t *testing.T, clock *courierClock, settle func(w http.ResponseWriter, call int) bool, nodeTransport http.RoundTripper) (*hubRide, towerhub.Job) {
	t.Helper()
	corePub, corePriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	registry := dispatch.NewWithStore(dispatch.Config{
		Network: link.PublicNetwork, Signer: corePriv, Lifetime: time.Minute, Now: time.Now,
	}, nil)
	nodePub, nodePriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	core := newCoreStub(t)
	core.reply["/tower/dispatch/key"] = func(w http.ResponseWriter, _ int) bool {
		_ = json.NewEncoder(w).Encode(map[string]any{"dispatch_key": hex.EncodeToString(corePub)})
		return true
	}
	core.answerHubNodes(`{"nodes":[{"station_id":"st-1","assertion_key":"` +
		hex.EncodeToString(nodePub) + `","state":"active"}]}`)
	if settle == nil {
		settle = func(w http.ResponseWriter, _ int) bool { _, _ = w.Write([]byte(`{}`)); return true }
	}
	core.reply["/tower/edge/settle"] = settle

	r := &hubRide{core: core, st: servingTower(t), out: &syncBuffer{}, stop: make(chan struct{}),
		submitted: make(chan towerhub.Result, 1)}
	r.wait, err = runHubInBackground(r.st, hubOptions{Addr: "127.0.0.1:0"}, r.out, r.stop)
	require.NoError(t, err)
	t.Cleanup(func() {
		r.stopOnce.Do(func() { close(r.stop) })
		clock.release()
		clock.end()
		r.wait()
	})
	require.Eventually(t, func() bool {
		for _, line := range strings.Split(r.out.String(), "\n") {
			if strings.HasPrefix(line, "hub: listening on ") {
				r.addr = strings.Fields(strings.TrimPrefix(line, "hub: listening on "))[0]
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "the hub never said where it is listening")

	identity, err := r.st.IdentityKey()
	require.NoError(t, err)
	grant, err := registry.MintEdge(dispatch.EdgeTarget{
		TowerID: "tw-1", StationID: "st-1", StationEpoch: 1,
		Model: "m", Modality: "text", RelayName: "st-1.relay.example",
		MaxIn: 1 << 20, MaxOut: 1 << 20,
		AssertionKey: nodePub, ConsumerKey: consumerHubPub(t),
	})
	require.NoError(t, err)
	r.grant = grant.Signed
	base := "http://" + r.addr
	sum := sha256.Sum256(identity.Public().(ed25519.PublicKey))
	r.consumer = &towerhub.Client{BaseURL: base, TowerID: "tw-1", HTTP: &http.Client{Timeout: 30 * time.Second}}
	r.node = &towerhub.Client{BaseURL: base, TowerID: "tw-1", TowerKeyHash: hex.EncodeToString(sum[:]),
		Sign: towerhub.SignWith(nodePriv), HTTP: &http.Client{Timeout: 30 * time.Second, Transport: nodeTransport}}

	// THE SUBMIT WAITS FOR THE HUB TO KNOW THE STATION, by the hub's own answer. The refresher
	// registers st-1 from Core's node list on its own goroutine, and a submit that arrives first
	// is answered 404 (and triggers that very refresh) - so a consumer that gave up there left
	// the node polling an empty queue forever, which a single-CPU run hit a dozen times in two
	// hundred. A 404 is retried; any other answer is final.
	go func() {
		for {
			res, serr := r.consumer.SubmitJob(t.Context(), r.grant, []byte("sealed-request"))
			var he *towerhub.HTTPError
			if errors.As(serr, &he) && he.Status == http.StatusNotFound {
				select {
				case <-t.Context().Done():
					return
				case <-time.After(5 * time.Millisecond):
				}
				continue
			}
			if serr == nil {
				r.submitted <- res
			}
			return
		}
	}()
	var job towerhub.Job
	require.Eventually(t, func() bool {
		j, ok, perr := r.node.PollJob(t.Context(), "st-1")
		if perr != nil || !ok {
			return false
		}
		job = j
		return true
	}, 10*time.Second, 50*time.Millisecond, "the submitted job never reached the node's queue")
	require.Equal(t, []byte("sealed-request"), job.Envelope, "the sealed body must cross the hub intact")
	return r, job
}

// settled decodes the last settle Core received.
func (r *hubRide) settled(t *testing.T) (stationID, attemptID string, receipt []byte) {
	t.Helper()
	var s struct {
		StationID string `json:"station_id"`
		AttemptID string `json:"attempt_id"`
		Receipt   []byte `json:"receipt"`
	}
	require.NoError(t, json.Unmarshal(r.core.body("/tower/edge/settle"), &s))
	return s.StationID, s.AttemptID, s.Receipt
}

// spooledIDs lists the attempt ids on the spool's disk DIRECTLY, so a spool assertion cannot
// pass vacuously. The spool's own loader also deletes every entry whose deadline has passed,
// and the deadlines these tests write come from a clock pinned to one morning in October:
// asserting on what the loader returned at the real time of day was true of an empty directory
// and of a directory the loader had just emptied.
func spooledIDs(t *testing.T, spool *settleSpool) []string {
	t.Helper()
	ents, err := os.ReadDir(spool.dir)
	require.NoError(t, err)
	var ids []string
	for _, e := range ents {
		raw, rerr := os.ReadFile(filepath.Join(spool.dir, e.Name()))
		require.NoError(t, rerr)
		var se spoolEntry
		require.NoError(t, json.Unmarshal(raw, &se))
		ids = append(ids, se.AttemptID)
	}
	sort.Strings(ids)
	return ids
}

// stopAndDrain closes stop, lets the courier reach its quiet window, ends it, and waits for
// every hub goroutine. It is the only way a run under courierClock ends: the quiet window
// never closes on its own.
func (r *hubRide) stopAndDrain(t *testing.T, clock *courierClock) {
	t.Helper()
	r.stopOnce.Do(func() { close(r.stop) })
	awaitClosed(t, clock.inQuiet, "reached its final drain")
	clock.end()
	r.wait()
}

// --- the main path: taken from the queue, held for the ack grace, forwarded -----------------

// THE WHOLE RIDE, on the main path: a consumer's sealed submit lands at the hub, the node polls
// it out over a signed poll and completes with a receipt, the courier takes the receipt and
// HOLDS it for the ack grace (Core has heard nothing yet), and the stop signal arriving inside
// that hold releases the forward - the receipt reaches Core, tower-signed, byte-identical,
// and is unspooled. The grace wait opens only when the test says, so the courier is proved to
// be inside it before stop is closed; nothing here is a race.
func TestACompletedJobsReceiptRidesToCore(t *testing.T) {
	clock := installCourierClock(t)
	r, job := startHubRide(t, clock, nil, nil)

	require.NoError(t, r.node.CompleteResult(t.Context(), "st-1", towerhub.Result{
		AttemptID: job.AttemptID, Envelope: []byte("sealed-answer"), Receipt: []byte("the-receipt"),
	}))
	got := <-r.submitted
	require.Equal(t, []byte("sealed-answer"), got.Envelope, "the consumer's blocked submit returns the sealed answer")

	awaitClosed(t, clock.inGrace, "took the receipt into its ack-grace wait")
	require.Zero(t, r.core.called("/tower/edge/settle"),
		"the forward must wait out the ack grace, or the consumer's acknowledgement can never arrive first")

	// Stopping inside the grace: the receipt matters more than the corroboration.
	r.stopAndDrain(t, clock)
	require.Equal(t, 1, r.core.called("/tower/edge/settle"),
		"the node completed and was never couriered: its pay ended here")
	station, attempt, receipt := r.settled(t)
	require.Equal(t, "st-1", station)
	require.Equal(t, job.AttemptID, attempt)
	require.Equal(t, []byte("the-receipt"), receipt,
		"the receipt must reach Core byte-identical: Core verifies it against the station's key")

	spool, err := newSettleSpool(r.st.Dir())
	require.NoError(t, err)
	require.Empty(t, spooledIDs(t, spool), "a settled receipt stayed in the spool")
	require.Contains(t, r.out.String(), "carried ", "a settled receipt must print the carried-job line")
	require.Contains(t, r.out.String(), "receipt settled")
}

// A FIRST FORWARD THAT FAILS TRANSIENTLY JOINS THE RETRY BACKLOG, and the backlog honours the
// ack grace too: a retry tick inside the grace skips the receipt, one after it forwards. The
// clock is frozen, so "inside" and "after" are the test's to decide.
func TestATransientlyRefusedReceiptRetriesAfterTheAckGrace(t *testing.T) {
	clock := installCourierClock(t)
	r, job := startHubRide(t, clock, func(w http.ResponseWriter, call int) bool {
		if call == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"briefly down"}}`))
			return true
		}
		_, _ = w.Write([]byte(`{}`))
		return true
	}, nil)
	require.NoError(t, r.node.CompleteResult(t.Context(), "st-1", towerhub.Result{
		AttemptID: job.AttemptID, Envelope: []byte("sealed-answer"), Receipt: []byte("the-receipt"),
	}))
	<-r.submitted

	awaitClosed(t, clock.inGrace, "took the receipt into its ack-grace wait")
	clock.release() // the grace elapses; Core answers 503
	// EVERY STEP BELOW IS SEQUENCED BY THE TICK CHANNEL, not by watching for a side effect.
	// The courier is inside the main path's grace wait (proved above), so it receives no tick
	// until that body - the refused forward and the admit into the backlog - has finished. Each
	// pass then judges the backlog by the time on its own tick, and each following send
	// completes only after the previous pass has finished.
	inGrace := clock.Now()
	clock.tick <- inGrace // a pass inside the grace: skipped, not forwarded
	clock.tick <- inGrace // barrier: that pass has finished (and this one skips too)
	require.Equal(t, 1, r.core.called("/tower/edge/settle"), "a retry inside the ack grace must not forward")
	require.Contains(t, r.out.String(), "failed (will retry)")
	pastGrace := inGrace.Add(settleAckGrace + time.Second)
	clock.tick <- pastGrace // a pass after the grace: forwards, Core takes it
	clock.tick <- pastGrace // barrier: the forward, drop and delete have run
	require.Equal(t, 2, r.core.called("/tower/edge/settle"), "the receipt was never retried once the grace had passed")
	_, attempt, receipt := r.settled(t)
	require.Equal(t, job.AttemptID, attempt)
	require.Equal(t, []byte("the-receipt"), receipt)

	r.stopAndDrain(t, clock)
	require.Equal(t, 2, r.core.called("/tower/edge/settle"),
		"a receipt already settled must not ride again at shutdown")
	spool, err := newSettleSpool(r.st.Dir())
	require.NoError(t, err)
	require.Empty(t, spooledIDs(t, spool), "a receipt settled on the retry tick must be unspooled")
}

// --- the retry tick over recovered receipts -------------------------------------------------

// RECOVERED RECEIPTS RIDE THE RETRY TICK: one is settled and dropped, one Core refuses
// transiently stays, and when the clock passes the settle window the one that stayed is
// abandoned by name and unspooled - a receipt Core can no longer accept must not wedge a slot
// in the backlog for good.
//
// EVERY DISK ASSERTION FOLLOWS A BARRIER TICK, never a log line. The courier prints "receipt
// settled" and "ABANDONED" before it removes the spool file, so reading the directory on the
// strength of the log could see the file still there. The tick channel is unbuffered and the
// courier receives from it only in its select, so a second send completes only after the first
// tick's whole loop body - every forward, drop and delete - has run.
//
// THE BARRIER'S OWN BODY MAY STILL BE RUNNING when its send returns, and two things keep that
// harmless. A pass judges every receipt by the time carried on its tick (hub.go), never by a
// clock read made while it runs, so advancing the test clock cannot reach into a pass already
// under way: the first barrier carries the first tick's time and re-forwards the kept receipt,
// whenever it runs. And Core refuses every settle after the first, so that re-forward changes
// nothing on disk; after the abandonment the second barrier finds an empty backlog. The exact
// settle count is asserted once, after wait(), when no courier body can still be running.
func TestTheRetryTickSettlesAbandonsAndUnspoolsByTheClock(t *testing.T) {
	clock := installCourierClock(t)
	core := newCoreStub(t)
	core.answerDispatchKey(t)
	core.answerHubNodes(`{"nodes":[]}`)
	core.reply["/tower/edge/settle"] = func(w http.ResponseWriter, call int) bool {
		if call > 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"briefly down"}}`))
			return true
		}
		_, _ = w.Write([]byte(`{}`))
		return true
	}
	st := servingTower(t)
	spool, err := newSettleSpool(st.Dir())
	require.NoError(t, err)
	for _, id := range []string{"att-one", "att-two"} {
		require.NoError(t, spool.put(pendingSettle{
			stationID: "st-1", attemptID: id, receipt: []byte("r-" + id),
			deadline: clock.Now().Add(settleRetryWindow),
		}))
	}

	out := &syncBuffer{}
	stop := make(chan struct{})
	wait, err := runHubInBackground(st, hubOptions{Addr: "127.0.0.1:0"}, out, stop)
	require.NoError(t, err)
	var stopOnce sync.Once
	t.Cleanup(func() { // pass or fail, the courier is gone before the clock seams are restored
		stopOnce.Do(func() { close(stop) })
		clock.end()
		wait()
	})

	require.Equal(t, []string{"att-one", "att-two"}, spooledIDs(t, spool), "both receipts start on disk")
	// The first tick forwards both; Core takes one and refuses the other. Which is which is
	// the backlog's iteration order, and the assertions do not depend on it.
	clock.tick <- clock.Now()
	clock.tick <- clock.Now() // barrier: the first tick's loop body has finished
	require.GreaterOrEqual(t, core.called("/tower/edge/settle"), 2, "the first tick forwards both receipts")
	s := out.String()
	require.Contains(t, s, "receipt settled")
	require.Contains(t, s, "failed (will retry)")
	kept := spooledIDs(t, spool)
	require.Len(t, kept, 1, "the settled receipt is unspooled; the refused one is kept")

	// The settle window closes before Core comes back: the kept receipt is abandoned by name.
	late := clock.Now().Add(settleRetryWindow + time.Minute)
	clock.tick <- late
	clock.tick <- late // barrier: the abandonment, drop and delete have run
	require.Contains(t, out.String(), "hub: settle for "+kept[0]+" ABANDONED - the settle window closed before Core answered")
	require.Empty(t, spooledIDs(t, spool), "an abandoned receipt must leave the spool, or every restart re-presents it")

	stopOnce.Do(func() { close(stop) })
	awaitClosed(t, clock.inQuiet, "reached its final drain")
	clock.end()
	wait()
	// Three settles in all: both receipts on the first tick and the kept one on the first
	// barrier. A fourth would mean the abandoned receipt was forwarded on the abandoning tick,
	// or something was left for the shutdown drain.
	require.Equal(t, 3, core.called("/tower/edge/settle"),
		"an abandoned receipt is not forwarded, and nothing was left for the shutdown drain")
}

// --- the final drain: a receipt from a handler that was mid-body when stop came -------------

// gatedBodyTransport hands the real transport all of a /complete body but its last byte, and
// holds that byte until released. The hub's handler is then live and mid-read when the test
// closes stop: Shutdown must wait for it, and the receipt it finally produces reaches the
// courier only after Shutdown has finished - which is the final drain's reason to exist.
//
// THE REQUEST CARRIES Expect: 100-continue, AND THAT IS WHAT MAKES "LIVE" A FACT RATHER THAN A
// LIKELIHOOD. Without it, "the transport has buffered all but the last byte" said nothing about
// the server: the connection could still be sitting in the accept backlog, and closing the
// listener then simply dropped it ("use of closed network connection", seen once in ten runs).
// With it, the transport sends no body until the hub's handler has started reading one and
// answered 100 Continue - so the held byte proves the handler is past the credential door and
// inside its body read, which is exactly the state Shutdown is obliged to wait for.
type gatedBodyTransport struct {
	inner   http.RoundTripper
	release <-chan struct{}
	// held is closed once the transport has sent everything but the last byte.
	held     chan struct{}
	heldOnce sync.Once
}

type gatedReader struct {
	rest    io.Reader
	release <-chan struct{}
	onHold  func()
	done    bool
}

func (g *gatedReader) Read(p []byte) (int, error) {
	if g.done {
		return 0, io.EOF
	}
	g.onHold()
	<-g.release
	g.done = true
	return g.rest.Read(p)
}

func (g *gatedBodyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path != towerhub.PathComplete {
		return g.inner.RoundTrip(req)
	}
	all, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	_ = req.Body.Close()
	last := &gatedReader{rest: bytes.NewReader(all[len(all)-1:]), release: g.release,
		onHold: func() { g.heldOnce.Do(func() { close(g.held) }) }}
	req = req.Clone(req.Context())
	req.Header.Set("Expect", "100-continue")
	req.Body = io.NopCloser(io.MultiReader(bytes.NewReader(all[:len(all)-1]), last))
	return g.inner.RoundTrip(req)
}

// continueTransport is the real transport with the 100-continue wait made unconditional for
// the length of a test: the default gives up on the server after one second and sends the body
// anyway, which would quietly turn the proof above back into a likelihood.
func continueTransport() http.RoundTripper {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ExpectContinueTimeout = 30 * time.Second
	return tr
}

// A RECEIPT FROM A HANDLER THAT WAS MID-BODY WHEN STOP CAME IS NOT LOST. The courier took the
// stop signal with an empty queue and is waiting for Shutdown to finish draining handlers; the
// completion lands after that, in the quiet window, and the final drain forwards it.
//
// One ordering here is the runtime's rather than the test's: the courier, blocked in its select
// on an empty queue, is made runnable the instant stop closes and takes it; the receipt cannot
// be enqueued until the test has afterwards watched the listener close, released the last byte,
// and the hub has verified and completed the request. Nothing short of a seam between the
// courier's two blocking points could turn that into a proof, and it is not worth one.
func TestAReceiptCompletedDuringShutdownRidesTheFinalDrain(t *testing.T) {
	clock := installCourierClock(t)
	release := make(chan struct{})
	gate := &gatedBodyTransport{inner: continueTransport(), release: release, held: make(chan struct{})}
	r, job := startHubRide(t, clock, nil, gate)

	completed := make(chan error, 1)
	go func() {
		completed <- r.node.CompleteResult(t.Context(), "st-1", towerhub.Result{
			AttemptID: job.AttemptID, Envelope: bytes.Repeat([]byte("a"), 64<<10),
			Receipt: []byte("the-receipt"),
		})
	}()
	awaitClosed(t, gate.held, "received everything but the last byte of the completion")
	// The handler is mid-read. Stop the hub: the courier takes stop (nothing is queued) and
	// waits on Shutdown; Shutdown closes the listener and waits on the live handler.
	r.stopOnce.Do(func() { close(r.stop) })
	require.Eventually(t, func() bool {
		c, derr := net.DialTimeout("tcp", r.addr, 200*time.Millisecond)
		if derr == nil {
			_ = c.Close()
		}
		return derr != nil
	}, 10*time.Second, 5*time.Millisecond, "the listener never closed after stop")

	// Now the body completes: the handler finishes, Shutdown returns, the courier enters its
	// final drain, and the receipt arrives there.
	close(release)
	require.NoError(t, <-completed, "a completion the hub was still reading must be accepted")
	got := <-r.submitted
	require.Len(t, got.Envelope, 64<<10)
	awaitClosed(t, clock.inQuiet, "reached its final drain")
	require.Eventually(t, func() bool { return r.core.called("/tower/edge/settle") == 1 },
		10*time.Second, 5*time.Millisecond, "the receipt completed during shutdown was never couriered")
	clock.end()
	r.wait()

	_, attempt, receipt := r.settled(t)
	require.Equal(t, job.AttemptID, attempt)
	require.Equal(t, []byte("the-receipt"), receipt)
	spool, err := newSettleSpool(r.st.Dir())
	require.NoError(t, err)
	require.Empty(t, spooledIDs(t, spool), "a receipt settled in the final drain is unspooled")
	require.Contains(t, r.out.String(), "receipt settled")
}

// consumerHubPub is a throwaway consumer key for the grants minted here.
func consumerHubPub(t *testing.T) ed25519.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return pub
}

// EVERY WAIT THE CLOCK HANDS OUT IS RELEASED AT CLEANUP. A run that never reaches its grace or
// its quiet window used to leave one goroutine parked on each for the life of the process. The
// proof is the waits themselves firing after the subtest's cleanup ran: a channel the After
// goroutine was still parked behind could not deliver.
func TestCourierClockReleasesEveryWaitAtCleanup(t *testing.T) {
	var grace, quiet <-chan time.Time
	t.Run("a run that reaches neither wait's release", func(t *testing.T) {
		c := installCourierClock(t)
		grace = c.After(settleAckGrace)
		quiet = c.After(settleQuietWindow)
	})
	for name, ch := range map[string]<-chan time.Time{"ack grace": grace, "quiet window": quiet} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("the %s wait never fired after cleanup: its goroutine is parked for good", name)
		}
	}
}
