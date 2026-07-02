package main

// share_voice_tts_detect_test.go specs the voice identity on the tts-DETECT headless
// branch: `roger share --model voice` against an AUTO-DETECTED bare voice server
// (kokoro-fastapi and most Whisper servers expose no /v1/models, so the detector
// synthesizes one offer under the id "voice" for tts / "transcribe" for stt).
//
// LIVE-VERIFIED (2026-07-02): a headless tts share went on air with a NAMELESS offer
// and the broker 400'd it ("voice name is empty after normalization"). The identity
// comes ONLY from the saved share_voices profile (name/language/sample_url have no
// flags), so the whole contract hangs on the LOOKUP KEY: applyShareVoice reads
// cfg.Voices[mdl] at the single cmdShare config-build (main.go applyShareVoice call),
// where mdl is the MODEL ID THE OFFER IS SHARED UNDER - the --model value (or the
// saved/first-detected id), NOT the server family name. These scenarios pin, end to
// end:
//   - the REGISTERED offer (the real agent.Start register body, not just the built
//     agent.Config) carries name + language + sample_url + default voice/speed from
//     a profile keyed by the shared model id ("voice" for a bare tts server);
//   - a `--model` RENAME resolves the profile under the RENAMED id and keeps tts;
//   - the two natural wrong keys from the live incident ("kokoro" - the server
//     family - and a rename id that is not the one being shared) do NOT apply, which
//     is what leaves an offer nameless and draws the broker 400;
//   - sibling profiles never bleed across model ids.
//
// Real config files in an isolated dir, the real detection stub seam, and for the
// registration scenario the REAL agent.Start against an httptest broker; no mocks.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/agent"
	"github.com/rogerai-fyi/roger/internal/detect"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// operatorProfile is the live box's share_voices entry (plus a default voice/speed so
// the full identity is pinned).
var operatorProfile = ShareVoice{
	Name: "roger-operator", Language: "en-US",
	SampleURL: "https://rogerai.fyi/assets/voice-samples/roger-operator-1950s.mp3",
	Voice:     "af_heart:0.5+af_aoede:0.5", Speed: 1.25,
}

// bareKokoroFound is what detection returns for a bare kokoro-fastapi: no /v1/models,
// one synthesized tts offer under the default id "voice".
func bareKokoroFound() detect.Found {
	return detect.Found{
		Name: "kokoro", BaseURL: "http://127.0.0.1:8095/v1",
		Chat:   "http://127.0.0.1:8095/v1/chat/completions",
		Models: []string{"voice"}, Modality: map[string]string{"voice": protocol.ModalityTTS},
	}
}

