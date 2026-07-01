package detect

// voice_bare_test.go: table tests for BARE voice servers — a local TTS/STT server that serves
// its audio endpoint but 404s GET /v1/models (kokoro-fastapi on :8095, most Whisper servers).
// The /v1/models gate used to drop these silently; detection must fall back to a capability probe
// and synthesize ONE offer per bare voice server. REAL httptest stubs, no mocks (per TDD-WORKFLOW).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// bareVoiceServer builds a stub that 404s GET /v1/models (like kokoro-fastapi) but serves the
// given audio endpoints (each a present route that rejects an empty body with 400). GET /health
// answers 200, mirroring the real server, though detection does not rely on it.
func bareVoiceServer(audioPaths ...string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound) // no model list to enumerate
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	for _, p := range audioPaths {
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadRequest) })
	}
	return httptest.NewServer(mux)
}

// detectOne runs DetectFull against exactly one stub base (env + port sources silenced) and
// returns the found servers.
func detectOne(t *testing.T, base string) []Found {
	t.Helper()
	defer quietSources(t)()
	old := probes
	probes = []struct{ name, base string }{{"test", base}}
	defer func() { probes = old }()
	found, _ := DetectFull()
	return found
}

// TestBareVoiceServerDetected exercises the core fix as a table: a bare TTS server (speech
// endpoint, no /v1/models) is one tts offer; a bare STT server is one stt offer; a bare server
// with NO audio endpoint is dropped (nothing to offer). One offer per server — there is no model
// list, so exactly one synthesized id carries the modality.
func TestBareVoiceServerDetected(t *testing.T) {
	cases := []struct {
		name         string
		audioPaths   []string
		wantOffers   int
		wantModality string // "" when wantOffers == 0
	}{
		{"bare tts (speech only)", []string{"/v1/audio/speech"}, 1, protocol.ModalityTTS},
		{"bare stt (transcriptions only)", []string{"/v1/audio/transcriptions"}, 1, protocol.ModalitySTT},
		{"bare, no audio at all -> dropped", nil, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := bareVoiceServer(tc.audioPaths...)
			defer srv.Close()

			found := detectOne(t, srv.URL+"/v1")

			offers := 0
			for _, f := range found {
				offers += len(f.Models)
			}
			if offers != tc.wantOffers {
				t.Fatalf("synthesized %d offers, want %d: %+v", offers, tc.wantOffers, found)
			}
			if tc.wantOffers == 0 {
				return
			}
			// Exactly one server, one model, and its modality is carried through Found.Modality.
			f := found[0]
			if len(f.Models) != 1 {
				t.Fatalf("want exactly one synthesized model, got %v", f.Models)
			}
			m := f.Models[0]
			if f.Modality[m] != tc.wantModality {
				t.Errorf("modality[%q] = %q, want %q", m, f.Modality[m], tc.wantModality)
			}
			// The BaseURL / Chat are wired like every other detect path so the offer-build
			// path is unchanged (agent.Config.Upstream = pick.Chat).
			if f.BaseURL != srv.URL+"/v1" {
				t.Errorf("BaseURL = %q, want %q", f.BaseURL, srv.URL+"/v1")
			}
			if f.Chat != srv.URL+"/v1/chat/completions" {
				t.Errorf("Chat = %q, want %q", f.Chat, srv.URL+"/v1/chat/completions")
			}
			// The synthesized offer normalizes to the modality's canonical unit downstream.
			o := protocol.ModelOffer{Model: m, Modality: f.Modality[m]}
			o.Normalize()
			wantUnit := map[string]string{protocol.ModalityTTS: protocol.UnitChar, protocol.ModalitySTT: protocol.UnitByte}[tc.wantModality]
			if o.Unit != wantUnit {
				t.Errorf("normalized unit = %q, want %q", o.Unit, wantUnit)
			}
		})
	}
}

