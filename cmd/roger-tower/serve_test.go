package main

// serve_test.go drives the relay link loop against a stub Roger Core.
//
// The loop is the one piece of this command that can misbehave silently: a Tower that stops
// heartbeating, or leaves without draining, does not fail - it just quietly stops being
// offered work, or keeps being offered work it can no longer do. Neither shows up anywhere
// except here.
//
// The signal and the clock are injected (see runLink) so these tests neither sleep nor send
// the test process a signal. Both would be races dressed up as tests.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/tower"
	"rogerai.fm/roger/v5/internal/towercore/link"
	"rogerai.fm/roger/v5/internal/towerjoin"
)

// coreStub is Roger Core's link surface, as much of it as the loop touches.
//
// Everything it records is behind a mutex, and the loop's output goes to a locked buffer,
// because the loop runs in its own goroutine while the test watches it. Unsynchronised these
// tests are data races - which is exactly what the race detector reported the first time
// they ran, and why the scaffolding looks heavier than the assertions need.
type coreStub struct {
	t   *testing.T
	srv *httptest.Server

	mu   sync.Mutex
	seen []string
	// keys records the public key that signed each path, so a test can assert that the
	// operator's account key and the Tower's identity key are not the same key.
	keys map[string]string
	// bodies is the LAST request body seen on each path. It exists because the Hello is the
	// only place the tower states its own data plane - the endpoint and, since TLS, the hub
	// certificate pin - and a test that cannot read it cannot tell an advertisement that was
	// made from one that was silently dropped.
	bodies      map[string][]byte
	sessions    int
	inventories int
	// reply overrides the canned answer for a path; the func returns true when it handled it.
	reply map[string]func(w http.ResponseWriter, call int) bool
}

func newCoreStub(t *testing.T) *coreStub {
	t.Helper()
	c := &coreStub{t: t, reply: map[string]func(http.ResponseWriter, int) bool{},
		keys: map[string]string{}, bodies: map[string][]byte{}}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Every link call is the MACHINE talking, and Core authenticates it by the Tower's
		// key. An unsigned call would be refused in production; catching it here is the only
		// place the client's side of that rule is checked from this package.
		//
		// /tower/dispatch/key is the one exception, and it is one in production too: it is a
		// public read of a public key, fetched before the tower has any session to sign with.
		if r.URL.Path != "/tower/dispatch/key" {
			require.NotEmpty(c.t, r.Header.Get("X-Roger-Pubkey"), "%s was unsigned", r.URL.Path)
		}

		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		c.mu.Lock()
		c.seen = append(c.seen, r.URL.Path)
		c.keys[r.URL.Path] = r.Header.Get("X-Roger-Pubkey")
		c.bodies[r.URL.Path] = raw
		n := 0
		for _, p := range c.seen {
			if p == r.URL.Path {
				n++
			}
		}
		switch r.URL.Path {
		case "/tower/session":
			c.sessions++
		case "/tower/inventory":
			c.inventories++
		}
		fn, override := c.reply[r.URL.Path]
		c.mu.Unlock()

		if override && fn(w, n) {
			return
		}
		switch r.URL.Path {
		case "/tower/session":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"session_id": "sess-1", "heartbeat_seconds": 30, "freshness_seconds": 180,
			})
		case "/tower/inventory":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"revision": 1, "hash": "h1", "routable": 0,
			})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(c.srv.Close)
	c.t.Setenv("ROGER_BROKER", c.srv.URL)
	return c
}

// body is the last request body seen on a path.
func (c *coreStub) body(path string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bodies[path]
}

// answerDispatchKey makes the stub serve a real (throwaway) grant-signing key, which is what
// runHubInBackground fetches before it will start a hub at all.
func (c *coreStub) answerDispatchKey(t *testing.T) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reply["/tower/dispatch/key"] = func(w http.ResponseWriter, _ int) bool {
		_ = json.NewEncoder(w).Encode(map[string]any{"dispatch_key": hex.EncodeToString(pub)})
		return true
	}
}

