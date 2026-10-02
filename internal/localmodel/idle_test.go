package localmodel

// NOTHING IS UNLOADED BY SURPRISE (local_models.feature §5): an Edge device exists to answer, so a
// model keeps serving by default. An idle timeout may be set, off by default; when it fires the band
// is withdrawn and the event recorded, so a peer that stops finding it can see why.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTheIdleTimeoutIsOffUntilTheOwnerSetsIt(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	s, err := h.Settings()
	require.NoError(t, err)
	require.Zero(t, s.IdleAfter, "off by default: an Edge device exists to answer")

	require.NoError(t, h.SetSettings(Settings{IdleAfter: 2 * time.Hour}))
	s, err = h.Settings()
	require.NoError(t, err)
	require.Equal(t, 2*time.Hour, s.IdleAfter)
	fi, err := os.Stat(filepath.Join(h.Dir, "settings.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())

	require.NoError(t, os.WriteFile(filepath.Join(h.Dir, "settings.json"), []byte("{not json"), 0o600))
	_, err = h.Settings()
	require.Error(t, err, "a damaged setting is said, never read as 'off'")
}

// Idleness is read from the runtime's own counters, not from roger's bookkeeping about requests it
// may never have seen: a peer talks to the runtime through the face, and only the runtime counts.
func TestIdlenessIsTheRuntimesOwnCountersStandingStill(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	w := &IdleWatch{After: time.Hour}
	require.False(t, w.Observe(t0, 100, false), "the first reading is the baseline")
	require.False(t, w.Observe(t0.Add(59*time.Minute), 100, false))
	require.False(t, w.Observe(t0.Add(70*time.Minute), 140, false), "a turn resets the clock")
	require.False(t, w.Observe(t0.Add(129*time.Minute), 140, false))
	require.False(t, w.Observe(t0.Add(131*time.Minute), 140, true), "a request in flight is not idle")
	require.False(t, w.Observe(t0.Add(190*time.Minute), 140, false))
	require.True(t, w.Observe(t0.Add(191*time.Minute), 140, false), "an hour with no turn and nothing in flight")
	require.Equal(t, t0.Add(131*time.Minute), w.Since)

	off := &IdleWatch{}
	require.False(t, off.Observe(t0, 1, false))
	require.False(t, off.Observe(t0.Add(1000*time.Hour), 1, false), "off never fires")
}

func TestTheRuntimesCountersAreReadWithItsKey(t *testing.T) {
	// The exact lines llama-server b11146 serves at /metrics (captured live, after one 5-token reply).
	const metrics = "# HELP llamacpp:prompt_tokens_total Number of prompt tokens processed.\n" +
		"# TYPE llamacpp:prompt_tokens_total counter\nllamacpp:prompt_tokens_total 9\n" +
		"llamacpp:prompt_tokens_cached_total 0\nllamacpp:tokens_predicted_total 5\n" +
		"llamacpp:n_decode_total 5\nllamacpp:requests_processing 1\nllamacpp:requests_deferred 0\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" || r.Header.Get("Authorization") != "Bearer k1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(metrics))
	}))
	defer srv.Close()
	total, busy, err := Activity(t.Context(), Serving{BaseURL: srv.URL + "/v1", Key: "k1"})
	require.NoError(t, err)
	require.Equal(t, int64(14), total, "prompt and predicted tokens together")
	require.True(t, busy)

	_, _, err = Activity(t.Context(), Serving{BaseURL: srv.URL + "/v1", Key: "wrong"})
	require.Error(t, err, "an unreadable counter is an error, never a zero that looks idle")
}

func TestTheRuntimeIsStartedWithItsCountersOn(t *testing.T) {
	require.Contains(t, Args(ServeOpts{Model: Model{Path: "m.gguf", Band: "b"}, Ctx: 4096}, 1234, "k"), "--metrics")
}

// The stop is recorded where the member's report reads it, so the owner on another machine sees why
// the band went quiet.
func TestAnIdleStopIsRecordedWithItsReason(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	_, ok := h.LastStop()
	require.False(t, ok)
	at := time.Date(2026, 10, 1, 14, 2, 0, 0, time.UTC)
	require.NoError(t, h.RecordStop(Stop{Band: "qwen3-4b", ID: "unsloth/Qwen3-4B-GGUF:Q4_K_M", At: at, Why: IdleWhy(2 * time.Hour)}))
	st, ok := h.LastStop()
	require.True(t, ok)
	require.Equal(t, "qwen3-4b", st.Band)
	require.Equal(t, "no turn for 2h0m0s - the owner's idle timeout (roger model idle off keeps it serving)", st.Why)
	require.Contains(t, st.Line(), "qwen3-4b stopped 2026-10-01 14:02 UTC: no turn for 2h0m0s")
}
