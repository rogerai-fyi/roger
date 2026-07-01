package agent

// Proves the node-side voice bridge: a job tagged with an audio Path is served against the SAME
// local server at that path (derived from the chat upstream's base), while a normal chat job is
// unchanged. This is the last mile that makes a shared voice actually serve.

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

func TestServeRoutesByJobPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("~audio bytes~"))
	}))
	defer srv.Close()

	_, priv, _ := ed25519.GenerateKey(nil)
	cfg := Config{NodeID: "n1", Model: "roger-operator-voice", Upstream: srv.URL + "/v1/chat/completions"}
	offer := protocol.ModelOffer{Model: "roger-operator-voice", Modality: protocol.ModalityTTS}

	// A voice job (broker-tagged Path) must hit the LOCAL /v1/audio/speech, not the chat endpoint.
	voice := protocol.Job{ID: "j1", Body: []byte(`{"input":"hello"}`), Path: "/v1/audio/speech"}
	res := serve(cfg, offer, priv, srv.Client(), voice)
	if gotPath != "/v1/audio/speech" {
		t.Errorf("voice job served to %q, want /v1/audio/speech", gotPath)
	}
	if res.Status != http.StatusOK || string(res.Body) != "~audio bytes~" {
		t.Errorf("voice result = %d %q, want 200 audio bytes", res.Status, res.Body)
	}

	// A chat job (no Path) is untouched — still the chat endpoint.
	chat := protocol.Job{ID: "j2", Body: []byte(`{"messages":[]}`)}
	_ = serve(cfg, offer, priv, srv.Client(), chat)
	if gotPath != "/v1/chat/completions" {
		t.Errorf("chat job served to %q, want /v1/chat/completions", gotPath)
	}
}