// answerHubNodes makes the stub serve a fixed /tower/hub/nodes body.
func (c *coreStub) answerHubNodes(body string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reply["/tower/hub/nodes"] = func(w http.ResponseWriter, _ int) bool {
		_, _ = io.WriteString(w, body)
		return true
	}
}

func (c *coreStub) called(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, p := range c.seen {
		if p == path {
			n++
		}
	}
	return n
}

func (c *coreStub) sessionCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessions
}

func (c *coreStub) reached() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.seen)
}

// syncBuffer is where the loop writes while a test is reading.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// servingTower is a joined Tower that has already registered.
func servingTower(t *testing.T) *tower.State {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "tw")
	var b bytes.Buffer
	require.NoError(t, run([]string{"init", "--dir", dir, "--mode", "joined"}, &b))

	// Written directly rather than by registering, because registration is a different
	// subsystem with its own tests; what this file is about is what happens AFTER it.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "admission.json"),
		[]byte(`{"tower_id":"tw-1"}`), 0o600))

	st, release, err := openDir(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = release() })
	return st
}

// manualTicker fires HEARTBEATS only when a test says so, and never fires the inventory
// refresh. The loop asks for two tickers; handing back one channel for both meant a
// heartbeat tick could be taken by the refresh branch instead, so the tests that care about
// heartbeats became a coin flip the moment the refresh existed.
func manualTicker(ch <-chan time.Time) func(time.Duration) (<-chan time.Time, func()) {
	return tickerFor(ch, nil)
}

// tickerFor hands out a DIFFERENT channel per interval, so a test can fire the heartbeat and
// the inventory refresh independently. The loop asks for two tickers and they must not be
// the same one: a test that could only fire both together could not tell which of them did
// the work.
func tickerFor(beats, refresh <-chan time.Time) func(time.Duration) (<-chan time.Time, func()) {
	return func(d time.Duration) (<-chan time.Time, func()) {
		if d == inventoryRefresh {
			return refresh, func() {}
		}
		return beats, func() {}
	}
}

// closedStop is a stop signal that has already fired.
//
// The tests that use it expect runLink to fail BEFORE it reaches the select loop, so the
// signal is never actually read. It is passed anyway so that a regression which stops
// failing there ends the test with a clean assertion instead of blocking forever - a hung
// suite is a much worse way to learn about a bug than a red one. (Found by mutation-testing
// these tests: dropping the chain restart hung the run rather than failing it.)
func closedStop() <-chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

// A standalone Tower must not be talked into joining by running `serve` at it. Standalone is
// a private network with its own trust root; silently opening a link to RogerAI would be the
// single worst thing this command could do.
func TestServingAStandaloneTowerRefusesAndExplainsWhy(t *testing.T) {
	core := newCoreStub(t)
	dir := filepath.Join(t.TempDir(), "tw")
	var b bytes.Buffer
	require.NoError(t, run([]string{"init", "--dir", dir, "--mode", "standalone"}, &b))
	st, release, err := openDir(dir)
	require.NoError(t, err)
	defer release()

	err = runLink(st, &b, closedStop(), manualTicker(nil), link.RelayPlane{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "standalone")
	require.Contains(t, err.Error(), "--mode joined", "it says how to get a Tower that can")
	require.Zero(t, core.reached(), "and it reaches nothing while refusing")
}

// The ordinary life of the link: open, push, heartbeat, and drain on the way out. The drain
// is the part worth asserting hardest - leaving without it means Core keeps offering this
// Tower for a full freshness window after it has gone.
func TestTheLinkOpensPushesHeartbeatsAndDrains(t *testing.T) {
	core := newCoreStub(t)
	st := servingTower(t)
	beats := make(chan time.Time, 1)
	stop := make(chan struct{})

	b := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- runLink(st, b, stop, manualTicker(beats), link.RelayPlane{}) }()

	beats <- time.Now()
	require.Eventually(t, func() bool { return core.called("/tower/session/heartbeat") > 0 },
		2*time.Second, 5*time.Millisecond)

	close(stop)
	require.NoError(t, <-done)

	require.Equal(t, 1, core.called("/tower/session"))
	require.Equal(t, 1, core.called("/tower/inventory"))
	require.Equal(t, 1, core.called("/tower/session/close"), "it drained")

	out := b.String()
	require.Contains(t, out, "linked to RogerAI as tw-1")
	require.Contains(t, out, "no Stations attached yet", "honest about carrying nothing")
	require.Contains(t, out, "drained")
}

