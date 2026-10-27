package towerhub

// coldbranches_test.go pins the branches of this package that were deterministic and simply
// never driven: refusals that sit one line past a test's happy path, doors that nothing had
// knocked on wrong, and the bookkeeping that follows a Station out of the hub. Each one is a
// behaviour a caller can observe, asserted as that behaviour; none of them depends on timing.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/towercore/envelope"
)

// --- the client's epoch memory ------------------------------------------------------------

// adoptEpoch ignores an empty epoch and remembers a bounded number of retired ones: the oldest
// falls off, so a hub that legitimately cycles through many redeploys is not eventually mistaken
// for two live processes, while the most recent retired epochs still are.
func TestAdoptEpochIgnoresNothingAndBoundsItsMemory(t *testing.T) {
	c := &Client{}
	require.NoError(t, c.adoptEpoch(""))
	require.Empty(t, c.hubEpoch(), "an empty epoch is no epoch: nothing adopted, nothing retired")

	for i := 0; i <= maxRetiredEpochs+3; i++ {
		require.NoError(t, c.adoptEpoch(fmt.Sprintf("e%d", i)))
	}
	require.Len(t, c.retired, maxRetiredEpochs, "the retired list is bounded")
	require.NoError(t, c.adoptEpoch("e0"), "the oldest epoch fell off the list and may be seen again")
	require.ErrorIs(t, c.adoptEpoch(fmt.Sprintf("e%d", maxRetiredEpochs+2)), ErrHubMultipleProcesses,
		"a recently retired epoch coming back is still the two-process verdict")
}

// epochFrom refuses every broken proof: a node that was never handed Core's fingerprint, a
// proof that is not hex, and a well-formed proof made for a different request's nonce - the
// stockpiled proof the nonce exists to defeat. The honest proof, for contrast, is adopted.
func TestEpochFromRefusesEveryBrokenProof(t *testing.T) {
	node := newTestNode(t)
	pubHex := hex.EncodeToString(testHubKey.Public().(ed25519.PublicKey))
	answer := func(epoch, keyHex, proofHex string) *http.Response {
		h := http.Header{}
		h.Set(HubEpochHeader, epoch)
		h.Set(HubKeyHeader, keyHex)
		h.Set(HubProofHeader, proofHex)
		return &http.Response{Header: h}
	}

	c := &Client{TowerID: testTowerID, Sign: node.signer()}
	_, err := c.epochFrom(answer("e2", pubHex, ""), "", "n")
	require.ErrorIs(t, err, ErrHubEpochUnproved, "without a fingerprint from Core there is nothing to prove against")

	c.TowerKeyHash = testHubKeyHash()
	_, err = c.epochFrom(answer("e2", pubHex, "not-hex"), "", "n")
	require.ErrorIs(t, err, ErrHubEpochUnproved, "a proof that is not a signature proves nothing")

	stale := ed25519.Sign(testHubKey, hubEpochStatement(testTowerID, "e2", "some-earlier-nonce"))
	_, err = c.epochFrom(answer("e2", pubHex, hex.EncodeToString(stale)), "", "n")
	require.ErrorIs(t, err, ErrHubEpochUnproved, "a proof for another request is a stockpiled proof")

	good := ed25519.Sign(testHubKey, hubEpochStatement(testTowerID, "e2", "n"))
	got, err := c.epochFrom(answer("e2", pubHex, hex.EncodeToString(good)), "", "n")
	require.NoError(t, err)
	require.Equal(t, "e2", got)
}

// A 401 THAT CARRIES NO NEWS IS HANDED BACK AS ITSELF. A node polling a Station this hub has
// never registered learns the epoch off the first refusal, re-signs, is refused again for the
// same reason - and that second answer names the epoch it already sent, so there is nothing to
// learn and the refusal reaches the caller as the HTTPError it is. A consumer-shaped client
// (no signer) polling is refused the same way, without ever trying to prove an epoch.
func TestAnOrdinaryRefusalIsHandedBackAsItself(t *testing.T) {
	id := newTestNode(t)
	_, base := serveTestRig(t) // nothing registered
	node := id.client(base, 2*time.Second)
	_, _, err := node.PollJob(t.Context(), "st-ghost")
	var he *HTTPError
	require.ErrorAs(t, err, &he)
	require.Equal(t, http.StatusUnauthorized, he.Status)
	require.Contains(t, he.Body, "no node is registered")
	require.NotEmpty(t, node.hubEpoch(), "the epoch was learned off the first refusal; the second was about the Station")

	consumer := &Client{BaseURL: base, TowerID: testTowerID}
	_, _, err = consumer.PollJob(t.Context(), "st-ghost")
	require.ErrorAs(t, err, &he)
	require.Equal(t, http.StatusUnauthorized, he.Status)
	require.Empty(t, consumer.hubEpoch(), "a client that signs nothing adopts no epoch")
}