// captureRegisterBroker is an httptest broker that records every /nodes/register body
// and keeps the agent loops calm: heartbeats ACK (200) so waitOnAir returns fast, and
// /agent/poll long-polls until the test ends (204), matching the broker contract, so
// the pollers neither spin nor serve phantom jobs.
func captureRegisterBroker(t *testing.T) (*httptest.Server, func() []protocol.NodeRegistration) {
	t.Helper()
	var (
		mu   sync.Mutex
		regs []protocol.NodeRegistration
	)
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/nodes/register":
			var reg protocol.NodeRegistration
			if err := json.NewDecoder(r.Body).Decode(&reg); err != nil {
				t.Errorf("register body did not decode: %v", err)
			}
			mu.Lock()
			regs = append(regs, reg)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{}`))
		case "/agent/poll":
			select {
			case <-done:
			case <-time.After(25 * time.Second):
			}
			w.WriteHeader(http.StatusNoContent)
		default: // heartbeat + any market/status probe: 200 {}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(done) }) // runs BEFORE srv.Close: drain the long-polls
	return srv, func() []protocol.NodeRegistration {
		mu.Lock()
		defer mu.Unlock()
		out := make([]protocol.NodeRegistration, len(regs))
		copy(out, regs)
		return out
	}
}

// realAgentStart routes cmdShare's go-live through the REAL agent.Start (register,
// heartbeat, pollers) and stops the session on cleanup so no loops outlive the test.
func realAgentStart(t *testing.T) {
	t.Helper()
	var (
		mu   sync.Mutex
		sess *agent.Session
	)
	origStart, origBlock := agentStart, shareBlock
	agentStart = func(c agent.Config) (*agent.Session, error) {
		s, err := agent.Start(c)
		if s != nil {
			mu.Lock()
			sess = s
			mu.Unlock()
		}
		return s, err
	}
	shareBlock = func() {}
	t.Cleanup(func() {
		agentStart, shareBlock = origStart, origBlock
		mu.Lock()
		defer mu.Unlock()
		if sess != nil {
			sess.Stop()
		}
	})
}

// The incident invocation, end to end: `roger share --model voice` on the tts-detect
// branch with the profile keyed "voice" must REGISTER an offer that carries the full
// identity - name, language, sample_url, default voice + speed - as a station-bound
// tts offer (the shape the broker's voice screen validates).
func TestShareTTSDetectRegistersVoiceIdentity(t *testing.T) {
	useTempConfig(t)
	stubDetectFull(t, []detect.Found{bareKokoroFound()}, nil)
	srv, registrations := captureRegisterBroker(t)
	realAgentStart(t)

	cfg := config{Broker: srv.URL, User: "op",
		Voices: map[string]ShareVoice{"voice": operatorProfile}}
	if err := runShare(t, cfg, []string{"--model", "voice"}); err != nil {
		t.Fatalf("cmdShare(--model voice) = %v, want nil", err)
	}

	regs := registrations()
	if len(regs) == 0 {
		t.Fatal("no /nodes/register arrived at the broker")
	}
	reg := regs[0]
	if len(reg.Offers) != 1 {
		t.Fatalf("registered %d offers, want 1: %+v", len(reg.Offers), reg.Offers)
	}
	o := reg.Offers[0]
	if o.Model != "voice" || o.Modality != protocol.ModalityTTS {
		t.Errorf("registered offer is %q/%q, want voice/tts", o.Model, o.Modality)
	}
	if o.Name != operatorProfile.Name {
		t.Errorf("registered offer name = %q, want %q (a nameless tts offer draws the broker 400 %q)",
			o.Name, operatorProfile.Name, "voice name is empty after normalization")
	}
	if o.Language != operatorProfile.Language {
		t.Errorf("registered offer language = %q, want %q", o.Language, operatorProfile.Language)
	}
	if o.SampleURL != operatorProfile.SampleURL {
		t.Errorf("registered offer sample_url = %q, want %q", o.SampleURL, operatorProfile.SampleURL)
	}
	if o.Voice != operatorProfile.Voice || o.Speed != operatorProfile.Speed {
		t.Errorf("registered offer default voice/speed = %q/%v, want %q/%v",
			o.Voice, o.Speed, operatorProfile.Voice, operatorProfile.Speed)
	}
	if reg.Station == "" {
		t.Error("registered station is empty - the offer would skip the broker's public-voice screen")
	}
}

// A `--model` rename of the synthesized offer (the founder's real workflow) resolves
// the profile under the RENAMED id - the id the offer is shared under - and the
// detected tts modality still rides along (soleModality fallback).
func TestShareTTSRenameResolvesProfileUnderRenamedID(t *testing.T) {
	useTempConfig(t)
	got := captureShareConfig(t)
	stubDetectFull(t, []detect.Found{bareKokoroFound()}, nil)

	cfg := config{Broker: "https://b", User: "op",
		Voices: map[string]ShareVoice{"roger-operator-voice": operatorProfile}}
	if err := runShare(t, cfg, []string{"--model", "roger-operator-voice"}); err != nil {
		t.Fatalf("cmdShare(rename) = %v, want nil", err)
	}
	if got.Model != "roger-operator-voice" || got.Modality != protocol.ModalityTTS {
		t.Errorf("cfgRun = %q/%q, want roger-operator-voice/tts", got.Model, got.Modality)
	}
	if got.Name != operatorProfile.Name || got.Language != operatorProfile.Language || got.SampleURL != operatorProfile.SampleURL {
		t.Errorf("renamed share identity = %q/%q/%q, want the roger-operator-voice profile applied",
			got.Name, got.Language, got.SampleURL)
	}
}

// The lookup key IS the shared model id. The live incident's two natural wrong keys
// (the server family "kokoro"; a rename id while sharing the synthesized "voice")
// must NOT apply - that miss is what registers a nameless tts offer the broker 400s -
// and a sibling profile must never bleed onto another model's offer. The stt twin
// ("transcribe", the other synthesized id the ShareVoice godoc documents) resolves
// the same way.
func TestShareTTSProfileKeyIsTheSharedModelID(t *testing.T) {
	bareWhisper := detect.Found{
		Name: "whisper", BaseURL: "http://127.0.0.1:9000/v1",
		Chat:   "http://127.0.0.1:9000/v1/chat/completions",
		Models: []string{"transcribe"}, Modality: map[string]string{"transcribe": protocol.ModalitySTT},
	}
	cases := []struct {
		name         string
		found        detect.Found
		voices       map[string]ShareVoice
		model        string
		wantModality string
		wantName     string
	}{
		{
			name:         "key voice matches the synthesized bare-tts id",
			found:        bareKokoroFound(),
			voices:       map[string]ShareVoice{"voice": operatorProfile},
			model:        "voice",
			wantModality: protocol.ModalityTTS,
			wantName:     "roger-operator",
		},
		{
			name:         "key kokoro (the server family) does not apply to model id voice",
			found:        bareKokoroFound(),
			voices:       map[string]ShareVoice{"kokoro": operatorProfile},
			model:        "voice",
			wantModality: protocol.ModalityTTS,
			wantName:     "",
		},
		{
			name:         "a rename key does not apply when sharing the synthesized id",
			found:        bareKokoroFound(),
			voices:       map[string]ShareVoice{"roger-operator-voice": operatorProfile},
			model:        "voice",
			wantModality: protocol.ModalityTTS,
			wantName:     "",
		},
		{
			name:  "sibling profiles do not bleed across model ids",
			found: bareKokoroFound(),
			voices: map[string]ShareVoice{
				"voice":       operatorProfile,
				"other-model": {Name: "Wrong DJ", SampleURL: "https://cdn.example/wrong.mp3"},
			},
			model:        "voice",
			wantModality: protocol.ModalityTTS,
			wantName:     "roger-operator",
		},
		{
			name:         "key transcribe matches the synthesized bare-stt id",
			found:        bareWhisper,
			voices:       map[string]ShareVoice{"transcribe": operatorProfile},
			model:        "transcribe",
			wantModality: protocol.ModalitySTT,
			wantName:     "roger-operator",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useTempConfig(t)
			got := captureShareConfig(t)
			stubDetectFull(t, []detect.Found{tc.found}, nil)

			cfg := config{Broker: "https://b", User: "op", Voices: tc.voices}
			if err := runShare(t, cfg, []string{"--model", tc.model}); err != nil {
				t.Fatalf("cmdShare(--model %s) = %v, want nil", tc.model, err)
			}
			if got.Modality != tc.wantModality {
				t.Errorf("cfgRun.Modality = %q, want %q", got.Modality, tc.wantModality)
			}
			if got.Name != tc.wantName {
				t.Errorf("cfgRun.Name = %q, want %q (lookup key must be the shared model id)", got.Name, tc.wantName)
			}
			if tc.wantName == "" && (got.SampleURL != "" || got.Language != "") {
				t.Errorf("a missed key must leave the identity empty, got lang %q sample %q", got.Language, got.SampleURL)
			}
			if tc.wantName != "" && got.SampleURL != operatorProfile.SampleURL {
				t.Errorf("cfgRun.SampleURL = %q, want %q", got.SampleURL, operatorProfile.SampleURL)
			}
		})
	}
}
