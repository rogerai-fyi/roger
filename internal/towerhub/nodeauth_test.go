package towerhub

// nodeauth_test.go proves the property signed polls exist for: that nothing a node puts on the
// plaintext hub link can be picked up by someone else and used to take that node's work.
//
// Each test below fails against the bearer-token hub these replaced. That is the bar - not
// "signatures are computed somewhere" but "the specific attack no longer works".

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
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

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/protocol"
)

// testNode is a serving node's identity: the Station assertion key it signs with, in the two
// shapes the code under test wants it (a Signer for the Client, a NodeAuth for the Server).
type testNode struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
}

func newTestNode(t *testing.T) *testNode {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return &testNode{priv: priv, pub: pub}
}

// testTowerID is the hub id every test server and every test client agrees on. It is not a
// detail: the tower id is signed into each request's target, so a mismatch here is a 401, which
// is exactly the property TestASignatureIsGoodAtOneHubOnly relies on.
const testTowerID = "tw-test"

func (n *testNode) signer() Signer { return SignWith(n.priv) }
func (n *testNode) auth() NodeAuth { return NodeAuth{AssertionKey: n.pub} }
func (n *testNode) client(base string, timeout time.Duration) *Client {
	return &Client{BaseURL: base, TowerID: testTowerID, Sign: n.signer(), HTTP: &http.Client{Timeout: timeout}}
}

// signedRequest builds one authenticated hub request by hand. It exists because the tests that
// matter here are about REUSING a request, and the Client deliberately makes that impossible -
// it mints a fresh nonce per call. Holding the target and the headers still lets a test play the
// on-path attacker, who has exactly these bytes and nothing else.
func (n *testNode) signedRequest(t *testing.T, method, base, target string, body []byte) func() *http.Request {
	t.Helper()
	pub, ts, sig := n.signer()(method, target, body)
	return func() *http.Request {
		var r io.Reader
		if body != nil {
			r = bytes.NewReader(body)
		}
		req, err := http.NewRequest(method, base+target, r)
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(protocol.HeaderPubkey, pub)
		req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
		req.Header.Set(protocol.HeaderSig, sig)
		return req
	}
}

// getSigned is the ordinary one-shot: a freshly signed GET with its own nonce.
func (n *testNode) getSigned(t *testing.T, base, path string, q url.Values) (*http.Response, []byte) {
	t.Helper()
	target := hubTarget(testTowerID, path, q)
	return do(t, n.signedRequest(t, http.MethodGet, base, target, nil)())
}

// postSigned is the ordinary one-shot POST. The body it signs is the body it sends, byte for
// byte - the server verifies the digest of what arrived, not of a re-encode.
func (n *testNode) postSigned(t *testing.T, base, path string, body any) (*http.Response, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	target := hubTarget(testTowerID, path, url.Values{})
	return do(t, n.signedRequest(t, http.MethodPost, base, target, raw)())
}

func do(t *testing.T, req *http.Request) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, raw
}