// THREE LIVE PROCESSES ARE CAUGHT ON THE RETRY TOO. The first call learns epoch A and, on its
// one retry, B. The second call is answered with a fresh C, retries, and is answered with A -
// an epoch this node already retired. The second answer is learned from (client.go), and
// learning it is where the two-process verdict lands; the call stops there rather than looping.
func TestAThirdLiveProcessIsCaughtOnTheRetryToo(t *testing.T) {
	node := newTestNode(t)
	pubHex := hex.EncodeToString(testHubKey.Public().(ed25519.PublicKey))
	script := []string{"ep-a", "ep-b", "ep-c", "ep-a"}
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(n.Add(1)) - 1
		if i >= len(script) {
			w.WriteHeader(http.StatusInternalServerError) // the client retried more than once
			return
		}
		epoch, nonce := script[i], r.URL.Query().Get(nonceParam)
		w.Header().Set(HubEpochHeader, epoch)
		w.Header().Set(HubKeyHeader, pubHex)
		w.Header().Set(HubProofHeader, hex.EncodeToString(
			ed25519.Sign(testHubKey, hubEpochStatement(testTowerID, epoch, nonce))))
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	c := node.client(srv.URL, 5*time.Second)
	_, _, err := c.PollJob(t.Context(), "st-1")
	var he *HTTPError
	require.ErrorAs(t, err, &he, "A then B: both learned, the second refusal handed back")
	require.Equal(t, "ep-b", c.hubEpoch())

	_, _, err = c.PollJob(t.Context(), "st-1")
	require.ErrorIs(t, err, ErrHubMultipleProcesses,
		"the retry was answered with an epoch this node had already retired")
	require.Equal(t, int64(4), n.Load(), "one retry per call, never a third attempt")
}

// A base URL that cannot become a request fails before the wire, on every node-side and
// consumer-side call; one that names a dead port fails at the wire. Neither is dressed up.
func TestAnUnusableBaseURLFailsBeforeOrAtTheWire(t *testing.T) {
	id := newTestNode(t)
	ctx := t.Context()
	broken := id.client("http://[::1", time.Second) // not a URL at all
	_, _, err := broken.PollJob(ctx, "st-1")
	require.Error(t, err)
	_, err = broken.SubmitJob(ctx, []byte("att-1|st-1"), []byte("x"))
	require.Error(t, err)
	_, err = broken.AuditWanted(ctx, "st-1")
	require.Error(t, err)
	require.Error(t, broken.AnswerAudit(ctx, "st-1", TranscriptReply{AttemptID: "att-1"}))

	dead := id.client("http://127.0.0.1:1", time.Second)
	_, err = dead.SubmitJob(ctx, []byte("att-1|st-1"), []byte("x"))
	require.Error(t, err)
	var he *HTTPError
	require.False(t, errors.As(err, &he), "a connection refused is not a hub answer")
}

// The audit plane's client refuses a non-200 the same way the job plane's does: a stranger
// asking for a Station's wanted list, or answering for it, gets the hub's 401 as an HTTPError.
// A REGISTERED node answering for a Station that is not its own is refused one door further
// in - its key opened the body read, and the signature check then names the mismatch.
func TestTheAuditClientSurfacesTheHubsRefusal(t *testing.T) {
	s, srv, _ := auditServer(t)
	stranger := newTestNode(t).client(srv.URL, 2*time.Second)
	var he *HTTPError
	_, err := stranger.AuditWanted(t.Context(), "st1")
	require.ErrorAs(t, err, &he)
	require.Equal(t, http.StatusUnauthorized, he.Status)
	err = stranger.AnswerAudit(t.Context(), "st1", TranscriptReply{AttemptID: "att-1"})
	require.ErrorAs(t, err, &he)
	require.Equal(t, http.StatusUnauthorized, he.Status)

	other := newTestNode(t)
	s.RegisterNode("st2", other.auth())
	err = other.client(srv.URL, 2*time.Second).AnswerAudit(t.Context(), "st1", TranscriptReply{AttemptID: "att-1"})
	require.ErrorAs(t, err, &he)
	require.Equal(t, http.StatusUnauthorized, he.Status)
	require.Contains(t, he.Body, "not this Station's attached assertion key")
}

