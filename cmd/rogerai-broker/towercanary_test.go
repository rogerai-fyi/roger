package main

// towercanary_test.go stands up the whole edge path and has Core probe it as a consumer.
//
// Contract: features/tower/edge_dispatch.feature.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/relay"
	"rogerai.fm/roger/v5/internal/station"
	"rogerai.fm/roger/v5/internal/store"
	"rogerai.fm/roger/v5/internal/towercore/admit"
	"rogerai.fm/roger/v5/internal/towercore/attach"
	"rogerai.fm/roger/v5/internal/towercore/fleet"
	"rogerai.fm/roger/v5/internal/towercore/link"
	"rogerai.fm/roger/v5/internal/towercore/reputation"
)

// canaryTower stands up a Station (attached, serving over TLS whose cert chains to Core's CA),
// a relay in front, and a routable row with the endpoint - everything a real edge attempt
// needs, so a canary is genuinely indistinguishable from a customer's.
func canaryTower(t *testing.T, b *broker, answer string) (towerID, endpoint string) {
	t.Helper()
	tw := enrolledTower(t, b, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))

	// A real Station, attached under ITS OWN assertion key so the receipt verifies.
	st, err := station.Init(t.TempDir())
	require.NoError(t, err)
	sessionRaw := make([]byte, 32)
	copy(sessionRaw, "session")
	auth, secret, err := attach.NewInvite(attach.Authorization{
		ID: "auth-canary", Network: link.PublicNetwork, StationID: st.StationID, Owner: "owner-1",
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: tw.id},
		AssertionKey: hex.EncodeToString(st.AssertionPub()), SessionKey: hex.EncodeToString(sessionRaw),
	}, time.Hour, time.Now())
	require.NoError(t, err)
	require.NoError(t, b.tower.stationStore.PutAuthorization(auth))
	_, err = b.tower.stations.Admit(attach.Proof{
		AuthID: "auth-canary", Secret: secret, Network: link.PublicNetwork, StationID: st.StationID,
		Owner: "owner-1", Origin: attach.Origin{Kind: attach.OriginJoined, TowerID: tw.id},
		AssertionKey: hex.EncodeToString(st.AssertionPub()), SessionKey: hex.EncodeToString(sessionRaw),
	})
	require.NoError(t, err)
	_, err = b.tower.stations.Promote(st.StationID)
	require.NoError(t, err)

	// The Station's TLS identity, a leaf Core's CA signed for the relay name - so the canary,
	// which trusts Core's root, accepts it.
	relayName := st.StationID + "." + relayDomain()
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: relayName}, DNSNames: []string{relayName},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, b.tower.ca.Root(), &leafKey.PublicKey,
		b.tower.ca.RootKey())
	require.NoError(t, err)

	edge := station.EdgeExecutor{
		Station: st, CoreKey: b.tower.dispatchPub, Network: link.PublicNetwork,
		Upstream: fixedBytes(answer),
	}
	rawLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	stationAddr := rawLn.Addr().String()
	tlsLn := tls.NewListener(rawLn, &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: leafKey}},
		MinVersion:   tls.VersionTLS12,
	})
	stationSrv := &http.Server{Handler: http.HandlerFunc(canaryServe(edge))}
	go func() { _ = stationSrv.Serve(tlsLn) }()
	t.Cleanup(func() { _ = stationSrv.Close() })

	// A blind relay in front.
	relayLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	endpoint = relayLn.Addr().String()
	r := &relay.Relay{Router: canaryRoute{name: relayName, addr: stationAddr},
		PeekTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	relayDone := make(chan struct{})
	go func() { defer close(relayDone); _ = r.Serve(ctx, relayLn) }()
	t.Cleanup(func() { cancel(); _ = relayLn.Close(); <-relayDone })

	// The routable projection, with the endpoint, and an inventory leaf so canaryTargetFor
	// finds a model behind this Tower.
	require.NoError(t, b.tower.routable.Replace(tw.id, []fleet.Station{{
		TowerID: tw.id, StationID: st.StationID, OfferID: "of-1", Model: "m",
		Modality: "text", Expires: time.Now().Add(time.Hour), Endpoint: endpoint,
	}}))
	return tw.id, endpoint
}

