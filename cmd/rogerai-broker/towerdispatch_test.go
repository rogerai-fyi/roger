package main

// towerdispatch_test.go drives a request all the way to a Station and back.
//
// Everything in this file is REAL except the model: a real grant signed by the broker's own
// derived key, a real Station holding its own assertion key and running the real Executor, a
// real receipt, and the real verification path. Only the thing at the far end that would
// otherwise need a GPU is a stub.
//
// That matters because every individual check here can be made to pass by a component that
// checks nothing. Only running the two halves against each other shows they agree.

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/admit"
	"rogerai.fm/roger/v6/internal/towercore/attach"
	"rogerai.fm/roger/v6/internal/towercore/attempt"
	"rogerai.fm/roger/v6/internal/towercore/cert"
	"rogerai.fm/roger/v6/internal/towercore/dispatch"
	"rogerai.fm/roger/v6/internal/towercore/enroll"
	"rogerai.fm/roger/v6/internal/towercore/envelope"
	"rogerai.fm/roger/v6/internal/towercore/fleet"
	"rogerai.fm/roger/v6/internal/towercore/head"
	"rogerai.fm/roger/v6/internal/towercore/link"
)

func TestCorePublishesItsGrantKey(t *testing.T) {
	b, srv := towerTestBroker(t)
	resp, err := http.Get(srv.URL + "/tower/dispatch/key")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out struct {
		Network     string `json:"network"`
		DispatchKey string `json:"dispatch_key"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Equal(t, link.PublicNetwork, out.Network)
	raw, err := hex.DecodeString(out.DispatchKey)
	require.NoError(t, err)
	require.Equal(t, []byte(b.tower.dispatchPub), raw, "the published key must be the signing one")
}

// THE DERIVED KEY IS STABLE. A Station pins it, so a broker restart that changed it would
// silently break every Station on the network - and the failure would look like tampering.
func TestTheGrantKeyIsStableAcrossRestarts(t *testing.T) {
	b, _ := towerTestBroker(t)
	first := b.tower.dispatchPub

	again, err := deriveDispatchKey(b.tower.ca)
	require.NoError(t, err)
	require.Equal(t, []byte(first), []byte(again.Public().(ed25519.PublicKey)))

	// And it is a DIFFERENT KEY from the CA root, and a different algorithm: the root signs
	// certificates with ECDSA, grants are Ed25519, and deriving is what keeps a mistake in
	// one use from changing what the other means.
	_, isECDSA := b.tower.ca.RootKey().(*ecdsa.PrivateKey)
	require.True(t, isECDSA, "the CA root signs certificates, not grants")
}

func postResult(t *testing.T, srv *httptest.Server, lt linkTower, payload map[string]any) (int, string) {
	t.Helper()
	return lt.call(t, srv, "/tower/dispatch/result", jsonOf(t, payload), nil)
}

func shortPolls(t *testing.T) {
	t.Helper()
	was := dispatchPollWait
	dispatchPollWait = 150 * time.Millisecond
	t.Cleanup(func() { dispatchPollWait = was })
}

// --- two brokers ------------------------------------------------------------
//
// Production runs more than one, and everything below is a property that was silently FALSE
// while the attempt table lived in each process: a Tower reaches whichever instance the load
// balancer chose, and that is very often not the one holding its work.

// twoBrokers builds two instances over ONE set of stores - which is what a real fleet is.
// The CA custody is shared too, so both derive the same grant key and a Station pinning it
// can verify a grant whichever broker issued it.
func twoBrokers(t *testing.T) (*broker, *httptest.Server, *broker, *httptest.Server) {
	t.Helper()
	registry, custody, enrollment := admit.NewMemStore(), cert.NewMemCustody(), enroll.NewMemStore()
	stations, heads, attempts := attach.NewMemStore(), head.NewMemStore(), dispatch.NewMemStore()
	routable := fleet.NewMemStore()
	shared := store.NewMem()

	build := func() (*broker, *httptest.Server) {
		b := testBrokerWithDB(shared)
		ts, err := newTowerSubsystem(b, registry, custody, enrollment, cert.Config{TTL: time.Hour},
			linkDeps{stations: stations, heads: heads, attempts: attempts, routable: routable})
		require.NoError(t, err)
		b.tower = ts
		mux := http.NewServeMux()
		b.registerTowerRoutes(mux)
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		return b, srv
	}
	a, aSrv := build()
	c, cSrv := build()
	return a, aSrv, c, cSrv
}

func twoBrokersOnBus(t *testing.T, mr *miniredis.Miniredis) (*broker, *httptest.Server, *broker, *httptest.Server) {
	t.Helper()
	a, aSrv, c, cSrv := twoBrokers(t)
	for _, b := range []*broker{a, c} {
		vs, err := newValkeyStore("redis://" + mr.Addr())
		require.NoError(t, err)
		t.Cleanup(func() { _ = vs.Close() })
		b.shared, b.multiInstance = vs, true
	}
	return a, aSrv, c, cSrv
}

// --- the shared refusals ----------------------------------------------------
//
// Both dispatch routes carry the same preamble, copied per handler - which is exactly the
// kind of check that goes missing from one of them. Asserted per route rather than once.

type brokenAttemptStore struct{ dispatch.Store }

func (brokenAttemptStore) ClaimNext(string, time.Time) (dispatch.Record, bool, error) {
	return dispatch.Record{}, false, errors.New("the attempt store is unreachable")
}

func towerAttemptLifetimeForTest(t *testing.T, d time.Duration) func() {
	t.Helper()
	was := towerAttemptLifetime
	towerAttemptLifetime = d
	return func() { towerAttemptLifetime = was }
}

// --- the attempt ledger, driven by real dispatch ----------------------------
//
// The ledger is the record money will be decided from, so what matters is not that it can
// hold a chain but that the REAL dispatch path writes the right one. These drive actual
// requests and then read the history back.

type brokenAttemptChain struct{ attempt.Store }

func (brokenAttemptChain) Append(attempt.Record, int64) error {
	return errors.New("the attempt ledger is unreachable")
}
func (brokenAttemptChain) Head(string) (attempt.Record, bool, error) {
	return attempt.Record{}, false, errors.New("the attempt ledger is unreachable")
}

// The attempt-state signer is a DIFFERENT key from the dispatch signer. A compromise of the
// one that signs authorizations must not be able to forge the record money is decided from.
func TestTheAttemptSignerIsNotTheGrantSigner(t *testing.T) {
	b, _ := towerTestBroker(t)
	attemptPub := b.tower.attemptKey.Public().(ed25519.PublicKey)
	require.NotEqual(t, []byte(b.tower.dispatchPub), []byte(attemptPub),
		"attempt state and dispatch authorization must not share a key")

	// Both are still stable across restarts, which is what makes a chain verifiable later.
	again, err := deriveAttemptKey(b.tower.ca)
	require.NoError(t, err)
	require.Equal(t, []byte(attemptPub), []byte(again.Public().(ed25519.PublicKey)))
}

func TestCorePublishesBothPinnedKeys(t *testing.T) {
	b, srv := towerTestBroker(t)
	resp, err := http.Get(srv.URL + "/tower/dispatch/key")
	require.NoError(t, err)
	defer resp.Body.Close()

	var out struct {
		DispatchKey string `json:"dispatch_key"`
		EnvelopeKey string `json:"envelope_key"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))

	grantKey, err := hex.DecodeString(out.DispatchKey)
	require.NoError(t, err)
	require.Equal(t, []byte(b.tower.dispatchPub), grantKey)

	envKey, err := hex.DecodeString(out.EnvelopeKey)
	require.NoError(t, err)
	require.Equal(t, b.tower.envelopePub, envKey)
	require.NotEqual(t, grantKey, envKey, "signing and receiving are different keys")

	// The envelope key is STABLE across restarts, like the others - a Station pins it, and a
	// key that moved would strand every Station on the network.
	again, err := deriveEnvelopeKey(b.tower.ca)
	require.NoError(t, err)
	againPub, err := envelope.PublicKeyOf(again)
	require.NoError(t, err)
	require.Equal(t, b.tower.envelopePub, againPub)
}