// THE ATTACK, VERBATIM. An on-path observer copies a poll off the wire and sends it again. Under
// the bearer scheme that took the victim's next job - the attacker cannot open it, but the honest
// node never sees it and is never paid for it, and the consumer sees a failure. Here the replay
// is refused and the job is still there for the node that earned it.
func TestAReplayedPollIsRefusedAndDoesNotSwallowTheJob(t *testing.T) {
	node := newTestNode(t)
	s, srv := testServer(t)
	s.RegisterNode("st-1", node.auth())

	// What the attacker captured: one complete, valid, unexpired poll request.
	target := hubTarget(testTowerID, PathPoll, url.Values{"station": {"st-1"}})
	captured := node.signedRequest(t, http.MethodGet, srv.URL, target, nil)

	// It worked the first time, as it must - this is a real request the node made.
	first, _ := do(t, captured())
	require.Equal(t, http.StatusNoContent, first.StatusCode, "an idle long poll is a 204")

	// A consumer now submits work for this Station. This is the job the attack is aimed at.
	submitted := make(chan *http.Response, 1)
	go func() {
		resp, _ := postJSON(t, srv.URL+PathSubmit, "", submitReq{
			Grant:    base64.StdEncoding.EncodeToString([]byte("att-1|st-1")),
			Envelope: base64.StdEncoding.EncodeToString([]byte("sealed-request")),
		})
		submitted <- resp
	}()

	// The attacker replays. Refused - and told why, so the failure is legible from a log.
	replay, body := do(t, captured())
	require.Equal(t, http.StatusUnauthorized, replay.StatusCode,
		"a captured poll was accepted a second time - the denial-of-earnings attack still works")
	require.Contains(t, string(body), "already been made")

	// And the job is still the honest node's. This is the half that makes the refusal worth
	// anything: refusing the replay AFTER it had dequeued would be no defence at all.
	got, raw := node.getSigned(t, srv.URL, PathPoll, url.Values{"station": {"st-1"}})
	require.Equal(t, http.StatusOK, got.StatusCode, string(raw))
	var job pollResp
	require.NoError(t, json.Unmarshal(raw, &job))
	require.Equal(t, "att-1", job.AttemptID)

	// Let the parked consumer go.
	node.postSigned(t, srv.URL, PathComplete, completeReq{AttemptID: job.AttemptID, StationID: "st-1",
		Envelope: base64.StdEncoding.EncodeToString([]byte("answer"))})
	<-submitted
}

// The same attack against the REAL client path rather than a hand-built request: record every
// byte an actual towerhub.Client sends while serving, then replay the recording. Nothing it
// transmits is reusable, and nothing it transmits is a credential.
func TestNothingTheClientSendsCanBeCapturedAndReused(t *testing.T) {
	node := newTestNode(t)
	s := NewServer(New(), stubCheck, ServerOptions{TowerID: testTowerID,
		SubmitTTL: 3 * time.Second, PollTTL: 200 * time.Millisecond, AllowLegacyBearer: true})
	// The legacy token exists on this registration, as it does on every real one during the
	// transition. The point is that the node never puts it on the wire.
	s.RegisterNode("st-1", NodeAuth{AssertionKey: node.pub, LegacyToken: "the-old-bearer-token"})

	type captured struct {
		method, target string
		header         http.Header
		body           []byte
	}
	seen := make(chan captured, 32)
	mux := http.NewServeMux()
	mux.HandleFunc(PathSubmit, s.Submit)
	mux.HandleFunc(PathPoll, s.Poll)
	mux.HandleFunc(PathComplete, s.Complete)
	// The tap: an on-path observer sees the request exactly as it arrives, because the link is
	// plain http and this is precisely what that means.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		select {
		case seen <- captured{r.Method, requestTarget(r), r.Header.Clone(), body}:
		default:
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = ServeLoop(ctx, node.client(srv.URL, 5*time.Second), "st-1", echoExec{}, nil) }()

	consumer := &Client{BaseURL: srv.URL}
	sctx, scancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer scancel()
	_, err := consumer.SubmitJob(sctx, []byte("att-1|st-1"), []byte("sealed-req"))
	require.NoError(t, err)
	cancel()

	var nodeCalls []captured
	for done := false; !done; {
		select {
		case c := <-seen:
			if c.target == PathSubmit {
				continue // the consumer's call, which is grant-authorized rather than signed
			}
			nodeCalls = append(nodeCalls, c)
		default:
			done = true
		}
	}
	require.NotEmpty(t, nodeCalls, "the node made no calls to capture")

	for _, c := range nodeCalls {
		// NO REUSABLE SECRET LEFT THE MACHINE. Not in a header, not in the target, not in the
		// body. This is the assertion the whole change exists to make true.
		require.Empty(t, c.header.Get("Authorization"), "%s carried an Authorization header", c.target)
		require.NotContains(t, c.target+string(c.body), "the-old-bearer-token")
		for name, vals := range c.header {
			for _, v := range vals {
				require.NotContains(t, v, "the-old-bearer-token", "the token appeared in %s", name)
			}
		}
	}

	// AND WHAT WAS CAPTURED IS INERT. Every recorded node call, replayed byte for byte, is
	// refused - including the completions, which are idempotent anyway, because a per-route
	// exemption is a trap for whoever adds the next route.
	for _, c := range nodeCalls {
		var body io.Reader
		if len(c.body) > 0 {
			body = bytes.NewReader(c.body)
		}
		req, err := http.NewRequest(c.method, srv.URL+c.target, body)
		require.NoError(t, err)
		req.Header = c.header.Clone()
		resp, _ := do(t, req)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
			"a replay of %s %s was accepted", c.method, c.target)
	}
}

