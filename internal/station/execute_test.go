package station

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
	"rogerai.fm/roger/v5/internal/towercore/envelope"
)

const execNetwork = "roger-public"

// stubUpstream is the local model, without a local model.
type stubUpstream struct {
	body []byte
	err  error
	saw  []byte
}

func (u *stubUpstream) Serve(_ context.Context, req []byte) ([]byte, error) {
	u.saw = append([]byte(nil), req...)
	return u.body, u.err
}

// core stands in for Roger Core: it holds the grant key and issues real grants.
type core struct {
	pub ed25519.PublicKey
	reg *dispatch.Registry
}

func newCore(t *testing.T) *core {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return &core{pub: pub, reg: dispatch.New(dispatch.Config{
		Network: execNetwork, Signer: priv, Lifetime: time.Minute,
	})}
}

func (c *core) grantFor(t *testing.T, s *Station, request []byte) dispatch.Grant {
	t.Helper()
	g, err := c.reg.Issue(dispatch.Target{
		TowerID: "tw-1", StationID: s.StationID, StationEpoch: 1,
		Model: "m1", Modality: "text", AssertionKey: s.AssertionPub(),
	}, request)
	require.NoError(t, err)
	return g
}

// coreEnvelope is the keypair results come home to, as Core holds it.
func coreEnvelope(t *testing.T) (pub, priv []byte) {
	t.Helper()
	pub, priv, err := envelope.NewKey()
	require.NoError(t, err)
	return pub, priv
}

// sealFor is what Core does before handing work to a Tower.
func sealFor(t *testing.T, s *Station, attemptID string, request []byte) json.RawMessage {
	t.Helper()
	sealed, err := envelope.SealTo(s.SessionPub(), request, attemptID)
	require.NoError(t, err)
	raw, err := sealed.Marshal()
	require.NoError(t, err)
	return raw
}

func execStation(t *testing.T) *Station {
	t.Helper()
	s, err := Init(filepath.Join(t.TempDir(), "st"))
	require.NoError(t, err)
	return s
}

// The whole point, end to end: an authorized request is served, and the receipt Core gets
// back is one Core will accept.
func TestAnAuthorizedRequestIsServedAndSignedForAcceptably(t *testing.T) {
	c := newCore(t)
	s := execStation(t)
	request := []byte(`{"model":"m1","messages":[]}`)
	g := c.grantFor(t, s, request)
	_, err := c.reg.Claim(g.AttemptID, "tw-1")
	require.NoError(t, err)

	envPub, envPriv := coreEnvelope(t)
	up := &stubUpstream{body: []byte(`{"choices":[{"message":{"content":"hello"}}]}`)}
	got := Executor{
		Station: s, CoreKey: c.pub, CoreEnvelopeKey: envPub, Network: execNetwork, Upstream: up,
	}.Execute(context.Background(), ExecuteRequest{
		Grant: g.Signed, Envelope: sealFor(t, s, g.AttemptID, request),
	})

	require.Empty(t, got.Failure)
	require.NotNil(t, got.Receipt)
	require.Equal(t, request, up.saw, "the model was given exactly what the grant authorized")

	// Core opens the result and only then checks it.
	sealed, err := envelope.Parse(got.Envelope)
	require.NoError(t, err)
	body, err := envelope.OpenWith(envPriv, sealed, g.AttemptID)
	require.NoError(t, err)
	require.Equal(t, up.body, body)

	// CORE ACCEPTS IT. Verified through Core's own Complete rather than by re-checking the
	// signature here: a receipt this package is happy with and Core is not would be worth
	// nothing, and only this assertion can tell the two apart.
	_, err = c.reg.Complete(g.AttemptID, *got.Receipt, body)
	require.NoError(t, err)
}

// A Station with no pinned Core key refuses everything. Serving anyway would make every
// other check theatre - it would have no way to tell a real grant from one the relay wrote.
func TestAStationWithNoPinnedKeyRefusesEverything(t *testing.T) {
	c := newCore(t)
	s := execStation(t)
	request := []byte(`{"x":1}`)
	g := c.grantFor(t, s, request)
	up := &stubUpstream{body: []byte(`{}`)}

	got := Executor{Station: s, Network: execNetwork, Upstream: up}.
		Execute(context.Background(), ExecuteRequest{
			Grant: g.Signed, Envelope: sealFor(t, s, g.AttemptID, request),
		})
	require.Contains(t, got.Failure, "no pinned Roger Core key")
	require.Nil(t, got.Receipt)
	require.Nil(t, up.saw, "and it did not run the request while deciding")
}