// A BROKER CAN AUTHORIZE ONTO A TOWER LINKED ELSEWHERE (the edge-path successor of the
// deleted Topology-1 cross-broker test): the tower holds ONE link, to ONE broker, and the
// fleet projection is what lets every other instance still route consumers to its hub.
func TestABrokerAuthorizesOntoATowerLinkedElsewhere(t *testing.T) {
	a, aSrv, c, cSrv := twoBrokers(t)

	// The tower links to broker A; the self-attached node's row lands in the SHARED
	// projection with A's advertised endpoint.
	tw := liveEdgeTower(t, a, aSrv, "xb-tower-op", "203.0.113.5:8444")
	node := signedInOperator(t, a, "xb-node-op")
	body, _ := selfAttachBodyFor(t, a, node)
	body["model"], body["modality"], body["price_out_micros"] = "xb-model", "chat", 250_000
	var attached map[string]any
	code, raw := node.attach(t, aSrv, body, &attached)
	require.Equal(t, http.StatusOK, code, raw)

	// Broker C never saw this tower's link - and still authorizes a consumer onto it, at the
	// node's own pinned price, with the endpoint the projection carried across.
	consumer := signedInConsumer(t, c)
	code, auth := consumerCall(t, cSrv, consumer, "/tower/edge/authorize", map[string]any{
		"model": "xb-model", "consumer_env_key": testEnvKeyHex(t),
	})
	require.Equal(t, http.StatusOK, code, auth)
	require.Equal(t, "203.0.113.5:8444", auth["endpoint"],
		"the endpoint crossed instances through the projection")
	require.EqualValues(t, 250_000, auth["price_out_micros"],
		"the node's own listed price is pinned by the instance that never held the link")
	require.NotEmpty(t, auth["station_session_key"],
		"the sealing key comes from the SHARED attachment record")
	_ = tw
}