// The transition, both halves of it. A hub still accepts the old bearer so a node built before
// this change keeps earning while it updates - and a hub whose operator has ended the tolerance
// refuses it, which is a thing an operator can now actually do (see cmdServe's
// -hub-legacy-bearer; it was documented as "default true" while being settable from nowhere but
// a test).
func TestAStolenBearerTokenDiesWithTheTransitionWindow(t *testing.T) {
	node := newTestNode(t)
	s, srv := testServer(t)
	s.RegisterNode("st-1", NodeAuth{AssertionKey: node.pub, LegacyToken: "stolen"})

	// During the window: an out-of-date node (and therefore a thief holding its token) is served.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+PathPoll+"?station=st-1", nil)
	req.Header.Set("Authorization", "Bearer stolen")
	resp, _ := do(t, req)
	require.Equal(t, http.StatusNoContent, resp.StatusCode,
		"a pre-signature node must keep working through the transition release")

	// A hub that has ended it: the token is not a credential any more, and the signing node is
	// unaffected.
	strict, strictSrv := testServerWith(t, ServerOptions{TowerID: testTowerID,
		SubmitTTL: 3 * time.Second, PollTTL: 300 * time.Millisecond})
	strict.RegisterNode("st-1", NodeAuth{AssertionKey: node.pub, LegacyToken: "stolen"})
	req, _ = http.NewRequest(http.MethodGet, strictSrv.URL+PathPoll+"?station=st-1", nil)
	req.Header.Set("Authorization", "Bearer stolen")
	resp, body := do(t, req)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "the stolen token still opened the queue")
	require.Contains(t, string(body), "bearer tokens are no longer accepted")

	ok, _ := node.getSigned(t, strictSrv.URL, PathPoll, url.Values{"station": {"st-1"}})
	require.Equal(t, http.StatusNoContent, ok.StatusCode, "the signing node kept serving")
}

// THE HOLE THE TRANSITION LEFT OPEN, AND THE POINT OF THE WHOLE CHANGE.
//
// The tolerance was registered for every Station, including ones whose node had already
// upgraded and was signing - and Core never rotates a hub token. So an attacker who lifted a
// token off the cleartext wire at any point BEFORE the node upgraded could still poll that
// node's queue afterwards, indefinitely, from off the path. For exactly the operators this
// change was written for - the ones who ran the vulnerable build on a hostile network -
// upgrading bought nothing for a whole release.
//
// The Station's first real signature is what ends it, because that is the moment this tower
// learns the node is not the old build the tolerance exists for.
func TestOnceAStationSignsItsStolenTokenIsDead(t *testing.T) {
	node := newTestNode(t)
	s, srv := testServer(t)
	s.RegisterNode("st-1", NodeAuth{AssertionKey: node.pub, LegacyToken: "lifted-before-the-upgrade"})

	// BEFORE THE UPGRADE. The node still presents a bearer, so the thief's copy works - this is
	// the exposure an already-released binary already has, and the reason the tolerance exists.
	stolen := func() *http.Request {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+PathPoll+"?station=st-1", nil)
		req.Header.Set("Authorization", "Bearer lifted-before-the-upgrade")
		return req
	}
	resp, _ := do(t, stolen())
	require.Equal(t, http.StatusNoContent, resp.StatusCode, "the transition tolerance is on")

	// THE UPGRADE: one signed poll. Nothing else about the registration changes - Core keeps
	// sending the same token, which is precisely why the tower cannot wait for Core to tell it.
	up, _ := node.getSigned(t, srv.URL, PathPoll, url.Values{"station": {"st-1"}})
	require.Equal(t, http.StatusNoContent, up.StatusCode, "the upgraded node polls normally")

	// AFTER IT: the same captured token, unchanged and unexpired, is no longer a credential.
	resp, body := do(t, stolen())
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"a token captured before the upgrade still opened the queue after it")
	require.Contains(t, string(body), "authenticates by signature")

	// And it stays dead across the refresher re-registering the Station every thirty seconds
	// with the identical answer from Core, which is the shape that would quietly undo this.
	s.RegisterNode("st-1", NodeAuth{AssertionKey: node.pub, LegacyToken: "lifted-before-the-upgrade"})
	resp, _ = do(t, stolen())
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"a routine re-registration reopened the bearer path")

	// The honest node is untouched throughout.
	ok, _ := node.getSigned(t, srv.URL, PathPoll, url.Values{"station": {"st-1"}})
	require.Equal(t, http.StatusNoContent, ok.StatusCode)
}