// A heartbeat that cannot reach Core is TRANSPORT, and the freshness window is several
// heartbeats wide. Tearing the session down over one lost frame would turn a blip into an
// outage, so the loop says so and keeps going.
func TestALostHeartbeatDoesNotTearTheSessionDown(t *testing.T) {
	core := newCoreStub(t)
	st := servingTower(t)
	core.reply["/tower/session/heartbeat"] = func(w http.ResponseWriter, call int) bool {
		if call == 1 {
			// Hijack and drop: the client sees a transport failure, not an HTTP status.
			conn, _, err := w.(http.Hijacker).Hijack()
			require.NoError(t, err)
			_ = conn.Close()
			return true
		}
		return false
	}

	beats := make(chan time.Time, 2)
	stop := make(chan struct{})
	b := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- runLink(st, b, stop, manualTicker(beats), link.RelayPlane{}) }()

	beats <- time.Now()
	require.Eventually(t, func() bool { return strings.Contains(b.String(), "will retry") },
		2*time.Second, 5*time.Millisecond)
	beats <- time.Now()
	require.Eventually(t, func() bool { return core.called("/tower/session/heartbeat") >= 2 },
		2*time.Second, 5*time.Millisecond)

	close(stop)
	require.NoError(t, <-done)
	require.Equal(t, 1, core.sessionCount(), "one lost frame did not cost us the session")
}

// A REFUSED heartbeat means the session is gone - Core restarted, or our lease lapsed. The
// loop re-opens rather than heartbeating into nothing, and pushes again when the new session
// says Core has nothing of ours.
func TestARefusedHeartbeatReopensTheSessionAndRepushes(t *testing.T) {
	core := newCoreStub(t)
	st := servingTower(t)
	core.reply["/tower/session/heartbeat"] = func(w http.ResponseWriter, call int) bool {
		if call == 1 {
			w.WriteHeader(http.StatusForbidden)
			return true
		}
		return false
	}
	core.reply["/tower/session"] = func(w http.ResponseWriter, call int) bool {
		if call == 2 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"session_id": "sess-2", "heartbeat_seconds": 30,
				"need_full_inventory": true,
			})
			return true
		}
		return false
	}

	beats := make(chan time.Time, 1)
	stop := make(chan struct{})
	b := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- runLink(st, b, stop, manualTicker(beats), link.RelayPlane{}) }()

	beats <- time.Now()
	require.Eventually(t, func() bool { return core.called("/tower/inventory") >= 2 },
		2*time.Second, 5*time.Millisecond)

	close(stop)
	require.NoError(t, <-done)
	require.Equal(t, 2, core.called("/tower/session"), "it re-opened")
	require.Contains(t, b.String(), "re-opening")
}