// A hub that answers the wanted list with something that is not a list is refused as
// unreadable rather than read as empty: an empty answer would quietly mean "nothing wanted".
func TestAnUnreadableWantedListIsAnErrorNotAnEmptyList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("this is not json"))
	}))
	t.Cleanup(srv.Close)
	_, err := newTestNode(t).client(srv.URL, 2*time.Second).AuditWanted(t.Context(), "st1")
	require.ErrorContains(t, err, "unreadable wanted response")
}

// --- the hub's dispatch records ------------------------------------------------------------

// A dispatch record answers only its own Station and goes with it: a probe from another Station
// neither consumes nor confirms, the owner then still collects, and an unregistered Station's
// records are dropped rather than left for a re-registration to find.
func TestDispatchRecordsBelongToTheirStationAndLeaveWithIt(t *testing.T) {
	h := New()
	h.Register("")
	require.Empty(t, h.stations, "an empty Station id registers nothing")

	h.Register("st-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, att := range []string{"att-1", "att-2"} {
		go func() { _, _ = h.Submit(ctx, Job{AttemptID: att, StationID: "st-1"}) }()
		job, ok := h.Poll(ctx, "st-1")
		require.True(t, ok)
		require.Equal(t, att, job.AttemptID)
	}

	carried, _ := h.ConsumeDispatched("att-1", "st-2")
	require.False(t, carried, "another Station's probe must not confirm the hand-off")
	carried, _ = h.ConsumeDispatched("att-1", "st-1")
	require.True(t, carried, "and must not have consumed it either")

	h.Unregister("st-1")
	carried, _ = h.ConsumeDispatched("att-2", "st-1")
	require.False(t, carried, "an unregistered Station's dispatch records go with it")
}

// An EXPIRED dispatch record is dead on both paths that meet it: a completion arriving after
// the TTL is not couriered (and the record is dropped on the spot), and the next hand-off on a
// busy hub sweeps the rest. The TTL is a constant, so the records are aged by hand.
func TestAnExpiredDispatchRecordIsDroppedWhereverItIsMet(t *testing.T) {
	h := New()
	h.Register("st-1")
	stale := time.Now().Add(-time.Second)
	h.mu.Lock()
	h.dispatched["att-old"] = dispatchRecord{station: "st-1", reqBytes: 7, expires: stale}
	h.dispatched["att-older"] = dispatchRecord{station: "st-1", reqBytes: 7, expires: stale}
	h.mu.Unlock()

	carried, wireIn := h.ConsumeDispatched("att-old", "st-1")
	require.False(t, carried, "a completion past the TTL is not couriered")
	require.Zero(t, wireIn)
	h.mu.Lock()
	_, still := h.dispatched["att-old"]
	h.mu.Unlock()
	require.False(t, still, "an expired record met by a completion is deleted on the spot")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _, _ = h.Submit(ctx, Job{AttemptID: "att-new", StationID: "st-1"}) }()
	_, ok := h.Poll(ctx, "st-1")
	require.True(t, ok)
	h.mu.Lock()
	_, still = h.dispatched["att-older"]
	_, fresh := h.dispatched["att-new"]
	h.mu.Unlock()
	require.False(t, still, "the hand-off sweeps the expired records")
	require.True(t, fresh, "and records the new one")
}

// --- the server's small doors --------------------------------------------------------------

// The helpers behind every node-facing answer behave on an empty server: a GET with no body
// reads as nothing, a hub with no epoch stamps nothing, one with an epoch but no key stamps the
// epoch alone, and a hub with no key has no fingerprint to hand out.
func TestTheServersSmallDoorsOnAnEmptyServer(t *testing.T) {
	body, err := readGETBody(httptest.NewRecorder(), &http.Request{})
	require.NoError(t, err)
	require.Nil(t, body)

	rec := httptest.NewRecorder()
	(&Server{}).stampEpoch(rec, httptest.NewRequest(http.MethodGet, PathPoll, nil))
	require.Empty(t, rec.Header().Get(HubEpochHeader), "no epoch, no stamp")

	rec = httptest.NewRecorder()
	(&Server{epoch: "e1"}).stampEpoch(rec, httptest.NewRequest(http.MethodGet, PathPoll, nil))
	require.Equal(t, "e1", rec.Header().Get(HubEpochHeader))
	require.Empty(t, rec.Header().Get(HubProofHeader), "an epoch with no key to sign it is unproved")
	require.Empty(t, (&Server{}).EpochKeyHash())
}