// A SIGNED request never falls back to the token. Otherwise an attacker who strips the signature
// headers off a captured request - or a hub that answers 401 until the node gives up on them -
// would walk the fleet back to the scheme this replaced.
func TestAnUnsignedRequestCannotBorrowASignedStationsRights(t *testing.T) {
	node := newTestNode(t)
	s, srv := testServer(t)
	s.RegisterNode("st-1", node.auth()) // no legacy token: this Station signs

	req, _ := http.NewRequest(http.MethodGet, srv.URL+PathPoll+"?station=st-1", nil)
	req.Header.Set("Authorization", "Bearer anything-at-all")
	resp, body := do(t, req)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Contains(t, string(body), "sign hub requests")
}

// A signature is good for one Station. Two nodes on one tower, and the second one's key cannot
// reach into the first one's queue - the pubkey must BE the assertion key that Station attached
// with, not merely a key that signs.
func TestASignatureFromAnotherStationIsRefused(t *testing.T) {
	mine, theirs := newTestNode(t), newTestNode(t)
	s, srv := testServer(t)
	s.RegisterNode("st-1", mine.auth())
	s.RegisterNode("st-2", theirs.auth())

	resp, body := theirs.getSigned(t, srv.URL, PathPoll, url.Values{"station": {"st-1"}})
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Contains(t, string(body), "not this Station's attached assertion key")
}

// The timestamp window is enforced, so a signature captured and held cannot be presented later -
// the nonce cache only has to remember as far back as this check accepts.
func TestASignatureOutsideTheSkewWindowIsRefused(t *testing.T) {
	node := newTestNode(t)
	s, srv := testServer(t)
	s.RegisterNode("st-1", node.auth())

	target := hubTarget(testTowerID, PathPoll, url.Values{"station": {"st-1"}})
	stale := time.Now().Add(-2 * protocol.SigMaxSkew).Unix()
	sig := ed25519.Sign(node.priv, []byte(protocol.CanonicalRequest(http.MethodGet, target, stale, nil)))
	req, _ := http.NewRequest(http.MethodGet, srv.URL+target, nil)
	req.Header.Set(protocol.HeaderPubkey, hex.EncodeToString(node.pub))
	req.Header.Set(protocol.HeaderTS, strconv.FormatInt(stale, 10))
	req.Header.Set(protocol.HeaderSig, hex.EncodeToString(sig))
	resp, body := do(t, req)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Contains(t, string(body), "clock")
}

// A tampered body is a different request. The signature covers the digest of the bytes that
// arrive, so an on-path attacker cannot take a real completion and swap the result inside it.
func TestATamperedBodyBreaksTheSignature(t *testing.T) {
	node := newTestNode(t)
	s, srv := testServer(t)
	s.RegisterNode("st-1", node.auth())

	honest, _ := json.Marshal(completeReq{AttemptID: "att-1", StationID: "st-1",
		Envelope: base64.StdEncoding.EncodeToString([]byte("the real answer"))})
	target := hubTarget(testTowerID, PathComplete, url.Values{})
	pub, ts, sig := node.signer()(http.MethodPost, target, honest)

	tampered, _ := json.Marshal(completeReq{AttemptID: "att-1", StationID: "st-1",
		Envelope: base64.StdEncoding.EncodeToString([]byte("a substituted answer"))})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+target, bytes.NewReader(tampered))
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	req.Header.Set(protocol.HeaderSig, sig)
	resp, _ := do(t, req)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// A tower that has not been told a Station's assertion key yet says exactly that, rather than
