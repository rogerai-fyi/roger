package towerhub

// nodeauth_test.go proves the property signed polls exist for: that nothing a node puts on the
// plaintext hub link can be picked up by someone else and used to take that node's work.
//
// Each test below fails against the bearer-token hub these replaced. That is the bar - not
// "signatures are computed somewhere" but "the specific attack no longer works".

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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

func (n *testNode) signer() Signer { return SignWith(n.priv) }
func (n *testNode) auth() NodeAuth { return NodeAuth{AssertionKey: n.pub} }
func (n *testNode) client(base string, timeout time.Duration) *Client {
	return &Client{BaseURL: base, Sign: n.signer(), HTTP: &http.Client{Timeout: timeout}}
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
	target := hubTarget(path, q)
	return do(t, n.signedRequest(t, http.MethodGet, base, target, nil)())
}

// postSigned is the ordinary one-shot POST. The body it signs is the body it sends, byte for
// byte - the server verifies the digest of what arrived, not of a re-encode.
func (n *testNode) postSigned(t *testing.T, base, path string, body any) (*http.Response, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	target := hubTarget(path, url.Values{})
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
	target := hubTarget(PathPoll, url.Values{"station": {"st-1"}})
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
	s := NewServer(New(), stubCheck, 3*time.Second, 200*time.Millisecond)
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
// this change keeps earning while it updates - and the moment that window closes, a stolen token
// buys nothing at all.
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

	// After it: the token is not a credential any more, and the signing node is unaffected.
	s.AllowLegacyBearer = false
	req, _ = http.NewRequest(http.MethodGet, srv.URL+PathPoll+"?station=st-1", nil)
	req.Header.Set("Authorization", "Bearer stolen")
	resp, body := do(t, req)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "the stolen token still opened the queue")
	require.Contains(t, string(body), "signed request")

	ok, _ := node.getSigned(t, srv.URL, PathPoll, url.Values{"station": {"st-1"}})
	require.Equal(t, http.StatusNoContent, ok.StatusCode, "the signing node kept serving")
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

	target := hubTarget(PathPoll, url.Values{"station": {"st-1"}})
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
	target := hubTarget(PathComplete, url.Values{})
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
		require.True(t, g.fresh("st-1", strconv.Itoa(i), now), "a fresh nonce was called a replay")
	}
	g.mu.Lock()
	r := g.rings["st-1"]
	total := len(r.cur) + len(r.prev)
	g.mu.Unlock()
	require.LessOrEqual(t, total, 2*maxNoncesPerStation,
		"the replay cache grows without bound: %d entries", total)

	// And within a generation it still does its job.
	require.True(t, g.fresh("st-2", "n", now))
	require.False(t, g.fresh("st-2", "n", now), "a repeat inside the window was not caught")
	// Stations do not share a namespace, so one node cannot evict or collide with another's.
	require.True(t, g.fresh("st-3", "n", now))
}

// Nonces are remembered for at least as long as a timestamp stays acceptable. Rotation on age is
// what makes that true without a per-entry sweeper, and rotating one generation too eagerly
// would reopen the replay window silently.
func TestANonceIsRememberedAcrossOneRotation(t *testing.T) {
	var g nonceGate
	start := time.Now()
	require.True(t, g.fresh("st-1", "n", start))
	// One rotation later it is in the previous generation, and still refused.
	require.False(t, g.fresh("st-1", "n", start.Add(protocol.SigMaxSkew+time.Second)))
	// Two rotations later the timestamp check has long since taken over.
	require.True(t, g.fresh("st-1", "n", start.Add(3*protocol.SigMaxSkew)))
}

// Dropping a Station drops its replay memory with it, so a tower serving a churning fleet does
// not accumulate rings for stations that left.
func TestUnregisteringAStationForgetsItsNonces(t *testing.T) {
	node := newTestNode(t)
	s, _ := testServer(t)
	s.RegisterNode("st-1", node.auth())
	require.True(t, s.nonces.fresh("st-1", "n", time.Now()))
	s.UnregisterNode("st-1")
	s.nonces.mu.Lock()
	_, still := s.nonces.rings["st-1"]
	s.nonces.mu.Unlock()
	require.False(t, still, "an unregistered Station kept its replay ring")
}