// Registering a Station with no credential at all indexes nothing, and unregistering it leaves
// the credential index exactly as it was - the index tracks credentials, not Stations.
func TestACredentiallessRegistrationIndexesNothing(t *testing.T) {
	s, _ := testServer(t)
	s.RegisterNode("st-bare", NodeAuth{})
	s.mu.RLock()
	_, indexed := s.indexed["st-bare"]
	s.mu.RUnlock()
	require.False(t, indexed)
	s.UnregisterNode("st-bare")
	_, known := s.nodes["st-bare"]
	require.False(t, known)
}

// A body the door admitted but the handler cannot finish reading is a 400 that names the
// read, on both routes that read a body before authenticating it.
func TestABodyThatDiesMidReadIsRefusedAsUnreadable(t *testing.T) {
	id := newTestNode(t)
	s, srv := testServer(t)
	s.RegisterNode("st-1", id.auth())
	for path, handler := range map[string]http.HandlerFunc{
		PathComplete: s.Complete, PathAuditTranscript: s.AuditTranscript,
	} {
		target := hubTarget(testTowerID, s.Epoch(), path, url.Values{})
		req := id.signedRequest(t, http.MethodPost, srv.URL, target, []byte(`{}`))()
		req.Body = io.NopCloser(iotest.ErrReader(errors.New("the body died mid-read")))
		rec := httptest.NewRecorder()
		handler(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code, path)
		require.Contains(t, rec.Body.String(), "unreadable request body", path)
	}

	// And a body that reads fine but is not JSON is refused as that, on the audit route too.
	target := hubTarget(testTowerID, s.Epoch(), PathAuditTranscript, url.Values{})
	resp, raw := do(t, id.signedRequest(t, http.MethodPost, srv.URL, target, []byte("not json"))())
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, string(raw), "invalid JSON body")
}

// --- the node door, knocked on wrong -------------------------------------------------------

// validNonce is shape only, and the shape is exact.
func TestValidNonceIsExactAboutShape(t *testing.T) {
	require.False(t, validNonce("ab"), "too short")
	require.False(t, validNonce(strings.Repeat("a", 65)), "too long")
	require.False(t, validNonce(strings.Repeat("z", 32)), "not hex")
	require.True(t, validNonce(strings.Repeat("a", 32)))
}

// A signed request whose timestamp is not a number, and one whose nonce is not a nonce, are
// each refused with the sentence for that fault - after the signature itself was checked, so
// neither refusal costs the nonce cache an entry.
func TestASignedRequestWithABrokenClockOrNonceIsRefusedByName(t *testing.T) {
	id := newTestNode(t)
	s, srv := testServer(t)
	s.RegisterNode("st-1", id.auth())

	target := hubTarget(testTowerID, s.Epoch(), PathPoll, url.Values{"station": {"st-1"}})
	req := id.signedRequest(t, http.MethodGet, srv.URL, target, nil)()
	req.Header.Set(protocol.HeaderTS, "yesterday")
	resp, raw := do(t, req)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Contains(t, string(raw), "not a unix second count")

	q := url.Values{"station": {"st-1"}, nonceParam: {"zz"}, towerParam: {testTowerID}, hubParam: {s.Epoch()}}
	resp, raw = do(t, id.signedRequest(t, http.MethodGet, srv.URL, PathPoll+"?"+q.Encode(), nil)())
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Contains(t, string(raw), "-byte nonce", "a signature over a malformed nonce is still refused")
}

// The transition bearer path refuses by name too: a token-registered Station polled with no
// token at all, and with the wrong one.
func TestTheBearerDoorRefusesNoTokenAndTheWrongToken(t *testing.T) {
	s, srv := testServer(t)
	s.RegisterNode("st-tok", NodeAuth{LegacyToken: "the-real-token"})
	get := func(token string) (*http.Response, []byte) {
		req, err := http.NewRequest(http.MethodGet, srv.URL+PathPoll+"?station=st-tok", nil)
		require.NoError(t, err)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return do(t, req)
	}
	resp, raw := get("")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Contains(t, string(raw), "neither a signature nor a token")
	resp, raw = get("the-wrong-token")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Contains(t, string(raw), "not the registered node")
}

// --- pins and reaching ---------------------------------------------------------------------

