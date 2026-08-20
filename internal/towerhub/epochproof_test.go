package towerhub

// epochproof_test.go is about WHERE THE EPOCH COMES FROM.
//
// The hub-side check of the epoch was always exact: a request naming a run that has ended is
// refused, no clock consulted. What these tests are about is the other half, which was missing -
// the value being checked reached the node on an UNAUTHENTICATED 401 over a link that is
// plaintext by construction, so it was the ANSWERING PARTY's value rather than the hub's. Every
// test here fails against a client that simply believed that header.

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// hubRig is one hub Server mounted on its own httptest server, with the node routes wired.
func hubRig(t *testing.T, towerID string) (*Server, *httptest.Server) {
	t.Helper()
	s := NewServer(New(), stubCheck, ServerOptions{TowerID: towerID, EpochKey: testHubKey,
		SubmitTTL: 3 * time.Second, PollTTL: 150 * time.Millisecond})
	mux := http.NewServeMux()
	mux.HandleFunc(PathSubmit, s.Submit)
	mux.HandleFunc(PathPoll, s.Poll)
	mux.HandleFunc(PathComplete, s.Complete)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv
}

// THE ATTACK THIS FILE EXISTS FOR. An on-path attacker answers the node's poll with a forged
// "401 + an epoch of my choosing". A client that caches that value re-signs against it - a
// genuine Ed25519 signature, a fresh nonce, a fresh timestamp, over bytes no hub has ever seen
// and no nonce ring has ever recorded. That is not a replay; it is a signature the node was
// tricked into MINTING, and it is good wherever that epoch is live.
//
// The node now refuses to sign for an epoch the relay cannot prove is its own.
func TestAForged401CannotChooseTheEpochThisNodeSignsFor(t *testing.T) {
	node := newTestNode(t)
	real, _ := hubRig(t, testTowerID)
	real.RegisterNode("st-1", node.auth())

	const attackerEpoch = "deadbeefdeadbeefdeadbeefdeadbeef"
	var signedForAttacker atomic.Int64
	var requests atomic.Int64
	// The on-path attacker: it sees every request (the link is plaintext) and answers the first
	// one with a forged epoch refusal. Anything that comes back naming its epoch is a signature
	// it manufactured.
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Query().Get(hubParam) == attackerEpoch {
			signedForAttacker.Add(1)
		}
		w.Header().Set(HubEpochHeader, attackerEpoch)
		writeErr(w, http.StatusUnauthorized, "this signature was made for a different run of this hub")
	}))
	t.Cleanup(attacker.Close)

	client := &Client{BaseURL: attacker.URL, TowerID: testTowerID, TowerKeyHash: real.EpochKeyHash(),
		Sign: node.signer(), HTTP: &http.Client{Timeout: 5 * time.Second}}
	_, _, err := client.PollJob(t.Context(), "st-1")

	require.ErrorIs(t, err, ErrHubEpochUnproved,
		"a node adopted an epoch that arrived on an unauthenticated 401 with no proof behind it")
	require.Zero(t, signedForAttacker.Load(),
		"the node signed a request naming the attacker's epoch: that signature is unconsumed, "+
			"in the clear, and good at any hub holding that epoch")
	require.Equal(t, int64(1), requests.Load(), "the refusal must not be retried into a loop")
	require.Empty(t, client.hubEpoch(), "the poisoned value was cached and every later request carries it")
}

// A proof is not enough on its own: it has to be made with the key CORE admitted this relay
// under. A tower that signs its epoch with some other key is a party the node was never placed
// on, whatever it says about itself.
func TestAnEpochProvedWithTheWrongKeyIsRefused(t *testing.T) {
	node := newTestNode(t)
	real, _ := hubRig(t, testTowerID)
	real.RegisterNode("st-1", node.auth())

	_, impostorKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	impostor := NewServer(New(), stubCheck, ServerOptions{TowerID: testTowerID, EpochKey: impostorKey,
		PollTTL: 100 * time.Millisecond})
	impostor.RegisterNode("st-1", node.auth())
	mux := http.NewServeMux()
	mux.HandleFunc(PathPoll, impostor.Poll)
	impostorSrv := httptest.NewServer(mux)
	t.Cleanup(impostorSrv.Close)

	// The node holds the fingerprint of the REAL tower's key, as Core gave it.
	client := &Client{BaseURL: impostorSrv.URL, TowerID: testTowerID, TowerKeyHash: real.EpochKeyHash(),
		Sign: node.signer(), HTTP: &http.Client{Timeout: 5 * time.Second}}
	_, _, err = client.PollJob(t.Context(), "st-1")
	require.ErrorIs(t, err, ErrHubEpochUnproved)
}

