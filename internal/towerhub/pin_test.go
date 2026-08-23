package towerhub

// pin_test.go holds the certificate check to the standard the rest of this plane is held to:
// every test here must FAIL against a client that dials https and believes whatever answers.
//
// That is not a rhetorical standard. The cheap version of this change - build an https URL,
// set InsecureSkipVerify, ship it - passes any test that only looks at the scheme, and it is
// worth strictly less than the plaintext it replaced: it costs a handshake, it tells the
// operator their link is encrypted, and it protects against nobody who can answer a socket.
// So the tests below are written the other way round: the ones that matter are the ones where
// a WRONG certificate, or NO TLS at all, must be refused.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"errors"
	"github.com/stretchr/testify/require"
	"net"
)

// selfSignedCert mints the kind of certificate a tower mints for itself: Ed25519, self-signed,
// no names, valid. Each call is a DIFFERENT key, which the tests here depend on -
// httptest.NewTLSServer shares one built-in certificate across every server it starts, so two
// of those would have the same pin and "the wrong certificate" would be untestable.
func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "roger tower hub test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv, Leaf: leaf}
}

// tlsHubRig is a hub Server behind a REAL TLS listener holding its own certificate, with the
// pin for it - the shape a tower serves when its operator passes --hub-tls.
func tlsHubRig(t *testing.T) (*Server, string, string) {
	t.Helper()
	s := NewServer(New(), stubCheck, ServerOptions{TowerID: testTowerID, EpochKey: testHubKey,
		SubmitTTL: 3 * time.Second, PollTTL: 300 * time.Millisecond})
	mux := http.NewServeMux()
	mux.HandleFunc(PathSubmit, s.Submit)
	mux.HandleFunc(PathPoll, s.Poll)
	mux.HandleFunc(PathComplete, s.Complete)
	cert := selfSignedCert(t)
	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	// The address without the scheme, because that is the ONLY shape the wire carries: a
	// Tower advertises host:port and the pin decides the rest. Building the test's endpoint
	// the same way the wire does is what keeps it a test of the real path.
	return s, strings.TrimPrefix(srv.URL, "https://"), CertPin(cert.Leaf)
}

// THE TEST THIS FILE EXISTS FOR. A pinned client must refuse a certificate that is not the one
// Core named - which is the on-path attacker, and equally a tower that quietly swapped its key.
//
// It fails against InsecureSkipVerify with no callback: that client connects happily to
// anything, so pinning to one server and dialling another succeeds, and this assertion goes red.
func TestAPinnedClientRefusesACertificateCoreDidNotName(t *testing.T) {
	_, endpoint, _ := tlsHubRig(t)
	_, _, otherPin := tlsHubRig(t) // a different listener, a different key

	base, hc, err := Reach(endpoint, otherPin, nil)
	require.NoError(t, err, "building the client is fine - it is the handshake that must fail")
	require.True(t, strings.HasPrefix(base, "https://"))

	req, err := http.NewRequest(http.MethodGet, base+PathPoll, nil)
	require.NoError(t, err)
	_, err = hc.Do(req)
	require.Error(t, err, "a hub presenting a certificate Core did not name must not be talked to")
	require.ErrorIs(t, err, ErrHubCertificateUnpinned)
}

// AND THE OTHER HALF: the right certificate is accepted. Without this the test above passes for
// a client that refuses everything, which protects nothing because nobody could use it.
func TestAPinnedClientAcceptsTheCertificateCoreNamed(t *testing.T) {
	_, endpoint, pin := tlsHubRig(t)
	base, hc, err := Reach(endpoint, pin, nil)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodGet, base+PathPoll, nil)
	require.NoError(t, err)
	resp, err := hc.Do(req)
	require.NoError(t, err, "the pinned certificate is the one this hub presents")
	resp.Body.Close()
}

