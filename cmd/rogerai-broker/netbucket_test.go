package main

// netbucket_test.go pins the ONE locality signal the broker now keeps about a serving node, and
// - just as importantly - pins how coarse it is.
//
// The relay-selection design's §4.1 rule is that supply-side location must never be
// self-declared: `--region` is a string the operator types (defaulting to the literal "home")
// and the moment a typed string feeds placement it becomes a lever. The connecting address is
// the one location signal the party being located does not choose, and the broker has been
// computing it on every registration for the free-registration rate limiter and discarding it.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
)

// COARSE MEANS COARSE. A bucket, never an address.
func TestTheNetworkBucketIsAPrefixAndNeverAnAddress(t *testing.T) {
	cases := []struct{ in, want string }{
		{"203.0.113.42", "203.0.113.0/24"},
		{"203.0.113.255", "203.0.113.0/24"},
		{"198.51.100.7", "198.51.100.0/24"},
		// IPv6 buckets at the ISP allocation, not the /48 subscriber site: a site is one
		// household, which would be FINER than the v4 /24, not coarser.
		{"2001:0db8:1234:5678::1", "2001:db8::/32"},
		{"127.0.0.1", "local"},
		{"192.168.1.50", "local"},
		{"10.0.0.9", "local"},
		{"", ""},
		{"not-an-address", ""},
	}
	for _, c := range cases {
		got := coarseNetBucket(c.in)
		require.Equal(t, c.want, got, "coarseNetBucket(%q)", c.in)
		if c.in != "" && got != "" && got != "local" {
			require.NotContains(t, got, c.in,
				"the bucket for %q contains the address itself, which is the thing it must not keep", c.in)
		}
	}
}

// AND IT IS OBSERVED, NOT CLAIMED. A node that declares itself in another region keeps whatever
// bucket its connection actually came from - which is the whole reason this signal is worth
// collecting instead of reading --region.
func TestTheNetworkBucketComesFromTheConnectionNotTheRegistration(t *testing.T) {
	b := bucketBroker()

	reg := signedTestReg(t, "n-loc", "somewhere-else-entirely")
	body, err := json.Marshal(reg)
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	// The header clientIP prefers over every client-appendable one.
	r.Header.Set("CF-Connecting-IP", "203.0.113.42")
	w := httptest.NewRecorder()
	b.register(w, r)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	require.Equal(t, "203.0.113.0/24", b.nodeNetBucket("n-loc"),
		"the registration's own region string, or nothing at all, was kept instead of the observed network")
	require.NotEqual(t, "somewhere-else-entirely", b.nodeNetBucket("n-loc"))
}

// NOTHING IS LOGGED. The address is used and dropped; the bucket is kept in memory. Neither may
// appear in a log line, because a full client IP in the ops log is a different commitment than
// the one PRIVACY.md describes and this change is not the place to make it.
func TestRegisteringANodeLogsNoAddress(t *testing.T) {
	b := bucketBroker()
	reg := signedTestReg(t, "n-quiet", "home")
	body, err := json.Marshal(reg)
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	r.Header.Set("CF-Connecting-IP", "203.0.113.42")

	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	b.register(httptest.NewRecorder(), r)
	log.SetOutput(old)

	require.NotContains(t, buf.String(), "203.0.113.42", "a registration logged the connecting address")
	require.NotContains(t, buf.String(), "203.0.113.0/24", "a registration logged the derived bucket")
}

// A node that moves to a network we cannot bucket must not keep claiming the old one.
func TestAnUnbucketableAddressClearsTheOldBucket(t *testing.T) {
	b := bucketBroker()
	// ONE key for both registrations: a node id belongs to the first key that claims it (TOFU),
	// so a re-register from a new key is a takeover attempt rather than a move.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	post := func(ip string) {
		reg := regSignedBy(t, "n-move", pub, priv)
		body, err := json.Marshal(reg)
		require.NoError(t, err)
		r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
		if ip != "" {
			r.Header.Set("CF-Connecting-IP", ip)
		} else {
			r.RemoteAddr = "not-a-host-port"
		}
		w := httptest.NewRecorder()
		b.register(w, r)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	}
	post("203.0.113.42")
	require.Equal(t, "203.0.113.0/24", b.nodeNetBucket("n-move"))
	post("")
	require.Empty(t, b.nodeNetBucket("n-move"),
		"a stale bucket survived a registration from an address we could not read")
}

// bucketBroker is the minimum broker /nodes/register needs.
func bucketBroker() *broker {
	return &broker{
		nodes:        map[string]protocol.NodeRegistration{},
		tunnels:      map[string]*nodeTunnel{},
		lastSeen:     map[string]time.Time{},
		confidential: map[string]bool{},
		tps:          map[string]float64{},
	}
}

// signedTestReg builds a minimal, correctly signed node registration under a fresh key, so the
// register handler takes the ordinary path rather than an auth refusal.
func signedTestReg(t *testing.T, nodeID, region string) protocol.NodeRegistration {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	reg := regSignedBy(t, nodeID, pub, priv)
	reg.Region = region
	reg.SignRegistration(priv)
	return reg
}

// regSignedBy builds one under a caller-supplied key, for the re-register case.
func regSignedBy(t *testing.T, nodeID string, pub ed25519.PublicKey, priv ed25519.PrivateKey) protocol.NodeRegistration {
	t.Helper()
	reg := protocol.NodeRegistration{
		NodeID: nodeID, PubKey: hex.EncodeToString(pub), TS: time.Now().Unix(),
		Region: "home", HW: "cpu",
		Offers: []protocol.ModelOffer{{Model: "m"}},
	}
	reg.SignRegistration(priv)
	return reg
}
