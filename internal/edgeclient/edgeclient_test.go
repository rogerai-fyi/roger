package edgeclient

// edgeclient_test.go drives the whole consumer side against a real Station behind a real
// relay, and a Core stub that records what the acknowledgement carried.
//
// Contract: features/tower/edge_dispatch.feature.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/relay"
	"rogerai.fm/roger/v5/internal/station"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
	"rogerai.fm/roger/v5/internal/towercore/link"
)

// edgeWorld stands up the whole path: Core (authorize + ack), a Station serving over TLS, a
// blind relay in front of it, and a client pointed at Core.
type edgeWorld struct {
	client   *Client
	acked    []dispatch.Ack
	mu       sync.Mutex
	stopRlay func()
}

func newEdgeWorld(t *testing.T, model, answer string) *edgeWorld {
	t.Helper()
	const relayName = "st-1.relay.example"

	// The Station's identity, issued by a CA the client will trust.
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "probe CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)

	stKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: relayName},
		DNSNames: []string{relayName}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &stKey.PublicKey, caKey)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(caCert)

	// The Station and Core share a grant key; the Station verifies grants with the public
	// half, Core mints with the private.
	corePub, corePriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	st, err := station.Init(t.TempDir())
	require.NoError(t, err)
	edge := station.EdgeExecutor{
		Station: st, CoreKey: corePub, Network: link.PublicNetwork,
		Upstream: fixedUpstream([]byte(answer)),
	}

	// The Station's TLS listener.
	rawLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	stationAddr := rawLn.Addr().String()
	tlsLn := tls.NewListener(rawLn, &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: stKey}},
		MinVersion:   tls.VersionTLS12,
	})
	stationSrv := &http.Server{Handler: http.HandlerFunc(edgeServe(edge))}
	go func() { _ = stationSrv.Serve(tlsLn) }()
	t.Cleanup(func() { _ = stationSrv.Close() })

	// A blind relay in front, dialing the Station.
	relayLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	relayAddr := relayLn.Addr().String()
	r := &relay.Relay{
		Router:      staticRoute{name: relayName, addr: stationAddr},
		PeekTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	relayDone := make(chan struct{})
	go func() { defer close(relayDone); _ = r.Serve(ctx, relayLn) }()

	w := &edgeWorld{stopRlay: func() { cancel(); _ = relayLn.Close(); <-relayDone }}
	reg := dispatch.NewWithStore(dispatch.Config{
		Network: link.PublicNetwork, Signer: corePriv, Lifetime: time.Minute,
	}, nil)

	// Core: authorize mints against the relay endpoint; ack verifies against the request key.
	mux := http.NewServeMux()
	mux.HandleFunc("/tower/edge/authorize", func(rw http.ResponseWriter, r *http.Request) {
		pub := r.Header.Get("X-Roger-Pubkey")
		require.NotEmpty(t, pub, "authorize must be signed")
		g, err := reg.MintEdge(dispatch.EdgeTarget{
			TowerID: "tw-1", StationID: st.StationID, Model: model, Modality: "text",
			RelayName: relayName, MaxIn: 1 << 20, MaxOut: 1 << 20, AssertionKey: st.AssertionPub(), ConsumerKey: edgeConsumerKey(),
		})
		require.NoError(t, err)
		writeJSON(rw, map[string]any{
			"attempt_id": g.AttemptID, "grant": base64.StdEncoding.EncodeToString(g.Signed),
			"relay_name": relayName, "endpoint": relayAddr,
			"deadline": g.Deadline.Unix(), "max_in": g.MaxIn, "max_out": g.MaxOut,
		})
	})
	mux.HandleFunc("/tower/edge/ack", func(rw http.ResponseWriter, r *http.Request) {
		var req struct {
			AttemptID string `json:"attempt_id"`
			Ack       string `json:"ack"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		raw, _ := base64.StdEncoding.DecodeString(req.Ack)
		pub, _ := hex.DecodeString(r.Header.Get("X-Roger-Pubkey"))
		ack, err := dispatch.ParseAck(raw, pub, link.PublicNetwork, req.AttemptID)
		require.NoError(t, err)
		w.mu.Lock()
		w.acked = append(w.acked, ack)
		w.mu.Unlock()
		writeJSON(rw, map[string]any{"recorded": true})
	})
	coreSrv := httptest.NewServer(mux)
	t.Cleanup(coreSrv.Close)

	_, clientKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	w.client = &Client{Broker: coreSrv.URL, Key: clientKey, Roots: roots}
	return w
}

// THE WHOLE CONSUMER LOOP: authorize, serve through the relay, acknowledge - and the
// acknowledgement Core received matches the bytes the Station actually returned.
func TestTheClientAuthorizesServesAndAcknowledges(t *testing.T) {
	answer := `{"choices":[{"text":"the answer"}]}`
	w := newEdgeWorld(t, "m", answer)
	defer w.stopRlay()

	ctx := context.Background()
	auth, err := w.client.Authorize(ctx, "m", 1<<10, 1<<10)
	require.NoError(t, err)
	require.NotEmpty(t, auth.AttemptID)
	require.Equal(t, "st-1.relay.example", auth.RelayName)

	res, err := w.client.Do(ctx, auth, "/v1/chat/completions", []byte(`{"prompt":"hi"}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.Status)
	require.JSONEq(t, answer, string(res.Body), "the consumer gets the model's own bytes")

	require.NoError(t, w.client.Ack(ctx, auth, res))

	w.mu.Lock()
	defer w.mu.Unlock()
	require.Len(t, w.acked, 1)
	require.Equal(t, auth.AttemptID, w.acked[0].AttemptID)
	require.Equal(t, int64(len(answer)), w.acked[0].Usage.Out,
		"the acknowledgement must commit to exactly what was received")
}

func TestAuthorizeNeedsAKey(t *testing.T) {
	c := &Client{Broker: "http://127.0.0.1:1"}
	_, err := c.Authorize(context.Background(), "m", 1, 1)
	require.ErrorContains(t, err, "signing key")
}

// A request over the grant's ceiling is refused BEFORE a connection is spent - the Station
// would refuse it anyway, so there is no reason to open a socket to find out.
func TestARequestOverTheCeilingNeverLeaves(t *testing.T) {
	c := &Client{Broker: "http://127.0.0.1:1", Key: mustKey(t)}
	_, err := c.Do(context.Background(),
		Authorization{RelayName: "x", Endpoint: "127.0.0.1:1", MaxIn: 4}, "/", []byte("too long"))
	require.ErrorContains(t, err, "the grant allows 4")
}

// An acknowledgement of a non-answer is a no-op, not an error: there is nothing to
// corroborate, and signing a claim about an error body would be a lie.
func TestThereIsNothingToAcknowledgeForARefusal(t *testing.T) {
	c := &Client{Broker: "http://127.0.0.1:1", Key: mustKey(t)}
	require.NoError(t, c.Ack(context.Background(), Authorization{AttemptID: "a"},
		Result{Status: http.StatusForbidden}))
	require.NoError(t, c.Ack(context.Background(), Authorization{AttemptID: "a"},
		Result{Status: http.StatusOK, Body: []byte("hi")})) // no receipt: nothing to sign against
}

func TestAuthorizeSurfacesCoresRefusal(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/tower/edge/authorize", func(rw http.ResponseWriter, r *http.Request) {
		http.Error(rw, `{"error":{"message":"no Station can take this on the edge path right now"}}`,
			http.StatusServiceUnavailable)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := &Client{Broker: srv.URL, Key: mustKey(t)}
	_, err := c.Authorize(context.Background(), "m", 1, 1)
	require.ErrorContains(t, err, "no Station can take this")
}

func TestAnIncompleteAuthorizationIsRejected(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/tower/edge/authorize", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, map[string]any{"attempt_id": "a"}) // no grant, endpoint, or relay name
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := &Client{Broker: srv.URL, Key: mustKey(t)}
	_, err := c.Authorize(context.Background(), "m", 1, 1)
	require.ErrorContains(t, err, "missing a grant")
}

// --- helpers -----------------------------------------------------------------

type staticRoute struct{ name, addr string }

func (s staticRoute) Upstream(name string) (string, bool) {
	if name != s.name {
		return "", false
	}
	return s.addr, true
}

type fixedUpstream []byte

func (u fixedUpstream) Serve(context.Context, []byte) ([]byte, error) { return []byte(u), nil }

func edgeServe(edge station.EdgeExecutor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := readAll(r)
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func readAll(r *http.Request) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, 8<<20))
}

func mustKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return k
}

// The custom HTTP client and non-default network are used when set, not silently ignored.
func TestOptionalFieldsAreHonoured(t *testing.T) {
	var reached bool
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		reached = true
		return &http.Response{StatusCode: 200, Body: nopCloser([]byte(`{"attempt_id":"a","grant":"g","relay_name":"n","endpoint":"e"}`)), Header: http.Header{}}, nil
	})
	c := &Client{Broker: "http://localhost", Key: mustKey(t), HTTP: &http.Client{Transport: rt}, Network: "roger-private"}
	require.Equal(t, "roger-private", c.network())
	_, err := c.Authorize(context.Background(), "m", 1, 1)
	require.NoError(t, err)
	require.True(t, reached, "the custom HTTP client must carry the request")
}

// A Station that cannot be dialled is a real error, surfaced rather than swallowed.
func TestAnUnreachableStationIsAnError(t *testing.T) {
	c := &Client{Broker: "http://127.0.0.1:1", Key: mustKey(t)}
	_, err := c.Do(context.Background(),
		Authorization{RelayName: "n", Endpoint: "127.0.0.1:1", MaxIn: 1 << 20, MaxOut: 1 << 20},
		"/v1/x", []byte("hi"))
	require.Error(t, err)
}

// A control-plane call to a broker that is not there surfaces the transport error.
func TestAControlPlaneFailureSurfaces(t *testing.T) {
	c := &Client{Broker: "http://127.0.0.1:1", Key: mustKey(t)}
	err := c.signedPost(context.Background(), "/tower/edge/ack", []byte("{}"), nil)
	require.Error(t, err)
}