// If the re-open itself fails there is nothing left to hold, so the loop stops - but it must
// still drain, because the FIRST session may well have registered inventory Core is still
// offering.
func TestAFailedReopenStopsAndStillDrains(t *testing.T) {
	core := newCoreStub(t)
	st := servingTower(t)
	core.reply["/tower/session/heartbeat"] = func(w http.ResponseWriter, call int) bool {
		w.WriteHeader(http.StatusForbidden)
		return true
	}
	core.reply["/tower/session"] = func(w http.ResponseWriter, call int) bool {
		if call >= 2 {
			w.WriteHeader(http.StatusForbidden)
			return true
		}
		return false
	}

	// A nil stop channel, not closedStop(), because this test must reach the select loop: a
	// fired stop signal would race the beat and the loop could take either branch.
	beats := make(chan time.Time, 1)
	var b bytes.Buffer
	beats <- time.Now()
	err := runLink(st, &b, nil, manualTicker(beats), link.RelayPlane{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "may not hold a link")
	require.Equal(t, 1, core.called("/tower/session/close"), "it drained on the way out")
}

// A Tower that cannot open a session at all reports THAT, and does not go on to push an
// inventory into a session it does not have.
func TestAnUnopenableSessionFailsBeforePushing(t *testing.T) {
	core := newCoreStub(t)
	st := servingTower(t)
	core.reply["/tower/session"] = func(w http.ResponseWriter, call int) bool {
		w.WriteHeader(http.StatusForbidden)
		return true
	}
	var b bytes.Buffer
	err := runLink(st, &b, closedStop(), manualTicker(nil), link.RelayPlane{})
	require.Error(t, err)
	require.Zero(t, core.called("/tower/inventory"))
	require.Zero(t, core.called("/tower/session/close"), "nothing to drain")
}

// A push that Core refuses outright stops the loop: retrying an inventory Core will never
// verify is a Tower spinning forever while an operator sees nothing wrong.
func TestARefusedInventoryStopsTheLoop(t *testing.T) {
	core := newCoreStub(t)
	st := servingTower(t)
	core.reply["/tower/inventory"] = func(w http.ResponseWriter, call int) bool {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"this revision will never verify"}}`))
		return true
	}
	var b bytes.Buffer
	err := runLink(st, &b, closedStop(), manualTicker(nil), link.RelayPlane{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "never verify")
}

// Core asking for a full inventory when we already sent one means our chain position is not
// Core's. We restart at revision 1 rather than guessing - and if that is refused too, we
// stop, instead of looping between the two forever.
func TestBeingAskedForAFullInventoryRestartsTheChain(t *testing.T) {
	core := newCoreStub(t)
	st := servingTower(t)
	core.reply["/tower/inventory"] = func(w http.ResponseWriter, call int) bool {
		if call == 1 {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"need_full_inventory":true,"error":"revision 1 is not next"}`))
			return true
		}
		return false
	}

	stop := make(chan struct{})
	close(stop)
	var b bytes.Buffer
	require.NoError(t, runLink(st, &b, stop, manualTicker(nil), link.RelayPlane{}))
	require.Equal(t, 2, core.called("/tower/inventory"), "it resent from genesis")
	require.Contains(t, b.String(), "revision 1 accepted")
}

func TestARefusedRestartAfterANeedFullIsReported(t *testing.T) {
	core := newCoreStub(t)
	st := servingTower(t)
	core.reply["/tower/inventory"] = func(w http.ResponseWriter, call int) bool {
		if call == 1 {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"need_full_inventory":true}`))
			return true
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"still no"}}`))
		return true
	}
	var b bytes.Buffer
	err := runLink(st, &b, closedStop(), manualTicker(nil), link.RelayPlane{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "still no")
}

// A drain that fails must not be silent. The operator needs to know Core may still be
// offering this Tower, because that is the difference between a clean stop and three minutes
// of work routed into a hole.
func TestAFailedDrainWarnsRatherThanPassingQuietly(t *testing.T) {
	core := newCoreStub(t)
	st := servingTower(t)
	core.reply["/tower/session/close"] = func(w http.ResponseWriter, call int) bool {
		w.WriteHeader(http.StatusInternalServerError)
		return true
	}
	stop := make(chan struct{})
	close(stop)
	var b bytes.Buffer
	require.NoError(t, runLink(st, &b, stop, manualTicker(nil), link.RelayPlane{}))
	require.Contains(t, b.String(), "could not drain cleanly")
}

