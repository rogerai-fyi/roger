package main

// hub_test.go covers the ONE PRODUCTION PATH from Roger Core's JSON to a hub registration.
//
// It had none. The e2e test in cmd/rogerai-broker reimplements this wiring in a helper of its
// own (hubAuthOf) and registers the node itself, so it proves the ATTACHMENT carries a hex
// assertion key and proves nothing whatever about the code that reads one. A field name that
// did not match, a key Core sent base64 while this read hex, a length check that let a short
// key through - any of those would have shipped green and shown up as a fleet that silently
// stopped serving.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"crypto/sha256"
	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/towercore/dispatch"
	"rogerai.fm/roger/v6/internal/towercore/link"
	"rogerai.fm/roger/v6/internal/towerhub"
	"rogerai.fm/roger/v6/internal/towerjoin"
)

const hubTestTowerID = "tw-hub-test"

// coreHubNodesBody is Roger Core's answer to /tower/hub/nodes, written out in the shape the
// broker's towerHubNodes handler emits (cmd/rogerai-broker/toweredgeattach.go). It is a literal
// rather than a call into the broker because these are two separate programs that only ever
// meet over this JSON - which is exactly why the field names are worth pinning from this side
// as well as from the producer's.
const coreHubNodesBody = `{"nodes":[
  {"station_id":"st-signing","assertion_key":"%SIGNING%","hub_token":"tok-signing","state":"active"},
  {"station_id":"st-unusable","assertion_key":"not-hex","hub_token":"tok-unusable","state":"active"},
  {"station_id":"st-short","assertion_key":"aabbcc","hub_token":"tok-short","state":"active"}
]}`