// A non-envelope error body still yields a status-bearing error rather than a bare one.
func TestAPlainErrorBodyStillReportsTheStatus(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: nopCloser([]byte("boom")), Header: http.Header{}}, nil
	})
	c := &Client{Broker: "http://localhost", Key: mustKey(t), HTTP: &http.Client{Transport: rt}}
	err := c.signedPost(context.Background(), "/x", []byte("{}"), nil)
	require.ErrorContains(t, err, "500")
}

// The path is normalised: a caller that omits the leading slash still reaches the Station.
func TestARelativePathIsNormalised(t *testing.T) {
	answer := `{"ok":true}`
	w := newEdgeWorld(t, "m", answer)
	defer w.stopRlay()
	ctx := context.Background()
	auth, err := w.client.Authorize(ctx, "m", 1<<10, 1<<10)
	require.NoError(t, err)
	res, err := w.client.Do(ctx, auth, "v1/no/leading/slash", []byte(`{}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.Status)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func nopCloser(b []byte) io.ReadCloser { return io.NopCloser(bytes.NewReader(b)) }

// A reply that is not the JSON the client expects is a read error, not a silent empty result.
func TestAnUnreadableAuthorizationReplyIsAnError(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: nopCloser([]byte("{not json")), Header: http.Header{}}, nil
	})
	c := &Client{Broker: "http://localhost", Key: mustKey(t), HTTP: &http.Client{Transport: rt}}
	_, err := c.Authorize(context.Background(), "m", 1, 1)
	require.ErrorContains(t, err, "could not read")
}

// The default HTTP client is bounded rather than nil - a probe that hangs forever on a
// wedged broker is worse than one that fails.
func TestTheDefaultHTTPClientIsBounded(t *testing.T) {
	c := &Client{}
	require.NotNil(t, c.httpClient())
	require.Positive(t, c.httpClient().Timeout)
}

// edgeConsumerKey is a valid consumer public key for a grant fixture. An edge grant is now
// issued to a consumer, so a target that named none would be refused.
func edgeConsumerKey() ed25519.PublicKey {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return pub
}
