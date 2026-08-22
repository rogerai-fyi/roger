package station

// edge_test.go covers the Station serving a consumer directly.
//
// Contract: features/tower/edge_dispatch.feature.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/towercore/dispatch"
)

type fixedUpstream struct {
	body []byte
	err  error
	saw  []byte
}

func (u *fixedUpstream) Serve(_ context.Context, request []byte) ([]byte, error) {
	u.saw = request
	if u.err != nil {
		return nil, u.err
	}
	return u.body, nil
}

// edgeSetup builds a Station, a Core registry, and a grant naming that Station.
func edgeSetup(t *testing.T, now time.Time, mutate func(*dispatch.EdgeTarget)) (
	EdgeExecutor, *fixedUpstream, string) {
	t.Helper()

	s := initStation(t)
	corePub, corePriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	reg := dispatch.NewWithStore(dispatch.Config{
		Network: "roger-public", Signer: corePriv, Lifetime: time.Minute,
		Now: func() time.Time { return now },
	}, nil)

	tgt := dispatch.EdgeTarget{
		TowerID: "tw-1", StationID: s.StationID, Model: "m", Modality: "text",
		RelayName: "st.relay.example", MaxIn: 4096, MaxOut: 4096,
		AssertionKey: s.AssertionPub(), ConsumerKey: edgeConsumerKey(),
	}
	if mutate != nil {
		mutate(&tgt)
	}
	g, err := reg.MintEdge(tgt)
	require.NoError(t, err)

	up := &fixedUpstream{body: []byte(`{"choices":[{"text":"hello"}]}`)}
	return EdgeExecutor{
		Station: s, CoreKey: corePub, Network: "roger-public", Upstream: up,
		Now: func() time.Time { return now },
	}, up, base64.StdEncoding.EncodeToString(g.Signed)
}

// The body a consumer gets back is the MODEL'S OWN. That is the whole compatibility claim:
// anything that can talk to an OpenAI-compatible endpoint can use a Tower without knowing
// any of this exists.
func TestAConsumerGetsTheModelsBytesUnchangedPlusEvidenceAlongside(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, up, grant := edgeSetup(t, now, nil)

	resp := e.Serve(context.Background(), EdgeRequest{Grant: grant, Body: []byte(`{"prompt":"hi"}`)})
	require.Empty(t, resp.Failure)
	require.Equal(t, 200, resp.Status)
	require.Equal(t, up.body, resp.Body, "the consumer must get the model's own bytes")
	require.Equal(t, []byte(`{"prompt":"hi"}`), up.saw, "the model must get the consumer's own bytes")

	// And the evidence is real: it verifies against this Station's attachment-recorded key
	// and commits to exactly what was returned.
	raw, err := base64.StdEncoding.DecodeString(resp.Receipt)
	require.NoError(t, err)
	require.NotEmpty(t, raw)
}

// A refusal must never carry a receipt. A receipt is what settles an attempt, and a Station
// that signed for work it did not do would be signing money into existence.
func TestARefusedEdgeRequestIsNeverRunAndNeverSigned(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	for name, tc := range map[string]struct {
		mutate func(*EdgeExecutor)
		req    EdgeRequest
		status int
		says   string
	}{
		"no pinned key": {
			mutate: func(e *EdgeExecutor) { e.CoreKey = nil },
			req:    EdgeRequest{Body: []byte("x")}, status: 500, says: "no pinned Roger Core key",
		},
		"no grant": {
			req: EdgeRequest{Body: []byte("x")}, status: 401, says: "no Roger Core grant",
		},
		"grant is not base64": {
			req: EdgeRequest{Grant: "!!!!", Body: []byte("x")}, status: 400, says: "not valid base64",
		},
		"grant is not a grant": {
			req: EdgeRequest{
				Grant: base64.StdEncoding.EncodeToString([]byte("{}")),
				Body:  []byte("x"),
			}, status: 403, says: "not signed by Roger Core",
		},
	} {
		t.Run(name, func(t *testing.T) {
			e, up, _ := edgeSetup(t, now, nil)
			if tc.mutate != nil {
				tc.mutate(&e)
			}
			resp := e.Serve(context.Background(), tc.req)
			require.Equal(t, tc.status, resp.Status)
			require.Contains(t, resp.Failure, tc.says)
			require.Empty(t, resp.Receipt, "a refusal must not be signed for")
			require.Nil(t, up.saw, "a refused request must never reach the model")
		})
	}
}