// EVERY REFUSAL RETURNS NO RECEIPT AND RUNS NOTHING. Both halves matter: a receipt would be
// something Core might accept, and running first would mean a relay could spend a Station's
// compute with an authorization it does not have.
func TestARefusedRequestIsNeverRunAndNeverSigned(t *testing.T) {
	c := newCore(t)
	other := newCore(t)
	s := execStation(t)
	request := []byte(`{"x":1}`)
	g := c.grantFor(t, s, request)

	envPub, _ := coreEnvelope(t)
	for name, in := range map[string]struct {
		key      ed25519.PublicKey
		grant    json.RawMessage
		envelope json.RawMessage
	}{
		"a grant Core did not sign":       {other.pub, g.Signed, sealFor(t, s, g.AttemptID, request)},
		"a request the grant disowns":     {c.pub, g.Signed, sealFor(t, s, g.AttemptID, []byte(`{"x":2}`))},
		"something that is not a grant":   {c.pub, json.RawMessage(`{nope`), sealFor(t, s, g.AttemptID, request)},
		"an envelope that is not one":     {c.pub, g.Signed, json.RawMessage(`{"nope":1}`)},
		"an envelope for another attempt": {c.pub, g.Signed, sealFor(t, s, "att-somebody-else", request)},
	} {
		t.Run(name, func(t *testing.T) {
			up := &stubUpstream{body: []byte(`{}`)}
			got := Executor{
				Station: s, CoreKey: in.key, CoreEnvelopeKey: envPub,
				Network: execNetwork, Upstream: up,
			}.Execute(context.Background(), ExecuteRequest{Grant: in.grant, Envelope: in.envelope})
			require.NotEmpty(t, got.Failure)
			require.Nil(t, got.Receipt, "a refusal must not be signable as a result")
			require.Nil(t, up.saw, "a refused request must not reach the model")
		})
	}
}

// A grant for another Station is somebody else's authorization, however valid.
func TestAGrantForAnotherStationIsRefused(t *testing.T) {
	c := newCore(t)
	mine := execStation(t)
	theirs := execStation(t)
	request := []byte(`{"x":1}`)
	g := c.grantFor(t, theirs, request)

	envPub, _ := coreEnvelope(t)
	up := &stubUpstream{body: []byte(`{}`)}
	got := Executor{
		Station: mine, CoreKey: c.pub, CoreEnvelopeKey: envPub, Network: execNetwork, Upstream: up,
	}.Execute(context.Background(), ExecuteRequest{
		Grant: g.Signed, Envelope: sealFor(t, mine, g.AttemptID, request),
	})
	require.Contains(t, got.Failure, "not this one")
	require.Nil(t, up.saw)
}

// A model that fails is reported in its own words - an operator debugging a Station needs
// what the model actually said - and produces no receipt.
func TestAModelFailureIsReportedWithoutAReceipt(t *testing.T) {
	c := newCore(t)
	s := execStation(t)
	request := []byte(`{"x":1}`)
	g := c.grantFor(t, s, request)

	envPub, _ := coreEnvelope(t)
	up := &stubUpstream{err: errors.New("out of memory")}
	got := Executor{
		Station: s, CoreKey: c.pub, CoreEnvelopeKey: envPub, Network: execNetwork, Upstream: up,
	}.Execute(context.Background(), ExecuteRequest{
		Grant: g.Signed, Envelope: sealFor(t, s, g.AttemptID, request),
	})
	require.Contains(t, got.Failure, "out of memory")
	require.Nil(t, got.Receipt)
}

func TestAStationWithNoUpstreamSaysSo(t *testing.T) {
	c := newCore(t)
	s := execStation(t)
	request := []byte(`{"x":1}`)
	g := c.grantFor(t, s, request)

	envPub, _ := coreEnvelope(t)
	got := Executor{Station: s, CoreKey: c.pub, CoreEnvelopeKey: envPub, Network: execNetwork}.
		Execute(context.Background(), ExecuteRequest{
			Grant: g.Signed, Envelope: sealFor(t, s, g.AttemptID, request),
		})
	require.Contains(t, got.Failure, "no upstream model")
}

// An expired grant is refused by the Station too, not only by Core. The Station is where the
// compute would be spent, so it is the place worth refusing at.
func TestAnExpiredGrantIsRefusedBeforeSpendingAnything(t *testing.T) {
	c := newCore(t)
	s := execStation(t)
	request := []byte(`{"x":1}`)
	g := c.grantFor(t, s, request)

	envPub, _ := coreEnvelope(t)
	up := &stubUpstream{body: []byte(`{}`)}
	got := Executor{
		Station: s, CoreKey: c.pub, CoreEnvelopeKey: envPub, Network: execNetwork, Upstream: up,
		Now: func() time.Time { return g.Deadline.Add(time.Second) },
	}.Execute(context.Background(), ExecuteRequest{
		Grant: g.Signed, Envelope: sealFor(t, s, g.AttemptID, request),
	})
	require.NotEmpty(t, got.Failure)
	require.Nil(t, up.saw)
}

