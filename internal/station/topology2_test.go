package station

// topology2_test.go is the CAPSTONE integration test for Option C, Topology 2: a consumer's
// sealed request travels through a REAL tower hub (HTTP), is pulled by a REAL node worker
// (towerhub.ServeLoop) running the REAL sealed executor (ServeSealed), and the sealed answer +
// token receipt come back - with the tower provably carrying only bytes it cannot read. The
// broker appears nowhere: authorize/settle are Core's, and the payload never touches it.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
	"rogerai.fm/roger/v5/internal/towercore/envelope"
	"rogerai.fm/roger/v5/internal/towerhub"
)

// sealedAdapter bridges EdgeExecutor.ServeSealed to the towerhub Executor seam.
type sealedAdapter struct{ e EdgeExecutor }

func (a sealedAdapter) Serve(ctx context.Context, grant, env []byte) ([]byte, []byte, string) {
	return a.e.ServeSealed(ctx, grant, env)
}

func TestTopology2BlindPathEndToEnd(t *testing.T) {
	now := time.Now()

	// --- CORE: mints the grant (authorize) and pins the keys. ---
	corePub, corePriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	reg := dispatch.NewWithStore(dispatch.Config{
		Network: "roger-public", Signer: corePriv, Lifetime: time.Minute,
		Now: func() time.Time { return now },
	}, nil)

	// --- THE NODE: a roger share station with its session key + a model upstream. ---
	s := initStation(t)
	up := &stubUpstream{body: []byte(`{"choices":[{"text":"the answer"}],"usage":{"prompt_tokens":9,"completion_tokens":4}}`)}
	exec := EdgeExecutor{Station: s, CoreKey: corePub, Network: "roger-public", Upstream: up,
		Outbox: NewOutbox(8), Now: func() time.Time { return now }}

	// --- THE TOWER: the blind hub, over real HTTP. Its grant check is the REAL EdgeGrantMeta
	// with a real clock and its own tower id (the wiring contract from the P5b audit). ---
	towerSaw := make(chan []byte, 2) // what the tower could observe of the payload
	hub := towerhub.New()
	check := func(grant []byte) (string, string, error) {
		att, st, _, err := dispatch.EdgeGrantMeta(grant, corePub, "roger-public", "tw-1", time.Now())
		return att, st, err
	}
	server := towerhub.NewServer(hub, check, 5*time.Second, 300*time.Millisecond)
	mux := http.NewServeMux()
	mux.HandleFunc(towerhub.PathSubmit, server.Submit)
	mux.HandleFunc(towerhub.PathPoll, server.Poll)
	mux.HandleFunc(towerhub.PathComplete, server.Complete)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	server.RegisterNode(s.StationID, "node-token")

	// The node worker polls the tower and serves.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	node := &towerhub.Client{BaseURL: srv.URL, Token: "node-token", HTTP: &http.Client{Timeout: 5 * time.Second}}
	go func() { _ = towerhub.ServeLoop(ctx, node, s.StationID, sealedAdapter{exec}, nil) }()

	// --- THE CONSUMER: authorizes with Core (grant carries its sealing key), seals the request
	// to the node, submits to the TOWER (not the broker), opens the sealed answer. ---
	consEnvPub, consEnvPriv, err := envelope.NewKey()
	require.NoError(t, err)
	g, err := reg.MintEdge(dispatch.EdgeTarget{
		TowerID: "tw-1", StationID: s.StationID, Model: "m", Modality: "text",
		RelayName: "st.relay.example", MaxIn: 1 << 20, MaxOut: 1 << 20,
		MaxTokIn: 4096, MaxTokOut: 4096,
		AssertionKey: s.AssertionPub(), ConsumerKey: edgeConsumerKey(), ConsumerEnvKey: consEnvPub,
	})
	require.NoError(t, err)

	plaintext := []byte(`{"prompt":"what is the answer"}`)
	sealedReq, err := envelope.SealTo(s.SessionPub(), plaintext, g.AttemptID)
	require.NoError(t, err)
	sealedRaw, err := sealedReq.Marshal()
	require.NoError(t, err)
	towerSaw <- sealedRaw // record the request bytes as the tower receives them

	consumer := &towerhub.Client{BaseURL: srv.URL}
	sctx, sc := context.WithTimeout(context.Background(), 5*time.Second)
	defer sc()
	res, err := consumer.SubmitJob(sctx, g.Signed, sealedRaw)
	require.NoError(t, err)
	require.Empty(t, res.Failure)
	towerSaw <- res.Envelope // and the result bytes as the tower returned them

	// The consumer opens the sealed answer - the model's own bytes.
	sealedRes, err := envelope.Parse(res.Envelope)
	require.NoError(t, err)
	body, err := envelope.OpenWith(consEnvPriv, sealedRes, g.AttemptID)
	require.NoError(t, err)
	require.Equal(t, up.body, body)

	// The token receipt verifies against the node's key, ready for Core to settle per-token.
	rec, err := dispatch.ParseReceipt(res.Receipt, s.AssertionPub(), "roger-public", g.AttemptID, s.StationID)
	require.NoError(t, err)
	require.Equal(t, dispatch.Usage{In: 9, Out: 4}, rec.TokUsage)

	// --- BLINDNESS, proven CRYPTOGRAPHICALLY: the exact wire bytes the tower carried do not
	// open for anyone but their intended recipient. A substring check alone would be vacuous
	// (base64 hides plaintext even from a broken seal); failing to open under a fresh key is
	// the property the design actually rests on. ---
	_, strangerPriv, err := envelope.NewKey()
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		carried := <-towerSaw
		require.NotContains(t, string(carried), "what is the answer", "no plaintext in a string field")
		sealed, perr := envelope.Parse(carried)
		require.NoError(t, perr, "what the tower carries is a well-formed sealed envelope")
		_, oerr := envelope.OpenWith(strangerPriv, sealed, g.AttemptID)
		require.Error(t, oerr, "the tower (or any stranger) cannot open the bytes it carried")
	}

	// And the courier copy of the receipt is queued at the node for settlement.
	pending := exec.Outbox.Collect(8)
	require.Len(t, pending, 1)
	require.Equal(t, g.AttemptID, pending[0].AttemptID)
}