// THE WHOLE PATH: Core's bytes in, a hub that will verify this node's signature out.
func TestHubNodeRegistrationReadsCoresAssertionKey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	var answer struct {
		Nodes []towerjoin.HubNode `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal(
		[]byte(strings.ReplaceAll(coreHubNodesBody, "%SIGNING%", hex.EncodeToString(pub))), &answer))
	require.Len(t, answer.Nodes, 3, "Core's field names and towerjoin.HubNode's tags no longer agree")

	server := towerhub.NewServer(towerhub.New(),
		func(grant []byte) (string, string, error) { return "att", "st-signing", nil },
		towerhub.ServerOptions{TowerID: hubTestTowerID, PollTTL: 100 * time.Millisecond,
			AllowLegacyBearer: true})
	var warnings strings.Builder
	seen := registerHubNodes(server, answer.Nodes, &warnings)
	require.Equal(t, map[string]bool{"st-signing": true, "st-unusable": true, "st-short": true}, seen,
		"every Station Core listed is registered, so the refresher does not unregister it a second later")

	mux := http.NewServeMux()
	mux.HandleFunc(towerhub.PathPoll, server.Poll)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// THE PROOF: a real signed poll, made exactly as a node makes one, is accepted - which can
	// only be true if the key Core sent as hex arrived at RegisterNode as 32 raw bytes.
	client := &towerhub.Client{BaseURL: srv.URL, TowerID: hubTestTowerID,
		// The fingerprint Core would have handed the node at attach: without it a client
		// refuses to adopt the epoch this hub names, because on the real link that value
		// arrives on an unauthenticated 401.
		TowerKeyHash: server.EpochKeyHash(),
		Sign:         towerhub.SignWith(priv), HTTP: &http.Client{Timeout: 5 * time.Second}}
	_, ok, perr := client.PollJob(t.Context(), "st-signing")
	require.NoError(t, perr, "the hub refused a poll signed with the key Core sent it")
	require.False(t, ok, "an idle queue answers empty")

	// A KEY THAT WILL NOT DECODE IS DROPPED, NOT REGISTERED SHORT, and the operator is told
	// once. Registering a truncated key would refuse that node's every poll forever, and the
	// operator would see only a station that never earns.
	require.Contains(t, warnings.String(), "st-unusable")
	require.Contains(t, warnings.String(), "st-short")
	require.NotContains(t, warnings.String(), "st-signing")
	for _, station := range []string{"st-unusable", "st-short"} {
		resp := pollWithSignature(t, srv.URL, station, priv)
		require.Equal(t, http.StatusUnauthorized, resp,
			"%s was registered with a key Core sent unusably", station)
	}

	// AND NEITHER OF THEM HOLDS ITS LEGACY TOKEN EITHER, which is a correction to what this
	// test used to assert.
	//
	// It pinned the opposite - that the bearer survives an unusable key - on the reading that
	// "no usable assertion key" describes a node too old to sign. It does not. An EMPTY key
	// describes that node, and that case still registers the token (see the empty-key leg
	// below). A key that is PRESENT and will not decode describes corruption on a Station that
	// definitely has a good key at Core: every self-attached Station is admitted with a hex
	// assertion key, and checkBindings makes it immutable for the life of the Station ID.
	//
	// Honouring the bearer there is the worst available answer. It opens that Station's queue,
	// over a plaintext link, to a string any on-path observer already holds - for a Station
	// that can no longer be authenticated any other way, so nothing will ever flip the latch
	// that would close it again. It also used to UNLATCH a Station that had been signing for
	// days, because the changed key cleared the "this Station signs" flag (see
	// internal/towerhub: TestAnUndecodableKeyDoesNotUnlatchAStationThatSigns).
	for _, station := range []string{"st-unusable", "st-short"} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+towerhub.PathPoll+"?station="+station, nil)
		req.Header.Set("Authorization", "Bearer tok-"+strings.TrimPrefix(station, "st-"))
		resp, rerr := http.DefaultClient.Do(req)
		require.NoError(t, rerr)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
			"%s: a bearer was registered against a key this tower cannot check", station)
	}
}

// THE EMPTY KEY IS THE CASE THE TOLERANCE IS ACTUALLY FOR, and it is still served.
//
// A Station Core sends no assertion key for is one whose Core predates signed polls, or whose
// attachment does - the genuine "this node is older than the change" population the transition
// promise was made to. Its bearer is registered, it keeps earning, and its own first signature
// would end the tolerance for it if it ever made one. The distinction between this and a
// mangled key is the whole of the fix above, so it is pinned from both sides.
func TestAStationWithNoAssertionKeyKeepsItsLegacyBearer(t *testing.T) {
	server := towerhub.NewServer(towerhub.New(),
		func(grant []byte) (string, string, error) { return "att", "st-old", nil },
		towerhub.ServerOptions{TowerID: hubTestTowerID, PollTTL: 100 * time.Millisecond,
			AllowLegacyBearer: true})
	var warnings strings.Builder
	seen := registerHubNodes(server, []towerjoin.HubNode{
		{StationID: "st-old", AssertionKey: "", HubToken: "tok-old"},
	}, &warnings)
	require.Equal(t, map[string]bool{"st-old": true}, seen)
	require.Empty(t, warnings.String(), "an absent key is the expected state of an old node, not a fault")

	mux := http.NewServeMux()
	mux.HandleFunc(towerhub.PathPoll, server.Poll)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+towerhub.PathPoll+"?station=st-old", nil)
	req.Header.Set("Authorization", "Bearer tok-old")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode,
		"the transition tolerance no longer reaches the nodes it was written for")
}

// pollWithSignature signs a poll for `station` with priv and returns the status. It builds the
// request by hand because towerhub.Client mints its own nonce per call and this needs to name a
// Station the caller is not the registered node for.
func pollWithSignature(t *testing.T, base, station string, priv ed25519.PrivateKey) int {
	t.Helper()
	q := url.Values{"station": {station}, "tower": {hubTestTowerID},
		"nonce": {"00112233445566778899aabbccddeeff"}}
	target := towerhub.PathPoll + "?" + q.Encode()
	pub, ts, sig := protocol.SignRequest(priv, http.MethodGet, target, nil)
	req, err := http.NewRequest(http.MethodGet, base+target, nil)
	require.NoError(t, err)
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	req.Header.Set(protocol.HeaderSig, sig)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

// THE LISTENER IS WHAT BOUNDS EVERY REMAINING PRE-AUTH COST ON THIS HUB.
//
// Two of them cannot be removed. /complete and /audit/transcript must READ the body before they
// can authenticate it, because the signature covers a digest of the bytes that arrived - so an
// admitted caller gets up to 16MB buffered before authNode sees it. And every node-facing answer
// carries an Ed25519 proof of this hub's epoch, so a request that reaches a handler costs a
// signature. With no connection cap and a two-minute read timeout, one machine could hold as
// many of both as it cared to open, which is a memory-exhaustion denial of earnings against
// every Station on the tower.
//
// A cap makes the worst case arithmetic. This pins the two halves that matter: the cap holds,
// and a connection that finishes RETURNS ITS SLOT - a leak there would be the same outage
// arriving slowly instead of quickly, and it is the half a test that only counted refusals
// would miss entirely.
func TestTheHubListenerCapsConcurrentConnections(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	ln := limitConns(raw, 2)

	accepted := make(chan net.Conn, 8)
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			accepted <- c
		}
	}()

	dial := func() net.Conn {
		c, derr := net.Dial("tcp", raw.Addr().String())
		require.NoError(t, derr)
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	dial()
	dial()
	first := <-accepted
	second := <-accepted

	// The third connects at the TCP level (the kernel's backlog answers the handshake) but the
	// listener must not hand it to a handler while both slots are held.
	dial()
	select {
	case <-accepted:
		t.Fatal("a third connection was accepted while the cap was full")
	case <-time.After(150 * time.Millisecond):
	}

	// A finished connection frees its slot, and the waiting one is served.
	require.NoError(t, first.Close())
	select {
	case third := <-accepted:
		_ = third.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("closing a connection did not release its slot - the cap leaks and the hub " +
			"stops accepting anything at all once it has seen 512 connections")
	}
	_ = second.Close()
}

// --- the settle courier, end to end through runHubInBackground ---------------------
//
// The courier is the reason a node behind this hub gets PAID: its receipt has no other ride
// to Core. runHubInBackground measured 60.5%, and the uncovered mass was exactly this - the
// spool recovery, the forward, the drop-on-success, and the shutdown drain. The seam that
// reaches it without minting consumer grants is the spool: a receipt written by a "previous
// run" (a put before the hub starts) must rejoin the backlog and be delivered.

func TestASpooledReceiptFromAPreviousRunIsDelivered(t *testing.T) {
	core := newCoreStub(t)
	core.answerDispatchKey(t)
	core.answerHubNodes(`{"nodes":[]}`)
	core.reply["/tower/edge/settle"] = func(w http.ResponseWriter, _ int) bool {
		_, _ = w.Write([]byte(`{}`))
		return true
	}
	st := servingTower(t)

	// The previous run: a receipt spooled, never forwarded, process gone.
	spool, err := newSettleSpool(st.Dir())
	require.NoError(t, err)
	require.NoError(t, spool.put(pendingSettle{
		stationID: "st-1", attemptID: "att-crash", receipt: []byte("receipt-bytes"),
		wireIn: 42, wireOut: 7, deadline: time.Now().Add(time.Hour),
	}))

	out := &syncBuffer{}
	stop := make(chan struct{})
	wait, err := runHubInBackground(st, hubOptions{Addr: "127.0.0.1:0", AllowLegacyBearer: true}, out, stop)
	require.NoError(t, err)

	// The recovery line appears at start; the retry ticker is 15s, so the delivery this test
	// observes rides the SHUTDOWN drain - which is itself the property that matters, because
	// "we are stopping" is precisely when an in-memory queue would have eaten the receipt.
	close(stop)
	wait()

	require.Contains(t, out.String(), "recovered spooled settle for att-crash",
		"the operator must be told a receipt from a previous run rejoined the backlog")
	require.Equal(t, 1, core.called("/tower/edge/settle"),
		"the recovered receipt never reached Core - somebody's pay stayed on this disk")

	// Delivered means DROPPED from the spool: a third run must recover nothing.
	require.Empty(t, spool.load(time.Now()), "a settled receipt must not be re-forwarded forever")
}

// An expired entry must NOT be revived: Core's settle window has closed, so forwarding it
// can only 4xx, and reloading it forever would wedge a slot in the backlog for good.
func TestAnExpiredSpoolEntryIsDiscardedAtLoad(t *testing.T) {
	st := servingTower(t)
	spool, err := newSettleSpool(st.Dir())
	require.NoError(t, err)
	require.NoError(t, spool.put(pendingSettle{
		stationID: "st-1", attemptID: "att-old", receipt: []byte("r"),
		deadline: time.Now().Add(-time.Minute),
	}))
	require.Empty(t, spool.load(time.Now()))
	// And the file itself is gone, not merely skipped - load's contract is that the expired
	// entry does not come back on the NEXT load either.
	require.Empty(t, spool.load(time.Now()))
}

// A receipt Core has judged invalid is dropped from the spool too - the ABANDONED branch.
// Leaving it spooled would re-present a receipt Core permanently refuses on every restart,
// filling the courier's log with the same corpse forever.
func TestAPermanentlyRefusedRecoveredReceiptIsAbandonedAndUnspooled(t *testing.T) {
	core := newCoreStub(t)
	core.answerDispatchKey(t)
	core.answerHubNodes(`{"nodes":[]}`)
	core.reply["/tower/edge/settle"] = func(w http.ResponseWriter, _ int) bool {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":{"message":"bad receipt"}}`))
		return true
	}
	st := servingTower(t)
	spool, err := newSettleSpool(st.Dir())
	require.NoError(t, err)
	require.NoError(t, spool.put(pendingSettle{
		stationID: "st-1", attemptID: "att-bad", receipt: []byte("r"),
		deadline: time.Now().Add(time.Hour),
	}))

	out := &syncBuffer{}
	stop := make(chan struct{})
	wait, err := runHubInBackground(st, hubOptions{Addr: "127.0.0.1:0", AllowLegacyBearer: true}, out, stop)
	require.NoError(t, err)
	close(stop)
	wait()

	require.Contains(t, out.String(), "ABANDONED")
	require.Empty(t, spool.load(time.Now()),
		"a permanently refused receipt must leave the spool, or every restart re-presents it")
}