// And the honest path still works, which is the half a fix like this most easily breaks: a real
// hub's epoch, proved with the real key, is adopted on the first refusal and the retry succeeds.
func TestAProvedEpochIsAdoptedAndThePollSucceeds(t *testing.T) {
	node := newTestNode(t)
	s, srv := hubRig(t, testTowerID)
	s.RegisterNode("st-1", node.auth())

	client := node.client(srv.URL, 5*time.Second)
	_, ok, err := client.PollJob(t.Context(), "st-1")
	require.NoError(t, err)
	require.False(t, ok, "an idle queue answers empty")
	require.Equal(t, s.Epoch(), client.hubEpoch(), "the client learned the hub's real epoch")
}

// THE ACTIVE HALF OF "TWO HUB PROCESSES FAIL CLOSED". The attacker reads hub B's epoch off an
// unauthenticated request, feeds it to the node in a forged 401 in front of hub A, captures the
// re-signed poll and replays it at B - which accepts it and hands over the victim's job.
//
// Fixing the epoch's provenance dissolves this particular route: the attacker cannot make the
// node sign for B's epoch by ASSERTING it, because an assertion is not a proof. (What survives
// is RELAYING the node's own request to B and handing back B's genuine answer, which needs two
// live processes to exist at all - see the passive test below, which is what now stops it.)
func TestAnEpochStolenFromASecondHubCannotBeFedToTheNode(t *testing.T) {
	node := newTestNode(t)
	hubA, _ := hubRig(t, testTowerID)
	hubA.RegisterNode("st-1", node.auth())
	hubB, hubBSrv := hubRig(t, testTowerID)
	hubB.RegisterNode("st-1", node.auth())

	// A job waiting at B, which is what a successful replay would take.
	go func() {
		_, _ = hubB.hub.Submit(t.Context(), Job{AttemptID: "att-b", StationID: "st-1",
			Grant: []byte("att-b|st-1"), Envelope: []byte("sealed")})
	}()
	time.Sleep(50 * time.Millisecond)

	var captured atomic.Value // the re-signed request the attacker would replay at B
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get(hubParam) == hubB.Epoch() {
			captured.Store(r.URL.RequestURI() + "\x00" + r.Header.Get("X-Roger-Pubkey") +
				"\x00" + r.Header.Get("X-Roger-TS") + "\x00" + r.Header.Get("X-Roger-Sig"))
		}
		// The forged refusal, naming the OTHER live process's epoch. Real value, wrong mouth.
		w.Header().Set(HubEpochHeader, hubB.Epoch())
		writeErr(w, http.StatusUnauthorized, "this signature was made for a different run of this hub")
	}))
	t.Cleanup(attacker.Close)

	client := &Client{BaseURL: attacker.URL, TowerID: testTowerID, TowerKeyHash: hubA.EpochKeyHash(),
		Sign: node.signer(), HTTP: &http.Client{Timeout: 5 * time.Second}}
	_, _, err := client.PollJob(t.Context(), "st-1")
	require.ErrorIs(t, err, ErrHubEpochUnproved)
	require.Nil(t, captured.Load(), "the node minted a poll signature for the other hub process")

	// And B still holds the job: nothing was dequeued from it.
	poll := node.client(hubBSrv.URL, 5*time.Second)
	_, ok, perr := poll.PollJob(t.Context(), "st-1")
	require.NoError(t, perr)
	require.True(t, ok, "the victim's job was taken from hub B by a replayed signature")
}