// TestChatServerUnaffectedByVoiceProbe: a NORMAL server that serves /v1/models is detected as
// chat exactly as before — the voice fallback must not run for it and must not add or change any
// offer. Guards against the fix leaking into the chat path.
func TestChatServerUnaffectedByVoiceProbe(t *testing.T) {
	// A chat server that ALSO serves a LIVE /v1/audio/speech (some gateways front both) must still
	// be classified by /v1/models via classifyModalities, NOT reach the voice fallback (which only
	// fires on probeMiss) or get collapsed to one voice offer.
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "llama3.2"}}})
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadRequest) })
	mux.HandleFunc("/v1/audio/speech", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadRequest) }) // live, but /v1/models wins
	srv := httptest.NewServer(mux)
	defer srv.Close()

	found := detectOne(t, srv.URL+"/v1")
	if len(found) != 1 || len(found[0].Models) != 1 || found[0].Models[0] != "llama3.2" {
		t.Fatalf("chat server should yield its one real model unchanged, got %+v", found)
	}
	if found[0].Modality["llama3.2"] != protocol.ModalityChat {
		t.Errorf("modality = %q, want chat", found[0].Modality["llama3.2"])
	}
}

// TestBareVoiceServerBothEndpoints: a bare server that 404s /v1/models but serves BOTH speech and
// transcription still synthesizes ONE offer (founder's one-offer-per-server rule). Speech wins the
// single label (tts) — a mixed bare server is an edge case; we pick a deterministic modality
// rather than emitting two phantom ids for a server that never enumerated a model.
func TestBareVoiceServerBothEndpoints(t *testing.T) {
	srv := bareVoiceServer("/v1/audio/speech", "/v1/audio/transcriptions")
	defer srv.Close()

	found := detectOne(t, srv.URL+"/v1")
	offers := 0
	for _, f := range found {
		offers += len(f.Models)
	}
	if offers != 1 {
		t.Fatalf("one offer per bare voice server, got %d: %+v", offers, found)
	}
	m := found[0].Models[0]
	if found[0].Modality[m] != protocol.ModalityTTS {
		t.Errorf("a speech-capable bare server should label tts, got %q", found[0].Modality[m])
	}
}

// stubAudioServer builds a server that 404s GET /v1/models (like a bare voice server) but answers
// its audio routes with a FIXED status code — used to prove that a STUBBED/unimplemented route
// (Hermes workers return 501; some return 405) is not mistaken for a live voice endpoint.
func stubAudioServer(code int, audioPaths ...string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	for _, p := range audioPaths {
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) })
	}
	return httptest.NewServer(mux)
}

// TestBareVoiceServerRejectsStubbedRoutes is the exact false-positive regression: a Hermes worker
// (and friends) 404s /v1/models AND STUBS the OpenAI audio routes — /v1/audio/speech returns 501
// Not Implemented (or 405 Method Not Allowed). A stubbed route is NOT a live voice endpoint, so the
// server must synthesize ZERO offers, not be tagged voice/tts. The real bare-voice codes (200/400,
// and 401 for a key-protected server) still count as live.
func TestBareVoiceServerRejectsStubbedRoutes(t *testing.T) {
	cases := []struct {
		name       string
		code       int
		wantOffers int
	}{
		{"501 not implemented -> not voice", http.StatusNotImplemented, 0},      // the live-box bug (:8779, :9090, ...)
		{"405 method not allowed -> not voice", http.StatusMethodNotAllowed, 0}, // a route that rejects POST
		{"502 bad gateway -> not voice", http.StatusBadGateway, 0},              // any 5xx is not a live endpoint
		{"400 bad request -> real bare voice", http.StatusBadRequest, 1},        // real Kokoro
		{"200 ok -> real bare voice", http.StatusOK, 1},                         // a permissive real server
		{"401 unauthorized -> key-protected real voice", http.StatusUnauthorized, 1},
		{"422 unprocessable -> real bare voice", http.StatusUnprocessableEntity, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := stubAudioServer(tc.code, "/v1/audio/speech")
			defer srv.Close()

			found := detectOne(t, srv.URL+"/v1")
			offers := 0
			for _, f := range found {
				offers += len(f.Models)
			}
			if offers != tc.wantOffers {
				t.Fatalf("audio route %d: synthesized %d offers, want %d: %+v", tc.code, offers, tc.wantOffers, found)
			}
			if tc.wantOffers == 1 && found[0].Modality[found[0].Models[0]] != protocol.ModalityTTS {
				t.Errorf("audio route %d: modality = %q, want tts", tc.code, found[0].Modality[found[0].Models[0]])
			}
		})
	}
}