// A PINNED CLIENT MUST ACTUALLY SPEAK TLS, not merely wear the prefix. This is the test that
// catches the version of the change that builds an "https://" string and leaves the transport
// alone: against a plaintext listener that client would talk cheerfully to a hub with no
// encryption at all while the operator was told the link was protected.
func TestAPinnedClientCannotBeSatisfiedByAPlaintextHub(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(plain.Close)
	_, _, pin := tlsHubRig(t)

	base, hc, err := Reach(strings.TrimPrefix(plain.URL, "http://"), pin, nil)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodGet, base+PathPoll, nil)
	require.NoError(t, err)
	_, err = hc.Do(req)
	require.Error(t, err, "an https request to a plaintext listener must fail, not downgrade")
}

// THE WHOLE DATA PATH OVER A PINNED HUB: a node polls and serves, a consumer submits sealed work
// and gets the sealed answer and the receipt back - every byte of it over verified TLS.
//
// It is one test rather than two because the two legs are the thing most likely to be half
// done. The node's leg and the consumer's leg reach the same hub through different call sites
// in different packages, and a change that threads the pin to one of them leaves the other
// talking plaintext to a TLS listener - which is not a degraded mode, it is an outage for half
// the traffic and a silent plaintext channel for the other half.
func TestBothLegsOfTheHubRunOverAPinnedTLSChannel(t *testing.T) {
	id := newTestNode(t)
	s, endpoint, pin := tlsHubRig(t)
	s.RegisterNode("st-1", id.auth())

	nodeBase, nodeHTTP, err := Reach(endpoint, pin, &http.Client{Timeout: 5 * time.Second})
	require.NoError(t, err)
	node := &Client{BaseURL: nodeBase, TowerID: testTowerID, TowerKeyHash: testHubKeyHash(),
		Sign: id.signer(), HTTP: nodeHTTP}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = ServeLoop(ctx, node, "st-1", echoExec{}, nil) }()

	consumerBase, consumerHTTP, err := Reach(endpoint, pin, nil)
	require.NoError(t, err)
	consumer := &Client{BaseURL: consumerBase, HTTP: consumerHTTP}

	sctx, scancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer scancel()
	res, err := consumer.SubmitJob(sctx, []byte("att-1|st-1"), []byte("sealed-req"))
	require.NoError(t, err)
	require.Equal(t, []byte("served:sealed-req"), res.Envelope)
	require.Equal(t, []byte("receipt:att-1|st-1"), res.Receipt)
}

// THERE IS NO WAY TO GET AN UNVERIFIED TLS CLIENT OUT OF THIS PACKAGE, and that is a structural
// property rather than a habit. InsecureSkipVerify is set exactly once in the tree, in a
// function that cannot return without also installing the check that replaces it.
func TestAnUnverifiedTLSClientCannotBeBuiltHere(t *testing.T) {
	for _, pin := range []string{"", "nonsense", strings.Repeat("a", PinLen-1), strings.Repeat("z", PinLen)} {
		_, err := PinnedTLSConfig(pin)
		require.Error(t, err, "pin %q must not produce a TLS configuration", pin)
	}
	cfg, err := PinnedTLSConfig(strings.Repeat("ab", 32))
	require.NoError(t, err)
	require.NotNil(t, cfg.VerifyPeerCertificate,
		"InsecureSkipVerify without a replacement check is the defect, not the feature")

	// A caller-supplied transport is REFUSED rather than overwritten: silently replacing it
	// would break their dialing, and silently keeping it would drop the pin - which is the
	// failure this whole file is about.
	_, _, err = Reach("relay.example:8443", strings.Repeat("ab", 32),
		&http.Client{Transport: http.DefaultTransport})
	require.ErrorContains(t, err, "Transport")
}