// --- the upstream itself ---------------------------------------------------

func TestTheHTTPUpstreamPassesTheRequestThroughUnchanged(t *testing.T) {
	var saw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		saw, _ = readAll(r)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	body, err := HTTPUpstream{URL: srv.URL}.Serve(context.Background(), []byte(`{"a":1}`))
	require.NoError(t, err)
	require.Equal(t, `{"ok":true}`, string(body))
	require.Equal(t, `{"a":1}`, string(saw), "the model must see the exact authorized bytes")
}

// An upstream that errors, or answers with nothing, must not be reported as a result. An
// empty body signed as an answer is a Station attesting to having served nothing.
func TestTheHTTPUpstreamRefusesAnUnusableAnswer(t *testing.T) {
	t.Run("an error status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`model exploded`))
		}))
		defer srv.Close()
		_, err := HTTPUpstream{URL: srv.URL}.Serve(context.Background(), []byte(`{}`))
		require.Error(t, err)
		require.Contains(t, err.Error(), "500")
		require.Contains(t, err.Error(), "model exploded")
	})

	t.Run("an empty body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
		defer srv.Close()
		_, err := HTTPUpstream{URL: srv.URL}.Serve(context.Background(), []byte(`{}`))
		require.Error(t, err)
		require.Contains(t, err.Error(), "empty")
	})

	t.Run("nothing listening", func(t *testing.T) {
		_, err := HTTPUpstream{URL: "http://127.0.0.1:1"}.Serve(context.Background(), []byte(`{}`))
		require.Error(t, err)
	})

	t.Run("not a URL", func(t *testing.T) {
		_, err := HTTPUpstream{URL: "://nope"}.Serve(context.Background(), []byte(`{}`))
		require.Error(t, err)
	})
}

// A long error from the model is truncated rather than logged whole: it reaches an operator
// through a chain of error strings, and a megabyte of HTML in one of them helps nobody.
func TestALongUpstreamErrorIsTruncated(t *testing.T) {
	long := make([]byte, 4096)
	for i := range long {
		long[i] = 'x'
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write(long)
	}))
	defer srv.Close()
	_, err := HTTPUpstream{URL: srv.URL}.Serve(context.Background(), []byte(`{}`))
	require.Error(t, err)
	require.Less(t, len(err.Error()), 400)
	require.Contains(t, err.Error(), "…")
}

func readAll(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	buf := make([]byte, 0, 512)
	tmp := make([]byte, 512)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf, nil
		}
	}
}

// Option C: the Station signs the model's own token counts into the receipt, so Core can bill
// per-token on the blind path. A body carrying a usage object yields a token claim.
func TestTheStationSignsTheModelsTokenUsage(t *testing.T) {
	c := newCore(t)
	s := execStation(t)
	request := []byte(`{"model":"m1","messages":[]}`)
	g := c.grantFor(t, s, request)
	_, err := c.reg.Claim(g.AttemptID, "tw-1")
	require.NoError(t, err)
	envPub, _ := coreEnvelope(t)
	up := &stubUpstream{body: []byte(`{"choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":12,"completion_tokens":7}}`)}
	got := Executor{Station: s, CoreKey: c.pub, CoreEnvelopeKey: envPub, Network: execNetwork, Upstream: up}.
		Execute(context.Background(), ExecuteRequest{Grant: g.Signed, Envelope: sealFor(t, s, g.AttemptID, request)})
	require.Empty(t, got.Failure)
	require.NotNil(t, got.Receipt)
	require.Equal(t, dispatch.Usage{In: 12, Out: 7}, got.Receipt.TokUsage,
		"the model's token usage is signed into the receipt for per-token billing")
}

func TestTokenUsageOfParsesUsageAndDefaultsToZero(t *testing.T) {
	require.Equal(t, dispatch.Usage{In: 5, Out: 9},
		tokenUsageOf([]byte(`{"usage":{"prompt_tokens":5,"completion_tokens":9}}`)))
	require.Equal(t, dispatch.Usage{}, tokenUsageOf([]byte(`{"choices":[]}`)), "no usage object -> zero")
	require.Equal(t, dispatch.Usage{}, tokenUsageOf([]byte(`not json at all`)), "non-JSON -> zero")
	require.Equal(t, dispatch.Usage{},
		tokenUsageOf([]byte(`{"usage":{"prompt_tokens":-3,"completion_tokens":-1}}`)), "negatives clamp to zero")
	require.Equal(t, dispatch.Usage{In: 0, Out: 5},
		tokenUsageOf([]byte(`{"usage":{"prompt_tokens":-3,"completion_tokens":5}}`)), "one negative field clamps alone")
}
