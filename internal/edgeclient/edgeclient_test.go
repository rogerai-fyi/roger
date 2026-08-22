package edgeclient

// edgeclient_test.go drives the whole SEALED consumer loop against a real serving node
// behind a real tower hub, and a Core stub that records what the acknowledgement carried.
// (The raw-TLS relay rig this file used to build died with the leaf-station generation.)
//
// Contract: features/tower/edge_dispatch.feature.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/station"
	"rogerai.fm/roger/v6/internal/towercore/dispatch"
	"rogerai.fm/roger/v6/internal/towercore/link"
	"rogerai.fm/roger/v6/internal/towerhub"
)

// sealedWorld stands up the whole path: Core (authorize + ack), a node blind-serving through
// a real tower hub, and a client pointed at Core.
type sealedWorld struct {
	client *Client
	acked  []dispatch.Ack
	mu     sync.Mutex
	cancel func()
}

func newSealedWorld(t *testing.T, model, answer string) *sealedWorld {
	t.Helper()
	return newSealedWorldWith(t, model, answer, false)
}

// newSealedWorldWith is the same world with the tower hub optionally behind REAL TLS, pinned the
// way Core pins it in production: the tower states its certificate's fingerprint, Core relays it
// in the authorize answer, and the consumer accepts that certificate and no other.
//
// The TLS variant exists because the consumer's leg reaches the hub through a completely
// different call site from the node's, in a different package, and "we threaded the pin" is a
// claim only an end-to-end submit can settle.
func newSealedWorldWith(t *testing.T, model, answer string, hubTLS bool) *sealedWorld {
	t.Helper()

	corePub, corePriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	st, err := station.Init(t.TempDir())
	require.NoError(t, err)
	exec := station.EdgeExecutor{
		Station: st, CoreKey: corePub, Network: link.PublicNetwork,
		Upstream: fixedUpstream([]byte(answer)),
		Outbox:   station.NewOutbox(16),
		Seen:     station.NewAttemptCache(),
	}

	// The tower hub, with the node's serve loop polling it.
	hub := towerhub.NewServer(towerhub.New(), func(grant []byte) (string, string, error) {
		att, stationID, _, gerr := dispatch.EdgeGrantMeta(grant, corePub, link.PublicNetwork, "tw-1", time.Now())
		if gerr != nil {
			return "", "", gerr
		}
		return att, stationID, nil
	}, towerhub.ServerOptions{TowerID: "tw-1", SubmitTTL: 10 * time.Second, PollTTL: 300 * time.Millisecond})
	hub.RegisterNode(st.StationID, towerhub.NodeAuth{AssertionKey: st.AssertionPub()})
	mux := http.NewServeMux()
	mux.HandleFunc(towerhub.PathSubmit, hub.Submit)
	mux.HandleFunc(towerhub.PathPoll, hub.Poll)
	mux.HandleFunc(towerhub.PathComplete, hub.Complete)
	hubSrv := httptest.NewUnstartedServer(mux)
	var hubPin string
	if hubTLS {
		cert := selfSignedHubCert(t)
		hubSrv.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}}
		hubSrv.StartTLS()
		hubPin = towerhub.CertPin(cert.Leaf)
	} else {
		hubSrv.Start()
	}
	t.Cleanup(hubSrv.Close)
	// THE NODE'S LEG GOES THROUGH THE SAME HELPER THE PRODUCT USES, so this world cannot
	// accidentally prove the consumer's leg works while the node's is left dialling plaintext.
	nodeBase, nodeHTTP, err := towerhub.Reach(hubSrv.Listener.Addr().String(), hubPin, nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	nodeClient := &towerhub.Client{BaseURL: nodeBase, TowerID: "tw-1",
		TowerKeyHash: hub.EpochKeyHash(), Sign: st.SignRequest, HTTP: nodeHTTP}
	go func() {
		_ = towerhub.ServeLoop(ctx, nodeClient, st.StationID, sealedServe{exec}, nil)
	}()

	w := &sealedWorld{cancel: cancel}
	reg := dispatch.NewWithStore(dispatch.Config{
		Network: link.PublicNetwork, Signer: corePriv, Lifetime: time.Minute,
	}, nil)

	// Core: authorize mints against the hub endpoint; ack verifies against the request key.
	coreMux := http.NewServeMux()
	coreMux.HandleFunc("/tower/edge/authorize", func(rw http.ResponseWriter, r *http.Request) {
		require.NotEmpty(t, r.Header.Get("X-Roger-Pubkey"), "authorize must be signed")
		var req struct {
			ConsumerEnvKey string `json:"consumer_env_key"`
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		require.NoError(t, json.Unmarshal(body, &req))
		envKey, kerr := hex.DecodeString(req.ConsumerEnvKey)
		require.NoError(t, kerr)
		g, gerr := reg.MintEdge(dispatch.EdgeTarget{
			TowerID: "tw-1", StationID: st.StationID, Model: model, Modality: "text",
			RelayName: "st-1.relay.example", MaxIn: 1 << 20, MaxOut: 1 << 20,
			AssertionKey: st.AssertionPub(), ConsumerKey: edgeConsumerKey(),
			ConsumerEnvKey: envKey, PriceOutMicros: 300_000,
		})
		require.NoError(t, gerr)
		writeJSON(rw, map[string]any{
			"attempt_id": g.AttemptID, "grant": base64.StdEncoding.EncodeToString(g.Signed),
			"endpoint":            hubSrv.Listener.Addr().String(),
			"endpoint_tls_spki":   hubPin,
			"station_session_key": hex.EncodeToString(st.SessionPub()),
			"price_out_micros":    300_000,
		})
	})
	coreMux.HandleFunc("/tower/edge/ack", func(rw http.ResponseWriter, r *http.Request) {
		var req struct {
			AttemptID string `json:"attempt_id"`
			Ack       string `json:"ack"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		raw, _ := base64.StdEncoding.DecodeString(req.Ack)
		pub, _ := hex.DecodeString(r.Header.Get("X-Roger-Pubkey"))
		ack, aerr := dispatch.ParseAck(raw, pub, link.PublicNetwork, req.AttemptID)
		require.NoError(t, aerr)
		w.mu.Lock()
		w.acked = append(w.acked, ack)
		w.mu.Unlock()
		writeJSON(rw, map[string]any{"recorded": true})
	})
	coreSrv := httptest.NewServer(coreMux)
	t.Cleanup(coreSrv.Close)

	_, clientKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	w.client = &Client{Broker: coreSrv.URL, Key: clientKey}
	return w
}

type sealedServe struct{ e station.EdgeExecutor }

func (s sealedServe) Serve(ctx context.Context, grant, env []byte) ([]byte, []byte, string) {
	return s.e.ServeSealed(ctx, grant, env)
}

// THE WHOLE CONSUMER LOOP: authorize, serve sealed through the hub, acknowledge - and the
// acknowledgement Core received matches the bytes the node actually returned.
func TestTheClientAuthorizesServesAndAcknowledges(t *testing.T) {
	answer := `{"choices":[{"text":"the answer"}]}`
	w := newSealedWorld(t, "m", answer)
	defer w.cancel()

	ctx := context.Background()
	auth, err := w.client.AuthorizeSealed(ctx, "m")
	require.NoError(t, err)
	require.NotEmpty(t, auth.AttemptID)
	require.EqualValues(t, 300_000, auth.PriceOutMicros, "the pinned price is echoed")

	res, err := w.client.DoSealed(ctx, &auth, []byte(`{"prompt":"hi"}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.Status)
	require.JSONEq(t, answer, string(res.Body), "the consumer gets the model's own bytes, opened")

	require.NoError(t, w.client.AckSealed(ctx, &auth, res))

	w.mu.Lock()
	defer w.mu.Unlock()
	require.Len(t, w.acked, 1)
	require.Equal(t, auth.AttemptID, w.acked[0].AttemptID)
	require.Equal(t, int64(len(answer)), w.acked[0].Usage.Out,
		"the acknowledgement must commit to exactly what was received")
}

// A spent authorization cannot be replayed: the opening key is zeroed on success.
func TestASpentAuthorizationCannotBeReused(t *testing.T) {
	w := newSealedWorld(t, "m", `{"ok":true}`)
	defer w.cancel()
	ctx := context.Background()
	auth, err := w.client.AuthorizeSealed(ctx, "m")
	require.NoError(t, err)
	_, err = w.client.DoSealed(ctx, &auth, []byte(`{}`))
	require.NoError(t, err)
	_, err = w.client.DoSealed(ctx, &auth, []byte(`{}`))
	require.ErrorContains(t, err, "already used")
}

func TestAuthorizeNeedsAKey(t *testing.T) {
	c := &Client{Broker: "http://127.0.0.1:1"}
	_, err := c.AuthorizeSealed(context.Background(), "m")
	require.ErrorContains(t, err, "signing key")
}

// An acknowledgement of a non-answer is a no-op, not an error: there is nothing to
// corroborate, and signing a claim about an error body would be a lie.
func TestThereIsNothingToAcknowledgeForARefusal(t *testing.T) {
	c := &Client{Broker: "http://127.0.0.1:1", Key: mustKey(t)}
	require.NoError(t, c.AckSealed(context.Background(), &SealedAuthorization{AttemptID: "a"},
		Result{Status: http.StatusForbidden}))
	require.NoError(t, c.AckSealed(context.Background(), &SealedAuthorization{AttemptID: "a"},
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
	_, err := c.AuthorizeSealed(context.Background(), "m")
	require.ErrorContains(t, err, "no Station can take this")
}

func TestAnIncompleteAuthorizationIsRejected(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/tower/edge/authorize", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, map[string]any{"attempt_id": "a"}) // no grant, endpoint, or session key
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := &Client{Broker: srv.URL, Key: mustKey(t)}
	_, err := c.AuthorizeSealed(context.Background(), "m")
	require.ErrorContains(t, err, "no readable grant")
}

// --- helpers -----------------------------------------------------------------

type fixedUpstream []byte

func (u fixedUpstream) Serve(context.Context, []byte) ([]byte, error) { return []byte(u), nil }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
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
		return &http.Response{StatusCode: 500, Body: nopCloser([]byte("nope")), Header: http.Header{}}, nil
	})
	c := &Client{Broker: "http://localhost", Key: mustKey(t), HTTP: &http.Client{Transport: rt}, Network: "roger-private"}
	require.Equal(t, "roger-private", c.network())
	_, err := c.AuthorizeSealed(context.Background(), "m")
	require.Error(t, err)
	require.True(t, reached, "the custom HTTP client must carry the request")
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func nopCloser(b []byte) io.ReadCloser { return io.NopCloser(bytes.NewReader(b)) }

// A reply that is not the JSON the client expects is a read error, not a silent empty result.
func TestAnUnreadableAuthorizationReplyIsAnError(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: nopCloser([]byte("{not json")), Header: http.Header{}}, nil
	})
	c := &Client{Broker: "http://localhost", Key: mustKey(t), HTTP: &http.Client{Transport: rt}}
	_, err := c.AuthorizeSealed(context.Background(), "m")
	require.ErrorContains(t, err, "could not read")
}