// answering "not the registered node" and sending an operator hunting for a revocation. This is
// the Core-older-than-the-tower case, and the legacy token is what covers it in practice.
func TestAStationWithNoAssertionKeySaysSo(t *testing.T) {
	node := newTestNode(t)
	s, srv := testServer(t)
	s.RegisterNode("st-1", NodeAuth{LegacyToken: "only-a-token"})

	resp, body := node.getSigned(t, srv.URL, PathPoll, url.Values{"station": {"st-1"}})
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Contains(t, string(body), "no assertion key")
}

// The nonce cache is the one attacker-facing data structure this change adds, so its bound is
// asserted rather than reasoned about: two generations per Station, each capped, whatever the
// traffic. A cache that grew with the number of requests would be a memory exhaustion primitive
// handed out with the fix for a denial-of-earnings one.
func TestTheNonceCacheIsBoundedPerStation(t *testing.T) {
	var g nonceGate
	now := time.Now()
	for i := 0; i < 20*maxNoncesPerStation; i++ {
		// Timestamps advance with the traffic, as a real node's do; a burst all stamped with
		// one instant would be refused by the floor rather than by the cache, which is a
		// different test (TestTheNonceCapIsNotAReplayWindow).
		require.True(t, g.fresh("st-1", strconv.Itoa(i), now.Add(time.Duration(i)*time.Millisecond), now),
			"a fresh nonce was called a replay")
	}
	g.mu.Lock()
	r := g.rings["st-1"]
	total := len(r.cur) + len(r.prev)
	g.mu.Unlock()
	require.LessOrEqual(t, total, 2*maxNoncesPerStation,
		"the replay cache grows without bound: %d entries", total)

	// And within a generation it still does its job.
	require.True(t, g.fresh("st-2", "n", now, now))
	require.False(t, g.fresh("st-2", "n", now, now), "a repeat inside the window was not caught")
	// Stations do not share a namespace, so one node cannot evict or collide with another's.
	require.True(t, g.fresh("st-3", "n", now, now))
}

// A NONCE IS REMEMBERED FOR AS LONG AS ITS SIGNATURE IS ACCEPTABLE, which is the whole
// invariant, and the first version of this file got it wrong by a factor of two.
//
// protocol.VerifyRequest accepts a timestamp within SigMaxSkew in EITHER direction, so a
// signature stamped at T is acceptable from T-skew to T+skew: a 2 x SigMaxSkew span of tower
// time. Rotating generations every SigMaxSkew therefore forgot a nonce while its own signature
// was still good - and the code claimed the opposite in so many words, "exactly the window in
// which its timestamp is still acceptable".
//
// This asserts the nonce is still IN the ring rather than merely refused, because the floor
// (see TestTheNonceCapIsNotAReplayWindow) would refuse it either way and would therefore hide a
// retention regression completely. Two overlapping defences are worth having; a test that
// cannot tell which one is working is not.
func TestANonceIsRememberedAsLongAsItsTimestampIsAccepted(t *testing.T) {
	var g nonceGate
	start := time.Now()
	require.True(t, g.fresh("st-1", "seed", start, start))

	// THE WORST CASE, which is the only one worth testing: recorded a moment before a rotation
	// (so it spends the least possible time in the live generation) and stamped a full skew in
	// the FUTURE, by a node whose clock leads the tower's by the most protocol will tolerate.
	wrote := start.Add(nonceRetention - time.Second)
	ts := wrote.Add(protocol.SigMaxSkew)
	require.True(t, g.fresh("st-1", "n", ts, wrote))
	lastAcceptable := ts.Add(protocol.SigMaxSkew)

	// The node keeps polling meanwhile, which is what drives rotation - a gate nobody calls
	// rotates nothing, so a test that jumped straight to the end would prove the wrong thing.
	step := protocol.SigMaxSkew / 4
	for at := start.Add(step); at.Before(lastAcceptable); at = at.Add(step) {
		require.True(t, g.fresh("st-1", "traffic-"+at.String(), at, at))
	}

	g.mu.Lock()
	r := g.rings["st-1"]
	_, inCur := r.cur["n"]
	_, inPrev := r.prev["n"]
	g.mu.Unlock()
	require.True(t, inCur || inPrev,
		"the gate forgot a nonce while protocol.VerifyRequest would still accept its timestamp")
	require.False(t, g.fresh("st-1", "n", ts, lastAcceptable), "and the replay is refused")
}