// The pin helpers refuse what they cannot pin: no certificate, no endpoint, a malformed pin
// on the Reach path, and a handshake that presents no certificate or an unparseable one.
func TestPinHelpersRefuseWhatTheyCannotPin(t *testing.T) {
	require.Empty(t, CertPin(nil))
	_, err := HubURL("", "")
	require.ErrorContains(t, err, "no hub endpoint")
	_, _, err = Reach("hub.example:1", "zz", nil)
	require.Error(t, err, "a malformed pin is refused on Reach, not deferred to the handshake")

	cfg, err := PinnedTLSConfig(strings.Repeat("ab", 32))
	require.NoError(t, err)
	require.ErrorIs(t, cfg.VerifyPeerCertificate(nil, nil), ErrHubCertificateUnpinned)
	require.ErrorIs(t, cfg.VerifyPeerCertificate([][]byte{{0x30, 0x00}}, nil), ErrHubCertificateUnpinned)
}

// The vetted dialer dials the ADDRESS it vetted: a live loopback listener is reached through a
// vet that allows it, an address with no port never reaches the resolver, and a port nothing
// listens on surfaces the dial's refusal rather than a vet verdict.
func TestTheVettedDialerDialsTheAddressItVetted(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			_ = c.Close()
		}
	}()

	_, hc, err := ReachVetted("localhost:9", "", func(net.IP) error { return nil })
	require.NoError(t, err)
	dial := hc.Transport.(*http.Transport).DialContext
	ctx := t.Context()

	c, err := dial(ctx, "tcp", ln.Addr().String())
	require.NoError(t, err)
	_ = c.Close()

	_, err = dial(ctx, "tcp", "127.0.0.1")
	require.Error(t, err, "no port is a malformed address, refused before any lookup")

	_, err = dial(ctx, "tcp", "127.0.0.1:1")
	require.Error(t, err, "a vetted address that refuses the connection surfaces that refusal")
	require.NotContains(t, err.Error(), "refusing to dial", "the vet allowed it; the socket did not")
}

// --- the audit answer loop -----------------------------------------------------------------

// AnswerAudits takes the default cadence for a non-positive one and leaves at once on a done
// context; and a hub that refuses an answer is reported and the loop lives on to retry.
func TestAnswerAuditsDefaultsItsCadenceAndReportsARefusedAnswer(t *testing.T) {
	corePub, _, err := envelope.NewKey()
	require.NoError(t, err)

	done, cancel := context.WithCancel(context.Background())
	cancel()
	var got []error
	AnswerAudits(done, nil, "st1", fakeTranscripts{}, corePub, 0, func(e error) { got = append(got, e) })
	require.Empty(t, got, "a loop that never ticked reported nothing")

	s, srv, id := auditServer(t)
	s.SetWanted("st1", []string{""}) // an attempt with no name: the hub refuses the answer
	errs := make(chan error, 4)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go AnswerAudits(ctx, id.client(srv.URL, 0), "st1", fakeTranscripts{}, corePub, 10*time.Millisecond,
		func(e error) {
			select {
			case errs <- e:
			default:
			}
		})
	select {
	case e := <-errs:
		var he *HTTPError
		require.ErrorAs(t, e, &he, "the hub's refusal of the answer is what gets reported")
		require.Equal(t, http.StatusBadRequest, he.Status)
	case <-time.After(5 * time.Second):
		t.Fatal("the refused answer was never reported")
	}
}

// A TRANSCRIPT THAT CANNOT BE SEALED IS NOT SENT. A Core envelope key of the right length that
// is not a usable X25519 point (the all-zero low-order point) fails key agreement; the loop
// reports the seal failure and answers nothing for that attempt rather than relaying the
// transcript unsealed or lying that it was not retained.
func TestATranscriptThatCannotBeSealedIsReportedNotRelayed(t *testing.T) {
	s, srv, id := auditServer(t)
	s.SetWanted("st1", []string{"att-1"})
	forwarded := make(chan TranscriptReply, 1)
	s.OnTranscript = func(_ string, r TranscriptReply) { forwarded <- r }
	errs := make(chan error, 4)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	lowOrder := make([]byte, 32)
	go AnswerAudits(ctx, id.client(srv.URL, 0), "st1",
		fakeTranscripts{"att-1": {[]byte("signed"), []byte("req"), []byte("resp")}}, lowOrder,
		10*time.Millisecond, func(e error) {
			select {
			case errs <- e:
			default:
			}
		})
	select {
	case e := <-errs:
		require.ErrorContains(t, e, "key agreement")
	case <-time.After(5 * time.Second):
		t.Fatal("the seal failure was never reported")
	}
	select {
	case r := <-forwarded:
		t.Fatalf("an unsealable transcript was still answered: %+v", r)
	case <-time.After(50 * time.Millisecond):
	}
}