// THE PASSIVE HALF, WHICH NEEDS NO ATTACKER AT ALL. A round-robin load balancer over two hub
// processes makes a client flap between their epochs, and every request that lands on the
// process it did not sign for leaves a genuine, unconsumed signature for the other one - in the
// clear, on a link anyone can watch. The flap MANUFACTURES the material a replay needs.
//
// An epoch is 128 bits of crypto/rand minted once per process, so a hub that restarted can never
// name an epoch this client has already moved off; only a second LIVE process can. Coming back
// to a retired epoch is therefore proof of the unsupported deployment, with no false positive
// available to anybody - and the client stops and says so rather than flapping forever.
func TestARoundRobinOverTwoHubProcessesStopsInsteadOfFlapping(t *testing.T) {
	node := newTestNode(t)
	hubA, _ := hubRig(t, testTowerID)
	hubA.RegisterNode("st-1", node.auth())
	hubB, _ := hubRig(t, testTowerID)
	hubB.RegisterNode("st-1", node.auth())

	var n atomic.Int64
	var mu sync.Mutex
	unconsumed := map[string]int{} // epoch -> signatures made for a process that never saw them
	lb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var here, there *Server
		if n.Add(1)%2 == 1 {
			here, there = hubA, hubB
		} else {
			here, there = hubB, hubA
		}
		if e := r.URL.Query().Get(hubParam); e == there.Epoch() {
			mu.Lock()
			unconsumed[e]++
			mu.Unlock()
		}
		here.Poll(w, r)
	}))
	t.Cleanup(lb.Close)

	client := &Client{BaseURL: lb.URL, TowerID: testTowerID, TowerKeyHash: hubA.EpochKeyHash(),
		Sign: node.signer(), HTTP: &http.Client{Timeout: 5 * time.Second}}

	// Twelve calls is the same budget the recorded flap ran for; the client must stop long
	// before it runs out. An ordinary 401 does not end the loop - a flapping client returns
	// those all day, which is exactly the state being refused.
	var last error
	for i := 0; i < 12; i++ {
		_, _, last = client.PollJob(t.Context(), "st-1")
		if last != nil && strings.Contains(last.Error(), "more than one live hub process") {
			break
		}
	}
	require.ErrorIs(t, last, ErrHubMultipleProcesses,
		"the client flapped between two hub processes forever, manufacturing an unconsumed "+
			"signature on every request that landed on the wrong one")
	mu.Lock()
	total := 0
	for _, c := range unconsumed {
		total += c
	}
	mu.Unlock()
	require.LessOrEqual(t, total, 3,
		"the flap kept minting signatures for the process that never saw them (%d of them)", total)
}

// EVERY WORKER RECOVERS FROM A RESTART, NOT ONE OF THEM.
//
// The retry used to be triggered by an epoch that was new TO THE CACHE, so on a redeploy the
// first worker to notice learned the value and retried while every other worker - which had
// already sent the stale epoch and got the same 401 - was told "nothing new" and hard-failed.
// In ServeLoop each of those is a 2s backoff plus an ErrHubRefusedThisNode notice: an
// operator-facing "your relay refuses this node's identity" alarm on every routine redeploy,
// fired on the channel deliberately designed not to be discardable.
//
// A worker's retry has to turn on ITS OWN request: did the answer name an epoch different from
// the one this attempt sent.
func TestEveryWorkerRecoversFromAHubRestartNotJustTheFirst(t *testing.T) {
	node := newTestNode(t)
	before, srv := hubRig(t, testTowerID)
	before.RegisterNode("st-1", node.auth())

	client := node.client(srv.URL, 5*time.Second)
	_, _, err := client.PollJob(t.Context(), "st-1")
	require.NoError(t, err, "the client learned the first run's epoch")

	// THE REDEPLOY: same address, same tower identity, new process and therefore new epoch.
	after := NewServer(New(), stubCheck, ServerOptions{TowerID: testTowerID, EpochKey: testHubKey,
		PollTTL: 100 * time.Millisecond})
	after.RegisterNode("st-1", node.auth())
	mux := http.NewServeMux()
	mux.HandleFunc(PathPoll, after.Poll)
	srv.Config.Handler = mux

	// Eight workers sharing one Client, exactly as ServeTower runs them, all polling across the
	// restart at once.
	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, perr := client.PollJob(t.Context(), "st-1")
			errs <- perr
		}()
	}
	wg.Wait()
	close(errs)
	failed := 0
	for e := range errs {
		if e != nil {
			failed++
			t.Logf("worker failed across the redeploy: %v", e)
		}
	}
	require.Zero(t, failed, "%d of %d workers hard-failed a single epoch change", failed, workers)
}