// TestAudioRouteLive covers the presence rule directly: a route is live only if implemented and
// handling POST — accept 2xx/4xx-except-404/405, reject 404/405/5xx and a transport error (0).
func TestAudioRouteLive(t *testing.T) {
	cases := []struct {
		code int
		live bool
	}{
		{http.StatusOK, true},                   // 200
		{http.StatusBadRequest, true},           // 400 — empty body rejected by a real route
		{http.StatusUnauthorized, true},         // 401 — key-protected, route IS present
		{http.StatusUnsupportedMediaType, true}, // 415
		{http.StatusUnprocessableEntity, true},  // 422
		{http.StatusNotFound, false},            // 404 — route absent
		{http.StatusMethodNotAllowed, false},    // 405 — route exists but not for POST
		{http.StatusNotImplemented, false},      // 501 — stubbed (the Hermes bug)
		{http.StatusBadGateway, false},          // 502 — any 5xx
		{http.StatusServiceUnavailable, false},  // 503
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.code) }))
		if got := audioRouteLive(srv.URL, ""); got != tc.live {
			t.Errorf("audioRouteLive(code=%d) = %v, want %v", tc.code, got, tc.live)
		}
		srv.Close()
	}
	// A dead port => transport error (code 0) => not live.
	if audioRouteLive("http://127.0.0.1:1/v1/audio/speech", "") {
		t.Error("dead endpoint reported as a live audio route")
	}
}

// TestBareKeyedVoiceServer: a BARE voice server that 404s /v1/models AND requires a Bearer key on
// its audio endpoint is STILL detected — endpointExists counts the 401 (from the unauthenticated
// capability probe) as "route present", so a keyed Kokoro is caught without sending any key. This
// is the mechanism that lets the fallback stay key-free (never sprays a key at a blind port hit).
func TestBareKeyedVoiceServer(t *testing.T) {
	defer quietSources(t)()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	mux.HandleFunc("/v1/audio/speech", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-voice" {
			w.WriteHeader(http.StatusUnauthorized) // 401 (non-404) => endpointExists sees the route
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	old := probes
	probes = []struct{ name, base string }{{"kokoro", srv.URL + "/v1"}}
	defer func() { probes = old }()

	found, _ := DetectFull()
	if len(found) != 1 || len(found[0].Models) != 1 {
		t.Fatalf("keyed bare voice server should be detected as one offer, got %+v", found)
	}
	m := found[0].Models[0]
	if found[0].Modality[m] != protocol.ModalityTTS {
		t.Errorf("modality = %q, want tts", found[0].Modality[m])
	}
}

// TestProbeVoice covers the capability probe helper directly: a speech route => tts, a
// transcription route => stt, neither => "".
func TestProbeVoice(t *testing.T) {
	tts := bareVoiceServer("/v1/audio/speech")
	defer tts.Close()
	if got := probeVoice(tts.URL + "/v1"); got != protocol.ModalityTTS {
		t.Errorf("probeVoice(tts) = %q, want tts", got)
	}
	stt := bareVoiceServer("/v1/audio/transcriptions")
	defer stt.Close()
	if got := probeVoice(stt.URL + "/v1"); got != protocol.ModalitySTT {
		t.Errorf("probeVoice(stt) = %q, want stt", got)
	}
	none := bareVoiceServer()
	defer none.Close()
	if got := probeVoice(none.URL + "/v1"); got != "" {
		t.Errorf("probeVoice(none) = %q, want empty", got)
	}
}