// THE PASS: a Tower carrying work to a live Station gives Core a valid receipt.
func TestACanaryThroughAHealthyTowerPasses(t *testing.T) {
	b, _ := towerTestBroker(t)
	towerID, _ := canaryTower(t, b, `{"choices":[{"text":"pong"}]}`)

	outcome := b.RunCanary(towerID)
	require.Equal(t, reputation.CanaryPass, outcome)

	tally, err := b.tower.outcomes.Tally(towerID, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, tally.CanaryPass)
}

// THE FAIL: a Tower whose data plane is unreachable is caught. Repeated, it suspends.
func TestACanaryToADeadTowerFailsAndRepeatedFailuresSuspend(t *testing.T) {
	b, _ := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))
	stationPriv := attachStation(t, b, "st-1", tw.id, "owner-1")
	_ = stationPriv
	// A routable row pointing at a dead endpoint.
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	deadAddr := dead.Addr().String()
	require.NoError(t, dead.Close())
	require.NoError(t, b.tower.routable.Replace(tw.id, []fleet.Station{{
		TowerID: tw.id, StationID: "st-1", OfferID: "of-1", Model: "m", Modality: "text",
		Expires: time.Now().Add(time.Hour), Endpoint: deadAddr,
	}}))

	// Each probe fails until the evidence crosses the threshold and the Tower is suspended;
	// once suspended it takes no work, so a further canary correctly finds nothing to probe.
	suspended := false
	for i := 0; i < 10 && !suspended; i++ {
		b.RunCanary(tw.id)
		got, _ := b.tower.registry.Get(tw.id)
		suspended = got.State == admit.StateSuspended
	}
	require.True(t, suspended, "a Tower that fails canaries repeatedly is taken off")
}

// A Tower with nothing routable is not canaried - there is nothing to probe, which is not a
// failure.
func TestACanaryWithNoTargetRecordsNothing(t *testing.T) {
	b, _ := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))
	require.Equal(t, reputation.Outcome(""), b.RunCanary(tw.id))
}

// The sweep probes every routable Tower.
func TestTheCanarySweepProbesTheFleet(t *testing.T) {
	b, _ := towerTestBroker(t)
	towerID, _ := canaryTower(t, b, `{"ok":true}`)
	b.towerCanarySweepOnce()
	tally, err := b.tower.outcomes.Tally(towerID, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, tally.CanaryPass)
}

func TestTheCanarySweepIsSafeWithoutTheSubsystem(t *testing.T) {
	b := testBrokerWithDB(store.NewMem())
	require.NotPanics(t, func() { b.towerCanarySweepOnce() })
	require.Equal(t, reputation.Outcome(""), b.RunCanary("tw-x"))
}

type canaryRoute struct{ name, addr string }

func (c canaryRoute) Upstream(name string) (string, bool) {
	if name != c.name {
		return "", false
	}
	return c.addr, true
}

type fixedBytes string

func (f fixedBytes) Serve(context.Context, []byte) ([]byte, error) { return []byte(f), nil }

func canaryServe(edge station.EdgeExecutor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, 0)
		buf := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buf)
			raw = append(raw, buf[:n]...)
			if err != nil {
				break
			}
		}
		resp := edge.Serve(r.Context(), station.EdgeRequest{
			Grant: r.Header.Get(station.GrantHeader), Body: raw,
		})
		if resp.Failure != "" {
			http.Error(w, resp.Failure, resp.Status)
			return
		}
		w.Header().Set(station.ReceiptHeader, resp.Receipt)
		_, _ = w.Write(resp.Body)
	}
}

// The canary sweep loop runs on its ticker and stops when asked.
func TestTheCanarySweepLoopStops(t *testing.T) {
	b, _ := towerTestBroker(t)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { defer close(done); b.towerCanarySweep(stop) }()
	close(stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the canary sweep did not stop")
	}
}

// The sweep on a broker whose fleet lists a failing Tower records the failure and logs it.
func TestTheSweepProbesADeadTowerAndRecordsIt(t *testing.T) {
	b, _ := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))
	attachStation(t, b, "st-1", tw.id, "owner-1")
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	deadAddr := dead.Addr().String()
	require.NoError(t, dead.Close())
	require.NoError(t, b.tower.routable.Replace(tw.id, []fleet.Station{{
		TowerID: tw.id, StationID: "st-1", OfferID: "of-1", Model: "m", Modality: "text",
		Expires: time.Now().Add(time.Hour), Endpoint: deadAddr,
	}}))

	b.towerCanarySweepOnce()
	tally, err := b.tower.outcomes.Tally(tw.id, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, tally.CanaryFail)
}
