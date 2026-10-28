package main

// `roger edge trouble [<node>] [--since <cursor>] [--json]`: read a member's trouble feed from a
// terminal, the way the phone does - over the node's pinned mTLS face with this machine's
// certificate - and print it in the station's words.

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/client"
	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/store"
)

func TestEdgeTroubleReadsAMembersFeed(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	now := time.Now()
	edgeSeed(t, store.EdgeNode{ID: "n_nas", Name: "nas", Kind: "host", Presence: string(edge.PresenceVerified), LastSeen: now.Unix(),
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: "192.168.1.9:44121", Fingerprint: "abab"}}, Pin: "abab"})
	var dialed []string
	prev := edgeDialTrouble
	edgeDialTrouble = func(ctx context.Context, p client.EdgePeer, cert *tls.Certificate, since string) (edge.TroubleFeed, error) {
		dialed = append(dialed, p.Addr+" since="+since)
		return edge.TroubleFeed{At: now.Unix(), Cursor: "c2", Rate: 1,
			Standing: []edge.TroubleItem{{Rule: "cpu_temp", NodeID: "n_nas", Node: "nas", Metric: "cpu_temp", When: "cpu_temp > 85", Value: "95.5°C",
				Priority: "HI", SinceS: 840, Why: "a hot processor throttles, then fails early", Do: "check the cooling and airflow"}},
			Transitions: []edge.TroubleTransition{{At: now.Add(-14 * time.Minute).Unix(), Rule: "cpu_temp", Node: "nas", When: "cpu_temp > 85", Firing: true, Priority: "HI"}},
			SignOff:     edge.TroubleSignOff{Raised: 1, DeadAir: []edge.TroubleDeadAir{}}}, nil
	}
	t.Cleanup(func() { edgeDialTrouble = prev })
	prevCert := edgeCallerCertFn
	edgeCallerCertFn = func() *tls.Certificate { return &tls.Certificate{} }
	t.Cleanup(func() { edgeCallerCertFn = prevCert })

	out, code := edgeRun(t, "edge", "trouble", "nas", "--since", "c1")
	require.Equal(t, 0, code, out)
	require.Equal(t, []string{"192.168.1.9:44121 since=c1"}, dialed)
	for _, want := range []string{"TROUBLE on nas", "1 standing", "HI", "cpu_temp > 85", "95.5°C", "14m", "why it matters", "check the cooling", "fired", "cursor c2"} {
		require.Contains(t, out, want)
	}

	out, code = edgeRun(t, "edge", "trouble", "nas", "--json")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, `"standing"`)
	require.Contains(t, out, `"cursor": "c2"`)

	out, code = edgeRun(t, "edge", "trouble", "nobody")
	require.NotEqual(t, 0, code)
	require.Contains(t, out, "nobody")
}

// The real dial verifies the face against its pin before a byte is sent: a face with another
// certificate is refused, whatever it answers.
func TestEdgeTroubleDialRefusesAFaceThatDoesNotMatchItsPin(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"at":1,"cursor":"x","standing":[],"transitions":[]}`))
	}))
	t.Cleanup(srv.Close)
	self := srv.TLS.Certificates[0]
	_, err := edgeDialTrouble(context.Background(), client.EdgePeer{Addr: srv.Listener.Addr().String(), Pin: "0000", Node: "imposter"}, &self, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "pin")
}

// --watch polls the feed the way the phone does: it prints each new transition once (the slack
// re-sends what was seen; the dedupe by rule, node, at drops it), carries the cursor forward, and
// stops when told.
func TestEdgeTroubleWatchPrintsEachTransitionOnce(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	now := time.Now()
	edgeSeed(t, store.EdgeNode{ID: "n_nas", Name: "nas", Kind: "host", Presence: string(edge.PresenceVerified), LastSeen: now.Unix(),
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: "192.168.1.9:44121", Fingerprint: "abab"}}, Pin: "abab"})
	reads := 0
	prev := edgeDialTrouble
	edgeDialTrouble = func(ctx context.Context, p client.EdgePeer, cert *tls.Certificate, since string) (edge.TroubleFeed, error) {
		reads++
		fired := edge.TroubleTransition{At: now.Unix(), Rule: "cpu_temp", NodeID: "n_nas", Node: "nas", When: "cpu_temp > 85", Firing: true, Priority: "HI"}
		cleared := edge.TroubleTransition{At: now.Unix() + 60, Rule: "cpu_temp", NodeID: "n_nas", Node: "nas", When: "cpu_temp > 85", Firing: false, Priority: "HI"}
		switch reads {
		case 1:
			return edge.TroubleFeed{At: now.Unix(), Cursor: "c1", Transitions: []edge.TroubleTransition{fired}}, nil
		case 2:
			return edge.TroubleFeed{At: now.Unix() + 60, Cursor: "c2", Transitions: []edge.TroubleTransition{fired, cleared}}, nil // the slack re-sends the firing
		}
		return edge.TroubleFeed{At: now.Unix() + 120, Cursor: "c3", Transitions: []edge.TroubleTransition{fired, cleared}}, nil
	}
	t.Cleanup(func() { edgeDialTrouble = prev })
	prevCert := edgeCallerCertFn
	edgeCallerCertFn = func() *tls.Certificate { return &tls.Certificate{} }
	t.Cleanup(func() { edgeCallerCertFn = prevCert })
	prevEvery := edgeTroubleWatchEvery
	edgeTroubleWatchEvery = time.Millisecond
	t.Cleanup(func() { edgeTroubleWatchEvery = prevEvery })
	prevMax := edgeTroubleWatchMax
	edgeTroubleWatchMax = 3
	t.Cleanup(func() { edgeTroubleWatchMax = prevMax })

	out, code := edgeRun(t, "edge", "trouble", "nas", "--watch")
	require.Equal(t, 0, code, out)
	require.Equal(t, 1, strings.Count(out, "fired    cpu_temp > 85 on nas"), "printed once, though re-sent")
	require.Equal(t, 1, strings.Count(out, "cleared  cpu_temp > 85 on nas"))
	require.Contains(t, out, "watching nas")
}

// A face that refuses answers in the infer error shape: {"error":{"message":...}}. The dial carries
// that message to the owner, not a bare status line (audit 2026-09-27 asked for the pin).
func TestEdgeTroubleDialCarriesTheFacesNestedReason(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"that certificate is not a member of this Edge","type":"edge_error"}}`))
	}))
	t.Cleanup(srv.Close)
	self := srv.TLS.Certificates[0]
	pin := fmt.Sprintf("%x", sha256.Sum256(srv.Certificate().Raw))
	_, err := edgeDialTrouble(context.Background(), client.EdgePeer{Addr: srv.Listener.Addr().String(), Pin: pin, Node: "desk"}, &self, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "desk refused the feed: that certificate is not a member of this Edge")
	require.NotContains(t, err.Error(), "403")
}
