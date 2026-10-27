package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/localmodel"
)

// features/lan/discovery_and_dial.feature, THE THREE SOURCES: 1. THIS MACHINE is `roger model
// serve`. The dial's self rung read the Edge household and the share records only, so a model
// roger served itself was "nothing on this LAN" to `roger --lan search` and `roger --lan use`
// while `roger model status` showed it live. Found 2026-10-24 serving a hand-placed Wave Nano
// file; the BDD steps had stubbed the self rung, so no test had read a real record.
func TestLANSelfBandsListWhatRogerModelServeHasOnTheAir(t *testing.T) {
	useTempConfig(t)
	h := modelHome()
	require.NoError(t, os.MkdirAll(h.Dir, 0o700))
	rec, _ := json.Marshal(localmodel.Serving{Supervisor: os.Getpid(), Port: 43165, BaseURL: "http://127.0.0.1:43165/v1", Key: "k",
		Band: "wave-nano-1p45b", ID: "wave-nano-1p45b-Q4_K_M", File: "wave-nano-1p45b-Q4_K_M.gguf", Quant: "Q4_K_M", Started: time.Now()})
	require.NoError(t, os.WriteFile(filepath.Join(h.Dir, "serving-43165.json"), rec, 0o600))

	require.Contains(t, lanSelfBands(), "wave-nano-1p45b", "the dial's self rung lists what roger model serve has on the air")
	u, k := lanSelfServe("wave-nano-1p45b")
	require.Equal(t, "http://127.0.0.1:43165/v1/chat/completions", u)
	require.Equal(t, "k", k)
}

// local_models.feature: a file whose header cannot be judged is served "on your word". The
// detached serve handed the child "--ctx 0" in that case, which the child refuses, so the
// background serve never came on the air and the caller waited out the whole detach window.
// Found 2026-10-24 serving a LoRA adapter file by name.
func TestAnUnjudgeableFileServedOnYourWordGetsTheDefaultContext(t *testing.T) {
	useTempConfig(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "opaque.gguf")
	require.NoError(t, os.WriteFile(p, []byte("not a gguf header"), 0o600))
	m := localmodel.Model{ID: "opaque", Path: p, File: "opaque.gguf"}
	ctxLen, fit, err := modelPlanFit(t.Context(), m, localmodel.Budget{Bytes: 16 << 30}, 0, "")
	require.NoError(t, err)
	require.Contains(t, fit.line, "serving on your word")
	require.Equal(t, localmodel.DefaultCtx, ctxLen, "no context asked for means the default, never zero")
}
