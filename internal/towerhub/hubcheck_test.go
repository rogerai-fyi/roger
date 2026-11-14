package towerhub

// The Station-free hub check (features/tower/tower_trust_ramp.feature, "the hub check"):
// Core asks a hub to prove its identity over a fresh nonce, with no Station involved.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func keyHashOf(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

func TestAnHonestHubPassesTheCheck(t *testing.T) {
	real, srv := hubRig(t, testTowerID)
	rtt, reason := CheckHub(t.Context(), srv.Client(), srv.URL, testTowerID, real.EpochKeyHash())
	require.Empty(t, reason)
	require.Greater(t, rtt, time.Duration(0))
}

func TestAHubCheckFailsOnAnyBrokenProof(t *testing.T) {
	real, srv := hubRig(t, testTowerID)
	good := real.EpochKeyHash()

	// Unreachable.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	_, reason := CheckHub(t.Context(), &http.Client{Timeout: time.Second}, deadURL, testTowerID, good)
	require.Equal(t, "unreachable", reason)

	// A key other than the admitted one.
	_, other, _ := ed25519.GenerateKey(nil)
	_, reason = CheckHub(t.Context(), srv.Client(), srv.URL, testTowerID, keyHashOf(other.Public().(ed25519.PublicKey)))
	require.Equal(t, "wrong_key", reason)

	// The right hub under another Tower's name: its proof is over its own id.
	_, reason = CheckHub(t.Context(), srv.Client(), srv.URL, "tw-someone-else", good)
	require.Equal(t, "stale_proof", reason)

	fake := func(h func(w http.ResponseWriter, r *http.Request)) string {
		s := httptest.NewServer(http.HandlerFunc(h))
		t.Cleanup(s.Close)
		return s.URL
	}
	// No proof at all.
	u := fake(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	_, reason = CheckHub(t.Context(), http.DefaultClient, u, testTowerID, good)
	require.Equal(t, "no_proof", reason)

	// A replayed proof: the real hub's answer to a DIFFERENT nonce, relayed.
	captured := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, PathPoll+"?nonce=someone-elses-nonce", nil)
	real.Poll(captured, req)
	u = fake(func(w http.ResponseWriter, r *http.Request) {
		for _, h := range []string{HubEpochHeader, HubKeyHeader, HubProofHeader} {
			w.Header().Set(h, captured.Header().Get(h))
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, reason = CheckHub(t.Context(), http.DefaultClient, u, testTowerID, good)
	require.Equal(t, "stale_proof", reason)

	// Malformed proof material.
	u = fake(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(HubEpochHeader, "e1")
		w.Header().Set(HubKeyHeader, captured.Header().Get(HubKeyHeader))
		w.Header().Set(HubProofHeader, "not-hex")
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, reason = CheckHub(t.Context(), http.DefaultClient, u, testTowerID, good)
	require.Equal(t, "malformed_proof", reason)
	u = fake(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(HubEpochHeader, "e1")
		w.Header().Set(HubKeyHeader, "zz")
		w.Header().Set(HubProofHeader, "00")
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, reason = CheckHub(t.Context(), http.DefaultClient, u, testTowerID, good)
	require.Equal(t, "malformed_proof", reason)

	// An empty admitted key cannot be checked against and never passes.
	_, reason = CheckHub(t.Context(), srv.Client(), srv.URL, testTowerID, "")
	require.Equal(t, "wrong_key", reason)
}

func TestAHubCheckOverAPinnedLinkRefusesTheWrongCertificate(t *testing.T) {
	real, _ := hubRig(t, testTowerID)
	mux := http.NewServeMux()
	mux.HandleFunc(PathPoll, real.Poll)
	tlsSrv := httptest.NewTLSServer(mux)
	t.Cleanup(tlsSrv.Close)
	endpoint := tlsSrv.Listener.Addr().String()

	pin := CertPin(tlsSrv.Certificate())
	base, hc, err := Reach(endpoint, pin, &http.Client{Timeout: 2 * time.Second})
	require.NoError(t, err)
	_, reason := CheckHub(t.Context(), hc, base, testTowerID, real.EpochKeyHash())
	require.Empty(t, reason, "the right pin passes")

	wrong := "00" + pin[2:]
	if wrong == pin {
		wrong = "11" + pin[2:]
	}
	base, hc, err = Reach(endpoint, wrong, &http.Client{Timeout: 2 * time.Second})
	require.NoError(t, err)
	_, reason = CheckHub(t.Context(), hc, base, testTowerID, real.EpochKeyHash())
	require.Equal(t, "pin_mismatch", reason)
}