// THE CAP IS AN OUTSIDER'S LEVER, and the code used to say it was not - "a fleet-management
// problem and not an outsider's lever", needing "hours" to reach at the real poll cadence.
//
// Both halves were wrong. The nonce is recorded when a request AUTHENTICATES, which is before
// the long poll blocks, and ServeLoop's floor on an empty poll cycle is 200ms - so an on-path
// attacker who forwards each poll and answers 204 immediately drives the node's own key at
// about five signatures per second per worker and evicts two full generations in a couple of
// minutes, well inside one skew window. Proved: after 4104 genuine signed polls a poll captured
// before them was accepted and dequeued the job.
//
// The bound is no longer a claim about traffic. Whatever the cap and whatever the rate, an
// evicted era is refused by its timestamp instead of being forgotten.
func TestTheNonceCapIsNotAReplayWindow(t *testing.T) {
	var g nonceGate
	start := time.Now()
	// The request the attacker captured: genuine, signed, and not yet replayed.
	require.True(t, g.fresh("st-1", "captured", start, start))
	// A verbatim replay right now is refused by the cache, which is the ordinary case and the
	// control for the one below - if this passed, the test would be proving nothing.
	require.False(t, g.fresh("st-1", "captured", start, start))

	// Now the attacker drives the node past two full generations, inside the skew window.
	for i := 0; i < 2*maxNoncesPerStation+8; i++ {
		at := start.Add(time.Duration(i) * time.Millisecond)
		require.True(t, g.fresh("st-1", "drive-"+strconv.Itoa(i), at, at))
	}
	// The captured nonce is long gone from both generations - and the replay is still refused,
	// because its timestamp is behind everything this ring has forgotten.
	end := start.Add(3 * time.Second)
	require.False(t, g.fresh("st-1", "captured", start, end),
		"a signed request captured before the cap overflowed became replayable again")
	// While the node's own next request, stamped now, sails through: the floor refuses an era,
	// not a station.
	require.True(t, g.fresh("st-1", "the-node-keeps-working", end, end))
}

// DROPPING A STATION RELEASES ITS MEMORY BUT NOT ITS FLOOR, and the difference is a real hole
// that used to be reachable without an attacker doing anything at all.
//
// The refresher unregisters any Station missing from ONE answer from Core, and unregistering
// used to delete the replay ring outright - so a transient omission followed by a
// re-registration, both inside the five-minute window, made every signature captured before it
// work again. The maps still go (a churning fleet must not accumulate them); what stays is one
// timestamp saying "everything I used to remember is refused on sight".
func TestUnregisteringAStationKeepsItsFloorButNotItsMemory(t *testing.T) {
	node := newTestNode(t)
	s, _ := testServer(t)
	s.RegisterNode("st-1", node.auth())
	captured := time.Now()
	require.True(t, s.nonces.fresh("st-1", "captured", captured, captured))

	// Core's answer omits the Station; the refresher drops it. It comes back on the next one.
	s.UnregisterNode("st-1")
	s.RegisterNode("st-1", node.auth())

	// THE ATTACK: the same captured request, still well inside its timestamp window.
	require.False(t, s.nonces.fresh("st-1", "captured", captured, captured.Add(time.Second)),
		"a registration flap made a captured signature usable again")
	// The node itself is unaffected - it is signing new requests, not re-signing old ones.
	now := captured.Add(2 * time.Second)
	require.True(t, s.nonces.fresh("st-1", "fresh-one", now, now))

	// And the memory really was released rather than kept: the ring holds no entries, and once
	// nothing it could refuse is inside a timestamp window any more, the ring itself goes.
	s.UnregisterNode("st-1")
	s.nonces.mu.Lock()
	r := s.nonces.rings["st-1"]
	entries := len(r.cur) + len(r.prev)
	r.rotated = time.Now().Add(-2 * nonceRetention) // age the tombstone past its usefulness
	s.nonces.mu.Unlock()
	require.Zero(t, entries, "an unregistered Station kept its nonces in memory")
	s.UnregisterNode("st-2") // any forget sweeps the tombstones that have aged out
	s.nonces.mu.Lock()
	_, still := s.nonces.rings["st-1"]
	s.nonces.mu.Unlock()
	require.False(t, still, "a tombstone outlived every timestamp it could have refused")
}