// A Tower with no offers directory relays nothing - the ordinary state of one whose Stations
// have not been set up yet. It is a valid state, not a failure.
func TestATowerWithNoOffersRelaysNothing(t *testing.T) {
	st := servingTower(t)
	var b bytes.Buffer
	leaves, err := localOffers(st, &b)
	require.NoError(t, err)
	require.Empty(t, leaves)
	require.Empty(t, b.String())
}

// STATION OFFERS ARE RELAYED BYTE FOR BYTE. A Tower that re-encoded one could change what
// the Station signed, so the bytes that arrive are the bytes that leave.
func TestStationSignedOffersAreRelayedVerbatim(t *testing.T) {
	st := servingTower(t)
	dir := filepath.Join(st.Dir(), offersDir)
	require.NoError(t, os.MkdirAll(dir, 0o700))

	// Deliberately ugly: odd spacing and member order survive if nothing re-encodes them.
	signed := `{ "offer_id":"off-1",   "station_id":"st-1",
 "station_sig":"abc" }`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.json"), []byte(signed), 0o600))

	var b bytes.Buffer
	leaves, err := localOffers(st, &b)
	require.NoError(t, err)
	require.Len(t, leaves, 1)
	require.Equal(t, signed, string(leaves[0]), "the relay altered what the Station signed")
}

// One unreadable file must not take a whole fleet off the network - and must not vanish
// quietly either, because a silent skip is how an operator ends up staring at a Station that
// never appears.
func TestABadOfferFileIsNamedAndSkippedRatherThanFatal(t *testing.T) {
	st := servingTower(t)
	dir := filepath.Join(st.Dir(), offersDir)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "good.json"), []byte(`{"offer_id":"o1"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{not json"), 0o600))
	// Not an offer at all, and not announced: a stray file is not an operator mistake.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "subdir.json"), 0o700))

	var b bytes.Buffer
	leaves, err := localOffers(st, &b)
	require.NoError(t, err)
	require.Len(t, leaves, 1, "the good offer still goes")
	require.Contains(t, b.String(), "bad.json")
	require.NotContains(t, b.String(), "notes.txt")
}

// The real clock is wired up in serveJoined and nothing else asserts it fires.
func TestTheRealTickerTicks(t *testing.T) {
	c, stop := realTicker(time.Millisecond)
	defer stop()
	select {
	case <-c:
	case <-time.After(2 * time.Second):
		t.Fatal("the production ticker never fired")
	}
}

// `serve` without a data directory fails on the directory, and one that does not exist fails
// on that - neither reaches the network.
func TestServeNeedsAUsableDataDirectory(t *testing.T) {
	core := newCoreStub(t)
	var b bytes.Buffer
	require.Error(t, cmdServe(nil, &b))
	require.Error(t, cmdServe([]string{"--dir", filepath.Join(t.TempDir(), "nope")}, &b))
	require.Error(t, cmdServe([]string{"--wat"}, &b))
	require.Zero(t, core.reached())
}

// serveJoined is the production wiring: real signal, real clock. Exercised through a
// standalone Tower, which refuses before either matters - enough to prove the wiring
// compiles and returns rather than hanging.
func TestServeJoinedWiresTheRealSignalAndClock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tw")
	var b bytes.Buffer
	require.NoError(t, run([]string{"init", "--dir", dir, "--mode", "standalone"}, &b))
	st, release, err := openDir(dir)
	require.NoError(t, err)
	defer release()
	require.Error(t, serveJoined(st, &b, "", hubOptions{}))
}

// A standalone Tower asked to serve a HUB reaches nothing, and is refused before it tries.
//
// The refusal already existed, in runLink - which runs after the hub is started, and the hub's
// first act is to fetch Core's grant key and this Tower's node list. So the one flag combination
// that could break standalone's "no RogerAI DNS lookup or network connection" guarantee
// (features/tower/modes.feature) was the one nobody had exercised. Signed hub polls make that
// worse rather than better: the assertion keys the hub verifies polls against ride the same
// fetch, so the hub depends on Core for MORE than it used to.
func TestAStandaloneTowerWithAHubReachesNothing(t *testing.T) {
	core := newCoreStub(t)
	dir := filepath.Join(t.TempDir(), "tw")
	var b bytes.Buffer
	require.NoError(t, run([]string{"init", "--dir", dir, "--mode", "standalone"}, &b))
	st, release, err := openDir(dir)
	require.NoError(t, err)
	defer release()

	err = serveJoined(st, &b, "203.0.113.9:8444", hubOptions{Addr: "127.0.0.1:0", AllowLegacyBearer: true})
	require.ErrorIs(t, err, errStandaloneCannotServeJoined)
	require.Zero(t, core.reached(), "a standalone Tower called Roger Core before being refused")
}

// An offers directory that cannot be listed is FATAL, unlike a single bad file inside it.
// The difference matters: one unreadable offer is a fleet minus one, but an unreadable
// directory means we have no idea what this Tower is meant to be relaying, and pushing an
// empty inventory in that state would silently take the whole fleet off the network.
func TestAnUnreadableOffersDirectoryStopsThePush(t *testing.T) {
	st := servingTower(t)
	// A regular file where the directory should be: ReadDir fails with ENOTDIR.
	require.NoError(t, os.WriteFile(filepath.Join(st.Dir(), offersDir), []byte("x"), 0o600))

	var b bytes.Buffer
	_, err := localOffers(st, &b)
	require.Error(t, err)
	require.Contains(t, err.Error(), "offers directory")
}

// A single offer file that cannot be READ is named and skipped, the same as one that is not
// JSON - the operator needs to know which file, and the rest of the fleet still goes.
func TestAnUnreadableOfferFileIsNamedAndSkipped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits this test relies on")
	}
	st := servingTower(t)
	dir := filepath.Join(st.Dir(), offersDir)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "good.json"), []byte(`{"offer_id":"o1"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "locked.json"), []byte(`{}`), 0o000))

	var b bytes.Buffer
	leaves, err := localOffers(st, &b)
	require.NoError(t, err)
	require.Len(t, leaves, 1)
	require.Contains(t, b.String(), "locked.json")
}

