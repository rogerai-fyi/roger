package station

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
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