// A SIGNATURE NAMES THE HUB IT WAS MADE FOR. Nothing in protocol.CanonicalRequest does - it
// binds the method, the target, the timestamp and a body digest, and not the host - so the same
// captured bytes used to authenticate at any hub process holding the same Station. Proved by
// presenting one node's real, already-used poll to a second Server; it returned 200 and the job.
func TestASignatureIsGoodAtOneHubOnly(t *testing.T) {
	node := newTestNode(t)
	home, mine := testServer(t)
	home.RegisterNode("st-1", node.auth())
	other, otherSrv := testServerWith(t, ServerOptions{TowerID: "tw-somebody-else",
		SubmitTTL: 3 * time.Second, PollTTL: 300 * time.Millisecond})
	other.RegisterNode("st-1", node.auth())

	// What an on-path observer has: one complete, valid request, signed for MY tower.
	target := hubTarget(testTowerID, PathPoll, url.Values{"station": {"st-1"}})
	captured := node.signedRequest(t, http.MethodGet, otherSrv.URL, target, nil)
	resp, body := do(t, captured())
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"a signature made for one tower authenticated at another")
	require.Contains(t, string(body), "names a different tower")

	// The control: the identical bytes at the hub they were signed for. Without this the test
	// would pass just as well against a signature that was broken for a different reason.
	sameBytes := node.signedRequest(t, http.MethodGet, mine.URL, target, nil)
	ok, _ := do(t, sameBytes())
	require.Equal(t, http.StatusNoContent, ok.StatusCode, "the request still works where it belongs")
}

// A HUB THAT RESTARTS DOES NOT FORGIVE EVERY SIGNATURE IT NEVER SAW. The nonce ring is memory,
// so a redeploy inside the five-minute window used to hand an attacker back every request they
// had captured before it - no attack required beyond waiting for a deploy. The replacement
// process refuses anything stamped before it started.
func TestASignatureFromBeforeARestartIsRefused(t *testing.T) {
	node := newTestNode(t)
	_, srv := testServer(t)

	// Captured a moment before the redeploy - well inside its timestamp window.
	target := hubTarget(testTowerID, PathPoll, url.Values{"station": {"st-1"}})
	stamp := time.Now().Add(-30 * time.Second).Unix()
	sig := ed25519.Sign(node.priv, []byte(protocol.CanonicalRequest(http.MethodGet, target, stamp, nil)))
	replay := func(base string) (*http.Response, []byte) {
		req, _ := http.NewRequest(http.MethodGet, base+target, nil)
		req.Header.Set(protocol.HeaderPubkey, hex.EncodeToString(node.pub))
		req.Header.Set(protocol.HeaderTS, strconv.FormatInt(stamp, 10))
		req.Header.Set(protocol.HeaderSig, hex.EncodeToString(sig))
		return do(t, req)
	}
	_ = srv

	// THE NEW PROCESS: same tower, same Station, same registrations, no memory of anything.
	restarted, restartedSrv := testServer(t)
	restarted.RegisterNode("st-1", node.auth())
	resp, body := replay(restartedSrv.URL)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"a hub restart made every captured signature usable again")
	require.Contains(t, string(body), "already been made")

	// And the node itself keeps serving through the restart, which is the constraint that makes
	// this affordable: it signs a NEW request, and a new request is stamped now.
	ok, _ := node.getSigned(t, restartedSrv.URL, PathPoll, url.Values{"station": {"st-1"}})
	require.Equal(t, http.StatusNoContent, ok.StatusCode)
}