// A hub that restarts BETWEEN the two attempts answers the retry with a third epoch. Not reading
// it left the cache holding a value already known to be stale, so the next call burned its one
// retry rediscovering that. No third attempt is made here - the poll loop is the retry - but the
// cache must come out correct.
func TestTheSecondAnswersEpochIsLearnedToo(t *testing.T) {
	node := newTestNode(t)
	first, _ := hubRig(t, testTowerID)
	second, _ := hubRig(t, testTowerID)
	third, _ := hubRig(t, testTowerID)
	for _, s := range []*Server{first, second, third} {
		s.RegisterNode("st-1", node.auth())
	}
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch n.Add(1) {
		case 1:
			first.Poll(w, r) // 401: the client knows no epoch yet, and learns first's
		default:
			second.Poll(w, r) // the retry lands on a process that has already replaced it
		}
	}))
	t.Cleanup(srv.Close)

	client := &Client{BaseURL: srv.URL, TowerID: testTowerID, TowerKeyHash: first.EpochKeyHash(),
		Sign: node.signer(), HTTP: &http.Client{Timeout: 5 * time.Second}}
	_, _, err := client.PollJob(t.Context(), "st-1")
	require.Error(t, err, "the retry landed on a hub with a different epoch and was refused")
	require.Equal(t, second.Epoch(), client.hubEpoch(),
		"the second answer's epoch was thrown away, so the next call starts from a known-stale value")
}

// TWO CAUSES, TWO SENTENCES. "This request carries no epoch" is the ordinary opening move - a
// client's first request to a hub cannot have one, because the 401 is the only way to learn it -
// and telling it that the hub "has restarted since" sends an operator hunting a redeploy that
// never happened. It is the same mistake the nonce gate's two refusals were separated for.
func TestNoEpochAndTheWrongEpochAreDifferentSentences(t *testing.T) {
	node := newTestNode(t)
	s, srv := hubRig(t, testTowerID)
	s.RegisterNode("st-1", node.auth())

	say := func(epoch string) string {
		q := url.Values{"station": {"st-1"}}
		target := hubTarget(testTowerID, epoch, PathPoll, q)
		req, err := http.NewRequest(http.MethodGet, srv.URL+target, nil)
		require.NoError(t, err)
		pub, ts, sig := node.signer()(http.MethodGet, target, nil)
		req.Header.Set("X-Roger-Pubkey", pub)
		req.Header.Set("X-Roger-TS", strconv.FormatInt(ts, 10))
		req.Header.Set("X-Roger-Sig", sig)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		var out struct {
			Error string `json:"error"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		return out.Error
	}

	none := say("")
	wrong := say("00000000000000000000000000000000")
	require.NotEqual(t, none, wrong, "a request with no epoch and one with the wrong epoch got the same answer")
	require.Contains(t, none, "names no hub run")
	require.NotContains(t, none, "restarted since",
		"a first request was told the hub had restarted, which it had not")
	require.Contains(t, wrong, "restarted since")
	require.Contains(t, none, HubEpochHeader, "the answer has to say where the epoch comes from")
}

// The proof headers are on EVERY node-facing answer, not only on the epoch refusal: a node adopts
// an epoch whenever a response names one it did not send, and that response is not always the
// epoch refusal (a door refusal or an unknown-Station refusal reaches a stale client too).
func TestEveryNodeFacingAnswerCarriesTheEpochProof(t *testing.T) {
	node := newTestNode(t)
	s, srv := hubRig(t, testTowerID)
	s.RegisterNode("st-1", node.auth())

	// An unauthenticated stranger's request: refused, and the answer still proves the epoch.
	resp, err := http.Get(srv.URL + PathPoll + "?station=st-1&nonce=" + strings.Repeat("ab", nonceBytes))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, s.Epoch(), resp.Header.Get(HubEpochHeader))
	keyHex := resp.Header.Get(HubKeyHeader)
	require.Equal(t, hex.EncodeToString(testHubKey.Public().(ed25519.PublicKey)), keyHex)
	raw, derr := hex.DecodeString(keyHex)
	require.NoError(t, derr)
	sig, derr := hex.DecodeString(resp.Header.Get(HubProofHeader))
	require.NoError(t, derr)
	require.True(t, ed25519.Verify(ed25519.PublicKey(raw),
		hubEpochStatement(testTowerID, s.Epoch(), strings.Repeat("ab", nonceBytes)), sig),
		"the hub's epoch proof does not verify over the statement both sides build")

	// AND IT BINDS THIS REQUEST'S NONCE. Without that the proof is a bearer token for an epoch:
	// captured once, replayable into any later request to point a node at a dead run.
	require.False(t, ed25519.Verify(ed25519.PublicKey(raw),
		hubEpochStatement(testTowerID, s.Epoch(), "some-other-nonce"), sig),
		"the proof is not bound to the request it answers, so it can be stockpiled")
}