// And the whole point, end to end through the push: a Station-signed offer reaches Core.
func TestAPushCarriesTheStationsOffers(t *testing.T) {
	core := newCoreStub(t)
	st := servingTower(t)
	dir := filepath.Join(st.Dir(), offersDir)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.json"),
		[]byte(`{"offer_id":"off-1","station_id":"st-1","station_sig":"abc"}`), 0o600))
	core.reply["/tower/inventory"] = func(w http.ResponseWriter, call int) bool {
		_ = json.NewEncoder(w).Encode(map[string]any{"revision": 1, "hash": "h1", "routable": 1})
		return true
	}

	var b bytes.Buffer
	_, _, err := pushInventory(st, &b, 0, towerjoin.Head{})
	require.NoError(t, err)
	require.Contains(t, b.String(), "1 of 1 Station offer(s) eligible")
}

// THE TOWER GOES DARK AFTER THIRTY MINUTES, and nothing says so.
//
// A pushed revision carries an expiry, and inv.Routable returns NOTHING once it passes. The
// loop pushed once at session open and then only heartbeated, so a Tower left running
// overnight stopped being routable half an hour in while reporting a perfectly healthy live
// link: the heartbeats kept succeeding, the operator's status kept saying "link live", and
// no request could ever reach it again.
//
// It is the worst shape of bug this command can have - it passes every test that runs for
// less than the lifetime, and fails silently in production only.
func TestTheInventoryIsRepushedBeforeItExpires(t *testing.T) {
	core := newCoreStub(t)
	st := servingTower(t)

	beats := make(chan time.Time, 1)
	refresh := make(chan time.Time, 1)
	stop := make(chan struct{})
	b := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- runLink(st, b, stop, tickerFor(beats, refresh), link.RelayPlane{}) }()

	require.Eventually(t, func() bool { return core.called("/tower/inventory") == 1 },
		2*time.Second, 5*time.Millisecond, "the first push")

	refresh <- time.Now()
	require.Eventually(t, func() bool { return core.called("/tower/inventory") >= 2 },
		2*time.Second, 5*time.Millisecond, "the inventory must be refreshed before it expires")

	close(stop)
	require.NoError(t, <-done)
	require.Equal(t, 1, core.called("/tower/session"), "refreshing is not reconnecting")
}