// The scheme is derived from the pin and from nothing else, and an endpoint that tries to name
// its own is a loud stop. The last version of this code honoured such an endpoint in a branch
// no wire format could reach, and its comment advertised the capability as shipped.
func TestTheSchemeComesFromThePinAndTheEndpointMayNotCarryOne(t *testing.T) {
	plain, err := HubURL("203.0.113.9:8443", "")
	require.NoError(t, err)
	require.Equal(t, "http://203.0.113.9:8443", plain)

	secure, err := HubURL("203.0.113.9:8443", strings.Repeat("ab", 32))
	require.NoError(t, err)
	require.Equal(t, "https://203.0.113.9:8443", secure)

	_, err = HubURL("https://relay.example:443", "")
	require.ErrorIs(t, err, ErrEndpointCarriesScheme)

	// A malformed pin does not fall back to plaintext. A downgrade reachable by corrupting one
	// field is not a security property.
	_, err = HubURL("203.0.113.9:8443", "not-a-fingerprint")
	require.ErrorContains(t, err, "malformed")

	_, err = HubURL("not-an-endpoint", "")
	require.ErrorContains(t, err, "host:port")
}

// ReachVetted: the vet rides inside BOTH transports and only when a vet is given.
func TestReachVettedBranches(t *testing.T) {
	refuseLoopback := func(ip net.IP) error {
		if ip.IsLoopback() {
			return errors.New("loopback refused by the vet")
		}
		return nil
	}

	// Plain HTTP: the guard replaces the dial and refuses on the resolved address.
	_, hc, err := ReachVetted("localhost:9", "", refuseLoopback)
	require.NoError(t, err)
	req, _ := http.NewRequest(http.MethodGet, "http://localhost:9/", nil)
	_, err = hc.Do(req)
	require.ErrorContains(t, err, "refusing to dial")

	// A host that does not resolve is the RESOLVER's error, surfaced, not a vet pass.
	req2, _ := http.NewRequest(http.MethodGet, "http://tower-hub.invalid:9/", nil)
	_, err = hc.Do(req2)
	require.Error(t, err)

	// Pinned TLS: the vetted dialer coexists with the pin's transport.
	pin := strings.Repeat("ab", 32)
	_, phc, err := ReachVetted("localhost:9", pin, refuseLoopback)
	require.NoError(t, err)
	preq, _ := http.NewRequest(http.MethodGet, "https://localhost:9/", nil)
	_, err = phc.Do(preq)
	require.ErrorContains(t, err, "refusing to dial", "the vet must hold on the pinned path too")

	// A bad pin still fails as a bad pin - vetting must not mask pin validation.
	_, _, err = ReachVetted("localhost:9", "zz", refuseLoopback)
	require.Error(t, err)

	// Nil vet is the unvetted path, byte-for-byte Reach.
	_, nhc, err := ReachVetted("localhost:9", "", nil)
	require.NoError(t, err)
	require.NotNil(t, nhc)
}

// errSnippet bounds and sanitizes tower-controlled error text: no megabytes, no escapes.
func TestErrSnippetSanitizesHostileText(t *testing.T) {
	long := strings.Repeat("a", 5000)
	require.LessOrEqual(t, len(errSnippet([]byte(long))), 2100)
	got := errSnippet([]byte("bad\x1b[2Jnews\r\nrow\tok\x00end"))
	require.NotContains(t, got, "\x1b", "terminal escapes must not survive")
	require.NotContains(t, got, "\x00")
	require.Contains(t, got, "news")
	require.Contains(t, got, "\n", "ordinary newlines survive")
}

// epochFrom learns nothing from silence, sameness, or a client that cannot sign.
func TestEpochFromIgnoresWhatItShould(t *testing.T) {
	c := &Client{}
	resp := &http.Response{Header: http.Header{}}
	got, err := c.epochFrom(resp, "e1", "n")
	require.NoError(t, err)
	require.Empty(t, got, "no header, nothing to learn")

	resp.Header.Set(HubEpochHeader, "e1")
	got, err = c.epochFrom(resp, "e1", "n")
	require.NoError(t, err)
	require.Empty(t, got, "the epoch already in use is not news")

	resp.Header.Set(HubEpochHeader, "e2")
	got, err = c.epochFrom(resp, "e1", "n")
	require.NoError(t, err)
	require.Empty(t, got, "a consumer signs nothing, so it has no epoch to be wrong about")
}