// The default HTTP client is bounded rather than nil - a probe that hangs forever on a
// wedged broker is worse than one that fails.
func TestTheDefaultHTTPClientIsBounded(t *testing.T) {
	c := &Client{}
	require.NotNil(t, c.httpClient())
	require.Positive(t, c.httpClient().Timeout)
}

// edgeConsumerKey is a valid consumer public key for a grant fixture. An edge grant is
// issued to a consumer, so a target that named none would be refused.
func edgeConsumerKey() ed25519.PublicKey {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return pub
}

// selfSignedHubCert mints what a tower mints for itself with --hub-tls: Ed25519, self-signed,
// nameless, and pinned by its public key rather than by anybody's authority.
func selfSignedHubCert(t *testing.T) tls.Certificate {
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

// THE CONSUMER'S LEG OVER A PINNED HUB, END TO END: authorize at Core, submit sealed work over
// verified TLS, open the answer, acknowledge. This is the half of the change most likely to be
// left behind - the node's leg and the consumer's leg reach the same hub from different
// packages - and it is the half that carries the money, since it is the consumer who is billed.
func TestTheConsumerSubmitsSealedWorkOverAPinnedTLSHub(t *testing.T) {
	answer := `{"choices":[{"text":"the answer"}]}`
	w := newSealedWorldWith(t, "m", answer, true)
	defer w.cancel()

	ctx := context.Background()
	auth, err := w.client.AuthorizeSealed(ctx, "m")
	require.NoError(t, err)
	require.True(t, towerhub.ValidPin(auth.EndpointTLSSPKI),
		"Core must hand the consumer the fingerprint the tower advertised")

	res, err := w.client.DoSealed(ctx, &auth, []byte(`{"prompt":"hi"}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.Status)
	require.JSONEq(t, answer, string(res.Body))
	require.NoError(t, w.client.AckSealed(ctx, &auth, res))
}

// AND THE CERTIFICATE IS ACTUALLY CHECKED. Core hands the consumer a pin for a certificate this
// hub does not present - which is the on-path attacker, or a tower that swapped its key - and
// the submit must fail rather than proceed over an encrypted channel to whoever answered.
//
// It fails against a client that dials https with verification disabled: that client submits
// happily, the answer comes back, and the assertion goes red.
func TestASubmitToAHubWithTheWrongCertificateIsRefused(t *testing.T) {
	w := newSealedWorldWith(t, "m", `{"choices":[{"text":"x"}]}`, true)
	defer w.cancel()

	ctx := context.Background()
	auth, err := w.client.AuthorizeSealed(ctx, "m")
	require.NoError(t, err)
	auth.EndpointTLSSPKI = towerhub.CertPin(selfSignedHubCert(t).Leaf)

	_, err = w.client.DoSealed(ctx, &auth, []byte(`{"prompt":"hi"}`))
	require.ErrorIs(t, err, towerhub.ErrHubCertificateUnpinned)
}

// A hub with no pin is reached exactly as it always was. The whole existing suite runs this way,
// which is the compatibility statement; this says it out loud so a future change that makes TLS
// mandatory has to delete a test rather than quietly strand every tower that has not moved.
func TestAnUnpinnedHubIsStillReachedOverPlainHTTP(t *testing.T) {
	w := newSealedWorldWith(t, "m", `{"choices":[{"text":"x"}]}`, false)
	defer w.cancel()
	auth, err := w.client.AuthorizeSealed(context.Background(), "m")
	require.NoError(t, err)
	require.Empty(t, auth.EndpointTLSSPKI)
	_, err = w.client.DoSealed(context.Background(), &auth, []byte(`{"prompt":"hi"}`))
	require.NoError(t, err)
}