func TestAnEmptyEdgeRequestIsRefused(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, up, grant := edgeSetup(t, now, nil)
	resp := e.Serve(context.Background(), EdgeRequest{Grant: grant})
	require.Equal(t, 400, resp.Status)
	require.Nil(t, up.saw)
}

// A valid grant for a DIFFERENT Station is somebody else's authorization, and pointing it
// here is exactly what a relay is positioned to do.
func TestAnEdgeGrantForAnotherStationIsRefusedHere(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, up, grant := edgeSetup(t, now, func(tgt *dispatch.EdgeTarget) {
		tgt.StationID = "st-somebody-else"
	})
	resp := e.Serve(context.Background(), EdgeRequest{Grant: grant, Body: []byte("x")})
	require.Equal(t, 403, resp.Status)
	require.Contains(t, resp.Failure, "not this one")
	require.Nil(t, up.saw)
}

func TestAnExpiredEdgeGrantIsRefusedBeforeSpendingAnything(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, up, grant := edgeSetup(t, now, nil)
	e.Now = func() time.Time { return now.Add(2 * time.Minute) }

	resp := e.Serve(context.Background(), EdgeRequest{Grant: grant, Body: []byte("x")})
	require.Equal(t, 403, resp.Status)
	require.Contains(t, resp.Failure, "expired")
	require.Nil(t, up.saw)
}

// The input ceiling is what the request digest used to be, and it must bite BEFORE the model
// is asked to spend anything on it.
func TestARequestOverTheGrantsCeilingNeverReachesTheModel(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, up, grant := edgeSetup(t, now, func(tgt *dispatch.EdgeTarget) { tgt.MaxIn = 16 })

	resp := e.Serve(context.Background(), EdgeRequest{Grant: grant, Body: make([]byte, 17)})
	require.Equal(t, 403, resp.Status)
	require.Contains(t, resp.Failure, "the grant allows 16")
	require.Nil(t, up.saw)
}

// OUTPUT IS THE EXPENSIVE DIRECTION. Without this check the output bound would be a number
// Core wrote down and nobody enforced.
func TestAnAnswerOverTheGrantsCeilingIsNotSignedFor(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, up, grant := edgeSetup(t, now, func(tgt *dispatch.EdgeTarget) { tgt.MaxOut = 4 })
	up.body = []byte("far too much output for this grant")

	resp := e.Serve(context.Background(), EdgeRequest{Grant: grant, Body: []byte("x")})
	require.Equal(t, 502, resp.Status)
	require.Contains(t, resp.Failure, "more than this grant allows")
	require.Empty(t, resp.Receipt)
}

func TestAModelFailureIsReportedAsTheModelsFailure(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, up, grant := edgeSetup(t, now, nil)
	up.err = errors.New("connection refused")

	resp := e.Serve(context.Background(), EdgeRequest{Grant: grant, Body: []byte("x")})
	require.Equal(t, 502, resp.Status)
	require.Contains(t, resp.Failure, "the model did not answer")
	require.Contains(t, resp.Failure, "connection refused")
	require.Empty(t, resp.Receipt)
}

func TestAStationWithNoUpstreamRefusesTheEdgePath(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, _, grant := edgeSetup(t, now, nil)
	e.Upstream = nil

	resp := e.Serve(context.Background(), EdgeRequest{Grant: grant, Body: []byte("x")})
	require.Equal(t, 500, resp.Status)
	require.Contains(t, resp.Failure, "no upstream model")
}