// The refresh interval must be derived from the lifetime, not guessed alongside it. A
// hardcoded interval that drifts when the lifetime changes is precisely how this bug comes
// back, and it comes back silently.
func TestTheRefreshIntervalLeavesRoomForARetry(t *testing.T) {
	require.Less(t, inventoryRefresh, towerjoin.InventoryLifetime,
		"a refresh at or after the expiry is not a refresh")
	require.LessOrEqual(t, inventoryRefresh*2, towerjoin.InventoryLifetime,
		"one lost refresh must not be enough to go dark")
}

// A refresh that Core refuses is reported and does NOT tear the link down: the current
// inventory is still good until it expires, so there is time to retry.
func TestAFailedRefreshIsReportedAndTheLinkSurvives(t *testing.T) {
	core := newCoreStub(t)
	st := servingTower(t)
	core.reply["/tower/inventory"] = func(w http.ResponseWriter, call int) bool {
		if call >= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"try later"}}`))
			return true
		}
		return false
	}

	beats := make(chan time.Time, 1)
	refresh := make(chan time.Time, 1)
	stop := make(chan struct{})
	b := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- runLink(st, b, stop, tickerFor(beats, refresh), link.RelayPlane{}) }()

	require.Eventually(t, func() bool { return core.called("/tower/inventory") == 1 },
		2*time.Second, 5*time.Millisecond)
	refresh <- time.Now()
	require.Eventually(t, func() bool { return strings.Contains(b.String(), "could not refresh") },
		2*time.Second, 5*time.Millisecond)

	// Still alive: a heartbeat still goes, so one bad refresh has not cost the session.
	beats <- time.Now()
	require.Eventually(t, func() bool { return core.called("/tower/session/heartbeat") > 0 },
		2*time.Second, 5*time.Millisecond)

	close(stop)
	require.NoError(t, <-done)
}

// THE SWITCH IS PARSED FROM BOTH DOORS, AND THE FLAG WINS. That is all this test proves, and
// saying so is a correction.
//
// It used to claim more. Its failure messages read "-hub-legacy-bearer=false did not reach the
// hub", and it asserted require.ErrorIs(err, errStandaloneCannotServeJoined) on every leg -
// which serveJoined returns BEFORE any hub is constructed, deliberately (see
// TestAStandaloneTowerWithAHubReachesNothing). So the only thing it could observe was the banner
// cmdServe prints from its own local variable. Hardcoding AllowLegacyBearer: true in hub.go -
// dropping the operator's switch on the floor entirely - left this whole package green.
//
// The reaches-the-hub half is TestTheBearerToleranceSwitchReachesTheHub, below, which builds a
// real hub through the real function and asks it.
func TestTheLegacyBearerToleranceIsParsedFromFlagAndConfig(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tw")
	var b bytes.Buffer
	require.NoError(t, run([]string{"init", "--dir", dir, "--mode", "standalone"}, &b))

	// The default is ON: an already-released node keeps earning while its operator updates.
	out, err := runCLI(t, "serve", "--dir", dir, "--hub", "127.0.0.1:0")
	require.ErrorIs(t, err, errStandaloneCannotServeJoined)
	require.NotContains(t, out, "REFUSED", "the transition tolerance is on unless an operator says otherwise")

	// The flag turns it off, and says so - an operator who does this is choosing to stop
	// serving some of their own fleet, and should see that they did.
	out, err = runCLI(t, "serve", "--dir", dir, "--hub", "127.0.0.1:0", "--hub-legacy-bearer=false")
	require.ErrorIs(t, err, errStandaloneCannotServeJoined)
	require.Contains(t, out, "bearer tokens are REFUSED",
		"-hub-legacy-bearer=false was not parsed")

	// And so does the config file, for an operator who does not hand-write the command line.
	cfg := writeConfig(t, standaloneYAML+"hub:\n  address: 127.0.0.1:0\n  allowLegacyBearer: false\n")
	out, err = runCLI(t, "serve", "--dir", dir, "--config", cfg)
	require.ErrorIs(t, err, errStandaloneCannotServeJoined)
	require.Contains(t, out, "bearer tokens are REFUSED", "hub.allowLegacyBearer was not parsed")

	// The flag is the more deliberate of the two and wins, which is the rule the rest of
	// cmdServe follows for every other hub setting.
	cfgOff := writeConfig(t, standaloneYAML+"hub:\n  address: 127.0.0.1:0\n  allowLegacyBearer: false\n")
	out, err = runCLI(t, "serve", "--dir", dir, "--config", cfgOff, "--hub-legacy-bearer=true")
	require.ErrorIs(t, err, errStandaloneCannotServeJoined)
	require.NotContains(t, out, "REFUSED", "the config file overrode an explicit flag")
}

// AND THE SWITCH REACHES THE HUB, which is the half nothing checked.
//
// This runs the real runHubInBackground - the only production path from hubOptions to
// towerhub.ServerOptions - against a stub Core, and then asks the listening hub the only
// question that matters: does a pre-signature bearer open a queue here. Both postures, because
// a test of an off switch that never sees it on cannot tell "off works" from "nothing works".
//
// Hardcoding AllowLegacyBearer: true at the NewServer call in hub.go fails the second leg.
func TestTheBearerToleranceSwitchReachesTheHub(t *testing.T) {
	for _, tc := range []struct {
		name  string
		allow bool
		want  int
	}{
		{"tolerated by default", true, http.StatusOK},
		{"turned off by the operator", false, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core := newCoreStub(t)
			core.answerDispatchKey(t)
			core.answerHubNodes(`{"nodes":[{"station_id":"st-old","assertion_key":"","hub_token":"tok-old","state":"active"}]}`)
			st := servingTower(t)

			addr := freeAddr(t)
			var out syncBuffer
			stop := make(chan struct{})
			wait, err := runHubInBackground(st, hubOptions{Addr: addr, AllowLegacyBearer: tc.allow}, &out, stop)
			require.NoError(t, err)
			t.Cleanup(func() { close(stop); wait() })

			// Poll until the listener is up and Core's answer has landed, then assert on the
			// last answer rather than inside the retry - a require.Eventually message is built
			// from arguments Go evaluates BEFORE the wait, so it can only ever report the state
			// the test started in.
			var status int
			var lastErr error
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				// /audit/wanted rather than /poll: it is authenticated identically and answers
				// at once, where an authorized poll parks on the hub's 25-second long-poll TTL.
				req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/audit/wanted?station=st-old", nil)
				req.Header.Set("Authorization", "Bearer tok-old")
				resp, rerr := http.DefaultClient.Do(req)
				if rerr != nil {
					lastErr = rerr
					time.Sleep(20 * time.Millisecond)
					continue
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				status, lastErr = resp.StatusCode, nil
				if status == tc.want {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			require.NoError(t, lastErr, "the hub never came up; it said: %s", out.String())
			require.Equal(t, tc.want, status,
				"AllowLegacyBearer=%v did not reach the hub; it said: %s", tc.allow, out.String())
		})
	}
}

// freeAddr picks a loopback address nothing is listening on. The hub takes its listen address
// at construction, so a test cannot ask it afterwards which port it got.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}