// ONE CANONICAL STRING IS ONE REQUEST. The server rebuilt what the client signed by joining the
// percent-DECODED path to the RAW query, so `/poll?station=st-1&nonce=N` and
// `/poll%3Fstation=st-1&nonce=N` produced the SAME canonical string - one signature, two
// requests. Nothing reads a path segment on today's four routes, so nothing was exploitable;
// the property was simply false, and the next route that takes an id from its path is where
// that stops being a curiosity.
func TestTheCanonicalTargetIsUnambiguous(t *testing.T) {
	node := newTestNode(t)
	s, srv := testServer(t)
	s.RegisterNode("st-1", node.auth())

	// The two requests that used to collide, reconstructed exactly as the server sees them.
	honest, _ := http.NewRequest(http.MethodGet, srv.URL+PathPoll+"?station=st-1&nonce=abc", nil)
	smuggled, _ := http.NewRequest(http.MethodGet, srv.URL+"/poll%3Fstation=st-1&nonce=abc", nil)
	require.NotEqual(t, requestTarget(honest), requestTarget(smuggled),
		"two different requests produce one identical canonical string, so one signature covers both")
}

// THE SIGNATURE COVERS THE BODY ON EVERY ROUTE, not on the half of them that have one by
// convention. The GET handlers passed nil to authNode regardless of what arrived, so a signed
// poll would be accepted with any unsigned payload attached to it. Nothing reads that payload
// today. "The signature covers the body" was still only half true, and the reason to fix it is
// that the sentence is what the next person will rely on.
func TestASignedGETCannotCarryAnUnsignedBody(t *testing.T) {
	node := newTestNode(t)
	s, srv := testServer(t)
	s.RegisterNode("st-1", node.auth())

	target := hubTarget(testTowerID, PathPoll, url.Values{"station": {"st-1"}})
	pub, ts, sig := node.signer()(http.MethodGet, target, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+target, bytes.NewReader([]byte("smuggled")))
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	req.Header.Set(protocol.HeaderSig, sig)
	resp, _ := do(t, req)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"a signed GET was accepted carrying a body its signature does not cover")

	// The ordinary bodyless poll is untouched: an empty read and a nil body hash identically.
	ok, _ := node.getSigned(t, srv.URL, PathPoll, url.Values{"station": {"st-1"}})
	require.Equal(t, http.StatusNoContent, ok.StatusCode)
}

// A STRANGER DOES NOT GET TO FILL THIS TOWER'S MEMORY BEFORE BEING TOLD NO.
//
// /complete and /audit/transcript must read the whole body before they can authenticate it -
// the signature covers a digest of the bytes that arrived, so verifying a re-serialization
// would verify the wrong thing. That ordering is right and stays. What it meant is that anyone
// at all could make this tower buffer sixteen megabytes, on a listener with no connection cap
// and a two-minute read timeout, and only then be refused. Proved with 8,388,608 wasted bytes.
//
// The request here ANNOUNCES a body and then sends none of it, which is what makes the answer
// unambiguous rather than a matter of counting: a hub that reads before it authenticates cannot
// answer this at all, and one that refuses on the headers answers it at once. It is spoken over
// a raw connection because Go's own client will not surrender an early response while it still
// has a body to write - the thing being tested is precisely what happens before that.
func TestAnUnauthenticatedCallerIsRefusedBeforeItsBodyIsRead(t *testing.T) {
	node := newTestNode(t)
	s, srv := testServer(t)
	s.RegisterNode("st-1", node.auth())

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	// Sixteen megabytes promised, nothing sent.
	_, err = conn.Write([]byte("POST " + PathComplete + " HTTP/1.1\r\nHost: hub\r\n" +
		"Content-Type: application/json\r\nContent-Length: 16777216\r\n\r\n"))
	require.NoError(t, err)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
	status, rerr := bufio.NewReader(conn).ReadString('\n')
	require.NoError(t, rerr, "the hub was still waiting for an unauthenticated body three seconds in")
	require.Contains(t, status, "401", "the hub answered %q rather than refusing on the headers", status)

	// And the node's own completion, which presents a credential this tower knows, is read and
	// served exactly as before - the gate is an admission check, not the authorization.
	resp, _ := node.postSigned(t, srv.URL, PathComplete, completeReq{AttemptID: "att-x", StationID: "st-1",
		Envelope: base64.StdEncoding.EncodeToString([]byte("answer"))})
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