func TestTheEdgeExecutorHasARealClockByDefault(t *testing.T) {
	e, _, _ := edgeSetup(t, time.Now(), nil)
	e.Now = nil
	require.WithinDuration(t, time.Now(), e.now(), time.Minute)
}

// The executor signs a transcript on demand for an attempt it kept, and reports "not kept"
// for one it did not - which the audit treats as cannot-produce.
func TestTheExecutorSignsTranscriptsOnDemand(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, _, grant := edgeSetup(t, now, nil)
	e.Transcripts = NewTranscripts(10, 1)

	resp := e.Serve(ctxTODO(), EdgeRequest{Grant: grant, Body: []byte(`{"p":"x"}`)})
	require.Equal(t, 200, resp.Status)

	// The attempt id is inside the grant; recover it from the receipt for the lookup.
	rawReceipt, err := base64.StdEncoding.DecodeString(resp.Receipt)
	require.NoError(t, err)
	var rec struct {
		AttemptID      string `json:"attempt_id"`
		RequestDigest  string `json:"request_digest"`
		ResponseDigest string `json:"response_digest"`
	}
	require.NoError(t, json.Unmarshal(rawReceipt, &rec))

	tr, ok, err := e.Transcript(rec.AttemptID)
	require.NoError(t, err)
	require.True(t, ok)
	// The transcript's digests match the receipt's, by construction - both hash the same bytes.
	require.Equal(t, rec.RequestDigest, tr.RequestDigest)
	require.Equal(t, rec.ResponseDigest, tr.ResponseDigest)
	require.NoError(t, tr.VerifyBytes([]byte(`{"p":"x"}`), resp.Body))

	// One it never kept.
	_, ok, err = e.Transcript("att-never")
	require.NoError(t, err)
	require.False(t, ok)

	// And with no transcript store at all, it simply produces nothing.
	e.Transcripts = nil
	_, ok, err = e.Transcript("anything")
	require.NoError(t, err)
	require.False(t, ok)
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

// Option C on the BLIND edge path (the metered path that becomes tower_relay money): the edge
// Station signs the model's token usage into the receipt, so Core can bill per-token.
func TestTheEdgeStationSignsTheModelsTokenUsage(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := initStation(t)
	corePub, corePriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	reg := dispatch.NewWithStore(dispatch.Config{
		Network: "roger-public", Signer: corePriv, Lifetime: time.Minute,
		Now: func() time.Time { return now },
	}, nil)
	g, err := reg.MintEdge(dispatch.EdgeTarget{
		TowerID: "tw-1", StationID: s.StationID, Model: "m", Modality: "text",
		RelayName: "st.relay.example", MaxIn: 4096, MaxOut: 4096,
		AssertionKey: s.AssertionPub(), ConsumerKey: edgeConsumerKey(),
	})
	require.NoError(t, err)
	up := &fixedUpstream{body: []byte(`{"choices":[{"text":"hi"}],"usage":{"prompt_tokens":8,"completion_tokens":3}}`)}
	e := EdgeExecutor{Station: s, CoreKey: corePub, Network: "roger-public", Upstream: up,
		Now: func() time.Time { return now }}

	resp := e.Serve(context.Background(), EdgeRequest{
		Grant: base64.StdEncoding.EncodeToString(g.Signed), Body: []byte(`{"prompt":"hi"}`)})
	require.Empty(t, resp.Failure)
	raw, err := base64.StdEncoding.DecodeString(resp.Receipt)
	require.NoError(t, err)
	rec, err := dispatch.ParseReceipt(raw, s.AssertionPub(), "roger-public", g.AttemptID, s.StationID)
	require.NoError(t, err)
	require.Equal(t, dispatch.Usage{In: 8, Out: 3}, rec.TokUsage,
		"the blind edge Station signs the model's token usage for per-token billing")
}