// --- the whole ride: submit -> poll -> complete -> settle at Core -------------------
//
// This is the one test that walks a receipt the way production does: a consumer's sealed
// submit lands at the hub, the node polls it out over a SIGNED poll, completes with a
// receipt, and the courier forwards that receipt to Core tower-signed. Everything before it
// tested the stations of that ride separately; nothing proved the track connects. It is also
// the only test that exercises OnComplete as wired - the spool write, the queue handoff -
// rather than by calling pieces directly.
func TestACompletedJobsReceiptRidesToCore(t *testing.T) {
	// Roger Core's grant-signing key - the test holds the private half, so it can mint the
	// grant a real Core would have minted for this consumer.
	corePub, corePriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	registry := dispatch.NewWithStore(dispatch.Config{
		Network: link.PublicNetwork, Signer: corePriv,
		Lifetime: time.Minute, Now: time.Now,
	}, nil)

	// The node's assertion keypair - Core would have recorded the public half at attach.
	nodePub, nodePriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	core := newCoreStub(t)
	core.reply["/tower/dispatch/key"] = func(w http.ResponseWriter, _ int) bool {
		_ = json.NewEncoder(w).Encode(map[string]any{"dispatch_key": hex.EncodeToString(corePub)})
		return true
	}
	core.answerHubNodes(`{"nodes":[{"station_id":"st-1","assertion_key":"` +
		hex.EncodeToString(nodePub) + `","state":"active"}]}`)
	core.reply["/tower/edge/settle"] = func(w http.ResponseWriter, _ int) bool {
		_, _ = w.Write([]byte(`{}`))
		return true
	}

	st := servingTower(t)
	out := &syncBuffer{}
	stop := make(chan struct{})
	wait, err := runHubInBackground(st, hubOptions{Addr: "127.0.0.1:0"}, out, stop)
	require.NoError(t, err)

	// The hub prints its bound address once the listener is up.
	var hubAddr string
	require.Eventually(t, func() bool {
		for _, line := range strings.Split(out.String(), "\n") {
			if strings.HasPrefix(line, "hub: serving the data plane on ") {
				hubAddr = strings.Fields(strings.TrimPrefix(line, "hub: serving the data plane on "))[0]
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "the hub never said where it is listening")

	// The grant a real Core mints at authorize time: it names THIS tower, so the hub's
	// tower-binding check passes, and the test's own Station.
	identity, err := st.IdentityKey()
	require.NoError(t, err)
	grant, err := registry.MintEdge(dispatch.EdgeTarget{
		// The ADMISSION id - what Core writes into every grant it issues. Never st.TowerID,
		// the local init id Core has never heard of; a hub that verifies grants against that
		// refuses every consumer, which is exactly what a live v6.0.0 Tower did.
		TowerID: "tw-1", StationID: "st-1", StationEpoch: 1,
		Model: "m", Modality: "text", RelayName: "st-1.relay.example",
		MaxIn: 1000, MaxOut: 1000,
		AssertionKey: nodePub, ConsumerKey: consumerHubPub(t),
	})
	require.NoError(t, err)

	base := "http://" + hubAddr
	sum := sha256.Sum256(identity.Public().(ed25519.PublicKey))
	consumer := &towerhub.Client{BaseURL: base, TowerID: "tw-1",
		HTTP: &http.Client{Timeout: 30 * time.Second}}
	node := &towerhub.Client{BaseURL: base, TowerID: "tw-1", // nodes sign with the id Core told them at attach
		TowerKeyHash: hex.EncodeToString(sum[:]),
		Sign:         towerhub.SignWith(nodePriv),
		HTTP:         &http.Client{Timeout: 30 * time.Second}}

	// The consumer submits and BLOCKS until the node answers - production shape.
	type submitOut struct {
		res towerhub.Result
		err error
	}
	submitted := make(chan submitOut, 1)
	go func() {
		res, serr := consumer.SubmitJob(t.Context(), grant.Signed, []byte("sealed-request"))
		submitted <- submitOut{res, serr}
	}()

	// The node polls its queue until the job lands, then completes with its receipt.
	var job towerhub.Job
	require.Eventually(t, func() bool {
		j, ok, perr := node.PollJob(t.Context(), "st-1")
		if perr != nil || !ok {
			return false
		}
		job = j
		return true
	}, 10*time.Second, 50*time.Millisecond, "the submitted job never reached the node's queue")
	require.Equal(t, []byte("sealed-request"), job.Envelope, "the sealed body must cross the hub intact")

	require.NoError(t, node.CompleteResult(t.Context(), "st-1", towerhub.Result{
		AttemptID: job.AttemptID, Envelope: []byte("sealed-answer"), Receipt: []byte("the-receipt"),
	}))

	// The consumer's blocked submit now returns the sealed answer and the receipt.
	got := <-submitted
	require.NoError(t, got.err)
	require.Equal(t, []byte("sealed-answer"), got.res.Envelope)

	// Stopping the hub drains the courier; the receipt must reach Core even though the
	// process is going down - that is the whole reason the final drain exists.
	close(stop)
	wait()
	require.Equal(t, 1, core.called("/tower/edge/settle"),
		"the node completed and was never couriered: its pay ended here")
	var settle struct {
		StationID string `json:"station_id"`
		AttemptID string `json:"attempt_id"`
		Receipt   []byte `json:"receipt"`
	}
	require.NoError(t, json.Unmarshal(core.body("/tower/edge/settle"), &settle))
	require.Equal(t, "st-1", settle.StationID)
	require.Equal(t, job.AttemptID, settle.AttemptID)
	require.Equal(t, []byte("the-receipt"), settle.Receipt,
		"the receipt must reach Core byte-identical: Core verifies it against the station's key")

	// And delivered means unspooled: the next run of this tower recovers nothing.
	spool, err := newSettleSpool(st.Dir())
	require.NoError(t, err)
	require.Empty(t, spool.load(time.Now()), "a settled receipt stayed in the spool")
}

func consumerHubPub(t *testing.T) ed25519.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return pub
}

// A receipt whose forward fails TRANSIENTLY at shutdown is abandoned for THIS run - the
// process is going down and cannot wait - but it must stay in the spool, because "Core was
// briefly unreachable while we stopped" is exactly the crash-insurance case: the next run
// recovers and delivers it. Contrast the permanent refusal above, which unspools: retrying
// a receipt Core has judged invalid re-presents the same corpse forever.
func TestATransientFailureAtShutdownLeavesTheReceiptSpooledForNextRun(t *testing.T) {
	core := newCoreStub(t)
	core.answerDispatchKey(t)
	core.answerHubNodes(`{"nodes":[]}`)
	settleAnswers := make(chan int, 4)
	core.reply["/tower/edge/settle"] = func(w http.ResponseWriter, _ int) bool {
		w.WriteHeader(<-settleAnswers)
		_, _ = w.Write([]byte(`{"error":{"message":"briefly down"}}`))
		return true
	}
	st := servingTower(t)
	spool, err := newSettleSpool(st.Dir())
	require.NoError(t, err)
	require.NoError(t, spool.put(pendingSettle{
		stationID: "st-1", attemptID: "att-flap", receipt: []byte("r"),
		deadline: time.Now().Add(time.Hour),
	}))

	// FIRST RUN: Core answers 503; the shutdown drain abandons but must not unspool.
	settleAnswers <- http.StatusServiceUnavailable
	out := &syncBuffer{}
	stop := make(chan struct{})
	wait, err := runHubInBackground(st, hubOptions{Addr: "127.0.0.1:0"}, out, stop)
	require.NoError(t, err)
	close(stop)
	wait()
	require.Contains(t, out.String(), "ABANDONED at shutdown")
	require.Len(t, spool.load(time.Now()), 1,
		"a transiently failed receipt left the spool: the next run can never deliver it")

	// SECOND RUN: Core is back; the receipt rides.
	settleAnswers <- http.StatusOK
	out2 := &syncBuffer{}
	stop2 := make(chan struct{})
	wait2, err := runHubInBackground(st, hubOptions{Addr: "127.0.0.1:0"}, out2, stop2)
	require.NoError(t, err)
	close(stop2)
	wait2()
	require.Contains(t, out2.String(), "recovered spooled settle for att-flap")
	require.Empty(t, spool.load(time.Now()), "delivered on the second run, so the spool must be empty")
}