// A consumer with a grant for ANOTHER tower is refused at this tower's door (the P5b tower
// binding, exercised through the real EdgeGrantMeta wiring).
func TestTopology2RejectsAGrantForAnotherTower(t *testing.T) {
	now := time.Now()
	corePub, corePriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	reg := dispatch.NewWithStore(dispatch.Config{
		Network: "roger-public", Signer: corePriv, Lifetime: time.Minute,
		Now: func() time.Time { return now },
	}, nil)
	s := initStation(t)

	hub := towerhub.New()
	check := func(grant []byte) (string, string, error) {
		att, st, _, err := dispatch.EdgeGrantMeta(grant, corePub, "roger-public", "tw-1", time.Now())
		return att, st, err
	}
	server := towerhub.NewServer(hub, check, 2*time.Second, 200*time.Millisecond)
	mux := http.NewServeMux()
	mux.HandleFunc(towerhub.PathSubmit, server.Submit)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	server.RegisterNode(s.StationID, "tok")

	envPub, _, err := envelope.NewKey()
	require.NoError(t, err)
	g, err := reg.MintEdge(dispatch.EdgeTarget{
		TowerID: "tw-OTHER", StationID: s.StationID, Model: "m", Modality: "text",
		RelayName: "st.relay.example", MaxIn: 1024, MaxOut: 1024,
		AssertionKey: s.AssertionPub(), ConsumerKey: edgeConsumerKey(), ConsumerEnvKey: envPub,
	})
	require.NoError(t, err)

	consumer := &towerhub.Client{BaseURL: srv.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = consumer.SubmitJob(ctx, g.Signed, []byte("sealed"))
	var he *towerhub.HTTPError
	require.ErrorAs(t, err, &he)
	require.Equal(t, http.StatusForbidden, he.Status, "a grant minted for another tower is refused here")
}
